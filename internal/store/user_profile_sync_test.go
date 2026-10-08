package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestUserProfileSettingsAndSecretVaultNotify(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	notifications, err := db.Listen(ctx, NotificationChannelProfiles)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_sync", Email: "profile-sync@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	settings, err := db.SaveUserProfileSettings(ctx, user.ID, nil, json.RawMessage(`{"dashboard":{"tiles":["light.kitchen"]}}`))
	if err != nil {
		t.Fatalf("SaveUserProfileSettings: %v", err)
	}
	if settings.Revision != 1 {
		t.Fatalf("settings revision = %d, want 1", settings.Revision)
	}

	vault, err := db.SaveUserProfileSecretVault(ctx, user.ID, nil, "device-key-1", json.RawMessage(`{"ciphertext":"abc","nonce":"xyz"}`))
	if err != nil {
		t.Fatalf("SaveUserProfileSecretVault: %v", err)
	}
	if vault.Revision != 1 || vault.KeyID != "device-key-1" {
		t.Fatalf("vault = %+v, want revision 1 key device-key-1", vault)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case notification := <-notifications:
			var payload struct {
				Event    string `json:"event"`
				UserID   string `json:"user_id"`
				Revision int    `json:"revision"`
			}
			if err := json.Unmarshal(notification.Payload, &payload); err != nil {
				t.Fatalf("notification payload json: %v", err)
			}
			if payload.UserID != user.ID || payload.Revision != 1 {
				t.Fatalf("notification payload = %+v", payload)
			}
			seen[payload.Event] = true
		case <-ctx.Done():
			t.Fatalf("timed out waiting for profile notifications, seen=%v", seen)
		}
	}
	if !seen["profile.settings_changed"] || !seen["profile.secret_vault_changed"] {
		t.Fatalf("seen notifications = %v", seen)
	}
}
