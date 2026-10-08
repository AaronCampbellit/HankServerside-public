package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestNormalizedQuickLinkAllowsDashboardFileServerURL(t *testing.T) {
	enabled := true
	link, err := normalizedQuickLink(quickLinkRequest{
		Title:              "Recipes",
		URL:                "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1",
		Description:        "Recipe index",
		HealthCheckEnabled: &enabled,
	}, domain.HomeQuickLink{})
	if err != nil {
		t.Fatalf("normalizedQuickLink returned error: %v", err)
	}
	if link.URL != "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1" {
		t.Fatalf("URL = %q", link.URL)
	}
	if link.HealthCheckEnabled {
		t.Fatal("internal dashboard links should not keep external health checks enabled")
	}
	if link.Status != domain.QuickLinkStatusDisabled {
		t.Fatalf("Status = %q, want disabled", link.Status)
	}
}

func TestQuickLinkHealthTransitionsCreateDurableNotifications(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_quick_link_notifications", Email: "quick-link-notifications@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_link_notifications", UserID: user.ID, Name: "Quick Link Health", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	statusCode := http.StatusServiceUnavailable
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(statusCode) }))
	defer upstream.Close()
	link := domain.HomeQuickLink{ID: "ql_notifications", HomeID: home.ID, Title: "Home Assistant", URL: upstream.URL, HealthCheckEnabled: true, Status: domain.QuickLinkStatusUnchecked, CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	must(t, db.CreateHomeQuickLink(ctx, link))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	link = server.checkHomeQuickLink(ctx, home.ID, link)
	link = server.checkHomeQuickLink(ctx, home.ID, link)
	statusCode = http.StatusOK
	link = server.checkHomeQuickLink(ctx, home.ID, link)

	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("quick-link notifications = %#v, %v", page.Items, err)
	}
	kinds := []string{page.Items[0].EventKind, page.Items[1].EventKind}
	if !((kinds[0] == notificationKindQuickLinkUp && kinds[1] == notificationKindQuickLinkDown) || (kinds[1] == notificationKindQuickLinkUp && kinds[0] == notificationKindQuickLinkDown)) {
		t.Fatalf("quick-link kinds = %#v", kinds)
	}
}

func TestQuickLinkNotificationRetriesAfterEmitterUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_quick_link_retry", Email: "quick-link-retry@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_link_retry", UserID: user.ID, Name: "Quick Link Retry", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer upstream.Close()
	link := domain.HomeQuickLink{ID: "ql_retry", HomeID: home.ID, Title: "Retry Link", URL: upstream.URL, HealthCheckEnabled: true, Status: domain.QuickLinkStatusUnchecked, CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	must(t, db.CreateHomeQuickLink(ctx, link))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	service := server.notificationService
	server.notificationService = nil
	link = server.checkHomeQuickLink(ctx, home.ID, link)
	server.notificationService = service
	server.checkHomeQuickLink(ctx, home.ID, link)

	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].EventKind != notificationKindQuickLinkDown {
		t.Fatalf("retried quick-link notification = %#v, %v", page.Items, err)
	}
}

func TestQuickLinkEnabledFromDisabledDoesNotEmitFalseRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_quick_link_enable", Email: "quick-link-enable@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_link_enable", UserID: user.ID, Name: "Quick Link Enable", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	link := domain.HomeQuickLink{ID: "ql_enable", HomeID: home.ID, Title: "New Link", URL: upstream.URL, HealthCheckEnabled: false, Status: domain.QuickLinkStatusDisabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	must(t, db.CreateHomeQuickLink(ctx, link))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	link = server.checkHomeQuickLink(ctx, home.ID, link)
	link.HealthCheckEnabled = true
	link = server.checkHomeQuickLink(ctx, home.ID, link)

	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("disabled-to-up notifications = %#v, %v", page.Items, err)
	}
}

func TestNormalizedQuickLinkRejectsRawSMBURL(t *testing.T) {
	_, err := normalizedQuickLink(quickLinkRequest{
		Title: "Raw SMB",
		URL:   "smb://nas.local/media/index.html",
	}, domain.HomeQuickLink{})
	if err == nil {
		t.Fatal("normalizedQuickLink accepted raw SMB URL")
	}
}

func TestNormalizedQuickLinkRejectsOtherRelativeURL(t *testing.T) {
	_, err := normalizedQuickLink(quickLinkRequest{
		Title: "Unsafe relative",
		URL:   "/v1/home/files/preview?path=%2Findex.html",
	}, domain.HomeQuickLink{})
	if err == nil {
		t.Fatal("normalizedQuickLink accepted non-dashboard relative URL")
	}
}
