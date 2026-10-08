package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAppMetadataStoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_apps_roundtrip", Email: "apps-roundtrip@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_apps_roundtrip", UserID: user.ID, Name: "Apps Home", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))

	app := domain.HomeAgentApp{
		HomeID:              home.ID,
		AppID:               "hermes",
		Name:                "Hermes",
		Version:             "1.0.0",
		Enabled:             true,
		PublicConfigJSON:    `{"api_base_url":"https://hermes.local"}`,
		SecretFieldsSetJSON: `{"api_key":true}`,
		SettingsSchemaJSON:  `{"fields":[{"key":"api_base_url","label":"Hermes URL","type":"url"}]}`,
		UserAccess:          "home_members",
		Status:              "installed",
		LastError:           "",
		UpdatedAt:           now,
		UpdatedBy:           user.ID,
	}
	mustStore(t, db.UpsertHomeApp(ctx, app))

	loaded, err := db.GetHomeApp(ctx, home.ID, "hermes")
	if err != nil {
		t.Fatalf("GetHomeApp: %v", err)
	}
	assertHomeAgentAppEqual(t, loaded, app)

	updated := app
	updated.Version = "1.0.1"
	updated.Enabled = false
	updated.PublicConfigJSON = `{"api_base_url":"https://new-hermes.local"}`
	updated.SecretFieldsSetJSON = `{"api_key":true,"other":false}`
	updated.SettingsSchemaJSON = `{"fields":[{"key":"api_base_url","label":"Hermes URL","type":"url"},{"key":"api_key","type":"password","secret":true}]}`
	updated.UserAccess = "admins_only"
	updated.Status = "degraded"
	updated.LastError = "agent offline"
	updated.UpdatedAt = now.Add(time.Minute)
	mustStore(t, db.UpsertHomeApp(ctx, updated))

	loaded, err = db.GetHomeApp(ctx, home.ID, "hermes")
	if err != nil {
		t.Fatalf("GetHomeApp updated: %v", err)
	}
	assertHomeAgentAppEqual(t, loaded, updated)

	if err := db.DeleteHomeApp(ctx, home.ID, "hermes"); err != nil {
		t.Fatalf("DeleteHomeApp: %v", err)
	}
	if _, err := db.GetHomeApp(ctx, home.ID, "hermes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHomeApp after delete error = %v, want ErrNotFound", err)
	}
	if err := db.DeleteHomeApp(ctx, home.ID, "hermes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteHomeApp missing error = %v, want ErrNotFound", err)
	}
}

func TestAppMetadataListOrdersByName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_apps_list", Email: "apps-list@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_apps_list", UserID: user.ID, Name: "Apps Home", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))

	for _, app := range []domain.HomeAgentApp{
		{HomeID: home.ID, AppID: "zeta", Name: "Zeta", Version: "1.0.0", Status: "installed", PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, UserAccess: "home_members", UpdatedAt: now, UpdatedBy: user.ID},
		{HomeID: home.ID, AppID: "alpha", Name: "Alpha", Version: "1.0.0", Status: "installed", PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, UpdatedAt: now, UpdatedBy: user.ID},
		{HomeID: home.ID, AppID: "beta", Name: "Alpha", Version: "1.0.0", Status: "installed", PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, UserAccess: "admins_only", UpdatedAt: now, UpdatedBy: user.ID},
	} {
		mustStore(t, db.UpsertHomeApp(ctx, app))
	}

	apps, err := db.ListHomeApps(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListHomeApps: %v", err)
	}
	got := make([]string, 0, len(apps))
	for _, app := range apps {
		got = append(got, app.AppID)
	}
	want := []string{"alpha", "beta", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("app ids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("app ids = %#v, want %#v", got, want)
		}
	}
	if apps[0].UserAccess != "admins_only" || apps[1].UserAccess != "admins_only" || apps[2].UserAccess != "home_members" {
		t.Fatalf("app access = %q, %q, %q", apps[0].UserAccess, apps[1].UserAccess, apps[2].UserAccess)
	}
}

func assertHomeAgentAppEqual(t *testing.T, got domain.HomeAgentApp, want domain.HomeAgentApp) {
	t.Helper()
	if got.HomeID != want.HomeID ||
		got.AppID != want.AppID ||
		got.Name != want.Name ||
		got.Version != want.Version ||
		got.Enabled != want.Enabled ||
		got.PublicConfigJSON != want.PublicConfigJSON ||
		got.SecretFieldsSetJSON != want.SecretFieldsSetJSON ||
		got.SettingsSchemaJSON != want.SettingsSchemaJSON ||
		got.UserAccess != want.UserAccess ||
		got.Status != want.Status ||
		got.LastError != want.LastError ||
		got.UpdatedBy != want.UpdatedBy ||
		!got.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(want.UpdatedAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("app = %#v, want %#v", got, want)
	}
}

func TestAppPermissionCacheRequiresJSONObject(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "app-grants", Email: "app-grants@example.test", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	home := domain.Home{ID: "app-grants", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateHome(ctx, home))
	app := domain.HomeAgentApp{HomeID: home.ID, AppID: "test", Name: "Test", Version: "1", PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, UpdatedBy: user.ID, UpdatedAt: now, PermissionsJSON: `{"grants":{"network:base":"fingerprint"}}`}
	mustStore(t, db.UpsertHomeApp(ctx, app))
	loaded, err := db.GetHomeApp(ctx, home.ID, app.AppID)
	if err != nil || !strings.Contains(loaded.PermissionsJSON, "fingerprint") {
		t.Fatal("permission cache did not round trip", err)
	}
	app.PermissionsJSON = `[]`
	if err := db.UpsertHomeApp(ctx, app); err == nil {
		t.Fatal("non-object permission state was stored")
	}
}
