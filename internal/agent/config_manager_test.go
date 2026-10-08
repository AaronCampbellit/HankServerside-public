package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestConfigManagerSMBTestDoesNotMutateOrPersist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env.agent")
	files := agentfiles.NewWithConfig(agentfiles.Config{Shares: []agentfiles.SMBConfig{{ID: "media", Host: "nas.local", Share: "media", Password: "saved-secret"}}})
	manager := newConfigManager(envPath, agentha.New("", "", 0), files)
	manager.testSMB = func(_ context.Context, cfg agentfiles.SMBConfig) error {
		if cfg.ID != "archive" || cfg.Host != "backup.local" || cfg.Share != "archive" || cfg.Password != "draft-secret" {
			t.Fatalf("test config = %#v", cfg)
		}
		return nil
	}
	before := files.SMBConfigs()
	response, err := manager.TestSMB(context.Background(), protocol.ConfigSMBTestRequest{
		ID: "archive", Name: "Archive", Host: "backup.local", Share: "archive", Username: "backup", Password: "draft-secret",
	})
	if err != nil {
		t.Fatalf("TestSMB: %v", err)
	}
	if !response.OK {
		t.Fatalf("response = %#v, want ok", response)
	}
	if after := files.SMBConfigs(); !reflect.DeepEqual(before, after) {
		t.Fatalf("live SMB config changed: before=%#v after=%#v", before, after)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("test wrote env file: err=%v", err)
	}
}

func TestConfigManagerSMBTestValidatesDraft(t *testing.T) {
	t.Parallel()

	manager := newConfigManager("", agentha.New("", "", 0), agentfiles.New(""))
	manager.testSMB = func(context.Context, agentfiles.SMBConfig) error {
		t.Fatal("probe called for invalid draft")
		return nil
	}
	if _, err := manager.TestSMB(context.Background(), protocol.ConfigSMBTestRequest{ID: "broken", Host: "nas.local"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("TestSMB error = %v, want sanitized validation error", err)
	}
}

func TestConfigManagerSMBTestReusesSavedPasswordWhenDraftIsBlank(t *testing.T) {
	t.Parallel()

	files := agentfiles.NewWithConfig(agentfiles.Config{Shares: []agentfiles.SMBConfig{{ID: "media", Host: "nas.local", Share: "media", Password: "saved-secret"}}})
	manager := newConfigManager("", agentha.New("", "", 0), files)
	manager.testSMB = func(_ context.Context, cfg agentfiles.SMBConfig) error {
		if cfg.Password != "saved-secret" {
			t.Fatalf("probe password = %q, want saved credential", cfg.Password)
		}
		return nil
	}
	if _, err := manager.TestSMB(context.Background(), protocol.ConfigSMBTestRequest{ID: "media", Host: "nas.local", Share: "media"}); err != nil {
		t.Fatalf("TestSMB: %v", err)
	}
}

func TestPrepareLocalConfigsCreatesAndValidates(t *testing.T) {
	t.Parallel()

	created := filepath.Join(t.TempDir(), "new-folder")
	configs, err := prepareLocalConfigs([]localFolderPayload{
		{ID: "media", Name: "Media", Root: created, Create: true},
	})
	if err != nil {
		t.Fatalf("prepareLocalConfigs: %v", err)
	}
	if len(configs) != 1 || configs[0].ID != "media" || configs[0].Root != filepath.Clean(created) {
		t.Fatalf("configs = %#v, want single created media folder", configs)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("created folder missing: err=%v", err)
	}
}

func TestPrepareLocalConfigsRejectsRelativeMissingAndFile(t *testing.T) {
	t.Parallel()

	if _, err := prepareLocalConfigs([]localFolderPayload{{Root: "relative/path"}}); err == nil {
		t.Fatal("relative path accepted, want error")
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := prepareLocalConfigs([]localFolderPayload{{Root: missing}}); err == nil {
		t.Fatal("missing path without create accepted, want error")
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := prepareLocalConfigs([]localFolderPayload{{Root: file}}); err == nil {
		t.Fatal("file path accepted, want error")
	}
}

func TestConfigManagerApplyCreatesAndPersistsHostFolders(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env.agent")
	files := agentfiles.New("")
	manager := newConfigManager(envPath, agentha.New("", "", 0), files)

	folderRoot := filepath.Join(dir, "media")
	public, err := json.Marshal(map[string]any{
		"shares": []any{},
		"folders": []map[string]any{
			{"id": "media", "name": "Media", "root": folderRoot, "create": true},
		},
	})
	if err != nil {
		t.Fatalf("marshal public config: %v", err)
	}

	if _, err := manager.Apply(context.Background(), protocol.ConfigApplyRequest{
		ServiceType:  domain.ServiceTypeSMB,
		PublicConfig: public,
		Persist:      true,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if info, err := os.Stat(folderRoot); err != nil || !info.IsDir() {
		t.Fatalf("host folder not created on apply: err=%v", err)
	}

	configs := files.LocalConfigs()
	if len(configs) != 1 || configs[0].ID != "media" || configs[0].Root != folderRoot {
		t.Fatalf("LocalConfigs = %#v, want media applied", configs)
	}

	if shares := files.SMBConfigs(); len(shares) != 0 {
		t.Fatalf("SMBConfigs = %#v, want no phantom share from a folder-only save", shares)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if !strings.Contains(string(data), "HANK_AGENT_FILES_ROOTS_JSON=") {
		t.Fatalf("env file missing roots json:\n%s", data)
	}
}

func TestConfigManagerApplyLeavesHostFoldersWhenAbsent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	existingRoot := t.TempDir()
	files := agentfiles.NewWithConfig(agentfiles.Config{
		LocalSources: []agentfiles.LocalConfig{{ID: "media", Root: existingRoot}},
	})
	manager := newConfigManager(filepath.Join(dir, ".env.agent"), agentha.New("", "", 0), files)

	// An SMB-only save (no "folders" key) must leave host folders untouched.
	public, err := json.Marshal(map[string]any{
		"shares": []map[string]any{
			{"id": "share", "host": "192.0.2.10", "share": "media"},
		},
	})
	if err != nil {
		t.Fatalf("marshal public config: %v", err)
	}

	if _, err := manager.Apply(context.Background(), protocol.ConfigApplyRequest{
		ServiceType:  domain.ServiceTypeSMB,
		PublicConfig: public,
		Persist:      true,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	configs := files.LocalConfigs()
	if len(configs) != 1 || configs[0].ID != "media" {
		t.Fatalf("LocalConfigs = %#v, want existing media folder preserved", configs)
	}
}

func TestConfigManagerFolderOnlyApplyPreservesSMBShares(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	folderRoot := t.TempDir()
	readOnly := true
	files := agentfiles.NewWithConfig(agentfiles.Config{
		Shares: []agentfiles.SMBConfig{
			{ID: "media", Host: "nas.local", Share: "media", Policy: agentfiles.AccessPolicy{Read: &readOnly}},
			{ID: "archive", Host: "nas.local", Share: "archive"},
		},
	})
	manager := newConfigManager(filepath.Join(dir, ".env.agent"), agentha.New("", "", 0), files)
	public, err := json.Marshal(map[string]any{
		"folders": []map[string]any{{"id": "documents", "root": folderRoot}},
	})
	if err != nil {
		t.Fatalf("marshal public config: %v", err)
	}

	if _, err := manager.Apply(context.Background(), protocol.ConfigApplyRequest{
		ServiceType:  domain.ServiceTypeSMB,
		PublicConfig: public,
	}); err != nil {
		t.Fatalf("Apply folder-only config: %v", err)
	}

	shares := files.SMBConfigs()
	if len(shares) != 2 || shares[0].ID != "media" || shares[1].ID != "archive" {
		t.Fatalf("SMBConfigs after folder-only apply = %#v, want both existing shares", shares)
	}
	if shares[0].Policy.Read == nil || !*shares[0].Policy.Read {
		t.Fatalf("media policy after folder-only apply = %#v, want preserved read policy", shares[0].Policy)
	}
}

func TestConfigManagerApplyFailsForMissingHostFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	files := agentfiles.NewWithConfig(agentfiles.Config{
		Shares: []agentfiles.SMBConfig{{ID: "existing", Host: "nas.local", Share: "media"}},
	})
	manager := newConfigManager(filepath.Join(dir, ".env.agent"), agentha.New("", "", 0), files)

	public, err := json.Marshal(map[string]any{
		"shares": []map[string]any{
			{"id": "replacement", "host": "backup.local", "share": "backups"},
		},
		"folders": []map[string]any{
			{"id": "media", "root": filepath.Join(dir, "missing")},
		},
	})
	if err != nil {
		t.Fatalf("marshal public config: %v", err)
	}

	if _, err := manager.Apply(context.Background(), protocol.ConfigApplyRequest{
		ServiceType:  domain.ServiceTypeSMB,
		PublicConfig: public,
		Persist:      true,
	}); err == nil {
		t.Fatal("Apply accepted a missing host folder without create, want error")
	}

	configs := files.SMBConfigs()
	if len(configs) != 1 || configs[0].ID != "existing" {
		t.Fatalf("SMBConfigs after rejected apply = %#v, want existing config unchanged", configs)
	}
}

func TestConfigManagerPersistenceFailureLeavesLiveFileConfigUnchanged(t *testing.T) {
	t.Parallel()

	existingRoot := t.TempDir()
	replacementRoot := t.TempDir()
	files := agentfiles.NewWithConfig(agentfiles.Config{
		Shares:       []agentfiles.SMBConfig{{ID: "existing-share", Host: "nas.local", Share: "media"}},
		LocalSources: []agentfiles.LocalConfig{{ID: "existing-folder", Root: existingRoot}},
	})
	manager := newConfigManager(t.TempDir(), agentha.New("", "", 0), files)
	public, err := json.Marshal(map[string]any{
		"shares": []map[string]any{
			{"id": "replacement-share", "host": "backup.local", "share": "backups"},
		},
		"folders": []map[string]any{
			{"id": "replacement-folder", "root": replacementRoot},
		},
	})
	if err != nil {
		t.Fatalf("marshal public config: %v", err)
	}

	if _, err := manager.Apply(context.Background(), protocol.ConfigApplyRequest{
		ServiceType:  domain.ServiceTypeSMB,
		PublicConfig: public,
		Persist:      true,
	}); err == nil {
		t.Fatal("Apply succeeded with a directory as the env file path, want persistence error")
	}

	shares := files.SMBConfigs()
	if len(shares) != 1 || shares[0].ID != "existing-share" {
		t.Fatalf("SMBConfigs after persistence failure = %#v, want existing share", shares)
	}
	folders := files.LocalConfigs()
	if len(folders) != 1 || folders[0].ID != "existing-folder" {
		t.Fatalf("LocalConfigs after persistence failure = %#v, want existing folder", folders)
	}
}

func TestWriteEnvFileReplacesContentWithPrivateMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env.agent")
	if err := os.WriteFile(path, []byte("OLD=value\n"), 0o644); err != nil {
		t.Fatalf("write original env: %v", err)
	}
	if err := writeEnvFile(path, map[string]string{"B": "two", "A": "one"}); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replaced env: %v", err)
	}
	if string(data) != "A=one\nB=two\n" {
		t.Fatalf("env content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat env: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteEnvFileFallsBackToInPlaceWriteForBindMount(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env.agent")
	if err := os.WriteFile(path, []byte("OLD=value\n"), 0o644); err != nil {
		t.Fatalf("write original env: %v", err)
	}
	renameCalls := 0
	err := writeEnvFileWithRename(path, map[string]string{"B": "two", "A": "one"}, func(_, _ string) error {
		renameCalls++
		return syscall.EBUSY
	})
	if err != nil {
		t.Fatalf("writeEnvFileWithRename: %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("rename calls = %d, want 1", renameCalls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env after bind-mount fallback: %v", err)
	}
	if string(data) != "A=one\nB=two\n" {
		t.Fatalf("env content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat env after bind-mount fallback: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode = %o, want 600", info.Mode().Perm())
	}
}
