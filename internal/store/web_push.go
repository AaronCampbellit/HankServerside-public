package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

type webPushEnvelope struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func (s *Store) UpsertWebPushSubscription(ctx context.Context, subscription domain.WebPushSubscription) (domain.WebPushSubscription, error) {
	if s.secretBox == nil {
		return domain.WebPushSubscription{}, errors.New("secret encryption key is required for Web Push subscriptions")
	}
	subscription.ID = strings.TrimSpace(subscription.ID)
	subscription.UserID = strings.TrimSpace(subscription.UserID)
	subscription.SessionID = strings.TrimSpace(subscription.SessionID)
	subscription.Endpoint = strings.TrimSpace(subscription.Endpoint)
	subscription.P256DH = strings.TrimSpace(subscription.P256DH)
	subscription.Auth = strings.TrimSpace(subscription.Auth)
	subscription.BrowserLabel = strings.TrimSpace(subscription.BrowserLabel)
	if subscription.ID == "" || subscription.UserID == "" || subscription.SessionID == "" || subscription.Endpoint == "" || subscription.P256DH == "" || subscription.Auth == "" {
		return domain.WebPushSubscription{}, errors.New("invalid Web Push subscription")
	}
	session, err := s.GetSessionByID(ctx, subscription.SessionID)
	if err != nil {
		return domain.WebPushSubscription{}, err
	}
	if session.UserID != subscription.UserID {
		return domain.WebPushSubscription{}, ErrConflict
	}
	envelope, err := json.Marshal(webPushEnvelope{Endpoint: subscription.Endpoint, P256DH: subscription.P256DH, Auth: subscription.Auth})
	if err != nil {
		return domain.WebPushSubscription{}, err
	}
	encrypted, err := s.encryptSecret(string(envelope))
	if err != nil {
		return domain.WebPushSubscription{}, err
	}
	if !isEncryptedSecret(encrypted) {
		return domain.WebPushSubscription{}, errors.New("Web Push subscription encryption failed")
	}
	fingerprint := webPushEndpointFingerprint(subscription.Endpoint)
	now := time.Now().UTC()
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.WebPushSubscription{}, err
	}
	defer tx.Rollback()

	var existingID, existingUserID, existingSessionID string
	err = tx.QueryRowContext(ctx, `SELECT id, user_id, session_id FROM web_push_subscriptions WHERE endpoint_fingerprint = ? FOR UPDATE`, fingerprint).
		Scan(&existingID, &existingUserID, &existingSessionID)
	switch {
	case err == nil:
		if existingUserID != subscription.UserID || existingSessionID != subscription.SessionID {
			var active bool
			if err := tx.QueryRowContext(ctx, `SELECT revoked_at IS NULL AND expires_at > ? FROM app_sessions WHERE id = ?`, now, existingSessionID).Scan(&active); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return domain.WebPushSubscription{}, err
			}
			if active {
				return domain.WebPushSubscription{}, ErrConflict
			}
		}
		subscription.ID = existingID
		_, err = tx.ExecContext(ctx, `UPDATE web_push_subscriptions SET
			user_id = ?, session_id = ?, encrypted_subscription = ?, browser_label = ?,
			refreshed_at = ?, last_failure_at = NULL, last_failure_code = ''
			WHERE id = ?`, subscription.UserID, subscription.SessionID, encrypted, subscription.BrowserLabel, now, existingID)
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO web_push_subscriptions (
			id, user_id, session_id, endpoint_fingerprint, encrypted_subscription, browser_label,
			created_at, refreshed_at, last_success_at, last_failure_at, last_failure_code
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, '')`,
			subscription.ID, subscription.UserID, subscription.SessionID, fingerprint, encrypted, subscription.BrowserLabel, now, now)
	default:
		return domain.WebPushSubscription{}, err
	}
	if err != nil {
		return domain.WebPushSubscription{}, mapWebPushStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.WebPushSubscription{}, mapWebPushStoreError(err)
	}
	subscription.EndpointFingerprint = fingerprint
	if subscription.CreatedAt.IsZero() {
		subscription.CreatedAt = now
	}
	subscription.RefreshedAt = now
	return subscription, nil
}

func (s *Store) ListWebPushSubscriptions(ctx context.Context, userID string) ([]domain.WebPushSubscription, error) {
	rows, err := s.query(ctx, `SELECT w.id, w.user_id, w.session_id, w.endpoint_fingerprint,
		w.encrypted_subscription, w.browser_label, w.created_at, w.refreshed_at,
		w.last_success_at, w.last_failure_at, w.last_failure_code
		FROM web_push_subscriptions w
		JOIN app_sessions session ON session.id = w.session_id
		WHERE w.user_id = ? AND session.revoked_at IS NULL AND session.expires_at > ?
		ORDER BY w.refreshed_at DESC`, strings.TrimSpace(userID), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subscriptions []domain.WebPushSubscription
	for rows.Next() {
		subscription, encrypted, err := scanWebPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		if err := s.decryptWebPushEnvelope(&subscription, encrypted); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

func (s *Store) ListActiveWebPushSubscriptionsForUsers(ctx context.Context, userIDs []string) ([]domain.WebPushSubscription, error) {
	userIDs = cleanUniqueStrings(userIDs)
	if len(userIDs) == 0 {
		return nil, nil
	}
	query := `SELECT w.id, w.user_id, w.session_id, w.endpoint_fingerprint,
		w.encrypted_subscription, w.browser_label, w.created_at, w.refreshed_at,
		w.last_success_at, w.last_failure_at, w.last_failure_code
		FROM web_push_subscriptions w
		JOIN app_sessions session ON session.id = w.session_id
		WHERE w.user_id IN (` + placeholders(len(userIDs)) + `)
			AND session.revoked_at IS NULL AND session.expires_at > ?
		ORDER BY w.refreshed_at DESC`
	args := make([]any, 0, len(userIDs)+1)
	for _, userID := range userIDs {
		args = append(args, userID)
	}
	args = append(args, time.Now().UTC())
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subscriptions []domain.WebPushSubscription
	for rows.Next() {
		subscription, encrypted, err := scanWebPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		if err := s.decryptWebPushEnvelope(&subscription, encrypted); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

func (s *Store) DeleteWebPushSubscription(ctx context.Context, userID, subscriptionID string) error {
	result, err := s.exec(ctx, `DELETE FROM web_push_subscriptions WHERE id = ? AND user_id = ?`, subscriptionID, userID)
	return requireAffected(result, err)
}

func (s *Store) DeleteWebPushSubscriptionsForSession(ctx context.Context, sessionID string) error {
	_, err := s.exec(ctx, `DELETE FROM web_push_subscriptions WHERE session_id = ?`, sessionID)
	return err
}

func (s *Store) InvalidateWebPushSubscription(ctx context.Context, subscriptionID string) error {
	_, err := s.exec(ctx, `DELETE FROM web_push_subscriptions WHERE id = ?`, subscriptionID)
	return err
}

func (s *Store) CreateWebPushDeliveriesForNotification(ctx context.Context, notificationID string, subscriptionIDs []string, now time.Time) (int, error) {
	subscriptionIDs = cleanUniqueStrings(subscriptionIDs)
	created := 0
	for _, subscriptionID := range subscriptionIDs {
		result, err := s.exec(ctx, `INSERT INTO web_push_deliveries (
			id, notification_id, subscription_id, state, attempt_count, next_attempt_at,
			claim_owner, claim_expires_at, last_outcome_code, created_at, updated_at, delivered_at
		) VALUES (?, ?, ?, 'pending', 0, ?, '', NULL, '', ?, ?, NULL)
		ON CONFLICT(notification_id, subscription_id) DO NOTHING`,
			newStoreID("wpd"), notificationID, subscriptionID, now, now, now)
		if err != nil {
			return created, err
		}
		count, _ := result.RowsAffected()
		created += int(count)
	}
	return created, nil
}

func (s *Store) ClaimDueWebPushDeliveries(ctx context.Context, owner string, now time.Time, limit int, claimTTL time.Duration) ([]domain.WebPushDelivery, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("claim owner is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if claimTTL <= 0 {
		claimTTL = time.Minute
	}
	oldestDelivery := now.Add(-domain.WebPushDeliveryMaxAge)
	_, err := s.exec(ctx, `UPDATE web_push_deliveries SET state = 'terminal', claim_owner = '', claim_expires_at = NULL,
		last_outcome_code = CASE WHEN attempt_count >= 6 THEN 'attempt_limit' ELSE 'delivery_expired' END, updated_at = ?
		WHERE state IN ('pending', 'claimed') AND (attempt_count >= 6 OR created_at < ?)`, now, oldestDelivery)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `WITH due AS (
		SELECT id FROM web_push_deliveries
		WHERE attempt_count < 6 AND created_at >= ? AND (
			(state = 'pending' AND next_attempt_at <= ?)
			OR (state = 'claimed' AND claim_expires_at <= ?)
		)
		ORDER BY next_attempt_at ASC, id ASC
		LIMIT ? FOR UPDATE SKIP LOCKED
	)
	UPDATE web_push_deliveries delivery SET
		state = 'claimed', attempt_count = delivery.attempt_count + 1,
		claim_owner = ?, claim_expires_at = ?, updated_at = ?
	FROM due WHERE delivery.id = due.id
	RETURNING delivery.id, delivery.notification_id, delivery.subscription_id, delivery.state,
		delivery.attempt_count, delivery.next_attempt_at, delivery.claim_owner,
		delivery.claim_expires_at, delivery.last_outcome_code, delivery.created_at,
		delivery.updated_at, delivery.delivered_at`,
		oldestDelivery, now, now, limit, owner, now.Add(claimTTL), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []domain.WebPushDelivery
	for rows.Next() {
		delivery, err := scanWebPushDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Store) CompleteWebPushDelivery(ctx context.Context, deliveryID, owner string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE web_push_deliveries SET state = 'delivered', delivered_at = ?,
		claim_owner = '', claim_expires_at = NULL, last_outcome_code = 'delivered', updated_at = ?
		WHERE id = ? AND state = 'claimed' AND claim_owner = ?`, now, now, deliveryID, owner)
	return requireAffected(result, err)
}

func (s *Store) RetryWebPushDelivery(ctx context.Context, deliveryID, owner string, nextAttempt time.Time, outcome string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE web_push_deliveries SET
		state = CASE WHEN attempt_count >= 6 OR created_at < ? THEN 'terminal' ELSE 'pending' END,
		next_attempt_at = ?, claim_owner = '', claim_expires_at = NULL,
		last_outcome_code = ?, updated_at = ?
		WHERE id = ? AND state = 'claimed' AND claim_owner = ?`,
		now.Add(-domain.WebPushDeliveryMaxAge), nextAttempt, boundedOutcome(outcome), now, deliveryID, owner)
	return requireAffected(result, err)
}

func (s *Store) FailWebPushDelivery(ctx context.Context, deliveryID, owner, outcome string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE web_push_deliveries SET state = 'terminal', claim_owner = '',
		claim_expires_at = NULL, last_outcome_code = ?, updated_at = ?
		WHERE id = ? AND state = 'claimed' AND claim_owner = ?`, boundedOutcome(outcome), now, deliveryID, owner)
	return requireAffected(result, err)
}

func (s *Store) CancelWebPushDelivery(ctx context.Context, deliveryID, owner, outcome string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE web_push_deliveries SET state = 'cancelled', claim_owner = '',
		claim_expires_at = NULL, last_outcome_code = ?, updated_at = ?
		WHERE id = ? AND state = 'claimed' AND claim_owner = ?`, boundedOutcome(outcome), now, deliveryID, owner)
	return requireAffected(result, err)
}

func (s *Store) GetWebPushDeliveryContext(ctx context.Context, deliveryID string, now time.Time) (domain.WebPushDeliveryContext, error) {
	row := s.queryRow(ctx, `SELECT
		d.id, d.notification_id, d.subscription_id, d.state, d.attempt_count, d.next_attempt_at,
		d.claim_owner, d.claim_expires_at, d.last_outcome_code, d.created_at, d.updated_at, d.delivered_at,
		n.id, n.user_id, n.home_id, n.category, n.event_kind, n.severity, n.title, n.body,
		n.target_path, n.collapse_key, n.outcome, n.occurrence_count, n.first_occurred_at,
		n.last_occurred_at, n.read_at, n.created_at,
		w.id, w.user_id, w.session_id, w.endpoint_fingerprint, w.encrypted_subscription,
		w.browser_label, w.created_at, w.refreshed_at, w.last_success_at, w.last_failure_at,
		w.last_failure_code,
		(session.revoked_at IS NULL AND session.expires_at > ?),
		CASE n.category
			WHEN 'agent_health' THEN COALESCE(settings.agent_health_enabled, TRUE)
			WHEN 'quick_links' THEN COALESCE(settings.quick_links_enabled, TRUE)
			WHEN 'storage' THEN COALESCE(settings.storage_enabled, TRUE)
			WHEN 'notes' THEN COALESCE(settings.notes_enabled, TRUE)
			WHEN 'dashboard_entities' THEN COALESCE(settings.dashboard_entities_enabled, TRUE)
			ELSE FALSE
		END
		FROM web_push_deliveries d
		JOIN user_notifications n ON n.id = d.notification_id
		JOIN web_push_subscriptions w ON w.id = d.subscription_id AND w.user_id = n.user_id
		JOIN app_sessions session ON session.id = w.session_id AND session.user_id = w.user_id
		LEFT JOIN notification_settings settings ON settings.user_id = n.user_id
		WHERE d.id = ?`, now, deliveryID)
	var result domain.WebPushDeliveryContext
	var encrypted string
	err := row.Scan(
		&result.Delivery.ID, &result.Delivery.NotificationID, &result.Delivery.SubscriptionID,
		&result.Delivery.State, &result.Delivery.AttemptCount, &result.Delivery.NextAttemptAt,
		&result.Delivery.ClaimOwner, &result.Delivery.ClaimExpiresAt, &result.Delivery.LastOutcomeCode,
		&result.Delivery.CreatedAt, &result.Delivery.UpdatedAt, &result.Delivery.DeliveredAt,
		&result.Notification.ID, &result.Notification.UserID, &result.Notification.HomeID,
		&result.Notification.Category, &result.Notification.EventKind, &result.Notification.Severity,
		&result.Notification.Title, &result.Notification.Body, &result.Notification.TargetPath,
		&result.Notification.CollapseKey, &result.Notification.Outcome, &result.Notification.OccurrenceCount,
		&result.Notification.FirstOccurredAt, &result.Notification.LastOccurredAt, &result.Notification.ReadAt,
		&result.Notification.CreatedAt,
		&result.Subscription.ID, &result.Subscription.UserID, &result.Subscription.SessionID,
		&result.Subscription.EndpointFingerprint, &encrypted, &result.Subscription.BrowserLabel,
		&result.Subscription.CreatedAt, &result.Subscription.RefreshedAt, &result.Subscription.LastSuccessAt,
		&result.Subscription.LastFailureAt, &result.Subscription.LastFailureCode,
		&result.SessionActive, &result.CategoryEnabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WebPushDeliveryContext{}, ErrNotFound
	}
	if err != nil {
		return domain.WebPushDeliveryContext{}, err
	}
	if err := s.decryptWebPushEnvelope(&result.Subscription, encrypted); err != nil {
		return domain.WebPushDeliveryContext{}, err
	}
	return result, nil
}

func (s *Store) RecordWebPushSubscriptionSuccess(ctx context.Context, subscriptionID string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE web_push_subscriptions SET last_success_at = ?, last_failure_at = NULL, last_failure_code = '' WHERE id = ?`, now, subscriptionID)
	return err
}

func (s *Store) RecordWebPushSubscriptionFailure(ctx context.Context, subscriptionID, outcome string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE web_push_subscriptions SET last_failure_at = ?, last_failure_code = ? WHERE id = ?`, now, boundedOutcome(outcome), subscriptionID)
	return err
}

func scanWebPushSubscription(scanner interface{ Scan(dest ...any) error }) (domain.WebPushSubscription, string, error) {
	var subscription domain.WebPushSubscription
	var encrypted string
	err := scanner.Scan(&subscription.ID, &subscription.UserID, &subscription.SessionID,
		&subscription.EndpointFingerprint, &encrypted, &subscription.BrowserLabel,
		&subscription.CreatedAt, &subscription.RefreshedAt, &subscription.LastSuccessAt,
		&subscription.LastFailureAt, &subscription.LastFailureCode)
	return subscription, encrypted, err
}

func (s *Store) decryptWebPushEnvelope(subscription *domain.WebPushSubscription, encrypted string) error {
	plaintext, err := s.decryptSecret(encrypted)
	if err != nil {
		return err
	}
	var envelope webPushEnvelope
	if err := json.Unmarshal([]byte(plaintext), &envelope); err != nil {
		return err
	}
	subscription.Endpoint = envelope.Endpoint
	subscription.P256DH = envelope.P256DH
	subscription.Auth = envelope.Auth
	return nil
}

func scanWebPushDelivery(scanner interface{ Scan(dest ...any) error }) (domain.WebPushDelivery, error) {
	var delivery domain.WebPushDelivery
	err := scanner.Scan(&delivery.ID, &delivery.NotificationID, &delivery.SubscriptionID, &delivery.State,
		&delivery.AttemptCount, &delivery.NextAttemptAt, &delivery.ClaimOwner, &delivery.ClaimExpiresAt,
		&delivery.LastOutcomeCode, &delivery.CreatedAt, &delivery.UpdatedAt, &delivery.DeliveredAt)
	return delivery, err
}

func webPushEndpointFingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return hex.EncodeToString(sum[:])
}

func newStoreID(prefix string) string {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(random)
}

func boundedOutcome(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 80 {
		return value[:80]
	}
	return value
}

func mapWebPushStoreError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
		return ErrConflict
	}
	return err
}
