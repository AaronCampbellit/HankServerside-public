package store

import (
	"context"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestNotificationSettingsPersistAllPushCategories(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_notification_settings", Email: "notification-settings@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	want := domain.NotificationSettings{
		UserID:                   user.ID,
		MonitoringEnabled:        true,
		AgentHealthEnabled:       false,
		QuickLinksEnabled:        true,
		StorageEnabled:           false,
		NotesEnabled:             true,
		DashboardEntitiesEnabled: false,
	}
	if _, err := db.SaveNotificationSettings(ctx, want); err != nil {
		t.Fatalf("SaveNotificationSettings: %v", err)
	}
	got, err := db.GetNotificationSettings(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	if got.MonitoringEnabled != want.MonitoringEnabled || got.AgentHealthEnabled != want.AgentHealthEnabled || got.QuickLinksEnabled != want.QuickLinksEnabled || got.StorageEnabled != want.StorageEnabled || got.NotesEnabled != want.NotesEnabled || got.DashboardEntitiesEnabled != want.DashboardEntitiesEnabled {
		t.Fatalf("settings = %#v, want category flags %#v", got, want)
	}
}

func TestNotificationSettingsColumnsDefaultEnabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_notification_defaults", Email: "notification-defaults@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := db.exec(ctx, `INSERT INTO notification_settings (user_id, updated_at) VALUES (?, ?)`, user.ID, now); err != nil {
		t.Fatalf("insert defaults: %v", err)
	}
	got, err := db.GetNotificationSettings(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	if !got.MonitoringEnabled || !got.AgentHealthEnabled || !got.QuickLinksEnabled || !got.StorageEnabled || !got.NotesEnabled || !got.DashboardEntitiesEnabled {
		t.Fatalf("default settings = %#v, want every category enabled", got)
	}
}
