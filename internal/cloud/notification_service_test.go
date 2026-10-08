package cloud

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/storageops"
)

func TestNotificationServicePersistsInboxBeforeAPNSAndHonorsDeliveryPreferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	owner := domain.User{ID: "usr_notify_service_owner_" + suffix, Email: "notify-service-owner+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	admin := domain.User{ID: "usr_notify_service_admin_" + suffix, Email: "notify-service-admin+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_notify_service_member_" + suffix, Email: "notify-service-member+" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notify_service_" + suffix, UserID: owner.ID, Name: "Notification Service", CreatedAt: now, UpdatedAt: now}
	for _, user := range []domain.User{owner, admin, member} {
		must(t, db.CreateUser(ctx, user))
		session := domain.AppSession{ID: "sess_" + user.ID, UserID: user.ID, TokenHash: "hash_" + user.ID, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
		must(t, db.CreateSession(ctx, session))
		_, err := db.UpsertAPNSDevice(ctx, domain.APNSDevice{UserID: user.ID, SessionID: session.ID, DeviceID: "device_" + user.ID, Token: "token_" + user.ID, Environment: "sandbox", BundleID: "com.dropfile.Hank"})
		must(t, err)
	}
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	_, err := db.SaveNotificationSettings(ctx, domain.NotificationSettings{UserID: admin.ID, AgentHealthEnabled: true, QuickLinksEnabled: true, StorageEnabled: false, NotesEnabled: true, DashboardEntitiesEnabled: true})
	must(t, err)

	sender := &failingRecordingPushSender{}
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.pushSender = sender
	event := storageops.Event{ID: "storage-service-event-" + suffix, Time: now, Operation: storageops.EventOperationBackup, Status: storageops.EventStatusFailed, Severity: storageops.EventSeverityError}
	server.notifyStorageEvent(ctx, event)
	server.notifyStorageEvent(ctx, event)

	for _, user := range []domain.User{owner, admin} {
		page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("inbox for %s = %#v, %v", user.ID, page.Items, err)
		}
		if page.Items[0].Category != domain.NotificationCategoryStorage || page.Items[0].OccurrenceCount != 1 || page.Items[0].TargetPath != "/dashboard/settings/backups" || page.Items[0].AuditEventID == "" {
			t.Fatalf("notification for %s = %#v", user.ID, page.Items[0])
		}
		linked, err := db.GetAuditEventForUserNotification(ctx, user.ID, page.Items[0].ID)
		if err != nil || linked.ID != page.Items[0].AuditEventID || linked.EventType != "storage.backup.failed" || linked.TargetID != event.ID {
			t.Fatalf("linked audit event for %s = %#v, %v", user.ID, linked, err)
		}
	}
	var linkedAuditCount int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE target_id = $1`, event.ID).Scan(&linkedAuditCount); err != nil || linkedAuditCount != 1 {
		t.Fatalf("notification audit event count = %d, %v", linkedAuditCount, err)
	}
	memberPage, err := db.ListUserNotifications(ctx, member.ID, domain.NotificationListOptions{})
	if err != nil || len(memberPage.Items) != 0 {
		t.Fatalf("member storage inbox = %#v, %v", memberPage.Items, err)
	}
	if got := sender.users(); !slices.Equal(got, []string{owner.ID}) {
		t.Fatalf("APNs users = %#v, want owner only; admin push preference is disabled", got)
	}
	var deliveries int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM web_push_deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("deliveries without VAPID = %d, %v", deliveries, err)
	}
}

func TestRenderStorageIntegrityWarningFromTypedFacts(t *testing.T) {
	t.Parallel()
	template, err := renderNotificationTemplate(NotificationEvent{
		Kind: "storage.checksum.success", HomeID: "home_1", ResourceID: "check_1",
		SourceEventKey: "storage:check_1", Operation: storageops.EventOperationChecksum,
		Status: storageops.EventStatusSuccess, IntegrityWarning: true,
	})
	if err != nil {
		t.Fatalf("renderNotificationTemplate: %v", err)
	}
	if template.title != "Storage Alert" || template.category != domain.NotificationCategoryStorage {
		t.Fatalf("template = %#v", template)
	}
}

type failingRecordingPushSender struct {
	mu      sync.Mutex
	userIDs []string
}

func (s *failingRecordingPushSender) Send(_ context.Context, device domain.APNSDevice, _ PushNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userIDs = append(s.userIDs, device.UserID)
	return errors.New("simulated APNs outage")
}

func (s *failingRecordingPushSender) users() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	users := append([]string(nil), s.userIDs...)
	slices.Sort(users)
	return users
}
