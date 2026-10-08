package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func fixtureMonitoringSettings() domain.MonitoringSettings {
	v := domain.DefaultMonitoringSettings("home")
	v.EmailEnabled = true
	v.SMTPHost = "smtp.example.com"
	v.SMTPUsername = "fixture-user"
	v.SMTPPassword = "fixture-password"
	v.EmailFrom = "hank@example.com"
	v.EmailTo = "admin@example.com"
	v.WebhookToken = "fixture-inbox"
	return v
}
func fixturePublicLookup(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func TestMonitoringSettingsSecurityAndReload(t *testing.T) {
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	must(t, db.ConfigureSecretEncryption("fixture-key"))
	now := time.Now().UTC()
	for _, id := range []string{"owner", "member", "outsider"} {
		must(t, db.CreateUser(ctx, domain.User{ID: id, Email: id + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}))
		must(t, db.CreateSession(ctx, domain.AppSession{ID: "session-" + id, UserID: id, TokenHash: hashToken("token-" + id), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))
	}
	must(t, db.CreateHome(ctx, domain.Home{ID: "home", UserID: "owner", Name: "Home", CreatedAt: now, UpdatedAt: now}))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: "home", UserID: "member", Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	instances := make(chan string, 2)
	var reloads, tests atomic.Int32
	var fail atomic.Bool
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "fixture internal secret must never reach response", 503)
			return
		}
		if r.URL.Path == "/-/reload" {
			reloads.Add(1)
		} else if r.URL.Path == "/api/v2/alerts" {
			var alerts []struct {
				Labels map[string]string `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&alerts); err != nil || len(alerts) != 1 {
				t.Error("invalid test alert")
			} else {
				instances <- alerts[0].Labels["instance"]
			}
			tests.Add(1)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer manager.Close()
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dir := t.TempDir()
	must(t, s.ConfigureMonitoringDelivery(dir, manager.URL, -1))
	s.monitoring.lookup = fixturePublicLookup
	request := func(method, path, token, body string, cookie, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		} else if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "fixture-csrf"})
			r.Header.Set(csrfHeaderName, "fixture-csrf")
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		return w
	}
	path := "/v1/home/monitoring-settings"
	for _, token := range []string{"", "token-member", "token-outsider"} {
		for _, method := range []string{"GET", "PUT"} {
			w := request(method, path, token, "{}", false, false)
			if w.Code == 200 {
				t.Fatal("non-admin settings access")
			}
		}
		if w := request("GET", path+"/health", token, "", false, false); w.Code == 200 {
			t.Fatal("non-admin monitoring health access")
		}
		if w := request("POST", path+"/test", token, "{}", false, false); w.Code == 200 {
			t.Fatal("non-admin test send")
		}
	}
	if w := request("PUT", path, "token-owner", "{}", true, false); w.Code != 403 {
		t.Fatalf("CSRF: %d", w.Code)
	}
	if w := request("GET", path+"/health", "token-owner", "", false, false); w.Code != 200 || strings.Contains(w.Body.String(), "fixture internal secret") {
		t.Fatal("admin monitoring health response")
	}
	v := fixtureMonitoringSettings()
	v.WebhookToken = ""
	raw, _ := json.Marshal(v)
	var body map[string]any
	must(t, json.Unmarshal(raw, &body))
	body["smtp_password"] = "hankenc:v1:literal-password"
	raw, _ = json.Marshal(body)
	w := request("PUT", path, "token-owner", string(raw), true, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"delivery_status":"ready"`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "literal-password") || strings.Contains(w.Body.String(), "hankenc:") {
		t.Fatal("secret returned")
	}
	saved, err := db.GetMonitoringSettings(ctx, "home")
	must(t, err)
	if saved.SMTPPassword != "hankenc:v1:literal-password" || saved.WebhookToken == "" {
		t.Fatal("password or generated credential lost")
	}
	alertBody := `{"version":"4","alerts":[{"status":"firing","startsAt":"2026-09-05T00:00:00Z","labels":{"alertname":"ManagedCredentialTest"}}]}`
	for _, token := range []string{"token-owner", "fixture-scrape"} {
		if got := request("POST", "/v1/integrations/alertmanager", token, alertBody, false, false); got.Code != 401 {
			t.Fatal("unscoped credential authorized managed webhook")
		}
	}
	if got := request("POST", "/v1/integrations/alertmanager", saved.WebhookToken, alertBody, false, false); got.Code != 200 {
		t.Fatalf("managed credential rejected: %d", got.Code)
	}

	var encrypted string
	must(t, db.DB().QueryRowContext(ctx, `SELECT encrypted_credentials FROM monitoring_delivery_settings`).Scan(&encrypted))
	if !strings.HasPrefix(encrypted, "hankenc:v1:") || strings.Contains(encrypted, "literal-password") || strings.Contains(encrypted, saved.WebhookToken) {
		t.Fatal("plaintext storage")
	}
	file, err := os.ReadFile(filepath.Join(dir, "alertmanager.yml"))
	must(t, err)
	if !strings.Contains(string(file), "8.8.8.8:587") || !strings.Contains(string(file), `"server_name":"smtp.example.com"`) {
		t.Fatal("SMTP DNS was not pinned with TLS hostname")
	}
	info, err := os.Stat(filepath.Join(dir, "alertmanager.yml"))
	must(t, err)
	if info.Mode().Perm() != 0640 {
		t.Fatal("runtime secrets not private")
	}
	delete(body, "smtp_password")
	body["inbox_audience"] = "members"
	raw, _ = json.Marshal(body)
	w = request("PUT", path, "token-owner", string(raw), false, false)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err = db.GetMonitoringSettings(ctx, "home")
	must(t, err)
	if saved.SMTPPassword != "hankenc:v1:literal-password" {
		t.Fatal("omitted password cleared secret")
	}
	ids, err := s.notificationService.resolveRecipients(ctx, NotificationEvent{HomeID: "home"}, domain.NotificationCategoryMonitoring)
	must(t, err)
	if len(ids) != 2 {
		t.Fatalf("all-member recipients: %v", ids)
	}
	w = request("POST", path+"/test", "token-owner", "{}", false, false)
	if w.Code != 200 || tests.Load() != 1 {
		t.Fatal("test not queued")
	}
	if w = request("POST", path+"/test", "token-owner", "{}", false, false); w.Code != 429 {
		t.Fatal("test rate limit missing")
	}
	s.monitoring.mu.Lock()
	s.monitoring.lastTest = time.Now().Add(-time.Minute)
	s.monitoring.mu.Unlock()
	if w = request("POST", path+"/test", "token-owner", "{}", false, false); w.Code != 200 {
		t.Fatal("second test not queued after cooldown")
	}
	first, second := <-instances, <-instances
	if first == "" || second == "" || first == second {
		t.Fatal("test alerts share an Alertmanager aggregation group and can expire before delivery")
	}
	fail.Store(true)
	body["email_to"] = "changed@example.com"
	raw, _ = json.Marshal(body)
	w = request("PUT", path, "token-owner", string(raw), false, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"delivery_status":"pending"`) || strings.Contains(w.Body.String(), "internal secret") {
		t.Fatal("reload failure not safely reported as pending")
	}
	fail.Store(false)
	saved, err = db.GetMonitoringSettings(ctx, "home")
	must(t, err)
	must(t, s.monitoring.apply(ctx, saved))
	if s.monitoring.status != "ready" || reloads.Load() < 2 {
		t.Fatal("retry failed")
	}
	body["clear_password"] = true
	body["email_enabled"] = false
	body["inbox_enabled"] = false
	raw, _ = json.Marshal(body)
	w = request("PUT", path, "token-owner", string(raw), false, false)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err = db.GetMonitoringSettings(ctx, "home")
	must(t, err)
	if saved.SMTPPassword != "" {
		t.Fatal("password not removed")
	}
	ids, err = s.notificationService.resolveRecipients(ctx, NotificationEvent{HomeID: "home"}, domain.NotificationCategoryMonitoring)
	must(t, err)
	if len(ids) != 0 {
		t.Fatal("disabled inbox still has recipients")
	}
	// A fresh server instance reads the persisted settings and credentials.
	fresh := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restored, err := fresh.monitoringSettings(ctx, "home")
	must(t, err)
	if restored.EmailTo != "changed@example.com" || restored.InboxAudience != "members" || restored.WebhookToken != saved.WebhookToken {
		t.Fatal("restart lost settings")
	}
}

func TestMonitoringConfigurationRejectsUnsafeDestinations(t *testing.T) {
	v := fixtureMonitoringSettings()
	for _, address := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "::ffff:127.0.0.1"} {
		m := monitoringDelivery{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}}
		if _, err := m.render(context.Background(), v); err == nil {
			t.Fatalf("private SMTP destination accepted: %s", address)
		}
	}
	for _, change := range []func(*domain.MonitoringSettings){func(v *domain.MonitoringSettings) { v.EmailTo = "{{.ExternalURL}}@example.com" }, func(v *domain.MonitoringSettings) { v.EmailFrom = "sender@example.com\r\nBcc: other@example.com" }, func(v *domain.MonitoringSettings) { v.SMTPHost = "http://host/path" }, func(v *domain.MonitoringSettings) { v.SMTPPort = 465 }, func(v *domain.MonitoringSettings) { v.InboxAudience = "other-home" }} {
		candidate := v
		change(&candidate)
		if validateMonitoringSettings(&candidate) == nil {
			t.Fatal("unsafe settings accepted")
		}
	}
	if err := validateMonitoringSettings(&v); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRendererWithPinnedAlertmanager(t *testing.T) {
	if os.Getenv("HANK_TEST_ALERTMANAGER") != "1" {
		t.Skip("run make monitoring-test for pinned Alertmanager validation")
	}
	m := monitoringDelivery{lookup: fixturePublicLookup}
	for _, channels := range [][2]bool{{true, true}, {true, false}, {false, true}, {false, false}} {
		v := fixtureMonitoringSettings()
		v.InboxEnabled = channels[0]
		v.EmailEnabled = channels[1]
		body, err := m.render(context.Background(), v)
		must(t, err)
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, "alertmanager.yml"), body, 0600))
		command := exec.Command("docker", "run", "--rm", "--network", "none", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-v", dir+":/config:ro", "--entrypoint", "amtool", "prom/alertmanager:v0.28.1", "check-config", "/config/alertmanager.yml")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("pinned configuration rejected: %v %s", err, output)
		}
		if channels[0] && channels[1] {
			delivery := exec.Command("python3", "../../scripts/tests/alertmanager-delivery-test.py")
			delivery.Env = append(os.Environ(), "HANK_MONITORING_TEST_CONFIG="+filepath.Join(dir, "alertmanager.yml"))
			if output, err := delivery.CombinedOutput(); err != nil {
				t.Fatalf("managed delivery fixture failed: %v %s", err, output)
			}
		}
	}
}

func TestMonitoringReloadFailureThenRevertRewritesStagedConfiguration(t *testing.T) {
	var fail atomic.Bool
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", 503)
		}
	}))
	defer manager.Close()
	dir := t.TempDir()
	m := monitoringDelivery{dir: dir, managerURL: manager.URL, group: -1, lookup: fixturePublicLookup, client: manager.Client()}
	original := fixtureMonitoringSettings()
	must(t, m.apply(context.Background(), original))
	candidate := original
	candidate.EmailTo = "different@example.com"
	fail.Store(true)
	if m.apply(context.Background(), candidate) == nil {
		t.Fatal("expected reload failure")
	}
	fail.Store(false)
	must(t, m.apply(context.Background(), original))
	body, err := os.ReadFile(filepath.Join(dir, "alertmanager.yml"))
	must(t, err)
	if strings.Contains(string(body), "different@example.com") {
		t.Fatal("reverting to last applied settings left a newer staged recipient on disk")
	}
}

func TestMonitoringSettingsNeverUsePlaintextOptOut(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	v := fixtureMonitoringSettings()
	if db.SaveMonitoringSettings(context.Background(), v) == nil {
		t.Fatal("monitoring credentials accepted without application encryption key")
	}
}
