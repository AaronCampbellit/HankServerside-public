package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/storageops"
)

func TestStorageRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	admin := domain.User{ID: "usr_storage_admin", Email: "admin-storage@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_storage_member", Email: "member-storage@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_storage", UserID: admin.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateHomeInvitation(ctx, domain.HomeInvitation{ID: "inv_storage_member", HomeID: home.ID, Email: member.Email, Role: domain.HomeRoleMember, TokenHash: "token-hash", CreatedAt: now}))
	must(t, db.AcceptHomeInvitation(ctx, "inv_storage_member", member, domain.HomeRoleMember))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_admin", UserID: admin.ID, TokenHash: hashToken("admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureStorageOps(t.TempDir(), t.TempDir(), "secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "admin-token", http.MethodGet, "/v1/home/storage/status", nil, &storageops.StatusSnapshot{})
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/home/storage/status", nil},
		{http.MethodGet, "/v1/home/storage/config", nil},
		{http.MethodPut, "/v1/home/storage/config", storageops.DefaultConfig()},
		{http.MethodGet, "/v1/home/storage/events", nil},
		{http.MethodDelete, "/v1/home/storage/events", nil},
		{http.MethodPost, "/v1/home/storage/backup", map[string]any{"backup_type": "diff"}},
		{http.MethodPost, "/v1/home/storage/restore-test", map[string]any{"backup_label": "20260430-010101F"}},
		{http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{"backup_label": "20260430-010101F", "confirmation": "RESTORE HANK DATABASE"}},
	} {
		response := requestJSONStatus(t, testServer, "member-token", tc.method, tc.path, tc.body, http.StatusForbidden)
		response.Body.Close()
	}
}

func TestStoragePageRequiresAdmin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	admin := domain.User{ID: "usr_storage_page_admin", Email: "storage-page-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_storage_page_member", Email: "storage-page-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_storage_page", UserID: admin.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateHomeInvitation(ctx, domain.HomeInvitation{ID: "inv_storage_page_member", HomeID: home.ID, Email: member.Email, Role: domain.HomeRoleMember, TokenHash: "token-hash", CreatedAt: now}))
	must(t, db.AcceptHomeInvitation(ctx, "inv_storage_page_member", member, domain.HomeRoleMember))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_page_admin", UserID: admin.ID, TokenHash: hashToken("admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_page_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	adminReq, err := http.NewRequest(http.MethodGet, testServer.URL+"/dashboard/settings/backups-pane", nil)
	if err != nil {
		t.Fatal(err)
	}
	adminReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	adminResp, err := testServer.Client().Do(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin storage page status = %d, want %d", adminResp.StatusCode, http.StatusOK)
	}

	memberReq, err := http.NewRequest(http.MethodGet, testServer.URL+"/dashboard/settings/backups-pane", nil)
	if err != nil {
		t.Fatal(err)
	}
	memberReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "member-token"})
	memberResp, err := testServer.Client().Do(memberReq)
	if err != nil {
		t.Fatal(err)
	}
	memberResp.Body.Close()
	if memberResp.StatusCode != http.StatusForbidden {
		t.Fatalf("member storage page status = %d, want %d", memberResp.StatusCode, http.StatusForbidden)
	}
}

func TestStorageEventsCanBeCleared(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_storage_clear", Email: "storage-clear@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_storage_clear", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_clear", UserID: user.ID, TokenHash: hashToken("storage-clear-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	stateDir := t.TempDir()
	logDir := t.TempDir()
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureStorageOps(stateDir, logDir, "secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	if _, err := storageops.AppendEvent(logDir, storageops.Event{
		Operation: storageops.EventOperationBackup,
		Status:    storageops.EventStatusFailed,
		Severity:  storageops.EventSeverityError,
		Message:   "backup failed",
	}); err != nil {
		t.Fatal(err)
	}
	var status storageops.StatusSnapshot
	requestJSON(t, testServer, "storage-clear-token", http.MethodGet, "/v1/home/storage/status", nil, &status)
	if len(status.Events) != 1 {
		t.Fatalf("events length before clear = %d, want 1", len(status.Events))
	}

	var payload map[string]bool
	requestJSON(t, testServer, "storage-clear-token", http.MethodDelete, "/v1/home/storage/events", nil, &payload)
	if !payload["cleared"] {
		t.Fatalf("clear payload = %+v", payload)
	}
	requestJSON(t, testServer, "storage-clear-token", http.MethodGet, "/v1/home/storage/status", nil, &status)
	if len(status.Events) != 0 || len(status.Failures) != 0 {
		t.Fatalf("status after clear = %+v", status)
	}
}

func TestPrimaryRestoreRequiresConfirmation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_restore_admin", Email: "restore-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_restore", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_restore_admin", UserID: user.ID, TokenHash: hashToken("restore-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureStorageOps(t.TempDir(), t.TempDir(), "secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, "restore-token", http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{
		"backup_label": "20260430-010101F",
		"confirmation": "wrong",
	}, http.StatusForbidden)
	response.Body.Close()

	var tokenPayload struct {
		AdminActionToken string `json:"admin_action_token"`
	}
	requestJSON(t, testServer, "restore-token", http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{
		"request_action_token": true,
		"backup_label":         "20260430-010101F",
	}, &tokenPayload)
	if tokenPayload.AdminActionToken == "" {
		t.Fatal("admin action token was empty")
	}
	response = requestJSONStatus(t, testServer, "restore-token", http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{
		"backup_label":       "20260429-010101F",
		"confirmation":       "RESTORE HANK DATABASE",
		"admin_action_token": tokenPayload.AdminActionToken,
	}, http.StatusForbidden)
	response.Body.Close()
	requestJSON(t, testServer, "restore-token", http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{"request_action_token": true, "backup_label": "20260430-010101F"}, &tokenPayload)
	response = requestJSONStatus(t, testServer, "restore-token", http.MethodPost, "/v1/home/storage/restore-primary", map[string]any{
		"backup_label":       "20260430-010101F",
		"confirmation":       "wrong",
		"admin_action_token": tokenPayload.AdminActionToken,
	}, http.StatusBadRequest)
	response.Body.Close()
}

func TestStorageRequestRealtimePayloadIsRedactedSummary(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	// Database creation/migrations have their own setup budget. Measure the
	// realtime behavior only after that fixture exists.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_storage_live", Email: "storage-live@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_storage_live", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_live", UserID: user.ID, TokenHash: hashToken("storage-live-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureStorageOps(t.TempDir(), t.TempDir(), "secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	appConn, _, err := appWebSocketDial(ctx, testServer, "storage-live-token")
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	subscribe, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_storage_subscribe", "", "", protocol.RoutedCommand{
		Command: "app.subscribe",
		Body:    mustEncodeBody(t, protocol.AppSubscribeRequest{Topics: []string{"storage.health"}}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope subscribe: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, subscribe); err != nil {
		t.Fatalf("subscribe write: %v", err)
	}
	if response := readUntilRequestID(t, ctx, appConn, "req_storage_subscribe"); response.Type != protocol.TypeAppResponse {
		t.Fatalf("subscribe response type = %q, want %q", response.Type, protocol.TypeAppResponse)
	}

	requestJSON(t, testServer, "storage-live-token", http.MethodPost, "/v1/home/storage/backup", map[string]any{"backup_type": "full"}, nil)

	eventEnvelope := readUntilEvent(t, ctx, appConn, "storage.health.changed")
	event, err := protocol.DecodePayload[protocol.AppEvent](eventEnvelope)
	if err != nil {
		t.Fatalf("DecodePayload event: %v", err)
	}
	if event.Topic != "storage.health" {
		t.Fatalf("event topic = %q, want storage.health", event.Topic)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Body, &payload); err != nil {
		t.Fatalf("decode event body: %v", err)
	}
	allowed := map[string]struct{}{
		"event_id":     {},
		"operation":    {},
		"status":       {},
		"severity":     {},
		"message":      {},
		"backup_label": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("storage realtime payload included disallowed key %q: %+v", key, payload)
		}
	}
	if payload["event_id"] == "" || payload["operation"] != storageops.EventOperationBackup || payload["status"] != storageops.EventStatusPending {
		t.Fatalf("storage realtime payload = %+v", payload)
	}
}

func TestStorageRequestIsRecordedInAuditTrail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_storage_audit", Email: "storage-audit@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_storage_audit", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_storage_audit", UserID: user.ID, TokenHash: hashToken("storage-audit-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureStorageOps(t.TempDir(), t.TempDir(), "secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "storage-audit-token", http.MethodPost, "/v1/home/storage/backup", map[string]any{"backup_type": "full"}, nil)

	var payload struct {
		Events []struct {
			ID          string         `json:"id"`
			EventType   string         `json:"event_type"`
			Severity    string         `json:"severity"`
			ActorUserID string         `json:"actor_user_id"`
			TargetType  string         `json:"target_type"`
			HelperText  string         `json:"helper_text"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"events"`
	}
	requestJSON(t, testServer, "storage-audit-token", http.MethodGet, "/v1/home/audit-events?target_type=storage", nil, &payload)
	if len(payload.Events) != 1 {
		t.Fatalf("storage audit event count = %d, want 1", len(payload.Events))
	}
	event := payload.Events[0]
	if event.EventType != "storage.backup.pending" || event.Severity != auditSeverityInfo || event.ActorUserID != user.ID || event.TargetType != "storage" {
		t.Fatalf("storage audit event = %#v", event)
	}
	if event.HelperText != "Manual pgBackRest backup requested." {
		t.Fatalf("storage audit helper text = %q", event.HelperText)
	}
	if event.Metadata["backup_type"] != "full" || event.Metadata["status"] != storageops.EventStatusPending {
		t.Fatalf("storage audit metadata = %#v", event.Metadata)
	}
	notifications, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(notifications.Items) != 1 || notifications.Items[0].AuditEventID != event.ID {
		t.Fatalf("storage notification audit link = %#v, %v", notifications.Items, err)
	}
}

func TestStorageAuditEventCoversEveryStorageOperation(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	cases := []struct {
		operation         string
		status            string
		severity          string
		wantType          string
		wantAuditSeverity string
	}{
		{storageops.EventOperationChecksum, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.checksum.success", auditSeverityInfo},
		{storageops.EventOperationChecksum, storageops.EventStatusFailed, storageops.EventSeverityError, "storage.checksum.failed", auditSeverityWarning},
		{storageops.EventOperationAMCheck, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.amcheck.success", auditSeverityInfo},
		{storageops.EventOperationAMCheck, storageops.EventStatusFailed, storageops.EventSeverityCritical, "storage.amcheck.failed", auditSeverityCritical},
		{storageops.EventOperationBackup, storageops.EventStatusPending, storageops.EventSeverityInfo, "storage.backup.pending", auditSeverityInfo},
		{storageops.EventOperationBackup, storageops.EventStatusStarted, storageops.EventSeverityInfo, "storage.backup.started", auditSeverityInfo},
		{storageops.EventOperationBackup, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.backup.success", auditSeverityInfo},
		{storageops.EventOperationBackup, storageops.EventStatusFailed, storageops.EventSeverityError, "storage.backup.failed", auditSeverityWarning},
		{storageops.EventOperationRestoreTest, storageops.EventStatusStarted, storageops.EventSeverityInfo, "storage.restore_test.started", auditSeverityInfo},
		{storageops.EventOperationRestoreTest, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.restore_test.success", auditSeverityInfo},
		{storageops.EventOperationRestoreTest, storageops.EventStatusFailed, storageops.EventSeverityError, "storage.restore_test.failed", auditSeverityWarning},
		{storageops.EventOperationPrimaryRestore, storageops.EventStatusStarted, storageops.EventSeverityWarning, "storage.primary_restore.started", auditSeverityWarning},
		{storageops.EventOperationPrimaryRestore, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.primary_restore.success", auditSeverityInfo},
		{storageops.EventOperationPrimaryRestore, storageops.EventStatusFailed, storageops.EventSeverityCritical, "storage.primary_restore.failed", auditSeverityCritical},
		{storageops.EventOperationConfig, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "storage.config.success", auditSeverityInfo},
	}
	for index, tc := range cases {
		storageEvent := storageops.Event{
			ID:        "storage_event_" + tc.operation + "_" + tc.status,
			Time:      now.Add(time.Duration(index) * time.Second),
			Operation: tc.operation,
			Status:    tc.status,
			Severity:  tc.severity,
			Message:   tc.wantType + " password=must-not-persist",
			Details: map[string]any{
				"home_id":             "home_storage_operation_audit",
				"requested_by":        "usr_storage_operation_audit",
				"output_excerpt":      "token=must-not-persist",
				"corruption_detected": tc.operation == storageops.EventOperationAMCheck,
			},
		}
		event := storageAuditEvent("home_storage_operation_audit", storageEvent)
		if event.EventType != tc.wantType || event.Severity != tc.wantAuditSeverity || event.TargetType != "storage" {
			t.Fatalf("storage audit event %d = %#v", index, event)
		}
		if event.HomeID == nil || *event.HomeID != "home_storage_operation_audit" || event.ActorUserID == nil || *event.ActorUserID != "usr_storage_operation_audit" {
			t.Fatalf("storage audit ownership %d = %#v", index, event)
		}
		if event.TargetID != storageEvent.ID || !event.OccurredAt.Equal(now.Add(time.Duration(index)*time.Second)) {
			t.Fatalf("storage audit event %d occurred_at = %s", index, event.OccurredAt)
		}
		if event.MetadataJSON == "" || strings.Contains(event.MetadataJSON, "must-not-persist") || !strings.Contains(event.MetadataJSON, "[redacted]") {
			t.Fatalf("storage audit event %d metadata was not redacted: %s", index, event.MetadataJSON)
		}
	}
}
