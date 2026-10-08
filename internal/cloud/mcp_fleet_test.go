package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestHostedFleetConnectionBindingRotationAndRevocation(t *testing.T) {
	for _, infinite := range []bool{false, true} {
		t.Run(fmt.Sprint("infinite=", infinite), func(t *testing.T) { testFleetMCPExpiry(t, infinite) })
	}
}
func testFleetMCPExpiry(t *testing.T, infinite bool) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "fleetmcp-user", Email: "fleetmcp@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	home := domain.Home{ID: "fleetmcp-home", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	agent := domain.Agent{ID: "fleetmcp-agent", HomeID: home.ID, Name: "Worker", Status: "offline", AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	must(t, db.UpsertAgent(ctx, agent))
	client := domain.MCPOAuthClient{ID: "fleetmcp-client", ClientName: "Test", RedirectURIs: []string{"https://example.com/callback"}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, CreatedAt: now}
	must(t, db.CreateMCPOAuthClient(ctx, client))
	token := domain.MCPToken{ID: "fleetmcp-token", UserID: user.ID, ClientID: client.ID, AccessTokenHash: "fleetmcp-access", RefreshTokenHash: "fleetmcp-refresh", Scopes: []string{"docs:read"}, Resource: "https://example.com/v1/mcp", AccessExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateMCPToken(ctx, token))
	expiry := now.Add(time.Hour)
	var expiresAt *time.Time
	if !infinite {
		expiresAt = &expiry
	}
	grant := store.FleetGrant{ID: "fleetmcp-grant", MCPTokenID: token.ID, HomeID: home.ID, UserID: user.ID, Agents: []string{agent.ID}, Operations: []string{"job.read"}, ExpiresAt: expiresAt}
	must(t, db.CreateFleetGrant(ctx, grant, "never-issued"))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := mcpAuthContext{User: user, Token: token}
	args := json.RawMessage(`{"grant_id":"fleetmcp-grant"}`)
	if _, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args); err == nil {
		t.Fatal("pending grant allowed")
	}
	must(t, db.SetFleetGrantState(ctx, home.ID, grant.ID, user.ID, "approved"))
	if _, err := db.FleetGrantByHash(ctx, "never-issued"); err == nil {
		t.Fatal("hosted grant accepted bearer credential")
	}
	result, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args)
	must(t, err)
	if !strings.Contains(result.Text, "jobs") {
		t.Fatal("missing jobs")
	}
	other := auth
	other.Token.ID = "another-connection"
	if _, err := server.executeMCPFleet(ctx, other, "fleet_jobs", args); err == nil {
		t.Fatal("other connection allowed")
	}
	result, err = server.executeMCPFleet(ctx, other, "fleet_agents", json.RawMessage(`{}`))
	must(t, err)
	if strings.Contains(result.Text, grant.ID) {
		t.Fatal("grant leaked to other connection")
	}
	result, err = server.executeMCPFleet(ctx, auth, "fleet_job_start", json.RawMessage(`{"grant_id":"fleetmcp-grant","agent_id":"fleetmcp-agent","workspace_id":"workspace123","job_id":"job123456","command":"true"}`))
	if err == nil {
		t.Fatal("read-only grant executed job")
	}
	rotated := token
	rotated.ID = "fleetmcp-rotated"
	rotated.AccessTokenHash = "rotated-access"
	rotated.RefreshTokenHash = "rotated-refresh"
	must(t, db.RotateMCPToken(ctx, token.ID, rotated))
	if _, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args); err == nil {
		t.Fatal("old token authorized after rotation")
	}
	auth.Token = rotated
	_, err = server.executeMCPFleet(ctx, auth, "fleet_jobs", args)
	must(t, err)
	must(t, db.RevokeMCPTokenForUser(ctx, rotated.ID, user.ID))
	if _, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args); err == nil {
		t.Fatal("revoked connection authorized")
	}
}

func TestLinuxPairingCodeSingleUseAndThrottle(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "pair-user", Email: "pair@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	home := domain.Home{ID: "pair-home", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	create := httptest.NewRequest("POST", "https://hank.example/v1/home/agent-enrollments/linux", strings.NewReader(`{"pairing":true}`))
	out := httptest.NewRecorder()
	server.handleHomeLinuxAgentEnrollments(out, create, home, authContext{User: user}, domain.HomeMembership{Role: domain.HomeRoleAdmin}, []string{"agent-enrollments", "linux"})
	if out.Code != 201 {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	var created struct {
		Code string `json:"pairing_code"`
		ID   string `json:"id"`
	}
	must(t, json.Unmarshal(out.Body.Bytes(), &created))
	if len(created.Code) != 14 {
		t.Fatalf("code length %d", len(created.Code))
	}
	if strings.Contains(out.Body.String(), "install_command") {
		t.Fatal("pairing created a secret URL")
	}
	consume := func(code string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://hank.example/v1/agent/enrollments/linux/consume", strings.NewReader(`{"device_id":"linux_1234567890123456","name":"Paired","agent_type":"worker","platform":"linux","architecture":"amd64","credential_hash":"`+strings.Repeat("a", 64)+`"}`))
		req.Header.Set("Authorization", "Hank-Enrollment "+code)
		response := httptest.NewRecorder()
		server.handleLinuxAgentEnrollmentConsume(response, req)
		return response
	}
	if got := consume(strings.ToLower(created.Code)); got.Code != 201 {
		t.Fatalf("consume: %d %s", got.Code, got.Body.String())
	}
	if got := consume(created.Code); got.Code != 404 {
		t.Fatalf("replay: %d", got.Code)
	}
	for i := 0; i < 10; i++ {
		consume("AAAA-AAAA-AAAA")
	}
	if got := consume("AAAA-AAAA-AAAA"); got.Code != 429 {
		t.Fatal("pairing not throttled")
	}
}

func TestMCPGUISettingPersistsAndRequiresAdmin(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "mcp-setting-user", Email: "settings@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureMCP(MCPConfig{Enabled: false})
	if server.mcpIsEnabled(ctx) {
		t.Fatal("default should be disabled")
	}
	must(t, db.SetMCPEnabled(ctx, true, user.ID))
	if !server.mcpIsEnabled(ctx) {
		t.Fatal("GUI setting did not enable connector")
	}
	restarted := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.ConfigureMCP(MCPConfig{Enabled: false})
	if !restarted.mcpIsEnabled(ctx) {
		t.Fatal("setting did not survive restart")
	}
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "mcp-setting-session", UserID: user.ID, TokenHash: hashToken("mcp-settings-session"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	response := doJSONRequest(t, ts, "mcp-settings-session", "PATCH", "/v1/me/mcp", map[string]any{"enabled": false})
	if response.StatusCode != 403 {
		t.Fatalf("non-member change: %d", response.StatusCode)
	}
	home := domain.Home{ID: "mcp-setting-home", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	response = doJSONRequest(t, ts, "mcp-settings-session", "PATCH", "/v1/me/mcp", map[string]any{"enabled": false})
	if response.StatusCode != 200 || server.mcpIsEnabled(ctx) {
		t.Fatalf("admin disable: %d", response.StatusCode)
	}
}
