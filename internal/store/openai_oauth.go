package store

import (
	"context"
	"database/sql"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) UpsertOpenAIAccount(ctx context.Context, account domain.OpenAIAccount) error {
	if account.AuthProvider == "" {
		account.AuthProvider = "chatgpt_codex"
	}
	accessToken, err := s.encryptSecret(account.AccessToken)
	if err != nil {
		return err
	}
	refreshToken, err := s.encryptSecret(account.RefreshToken)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO openai_accounts (user_id, provider_user_id, auth_provider, chatgpt_plan_type, access_token, refresh_token, token_type, scope, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			provider_user_id = excluded.provider_user_id,
			auth_provider = excluded.auth_provider,
			chatgpt_plan_type = excluded.chatgpt_plan_type,
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			token_type = excluded.token_type,
			scope = excluded.scope,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`,
		account.UserID, account.ProviderUserID, account.AuthProvider, account.ChatGPTPlanType, accessToken, refreshToken, account.TokenType, account.Scope, account.ExpiresAt, account.CreatedAt, account.UpdatedAt)
	return err
}

func (s *Store) GetOpenAIAccount(ctx context.Context, userID string) (domain.OpenAIAccount, error) {
	var account domain.OpenAIAccount
	err := s.queryRow(ctx, `SELECT user_id, provider_user_id, auth_provider, chatgpt_plan_type, access_token, refresh_token, token_type, scope, expires_at, created_at, updated_at
		FROM openai_accounts WHERE user_id = ?`, userID).Scan(&account.UserID, &account.ProviderUserID, &account.AuthProvider, &account.ChatGPTPlanType, &account.AccessToken, &account.RefreshToken, &account.TokenType, &account.Scope, &account.ExpiresAt, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.OpenAIAccount{}, ErrNotFound
		}
		return domain.OpenAIAccount{}, err
	}
	if account.AccessToken, err = s.decryptSecret(account.AccessToken); err != nil {
		return domain.OpenAIAccount{}, err
	}
	if account.RefreshToken, err = s.decryptSecret(account.RefreshToken); err != nil {
		return domain.OpenAIAccount{}, err
	}
	return account, nil
}

func (s *Store) DeleteOpenAIAccount(ctx context.Context, userID string) error {
	_, err := s.exec(ctx, `DELETE FROM openai_accounts WHERE user_id = ?`, userID)
	return err
}
