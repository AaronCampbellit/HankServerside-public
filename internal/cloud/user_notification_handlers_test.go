package cloud

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestUserNotificationAndWebPushHandlers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	must(t, db.ConfigureSecretEncryption("handler-web-push-key"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	user := domain.User{ID: "usr_notification_handler_" + suffix, Email: "notification-handler+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notification_handler_" + suffix, UserID: user.ID, Name: "Notification Handler", CreatedAt: now, UpdatedAt: now}
	rawToken := "notification-handler-token-" + suffix
	session := domain.AppSession{ID: "sess_notification_handler_" + suffix, UserID: user.ID, TokenHash: hashToken(rawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	other := domain.User{ID: "usr_notification_handler_other_" + suffix, Email: "notification-handler-other+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	otherRawToken := "notification-handler-other-token-" + suffix
	otherSession := domain.AppSession{ID: "sess_notification_handler_other_" + suffix, UserID: other.ID, TokenHash: hashToken(otherRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateUser(ctx, other))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	must(t, db.CreateSession(ctx, otherSession))
	auditEvent := store.AuditEvent{ID: "audit_notification_handler_" + suffix, OccurredAt: now, HomeID: nullableString(home.ID), EventType: notificationKindAgentOffline, Severity: auditSeverityWarning, TargetType: "agent", TargetID: "one", MetadataJSON: `{"token":"must-not-leak","reason":"offline"}`}
	must(t, db.CreateAuditEvent(ctx, auditEvent))
	_, _, err := db.CreateOrCoalesceUserNotification(ctx, domain.CreateUserNotificationInput{EventKey: "handler-event", Notification: domain.UserNotification{ID: "ntf_handler_" + suffix, UserID: user.ID, HomeID: home.ID, AuditEventID: auditEvent.ID, Category: domain.NotificationCategoryAgentHealth, EventKind: notificationKindAgentOffline, Severity: "warning", Title: "Agent offline", Body: "A Hank Agent is offline.", TargetPath: "/dashboard/agents/one", CollapseKey: "agent-one", Outcome: "offline", FirstOccurredAt: now, LastOccurredAt: now, CreatedAt: now}})
	must(t, err)

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	must(t, server.ConfigureWebPush(WebPushConfig{Enabled: true, PublicKey: "vapid-public", PrivateKey: "vapid-private", Subject: "mailto:operator@example.com"}))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var inbox struct {
		Notifications []domain.UserNotification `json:"notifications"`
		UnreadCount   int                       `json:"unread_count"`
		NextCursor    string                    `json:"next_cursor"`
	}
	requestJSON(t, testServer, rawToken, http.MethodGet, "/v1/me/notifications?limit=1&unread=true", nil, &inbox)
	if len(inbox.Notifications) != 1 || inbox.UnreadCount != 1 || inbox.Notifications[0].ID == "" {
		t.Fatalf("inbox = %#v", inbox)
	}
	var eventPayload struct {
		Event map[string]any `json:"event"`
	}
	requestJSON(t, testServer, rawToken, http.MethodGet, "/v1/me/notifications/"+inbox.Notifications[0].ID+"/event", nil, &eventPayload)
	if eventPayload.Event["id"] != auditEvent.ID || eventPayload.Event["event_type"] != notificationKindAgentOffline {
		t.Fatalf("notification event = %#v", eventPayload.Event)
	}
	metadata, _ := eventPayload.Event["metadata"].(map[string]any)
	if metadata["token"] != "[redacted]" || metadata["reason"] != "offline" {
		t.Fatalf("notification event metadata = %#v", metadata)
	}
	response := requestJSONStatus(t, testServer, otherRawToken, http.MethodGet, "/v1/me/notifications/"+inbox.Notifications[0].ID+"/event", nil, http.StatusNotFound)
	response.Body.Close()
	requestJSON(t, testServer, rawToken, http.MethodPut, "/v1/me/notifications/"+inbox.Notifications[0].ID+"/read", nil, nil)
	requestJSON(t, testServer, rawToken, http.MethodPut, "/v1/me/notifications/read-all", nil, nil)

	var settings struct {
		AgentHealth bool `json:"agent_health"`
		QuickLinks  bool `json:"quick_links"`
		Storage     bool `json:"storage"`
	}
	requestJSON(t, testServer, rawToken, http.MethodPut, "/v1/me/notification-settings", map[string]any{"agent_health": false, "quick_links": true, "storage": false}, &settings)
	if settings.AgentHealth || !settings.QuickLinks || settings.Storage {
		t.Fatalf("settings = %#v", settings)
	}

	var config struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	requestJSON(t, testServer, rawToken, http.MethodGet, "/v1/me/web-push/config", nil, &config)
	if !config.Enabled || config.PublicKey != "vapid-public" {
		t.Fatalf("config = %#v", config)
	}
	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	p256dh := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y))
	auth := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	var registered struct {
		Subscription domain.WebPushSubscription `json:"subscription"`
	}
	requestJSON(t, testServer, rawToken, http.MethodPost, "/v1/me/web-push/subscriptions", map[string]any{"endpoint": "https://push.example.com/send/handler", "keys": map[string]string{"p256dh": p256dh, "auth": auth}, "browser_label": "Test browser"}, &registered)
	if registered.Subscription.ID == "" || registered.Subscription.Endpoint != "" || registered.Subscription.P256DH != "" || registered.Subscription.Auth != "" {
		t.Fatalf("registration response leaks or omits fields: %#v", registered)
	}
	requestJSON(t, testServer, rawToken, http.MethodDelete, "/v1/me/web-push/subscriptions/"+registered.Subscription.ID, nil, nil)
	if subscriptions, err := db.ListWebPushSubscriptions(ctx, user.ID); err != nil || len(subscriptions) != 0 {
		t.Fatalf("subscriptions after delete = %#v, %v", subscriptions, err)
	}

	response = requestJSONStatus(t, testServer, "", http.MethodGet, "/v1/me/notifications", nil, http.StatusUnauthorized)
	response.Body.Close()
	response = requestJSONStatus(t, testServer, rawToken, http.MethodGet, "/v1/me/notifications?cursor=invalid", nil, http.StatusBadRequest)
	response.Body.Close()
	requestJSON(t, testServer, rawToken, http.MethodDelete, "/v1/me/notifications", nil, nil)
}

func TestLogoutDeletesSessionWebPushSubscriptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	must(t, db.ConfigureSecretEncryption("logout-web-push-key"))
	now := time.Now().UTC()
	user := domain.User{ID: "usr_web_push_logout", Email: "web-push-logout@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	rawToken := "web-push-logout-token"
	session := domain.AppSession{ID: "sess_web_push_logout", UserID: user.ID, TokenHash: hashToken(rawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))
	_, err := db.UpsertWebPushSubscription(ctx, domain.WebPushSubscription{ID: "wps_logout", UserID: user.ID, SessionID: session.ID, Endpoint: "https://push.example.com/send/logout", P256DH: "key", Auth: "auth"})
	must(t, err)
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()
	requestJSON(t, testServer, rawToken, http.MethodPost, "/v1/auth/logout", nil, nil)
	var count int
	must(t, db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM web_push_subscriptions WHERE session_id = $1`, session.ID).Scan(&count))
	if count != 0 {
		t.Fatalf("session subscriptions after logout = %d", count)
	}
}
