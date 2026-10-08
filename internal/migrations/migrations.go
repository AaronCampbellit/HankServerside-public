package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Migration struct {
	Version    int64
	Name       string
	Statements []string
	Checksum   string
}

type Status struct {
	Version    int64     `json:"version"`
	Name       string    `json:"name"`
	Checksum   string    `json:"checksum"`
	AppliedAt  time.Time `json:"applied_at"`
	DurationMS int       `json:"duration_ms"`
}

var ErrChecksumMismatch = errors.New("schema migration checksum mismatch")
var ErrPendingMigrations = errors.New("schema migrations pending")
var ErrUnknownMigration = errors.New("unknown schema migration recorded")

var compatibleChecksums = map[int64]map[string]struct{}{
	// Version 1 was originally a baseline marker that only created
	// schema_migrations. Existing single-home installs may have this checksum
	// recorded even though the live schema was created before file-backed
	// migrations were introduced.
	1: {
		"50f7f293c05c9e0636f07dc8ddba6e0a682ef58d31686c820719a411a07ca035": {},
	},
}

//go:embed sql/*.up.sql
var migrationFiles embed.FS

var migrationFilePattern = regexp.MustCompile(`^(\d{6})_(.+)\.up\.sql$`)

func Checksum(migration Migration) string {
	if migration.Checksum != "" {
		return migration.Checksum
	}
	sum := sha256.New()
	_, _ = fmt.Fprintf(sum, "%d:%s\n", migration.Version, migration.Name)
	for _, statement := range migration.Statements {
		_, _ = sum.Write([]byte(statement))
		_, _ = sum.Write([]byte{'\n'})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func checksumMatches(status Status, migration Migration) bool {
	if status.Checksum == Checksum(migration) {
		return true
	}
	if allowed, ok := compatibleChecksums[status.Version]; ok {
		_, ok := allowed[status.Checksum]
		return ok
	}
	return false
}

func All() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "sql")
	if err != nil {
		return nil, err
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		matches := migrationFilePattern.FindStringSubmatch(name)
		if len(matches) != 3 {
			continue
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", name, err)
		}
		raw, err := fs.ReadFile(migrationFiles, path.Join("sql", name))
		if err != nil {
			return nil, err
		}
		statements := splitSQLStatements(string(raw))
		if len(statements) == 0 {
			return nil, fmt.Errorf("migration %s has no statements", name)
		}
		migration := Migration{
			Version:    version,
			Name:       matches[2],
			Statements: statements,
		}
		migration.Checksum = Checksum(migration)
		migrations = append(migrations, migration)
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version == migrations[i-1].Version {
			return nil, fmt.Errorf("duplicate migration version %d", migrations[i].Version)
		}
	}
	return migrations, nil
}

func EnsureTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		duration_ms INTEGER NOT NULL
	);`)
	return err
}

func ApplyPending(ctx context.Context, db *sql.DB) error {
	if err := EnsureTable(ctx, db); err != nil {
		return err
	}
	migrations, err := All()
	if err != nil {
		return err
	}
	statuses, err := AppliedReadOnly(ctx, db)
	if err != nil {
		return err
	}
	applied := map[int64]Status{}
	for _, status := range statuses {
		applied[status.Version] = status
	}
	for _, migration := range migrations {
		if status, ok := applied[migration.Version]; ok {
			if !checksumMatches(status, migration) {
				return fmt.Errorf("%w: version %d", ErrChecksumMismatch, migration.Version)
			}
			continue
		}
		if err := applyOne(ctx, db, migration); err != nil {
			return err
		}
	}
	if err := Check(ctx, db); err != nil {
		return err
	}
	return CheckRequiredExtensions(ctx, db)
}

func BaselineExisting(ctx context.Context, db *sql.DB, duration time.Duration) error {
	if err := EnsureTable(ctx, db); err != nil {
		return err
	}
	migrations, err := All()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return nil
	}
	migration := migrations[0]
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at, duration_ms)
			VALUES ($1, $2, $3, now(), $4)
			ON CONFLICT(version) DO NOTHING`,
		migration.Version,
		migration.Name,
		Checksum(migration),
		int(duration.Milliseconds()),
	); err != nil {
		return err
	}
	return nil
}

func Applied(ctx context.Context, db *sql.DB) ([]Status, error) {
	if err := EnsureTable(ctx, db); err != nil {
		return nil, err
	}
	return AppliedReadOnly(ctx, db)
}

func AppliedReadOnly(ctx context.Context, db *sql.DB) ([]Status, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name, checksum, applied_at, duration_ms FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var statuses []Status
	for rows.Next() {
		var status Status
		if err := rows.Scan(&status.Version, &status.Name, &status.Checksum, &status.AppliedAt, &status.DurationMS); err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func Check(ctx context.Context, db *sql.DB) error {
	statuses, err := Applied(ctx, db)
	if err != nil {
		return err
	}
	return CheckStatuses(statuses)
}

func CheckReadOnly(ctx context.Context, db *sql.DB) error {
	statuses, err := AppliedReadOnly(ctx, db)
	if err != nil {
		return err
	}
	return CheckStatuses(statuses)
}

func CheckStatuses(statuses []Status) error {
	migrations, err := All()
	if err != nil {
		return err
	}
	known := map[int64]Migration{}
	for _, migration := range migrations {
		known[migration.Version] = migration
	}
	applied := map[int64]Status{}
	for _, status := range statuses {
		migration, ok := known[status.Version]
		if !ok {
			return fmt.Errorf("%w: version %d", ErrUnknownMigration, status.Version)
		}
		if !checksumMatches(status, migration) {
			return fmt.Errorf("%w: version %d", ErrChecksumMismatch, status.Version)
		}
		applied[status.Version] = status
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; !ok {
			return fmt.Errorf("%w: version %d", ErrPendingMigrations, migration.Version)
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, migration Migration) error {
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range migration.Statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %06d %s: %w", migration.Version, migration.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at, duration_ms)
		VALUES ($1, $2, $3, now(), $4)`,
		migration.Version,
		migration.Name,
		Checksum(migration),
		int(time.Since(start).Milliseconds()),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func splitSQLStatements(raw string) []string {
	var statements []string
	var current strings.Builder
	var dollarQuote string
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if dollarQuote == "" && !inSingleQuote && !inDoubleQuote {
			if strings.HasPrefix(raw[i:], "--") {
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
				current.WriteByte('\n')
				continue
			}
			if strings.HasPrefix(raw[i:], "/*") {
				commentStart := i
				depth := 1
				i += 2
				for i < len(raw) && depth > 0 {
					if strings.HasPrefix(raw[i:], "/*") {
						depth++
						i += 2
					} else if strings.HasPrefix(raw[i:], "*/") {
						depth--
						i += 2
					} else {
						i++
					}
				}
				if depth > 0 {
					current.WriteString(raw[commentStart:])
					break
				}
				i--
				current.WriteByte(' ')
				continue
			}
		}
		current.WriteByte(ch)
		if escaped {
			escaped = false
			continue
		}
		if inSingleQuote && ch == '\\' {
			escaped = true
			continue
		}
		if dollarQuote != "" {
			if strings.HasPrefix(raw[i:], dollarQuote) {
				current.WriteString(raw[i+1 : i+len(dollarQuote)])
				i += len(dollarQuote) - 1
				dollarQuote = ""
			}
			continue
		}
		switch ch {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '$':
			if !inSingleQuote && !inDoubleQuote {
				if end := strings.IndexByte(raw[i+1:], '$'); end >= 0 {
					tag := raw[i : i+end+2]
					if validDollarQuote(tag) {
						current.WriteString(raw[i+1 : i+len(tag)])
						i += len(tag) - 1
						dollarQuote = tag
					}
				}
			}
		case ';':
			if !inSingleQuote && !inDoubleQuote {
				statement := strings.TrimSpace(current.String())
				if statement != "" {
					statements = append(statements, statement)
				}
				current.Reset()
			}
		}
	}
	if tail := strings.TrimSpace(current.String()); tail != "" {
		statements = append(statements, tail)
	}
	return statements
}

func validDollarQuote(tag string) bool {
	if len(tag) < 2 || tag[0] != '$' || tag[len(tag)-1] != '$' {
		return false
	}
	for _, ch := range tag[1 : len(tag)-1] {
		if ch != '_' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}
