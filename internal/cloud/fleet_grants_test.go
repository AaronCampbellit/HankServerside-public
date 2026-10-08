package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestFleetGrantApprovalScopeRevocationAndIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ts, home, agent, session, conn := setupServerAndAgent(t, ctx)
	defer ts.Close()
	defer conn.CloseNow()
	cookieRequest, err := http.NewRequest("POST", ts.URL+"/v1/fleet/grants", strings.NewReader(`{}`))
	must(t, err)
	cookieRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	cookieResponse, err := ts.Client().Do(cookieRequest)
	must(t, err)
	cookieResponse.Body.Close()
	if cookieResponse.StatusCode != 403 {
		t.Fatal("cookie grant write bypassed CSRF")
	}
	var requested struct {
		Grant struct {
			ID string `json:"id"`
		} `json:"grant"`
		Token string `json:"token"`
	}
	requestJSON(t, ts, session, "POST", "/v1/fleet/grants", map[string]any{"agents": []string{agent}, "operations": []string{"workspace.write", "workspace.read", "job.run", "job.read", "job.cancel"}, "hours": 1}, &requested)
	if requested.Token == "" {
		t.Fatal("no pending token")
	}
	if response := doJSONRequest(t, ts, requested.Token, "GET", "/v1/fleet/agents", nil); response.StatusCode != 401 {
		t.Fatal("pending grant authorized")
	}
	var preview struct {
		Token        string `json:"action_token"`
		Confirmation string `json:"confirmation"`
	}
	endpoint := "/v1/fleet/grants/" + requested.Grant.ID + "/approve"
	requestJSON(t, ts, session, "POST", endpoint, map[string]any{}, &preview)
	approval := map[string]any{"action_token": preview.Token, "confirmation": preview.Confirmation}
	requestJSON(t, ts, session, "POST", endpoint, approval, nil)
	if response := doJSONRequest(t, ts, session, "POST", endpoint, approval); response.StatusCode != 403 {
		t.Fatal("approval token replay accepted")
	}
	if response := doJSONRequest(t, ts, requested.Token, "POST", "/v1/fleet/workspaces", map[string]any{"agent_id": "foreign-agent"}); response.StatusCode != 404 {
		t.Fatal("foreign target authorized")
	}
	// Fleet commands cannot bypass grants through the generic administrator relay.
	app, _, err := appWebSocketDial(ctx, ts, session)
	must(t, err)
	defer app.CloseNow()
	body, _ := json.Marshal(protocol.FleetRequest{GrantID: requested.Grant.ID, WorkspaceID: "workspace123"})
	envelope, _ := protocol.NewEnvelope(protocol.TypeAppCommand, "bypass-test", agent, home, protocol.RoutedCommand{Command: "fleet.workspace.create", Body: body})
	must(t, wsjson.Write(ctx, app, envelope))
	var denied protocol.Envelope
	must(t, wsjson.Read(ctx, app, &denied))
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatal("raw relay bypass accepted")
	}
	// Refresh capability; requests below wait for inventory to observe it.
	heartbeat, _ := protocol.NewEnvelope(protocol.TypeAgentHeartbeat, "", agent, home, protocol.AgentHeartbeat{Capabilities: []string{protocol.CapabilityFleetV1}})
	must(t, wsjson.Write(ctx, conn, heartbeat))
	deadline := time.Now().Add(2 * time.Second)
	for {
		var list struct {
			Agents []struct {
				Capabilities []string `json:"capabilities"`
			} `json:"agents"`
		}
		requestJSON(t, ts, requested.Token, "GET", "/v1/fleet/agents", nil, &list)
		if len(list.Agents) > 0 && len(list.Agents[0].Capabilities) > 0 && list.Agents[0].Capabilities[0] == protocol.CapabilityFleetV1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capability update missing")
		}
		time.Sleep(time.Millisecond)
	}
	starts := make(chan string, 4)
	go func() {
		for {
			var e protocol.Envelope
			if wsjson.Read(ctx, conn, &e) != nil {
				return
			}
			var c protocol.RoutedCommand
			_ = json.Unmarshal(e.Payload, &c)
			var r protocol.FleetRequest
			_ = json.Unmarshal(c.Body, &r)
			var result any
			switch c.Command {
			case "fleet.workspace.create":
				result = map[string]any{"workspace_id": r.WorkspaceID}
			case "fleet.job.start":
				starts <- r.JobID
				result = protocol.FleetJob{ID: r.JobID, GrantID: r.GrantID, WorkspaceID: r.WorkspaceID, State: "succeeded"}
			default:
				result = map[string]any{}
			}
			response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, e.RequestID, agent, home, result)
			_ = wsjson.Write(ctx, conn, response)
		}
	}()
	var workspace struct {
		ID string `json:"workspace_id"`
	}
	requestJSON(t, ts, requested.Token, "POST", "/v1/fleet/workspaces", map[string]any{"agent_id": agent}, &workspace)
	if workspace.ID == "" {
		t.Fatal("workspace missing")
	}
	jobBody := map[string]any{"agent_id": agent, "workspace_id": workspace.ID, "job_id": "job_repeat123", "command": "true", "grant_id": "forged-grant"}
	var job protocol.FleetJob
	requestJSON(t, ts, requested.Token, "POST", "/v1/fleet/jobs", jobBody, &job)
	if job.GrantID != requested.Grant.ID {
		t.Fatal("grant ownership not stamped")
	}
	requestJSON(t, ts, requested.Token, "POST", "/v1/fleet/jobs", jobBody, &job)
	if len(starts) != 1 {
		t.Fatalf("job started %d times", len(starts))
	}
	jobBody["command"] = "false"
	if response := doJSONRequest(t, ts, requested.Token, "POST", "/v1/fleet/jobs", jobBody); response.StatusCode != 409 {
		t.Fatal("changed idempotency request accepted")
	}
	endpoint = "/v1/fleet/grants/" + requested.Grant.ID + "/revoke"
	requestJSON(t, ts, session, "POST", endpoint, map[string]any{}, &preview)
	requestJSON(t, ts, session, "POST", endpoint, map[string]any{"action_token": preview.Token, "confirmation": preview.Confirmation}, nil)
	if response := doJSONRequest(t, ts, requested.Token, "GET", "/v1/fleet/jobs", nil); response.StatusCode != 401 {
		t.Fatal("revoked grant authorized")
	}
}

func TestFleetGrantMemberApprovalDeniedAndScopeEnforced(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	admin := domain.User{ID: "admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	home := domain.Home{ID: "home", UserID: admin.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.UpsertAgent(ctx, domain.Agent{ID: "agent", HomeID: home.ID, Name: "Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}))
	for _, u := range []domain.User{admin, member} {
		must(t, db.CreateSession(ctx, domain.AppSession{ID: u.ID, UserID: u.ID, TokenHash: hashToken(u.ID), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	}
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	var requested struct {
		Grant struct {
			ID string `json:"id"`
		} `json:"grant"`
		Token string `json:"token"`
	}
	requestJSON(t, ts, "member", "POST", "/v1/fleet/grants", map[string]any{"agents": []string{"agent"}, "operations": []string{"workspace.read"}, "hours": 1}, &requested)
	endpoint := "/v1/fleet/grants/" + requested.Grant.ID + "/approve"
	if response := doJSONRequest(t, ts, "member", "POST", endpoint, map[string]any{}); response.StatusCode != 403 {
		t.Fatal("member self-approved")
	}
	var preview struct {
		Token        string `json:"action_token"`
		Confirmation string `json:"confirmation"`
	}
	requestJSON(t, ts, "admin", "POST", endpoint, map[string]any{}, &preview)
	requestJSON(t, ts, "admin", "POST", endpoint, map[string]any{"action_token": preview.Token, "confirmation": preview.Confirmation}, nil)
	if response := doJSONRequest(t, ts, requested.Token, "POST", "/v1/fleet/jobs", map[string]any{}); response.StatusCode != 403 {
		t.Fatal("read grant executed job")
	}
	must(t, db.RemoveHomeMembership(ctx, home.ID, member.ID))
	if response := doJSONRequest(t, ts, requested.Token, "GET", "/v1/fleet/agents", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatal("former member retained fleet access")
	}
}
