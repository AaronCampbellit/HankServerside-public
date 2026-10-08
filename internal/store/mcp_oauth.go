package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

// --- Clients (Dynamic Client Registration) ---

func (s *Store) CreateMCPOAuthClient(ctx context.Context, client domain.MCPOAuthClient) error {
	redirects, err := json.Marshal(client.RedirectURIs)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(client.GrantTypes)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO mcp_oauth_clients (
		id, client_secret_hash, redirect_uris, client_name, token_endpoint_auth_method, grant_types, scope, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		client.ID,
		client.ClientSecretHash,
		string(redirects),
		client.ClientName,
		client.TokenEndpointAuthMethod,
		string(grants),
		client.Scope,
		client.CreatedAt,
	)
	return err
}

func (s *Store) GetMCPOAuthClient(ctx context.Context, id string) (domain.MCPOAuthClient, error) {
	row := s.queryRow(ctx, `SELECT id, client_secret_hash, redirect_uris, client_name, token_endpoint_auth_method, grant_types, scope, created_at
		FROM mcp_oauth_clients WHERE id = ?`, id)
	var client domain.MCPOAuthClient
	var redirects, grants string
	err := row.Scan(
		&client.ID,
		&client.ClientSecretHash,
		&redirects,
		&client.ClientName,
		&client.TokenEndpointAuthMethod,
		&grants,
		&client.Scope,
		&client.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MCPOAuthClient{}, ErrNotFound
	}
	if err != nil {
		return domain.MCPOAuthClient{}, err
	}
	if redirects != "" {
		if err := json.Unmarshal([]byte(redirects), &client.RedirectURIs); err != nil {
			return domain.MCPOAuthClient{}, err
		}
	}
	if grants != "" {
		if err := json.Unmarshal([]byte(grants), &client.GrantTypes); err != nil {
			return domain.MCPOAuthClient{}, err
		}
	}
	return client, nil
}

// --- Authorization codes ---

func (s *Store) CreateMCPAuthCode(ctx context.Context, code domain.MCPAuthCode) error {
	scopes, err := json.Marshal(code.Scopes)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO mcp_oauth_auth_codes (
		code_hash, client_id, user_id, redirect_uri, code_challenge, code_challenge_method, scopes, resource, expires_at, consumed_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		code.CodeHash,
		code.ClientID,
		code.UserID,
		code.RedirectURI,
		code.CodeChallenge,
		code.CodeChallengeMethod,
		string(scopes),
		code.Resource,
		code.ExpiresAt,
		code.ConsumedAt,
		code.CreatedAt,
	)
	return err
}

// GetMCPAuthCode returns an active authorization code without consuming it.
// Callers use this to validate client bindings and PKCE before the atomic consume.
func (s *Store) GetMCPAuthCode(ctx context.Context, codeHash string) (domain.MCPAuthCode, error) {
	return scanActiveMCPAuthCode(s.queryRow(ctx, `SELECT code_hash, client_id, user_id, redirect_uri, code_challenge, code_challenge_method, scopes, resource, expires_at, consumed_at, created_at
		FROM mcp_oauth_auth_codes WHERE code_hash = ?`, codeHash))
}

// ConsumeMCPAuthCode atomically marks an unconsumed, unexpired code as used and
// returns it. A second attempt (or an expired code) returns ErrNotFound, giving
// single-use semantics required by OAuth 2.1.
func (s *Store) ConsumeMCPAuthCode(ctx context.Context, codeHash string) (domain.MCPAuthCode, error) {
	code, err := scanActiveMCPAuthCode(s.queryRow(ctx, `SELECT code_hash, client_id, user_id, redirect_uri, code_challenge, code_challenge_method, scopes, resource, expires_at, consumed_at, created_at
		FROM mcp_oauth_auth_codes WHERE code_hash = ?`, codeHash))
	if err != nil {
		return domain.MCPAuthCode{}, err
	}
	now := time.Now().UTC()
	result, err := s.exec(ctx, `UPDATE mcp_oauth_auth_codes SET consumed_at = ? WHERE code_hash = ? AND consumed_at IS NULL AND expires_at > ?`, now, codeHash, now)
	if err != nil {
		return domain.MCPAuthCode{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.MCPAuthCode{}, err
	}
	if affected == 0 {
		return domain.MCPAuthCode{}, ErrNotFound
	}
	return code, nil
}

func scanActiveMCPAuthCode(row interface{ Scan(dest ...any) error }) (domain.MCPAuthCode, error) {
	var code domain.MCPAuthCode
	var scopesRaw string
	var consumedAt sql.NullTime
	err := row.Scan(
		&code.CodeHash,
		&code.ClientID,
		&code.UserID,
		&code.RedirectURI,
		&code.CodeChallenge,
		&code.CodeChallengeMethod,
		&scopesRaw,
		&code.Resource,
		&code.ExpiresAt,
		&consumedAt,
		&code.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MCPAuthCode{}, ErrNotFound
	}
	if err != nil {
		return domain.MCPAuthCode{}, err
	}
	now := time.Now().UTC()
	if consumedAt.Valid || code.ExpiresAt.Before(now) {
		return domain.MCPAuthCode{}, ErrNotFound
	}
	if scopesRaw != "" {
		if err := json.Unmarshal([]byte(scopesRaw), &code.Scopes); err != nil {
			return domain.MCPAuthCode{}, err
		}
	}
	return code, nil
}

// --- Access / refresh tokens ---

func (s *Store) CreateMCPToken(ctx context.Context, token domain.MCPToken) error {
	scopes, err := json.Marshal(token.Scopes)
	if err != nil {
		return err
	}
	var refreshHash any
	if token.RefreshTokenHash != "" {
		refreshHash = token.RefreshTokenHash
	}
	_, err = s.exec(ctx, `INSERT INTO mcp_oauth_tokens (
		id, client_id, user_id, access_token_hash, refresh_token_hash, scopes, resource,
		access_expires_at, refresh_expires_at, revoked_at, last_used_at, last_used_route,
		last_used_ip_hash, request_count, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token.ID,
		token.ClientID,
		token.UserID,
		token.AccessTokenHash,
		refreshHash,
		string(scopes),
		token.Resource,
		token.AccessExpiresAt,
		token.RefreshExpiresAt,
		token.RevokedAt,
		token.LastUsedAt,
		token.LastUsedRoute,
		token.LastUsedIPHash,
		token.RequestCount,
		token.CreatedAt,
		token.UpdatedAt,
	)
	return err
}

func (s *Store) GetMCPTokenByAccessHash(ctx context.Context, accessHash string) (domain.MCPToken, error) {
	row := s.queryRow(ctx, mcpTokenSelect+` WHERE access_token_hash = ?`, accessHash)
	token, err := scanMCPToken(row)
	if err != nil {
		return domain.MCPToken{}, err
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil || token.AccessExpiresAt.Before(now) {
		return domain.MCPToken{}, ErrNotFound
	}
	return token, nil
}

func (s *Store) GetMCPTokenByRefreshHash(ctx context.Context, refreshHash string) (domain.MCPToken, error) {
	row := s.queryRow(ctx, mcpTokenSelect+` WHERE refresh_token_hash = ?`, refreshHash)
	token, err := scanMCPToken(row)
	if err != nil {
		return domain.MCPToken{}, err
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil || token.RefreshExpiresAt == nil || token.RefreshExpiresAt.Before(now) {
		return domain.MCPToken{}, ErrNotFound
	}
	return token, nil
}

func (s *Store) RevokeMCPToken(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result, err := s.exec(ctx, `UPDATE mcp_oauth_tokens SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateMCPToken atomically revokes the active token and inserts its replacement.
// A failed insert leaves the original refresh token usable, while concurrent
// rotations allow exactly one winner.
func (s *Store) RotateMCPToken(ctx context.Context, oldID string, token domain.MCPToken) error {
	scopes, err := json.Marshal(token.Scopes)
	if err != nil {
		return err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_tokens SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`, now, now, oldID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	var refreshHash any
	if token.RefreshTokenHash != "" {
		refreshHash = token.RefreshTokenHash
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_oauth_tokens (
		id, client_id, user_id, access_token_hash, refresh_token_hash, scopes, resource,
		access_expires_at, refresh_expires_at, revoked_at, last_used_at, last_used_route,
		last_used_ip_hash, request_count, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token.ID, token.ClientID, token.UserID, token.AccessTokenHash, refreshHash, string(scopes), token.Resource,
		token.AccessExpiresAt, token.RefreshExpiresAt, token.RevokedAt, token.LastUsedAt, token.LastUsedRoute,
		token.LastUsedIPHash, token.RequestCount, token.CreatedAt, token.UpdatedAt,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fleet_grants SET mcp_token_id=? WHERE mcp_token_id=? AND user_id=?`, token.ID, oldID, token.UserID); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) RecordMCPTokenUse(ctx context.Context, id string, route string, ipHash string, usedAt time.Time) error {
	_, err := s.exec(ctx, `UPDATE mcp_oauth_tokens
		SET last_used_at = ?, last_used_route = ?, last_used_ip_hash = ?, request_count = request_count + 1, updated_at = ?
		WHERE id = ?`,
		usedAt, route, ipHash, usedAt, id,
	)
	return err
}

// ListMCPTokensByUser returns the user's active (non-revoked) MCP grants, newest
// first. Each row is one connected client/app for the dashboard to display.
func (s *Store) ListMCPTokensByUser(ctx context.Context, userID string) ([]domain.MCPToken, error) {
	rows, err := s.query(ctx, mcpTokenSelect+` WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []domain.MCPToken
	for rows.Next() {
		token, err := scanMCPToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeMCPTokenForUser revokes a grant only if it belongs to the given user, so
// a user can disconnect their own AI apps but not anyone else's.
func (s *Store) RevokeMCPTokenForUser(ctx context.Context, id string, userID string) error {
	now := time.Now().UTC()
	result, err := s.exec(ctx, `UPDATE mcp_oauth_tokens SET revoked_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, now, now, id, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

const mcpTokenSelect = `SELECT id, client_id, user_id, access_token_hash, refresh_token_hash, scopes, resource,
	access_expires_at, refresh_expires_at, revoked_at, last_used_at, last_used_route, last_used_ip_hash,
	request_count, created_at, updated_at
	FROM mcp_oauth_tokens`

func scanMCPToken(scanner interface{ Scan(dest ...any) error }) (domain.MCPToken, error) {
	var token domain.MCPToken
	var scopesRaw string
	var refreshHash sql.NullString
	var refreshExpiresAt, revokedAt, lastUsedAt sql.NullTime
	err := scanner.Scan(
		&token.ID,
		&token.ClientID,
		&token.UserID,
		&token.AccessTokenHash,
		&refreshHash,
		&scopesRaw,
		&token.Resource,
		&token.AccessExpiresAt,
		&refreshExpiresAt,
		&revokedAt,
		&lastUsedAt,
		&token.LastUsedRoute,
		&token.LastUsedIPHash,
		&token.RequestCount,
		&token.CreatedAt,
		&token.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MCPToken{}, ErrNotFound
	}
	if err != nil {
		return domain.MCPToken{}, err
	}
	if refreshHash.Valid {
		token.RefreshTokenHash = refreshHash.String
	}
	if scopesRaw != "" {
		if err := json.Unmarshal([]byte(scopesRaw), &token.Scopes); err != nil {
			return domain.MCPToken{}, err
		}
	}
	if refreshExpiresAt.Valid {
		token.RefreshExpiresAt = &refreshExpiresAt.Time
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Time
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Time
	}
	return token, nil
}

func (s *Store) MCPEnabled(ctx context.Context, fallback bool) (bool, error) {
	var enabled bool
	err := s.queryRow(ctx, `SELECT enabled FROM mcp_connector_settings WHERE singleton=true`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return enabled, err
}
func (s *Store) SetMCPEnabled(ctx context.Context, enabled bool, user string) error {
	_, err := s.exec(ctx, `INSERT INTO mcp_connector_settings(singleton,enabled,updated_by) VALUES(true,?,?) ON CONFLICT(singleton) DO UPDATE SET enabled=excluded.enabled,updated_by=excluded.updated_by,updated_at=now()`, enabled, user)
	return err
}
