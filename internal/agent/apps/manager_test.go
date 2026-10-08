package apps

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestManagerListEmptyDirectory(t *testing.T) {
	t.Parallel()
	manager := NewManager(filepath.Join(t.TempDir(), "apps"), filepath.Join(t.TempDir(), "staging"), testRunner(Runner{}))

	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	response := manager.List(context.Background())
	if len(response.Apps) != 0 {
		t.Fatalf("apps = %#v, want empty", response.Apps)
	}
}

func TestManagerPreviewAndActivateHermesPackage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesRuntimeScript(`{"installed":true}`))
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{}))

	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if preview.StagingID != "stage_1" || preview.App.ID != "hermes" || preview.Replacing {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.PackageSHA256 != fileSHA256(t, archivePath) {
		t.Fatalf("PackageSHA256 = %q, want archive hash", preview.PackageSHA256)
	}

	activated, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{StagingID: "stage_1"})
	if err != nil {
		t.Fatalf("ActivatePackage error: %v", err)
	}
	if activated.App.ID != "hermes" || activated.App.Enabled {
		t.Fatalf("activated app = %#v, want installed disabled hermes app", activated.App)
	}
	if _, err := os.Stat(filepath.Join(appsDir, "hermes", "app.json")); err != nil {
		t.Fatalf("installed app.json missing: %v", err)
	}
}

func TestManagerUninstallRemovesPackageStateAndCapabilities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackageWithScript(t, true, hermesSecretEchoScript())
	if _, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:   "hermes",
		Secrets: json.RawMessage(`{"api_key":"delete-me"}`),
	}); err != nil {
		t.Fatalf("ConfigApply error: %v", err)
	}
	status, err := manager.ConfigStatus(ctx, protocol.AppsConfigStatusRequest{AppID: "hermes"})
	if err != nil || len(status.Apps) != 1 {
		t.Fatalf("ConfigStatus before uninstall = %#v, %v", status, err)
	}
	appPath := manager.apps["hermes"].Path

	response, err := manager.Uninstall(ctx, protocol.AppsUninstallRequest{AppID: "hermes"})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if response.AppID != "hermes" {
		t.Fatalf("Uninstall AppID = %q", response.AppID)
	}
	if _, err := os.Stat(appPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed app path still exists: %v", err)
	}
	if apps := manager.List(ctx).Apps; len(apps) != 0 {
		t.Fatalf("apps after uninstall = %#v", apps)
	}
	if capabilities := manager.Capabilities(); len(capabilities) != 0 {
		t.Fatalf("capabilities after uninstall = %#v", capabilities)
	}
	if _, err := manager.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin", AppID: "hermes", CommandID: "chat"}); !errors.Is(err, ErrUnknownApp) {
		t.Fatalf("Invoke after uninstall error = %v, want ErrUnknownApp", err)
	}

	// Retrying is safe when an earlier cloud response was lost after the agent
	// had already removed the package.
	if _, err := manager.Uninstall(ctx, protocol.AppsUninstallRequest{AppID: "hermes"}); err != nil {
		t.Fatalf("idempotent Uninstall error: %v", err)
	}
}

func TestManagerUninstallRejectsUnsafeAppID(t *testing.T) {
	t.Parallel()
	manager := installManagerHermesPackage(t, false)
	if _, err := manager.Uninstall(context.Background(), protocol.AppsUninstallRequest{AppID: "../hermes"}); !errors.Is(err, ErrPermissionRefused) {
		t.Fatalf("Uninstall error = %v, want ErrPermissionRefused", err)
	}
	if apps := manager.List(context.Background()).Apps; len(apps) != 1 || apps[0].ID != "hermes" {
		t.Fatalf("apps after rejected uninstall = %#v", apps)
	}
}

func TestManagerPreviewAndActivateFolderWrappedPackage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerFolderWrappedPackage(t, t.TempDir(), hermesRuntimeScript(`{"installed":true}`))
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{}))

	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_wrapped",
		DownloadURL: fileURL(t, archivePath),
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if preview.App.ID != "hermes" {
		t.Fatalf("preview app = %#v, want hermes", preview.App)
	}

	if _, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{StagingID: "stage_wrapped"}); err != nil {
		t.Fatalf("ActivatePackage error: %v", err)
	}
	appRoot := filepath.Join(appsDir, "hermes")
	if _, err := os.Stat(filepath.Join(appRoot, "app.json")); err != nil {
		t.Fatalf("installed app.json missing from app root: %v", err)
	}
	runtimeInfo, err := os.Stat(filepath.Join(appRoot, "bin", "hermes-app"))
	if err != nil {
		t.Fatalf("installed runtime missing from app root: %v", err)
	}
	if runtimeInfo.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed runtime mode = %o, want owner executable", runtimeInfo.Mode().Perm())
	}
	for _, unwanted := range []string{"hermes-source", "__MACOSX", ".DS_Store", "._app.json"} {
		if _, err := os.Stat(filepath.Join(appRoot, unwanted)); !os.IsNotExist(err) {
			t.Fatalf("unwanted archive entry %q was installed: %v", unwanted, err)
		}
	}
}

func TestManagerActivateRebuildsPreviewFromStagedPackageAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesRuntimeScript(`{"installed":true}`))
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{}))
	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}

	restarted := NewManager(appsDir, stagingDir, testRunner(Runner{}))
	activated, err := restarted.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
		StagingID:     "stage_1",
		PackageSHA256: preview.PackageSHA256,
	})
	if err != nil {
		t.Fatalf("ActivatePackage after restart error: %v", err)
	}
	if activated.App.ID != "hermes" {
		t.Fatalf("activated app = %#v", activated.App)
	}
}

func TestManagerActivateRejectsPackageHashMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesRuntimeScript(`{"installed":true}`))
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{}))
	if _, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	}); err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}

	_, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
		StagingID:     "stage_1",
		PackageSHA256: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "package hash mismatch") {
		t.Fatalf("ActivatePackage error = %v, want package hash mismatch", err)
	}
}

func TestManagerPreviewRejectsOversizedPackage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "oversized.hankapp")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPackageBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(filepath.Join(t.TempDir(), "apps"), filepath.Join(t.TempDir(), "staging"), testRunner(Runner{}))

	_, err = manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	})
	if err == nil || !strings.Contains(err.Error(), "app package exceeds") {
		t.Fatalf("PreviewPackage error = %v, want package size limit", err)
	}
}

func TestManagerPreviewHTTPDownloadUsesAgentAndPackageTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesRuntimeScript(`{"installed":true}`))
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Fatalf("Authorization = %q, want agent bearer token", got)
		}
		if got := r.Header.Get("X-Hank-Agent-ID"); got != "agent_1" {
			t.Fatalf("X-Hank-Agent-ID = %q", got)
		}
		if got := r.Header.Get("X-Hank-App-Package-Token"); got != "package-secret" {
			t.Fatalf("X-Hank-App-Package-Token = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	manager := NewManager(filepath.Join(t.TempDir(), "apps"), filepath.Join(t.TempDir(), "staging"), testRunner(Runner{}))
	manager.SetPackageDownloadAuth("agent_1", "agent-secret")
	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:     "stage_1",
		DownloadURL:   server.URL + "/hermes.hankapp",
		DownloadToken: "package-secret",
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if preview.App.ID != "hermes" {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestManagerConfigApplyEnablesAppAndTracksSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackage(t, false)
	enable := true

	response, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:        "hermes",
		PublicConfig: json.RawMessage(`{"api_base_url":"https://hermes.local"}`),
		Secrets:      json.RawMessage(`{"api_key":"secret"}`),
		Enable:       &enable,
	})
	if err != nil {
		t.Fatalf("ConfigApply error: %v", err)
	}
	if !response.App.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if string(response.App.PublicConfig) != `{"api_base_url":"https://hermes.local"}` {
		t.Fatalf("PublicConfig = %s", response.App.PublicConfig)
	}
	if !response.App.SecretFieldsSet["api_key"] {
		t.Fatalf("SecretFieldsSet = %#v, want api_key set", response.App.SecretFieldsSet)
	}
}

func TestManagerLoadRestoresPersistedAppState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesSecretEchoScript())
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024}))
	if _, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	}); err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if _, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
		StagingID: "stage_1",
		Enable:    true,
	}); err != nil {
		t.Fatalf("ActivatePackage error: %v", err)
	}
	if _, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:        "hermes",
		PublicConfig: json.RawMessage(`{"api_base_url":"https://hermes.local"}`),
		Secrets:      json.RawMessage(`{"api_key":"persisted-secret"}`),
	}); err != nil {
		t.Fatalf("ConfigApply error: %v", err)
	}

	reloaded := NewManager(appsDir, stagingDir, testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024}))
	if err := reloaded.Load(ctx); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if capabilities := reloaded.Capabilities(); len(capabilities) != 1 || capabilities[0] != "apps.hermes.chat" {
		t.Fatalf("Capabilities = %#v, want persisted hermes capability", capabilities)
	}
	response, err := reloaded.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Invoke after reload error: %v", err)
	}
	if string(response.Output) != `{"api_key":"persisted-secret"}` {
		t.Fatalf("Output = %s, want persisted secret", response.Output)
	}
}

func TestManagerActivateReplacementPreservesAppConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024}))

	activate := func(stagingID string) {
		t.Helper()
		archivePath := writeManagerHermesPackage(t, t.TempDir(), hermesSecretEchoScript())
		if _, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
			StagingID:   stagingID,
			DownloadURL: fileURL(t, archivePath),
		}); err != nil {
			t.Fatalf("PreviewPackage(%s) error: %v", stagingID, err)
		}
		if _, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
			StagingID: stagingID,
			Enable:    true,
		}); err != nil {
			t.Fatalf("ActivatePackage(%s) error: %v", stagingID, err)
		}
	}

	activate("stage_initial")
	if _, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:        "hermes",
		PublicConfig: json.RawMessage(`{"api_base_url":"https://hermes.local"}`),
		Secrets:      json.RawMessage(`{"api_key":"preserved-secret"}`),
	}); err != nil {
		t.Fatalf("ConfigApply error: %v", err)
	}

	activate("stage_replacement")

	status, err := manager.ConfigStatus(ctx, protocol.AppsConfigStatusRequest{AppID: "hermes"})
	if err != nil {
		t.Fatalf("ConfigStatus error: %v", err)
	}
	if len(status.Apps) != 1 {
		t.Fatalf("ConfigStatus apps = %#v, want one app", status.Apps)
	}
	if string(status.Apps[0].PublicConfig) != `{"api_base_url":"https://hermes.local"}` {
		t.Fatalf("PublicConfig = %s, want preserved config", status.Apps[0].PublicConfig)
	}
	if !status.Apps[0].SecretFieldsSet["api_key"] {
		t.Fatalf("SecretFieldsSet = %#v, want api_key preserved", status.Apps[0].SecretFieldsSet)
	}
	response, err := manager.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Invoke after replacement error: %v", err)
	}
	if string(response.Output) != `{"api_key":"preserved-secret"}` {
		t.Fatalf("Output = %s, want preserved secret", response.Output)
	}
}

func TestManagerCapabilitiesOnlyIncludesEnabledApps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackage(t, false)

	if capabilities := manager.Capabilities(); len(capabilities) != 0 {
		t.Fatalf("Capabilities = %#v, want none for disabled app", capabilities)
	}

	enable := true
	if _, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:  "hermes",
		Enable: &enable,
	}); err != nil {
		t.Fatalf("ConfigApply error: %v", err)
	}
	capabilities := manager.Capabilities()
	if len(capabilities) != 1 || capabilities[0] != "apps.hermes.chat" {
		t.Fatalf("Capabilities = %#v, want enabled hermes chat capability", capabilities)
	}
}

func TestManagerConfigApplyPreservesExistingSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackageWithScript(t, true, hermesSecretEchoScript())

	response, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:   "hermes",
		Secrets: json.RawMessage(`{"api_key":"original-secret"}`),
	})
	if err != nil {
		t.Fatalf("ConfigApply initial secret error: %v", err)
	}
	if !response.App.SecretFieldsSet["api_key"] {
		t.Fatalf("SecretFieldsSet = %#v, want api_key set", response.App.SecretFieldsSet)
	}
	assertHermesReceivesAPIKey(t, manager, "original-secret")

	for _, secrets := range []json.RawMessage{
		json.RawMessage(`{"api_key":""}`),
		json.RawMessage(`{"api_key":null}`),
		json.RawMessage(`{}`),
	} {
		response, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
			AppID:   "hermes",
			Secrets: secrets,
		})
		if err != nil {
			t.Fatalf("ConfigApply preserving secret %s error: %v", secrets, err)
		}
		if !response.App.SecretFieldsSet["api_key"] {
			t.Fatalf("SecretFieldsSet after %s = %#v, want api_key preserved", secrets, response.App.SecretFieldsSet)
		}
		assertHermesReceivesAPIKey(t, manager, "original-secret")
	}
}

func TestManagerConfigApplyEmptySecretWithoutExistingValueDoesNotSetMetadata(t *testing.T) {
	t.Parallel()
	manager := installManagerHermesPackage(t, false)

	for _, secrets := range []json.RawMessage{
		json.RawMessage(`{"api_key":""}`),
		json.RawMessage(`{"api_key":null}`),
		json.RawMessage(`{}`),
	} {
		response, err := manager.ConfigApply(context.Background(), protocol.AppsConfigApplyRequest{
			AppID:   "hermes",
			Secrets: secrets,
		})
		if err != nil {
			t.Fatalf("ConfigApply empty secret %s error: %v", secrets, err)
		}
		if response.App.SecretFieldsSet["api_key"] {
			t.Fatalf("SecretFieldsSet after %s = %#v, want api_key unset", secrets, response.App.SecretFieldsSet)
		}
	}
}

func TestManagerConfigApplyRunsSettingsApplyBeforePersisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerGramatonPackageWithScript(t, true, gramatonSettingsApplyScript())

	_, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:        "gramaton",
		PublicConfig: json.RawMessage(`{"enabled":true,"base_url":"https://media.local","username":"bad-user","source_id":"media","destination_path":"/downloads"}`),
		Secrets:      json.RawMessage(`{"password":"bad"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("ConfigApply error = %v, want settings_apply validation failure", err)
	}

	status, err := manager.ConfigStatus(ctx, protocol.AppsConfigStatusRequest{AppID: "gramaton"})
	if err != nil {
		t.Fatalf("ConfigStatus error: %v", err)
	}
	if len(status.Apps) != 1 {
		t.Fatalf("ConfigStatus apps = %#v, want one app", status.Apps)
	}
	if len(status.Apps[0].PublicConfig) != 0 {
		t.Fatalf("PublicConfig after failed validation = %s, want unchanged empty config", status.Apps[0].PublicConfig)
	}
	if status.Apps[0].SecretFieldsSet["password"] {
		t.Fatalf("SecretFieldsSet after failed validation = %#v, want password unset", status.Apps[0].SecretFieldsSet)
	}

	response, err := manager.ConfigApply(ctx, protocol.AppsConfigApplyRequest{
		AppID:        "gramaton",
		PublicConfig: json.RawMessage(`{"enabled":true,"base_url":"https://media.local","username":"good-user","source_id":"media","destination_path":"/downloads"}`),
		Secrets:      json.RawMessage(`{"password":"good"}`),
	})
	if err != nil {
		t.Fatalf("ConfigApply good credentials error: %v", err)
	}
	if string(response.App.PublicConfig) != `{"enabled":true,"base_url":"https://media.local","username":"good-user","source_id":"media","destination_path":"/downloads"}` {
		t.Fatalf("PublicConfig = %s, want good config persisted", response.App.PublicConfig)
	}
	if !response.App.SecretFieldsSet["password"] {
		t.Fatalf("SecretFieldsSet = %#v, want password set", response.App.SecretFieldsSet)
	}
}

func TestManagerInvokeRefusesDisabledApp(t *testing.T) {
	t.Parallel()
	manager := installManagerHermesPackage(t, false)

	_, err := manager.Invoke(context.Background(), protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Invoke error = %v, want disabled app refusal", err)
	}
}

func TestManagerInvokeRunsEnabledApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackage(t, true)

	response, err := manager.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if string(response.Output) != `{"text":"hello from hermes"}` {
		t.Fatalf("Output = %s", response.Output)
	}
}

func TestManagerInvokeRejectsForgedCoreEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manager := installManagerHermesPackageWithScript(t, true, hermesRuntimeScriptWithEvent(`{"text":"hello from hermes"}`))
	var events []AppStdioEvent
	manager.SetEventSink(func(ctx context.Context, event string, topic string, payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		events = append(events, AppStdioEvent{Event: event, Topic: topic, Body: raw})
		return nil
	})

	_, err := manager.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
		Context:   json.RawMessage(`{"trace_id":"trace_1"}`),
	})
	if !errors.Is(err, ErrPermissionRefused) {
		t.Fatalf("expected refused core events, got %v", err)
	}
	if len(events) != 0 {
		t.Fatal("forged core event reached agent event sink")
	}
}

func installManagerHermesPackage(t *testing.T, enable bool) *Manager {
	t.Helper()
	return installManagerHermesPackageWithScript(t, enable, hermesRuntimeScript(`{"text":"hello from hermes"}`))
}

func installManagerHermesPackageWithScript(t *testing.T, enable bool, script string) *Manager {
	t.Helper()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerHermesPackage(t, t.TempDir(), script)
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024}))
	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if preview.App.ID != "hermes" {
		t.Fatalf("preview app = %#v", preview.App)
	}
	if _, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
		StagingID: "stage_1",
		Enable:    enable,
	}); err != nil {
		t.Fatalf("ActivatePackage error: %v", err)
	}
	return manager
}

func installManagerGramatonPackageWithScript(t *testing.T, enable bool, script string) *Manager {
	t.Helper()
	ctx := context.Background()
	appsDir := filepath.Join(t.TempDir(), "apps")
	stagingDir := filepath.Join(t.TempDir(), "staging")
	archivePath := writeManagerGramatonPackage(t, t.TempDir(), script)
	manager := NewManager(appsDir, stagingDir, testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024}))
	preview, err := manager.PreviewPackage(ctx, protocol.AppsPackagePreviewRequest{
		StagingID:   "stage_1",
		DownloadURL: fileURL(t, archivePath),
	})
	if err != nil {
		t.Fatalf("PreviewPackage error: %v", err)
	}
	if preview.App.ID != "gramaton" {
		t.Fatalf("preview app = %#v", preview.App)
	}
	if _, err := manager.ActivatePackage(ctx, protocol.AppsPackageActivateRequest{
		StagingID: "stage_1",
		Enable:    enable,
	}); err != nil {
		t.Fatalf("ActivatePackage error: %v", err)
	}
	return manager
}

func assertHermesReceivesAPIKey(t *testing.T, manager *Manager, want string) {
	t.Helper()
	response, err := manager.Invoke(context.Background(), protocol.AppsInvokeRequest{ActorRole: "admin",
		AppID:     "hermes",
		CommandID: "chat",
		Input:     json.RawMessage(`{"prompt":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if string(response.Output) != `{"api_key":"`+want+`"}` {
		t.Fatalf("Output = %s, want api_key %q", response.Output, want)
	}
}

func fileURL(t *testing.T, path string) string {
	t.Helper()
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validHermesManifest() Manifest {
	return Manifest{
		SchemaVersion: "hank.app.v1",
		ID:            "hermes",
		Name:          "Hermes",
		Version:       "1.0.0",
		Publisher:     "Hank",
		Description:   "Chat with Hermes.",
		Runtime: Runtime{
			Type:    "stdio",
			Command: "bin/hermes-app",
		},
		Commands: []Command{{
			ID:             "chat",
			Mode:           "request_response",
			InputSchema:    "schemas/chat.input.schema.json",
			OutputSchema:   "schemas/chat.output.schema.json",
			TimeoutSeconds: 30,
		}},
		Config: Config{
			Schema:       "schemas/config.schema.json",
			SecretFields: []string{"api_key"},
			Settings: SettingsSchema{Fields: []SettingsField{
				{Key: "api_base_url", Label: "API URL", Type: "url", Required: true},
				{Key: "api_key", Label: "API key", Type: "password", Secret: true, SecretKey: "api_key"},
			}},
		},
		Permissions: Permissions{
			Network: []NetworkPermission{{Kind: "configured_base_url", Field: "api_base_url"}},
		},
	}
}

func writeManagerHermesPackage(t *testing.T, dir string, script string) string {
	t.Helper()
	manifest := validHermesManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "hermes.hankapp")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	writeZipEntry(t, zw, "app.json", string(rawManifest), 0o600)
	writeZipEntry(t, zw, "bin/hermes-app", script, 0o700)
	writeZipEntry(t, zw, "schemas/config.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "schemas/chat.input.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "schemas/chat.output.schema.json", `{"type":"object"}`, 0o600)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func writeManagerFolderWrappedPackage(t *testing.T, dir string, script string) string {
	t.Helper()
	manifest := validHermesManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "hermes-folder.hankapp")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	writeZipEntry(t, zw, "hermes-source/app.json", string(rawManifest), 0o600)
	writeZipEntry(t, zw, "hermes-source/bin/hermes-app", script, 0o700)
	writeZipEntry(t, zw, "hermes-source/schemas/config.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "hermes-source/schemas/chat.input.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "hermes-source/schemas/chat.output.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "__MACOSX/hermes-source/._app.json", "metadata", 0o600)
	writeZipEntry(t, zw, ".DS_Store", "metadata", 0o600)
	writeZipEntry(t, zw, "hermes-source/.DS_Store", "metadata", 0o600)
	writeZipEntry(t, zw, "hermes-source/._app.json", "metadata", 0o600)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func writeManagerGramatonPackage(t *testing.T, dir string, script string) string {
	t.Helper()
	manifest := Manifest{
		SchemaVersion: "hank.app.v1",
		ID:            "gramaton",
		Name:          "Gramaton",
		Version:       "1.0.0",
		Publisher:     "Hank",
		Description:   "Search and queue media downloads.",
		Runtime: Runtime{
			Type:    "stdio",
			Command: "bin/gramaton-app",
		},
		Commands: []Command{{
			ID:             "settings_apply",
			Mode:           "request_response",
			InputSchema:    "schemas/settings_apply.input.schema.json",
			OutputSchema:   "schemas/settings_apply.output.schema.json",
			TimeoutSeconds: 30,
			AdminOnly:      true,
		}},
		Config: Config{
			Schema:       "schemas/config.schema.json",
			SecretFields: []string{"password"},
		},
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "gramaton.hankapp")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	writeZipEntry(t, zw, "app.json", string(rawManifest), 0o600)
	writeZipEntry(t, zw, "bin/gramaton-app", script, 0o700)
	writeZipEntry(t, zw, "schemas/config.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "schemas/settings_apply.input.schema.json", `{"type":"object"}`, 0o600)
	writeZipEntry(t, zw, "schemas/settings_apply.output.schema.json", `{"type":"object"}`, 0o600)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name string, body string, mode os.FileMode) {
	t.Helper()
	header := &zip.FileHeader{Name: name}
	header.SetMode(mode)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func hermesRuntimeScript(output string) string {
	return "#!/bin/sh\nread line\nrequest_id=$(printf '%s' \"$line\" | sed -n 's/.*\"request_id\":\"\\([^\"]*\\)\".*/\\1/p')\nprintf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":true,\"output\":" + output + "}'\n"
}

func hermesRuntimeScriptWithEvent(output string) string {
	return "#!/bin/sh\nread line\ncase \"$line\" in *'\"trace_id\":\"trace_1\"'*) ;; *) printf '%s\\n' '{\"request_id\":\"req_1\",\"ok\":false,\"error\":{\"code\":\"missing_context\",\"message\":\"missing context\"}}'; exit 0 ;; esac\nrequest_id=$(printf '%s' \"$line\" | sed -n 's/.*\"request_id\":\"\\([^\"]*\\)\".*/\\1/p')\nprintf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":true,\"output\":" + output + ",\"events\":[{\"event\":\"media.download_progress\",\"topic\":\"media.downloads\",\"body\":{\"job_id\":\"job_1\",\"status\":\"running\"}}]}'\n"
}

func hermesSecretEchoScript() string {
	return "#!/bin/sh\nread line\nrequest_id=$(printf '%s' \"$line\" | sed -n 's/.*\"request_id\":\"\\([^\"]*\\)\".*/\\1/p')\napi_key=$(printf '%s' \"$line\" | sed -n 's/.*\"api_key\":\"\\([^\"]*\\)\".*/\\1/p')\nprintf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":true,\"output\":{\"api_key\":\"'\"$api_key\"'\"}}'\n"
}

func gramatonSettingsApplyScript() string {
	return "#!/bin/sh\nread line\nrequest_id=$(printf '%s' \"$line\" | sed -n 's/.*\"request_id\":\"\\([^\"]*\\)\".*/\\1/p')\ncase \"$line\" in *'\"command_id\":\"settings_apply\"'*) ;; *) printf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":false,\"error\":{\"code\":\"validation_failed\",\"message\":\"wrong command\"}}'; exit 1 ;; esac\ncase \"$line\" in *'\"settings\":{\"base_url\":\"https://media.local\",\"destination_path\":\"/downloads\",\"enabled\":true'*|*'\"settings\":{\"enabled\":true'*);; *) printf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":false,\"error\":{\"code\":\"validation_failed\",\"message\":\"missing settings\"}}'; exit 1 ;; esac\ncase \"$line\" in *'\"persist\":false'*) ;; *) printf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":false,\"error\":{\"code\":\"validation_failed\",\"message\":\"persist should be false\"}}'; exit 1 ;; esac\ncase \"$line\" in *'\"password\":\"good\"'*) printf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":true,\"output\":{\"settings\":{\"enabled\":true}}}' ;; *) printf '%s\\n' '{\"request_id\":\"'\"$request_id\"'\",\"ok\":false,\"error\":{\"code\":\"validation_failed\",\"message\":\"login failed\"}}'; exit 1 ;; esac\n"
}

func TestManagerLoadIsolatesUnsupportedLegacyManifest(t *testing.T) {
	m := installManagerHermesPackageWithScript(t, false, hermesRuntimeScript(`{}`))
	app := m.apps["hermes"]
	bad := app.Manifest
	bad.ID = "aaa-legacy"
	bad.Permissions.Network = []NetworkPermission{{Kind: networkPermissionConfiguredBaseURL, Field: "base"}, {Kind: networkPermissionConfiguredBaseURL, Field: "base"}}
	dir := filepath.Join(m.appsDir, "aaa-legacy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(context.Background()); err == nil {
		t.Fatal("unsupported app not reported")
	}
	if _, ok := m.apps["aaa-legacy"]; ok {
		t.Fatal("unsupported app loaded")
	}
	if _, ok := m.apps["hermes"]; !ok {
		t.Fatal("valid later app was not loaded")
	}
}
