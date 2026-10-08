package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const userNoteColumns = `id, note_id, owner_user_id, home_id, parent_id, mcp_excluded, pinned, sort_order, title, body_markdown, body_format, page_type, board_json, revision, checksum, crdt_state_json, collab_version, deleted_at, created_at, updated_at, updated_by`

const userNoteColumnsWithUN = `un.id, un.note_id, un.owner_user_id, un.home_id, un.parent_id, un.mcp_excluded, un.pinned, un.sort_order, un.title, un.body_markdown, un.body_format, un.page_type, un.board_json, un.revision, un.checksum, un.crdt_state_json, un.collab_version, un.deleted_at, un.created_at, un.updated_at, un.updated_by`

type UserNoteRevisionConflictError struct {
	Current domain.UserNote
}

func (e *UserNoteRevisionConflictError) Error() string { return "note revision conflict" }
func (e *UserNoteRevisionConflictError) Unwrap() error { return ErrConflict }

func txRecordExists(ctx context.Context, tx *dbTx, query string, args ...any) (bool, error) {
	row := tx.QueryRowContext(ctx, query, args...)
	var value int
	err := row.Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ListProfileNotes(ctx context.Context, ownerUserID string, includeDeleted bool) ([]domain.UserNote, error) {
	query := `SELECT ` + userNoteColumns + `
		FROM user_notes
		WHERE owner_user_id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.query(ctx, query, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []domain.UserNote
	for rows.Next() {
		note, err := scanUserNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

func (s *Store) ListVisibleHomeNotes(ctx context.Context, homeID string, userID string, includeDeleted bool) ([]domain.UserNote, error) {
	query := `SELECT DISTINCT ` + userNoteColumnsWithUN + `
		FROM user_notes un
		LEFT JOIN note_shares ns ON ns.note_id = un.id AND ns.target_user_id = ?
		WHERE un.home_id = ?
			AND (un.owner_user_id = ? OR ns.target_user_id = ?)`
	args := []any{userID, homeID, userID, userID}
	if !includeDeleted {
		query += ` AND un.deleted_at IS NULL`
	}
	query += ` ORDER BY un.updated_at DESC`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []domain.UserNote
	for rows.Next() {
		note, err := scanUserNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

func (s *Store) ListSyncedHomeNotes(ctx context.Context, homeID string, includeDeleted bool) ([]domain.UserNote, error) {
	query := `SELECT DISTINCT ` + userNoteColumnsWithUN + `
		FROM user_notes un
		WHERE un.home_id = ?
			AND EXISTS (SELECT 1 FROM note_shares ns WHERE ns.note_id = un.id)`
	if !includeDeleted {
		query += ` AND un.deleted_at IS NULL`
	}
	query += ` ORDER BY un.updated_at DESC`
	rows, err := s.query(ctx, query, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []domain.UserNote
	for rows.Next() {
		note, err := scanUserNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

func (s *Store) GetUserNoteByID(ctx context.Context, noteID string) (domain.UserNote, error) {
	row := s.queryRow(ctx, `SELECT `+userNoteColumns+`
		FROM user_notes
		WHERE id = ?`, noteID)
	return scanUserNote(row)
}

func (s *Store) GetProfileNote(ctx context.Context, ownerUserID string, noteKey string) (domain.UserNote, error) {
	row := s.queryRow(ctx, `SELECT `+userNoteColumns+`
		FROM user_notes
		WHERE owner_user_id = ? AND note_id = ?`, ownerUserID, noteKey)
	return scanUserNote(row)
}

func (s *Store) PermanentlyDeleteUserNote(ctx context.Context, noteID string, ownerUserID string, now time.Time) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE user_notes
		SET parent_id = NULL, pinned = FALSE, updated_at = ?, updated_by = ?
		WHERE owner_user_id = ? AND parent_id = (SELECT note_id FROM user_notes WHERE id = ? AND owner_user_id = ?)`,
		now, ownerUserID, ownerUserID, noteID, ownerUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM note_shares WHERE note_id = ?`, noteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM note_operations WHERE note_id = ?`, noteID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM user_notes WHERE id = ? AND owner_user_id = ? AND deleted_at IS NOT NULL`, noteID, ownerUserID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) GetHomeNoteVisibleToUser(ctx context.Context, homeID string, userID string, noteKey string) (domain.UserNote, error) {
	row := s.queryRow(ctx, `SELECT `+userNoteColumnsWithUN+`
			FROM user_notes un
			WHERE un.home_id = ? AND un.note_id = ?
				AND (un.owner_user_id = ? OR EXISTS (
					SELECT 1 FROM note_shares ns
					WHERE ns.note_id = un.id AND ns.target_user_id = ?
				))`,
		homeID, noteKey, userID, userID)
	return scanUserNote(row)
}

func (s *Store) GetHomeNoteByKey(ctx context.Context, homeID string, noteKey string) (domain.UserNote, error) {
	row := s.queryRow(ctx, `SELECT `+userNoteColumns+`
		FROM user_notes
		WHERE home_id = ? AND note_id = ?`, homeID, noteKey)
	return scanUserNote(row)
}

func (s *Store) GetOwnedHomeNote(ctx context.Context, homeID string, ownerUserID string, noteKey string) (domain.UserNote, error) {
	row := s.queryRow(ctx, `SELECT `+userNoteColumns+`
		FROM user_notes
		WHERE home_id = ? AND owner_user_id = ? AND note_id = ?`, homeID, ownerUserID, noteKey)
	return scanUserNote(row)
}

func (s *Store) UpsertUserNote(ctx context.Context, note domain.UserNote) error {
	_, err := s.exec(ctx, `INSERT INTO user_notes (
			id, note_id, owner_user_id, home_id, parent_id, mcp_excluded, pinned, sort_order, title, body_markdown, body_format, page_type, board_json,
			revision, checksum, crdt_state_json, collab_version, deleted_at, created_at, updated_at, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			note_id = excluded.note_id,
			owner_user_id = excluded.owner_user_id,
			home_id = excluded.home_id,
			parent_id = excluded.parent_id,
			mcp_excluded = excluded.mcp_excluded,
			pinned = excluded.pinned,
			sort_order = excluded.sort_order,
			title = excluded.title,
			body_markdown = excluded.body_markdown,
			body_format = excluded.body_format,
			page_type = excluded.page_type,
			board_json = excluded.board_json,
			revision = excluded.revision,
			checksum = excluded.checksum,
			crdt_state_json = excluded.crdt_state_json,
			collab_version = excluded.collab_version,
			deleted_at = excluded.deleted_at,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		note.ID,
		note.NoteID,
		note.OwnerUserID,
		nullableText(note.HomeID),
		nullableText(note.ParentID),
		note.MCPExcluded,
		note.Pinned,
		note.SortOrder,
		note.Title,
		noteBodyMarkdown(note),
		noteBodyFormat(note),
		note.PageType,
		note.BoardJSON,
		note.Revision,
		note.Checksum,
		note.CRDTStateJSON,
		note.CollabVersion,
		note.DeletedAt,
		note.CreatedAt,
		note.UpdatedAt,
		note.UpdatedBy,
	)
	return err
}

func (s *Store) SaveUserNoteWithOperations(ctx context.Context, note domain.UserNote, operations []domain.NoteOperation) error {
	return s.saveUserNoteWithOperations(ctx, note, operations)
}

func (s *Store) SaveUserNoteWithOperationsIfRevision(ctx context.Context, note domain.UserNote, operations []domain.NoteOperation, expectedRevision string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE user_notes SET
			note_id = ?,
			home_id = ?,
			parent_id = ?,
			mcp_excluded = ?,
			pinned = ?,
			sort_order = ?,
			title = ?,
			body_markdown = ?,
			body_format = ?,
			page_type = ?,
			board_json = ?,
			revision = ?,
			checksum = ?,
			crdt_state_json = ?,
			collab_version = ?,
			deleted_at = ?,
			updated_at = ?,
			updated_by = ?
		WHERE id = ? AND owner_user_id = ? AND (? = '' OR revision = ?)`,
		note.NoteID,
		nullableText(note.HomeID),
		nullableText(note.ParentID),
		note.MCPExcluded,
		note.Pinned,
		note.SortOrder,
		note.Title,
		noteBodyMarkdown(note),
		noteBodyFormat(note),
		note.PageType,
		note.BoardJSON,
		note.Revision,
		note.Checksum,
		note.CRDTStateJSON,
		note.CollabVersion,
		note.DeletedAt,
		note.UpdatedAt,
		note.UpdatedBy,
		note.ID,
		note.OwnerUserID,
		expectedRevision,
		expectedRevision,
	)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		current, currentErr := scanUserNote(tx.QueryRowContext(ctx, `SELECT `+userNoteColumns+`
			FROM user_notes
			WHERE id = ? AND owner_user_id = ?`, note.ID, note.OwnerUserID))
		if errors.Is(currentErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if currentErr != nil {
			return currentErr
		}
		return &UserNoteRevisionConflictError{Current: current}
	}

	for _, operation := range operations {
		sessionID, err := nullableExistingSessionID(ctx, tx, operation.SessionID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO note_operations (
				note_id, op_id, actor_user_id, session_id, base_version, applied_version, op_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			operation.NoteID,
			operation.OpID,
			operation.ActorUserID,
			sessionID,
			operation.BaseVersion,
			operation.AppliedVersion,
			operation.OpJSON,
			operation.CreatedAt,
		); err != nil {
			return err
		}
	}
	if err := txNotifyNoteChanged(ctx, tx, note, "notes.changed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) saveUserNoteWithOperations(ctx context.Context, note domain.UserNote, operations []domain.NoteOperation) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := saveUserNoteWithOperationsTx(ctx, tx, note, operations); err != nil {
		return err
	}
	return tx.Commit()
}

func saveUserNoteWithOperationsTx(ctx context.Context, tx *dbTx, note domain.UserNote, operations []domain.NoteOperation) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_notes (
				id, note_id, owner_user_id, home_id, parent_id, mcp_excluded, pinned, sort_order, title, body_markdown, body_format, page_type, board_json,
				revision, checksum, crdt_state_json, collab_version, deleted_at, created_at, updated_at, updated_by
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				note_id = excluded.note_id,
				owner_user_id = excluded.owner_user_id,
				home_id = excluded.home_id,
				parent_id = excluded.parent_id,
				mcp_excluded = excluded.mcp_excluded,
				pinned = excluded.pinned,
				sort_order = excluded.sort_order,
				title = excluded.title,
				body_markdown = excluded.body_markdown,
				body_format = excluded.body_format,
				page_type = excluded.page_type,
				board_json = excluded.board_json,
				revision = excluded.revision,
				checksum = excluded.checksum,
				crdt_state_json = excluded.crdt_state_json,
				collab_version = excluded.collab_version,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at,
				updated_by = excluded.updated_by`,
		note.ID,
		note.NoteID,
		note.OwnerUserID,
		nullableText(note.HomeID),
		nullableText(note.ParentID),
		note.MCPExcluded,
		note.Pinned,
		note.SortOrder,
		note.Title,
		noteBodyMarkdown(note),
		noteBodyFormat(note),
		note.PageType,
		note.BoardJSON,
		note.Revision,
		note.Checksum,
		note.CRDTStateJSON,
		note.CollabVersion,
		note.DeletedAt,
		note.CreatedAt,
		note.UpdatedAt,
		note.UpdatedBy,
	); err != nil {
		return err
	}

	for _, operation := range operations {
		sessionID, err := nullableExistingSessionID(ctx, tx, operation.SessionID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO note_operations (
				note_id, op_id, actor_user_id, session_id, base_version, applied_version, op_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			operation.NoteID,
			operation.OpID,
			operation.ActorUserID,
			sessionID,
			operation.BaseVersion,
			operation.AppliedVersion,
			operation.OpJSON,
			operation.CreatedAt,
		); err != nil {
			return err
		}
	}

	if err := txNotifyNoteChanged(ctx, tx, note, "notes.changed"); err != nil {
		return err
	}

	return nil
}

func nullableExistingSessionID(ctx context.Context, tx *dbTx, sessionID string) (any, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	exists, err := txRecordExists(ctx, tx, `SELECT 1 FROM app_sessions WHERE id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return sessionID, nil
}

func (s *Store) ListNoteShares(ctx context.Context, noteID string) ([]domain.NoteShareMember, error) {
	rows, err := s.query(ctx, `SELECT ns.note_id, ns.home_id, ns.target_user_id, ns.shared_by, ns.created_at, ns.updated_at, u.email
		FROM note_shares ns
		JOIN users u ON u.id = ns.target_user_id
		WHERE ns.note_id = ?
		ORDER BY ns.created_at ASC`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []domain.NoteShareMember
	for rows.Next() {
		share, err := scanNoteShareMember(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

func (s *Store) AddNoteShare(ctx context.Context, share domain.NoteShare) error {
	_, err := s.exec(ctx, `INSERT INTO note_shares (note_id, home_id, target_user_id, shared_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(note_id, target_user_id) DO UPDATE SET
			home_id = excluded.home_id,
			shared_by = excluded.shared_by,
			updated_at = excluded.updated_at`,
		share.NoteID,
		share.HomeID,
		share.TargetUserID,
		share.SharedBy,
		share.CreatedAt,
		share.UpdatedAt,
	)
	return err
}

func (s *Store) RemoveNoteShare(ctx context.Context, noteID string, targetUserID string) error {
	result, err := s.exec(ctx, `DELETE FROM note_shares WHERE note_id = ? AND target_user_id = ?`, noteID, targetUserID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveNoteSharesForHomeUser(ctx context.Context, homeID string, userID string) error {
	_, err := s.exec(ctx, `DELETE FROM note_shares WHERE home_id = ? AND target_user_id = ?`, homeID, userID)
	return err
}

func (s *Store) CountNoteShares(ctx context.Context, noteID string) (int, error) {
	row := s.queryRow(ctx, `SELECT COUNT(*) FROM note_shares WHERE note_id = ?`, noteID)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) ListNoteOperationsSince(ctx context.Context, noteID string, appliedVersion int64, limit int) ([]domain.NoteOperation, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.query(ctx, `SELECT note_id, op_id, actor_user_id, session_id, base_version, applied_version, op_json, created_at
		FROM note_operations
		WHERE note_id = ? AND applied_version > ?
		ORDER BY applied_version ASC
		LIMIT ?`, noteID, appliedVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var operations []domain.NoteOperation
	for rows.Next() {
		operation, err := scanNoteOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (s *Store) GetOldestNoteOperationVersion(ctx context.Context, noteID string) (int64, error) {
	row := s.queryRow(ctx, `SELECT COALESCE(MIN(applied_version), 0) FROM note_operations WHERE note_id = ?`, noteID)
	var version int64
	if err := row.Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Store) HasNoteOperation(ctx context.Context, noteID string, opID string) (bool, error) {
	row := s.queryRow(ctx, `SELECT 1 FROM note_operations WHERE note_id = ? AND op_id = ?`, noteID, opID)
	var value int
	err := row.Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func scanUserNote(scanner interface{ Scan(dest ...any) error }) (domain.UserNote, error) {
	var note domain.UserNote
	var homeID sql.NullString
	var parentID sql.NullString
	var deletedAt sql.NullTime
	err := scanner.Scan(
		&note.ID,
		&note.NoteID,
		&note.OwnerUserID,
		&homeID,
		&parentID,
		&note.MCPExcluded,
		&note.Pinned,
		&note.SortOrder,
		&note.Title,
		&note.BodyMarkdown,
		&note.BodyFormat,
		&note.PageType,
		&note.BoardJSON,
		&note.Revision,
		&note.Checksum,
		&note.CRDTStateJSON,
		&note.CollabVersion,
		&deletedAt,
		&note.CreatedAt,
		&note.UpdatedAt,
		&note.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserNote{}, ErrNotFound
	}
	if err != nil {
		return domain.UserNote{}, err
	}
	if homeID.Valid {
		note.HomeID = homeID.String
	}
	if parentID.Valid {
		note.ParentID = parentID.String
	}
	note.Content = note.BodyMarkdown
	if note.BodyFormat == "" {
		note.BodyFormat = "markdown"
	}
	if deletedAt.Valid {
		note.DeletedAt = &deletedAt.Time
	}
	return note, nil
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func noteBodyMarkdown(note domain.UserNote) string {
	if note.BodyMarkdown != "" {
		return note.BodyMarkdown
	}
	return note.Content
}

func noteBodyFormat(note domain.UserNote) string {
	if strings.TrimSpace(note.BodyFormat) != "" {
		return note.BodyFormat
	}
	return "markdown"
}

func scanNoteShareMember(scanner interface{ Scan(dest ...any) error }) (domain.NoteShareMember, error) {
	var share domain.NoteShareMember
	err := scanner.Scan(
		&share.NoteID,
		&share.HomeID,
		&share.TargetUserID,
		&share.SharedBy,
		&share.CreatedAt,
		&share.UpdatedAt,
		&share.Email,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NoteShareMember{}, ErrNotFound
	}
	return share, err
}

func scanNoteOperation(scanner interface{ Scan(dest ...any) error }) (domain.NoteOperation, error) {
	var operation domain.NoteOperation
	var sessionID sql.NullString
	err := scanner.Scan(
		&operation.NoteID,
		&operation.OpID,
		&operation.ActorUserID,
		&sessionID,
		&operation.BaseVersion,
		&operation.AppliedVersion,
		&operation.OpJSON,
		&operation.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NoteOperation{}, ErrNotFound
	}
	if err != nil {
		return domain.NoteOperation{}, err
	}
	if sessionID.Valid {
		operation.SessionID = sessionID.String
	}
	return operation, nil
}
