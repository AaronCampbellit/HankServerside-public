package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) GetUserProfileBackup(ctx context.Context, userID string) (domain.UserProfileBackup, error) {
	row := s.queryRow(ctx, `SELECT user_id, revision, snapshot_json, created_at, updated_at
		FROM user_profile_backups
		WHERE user_id = ?`, userID)

	var backup domain.UserProfileBackup
	var snapshotJSON string
	if err := row.Scan(&backup.UserID, &backup.Revision, &snapshotJSON, &backup.CreatedAt, &backup.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.UserProfileBackup{}, ErrNotFound
		}
		return domain.UserProfileBackup{}, err
	}
	backup.Snapshot = json.RawMessage(snapshotJSON)
	return backup, nil
}

func (s *Store) SaveUserProfileBackup(
	ctx context.Context,
	userID string,
	expectedRevision *int,
	snapshot json.RawMessage,
) (domain.UserProfileBackup, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.UserProfileBackup{}, err
	}
	defer tx.Rollback()

	var existing domain.UserProfileBackup
	var snapshotJSON string
	row := tx.QueryRowContext(ctx, `SELECT user_id, revision, snapshot_json, created_at, updated_at
		FROM user_profile_backups
		WHERE user_id = ?`, userID)
	err = row.Scan(&existing.UserID, &existing.Revision, &snapshotJSON, &existing.CreatedAt, &existing.UpdatedAt)
	now := time.Now().UTC()

	switch err {
	case nil:
		if expectedRevision != nil && existing.Revision != *expectedRevision {
			return domain.UserProfileBackup{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, existing.Revision)
		}
		existing.Revision++
		existing.Snapshot = append(json.RawMessage(nil), snapshot...)
		existing.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `UPDATE user_profile_backups
			SET revision = ?, snapshot_json = ?, updated_at = ?
			WHERE user_id = ?`,
			existing.Revision,
			string(snapshot),
			existing.UpdatedAt,
			userID,
		); err != nil {
			return domain.UserProfileBackup{}, err
		}
	case sql.ErrNoRows:
		if expectedRevision != nil {
			return domain.UserProfileBackup{}, fmt.Errorf("%w: expected revision %d, current revision %d", ErrConflict, *expectedRevision, 0)
		}
		existing = domain.UserProfileBackup{
			UserID:    userID,
			Revision:  1,
			Snapshot:  append(json.RawMessage(nil), snapshot...),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_profile_backups (
			user_id, revision, snapshot_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`,
			existing.UserID,
			existing.Revision,
			string(snapshot),
			existing.CreatedAt,
			existing.UpdatedAt,
		); err != nil {
			return domain.UserProfileBackup{}, err
		}
	default:
		return domain.UserProfileBackup{}, err
	}

	if err := txNotifyProfileChanged(ctx, tx, userID, "profile.backup_changed", existing.Revision); err != nil {
		return domain.UserProfileBackup{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.UserProfileBackup{}, err
	}
	return existing, nil
}
