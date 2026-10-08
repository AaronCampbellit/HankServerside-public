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

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAlertmanagerWebhookAuthorizationAndValidation(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(method, query, token, body string) int {
		r := httptest.NewRequest(method, "/v1/integrations/alertmanager"+query, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.AddCookie(&http.Cookie{Name: "hank_session", Value: "admin-session"})
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("POST", "", "inbox-only", "{}"); got != 404 {
		t.Fatalf("disabled: %d", got)
	}
	s.ConfigureAlertmanagerWebhookToken("inbox-only")
	for _, token := range []string{"", "wrong", "scrape-only", "admin-session"} {
		if got := request("POST", "", token, "{}"); got != 401 {
			t.Fatalf("unauthorized token: %d", got)
		}
	}
	if got := request("GET", "", "inbox-only", "{}"); got != 405 {
		t.Fatalf("method: %d", got)
	}
	if got := request("POST", "?token=inbox-only", "inbox-only", "{}"); got != 400 {
		t.Fatalf("query: %d", got)
	}
	for _, body := range []string{"{} {}", strings.Repeat(" ", 1<<20) + "{}", `{"version":"4","alerts":[]}`, `{"version":"4","truncatedAlerts":1,"alerts":[{}]}`, `{"version":"4","alerts":[{"status":"firing","startsAt":"2026-09-05T00:00:00Z","labels":{"alertname":"A"}},{"status":"invalid"}]}`} {
		if got := request("POST", "", "inbox-only", body); got != 400 {
			t.Fatalf("invalid payload: %d", got)
		}
	}
}

func TestAlertmanagerWebhookPersistsAdminInboxAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	for _, id := range []string{"owner", "admin", "member"} {
		must(t, db.CreateUser(ctx, domain.User{ID: id, Email: id + "@example.invalid", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}))
	}
	must(t, db.CreateHome(ctx, domain.Home{ID: "home", UserID: "owner", Name: "Home", CreatedAt: now, UpdatedAt: now}))
	for id, role := range map[string]string{"admin": domain.HomeRoleAdmin, "member": domain.HomeRoleMember} {
		must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: "home", UserID: id, Role: role, CreatedAt: now, UpdatedAt: now}))
	}
	_, err := db.DB().ExecContext(ctx, `INSERT INTO notification_settings (user_id, updated_at) VALUES ('admin', now())`)
	must(t, err)
	settings, err := db.GetNotificationSettings(ctx, "admin")
	must(t, err)
	if !settings.MonitoringEnabled {
		t.Fatal("monitoring must default on")
	}
	settings.MonitoringEnabled = false
	_, err = db.SaveNotificationSettings(ctx, settings)
	must(t, err)
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.ConfigureAlertmanagerWebhookToken("inbox-only")
	alert := alertmanagerAlert{Status: "firing", StartsAt: now, Labels: map[string]string{"alertname": "HankHostDiskSpaceLow", "severity": "warning", "instance": "private-instance"}, Annotations: map[string]string{"summary": "Disk space is low", "description": "private-description"}}
	post := func() {
		body, err := json.Marshal(alertmanagerNotification{Version: "4", Alerts: []alertmanagerAlert{alert}})
		must(t, err)
		r := httptest.NewRequest("POST", "/v1/integrations/alertmanager", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer inbox-only")
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("delivery: %d %s", w.Code, w.Body.String())
		}
	}
	post()
	post()
	for _, id := range []string{"owner", "admin"} {
		page, err := db.ListUserNotifications(ctx, id, domain.NotificationListOptions{})
		must(t, err)
		if len(page.Items) != 1 || page.Items[0].OccurrenceCount != 1 || page.Items[0].Category != "monitoring" || page.Items[0].TargetPath != "/dashboard/settings/logs" {
			t.Fatalf("inbox: %#v", page.Items)
		}
		serialized, _ := json.Marshal(page.Items[0])
		if strings.Contains(string(serialized), "private-") {
			t.Fatal("raw alert metadata exposed")
		}
	}
	page, err := db.ListUserNotifications(ctx, "member", domain.NotificationListOptions{})
	must(t, err)
	if len(page.Items) != 0 {
		t.Fatal("member received administrator alert")
	}
	alert.Status = "resolved"
	post()
	post()
	alert.Status = "firing"
	alert.StartsAt = now.Add(time.Hour)
	post()
	var receipts int
	must(t, db.DB().QueryRowContext(ctx, `SELECT count(*) FROM notification_event_receipts WHERE user_id = 'owner'`).Scan(&receipts))
	if receipts != 3 {
		t.Fatalf("incident transitions produced %d receipts", receipts)
	}
}
