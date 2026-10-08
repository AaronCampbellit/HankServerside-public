package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type FileSearchDirectory struct {
	SourceID string
	Path     string
	Depth    int
}

type FileSearchMatch struct {
	Item            protocol.FileItem
	AgentID         string
	SourceName      string
	AllowedPrefixes []string
	BlockedPrefixes []string
}

type FileSearchStatus struct {
	SourceID  string    `json:"source_id"`
	AgentID   string    `json:"agent_id"`
	Name      string    `json:"name"`
	Indexed   int64     `json:"indexed"`
	Pending   int64     `json:"pending"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     bool      `json:"error"`
}

func (s *Store) SyncFileSearchSources(ctx context.Context, homeID, agentID, defaultID string, sources []protocol.FileSourceInfo) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		if source.ID == "" || !source.Enabled || seen[source.ID] {
			continue
		}
		seen[source.ID] = true
		allowed, _ := json.Marshal(source.AllowedPrefixes)
		blocked, _ := json.Marshal(source.BlockedPrefixes)
		if len(source.AllowedPrefixes) == 0 {
			allowed = []byte("[]")
		}
		if len(source.BlockedPrefixes) == 0 {
			blocked = []byte("[]")
		}
		var previousAllowed, previousBlocked []byte
		var previousRevision string
		lookupErr := tx.QueryRowContext(ctx, `SELECT allowed_prefixes, blocked_prefixes, revision FROM file_search_sources
			WHERE home_id=? AND agent_id=? AND source_id=?`, homeID, agentID, source.ID).Scan(&previousAllowed, &previousBlocked, &previousRevision)
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil {
			var oldAllowed, oldBlocked []string
			if err := json.Unmarshal(previousAllowed, &oldAllowed); err != nil {
				return err
			}
			if err := json.Unmarshal(previousBlocked, &oldBlocked); err != nil {
				return err
			}
			if previousRevision != source.Revision || !slices.Equal(oldAllowed, source.AllowedPrefixes) || !slices.Equal(oldBlocked, source.BlockedPrefixes) {
				if _, err := tx.ExecContext(ctx, `DELETE FROM file_search_sources WHERE home_id=? AND agent_id=? AND source_id=?`, homeID, agentID, source.ID); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO file_search_sources
			(home_id, agent_id, source_id, name, revision, is_default, readable, allowed_prefixes, blocked_prefixes, discovered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, now())
			ON CONFLICT (home_id, agent_id, source_id) DO UPDATE SET
			name=excluded.name, revision=excluded.revision, is_default=excluded.is_default, readable=excluded.readable,
			allowed_prefixes=excluded.allowed_prefixes, blocked_prefixes=excluded.blocked_prefixes,
			discovered_at=excluded.discovered_at`, homeID, agentID, source.ID, source.Name, source.Revision,
			source.ID == defaultID, source.Readable, string(allowed), string(blocked))
		if err != nil {
			return err
		}
		roots := source.AllowedPrefixes
		if len(roots) == 0 {
			roots = []string{""}
		}
		for _, raw := range roots {
			root := strings.TrimPrefix(path.Clean("/"+raw), "/")
			depth := 0
			if root != "" {
				depth = strings.Count(root, "/") + 1
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO file_search_directories
				(home_id, agent_id, source_id, path, depth) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, homeID, agentID, source.ID, root, depth)
			if err != nil {
				return err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_id FROM file_search_sources WHERE home_id=? AND agent_id=?`, homeID, agentID)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if !seen[id] {
			removed = append(removed, id)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range removed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM file_search_sources WHERE home_id=? AND agent_id=? AND source_id=?`, homeID, agentID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) NextFileSearchDirectory(ctx context.Context, homeID, agentID string) (FileSearchDirectory, error) {
	var dir FileSearchDirectory
	root := s.queryRow(ctx, `SELECT d.source_id, d.path, d.depth FROM file_search_directories d
		JOIN file_search_sources s USING (home_id, agent_id, source_id)
		WHERE d.home_id=? AND d.agent_id=? AND d.path='' AND s.readable AND d.next_scan_at <= now()
		LIMIT 1`, homeID, agentID).Scan(&dir.SourceID, &dir.Path, &dir.Depth)
	if root == nil {
		return dir, nil
	}
	if !errors.Is(root, sql.ErrNoRows) {
		return dir, root
	}
	err := s.queryRow(ctx, `SELECT d.source_id, d.path, d.depth FROM file_search_directories d
		JOIN file_search_sources s USING (home_id, agent_id, source_id)
		WHERE d.home_id=? AND d.agent_id=? AND s.readable AND d.scanned_at IS NULL
			AND d.next_scan_at <= now()
		ORDER BY d.depth, d.path LIMIT 1`, homeID, agentID).Scan(&dir.SourceID, &dir.Path, &dir.Depth)
	if err == nil {
		return dir, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dir, err
	}
	err = s.queryRow(ctx, `SELECT d.source_id, d.path, d.depth FROM file_search_directories d
		JOIN file_search_sources s USING (home_id, agent_id, source_id)
		WHERE d.home_id=? AND d.agent_id=? AND s.readable AND d.next_scan_at <= now()
		ORDER BY d.next_scan_at, d.depth LIMIT 1`, homeID, agentID).
		Scan(&dir.SourceID, &dir.Path, &dir.Depth)
	return dir, err
}

func (s *Store) DeferFileSearchDirectory(ctx context.Context, homeID, agentID string, dir FileSearchDirectory) error {
	_, err := s.exec(ctx, `UPDATE file_search_directories SET next_scan_at=now()+interval '5 minutes'
		, last_error_at=now()
		WHERE home_id=? AND agent_id=? AND source_id=? AND path=?`, homeID, agentID, dir.SourceID, dir.Path)
	return err
}

func (s *Store) MarkFileSearchDirectoryDue(ctx context.Context, homeID, agentID, sourceID, directory string) error {
	directory = strings.TrimPrefix(path.Clean("/"+directory), "/")
	depth := 0
	if directory != "" {
		depth = strings.Count(directory, "/") + 1
	}
	_, err := s.exec(ctx, `INSERT INTO file_search_directories (home_id, agent_id, source_id, path, depth, next_scan_at)
		SELECT home_id, agent_id, source_id, ?, ?, now() FROM file_search_sources
		WHERE home_id=? AND agent_id=? AND ((?='' AND is_default) OR source_id=?)
		ON CONFLICT (home_id, agent_id, source_id, path) DO UPDATE SET next_scan_at=now()`,
		directory, depth, homeID, agentID, sourceID, sourceID)
	return err
}

// SaveFileSearchDirectory publishes a complete directory listing atomically.
// Failed listings never remove catalog entries.
func (s *Store) SaveFileSearchDirectory(ctx context.Context, homeID, agentID string, dir FileSearchDirectory, items []protocol.FileItem) error {
	for _, item := range items {
		if item.SourceID != dir.SourceID || item.Path == "" || path.Clean(strings.TrimPrefix(item.Path, "/")) != strings.TrimPrefix(item.Path, "/") ||
			fileSearchParent(item.Path) != dir.Path || item.Name != path.Base(item.Path) {
			return fmt.Errorf("invalid file search listing")
		}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	marker := time.Now().UTC()
	if dir.Path != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO file_search_items
			(home_id, agent_id, source_id, path, parent_path, name, is_directory, indexed_at)
			VALUES (?, ?, ?, ?, ?, ?, TRUE, ?) ON CONFLICT DO NOTHING`,
			homeID, agentID, dir.SourceID, dir.Path, fileSearchParent(dir.Path), path.Base(dir.Path), marker)
		if err != nil {
			return err
		}
	}
	for _, item := range items {
		_, err = tx.ExecContext(ctx, `INSERT INTO file_search_items
			(home_id, agent_id, source_id, path, parent_path, name, is_directory, size_bytes, modified_at, indexed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (home_id, agent_id, source_id, path) DO UPDATE SET
			parent_path=excluded.parent_path, name=excluded.name, is_directory=excluded.is_directory,
			size_bytes=excluded.size_bytes, modified_at=excluded.modified_at, indexed_at=excluded.indexed_at`,
			homeID, agentID, dir.SourceID, item.Path, dir.Path, item.Name, item.IsDirectory, item.Size, nullableFileTime(item.ModifiedAt), marker)
		if err != nil {
			return err
		}
		if item.IsDirectory && !strings.EqualFold(item.Name, ".git") {
			_, err = tx.ExecContext(ctx, `INSERT INTO file_search_directories
				(home_id, agent_id, source_id, path, depth) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, homeID, agentID, dir.SourceID, item.Path, dir.Depth+1)
			if err != nil {
				return err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `DELETE FROM file_search_items
		WHERE home_id=? AND agent_id=? AND source_id=? AND parent_path=? AND indexed_at < ?
		RETURNING path, is_directory`, homeID, agentID, dir.SourceID, dir.Path, marker)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var p string
		var isDir bool
		if err = rows.Scan(&p, &isDir); err != nil {
			break
		}
		if isDir {
			removed = append(removed, p)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range removed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM file_search_items WHERE home_id=? AND agent_id=? AND source_id=? AND left(path, length(?)+1)=? || '/'`, homeID, agentID, dir.SourceID, p, p); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM file_search_directories WHERE home_id=? AND agent_id=? AND source_id=? AND (path=? OR left(path, length(?)+1)=? || '/')`, homeID, agentID, dir.SourceID, p, p, p); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE file_search_directories SET scanned_at=?, next_scan_at=?, last_error_at=NULL
		WHERE home_id=? AND agent_id=? AND source_id=? AND path=?`, marker, marker.Add(30*time.Minute), homeID, agentID, dir.SourceID, dir.Path)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveFileSearchSingleItem(ctx context.Context, homeID, agentID string, dir FileSearchDirectory, item protocol.FileItem) error {
	if item.SourceID != dir.SourceID || item.Path != dir.Path || item.Name != path.Base(item.Path) || item.IsDirectory {
		return errors.New("invalid file search item")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO file_search_items
		(home_id, agent_id, source_id, path, parent_path, name, is_directory, size_bytes, modified_at)
		VALUES (?, ?, ?, ?, ?, ?, FALSE, ?, ?)
		ON CONFLICT (home_id, agent_id, source_id, path) DO UPDATE SET
		name=excluded.name, is_directory=FALSE, size_bytes=excluded.size_bytes,
		modified_at=excluded.modified_at, indexed_at=now()`,
		homeID, agentID, dir.SourceID, item.Path, fileSearchParent(item.Path), item.Name, item.Size, nullableFileTime(item.ModifiedAt))
	if err != nil {
		return err
	}
	marker := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE file_search_directories SET scanned_at=?, next_scan_at=?, last_error_at=NULL WHERE home_id=? AND agent_id=? AND source_id=? AND path=?`,
		marker, marker.Add(30*time.Minute), homeID, agentID, dir.SourceID, dir.Path)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func nullableFileTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func fileSearchParent(p string) string {
	parent := path.Dir(p)
	if parent == "." || parent == "/" {
		return ""
	}
	return strings.TrimPrefix(parent, "/")
}

func (s *Store) SearchFileCatalog(ctx context.Context, homeID string, agentIDs []string, sourceID, query string, defaultOnly bool, limit int) ([]FileSearchMatch, error) {
	if len(agentIDs) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	var where strings.Builder
	args := []any{homeID, agentIDs, sourceID, sourceID, defaultOnly}
	for _, term := range strings.Fields(needle) {
		pattern := "%" + escape.Replace(term) + "%"
		if len([]rune(term)) < 3 && len(strings.Fields(needle)) == 1 {
			pattern = escape.Replace(term) + "%"
		}
		where.WriteString(` AND (lower(i.name) LIKE ? ESCAPE '\' OR lower(i.path) LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	full := escape.Replace(needle)
	args = append(args, needle, full+"%", "%"+full+"%", limit)
	rows, err := s.query(ctx, `SELECT i.source_id, i.path, i.name, i.is_directory, i.size_bytes, i.modified_at,
		i.agent_id, s.name, s.allowed_prefixes, s.blocked_prefixes
		FROM file_search_items i JOIN file_search_sources s USING (home_id, agent_id, source_id)
		WHERE i.home_id=? AND i.agent_id=ANY(?) AND s.readable
		AND (?='' OR i.source_id=?) AND (NOT ? OR s.is_default)`+where.String()+`
		ORDER BY CASE WHEN lower(i.name)=? THEN 4 WHEN lower(i.name) LIKE ? ESCAPE '\' THEN 3
			WHEN lower(i.name) LIKE ? ESCAPE '\' THEN 2 ELSE 1 END DESC,
			i.is_directory DESC, length(i.path), i.path LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []FileSearchMatch
	for rows.Next() {
		var match FileSearchMatch
		var modified sql.NullTime
		var allowed, blocked []byte
		if err := rows.Scan(&match.Item.SourceID, &match.Item.Path, &match.Item.Name, &match.Item.IsDirectory,
			&match.Item.Size, &modified, &match.AgentID, &match.SourceName, &allowed, &blocked); err != nil {
			return nil, err
		}
		if modified.Valid {
			match.Item.ModifiedAt = modified.Time
		}
		if err := json.Unmarshal(allowed, &match.AllowedPrefixes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(blocked, &match.BlockedPrefixes); err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	return matches, rows.Err()
}

func (s *Store) FileSearchStatuses(ctx context.Context, homeID string, agentIDs []string, sourceID string, defaultOnly bool) ([]FileSearchStatus, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	rows, err := s.query(ctx, `SELECT s.source_id, s.agent_id, s.name,
		COALESCE((SELECT max(d.scanned_at) FROM file_search_directories d WHERE d.home_id=s.home_id AND d.agent_id=s.agent_id AND d.source_id=s.source_id), s.discovered_at),
		(SELECT max(d.last_error_at) FROM file_search_directories d WHERE d.home_id=s.home_id AND d.agent_id=s.agent_id AND d.source_id=s.source_id),
		(SELECT count(*) FROM file_search_items i WHERE i.home_id=s.home_id AND i.agent_id=s.agent_id AND i.source_id=s.source_id),
		(SELECT count(*) FROM file_search_directories d WHERE d.home_id=s.home_id AND d.agent_id=s.agent_id AND d.source_id=s.source_id AND d.scanned_at IS NULL)
		FROM file_search_sources s WHERE s.home_id=? AND s.agent_id=ANY(?) AND s.readable
		AND (?='' OR s.source_id=?) AND (NOT ? OR s.is_default)
		ORDER BY s.agent_id, s.source_id`, homeID, agentIDs, sourceID, sourceID, defaultOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var statuses []FileSearchStatus
	for rows.Next() {
		var status FileSearchStatus
		var failure sql.NullTime
		if err := rows.Scan(&status.SourceID, &status.AgentID, &status.Name, &status.UpdatedAt, &failure, &status.Indexed, &status.Pending); err != nil {
			return nil, err
		}
		status.Error = failure.Valid
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func IsNoFileSearchWork(err error) bool { return errors.Is(err, sql.ErrNoRows) }
