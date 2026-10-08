package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestEntraSSOSettingsEncryptClientSecretAndNeverExposeItInStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	if err := db.ConfigureSecretEncryption("entra-settings-test-key"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := domain.User{ID: "usr_entra_settings", Email: "entra-settings@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	home := domain.Home{ID: "home_entra_settings", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	value := domain.EntraSSOSettings{HomeID: home.ID, Enabled: true, TenantID: "tenant", ClientID: "client", ClientSecret: "client-secret", PublicBaseURL: "https://hank.example", UpdatedAt: now, UpdatedBy: user.ID}
	if err := db.UpsertEntraSSOSettings(ctx, value); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.queryRow(ctx, `SELECT client_secret FROM entra_sso_settings WHERE home_id = ?`, home.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, encryptedSecretPrefix) || strings.Contains(stored, value.ClientSecret) {
		t.Fatalf("stored secret is not encrypted: %q", stored)
	}
	loaded, err := db.GetEntraSSOSettings(ctx, home.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClientSecret != value.ClientSecret {
		t.Fatalf("ClientSecret = %q", loaded.ClientSecret)
	}
}
