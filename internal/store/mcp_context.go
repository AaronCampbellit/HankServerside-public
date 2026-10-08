package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dropfile/HankServerside/internal/domain"
)

const mcpContextSourceColumns = `id, user_id, home_id, name, source_id, root_path, enabled, last_tested_at, last_test_error, created_at, updated_at`

func (s *Store) CreateMCPContextSource(ctx context.Context, source domain.MCPContextSource) error {
	_, err := s.exec(ctx, `INSERT INTO mcp_context_sources (`+mcpContextSourceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, source.ID, source.OwnerUserID, source.HomeID, source.Name, source.FileSourceID, source.RootPath, source.Enabled, source.LastTestedAt, source.LastTestError, source.CreatedAt, source.UpdatedAt)
	return err
}

func (s *Store) ListMCPContextSourcesByUser(ctx context.Context, userID string, enabledOnly bool) ([]domain.MCPContextSource, error) {
	query := `SELECT ` + mcpContextSourceColumns + ` FROM mcp_context_sources WHERE user_id = ?`
	if enabledOnly {
		query += ` AND enabled = TRUE`
	}
	query += ` ORDER BY lower(name), created_at`
	rows, err := s.query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MCPContextSource
	for rows.Next() {
		item, err := scanMCPContextSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetMCPContextSourceForUser(ctx context.Context, id, userID string) (domain.MCPContextSource, error) {
	return scanMCPContextSource(s.queryRow(ctx, `SELECT `+mcpContextSourceColumns+` FROM mcp_context_sources WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) UpdateMCPContextSourceForUser(ctx context.Context, source domain.MCPContextSource) error {
	result, err := s.exec(ctx, `UPDATE mcp_context_sources SET home_id = ?, name = ?, source_id = ?, root_path = ?, enabled = ?, last_tested_at = ?, last_test_error = ?, updated_at = ? WHERE id = ? AND user_id = ?`, source.HomeID, source.Name, source.FileSourceID, source.RootPath, source.Enabled, source.LastTestedAt, source.LastTestError, source.UpdatedAt, source.ID, source.OwnerUserID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMCPContextSourceForUser(ctx context.Context, id, userID string) error {
	result, err := s.exec(ctx, `DELETE FROM mcp_context_sources WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMCPContextSource(scanner interface{ Scan(...any) error }) (domain.MCPContextSource, error) {
	var source domain.MCPContextSource
	var tested sql.NullTime
	err := scanner.Scan(&source.ID, &source.OwnerUserID, &source.HomeID, &source.Name, &source.FileSourceID, &source.RootPath, &source.Enabled, &tested, &source.LastTestError, &source.CreatedAt, &source.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MCPContextSource{}, ErrNotFound
	}
	if err != nil {
		return domain.MCPContextSource{}, err
	}
	if tested.Valid {
		source.LastTestedAt = &tested.Time
	}
	return source, nil
}
