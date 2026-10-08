package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) CreateNotesAPIToken(ctx context.Context, token domain.NotesAPIToken) error {
	scopes, err := json.Marshal(token.Scopes)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO notes_api_tokens (
		id, home_id, user_id, name, token_hash, scopes, allow_home_notes, expires_at, revoked_at,
		last_used_at, last_used_route, last_used_ip_hash, last_used_user_agent_hash, request_count,
		created_at, created_by, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token.ID,
		token.HomeID,
		token.UserID,
		token.Name,
		token.TokenHash,
		string(scopes),
		token.AllowHomeNotes,
		token.ExpiresAt,
		token.RevokedAt,
		token.LastUsedAt,
		token.LastUsedRoute,
		token.LastUsedIPHash,
		token.LastUsedUserAgentHash,
		token.RequestCount,
		token.CreatedAt,
		token.CreatedBy,
		token.UpdatedAt,
	)
	return err
}

func (s *Store) ListNotesAPITokensByHome(ctx context.Context, homeID string) ([]domain.NotesAPIToken, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, user_id, name, token_hash, scopes, allow_home_notes, expires_at, revoked_at,
		last_used_at, last_used_route, last_used_ip_hash, last_used_user_agent_hash, request_count,
		created_at, created_by, updated_at
		FROM notes_api_tokens
		WHERE home_id = ?
		ORDER BY created_at DESC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []domain.NotesAPIToken
	for rows.Next() {
		token, err := scanNotesAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) GetNotesAPITokenByHash(ctx context.Context, tokenHash string) (domain.NotesAPIToken, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, user_id, name, token_hash, scopes, allow_home_notes, expires_at, revoked_at,
		last_used_at, last_used_route, last_used_ip_hash, last_used_user_agent_hash, request_count,
		created_at, created_by, updated_at
		FROM notes_api_tokens
		WHERE token_hash = ?`, tokenHash)
	token, err := scanNotesAPIToken(row)
	if err != nil {
		return domain.NotesAPIToken{}, err
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil || (token.ExpiresAt != nil && token.ExpiresAt.Before(now)) {
		return domain.NotesAPIToken{}, ErrNotFound
	}
	return token, nil
}

func (s *Store) RevokeNotesAPIToken(ctx context.Context, homeID string, tokenID string) error {
	now := time.Now().UTC()
	result, err := s.exec(ctx, `UPDATE notes_api_tokens SET revoked_at = ?, updated_at = ? WHERE home_id = ? AND id = ? AND revoked_at IS NULL`, now, now, homeID, tokenID)
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

func (s *Store) RecordNotesAPITokenUse(ctx context.Context, tokenID string, route string, ipHash string, userAgentHash string, usedAt time.Time) error {
	_, err := s.exec(ctx, `UPDATE notes_api_tokens
		SET last_used_at = ?, last_used_route = ?, last_used_ip_hash = ?, last_used_user_agent_hash = ?,
			request_count = request_count + 1, updated_at = ?
		WHERE id = ?`,
		usedAt,
		route,
		ipHash,
		userAgentHash,
		usedAt,
		tokenID,
	)
	return err
}

func scanNotesAPIToken(scanner interface{ Scan(dest ...any) error }) (domain.NotesAPIToken, error) {
	var token domain.NotesAPIToken
	var scopesRaw string
	var expiresAt sql.NullTime
	var revokedAt sql.NullTime
	var lastUsedAt sql.NullTime
	err := scanner.Scan(
		&token.ID,
		&token.HomeID,
		&token.UserID,
		&token.Name,
		&token.TokenHash,
		&scopesRaw,
		&token.AllowHomeNotes,
		&expiresAt,
		&revokedAt,
		&lastUsedAt,
		&token.LastUsedRoute,
		&token.LastUsedIPHash,
		&token.LastUsedUserAgentHash,
		&token.RequestCount,
		&token.CreatedAt,
		&token.CreatedBy,
		&token.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotesAPIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.NotesAPIToken{}, err
	}
	if scopesRaw != "" {
		if err := json.Unmarshal([]byte(scopesRaw), &token.Scopes); err != nil {
			return domain.NotesAPIToken{}, err
		}
	}
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Time
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Time
	}
	return token, nil
}
