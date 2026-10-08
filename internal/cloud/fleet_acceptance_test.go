package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

// Opt-in acceptance runs the real standalone Linux agent and CLI against a TLS
// server and PostgreSQL. All credentials, workspaces and processes are disposable.
func TestFleetTwoLinuxAgentsAcceptance(t *testing.T) {
	binary := os.Getenv("HANK_FLEET_AGENT_BINARY")
	if binary == "" {
		t.Skip("set HANK_FLEET_AGENT_BINARY to the built Linux worktree binary")
	}
	db := storeForTest(t)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Now().UTC()
	user := domain.User{ID: "fleet-admin", Email: "fleet@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	home := domain.Home{ID: "fleet-home", UserID: user.ID, Name: "Fleet", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "session", UserID: user.ID, TokenHash: hashToken("fleet-session"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var activeHandler atomic.Value
	activeHandler.Store(server.http.Handler)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { activeHandler.Load().(http.Handler).ServeHTTP(w, r) }))
	defer ts.Close()
	// The fixture client and child processes trust only this synthetic TLS certificate.
	root := t.TempDir()
	cert := filepath.Join(root, "ca.pem")
	must(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600))
	agentIDs := []string{"linux-alpha", "linux-beta"}
	processes := map[string]*exec.Cmd{}
	for _, id := range agentIDs {
		must(t, db.UpsertAgent(ctx, domain.Agent{ID: id, HomeID: home.ID, Name: id, AgentType: AgentTypeWorker, Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}))
		must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "token-" + id, HomeID: home.ID, AgentID: id, TokenHash: hashToken("token-" + id), CreatedAt: now}))
		dir := filepath.Join(root, id)
		configDir := filepath.Join(dir, "config", "hankagent")
		stateDir := filepath.Join(dir, "state", "hankagent")
		must(t, os.MkdirAll(configDir, 0700))
		must(t, os.MkdirAll(stateDir, 0700))
		config, _ := json.Marshal(map[string]any{"server_url": ts.URL, "agent_id": id, "name": id, "shared_roots": []string{}, "shell_enabled": true})
		must(t, os.WriteFile(filepath.Join(configDir, "config.json"), config, 0600))
		must(t, os.WriteFile(filepath.Join(stateDir, "agent-token"), []byte("token-"+id), 0600))
		process := exec.CommandContext(ctx, binary, "--user", "run")
		process.Env = append(os.Environ(), "SSL_CERT_FILE="+cert, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_STATE_HOME="+filepath.Join(dir, "state"))
		process.Stdout = io.Discard
		process.Stderr = io.Discard
		must(t, process.Start())
		processes[id] = process
		defer func() { _ = process.Process.Signal(syscall.SIGTERM); _ = process.Wait() }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(server.router.AgentsForHome(home.ID)) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("real Linux workers did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var requested struct {
		Grant struct {
			ID string `json:"id"`
		} `json:"grant"`
		Token string `json:"token"`
	}
	fleetTLSJSON(t, ts, "fleet-session", "POST", "/v1/fleet/grants", map[string]any{"agents": agentIDs, "operations": protocol.FleetOperations, "hours": 1}, &requested)
	var preview struct {
		Token        string `json:"action_token"`
		Confirmation string `json:"confirmation"`
	}
	approval := "/v1/fleet/grants/" + requested.Grant.ID + "/approve"
	fleetTLSJSON(t, ts, "fleet-session", "POST", approval, map[string]any{}, &preview)
	fleetTLSJSON(t, ts, "fleet-session", "POST", approval, map[string]any{"action_token": preview.Token, "confirmation": preview.Confirmation}, nil)
	tokenFile := filepath.Join(root, "grant-token")
	must(t, os.WriteFile(tokenFile, []byte(requested.Token), 0600))
	callCLI := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"--user", "fleet"}, args...)...)
		command.Env = append(os.Environ(), "SSL_CERT_FILE="+cert, "HANK_FLEET_SERVER="+ts.URL, "HANK_FLEET_TOKEN_FILE="+tokenFile)
		var out bytes.Buffer
		command.Stdout = &out
		command.Stderr = io.Discard
		if err := command.Run(); err != nil {
			t.Fatalf("fleet CLI %s failed: %v", args[0], err)
		}
		return out.Bytes()
	}
	if raw := callCLI("agents"); !bytes.Contains(raw, []byte("linux-alpha")) || !bytes.Contains(raw, []byte("linux-beta")) {
		t.Fatal("CLI did not discover both targets")
	}
	steps := []map[string]any{}
	for i, id := range agentIDs {
		var workspace struct {
			ID string `json:"workspace_id"`
		}
		must(t, json.Unmarshal(callCLI("workspace-create", "--agent", id), &workspace))
		fleetTLSJSON(t, ts, requested.Token, "POST", "/v1/fleet/workspaces/"+workspace.ID+"/file", map[string]any{"agent_id": id, "path": "fixture.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte(id)), "revision": ""}, nil)
		if i == 0 {
			requestBody, _ := json.Marshal(map[string]any{"agent_id": id, "path": "fixture.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("overwrite")), "revision": ""})
			req, err := http.NewRequest("POST", ts.URL+"/v1/fleet/workspaces/"+workspace.ID+"/file", bytes.NewReader(requestBody))
			must(t, err)
			req.Header.Set("Authorization", "Bearer "+requested.Token)
			response, err := ts.Client().Do(req)
			must(t, err)
			response.Body.Close()
			if response.StatusCode != 409 {
				t.Fatal("stale workspace revision not rejected as conflict")
			}
		}
		if i == 0 {
			for _, size := range []int{4096, 65536, 524288} {
				content := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), size))
				start := time.Now()
				for n := 0; n < 5; n++ {
					fleetTLSJSON(t, ts, requested.Token, "POST", "/v1/fleet/workspaces/"+workspace.ID+"/file", map[string]any{"agent_id": id, "path": fmt.Sprintf("benchmark/%d-%d.bin", size, n), "content_base64": content, "revision": ""}, nil)
				}
				elapsed := time.Since(start)
				t.Logf("fleet_relay payload_bytes=%d samples=5 average_ms=%.2f useful_MiB_per_s=%.2f", size, float64(elapsed.Microseconds())/5000, float64(size*5)/(1<<20)/elapsed.Seconds())
			}
		}
		steps = append(steps, map[string]any{"agent_id": id, "workspace_id": workspace.ID, "job_id": []string{"job_alpha123", "job_beta123"}[i], "command": "test \"$(cat fixture.txt)\" = " + id + " && printf 'tested " + id + "\\n'", "timeout_seconds": 10})
	}
	manifest := filepath.Join(root, "workflow.json")
	raw, _ := json.Marshal(steps)
	must(t, os.WriteFile(manifest, raw, 0600))
	output := callCLI("workflow", "--file", manifest)
	if !strings.Contains(string(output), "tested linux-alpha") || !strings.Contains(string(output), "tested linux-beta") {
		t.Fatal("multi-device build/test workflow did not return both outputs")
	}
	// A replacement server reuses durable PostgreSQL state; agents reconnect outbound.
	oldServer := server
	server = NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	activeHandler.Store(server.http.Handler)
	for _, id := range agentIDs {
		oldServer.router.DisconnectAgent(home.ID, id, "synthetic restart")
	}
	deadline = time.Now().Add(10 * time.Second)
	for len(server.router.AgentsForHome(home.ID)) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("workers did not reconnect after server restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Restart one real worker from its persisted private state, then reattach.
	originalConnection, _ := server.router.ResolveAgent(home.ID, "linux-alpha")
	original := processes["linux-alpha"]
	_ = original.Process.Signal(syscall.SIGTERM)
	_ = original.Wait()
	restarted := exec.CommandContext(ctx, binary, "--user", "run")
	restarted.Env = original.Env
	restarted.Stdout = io.Discard
	restarted.Stderr = io.Discard
	must(t, restarted.Start())
	defer func() { _ = restarted.Process.Signal(syscall.SIGTERM); _ = restarted.Wait() }()
	deadline = time.Now().Add(10 * time.Second)
	for {
		connection, ok := server.router.ResolveAgent(home.ID, "linux-alpha")
		if ok && connection != originalConnection && server.router.supportsCurrentAgent(connection, protocol.CapabilityFleetV1) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not recover after restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Reattaching a new CLI process must replay bounded output without resubmitting.
	if raw := callCLI("job-read", "--job", "job_alpha123"); !bytes.Contains(raw, []byte("succeeded")) {
		t.Fatal("job reattach lost terminal state")
	}
	// Persist a cancellation while offline, then let reconnect reconciliation deliver it.
	fleetTLSJSON(t, ts, requested.Token, "POST", "/v1/fleet/jobs", map[string]any{"agent_id": "linux-alpha", "workspace_id": steps[0]["workspace_id"], "job_id": "job_cancel_001", "command": "sleep 30", "timeout_seconds": 60}, nil)
	server.router.DisconnectAgent(home.ID, "linux-alpha", "synthetic outage")
	cancelReq, err := http.NewRequest("POST", ts.URL+"/v1/fleet/jobs/job_cancel_001/cancel", strings.NewReader(`{}`))
	must(t, err)
	cancelReq.Header.Set("Authorization", "Bearer "+requested.Token)
	cancelResp, err := ts.Client().Do(cancelReq)
	must(t, err)
	cancelResp.Body.Close()
	if cancelResp.StatusCode != 503 && cancelResp.StatusCode != 200 {
		t.Fatalf("cancel status=%d", cancelResp.StatusCode)
	}
	pending, err := db.GetFleetJob(ctx, "job_cancel_001", requested.Grant.ID)
	must(t, err)
	if !pending.CancelRequested {
		t.Fatal("offline cancellation intent not persisted")
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		state, err := db.GetFleetJob(ctx, "job_cancel_001", requested.Grant.ID)
		must(t, err)
		if state.State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("offline cancellation did not complete, state=%s", state.State)
		}
		// A read also advances state after the process group has actually exited.
		if connection, ok := server.router.ResolveAgent(home.ID, "linux-alpha"); ok && connection != nil {
			var state protocol.FleetJob
			fleetTLSJSON(t, ts, requested.Token, "GET", "/v1/fleet/jobs/job_cancel_001", nil, &state)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Hosted MCP controls the real workers with a separately approved account grant.
	server.ConfigureMCP(MCPConfig{Enabled: true, PublicBaseURL: ts.URL})
	must(t, db.CreateMCPOAuthClient(ctx, domain.MCPOAuthClient{ID: "hosted-client", ClientName: "Fleet acceptance", RedirectURIs: []string{"https://example.com/callback"}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code"}, CreatedAt: now}))
	must(t, db.CreateMCPToken(ctx, domain.MCPToken{ID: "hosted-token", ClientID: "hosted-client", UserID: user.ID, AccessTokenHash: hashToken("hosted-test-access"), Scopes: []string{"docs:read"}, Resource: ts.URL + "/v1/mcp", AccessExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}))
	hostedExpiry := now.Add(time.Hour)
	must(t, db.CreateFleetGrant(ctx, store.FleetGrant{ID: "hosted-grant", MCPAccount: true, HomeID: home.ID, UserID: user.ID, Operations: protocol.FleetOperations, ExpiresAt: &hostedExpiry}, hashToken("unissued-hosted-grant")))
	must(t, db.SetFleetGrantState(ctx, home.ID, "hosted-grant", user.ID, "approved"))
	hostedCall := func(name string, args map[string]any) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": mcpModernProtocolVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
		request, err := http.NewRequestWithContext(ctx, "POST", ts.URL+"/v1/mcp", bytes.NewReader(body))
		must(t, err)
		request.Header.Set("Authorization", "Bearer hosted-test-access")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
		request.Header.Set("Mcp-Method", "tools/call")
		request.Header.Set("Mcp-Name", name)
		response, err := ts.Client().Do(request)
		must(t, err)
		defer response.Body.Close()
		var result map[string]any
		must(t, json.NewDecoder(response.Body).Decode(&result))
		if response.StatusCode != 200 || mcpToolIsError(result) || result["error"] != nil {
			t.Fatalf("hosted MCP %s failed: %v", name, result)
		}
		return mcpResultText(result)
	}
	if !strings.Contains(hostedCall("fleet_agents", map[string]any{}), "hosted-grant") {
		t.Fatal("hosted discovery missing grant")
	}
	hostedCall("fleet_workspace_create", map[string]any{"grant_id": "hosted-grant", "agent_id": "linux-alpha", "workspace_id": "hosted-workspace"})
	hostedCall("fleet_file_write", map[string]any{"grant_id": "hosted-grant", "agent_id": "linux-alpha", "workspace_id": "hosted-workspace", "path": "fixture.txt", "revision": "", "content_base64": base64.StdEncoding.EncodeToString([]byte("hosted fixture"))})
	hostedCall("fleet_job_start", map[string]any{"grant_id": "hosted-grant", "agent_id": "linux-alpha", "workspace_id": "hosted-workspace", "job_id": "hosted-job", "command": "cat fixture.txt", "timeout_seconds": 10})
	deadline = time.Now().Add(5 * time.Second)
	for {
		output := hostedCall("fleet_job_read", map[string]any{"grant_id": "hosted-grant", "job_id": "hosted-job"})
		if strings.Contains(output, "succeeded") && strings.Contains(output, "hosted fixture") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hosted job not verified: %s", output)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// MCP initialization and discovery use the same scoped token, not a user session.
	command := exec.CommandContext(ctx, binary, "--user", "fleet", "mcp")
	command.Env = append(os.Environ(), "SSL_CERT_FILE="+cert, "HANK_FLEET_SERVER="+ts.URL, "HANK_FLEET_TOKEN_FILE="+tokenFile)
	command.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\"}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	raw, err = command.Output()
	must(t, err)
	if !bytes.Contains(raw, []byte("fleet_job_start")) {
		t.Fatal("MCP tools missing")
	}
}

func fleetTLSJSON(t *testing.T, server *httptest.Server, token, method, path string, body, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		must(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	must(t, err)
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("fleet %s %s status=%d", method, path, response.StatusCode)
	}
	if out != nil {
		must(t, json.NewDecoder(response.Body).Decode(out))
	}
}
