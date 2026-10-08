package cloud

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestFleetAccountAccessAcrossAppsAndNewDevices(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	admin := domain.User{ID: "account-admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "account-member", Email: "member@example.com", DisplayName: "Member", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	home := domain.Home{ID: "account-home", UserID: admin.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	for _, user := range []domain.User{admin, member} {
		must(t, db.CreateSession(ctx, domain.AppSession{ID: user.ID, UserID: user.ID, TokenHash: hashToken(user.ID), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	}
	must(t, db.CreateMCPOAuthClient(ctx, domain.MCPOAuthClient{ID: "account-client", ClientName: "Test", RedirectURIs: []string{"https://example.com/callback"}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, CreatedAt: now}))
	tokens := []domain.MCPToken{}
	for _, id := range []string{"account-app-one", "account-app-two", "account-admin-app"} {
		owner := member.ID
		if id == "account-admin-app" {
			owner = admin.ID
		}
		token := domain.MCPToken{ID: id, UserID: owner, ClientID: "account-client", AccessTokenHash: id + "-access", RefreshTokenHash: id + "-refresh", Scopes: []string{"docs:read"}, Resource: "https://example.com/v1/mcp", AccessExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
		must(t, db.CreateMCPToken(ctx, token))
		tokens = append(tokens, token)
	}
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	for _, body := range []map[string]any{
		{"mcp_token_id": tokens[0].ID, "duration": "infinite"},
		{"mcp_account": true, "agents": []string{"one"}, "duration": "infinite"},
		{"mcp_account": true, "operations": []string{"job.read"}, "duration": "infinite"},
	} {
		response := doJSONRequest(t, ts, member.ID, "POST", "/v1/fleet/grants", body)
		if response.StatusCode != 400 {
			t.Fatalf("scoped account request accepted: %d", response.StatusCode)
		}
	}
	var requested struct {
		Grant store.FleetGrant `json:"grant"`
		Token string           `json:"token"`
	}
	requestJSON(t, ts, member.ID, "POST", "/v1/fleet/grants", map[string]any{"mcp_account": true, "duration": "infinite"}, &requested)
	if !requested.Grant.MCPAccount || requested.Grant.MCPTokenID != "" || requested.Token != "" || !slices.Equal(requested.Grant.Operations, protocol.FleetOperations) {
		t.Fatalf("incorrect account grant: %+v", requested.Grant)
	}
	args, _ := json.Marshal(map[string]any{"grant_id": requested.Grant.ID})
	auth := mcpAuthContext{User: member, Token: tokens[0]}
	if _, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args); err == nil {
		t.Fatal("pending account access allowed")
	}
	endpoint := "/v1/fleet/grants/" + requested.Grant.ID + "/approve"
	response := doJSONRequest(t, ts, member.ID, "POST", endpoint, map[string]any{})
	if response.StatusCode != 403 {
		t.Fatal("member approved own account")
	}
	var preview struct {
		Token        string `json:"action_token"`
		Confirmation string `json:"confirmation"`
	}
	requestJSON(t, ts, admin.ID, "POST", endpoint, map[string]any{}, &preview)
	requestJSON(t, ts, admin.ID, "POST", endpoint, map[string]any{"action_token": preview.Token, "confirmation": preview.Confirmation}, nil)
	for _, token := range tokens[:2] {
		auth.Token = token
		if _, err := server.executeMCPFleet(ctx, auth, "fleet_jobs", args); err != nil {
			t.Fatalf("same account app denied: %v", err)
		}
	}
	// Devices enrolled after approval must be covered without another grant.
	agent := domain.Agent{ID: "new-account-device", HomeID: home.ID, Name: "New device", Status: "offline", AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	must(t, db.UpsertAgent(ctx, agent))
	grant, err := db.FleetGrantForMCP(ctx, requested.Grant.ID, tokens[1].ID, member.ID)
	must(t, err)
	if !slices.Contains(grant.Agents, agent.ID) || grant.RequesterName != "Member" {
		t.Fatal("new device or account name missing")
	}
	must(t, db.CreateFleetWorkspace(ctx, "new-account-workspace", grant.ID, agent.ID))
	must(t, db.CreateFleetWorkspace(ctx, "new-account-workspace", grant.ID, agent.ID)) // Stable retry.
	if _, err := db.FleetGrantByHash(ctx, "unused"); err == nil {
		t.Fatal("account grant used bearer transport")
	}
	// Another account cannot use the grant, even with Home administrator privileges.
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, tokens[2].ID, admin.ID); err == nil {
		t.Fatal("different account authorized")
	}
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, tokens[2].ID, member.ID); err == nil {
		t.Fatal("token owner mismatch authorized")
	}
	other := mcpAuthContext{User: admin, Token: tokens[2]}
	result, err := server.executeMCPFleet(ctx, other, "fleet_agents", json.RawMessage(`{}`))
	must(t, err)
	if strings.Contains(result.Text, grant.ID) {
		t.Fatal("grant leaked to another account")
	}
	foreignHome := domain.Home{ID: "foreign-account-home", UserID: admin.ID, Name: "Foreign", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, foreignHome))
	foreignAgent := agent
	foreignAgent.ID = "foreign-account-device"
	foreignAgent.HomeID = foreignHome.ID
	must(t, db.UpsertAgent(ctx, foreignAgent))
	if db.CreateFleetWorkspace(ctx, "foreign-account-workspace", grant.ID, foreignAgent.ID) == nil {
		t.Fatal("cross-Home workspace allowed")
	}
	if db.CreateFleetWorkspace(ctx, "different-grant-workspace", "missing-grant", agent.ID) == nil {
		t.Fatal("unowned workspace allowed")
	}
	// App disconnect stops that credential, but leaves other apps and durable work usable.
	_, err = db.CreateFleetJob(ctx, "account-job", grant.ID, agent.ID, "new-account-workspace", "request-hash")
	must(t, err)
	must(t, db.RevokeMCPTokenForUser(ctx, tokens[0].ID, member.ID))
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, tokens[0].ID, member.ID); err == nil {
		t.Fatal("revoked credential allowed")
	}
	_, err = db.FleetGrantForMCP(ctx, grant.ID, tokens[1].ID, member.ID)
	must(t, err)
	jobs, err := db.FleetAgentJobs(ctx, agent.ID)
	must(t, err)
	if len(jobs) != 1 || jobs[0].CancelRequested {
		t.Fatal("one app disconnect cancelled account job")
	}
	rotated := tokens[1]
	rotated.ID = "account-app-rotated"
	rotated.AccessTokenHash = "rotated-access"
	rotated.RefreshTokenHash = "rotated-refresh"
	must(t, db.RotateMCPToken(ctx, tokens[1].ID, rotated))
	_, err = db.FleetGrantForMCP(ctx, grant.ID, rotated.ID, member.ID)
	must(t, err)
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, tokens[1].ID, member.ID); err == nil {
		t.Fatal("rotated old token allowed")
	}
	must(t, db.RemoveHomeMembership(ctx, home.ID, member.ID))
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, rotated.ID, member.ID); err == nil {
		t.Fatal("former member allowed")
	}
	jobs, err = db.FleetAgentJobs(ctx, agent.ID)
	must(t, err)
	if len(jobs) != 1 || !jobs[0].CancelRequested {
		t.Fatal("membership removal failed to request cancellation")
	}
}
