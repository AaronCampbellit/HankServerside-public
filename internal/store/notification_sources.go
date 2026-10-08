package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) ObserveNotificationSource(ctx context.Context, observation domain.NotificationSourceObservation) (bool, error) {
	observation.SourceKey = strings.TrimSpace(observation.SourceKey)
	observation.HomeID = strings.TrimSpace(observation.HomeID)
	observation.ResourceID = strings.TrimSpace(observation.ResourceID)
	observation.State = strings.TrimSpace(observation.State)
	observation.EventKind = strings.TrimSpace(observation.EventKind)
	observation.Outcome = strings.TrimSpace(observation.Outcome)
	observation.Severity = strings.TrimSpace(observation.Severity)
	observation.DisplayName = strings.TrimSpace(observation.DisplayName)
	if observation.OccurredAt.IsZero() {
		observation.OccurredAt = time.Now().UTC()
	}
	if observation.SourceKey == "" || len(observation.SourceKey) > 512 || observation.HomeID == "" || observation.ResourceID == "" || len(observation.ResourceID) > 256 || observation.State == "" || len(observation.State) > 80 {
		return false, errors.New("invalid notification source observation")
	}
	if observation.EventKind != "" && (observation.Outcome == "" || observation.Severity == "" || len(observation.EventKind) > 80 || len(observation.Outcome) > 80 || len(observation.Severity) > 80 || len(observation.DisplayName) > 160) {
		return false, errors.New("invalid notification source event")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(observation.SourceKey))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, hex.EncodeToString(digest[:])); err != nil {
		return false, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT observed_state FROM notification_source_states WHERE source_key = ? FOR UPDATE`, observation.SourceKey).Scan(&previous)
	initial := errors.Is(err, sql.ErrNoRows)
	if err != nil && !initial {
		return false, err
	}
	if initial {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_source_states (source_key, home_id, resource_id, observed_state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, observation.SourceKey, observation.HomeID, observation.ResourceID, observation.State, observation.OccurredAt, observation.OccurredAt); err != nil {
			return false, err
		}
	} else if previous != observation.State {
		if _, err := tx.ExecContext(ctx, `UPDATE notification_source_states SET home_id = ?, resource_id = ?, observed_state = ?, updated_at = ? WHERE source_key = ?`,
			observation.HomeID, observation.ResourceID, observation.State, observation.OccurredAt, observation.SourceKey); err != nil {
			return false, err
		}
	}
	transitioned := (!initial && previous != observation.State) || (initial && observation.NotifyInitial)
	enqueueEvent := transitioned && observation.EventKind != ""
	if enqueueEvent && !initial && len(observation.AllowedPreviousStates) > 0 {
		enqueueEvent = false
		for _, allowed := range observation.AllowedPreviousStates {
			if strings.TrimSpace(allowed) == previous {
				enqueueEvent = true
				break
			}
		}
	}
	if enqueueEvent {
		eventID := newStoreID("nse")
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_source_events (
				id, event_key, source_key, home_id, resource_id, event_kind, outcome, severity, display_name, occurred_at, emitted_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`, eventID, "source-transition:"+eventID,
			observation.SourceKey, observation.HomeID, observation.ResourceID, observation.EventKind,
			observation.Outcome, observation.Severity, observation.DisplayName, observation.OccurredAt); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return transitioned, nil
}

func (s *Store) ListPendingNotificationSourceEvents(ctx context.Context, limit int) ([]domain.PendingNotificationSourceEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.query(ctx, `SELECT id, event_key, source_key, home_id, resource_id, event_kind, outcome, severity, display_name, occurred_at
		FROM notification_source_events WHERE emitted_at IS NULL ORDER BY occurred_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.PendingNotificationSourceEvent, 0)
	for rows.Next() {
		var item domain.PendingNotificationSourceEvent
		if err := rows.Scan(&item.ID, &item.EventKey, &item.SourceKey, &item.HomeID, &item.ResourceID, &item.EventKind, &item.Outcome, &item.Severity, &item.DisplayName, &item.OccurredAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MarkNotificationSourceEventEmitted(ctx context.Context, id string, emittedAt time.Time) error {
	result, err := s.exec(ctx, `UPDATE notification_source_events SET emitted_at = ? WHERE id = ? AND emitted_at IS NULL`, emittedAt, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
