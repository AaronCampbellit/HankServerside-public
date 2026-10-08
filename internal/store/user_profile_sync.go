package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) GetUserProfileSettings(ctx context.Context, userID string) (domain.UserProfileSettings, error) {
	row := s.queryRow(ctx, `SELECT user_id, revision, settings_json, created_at, updated_at
		FROM user_profile_settings
		WHERE user_id = ?`, userID)
	return scanUserProfileSettings(row)
}

func (s *Store) SaveUserProfileSettings(ctx context.Context, userID string, expectedRevision *int, settings json.RawMessage) (domain.UserProfileSettings, error) {
	if len(settings) == 0 || !json.Valid(settings) {
		return domain.UserProfileSettings{}, fmt.Errorf("settings must be valid json")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.UserProfileSettings{}, err
	}
	defer tx.Rollback()

	existing, err := scanUserProfileSettings(tx.QueryRowContext(ctx, `SELECT user_id, revision, settings_json, created_at, updated_at
		FROM user_profile_settings
		WHERE user_id = ?
		FOR UPDATE`, userID))
	now := time.Now().UTC()
	switch err {
	case nil:
		if expectedRevision != nil && existing.Revision != *expectedRevision {
			return domain.UserProfileSettings{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, existing.Revision)
		}
		existing.Revision++
		existing.Settings = append(json.RawMessage(nil), settings...)
		existing.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `UPDATE user_profile_settings
			SET revision = ?, settings_json = ?, updated_at = ?
			WHERE user_id = ?`, existing.Revision, string(settings), existing.UpdatedAt, userID); err != nil {
			return domain.UserProfileSettings{}, err
		}
	case ErrNotFound:
		if expectedRevision != nil {
			return domain.UserProfileSettings{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, 0)
		}
		existing = domain.UserProfileSettings{
			UserID:    userID,
			Revision:  1,
			Settings:  append(json.RawMessage(nil), settings...),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_profile_settings (user_id, revision, settings_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, existing.UserID, existing.Revision, string(settings), existing.CreatedAt, existing.UpdatedAt); err != nil {
			return domain.UserProfileSettings{}, err
		}
	default:
		return domain.UserProfileSettings{}, err
	}

	if err := txNotifyProfileChanged(ctx, tx, userID, "profile.settings_changed", existing.Revision); err != nil {
		return domain.UserProfileSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.UserProfileSettings{}, err
	}
	return existing, nil
}

func (s *Store) GetUserProfileSecretVault(ctx context.Context, userID string) (domain.UserProfileSecretVault, error) {
	row := s.queryRow(ctx, `SELECT user_id, revision, key_id, vault_json, created_at, updated_at
		FROM user_profile_secret_vaults
		WHERE user_id = ?`, userID)
	record, err := scanUserProfileSecretVault(row)
	if err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	decrypted, err := s.decryptJSONSecret(string(record.Vault))
	if err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	record.Vault = json.RawMessage(decrypted)
	return record, nil
}

func (s *Store) SaveUserProfileSecretVault(ctx context.Context, userID string, expectedRevision *int, keyID string, vault json.RawMessage) (domain.UserProfileSecretVault, error) {
	if len(vault) == 0 || !json.Valid(vault) {
		return domain.UserProfileSecretVault{}, fmt.Errorf("vault must be valid json")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	defer tx.Rollback()
	storedVault, err := s.encryptJSONSecret(vault)
	if err != nil {
		return domain.UserProfileSecretVault{}, err
	}

	existing, err := scanUserProfileSecretVault(tx.QueryRowContext(ctx, `SELECT user_id, revision, key_id, vault_json, created_at, updated_at
		FROM user_profile_secret_vaults
		WHERE user_id = ?
		FOR UPDATE`, userID))
	now := time.Now().UTC()
	switch err {
	case nil:
		if expectedRevision != nil && existing.Revision != *expectedRevision {
			return domain.UserProfileSecretVault{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, existing.Revision)
		}
		existing.Revision++
		existing.KeyID = keyID
		existing.Vault = append(json.RawMessage(nil), vault...)
		existing.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `UPDATE user_profile_secret_vaults
			SET revision = ?, key_id = ?, vault_json = ?, updated_at = ?
			WHERE user_id = ?`, existing.Revision, existing.KeyID, storedVault, existing.UpdatedAt, userID); err != nil {
			return domain.UserProfileSecretVault{}, err
		}
	case ErrNotFound:
		if expectedRevision != nil {
			return domain.UserProfileSecretVault{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, 0)
		}
		existing = domain.UserProfileSecretVault{
			UserID:    userID,
			Revision:  1,
			KeyID:     keyID,
			Vault:     append(json.RawMessage(nil), vault...),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_profile_secret_vaults (user_id, revision, key_id, vault_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, existing.UserID, existing.Revision, existing.KeyID, storedVault, existing.CreatedAt, existing.UpdatedAt); err != nil {
			return domain.UserProfileSecretVault{}, err
		}
	default:
		return domain.UserProfileSecretVault{}, err
	}

	if err := txNotifyProfileChanged(ctx, tx, userID, "profile.secret_vault_changed", existing.Revision); err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	existing.Vault = append(json.RawMessage(nil), vault...)
	return existing, nil
}

func scanUserProfileSettings(scanner interface{ Scan(dest ...any) error }) (domain.UserProfileSettings, error) {
	var record domain.UserProfileSettings
	var settings string
	err := scanner.Scan(&record.UserID, &record.Revision, &settings, &record.CreatedAt, &record.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.UserProfileSettings{}, ErrNotFound
	}
	if err != nil {
		return domain.UserProfileSettings{}, err
	}
	record.Settings = json.RawMessage(settings)
	return record, nil
}

func scanUserProfileSecretVault(scanner interface{ Scan(dest ...any) error }) (domain.UserProfileSecretVault, error) {
	var record domain.UserProfileSecretVault
	var vault string
	err := scanner.Scan(&record.UserID, &record.Revision, &record.KeyID, &vault, &record.CreatedAt, &record.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.UserProfileSecretVault{}, ErrNotFound
	}
	if err != nil {
		return domain.UserProfileSecretVault{}, err
	}
	record.Vault = json.RawMessage(vault)
	return record, nil
}
