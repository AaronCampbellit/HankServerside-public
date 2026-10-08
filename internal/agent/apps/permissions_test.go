package apps

import (
	"context"
	"encoding/json"
	"errors"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAppGrantsBindPackageTargetAndRevokeCachedBroker(t *testing.T) {
	ctx := context.Background()
	m := installManagerHermesPackage(t, true)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "allowed"), []byte("allowed"), 0600); err != nil {
		t.Fatal(err)
	}
	files := agentfiles.NewWithConfig(agentfiles.Config{LocalSources: []agentfiles.LocalConfig{{ID: "chosen", Root: root}}})
	m.SetFilesService(files)
	app := m.apps["hermes"]
	app.Manifest.Permissions.Files = []FilePermission{{Kind: filePermissionConfiguredSource, Field: "source"}}
	app.PublicConfig = json.RawMessage(`{"source":"chosen"}`)
	state, err := m.permissionState(ctx, *app)
	if err != nil {
		t.Fatal(err)
	}
	req := BrokerRequest{Permission: "files_read:source", Operation: "files.read", Path: "/allowed"}
	if _, err := m.brokerFor(*app, state)(ctx, req); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("ungranted source accepted", err)
	}
	grants := map[string]string{}
	for _, target := range state.Targets {
		if target.ID == req.Permission {
			grants[target.ID] = target.Fingerprint
		}
	}
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", PermissionGrants: &grants}); err != nil {
		t.Fatal(err)
	}
	snapshot := cloneInstalledApp(app)
	state, err = m.permissionState(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	broker := m.brokerFor(snapshot, state)
	output, err := broker(ctx, req)
	if err != nil || string(output.(map[string]any)["data"].([]byte)) != "allowed" {
		t.Fatal("granted read failed", err)
	}
	req.Operation = "files.write"
	req.Data = []byte("bad")
	if _, err := broker(ctx, req); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("read grant allowed write", err)
	}
	req.Operation = "files.read"
	for _, path := range []string{"/../../outside", "/link"} {
		if path == "/link" {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		}
		req.Path = path
		if _, err := broker(ctx, req); err == nil {
			t.Fatal("escaped configured root", path)
		}
	}
	req.Path = "/allowed"
	files.ApplyLocalConfigs([]agentfiles.LocalConfig{{ID: "chosen", Root: t.TempDir()}})
	if _, err := broker(ctx, req); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("retargeted source accepted", err)
	}
	files.ApplyLocalConfigs([]agentfiles.LocalConfig{{ID: "chosen", Root: root}})
	empty := map[string]string{}
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", PermissionGrants: &empty}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker(ctx, req); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("cached grant survived revocation", err)
	}
	if err := os.WriteFile(filepath.Join(app.Path, "new-package-byte"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", PermissionGrants: &grants}); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("old package grant accepted", err)
	}
}

func TestAppNetworkBrokerPinsScopeAndRefusesRedirects(t *testing.T) {
	ctx := context.Background()
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/api/redirect" {
			http.Redirect(w, r, "http://example.invalid/stolen", 302)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	m := installManagerHermesPackage(t, true)
	app := m.apps["hermes"]
	app.Manifest.Permissions.Network = []NetworkPermission{{Kind: networkPermissionConfiguredBaseURL, Field: "base"}}
	app.PublicConfig, _ = json.Marshal(map[string]string{"base": server.URL + "/api"})
	state, err := m.permissionState(ctx, *app)
	if err != nil || len(state.Targets) != 1 {
		t.Fatal(err)
	}
	permission := state.Targets[0]
	app.Grants = map[string]string{permission.ID: permission.Fingerprint}
	broker := m.brokerFor(*app, state)
	req := BrokerRequest{Permission: permission.ID, Operation: "http.request", Method: "GET", URL: server.URL + "/api/value"}
	out, err := broker(ctx, req)
	if err != nil || out.(map[string]any)["status"] != 200 {
		t.Fatal("granted URL failed", err)
	}
	for _, target := range []string{server.URL + "/elsewhere", server.URL + "/api/../outside", server.URL + "/api/%2e%2e/outside", server.URL + "/api/%252e%252e/outside", "http://example.invalid/api"} {
		req.URL = target
		if _, err := broker(ctx, req); !errors.Is(err, ErrPermissionRefused) {
			t.Fatal("URL escaped grant", target, err)
		}
	}
	req.URL = server.URL + "/api/redirect"
	out, err = broker(ctx, req)
	if err != nil || out.(map[string]any)["status"] != 302 || hits != 2 {
		t.Fatal("redirect was followed or request failed", err, hits)
	}
	req.URL = server.URL + "/api/value"
	req.Headers = map[string]string{"Host": "evil"}
	if _, err := broker(ctx, req); !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("Host override allowed", err)
	}
}

func TestAppAgentEnforcesCurrentManifestAdminOnly(t *testing.T) {
	m := installManagerHermesPackage(t, true)
	m.apps["hermes"].Manifest.Commands[0].AdminOnly = true
	_, err := m.Invoke(context.Background(), protocol.AppsInvokeRequest{AppID: "hermes", CommandID: "chat", ActorRole: "member", Context: json.RawMessage(`{"role":"admin"}`)})
	if !errors.Is(err, ErrPermissionRefused) {
		t.Fatal("agent accepted forged context role", err)
	}
	if _, err := m.Invoke(context.Background(), protocol.AppsInvokeRequest{AppID: "hermes", CommandID: "chat", ActorRole: "admin"}); err != nil {
		t.Fatal("legitimate admin rejected", err)
	}
}
