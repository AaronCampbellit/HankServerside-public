package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestAppsListRequiresHomeMembership(t *testing.T) {
	ctx := context.Background()
	db, testServer, tokens := setupAppsHTTPOnly(t, ctx)

	now := time.Now().UTC()
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID:              "home_apps",
		AppID:               "hermes",
		Name:                "Hermes",
		Version:             "1.0.0",
		Enabled:             true,
		PublicConfigJSON:    `{}`,
		SecretFieldsSetJSON: `{}`,
		CapabilitiesJSON:    `["apps.hermes.chat"]`,
		SlashCommandsJSON:   `[{"command":"/Hermes","command_id":"chat","description":"Send a prompt to Hermes."}]`,
		CommandsJSON:        `[{"id":"chat","mode":"request_response","timeout_seconds":120,"admin_only":true}]`,
		UserAccess:          domain.HomeAgentAppUserAccessHomeMembers,
		Status:              "installed",
		UpdatedAt:           now,
		UpdatedBy:           "usr_apps_admin",
	}))

	member := requestJSONStatus(t, testServer, tokens.member, http.MethodGet, "/v1/home/apps", nil, http.StatusOK)
	defer member.Body.Close()
	var payload protocol.AppsListResponse
	if err := json.NewDecoder(member.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Apps) != 1 || payload.Apps[0].ID != "hermes" {
		t.Fatalf("apps payload = %#v", payload)
	}
	got := payload.Apps[0]
	if !got.Enabled || len(got.SlashCommands) != 1 || got.SlashCommands[0].Command != "/Hermes" || got.SlashCommands[0].CommandID != "chat" {
		t.Fatalf("apps slash metadata = %#v", got)
	}
	if len(got.Commands) != 1 || got.Commands[0].ID != "chat" || len(got.Capabilities) != 1 || got.Capabilities[0] != "apps.hermes.chat" {
		t.Fatalf("apps command metadata = %#v", got)
	}

	outsider := requestJSONStatus(t, testServer, tokens.outsider, http.MethodGet, "/v1/home/apps", nil, http.StatusNotFound)
	outsider.Body.Close()
}

func TestAppsListFiltersMemberAccess(t *testing.T) {
	ctx := context.Background()
	db, testServer, tokens := setupAppsHTTPOnly(t, ctx)

	now := time.Now().UTC()
	for _, app := range []domain.HomeAgentApp{
		{
			HomeID:              "home_apps",
			AppID:               "members_app",
			Name:                "Members App",
			Version:             "1.0.0",
			Enabled:             true,
			PublicConfigJSON:    `{}`,
			SecretFieldsSetJSON: `{}`,
			SlashCommandsJSON:   `[{"command":"/members","command_id":"run","description":"Run member app."}]`,
			CommandsJSON:        `[{"id":"run","mode":"request_response","timeout_seconds":30}]`,
			UserAccess:          domain.HomeAgentAppUserAccessHomeMembers,
			Status:              "installed",
			UpdatedAt:           now,
			UpdatedBy:           "usr_apps_admin",
		},
		{
			HomeID:              "home_apps",
			AppID:               "admin_app",
			Name:                "Admin App",
			Version:             "1.0.0",
			Enabled:             true,
			PublicConfigJSON:    `{}`,
			SecretFieldsSetJSON: `{}`,
			SlashCommandsJSON:   `[{"command":"/admin","command_id":"run","description":"Run admin app."}]`,
			CommandsJSON:        `[{"id":"run","mode":"request_response","timeout_seconds":30}]`,
			UserAccess:          domain.HomeAgentAppUserAccessAdminsOnly,
			Status:              "installed",
			UpdatedAt:           now,
			UpdatedBy:           "usr_apps_admin",
		},
	} {
		must(t, db.UpsertHomeApp(ctx, app))
	}

	adminResponse := requestJSONStatus(t, testServer, tokens.admin, http.MethodGet, "/v1/home/apps", nil, http.StatusOK)
	defer adminResponse.Body.Close()
	var adminPayload protocol.AppsListResponse
	if err := json.NewDecoder(adminResponse.Body).Decode(&adminPayload); err != nil {
		t.Fatal(err)
	}
	if len(adminPayload.Apps) != 2 {
		t.Fatalf("admin apps = %#v", adminPayload.Apps)
	}

	memberResponse := requestJSONStatus(t, testServer, tokens.member, http.MethodGet, "/v1/home/apps", nil, http.StatusOK)
	defer memberResponse.Body.Close()
	var memberPayload protocol.AppsListResponse
	if err := json.NewDecoder(memberResponse.Body).Decode(&memberPayload); err != nil {
		t.Fatal(err)
	}
	if len(memberPayload.Apps) != 1 || memberPayload.Apps[0].ID != "members_app" || memberPayload.Apps[0].UserAccess != domain.HomeAgentAppUserAccessHomeMembers {
		t.Fatalf("member apps = %#v", memberPayload.Apps)
	}
}

func TestFilterAppSummariesForMembership(t *testing.T) {
	apps := []protocol.AppSummary{
		{ID: "members_app", Enabled: true, UserAccess: domain.HomeAgentAppUserAccessHomeMembers, SlashCommands: []protocol.AppSlashCommand{{Command: "/members", CommandID: "run"}}},
		{ID: "admin_app", Enabled: true, UserAccess: domain.HomeAgentAppUserAccessAdminsOnly, SlashCommands: []protocol.AppSlashCommand{{Command: "/admin", CommandID: "run"}}},
		{ID: "disabled_app", Enabled: false, UserAccess: domain.HomeAgentAppUserAccessHomeMembers, SlashCommands: []protocol.AppSlashCommand{{Command: "/disabled", CommandID: "run"}}},
		{ID: "legacy_app", Enabled: true, SlashCommands: []protocol.AppSlashCommand{{Command: "/legacy", CommandID: "run"}}},
	}

	admin := filterAppSummariesForMembership(apps, domain.HomeMembership{Role: domain.HomeRoleAdmin})
	if len(admin) != 4 {
		t.Fatalf("admin apps = %#v", admin)
	}

	member := filterAppSummariesForMembership(apps, domain.HomeMembership{Role: domain.HomeRoleMember})
	if len(member) != 1 || member[0].ID != "members_app" || len(member[0].SlashCommands) != 1 {
		t.Fatalf("member apps = %#v", member)
	}
}

func TestCanUseHomeAgentApp(t *testing.T) {
	admin := domain.HomeMembership{Role: domain.HomeRoleAdmin}
	member := domain.HomeMembership{Role: domain.HomeRoleMember}

	cases := []struct {
		name       string
		app        domain.HomeAgentApp
		membership domain.HomeMembership
		want       bool
	}{
		{
			name:       "admin can use admins only app",
			app:        domain.HomeAgentApp{Enabled: true, UserAccess: domain.HomeAgentAppUserAccessAdminsOnly},
			membership: admin,
			want:       true,
		},
		{
			name:       "member cannot use admins only app",
			app:        domain.HomeAgentApp{Enabled: true, UserAccess: domain.HomeAgentAppUserAccessAdminsOnly},
			membership: member,
			want:       false,
		},
		{
			name:       "member can use home members app",
			app:        domain.HomeAgentApp{Enabled: true, UserAccess: domain.HomeAgentAppUserAccessHomeMembers},
			membership: member,
			want:       true,
		},
		{
			name:       "member cannot use disabled home members app",
			app:        domain.HomeAgentApp{Enabled: false, UserAccess: domain.HomeAgentAppUserAccessHomeMembers},
			membership: member,
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canUseHomeAgentApp(tc.app, tc.membership); got != tc.want {
				t.Fatalf("canUseHomeAgentApp() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppsImportPreviewRequiresAdminAndOnlineAgent(t *testing.T) {
	ctx := context.Background()
	_, testServer, tokens := setupAppsHTTPOnly(t, ctx)

	member := doRawRequest(t, testServer, tokens.member, http.MethodPost, "/v1/home/apps/import/preview", []byte("package"))
	if member.StatusCode != http.StatusForbidden {
		t.Fatalf("member preview status = %d body=%s", member.StatusCode, member.Body)
	}
	admin := doRawRequest(t, testServer, tokens.admin, http.MethodPost, "/v1/home/apps/import/preview", []byte("package"))
	if admin.StatusCode != http.StatusConflict {
		t.Fatalf("offline preview status = %d body=%s", admin.StatusCode, admin.Body)
	}
}

func TestAppsListPrefersOnlineAgentSlashMetadata(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doJSONRequest(t, testServer, sessionToken, http.MethodGet, "/v1/home/apps", nil)
	}()

	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsList)
	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsListResponse{
		Apps: []protocol.AppSummary{{
			ID:           "ydownload",
			Name:         "YDownload",
			Version:      "1.0.0",
			Enabled:      true,
			Status:       "installed",
			Capabilities: []string{"apps.ydownload.download"},
			SlashCommands: []protocol.AppSlashCommand{{
				Command:     "/ydownload",
				CommandID:   "download",
				Description: "Download a YouTube video.",
			}},
			Commands: []protocol.AppCommandSummary{{
				ID:             "download",
				Mode:           "request_response",
				TimeoutSeconds: 900,
				AdminOnly:      true,
			}},
			PublicConfig: json.RawMessage(`{"source_id":"hankdemo"}`),
		}},
	})

	result := <-resultCh
	if result.StatusCode != http.StatusOK {
		t.Fatalf("apps list status = %d body=%s", result.StatusCode, result.Body)
	}
	var payload protocol.AppsListResponse
	if err := json.Unmarshal([]byte(result.Body), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Apps) != 1 || len(payload.Apps[0].SlashCommands) != 1 || payload.Apps[0].SlashCommands[0].Command != "/ydownload" {
		t.Fatalf("apps list payload = %#v", payload)
	}
	app, err := db.GetHomeApp(ctx, homeID, "ydownload")
	if err != nil {
		t.Fatalf("GetHomeApp: %v", err)
	}
	if app.SlashCommandsJSON == "" || app.CommandsJSON == "" || app.CapabilitiesJSON != `["apps.ydownload.download"]` {
		t.Fatalf("persisted app metadata = %#v", app)
	}
}

func TestAppsImportPreviewRoutesPackageToAgent(t *testing.T) {
	ctx := context.Background()
	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doRawRequest(t, testServer, sessionToken, http.MethodPost, "/v1/home/apps/import/preview", []byte("package-bytes"))
	}()

	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsPackagePreview)
	request := decodeRoutedCommandBody[protocol.AppsPackagePreviewRequest](t, envelope)
	if request.StagingID == "" || request.DownloadURL == "" || request.DownloadToken == "" {
		t.Fatalf("preview request = %#v", request)
	}
	packageRequest, err := http.NewRequest(http.MethodGet, request.DownloadURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	packageRequest.Header.Set("Authorization", "Bearer agent-token")
	packageRequest.Header.Set("X-Hank-Agent-ID", agentID)
	packageRequest.Header.Set("X-Hank-App-Package-Token", request.DownloadToken)
	packageResponse, err := http.DefaultClient.Do(packageRequest)
	if err != nil {
		t.Fatal(err)
	}
	packageData, _ := io.ReadAll(packageResponse.Body)
	packageResponse.Body.Close()
	if packageResponse.StatusCode != http.StatusOK || string(packageData) != "package-bytes" {
		t.Fatalf("package download status=%d body=%q", packageResponse.StatusCode, string(packageData))
	}

	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsPackagePreviewResponse{
		StagingID: request.StagingID,
		App:       protocol.AppSummary{ID: "hermes", Name: "Hermes", Version: "1.0.0", Status: "preview"},
	})
	result := <-resultCh
	if result.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", result.StatusCode, result.Body)
	}
}

func TestAppsImportPreviewReturnsStructuredAgentError(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doRawRequest(t, testServer, sessionToken, http.MethodPost, "/v1/home/apps/import/preview", []byte("not-a-package"))
	}()

	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsPackagePreview)
	writeAgentError(t, ctx, agentConn, envelope, agentID, homeID, "app_package_invalid", "package validation failed")
	result := <-resultCh
	if result.StatusCode != http.StatusBadRequest {
		t.Fatalf("preview status = %d body=%s", result.StatusCode, result.Body)
	}
	var body struct {
		Error   string         `json:"error"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error != "app_package_invalid" || body.Message != "package validation failed" || body.Details["stage"] != "agent_preview" {
		t.Fatalf("error body = %#v", body)
	}
	events, err := db.ListAuditEvents(ctx, homeID, "app_package.preview_failed", auditSeverityWarning, "app_package", 10, "occurred_at", "desc")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 || events[0].TargetID == "" || !strings.Contains(events[0].MetadataJSON, "app_package_invalid") {
		t.Fatalf("preview audit events = %#v", events)
	}
}

func TestAppsActivatePersistsReturnedAppMetadata(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doJSONRequest(t, testServer, sessionToken, http.MethodPost, "/v1/home/apps/import/activate", map[string]any{
			"staging_id": "stage_1",
			"enable":     true,
		})
	}()

	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsPackageActivate)
	request := decodeRoutedCommandBody[protocol.AppsPackageActivateRequest](t, envelope)
	if request.StagingID != "stage_1" || !request.Enable {
		t.Fatalf("activate request = %#v", request)
	}
	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsPackageActivateResponse{
		App: protocol.AppSummary{
			ID:           "hermes",
			Name:         "Hermes",
			Version:      "1.0.0",
			Enabled:      true,
			Status:       "installed",
			Capabilities: []string{"apps.hermes.chat"},
			SlashCommands: []protocol.AppSlashCommand{{
				Command:     "/Hermes",
				CommandID:   "chat",
				Description: "Send a prompt to Hermes.",
			}},
			Commands: []protocol.AppCommandSummary{{
				ID:             "chat",
				Mode:           "request_response",
				TimeoutSeconds: 120,
				AdminOnly:      true,
			}},
			PublicConfig:    json.RawMessage(`{"api_base_url":"https://hermes.local"}`),
			SecretFieldsSet: map[string]bool{"api_key": true},
			SettingsSchema: protocol.AppSettingsSchema{
				Fields: []protocol.AppSettingsField{{
					Key:   "api_base_url",
					Label: "Hermes URL",
					Type:  "url",
				}},
			},
		},
	})
	result := <-resultCh
	if result.StatusCode != http.StatusOK {
		t.Fatalf("activate status = %d body=%s", result.StatusCode, result.Body)
	}
	var activation protocol.AppsPackageActivateResponse
	if err := json.Unmarshal([]byte(result.Body), &activation); err != nil {
		t.Fatal(err)
	}
	if activation.App.UserAccess != domain.HomeAgentAppUserAccessAdminsOnly {
		t.Fatalf("activation user access = %q", activation.App.UserAccess)
	}
	app, err := db.GetHomeApp(ctx, homeID, "hermes")
	if err != nil {
		t.Fatalf("GetHomeApp: %v", err)
	}
	if !app.Enabled || app.PublicConfigJSON != `{"api_base_url":"https://hermes.local"}` || app.SecretFieldsSetJSON != `{"api_key":true}` || app.SettingsSchemaJSON == "" {
		t.Fatalf("persisted app = %#v", app)
	}
	if app.CapabilitiesJSON != `["apps.hermes.chat"]` || app.SlashCommandsJSON == "" || app.CommandsJSON == "" {
		t.Fatalf("persisted app = %#v", app)
	}
	var slashCommands []protocol.AppSlashCommand
	if err := json.Unmarshal([]byte(app.SlashCommandsJSON), &slashCommands); err != nil {
		t.Fatal(err)
	}
	if len(slashCommands) != 1 || slashCommands[0].Command != "/Hermes" {
		t.Fatalf("persisted slash commands = %#v", slashCommands)
	}
}

func TestAppsUninstallRemovesAgentPackageAndPersistedMetadata(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	now := time.Now().UTC()
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID: homeID, AppID: "gramaton", Name: "Gramaton", Version: "1.0.0",
		Enabled: true, PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{"password":true}`,
		Status: "installed", UpdatedAt: now, UpdatedBy: "usr_1",
	}))

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doJSONRequest(t, testServer, sessionToken, http.MethodDelete, "/v1/home/apps/gramaton", nil)
	}()
	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsUninstall)
	request := decodeRoutedCommandBody[protocol.AppsUninstallRequest](t, envelope)
	if request.AppID != "gramaton" {
		t.Fatalf("uninstall request = %#v", request)
	}
	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsUninstallResponse{AppID: "gramaton"})

	result := <-resultCh
	if result.StatusCode != http.StatusOK {
		t.Fatalf("uninstall status = %d body=%s", result.StatusCode, result.Body)
	}
	if _, err := db.GetHomeApp(ctx, homeID, "gramaton"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetHomeApp after uninstall error = %v, want ErrNotFound", err)
	}
	events, err := db.ListAuditEvents(ctx, homeID, "app.uninstalled", auditSeverityInfo, "app", 10, "occurred_at", "desc")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 || events[0].TargetID != "gramaton" {
		t.Fatalf("uninstall audit events = %#v", events)
	}
}

func TestAppsUninstallRequiresAdminAndOnlineAgent(t *testing.T) {
	ctx := context.Background()
	db, testServer, tokens := setupAppsHTTPOnly(t, ctx)
	now := time.Now().UTC()
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID: "home_apps", AppID: "gramaton", Name: "Gramaton", Version: "1.0.0",
		PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, Status: "installed",
		UpdatedAt: now, UpdatedBy: "usr_apps_admin",
	}))

	member := requestJSONStatus(t, testServer, tokens.member, http.MethodDelete, "/v1/home/apps/gramaton", nil, http.StatusForbidden)
	member.Body.Close()
	admin := requestJSONStatus(t, testServer, tokens.admin, http.MethodDelete, "/v1/home/apps/gramaton", nil, http.StatusConflict)
	admin.Body.Close()
	if _, err := db.GetHomeApp(ctx, "home_apps", "gramaton"); err != nil {
		t.Fatalf("offline uninstall removed metadata: %v", err)
	}
}

func TestAppsUninstallRejectsMismatchedAgentResponse(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID: homeID, AppID: "gramaton", Name: "Gramaton", Version: "1.0.0",
		PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, Status: "installed",
		UpdatedAt: time.Now().UTC(), UpdatedBy: "usr_1",
	}))

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doJSONRequest(t, testServer, sessionToken, http.MethodDelete, "/v1/home/apps/gramaton", nil)
	}()
	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsUninstall)
	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsUninstallResponse{AppID: "other"})

	result := <-resultCh
	if result.StatusCode != http.StatusBadGateway {
		t.Fatalf("mismatched uninstall status = %d body=%s", result.StatusCode, result.Body)
	}
	if _, err := db.GetHomeApp(ctx, homeID, "gramaton"); err != nil {
		t.Fatalf("mismatched uninstall removed metadata: %v", err)
	}
}

func TestAppsConfigApplyRoutesToAgent(t *testing.T) {
	ctx := context.Background()
	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "test done")

	resultCh := make(chan testJSONResponse, 1)
	go func() {
		resultCh <- doJSONRequest(t, testServer, sessionToken, http.MethodPut, "/v1/home/apps/hermes/config", map[string]any{
			"public_config": map[string]any{"api_base_url": "https://hermes.local"},
			"secrets":       map[string]any{"api_key": "secret"},
			"enable":        true,
			"user_access":   "home_members",
		})
	}()

	envelope := readAgentCommandEnvelope(t, ctx, agentConn, protocol.CommandAppsConfigApply)
	request := decodeRoutedCommandBody[protocol.AppsConfigApplyRequest](t, envelope)
	if request.AppID != "hermes" || string(request.PublicConfig) != `{"api_base_url":"https://hermes.local"}` || request.Enable == nil || !*request.Enable {
		t.Fatalf("config apply request = %#v public=%s", request, request.PublicConfig)
	}
	writeAgentResponse(t, ctx, agentConn, envelope, agentID, homeID, protocol.AppsConfigApplyResponse{
		App: protocol.AppSummary{
			ID:              "hermes",
			Name:            "Hermes",
			Version:         "1.0.0",
			Enabled:         true,
			Status:          "installed",
			PublicConfig:    json.RawMessage(`{"api_base_url":"https://hermes.local"}`),
			SecretFieldsSet: map[string]bool{"api_key": true},
		},
	})
	result := <-resultCh
	if result.StatusCode != http.StatusOK {
		t.Fatalf("config status = %d body=%s", result.StatusCode, result.Body)
	}
	app, err := db.GetHomeApp(ctx, homeID, "hermes")
	if err != nil {
		t.Fatalf("GetHomeApp: %v", err)
	}
	if app.SecretFieldsSetJSON != `{"api_key":true}` {
		t.Fatalf("persisted app = %#v", app)
	}
	if app.UserAccess != domain.HomeAgentAppUserAccessHomeMembers {
		t.Fatalf("persisted app user access = %q", app.UserAccess)
	}
}

type appsHTTPOnlyTokens struct {
	admin    string
	member   string
	outsider string
}

func setupAppsHTTPOnly(t *testing.T, ctx context.Context) (*store.Store, *httptest.Server, appsHTTPOnlyTokens) {
	t.Helper()
	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_apps_admin", Email: "apps-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_apps_member", Email: "apps-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	outsider := domain.User{ID: "usr_apps_outsider", Email: "apps-outsider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_apps", UserID: admin.ID, Name: "Apps Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_apps", HomeID: home.ID, Name: "Apps Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	for _, user := range []domain.User{admin, member, outsider} {
		must(t, db.CreateUser(ctx, user))
	}
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.UpsertAgent(ctx, agent))

	tokens := appsHTTPOnlyTokens{admin: "apps-admin-token", member: "apps-member-token", outsider: "apps-outsider-token"}
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_apps_admin", UserID: admin.ID, TokenHash: hashToken(tokens.admin), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_apps_member", UserID: member.ID, TokenHash: hashToken(tokens.member), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_apps_outsider", UserID: outsider.ID, TokenHash: hashToken(tokens.outsider), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	return db, httptest.NewServer(server.http.Handler), tokens
}

func doRawRequest(t *testing.T, server *httptest.Server, sessionToken string, method string, path string, body []byte) testJSONResponse {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if sessionToken != "" {
		request.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return testJSONResponse{StatusCode: response.StatusCode, Body: string(data)}
}

func readAgentCommandEnvelope(t *testing.T, ctx context.Context, conn *websocket.Conn, command string) protocol.Envelope {
	t.Helper()
	var envelope protocol.Envelope
	if err := wsjson.Read(ctx, conn, &envelope); err != nil {
		t.Fatalf("agent read command: %v", err)
	}
	if envelope.Type != protocol.TypeCloudCommand {
		t.Fatalf("envelope type = %q, want %q", envelope.Type, protocol.TypeCloudCommand)
	}
	var routed protocol.RoutedCommand
	if err := json.Unmarshal(envelope.Payload, &routed); err != nil {
		t.Fatalf("decode routed command: %v", err)
	}
	if routed.Command != command {
		t.Fatalf("command = %q, want %q", routed.Command, command)
	}
	return envelope
}

func decodeRoutedCommandBody[T any](t *testing.T, envelope protocol.Envelope) T {
	t.Helper()
	var routed protocol.RoutedCommand
	if err := json.Unmarshal(envelope.Payload, &routed); err != nil {
		t.Fatalf("decode routed command: %v", err)
	}
	var body T
	if err := json.Unmarshal(routed.Body, &body); err != nil {
		t.Fatalf("decode routed body: %v", err)
	}
	return body
}

func writeAgentResponse(t *testing.T, ctx context.Context, conn *websocket.Conn, request protocol.Envelope, agentID string, homeID string, payload any) {
	t.Helper()
	response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, request.RequestID, agentID, homeID, payload)
	if err != nil {
		t.Fatalf("NewEnvelope response: %v", err)
	}
	if err := wsjson.Write(ctx, conn, response); err != nil {
		t.Fatalf("agent response write: %v", err)
	}
}

func writeAgentError(t *testing.T, ctx context.Context, conn *websocket.Conn, request protocol.Envelope, agentID string, homeID string, code string, message string) {
	t.Helper()
	response := protocol.NewErrorEnvelope(protocol.TypeCloudResponse, request.RequestID, agentID, homeID, code, message, nil)
	if err := wsjson.Write(ctx, conn, response); err != nil {
		t.Fatalf("agent error write: %v", err)
	}
}
