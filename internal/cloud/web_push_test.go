package cloud

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestValidateWebPushEndpointRejectsSSRFTargets(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"http://push.example.com/send/1",
		"https://user:password@push.example.com/send/1",
		"https://127.0.0.1/send/1",
		"https://10.1.2.3/send/1",
		"https://[::1]/send/1",
		"https://push.example.com/send/1#fragment",
	} {
		if err := validateWebPushEndpoint(endpoint); err == nil {
			t.Errorf("validateWebPushEndpoint(%q) succeeded", endpoint)
		}
	}
	if err := validateWebPushEndpoint("https://push.example.com/send/capability"); err != nil {
		t.Fatalf("valid endpoint: %v", err)
	}
}

func TestWebPushHTTPClientDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()
	client := newWebPushHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("transport = %#v, want explicit no-proxy transport", client.Transport)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://push.example.com/next", nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect error = %v", err)
	}
}

func TestWebPushSubscriberNormalizesMailtoForPinnedLibrary(t *testing.T) {
	t.Parallel()
	if got := webPushSubscriber("mailto:operator@example.com"); got != "operator@example.com" {
		t.Fatalf("webPushSubscriber(mailto) = %q", got)
	}
	if got := webPushSubscriber("https://hank.example/contact"); got != "https://hank.example/contact" {
		t.Fatalf("webPushSubscriber(https) = %q", got)
	}
}

func TestStandardsWebPushSenderGeneratesSingleMailtoVAPIDSubject(t *testing.T) {
	t.Parallel()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	browserKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	transport := &captureWebPushTransport{}
	sender := &standardsWebPushSender{client: &http.Client{Transport: transport}}
	_, err = sender.Send(context.Background(), domain.WebPushSubscription{
		Endpoint: "https://push.example.test/send/one",
		P256DH:   base64.RawURLEncoding.EncodeToString(browserKey.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}, []byte(`{"schema_version":1}`), "test-topic", WebPushConfig{
		Enabled: true, PublicKey: publicKey, PrivateKey: privateKey, Subject: "mailto:operator@example.com",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	authorization := transport.request.Header.Get("Authorization")
	token := ""
	for _, part := range strings.Split(strings.TrimPrefix(authorization, "vapid "), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == "t" {
			token = value
		}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("Authorization token = %q", authorization)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "mailto:operator@example.com" {
		t.Fatalf("VAPID sub = %q", claims.Subject)
	}
}

type captureWebPushTransport struct{ request *http.Request }

func (t *captureWebPushTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.request = request.Clone(request.Context())
	return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestConfigureWebPushRejectsIncompleteConfigurationWithoutEnablingIt(t *testing.T) {
	t.Parallel()
	server := &Server{}
	if err := server.ConfigureWebPush(WebPushConfig{Enabled: true, PublicKey: "public-only"}); err == nil {
		t.Fatal("incomplete configuration succeeded")
	}
	if server.webPushConfig.Enabled {
		t.Fatal("failed configuration left Web Push enabled")
	}
}

func TestStartingMaintenanceDoesNotCancelWebPushDispatcher(t *testing.T) {
	t.Parallel()
	cancelled := false
	server := &Server{webPushCancel: func() { cancelled = true }}
	server.StartMaintenance(time.Hour, 30*24*time.Hour)
	defer server.maintenanceCancel()
	if cancelled {
		t.Fatal("starting maintenance cancelled the Web Push dispatcher")
	}
}

func TestShutdownCancelsWebPushDispatcher(t *testing.T) {
	t.Parallel()
	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	cancelled := false
	server.webPushCancel = func() { cancelled = true }
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("shutdown left the Web Push dispatcher running")
	}
}

func TestWebPushPayloadIsVersionedCompactAndContainsNoCapabilityMaterial(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	item := domain.UserNotification{ID: "ntf_payload", Category: domain.NotificationCategoryNotes, EventKind: notificationKindNoteChanged, Severity: "info", Title: "Note Edited", Body: "A shared Hank note was updated.", TargetPath: "/dashboard/profile-notes?note=note-one", CollapseKey: "notes-note-one", LastOccurredAt: now}
	payload, err := encodeWebPushPayload(item, 7)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, required := range []string{`"schema_version":1`, `"notification_id":"ntf_payload"`, `"unread_count":7`, `"occurred_at":"2026-08-21T12:00:00Z"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("payload missing %s: %s", required, text)
		}
	}
	for _, forbidden := range []string{"endpoint", "p256dh", "auth-secret", "token"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("payload contains forbidden capability field %q: %s", forbidden, text)
		}
	}
	if len(payload) >= 3*1024 {
		t.Fatalf("payload length = %d", len(payload))
	}
}

func TestWebPushDispatcherCancelsReadNotificationBeforeSending(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixtureForCloud(t, "read_cancel")
	defer db.Close()
	sender := &sequenceWebPushSender{statuses: []int{http.StatusOK}}
	dispatcher := newWebPushDispatcher(db, WebPushConfig{Enabled: true, PublicKey: "public", PrivateKey: "private", Subject: "mailto:test@example.com"}, nil, sender)
	deliveryID, _ := createCloudWebPushDelivery(t, ctx, db, user, home, now)
	claimed, err := db.ClaimDueWebPushDeliveries(ctx, "dispatcher-read", now, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := db.MarkUserNotificationRead(ctx, user.ID, claimed[0].NotificationID, now); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.dispatchClaim(ctx, "dispatcher-read", claimed[0], now); err != nil {
		t.Fatalf("dispatch read notification: %v", err)
	}
	var state, outcome string
	if err := db.DB().QueryRowContext(ctx, `SELECT state, last_outcome_code FROM web_push_deliveries WHERE id = $1`, deliveryID).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || outcome != "notification_read" || len(sender.statuses) != 1 {
		t.Fatalf("read delivery state=%q outcome=%q remaining sends=%d", state, outcome, len(sender.statuses))
	}
}

func TestWebPushDispatcherRetriesTransientAndInvalidatesGoneSubscription(t *testing.T) {
	t.Parallel()
	ctx, db, user, home, now := notificationStoreFixtureForCloud(t, "dispatcher")
	defer db.Close()
	sender := &sequenceWebPushSender{statuses: []int{http.StatusServiceUnavailable, http.StatusGone}}
	dispatcher := newWebPushDispatcher(db, WebPushConfig{Enabled: true, PublicKey: "public", PrivateKey: "private", Subject: "mailto:test@example.com"}, nil, sender)
	deliveryID, subscriptionID := createCloudWebPushDelivery(t, ctx, db, user, home, now)

	claimed, err := db.ClaimDueWebPushDeliveries(ctx, "dispatcher-test", now, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != deliveryID {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := dispatcher.dispatchClaim(ctx, "dispatcher-test", claimed[0], now); err != nil {
		t.Fatalf("transient dispatch: %v", err)
	}
	var state string
	if err := db.DB().QueryRowContext(ctx, `SELECT state FROM web_push_deliveries WHERE id = $1`, deliveryID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("state after 503 = %q, %v", state, err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE web_push_deliveries SET next_attempt_at = $1 WHERE id = $2`, now, deliveryID); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimDueWebPushDeliveries(ctx, "dispatcher-test", now, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("retry claim = %#v, %v", claimed, err)
	}
	if err := dispatcher.dispatchClaim(ctx, "dispatcher-test", claimed[0], now); err != nil {
		t.Fatalf("gone dispatch: %v", err)
	}
	var count int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM web_push_subscriptions WHERE id = $1`, subscriptionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("subscription count after 410 = %d, %v", count, err)
	}
}

type sequenceWebPushSender struct {
	statuses []int
}

func (s *sequenceWebPushSender) Send(context.Context, domain.WebPushSubscription, []byte, string, WebPushConfig) (*http.Response, error) {
	if len(s.statuses) == 0 {
		return nil, errors.New("no response configured")
	}
	status := s.statuses[0]
	s.statuses = s.statuses[1:]
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response"))}, nil
}

func notificationStoreFixtureForCloud(t *testing.T, suffix string) (context.Context, *store.Store, domain.User, domain.Home, time.Time) {
	t.Helper()
	ctx := context.Background()
	db := storeForTest(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_web_push_" + suffix, Email: "web-push-" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_web_push_" + suffix, UserID: user.ID, Name: "Web Push", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	return ctx, db, user, home, now
}

func createCloudWebPushDelivery(t *testing.T, ctx context.Context, db *store.Store, user domain.User, home domain.Home, now time.Time) (string, string) {
	t.Helper()
	must(t, db.ConfigureSecretEncryption("cloud-web-push-test-key"))
	session := domain.AppSession{ID: "sess_" + user.ID, UserID: user.ID, TokenHash: "hash_" + user.ID, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateSession(ctx, session))
	subscription, err := db.UpsertWebPushSubscription(ctx, domain.WebPushSubscription{ID: "wps_" + user.ID, UserID: user.ID, SessionID: session.ID, Endpoint: "https://push.example.com/send/test", P256DH: "key", Auth: "auth"})
	must(t, err)
	input := domain.CreateUserNotificationInput{EventKey: "event-dispatcher", Notification: domain.UserNotification{ID: "ntf_dispatcher", UserID: user.ID, HomeID: home.ID, Category: domain.NotificationCategoryAgentHealth, EventKind: notificationKindAgentOffline, Severity: "warning", Title: "Agent offline", Body: "A Hank Agent is offline.", TargetPath: "/dashboard/agents/one", CollapseKey: "agent-one", Outcome: "offline", FirstOccurredAt: now, LastOccurredAt: now, CreatedAt: now}}
	item, _, err := db.CreateOrCoalesceUserNotification(ctx, input)
	must(t, err)
	_, err = db.CreateWebPushDeliveriesForNotification(ctx, item.ID, []string{subscription.ID}, now)
	must(t, err)
	var deliveryID string
	must(t, db.DB().QueryRowContext(ctx, `SELECT id FROM web_push_deliveries WHERE notification_id = $1`, item.ID).Scan(&deliveryID))
	return deliveryID, subscription.ID
}
