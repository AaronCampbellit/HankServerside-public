package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/storageops"
)

func TestNotificationSettingsAndAPNSHandlers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	user := domain.User{ID: "usr_notify_handler_" + suffix, Email: "notify-handler+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	sessionRawToken := "session-notify-handler-" + suffix
	session := domain.AppSession{ID: "sess_notify_handler_" + suffix, UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var defaults struct {
		Monitoring        bool `json:"monitoring"`
		Storage           bool `json:"storage"`
		Notes             bool `json:"notes"`
		DashboardEntities bool `json:"dashboard_entities"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/me/notification-settings", nil, &defaults)
	if !defaults.Monitoring || !defaults.Storage || !defaults.Notes || !defaults.DashboardEntities {
		t.Fatalf("default notification settings = %#v", defaults)
	}

	var updated struct {
		Monitoring        bool `json:"monitoring"`
		Storage           bool `json:"storage"`
		Notes             bool `json:"notes"`
		DashboardEntities bool `json:"dashboard_entities"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodPut, "/v1/me/notification-settings", map[string]any{
		"storage":            false,
		"notes":              true,
		"dashboard_entities": false,
	}, &updated)
	if !updated.Monitoring || updated.Storage || !updated.Notes || updated.DashboardEntities {
		t.Fatalf("updated notification settings = %#v", updated)
	}

	var registration struct {
		OK         bool     `json:"ok"`
		DeviceID   string   `json:"device_id"`
		Categories []string `json:"categories"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodPost, "/v1/me/devices/apns", map[string]any{
		"device_id":          "device-handler-" + suffix,
		"token":              "token-handler-" + suffix,
		"environment":        "production",
		"bundle_id":          "com.dropfile.Hank",
		"enabled_categories": []string{"notes", "bad-category", "notes"},
	}, &registration)
	if !registration.OK || registration.DeviceID != "device-handler-"+suffix || !slices.Equal(registration.Categories, []string{domain.NotificationCategoryNotes}) {
		t.Fatalf("registration = %#v", registration)
	}

	devices, err := db.ListActiveAPNSDevicesForUsers(ctx, []string{user.ID})
	if err != nil {
		t.Fatalf("ListActiveAPNSDevicesForUsers: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceID != "device-handler-"+suffix || devices[0].Environment != "production" {
		t.Fatalf("devices = %#v", devices)
	}

	requestJSON(t, testServer, sessionRawToken, http.MethodDelete, "/v1/me/devices/device-handler-"+suffix+"/apns", nil, nil)
	devices, err = db.ListActiveAPNSDevicesForUsers(ctx, []string{user.ID})
	if err != nil {
		t.Fatalf("ListActiveAPNSDevicesForUsers after delete: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("devices after delete = %#v", devices)
	}
}

func TestHomeNotificationsFeedReportsOperationalIssues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	user := domain.User{ID: "usr_notify_feed_" + suffix, Email: "notify-feed+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notify_feed_" + suffix, UserID: user.ID, Name: "Notify Feed", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_notify_feed_" + suffix, HomeID: home.ID, Name: "Kitchen Mac", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	sessionRawToken := "session-notify-feed-" + suffix
	session := domain.AppSession{ID: "sess_notify_feed_" + suffix, UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateSession(ctx, session))
	must(t, db.CreateHomeQuickLink(ctx, domain.HomeQuickLink{
		ID:                 "ql_notify_feed_" + suffix,
		HomeID:             home.ID,
		Title:              "Home Assistant",
		URL:                "https://ha.example.test",
		Description:        "Local HA",
		SortOrder:          10,
		HealthCheckEnabled: true,
		Status:             domain.QuickLinkStatusDown,
		StatusCode:         502,
		LastError:          "bad gateway",
		CreatedAt:          now,
		UpdatedAt:          now,
		UpdatedBy:          user.ID,
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		Notifications []homeNotification `json:"notifications"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/home/notifications", nil, &payload)

	if len(payload.Notifications) < 2 {
		t.Fatalf("notifications = %#v, want agent and quick link issues", payload.Notifications)
	}
	if !notificationTitlesContain(payload.Notifications, "Agent offline") {
		t.Fatalf("missing agent health notification: %#v", payload.Notifications)
	}
	if !notificationTitlesContain(payload.Notifications, "Quick link is down") {
		t.Fatalf("missing quick link notification: %#v", payload.Notifications)
	}
}

func notificationTitlesContain(items []homeNotification, title string) bool {
	for _, item := range items {
		if item.Title == title {
			return true
		}
	}
	return false
}

func TestNotificationEventsTargetRelevantUsers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	owner := domain.User{ID: "usr_notify_owner_" + suffix, Email: "notify-owner+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	admin := domain.User{ID: "usr_notify_admin_" + suffix, Email: "notify-admin+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_notify_member_" + suffix, Email: "notify-member+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notify_events_" + suffix, UserID: owner.ID, Name: "Notify Events", CreatedAt: now, UpdatedAt: now}
	for _, user := range []domain.User{owner, admin, member} {
		must(t, db.CreateUser(ctx, user))
		must(t, db.CreateSession(ctx, domain.AppSession{
			ID:        "sess_" + user.ID,
			UserID:    user.ID,
			TokenHash: "hash_" + user.ID,
			ExpiresAt: now.Add(time.Hour),
			CreatedAt: now,
		}))
		if _, err := db.UpsertAPNSDevice(ctx, domain.APNSDevice{
			UserID:            user.ID,
			SessionID:         "sess_" + user.ID,
			DeviceID:          "device_" + user.ID,
			Token:             "token_" + user.ID,
			Environment:       "sandbox",
			BundleID:          "com.dropfile.Hank",
			EnabledCategories: json.RawMessage(`["storage","notes","dashboard_entities"]`),
		}); err != nil {
			t.Fatalf("UpsertAPNSDevice %s: %v", user.ID, err)
		}
	}
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	_, err := db.SaveNotificationSettings(ctx, domain.NotificationSettings{
		UserID:                   admin.ID,
		StorageEnabled:           true,
		NotesEnabled:             false,
		DashboardEntitiesEnabled: true,
	})
	if err != nil {
		t.Fatalf("SaveNotificationSettings admin: %v", err)
	}

	sender := &recordingPushSender{}
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.pushSender = sender

	server.notifyStorageEvent(ctx, storageops.Event{
		ID:        "backup_failed",
		Operation: storageops.EventOperationBackup,
		Status:    storageops.EventStatusFailed,
		Severity:  storageops.EventSeverityError,
	})
	assertNotificationUsers(t, sender.drain(), []string{owner.ID, admin.ID})

	note := domain.UserNote{
		ID:            "note_notify_events_" + suffix,
		NoteID:        "note-events-" + suffix + ".md",
		OwnerUserID:   owner.ID,
		HomeID:        home.ID,
		Title:         "Events",
		Content:       "body",
		BodyMarkdown:  "body",
		BodyFormat:    "markdown",
		PageType:      "text",
		Revision:      "rev-1",
		Checksum:      "sum-1",
		CRDTStateJSON: "{}",
		CollabVersion: 1,
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     owner.ID,
	}
	must(t, db.SaveUserNoteWithOperations(ctx, note, nil))
	must(t, db.AddNoteShare(ctx, domain.NoteShare{NoteID: note.ID, HomeID: home.ID, TargetUserID: admin.ID, SharedBy: owner.ID, CreatedAt: now, UpdatedAt: now}))
	must(t, db.AddNoteShare(ctx, domain.NoteShare{NoteID: note.ID, HomeID: home.ID, TargetUserID: member.ID, SharedBy: owner.ID, CreatedAt: now, UpdatedAt: now}))
	recipients, err := db.ListNoteNotificationUserIDs(ctx, note.ID, owner.ID)
	if err != nil {
		t.Fatalf("ListNoteNotificationUserIDs: %v", err)
	}
	if !slices.Contains(recipients, member.ID) {
		t.Fatalf("note notification recipients = %#v, want member %q", recipients, member.ID)
	}
	devices, err := db.ListActiveAPNSDevicesForUsers(ctx, recipients)
	if err != nil {
		t.Fatalf("ListActiveAPNSDevicesForUsers: %v", err)
	}
	memberDeviceFound := false
	for _, device := range devices {
		if device.UserID == member.ID {
			memberDeviceFound = true
		}
	}
	if !memberDeviceFound {
		t.Fatalf("active notification devices = %#v, want member device", devices)
	}

	server.notifyNoteChanged(ctx, note.ID, note.NoteID, owner.ID)
	noteSends := sender.drain()
	assertNotificationUsers(t, noteSends, []string{member.ID})
	if len(noteSends) != 1 || noteSends[0].notification.URL != "hank://notifications/notes/"+note.ID {
		t.Fatalf("note sends = %#v", noteSends)
	}

	if _, err := db.SaveUserProfileSettings(ctx, member.ID, nil, json.RawMessage(`{"dashboard_tiles":[{"entity_id":"light.kitchen","is_enabled":true}]}`)); err != nil {
		t.Fatalf("SaveUserProfileSettings member: %v", err)
	}
	if _, err := db.SaveUserProfileSettings(ctx, admin.ID, nil, json.RawMessage(`{"dashboard_tiles":[{"entity_id":"light.kitchen","is_enabled":false}]}`)); err != nil {
		t.Fatalf("SaveUserProfileSettings admin: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"state": protocol.HomeAssistantState{
			EntityID:   "light.kitchen",
			State:      "on",
			Attributes: map[string]any{"friendly_name": "Kitchen Light"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.notifyDashboardEntityChanged(ctx, home.ID, body)
	dashboardSends := sender.drain()
	assertNotificationUsers(t, dashboardSends, []string{member.ID})
	if len(dashboardSends) != 1 || dashboardSends[0].notification.Category != domain.NotificationCategoryDashboardEntities {
		t.Fatalf("dashboard sends = %#v", dashboardSends)
	}
}

type recordedPush struct {
	userID       string
	deviceID     string
	notification PushNotification
}

type recordingPushSender struct {
	mu    sync.Mutex
	sends []recordedPush
}

func (s *recordingPushSender) Send(_ context.Context, device domain.APNSDevice, notification PushNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, recordedPush{
		userID:       device.UserID,
		deviceID:     device.DeviceID,
		notification: notification,
	})
	return nil
}

func (s *recordingPushSender) drain() []recordedPush {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]recordedPush(nil), s.sends...)
	s.sends = nil
	return out
}

func assertNotificationUsers(t *testing.T, sends []recordedPush, want []string) {
	t.Helper()
	got := make([]string, 0, len(sends))
	for _, send := range sends {
		got = append(got, send.userID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("notification users = %#v, want %#v; sends=%#v", got, want, sends)
	}
}
