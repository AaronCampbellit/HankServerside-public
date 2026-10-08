package store

import (
	"context"
	"database/sql"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) GetEntraSSOSettings(ctx context.Context, homeID string) (domain.EntraSSOSettings, error) {
	var value domain.EntraSSOSettings
	err := s.queryRow(ctx, `SELECT home_id, enabled, tenant_id, client_id, client_secret, public_base_url, updated_at, updated_by
		FROM entra_sso_settings WHERE home_id = ?`, homeID).Scan(&value.HomeID, &value.Enabled, &value.TenantID, &value.ClientID, &value.ClientSecret, &value.PublicBaseURL, &value.UpdatedAt, &value.UpdatedBy)
	if err == sql.ErrNoRows {
		return domain.EntraSSOSettings{}, ErrNotFound
	}
	if err != nil {
		return domain.EntraSSOSettings{}, err
	}
	value.ClientSecret, err = s.decryptSecret(value.ClientSecret)
	return value, err
}

func (s *Store) UpsertEntraSSOSettings(ctx context.Context, value domain.EntraSSOSettings) error {
	secret, err := s.encryptSecret(value.ClientSecret)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO entra_sso_settings (home_id, enabled, tenant_id, client_id, client_secret, public_base_url, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id) DO UPDATE SET enabled = excluded.enabled, tenant_id = excluded.tenant_id, client_id = excluded.client_id, client_secret = excluded.client_secret, public_base_url = excluded.public_base_url, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		value.HomeID, value.Enabled, value.TenantID, value.ClientID, secret, value.PublicBaseURL, value.UpdatedAt, value.UpdatedBy)
	return err
}
