package cloud

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestRecoveryExportRequiresAdmin(t *testing.T) {
	t.Parallel()

	testServer, _, memberToken := setupRecoveryTestServer(t)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/home/recovery/export", nil, http.StatusForbidden)
	response.Body.Close()
}

func TestRecoveryExportRedactsSecretsAndListsMissingSecretPrompts(t *testing.T) {
	t.Parallel()

	testServer, adminToken, _ := setupRecoveryTestServer(t)
	defer testServer.Close()

	var bundle recoveryBundle
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/home/recovery/export", nil, &bundle)

	if bundle.SchemaVersion != 2 || bundle.Product != "hank" {
		t.Fatalf("bundle identity = version %d product %q", bundle.SchemaVersion, bundle.Product)
	}
	if bundle.Home.Name != "Recovery Home" {
		t.Fatalf("home name = %q", bundle.Home.Name)
	}
	if len(bundle.ServiceProfiles) != 3 {
		t.Fatalf("service profile count = %d, want 3", len(bundle.ServiceProfiles))
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"ha-secret-token", "smb-secret-password", "hermes-secret-key", "agent-secret-token", "db-secret-password"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("export leaked secret %q in %s", forbidden, text)
		}
	}
	for _, want := range []string{"homeassistant.token", "smb.media.password", "hermes.api_key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing required secret descriptor %q in %s", want, text)
		}
	}
	if bundle.EnvTemplates.Cloud["HANK_SECRET_ENCRYPTION_KEY"] != "" || bundle.EnvTemplates.Agent["HANK_AGENT_TOKEN"] != "" {
		t.Fatalf("env secret templates were not blank: %#v", bundle.EnvTemplates)
	}
}

func TestRecoveryImportPreviewValidatesAndReportsRequiredSecrets(t *testing.T) {
	t.Parallel()

	testServer, adminToken, _ := setupRecoveryTestServer(t)
	defer testServer.Close()

	bundle := recoveryBundle{
		SchemaVersion: 2,
		Product:       "hank",
		Home:          recoveryHome{Name: "Imported Home"},
		ServiceProfiles: []recoveryServiceProfile{{
			ServiceType:  domain.ServiceTypeSMB,
			PublicConfig: json.RawMessage(`{"shares":[{"id":"media","name":"Media","host":"nas.local","share":"Media","username":"aaron"}]}`),
		}},
	}
	var preview recoveryImportPreview
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/recovery/import/preview", bundle, &preview)

	if preview.Valid != true {
		t.Fatalf("preview valid = %v", preview.Valid)
	}
	if len(preview.Changes) == 0 {
		t.Fatal("expected preview changes")
	}
	if len(preview.RequiredSecrets) != 1 || preview.RequiredSecrets[0].ID != "smb.media.password" {
		t.Fatalf("required secrets = %#v", preview.RequiredSecrets)
	}
}

func TestRecoveryImportApplyWritesNonSecretSettingsAndLeavesServiceProfilePending(t *testing.T) {
	t.Parallel()

	testServer, adminToken, _ := setupRecoveryTestServer(t)
	defer testServer.Close()

	bundle := recoveryBundle{
		SchemaVersion: 2,
		Product:       "hank",
		Home:          recoveryHome{Name: "Imported Home"},
		Settings: recoverySettings{
			Profile:   json.RawMessage(`{"dashboard":{"tiles":["light.kitchen"]}}`),
			Assistant: json.RawMessage(`{"files_enabled":false,"chat_model":"gpt-4.1","prompt_profile":"chatgpt"}`),
		},
		ServiceProfiles: []recoveryServiceProfile{{
			ServiceType:  domain.ServiceTypeHomeAssistant,
			PublicConfig: json.RawMessage(`{"base_url":"http://ha.local:8123","timeout_seconds":30}`),
		}},
	}
	var result recoveryImportApplyResult
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/recovery/import/apply", map[string]any{
		"bundle":  bundle,
		"confirm": true,
	}, &result)

	if !result.Applied {
		t.Fatalf("apply result = %#v", result)
	}
	var home domain.Home
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/home", nil, &home)
	if home.Name != "Imported Home" {
		t.Fatalf("home name = %q, want Imported Home", home.Name)
	}
	var profiles struct {
		Profiles []domain.HomeServiceProfile `json:"profiles"`
	}
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/home/service-profiles", nil, &profiles)
	profile := profileByServiceType(profiles.Profiles, domain.ServiceTypeHomeAssistant)
	if profile == nil || profile.Status != domain.SyncStatusPending || profile.LastError != "secret required" {
		t.Fatalf("homeassistant profile = %#v", profile)
	}
	var profileSettings struct {
		Settings json.RawMessage `json:"settings"`
	}
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/me/profile", nil, &profileSettings)
	if !strings.Contains(string(profileSettings.Settings), "light.kitchen") {
		t.Fatalf("profile settings not imported: %s", string(profileSettings.Settings))
	}
}

func setupRecoveryTestServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()

	ctx := context.Background()
	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_recovery_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_recovery_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_recovery", UserID: admin.ID, Name: "Recovery Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "recovery-admin-token"
	memberToken := "recovery-member-token"

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_recovery_admin", UserID: admin.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_recovery_member", UserID: member.ID, TokenHash: hashToken(memberToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	if _, err := db.SaveUserProfileSettings(ctx, admin.ID, nil, json.RawMessage(`{"dashboard":{"density":"compact"}}`)); err != nil {
		t.Fatalf("SaveUserProfileSettings: %v", err)
	}
	must(t, db.UpsertAssistantSettings(ctx, domain.AssistantSettings{
		HomeID:               home.ID,
		UserID:               admin.ID,
		ProfileNotesEnabled:  true,
		HomeNotesEnabled:     true,
		FilesEnabled:         true,
		CalendarEnabled:      true,
		HomeAssistantEnabled: true,
		ProjectDocsEnabled:   true,
		ConversationsEnabled: true,
		SystemPrompt:         "custom prompt",
		MaxContextItems:      12,
		AIProvider:           "openai",
		ChatModel:            "gpt-4.1",
		PromptProfile:        "custom",
		PlannerEnabled:       true,
		UpdatedBy:            admin.ID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}))
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{
		HomeID:           home.ID,
		ServiceType:      domain.ServiceTypeHomeAssistant,
		PublicConfigJSON: `{"base_url":"http://ha.local:8123","timeout_seconds":15,"token":"ha-secret-token"}`,
		SecretVersion:    1,
		AppliedVersion:   1,
		Status:           domain.SyncStatusHealthy,
		UpdatedAt:        now,
		UpdatedBy:        admin.ID,
	}))
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{
		HomeID:           home.ID,
		ServiceType:      domain.ServiceTypeSMB,
		PublicConfigJSON: `{"shares":[{"id":"media","name":"Media","host":"nas.local","share":"Media","username":"aaron","password":"smb-secret-password"}]}`,
		SecretVersion:    1,
		AppliedVersion:   1,
		Status:           domain.SyncStatusHealthy,
		UpdatedAt:        now,
		UpdatedBy:        admin.ID,
	}))
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{
		HomeID:           home.ID,
		ServiceType:      domain.ServiceTypeHermes,
		PublicConfigJSON: `{"api_base_url":"http://hermes.local:8642","model":"hermes-agent","api_key":"hermes-secret-key","api_key_set":true}`,
		SecretVersion:    1,
		AppliedVersion:   1,
		Status:           domain.SyncStatusHealthy,
		UpdatedAt:        now,
		UpdatedBy:        admin.ID,
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	return httptest.NewServer(server.http.Handler), adminToken, memberToken
}

func profileByServiceType(profiles []domain.HomeServiceProfile, serviceType string) *domain.HomeServiceProfile {
	for i := range profiles {
		if profiles[i].ServiceType == serviceType {
			return &profiles[i]
		}
	}
	return nil
}
