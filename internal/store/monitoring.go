package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/dropfile/HankServerside/internal/domain"
)

type monitoringCredentials struct {
	Password     string
	WebhookToken string
}

func (s *Store) GetMonitoringSettings(ctx context.Context, homeID string) (domain.MonitoringSettings, error) {
	var v domain.MonitoringSettings
	var encrypted string
	err := s.queryRow(ctx, `SELECT home_id,inbox_enabled,inbox_audience,email_enabled,smtp_host,smtp_port,smtp_username,email_from,email_to,encrypted_credentials,updated_at,coalesce(updated_by,'') FROM monitoring_delivery_settings WHERE home_id=?`, homeID).Scan(&v.HomeID, &v.InboxEnabled, &v.InboxAudience, &v.EmailEnabled, &v.SMTPHost, &v.SMTPPort, &v.SMTPUsername, &v.EmailFrom, &v.EmailTo, &encrypted, &v.UpdatedAt, &v.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	raw, err := s.decryptSecret(encrypted)
	if err != nil {
		return v, err
	}
	var credentials monitoringCredentials
	if err = json.Unmarshal([]byte(raw), &credentials); err != nil {
		return v, errors.New("cannot decode monitoring credentials")
	}
	v.SMTPPassword = credentials.Password
	v.WebhookToken = credentials.WebhookToken
	return v, nil
}

func (s *Store) SaveMonitoringSettings(ctx context.Context, v domain.MonitoringSettings) error {
	if s.secretBox == nil {
		return errors.New("monitoring settings require secret encryption")
	}
	// Encode before encryption so a literal password beginning with the storage
	// envelope prefix is still encrypted as plaintext input, never reinterpreted.
	raw, err := json.Marshal(monitoringCredentials{v.SMTPPassword, v.WebhookToken})
	if err != nil {
		return err
	}
	encrypted, err := s.encryptSecret(string(raw))
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO monitoring_delivery_settings(home_id,inbox_enabled,inbox_audience,email_enabled,smtp_host,smtp_port,smtp_username,email_from,email_to,encrypted_credentials,updated_at,updated_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,'')) ON CONFLICT(home_id) DO UPDATE SET inbox_enabled=excluded.inbox_enabled,inbox_audience=excluded.inbox_audience,email_enabled=excluded.email_enabled,smtp_host=excluded.smtp_host,smtp_port=excluded.smtp_port,smtp_username=excluded.smtp_username,email_from=excluded.email_from,email_to=excluded.email_to,encrypted_credentials=excluded.encrypted_credentials,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, v.HomeID, v.InboxEnabled, v.InboxAudience, v.EmailEnabled, v.SMTPHost, v.SMTPPort, v.SMTPUsername, v.EmailFrom, v.EmailTo, encrypted, v.UpdatedAt, v.UpdatedBy)
	return err
}
