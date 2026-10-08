package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestWebPushSubscriptionIsEncryptedAndSessionBound(t *testing.T) {
	t.Parallel()
	ctx, db, user, _, now := notificationStoreFixture(t, "push_encrypted")
	defer db.Close()
	if err := db.ConfigureSecretEncryption("web-push-test-key"); err != nil {
		t.Fatal(err)
	}
	session := domain.AppSession{ID: "sess_push_encrypted", UserID: user.ID, TokenHash: "push-encrypted-hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	want := domain.WebPushSubscription{ID: "wps_encrypted", UserID: user.ID, SessionID: session.ID, Endpoint: "https://push.example.test/send/secret-capability", P256DH: "public-key", Auth: "auth-secret", BrowserLabel: "Test browser"}
	got, err := db.UpsertWebPushSubscription(ctx, want)
	if err != nil {
		t.Fatalf("UpsertWebPushSubscription: %v", err)
	}
	if got.Endpoint != want.Endpoint || got.P256DH != want.P256DH || got.Auth != want.Auth || len(got.EndpointFingerprint) != 64 {
		t.Fatalf("subscription = %#v", got)
	}
	var stored string
	if err := db.queryRow(ctx, `SELECT encrypted_subscription FROM web_push_subscriptions WHERE id = ?`, got.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, encryptedSecretPrefix) || strings.Contains(stored, "secret-capability") || strings.Contains(stored, "auth-secret") {
		t.Fatalf("stored subscription is not an opaque encrypted envelope")
	}
	listed, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil || len(listed) != 1 || listed[0].Endpoint != want.Endpoint {
		t.Fatalf("ListWebPushSubscriptions = %#v, %v", listed, err)
	}
}

func TestWebPushSubscriptionRejectsActiveCrossSessionTransfer(t *testing.T) {
	t.Parallel()
	ctx, db, user, _, now := notificationStoreFixture(t, "push_transfer")
	defer db.Close()
	if err := db.ConfigureSecretEncryption("web-push-transfer-key"); err != nil {
		t.Fatal(err)
	}
	other := domain.User{ID: "usr_push_transfer_other", Email: "push-transfer-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	firstSession := domain.AppSession{ID: "sess_push_transfer_one", UserID: user.ID, TokenHash: "push-transfer-one", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	secondSession := domain.AppSession{ID: "sess_push_transfer_two", UserID: other.ID, TokenHash: "push-transfer-two", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateSession(ctx, firstSession); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, secondSession); err != nil {
		t.Fatal(err)
	}
	base := domain.WebPushSubscription{ID: "wps_transfer", UserID: user.ID, SessionID: firstSession.ID, Endpoint: "https://push.example.test/transfer", P256DH: "key", Auth: "auth"}
	if _, err := db.UpsertWebPushSubscription(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.ID, base.UserID, base.SessionID = "wps_transfer_other", other.ID, secondSession.ID
	if _, err := db.UpsertWebPushSubscription(ctx, base); !errors.Is(err, ErrConflict) {
		t.Fatalf("active transfer error = %v, want ErrConflict", err)
	}
	if err := db.RevokeSession(ctx, firstSession.ID); err != nil {
		t.Fatal(err)
	}
	transferred, err := db.UpsertWebPushSubscription(ctx, base)
	if err != nil || transferred.UserID != other.ID || transferred.SessionID != secondSession.ID {
		t.Fatalf("transferred = %#v, %v", transferred, err)
	}
}

func TestWebPushDeliveryClaimAndRetryLifecycle(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "push_delivery")
	defer db.Close()
	if err := db.ConfigureSecretEncryption("web-push-delivery-key"); err != nil {
		t.Fatal(err)
	}
	session := domain.AppSession{ID: "sess_push_delivery", UserID: user.ID, TokenHash: "push-delivery-hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	subscription, err := db.UpsertWebPushSubscription(ctx, domain.WebPushSubscription{ID: "wps_delivery", UserID: user.ID, SessionID: session.ID, Endpoint: "https://push.example.test/delivery", P256DH: "key", Auth: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_delivery", user.ID, home.ID, "event-delivery", "delivery", "down", now))
	if err != nil {
		t.Fatal(err)
	}
	if count, err := db.CreateWebPushDeliveriesForNotification(ctx, item.ID, []string{subscription.ID}, now); err != nil || count != 1 {
		t.Fatalf("CreateWebPushDeliveriesForNotification = %d, %v", count, err)
	}
	claimed, err := db.ClaimDueWebPushDeliveries(ctx, "worker-one", now, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].AttemptCount != 1 || claimed[0].State != "claimed" {
		t.Fatalf("claimed = %#v, %v", claimed, err)
	}
	if again, err := db.ClaimDueWebPushDeliveries(ctx, "worker-two", now, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("second claim = %#v, %v", again, err)
	}
	next := now.Add(time.Minute)
	if err := db.RetryWebPushDelivery(ctx, claimed[0].ID, "worker-one", next, "http_503", now); err != nil {
		t.Fatal(err)
	}
	if early, err := db.ClaimDueWebPushDeliveries(ctx, "worker-two", now, 10, time.Minute); err != nil || len(early) != 0 {
		t.Fatalf("early retry claim = %#v, %v", early, err)
	}
	retry, err := db.ClaimDueWebPushDeliveries(ctx, "worker-two", next, 10, time.Minute)
	if err != nil || len(retry) != 1 || retry[0].AttemptCount != 2 {
		t.Fatalf("retry claim = %#v, %v", retry, err)
	}
	if err := db.CompleteWebPushDelivery(ctx, retry[0].ID, "worker-two", next); err != nil {
		t.Fatal(err)
	}
}

func TestWebPushDeliveryClaimExpiresStaleBacklog(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "push_stale")
	defer db.Close()
	if err := db.ConfigureSecretEncryption("web-push-stale-key"); err != nil {
		t.Fatal(err)
	}
	session := domain.AppSession{ID: "sess_push_stale", UserID: user.ID, TokenHash: "push-stale-hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	subscription, err := db.UpsertWebPushSubscription(ctx, domain.WebPushSubscription{ID: "wps_stale", UserID: user.ID, SessionID: session.ID, Endpoint: "https://push.example.test/stale", P256DH: "key", Auth: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	occurredAt := now.Add(-domain.WebPushDeliveryMaxAge - time.Minute)
	item, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_stale", user.ID, home.ID, "event-stale", "stale", "down", occurredAt))
	if err != nil {
		t.Fatal(err)
	}
	if count, err := db.CreateWebPushDeliveriesForNotification(ctx, item.ID, []string{subscription.ID}, occurredAt); err != nil || count != 1 {
		t.Fatalf("CreateWebPushDeliveriesForNotification = %d, %v", count, err)
	}
	claimed, err := db.ClaimDueWebPushDeliveries(ctx, "worker-stale", now, 10, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("stale claim = %#v, %v", claimed, err)
	}
	var state, outcome string
	if err := db.DB().QueryRowContext(ctx, `SELECT state, last_outcome_code FROM web_push_deliveries WHERE notification_id = $1`, item.ID).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "terminal" || outcome != "delivery_expired" {
		t.Fatalf("stale delivery state=%q outcome=%q", state, outcome)
	}
}

func TestNotificationRetentionUsesLastOccurrence(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixture(t, "retention")
	defer db.Close()
	old := now.Add(-31 * 24 * time.Hour)
	recent := now.Add(-29 * 24 * time.Hour)
	if _, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_retention_old", user.ID, home.ID, "event-retention-old", "retention-old", "done", old)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateOrCoalesceUserNotification(ctx, notificationInput("ntf_retention_recent", user.ID, home.ID, "event-retention-recent", "retention-recent", "done", recent)); err != nil {
		t.Fatal(err)
	}
	summary, err := db.PruneLifecycleWithSummary(ctx, now, 7*24*time.Hour)
	if err != nil || summary.UserNotificationsDeleted != 1 {
		t.Fatalf("prune summary = %#v, %v", summary, err)
	}
	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "ntf_retention_recent" {
		t.Fatalf("remaining notifications = %#v, %v", page.Items, err)
	}
}
