package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

var ErrInvalidCursor = errors.New("invalid cursor")

const userNotificationColumns = `id, user_id, home_id, audit_event_id, category, event_kind, severity,
		title, body, target_path, collapse_key, outcome, occurrence_count,
		first_occurred_at, last_occurred_at, read_at, created_at`

type notificationCursor struct {
	OccurredAt string `json:"occurred_at"`
	ID         string `json:"id"`
}

func (s *Store) CreateOrCoalesceUserNotification(ctx context.Context, input domain.CreateUserNotificationInput) (domain.UserNotification, bool, error) {
	item := input.Notification
	input.EventKey = strings.TrimSpace(input.EventKey)
	item.ID = strings.TrimSpace(item.ID)
	item.UserID = strings.TrimSpace(item.UserID)
	item.HomeID = strings.TrimSpace(item.HomeID)
	item.AuditEventID = strings.TrimSpace(item.AuditEventID)
	item.Category = strings.TrimSpace(item.Category)
	item.EventKind = strings.TrimSpace(item.EventKind)
	item.Severity = strings.TrimSpace(item.Severity)
	item.Title = strings.TrimSpace(item.Title)
	item.Body = strings.TrimSpace(item.Body)
	item.TargetPath = strings.TrimSpace(item.TargetPath)
	item.CollapseKey = strings.TrimSpace(item.CollapseKey)
	item.Outcome = strings.TrimSpace(item.Outcome)
	if item.ID == "" || item.UserID == "" || item.HomeID == "" || input.EventKey == "" || !domain.ValidNotificationCategory(item.Category) {
		return domain.UserNotification{}, false, errors.New("invalid user notification")
	}
	if item.FirstOccurredAt.IsZero() {
		item.FirstOccurredAt = time.Now().UTC()
	}
	if item.LastOccurredAt.IsZero() {
		item.LastOccurredAt = item.FirstOccurredAt
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = item.FirstOccurredAt
	}
	if item.OccurrenceCount <= 0 {
		item.OccurrenceCount = 1
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return domain.UserNotification{}, false, err
	}
	defer tx.Rollback()

	lockDigest := sha256.Sum256([]byte(item.UserID + "\x00" + item.CollapseKey + "\x00" + item.Outcome))
	lockKey := hex.EncodeToString(lockDigest[:])
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, lockKey); err != nil {
		return domain.UserNotification{}, false, err
	}
	if existing, err := notificationForEventReceipt(ctx, tx, item.UserID, input.EventKey); err == nil {
		if err := tx.Commit(); err != nil {
			return domain.UserNotification{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.UserNotification{}, false, err
	}

	coalesceAfter := item.LastOccurredAt.Add(-5 * time.Minute)
	coalesceBefore := item.LastOccurredAt.Add(5 * time.Minute)
	row := tx.QueryRowContext(ctx, `SELECT `+userNotificationColumns+`
		FROM user_notifications
		WHERE user_id = ? AND category = ? AND event_kind = ? AND collapse_key = ? AND outcome = ? AND read_at IS NULL
			AND last_occurred_at >= ? AND last_occurred_at <= ?
		ORDER BY last_occurred_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, item.UserID, item.Category, item.EventKind, item.CollapseKey, item.Outcome, coalesceAfter, coalesceBefore)
	existing, err := scanUserNotification(row)
	if err == nil {
		row = tx.QueryRowContext(ctx, `UPDATE user_notifications SET
			audit_event_id = COALESCE(?, audit_event_id), event_kind = ?, severity = ?, title = ?, body = ?, target_path = ?,
			occurrence_count = occurrence_count + 1,
			last_occurred_at = GREATEST(last_occurred_at, ?)
			WHERE id = ?
			RETURNING `+userNotificationColumns,
			nullableNotificationAuditID(item.AuditEventID), item.EventKind, item.Severity, item.Title, item.Body, item.TargetPath,
			item.LastOccurredAt, existing.ID)
		existing, err = scanUserNotification(row)
		if err != nil {
			return domain.UserNotification{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_event_receipts (user_id, event_key, notification_id, created_at)
			VALUES (?, ?, ?, ?)`, item.UserID, input.EventKey, existing.ID, item.CreatedAt); err != nil {
			return domain.UserNotification{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.UserNotification{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.UserNotification{}, false, err
	}

	row = tx.QueryRowContext(ctx, `INSERT INTO user_notifications (
			id, user_id, home_id, audit_event_id, category, event_kind, severity, title, body, target_path,
			collapse_key, outcome, occurrence_count, first_occurred_at, last_occurred_at, read_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+userNotificationColumns,
		item.ID, item.UserID, item.HomeID, nullableNotificationAuditID(item.AuditEventID), item.Category, item.EventKind, item.Severity,
		item.Title, item.Body, item.TargetPath, item.CollapseKey, item.Outcome,
		item.OccurrenceCount, item.FirstOccurredAt, item.LastOccurredAt, item.ReadAt, item.CreatedAt)
	created, err := scanUserNotification(row)
	if err != nil {
		return domain.UserNotification{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_event_receipts (user_id, event_key, notification_id, created_at)
		VALUES (?, ?, ?, ?)`, item.UserID, input.EventKey, created.ID, item.CreatedAt); err != nil {
		return domain.UserNotification{}, false, err
	}
	for _, subscriptionID := range cleanUniqueStrings(input.DeliverySubscriptionIDs) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO web_push_deliveries (
			id, notification_id, subscription_id, state, attempt_count, next_attempt_at,
			claim_owner, claim_expires_at, last_outcome_code, created_at, updated_at, delivered_at
		) VALUES (?, ?, ?, 'pending', 0, ?, '', NULL, '', ?, ?, NULL)
		ON CONFLICT(notification_id, subscription_id) DO NOTHING`,
			newStoreID("wpd"), created.ID, subscriptionID, item.CreatedAt, item.CreatedAt, item.CreatedAt); err != nil {
			return domain.UserNotification{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.UserNotification{}, false, err
	}
	return created, true, nil
}

func notificationForEventReceipt(ctx context.Context, tx *dbTx, userID, eventKey string) (domain.UserNotification, error) {
	return scanUserNotification(tx.QueryRowContext(ctx, `SELECT `+prefixedNotificationColumns("n")+`
		FROM notification_event_receipts r
		JOIN user_notifications n ON n.id = r.notification_id
		WHERE r.user_id = ? AND r.event_key = ?`, userID, eventKey))
}

func prefixedNotificationColumns(prefix string) string {
	parts := strings.Split(userNotificationColumns, ",")
	for index, part := range parts {
		parts[index] = prefix + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

func (s *Store) ListUserNotifications(ctx context.Context, userID string, opts domain.NotificationListOptions) (domain.NotificationPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT ` + userNotificationColumns + ` FROM user_notifications WHERE user_id = ?`
	args := []any{strings.TrimSpace(userID)}
	if opts.Unread {
		query += ` AND read_at IS NULL`
	}
	if strings.TrimSpace(opts.Cursor) != "" {
		occurredAt, id, err := decodeNotificationCursor(opts.Cursor)
		if err != nil {
			return domain.NotificationPage{}, err
		}
		query += ` AND (last_occurred_at, id) < (?, ?)`
		args = append(args, occurredAt, id)
	}
	query += ` ORDER BY last_occurred_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return domain.NotificationPage{}, err
	}
	defer rows.Close()
	items := make([]domain.UserNotification, 0, limit+1)
	for rows.Next() {
		item, err := scanUserNotification(rows)
		if err != nil {
			return domain.NotificationPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.NotificationPage{}, err
	}
	unread, err := s.CountUnreadUserNotifications(ctx, userID)
	if err != nil {
		return domain.NotificationPage{}, err
	}
	page := domain.NotificationPage{Items: items, UnreadCount: unread}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor, err = encodeNotificationCursor(page.Items[len(page.Items)-1])
		if err != nil {
			return domain.NotificationPage{}, err
		}
	}
	return page, nil
}

func (s *Store) CountUnreadUserNotifications(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM user_notifications WHERE user_id = ? AND read_at IS NULL`, strings.TrimSpace(userID)).Scan(&count)
	return count, err
}

func (s *Store) MarkUserNotificationRead(ctx context.Context, userID, notificationID string, readAt time.Time) error {
	result, err := s.exec(ctx, `UPDATE user_notifications SET read_at = COALESCE(read_at, ?) WHERE id = ? AND user_id = ?`, readAt, notificationID, userID)
	return requireAffected(result, err)
}

func (s *Store) MarkAllUserNotificationsRead(ctx context.Context, userID string, readAt time.Time) (int64, error) {
	result, err := s.exec(ctx, `UPDATE user_notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, readAt, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeleteUserNotification(ctx context.Context, userID, notificationID string) error {
	result, err := s.exec(ctx, `DELETE FROM user_notifications WHERE id = ? AND user_id = ?`, notificationID, userID)
	return requireAffected(result, err)
}

func (s *Store) DeleteAllUserNotifications(ctx context.Context, userID string) (int64, error) {
	result, err := s.exec(ctx, `DELETE FROM user_notifications WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func requireAffected(result sql.Result, err error) error {
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

func scanUserNotification(scanner interface{ Scan(dest ...any) error }) (domain.UserNotification, error) {
	var item domain.UserNotification
	var auditEventID sql.NullString
	err := scanner.Scan(
		&item.ID, &item.UserID, &item.HomeID, &auditEventID, &item.Category, &item.EventKind, &item.Severity,
		&item.Title, &item.Body, &item.TargetPath, &item.CollapseKey, &item.Outcome,
		&item.OccurrenceCount, &item.FirstOccurredAt, &item.LastOccurredAt, &item.ReadAt, &item.CreatedAt,
	)
	item.AuditEventID = auditEventID.String
	return item, err
}

func nullableNotificationAuditID(value string) any {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return nil
}

func encodeNotificationCursor(item domain.UserNotification) (string, error) {
	payload, err := json.Marshal(notificationCursor{OccurredAt: item.LastOccurredAt.UTC().Format(time.RFC3339Nano), ID: item.ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeNotificationCursor(value string) (time.Time, string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	var cursor notificationCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || strings.TrimSpace(cursor.ID) == "" {
		return time.Time{}, "", ErrInvalidCursor
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, cursor.OccurredAt)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: occurred_at", ErrInvalidCursor)
	}
	return occurredAt, cursor.ID, nil
}
