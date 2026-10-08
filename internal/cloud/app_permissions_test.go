package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"testing"
	"time"
)

func TestAppCommandAdminScopeAndManagementBoundary(t *testing.T) {
	app := domain.HomeAgentApp{Enabled: true, UserAccess: "home_members", CommandsJSON: `[{"id":"read"},{"id":"settings_apply","admin_only":true}]`}
	member := domain.HomeMembership{Role: domain.HomeRoleMember}
	admin := domain.HomeMembership{Role: domain.HomeRoleAdmin}
	if canUseHomeAgentAppCommand(app, member, "settings_apply") || !canUseHomeAgentAppCommand(app, member, "read") || !canUseHomeAgentAppCommand(app, admin, "settings_apply") {
		t.Fatal("command role boundary failed")
	}
	for _, command := range []string{protocol.CommandAppsPackageActivate, protocol.CommandAppsConfigApply, protocol.CommandAppsUninstall, protocol.CommandAppsPackagePreview} {
		if !isManagementCommand(command) {
			t.Fatal("app management command lacks admin gate", command)
		}
	}
}
func TestAppGrantChangesDeniedToMemberWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, server, homeID, _, _, agent := setupServerAndAgentWithDB(t, ctx)
	defer server.Close()
	defer agent.Close(websocket.StatusNormalClosure, "done")
	now := time.Now().UTC()
	member := domain.User{ID: "grant-member", Email: "grant-member@example.test", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, member))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: homeID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "grant-member", UserID: member.ID, TokenHash: hashToken("grant-member"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))
	conn, _, err := appWebSocketDial(ctx, server, "grant-member")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	sendAppCommandExpectError(t, ctx, conn, "grant-attempt", protocol.CommandAppsConfigApply, map[string]any{"app_id": "hermes", "permission_grants": map[string]string{"network:base": "forged"}}, "permission_denied")
}
func TestAppInvokeRechecksCommandRoleFromDurableMetadata(t *testing.T) {
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	u := domain.User{ID: "app-admin", Email: "app-admin@example.test", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, u))
	h := domain.Home{ID: "app-home", UserID: u.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateHome(ctx, h))
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{HomeID: h.ID, AppID: "test", Enabled: true, UserAccess: "home_members", CommandsJSON: `[{"id":"admin","admin_only":true}]`, PublicConfigJSON: `{}`, SecretFieldsSetJSON: `{}`, UpdatedBy: u.ID, UpdatedAt: now}))
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	body, _ := json.Marshal(protocol.AppsInvokeRequest{AppID: "test", CommandID: "admin"})
	err := s.authorizeAppsInvokeCommand(ctx, h.ID, domain.HomeMembership{Role: domain.HomeRoleMember}, protocol.RoutedCommand{Command: protocol.CommandAppsInvoke, Body: body})
	if !errors.Is(err, errAppInvokeAccessDenied) {
		t.Fatal("admin command allowed", err)
	}
}

func TestAppActorStampReplacesSpoofedRoleAndIdentity(t *testing.T) {
	body, err := stampAppInvocation(json.RawMessage(`{"app_id":"test","command_id":"read","actor_role":"admin","context":{"role":"admin","user_id":"victim","home_id":"other","trace_id":"client-trace"}}`), "home", "member", "member")
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.AppsInvokeRequest
	must(t, json.Unmarshal(body, &request))
	var fields map[string]string
	must(t, json.Unmarshal(request.Context, &fields))
	if request.ActorRole != "member" || fields["role"] != "member" || fields["user_id"] != "member" || fields["home_id"] != "home" || fields["trace_id"] != "client-trace" {
		t.Fatal("client spoofed authoritative app context")
	}
}

func TestAppSandboxCapabilityCannotFollowReplacedConnection(t *testing.T) {
	router := NewRouter()
	agent := domain.Agent{ID: "agent", HomeID: "home"}
	router.RegisterAgent("home", agent, nil, []string{protocol.CapabilityAppsSandboxV1}, AgentTypePrimary, nil)
	first, _ := router.GetAgent("home")
	if !router.supportsCurrentAgent(first, protocol.CapabilityAppsSandboxV1) {
		t.Fatal("updated agent capability not recognized")
	}
	router.RegisterAgent("home", agent, nil, nil, AgentTypePrimary, nil)
	replacement, _ := router.GetAgent("home")
	if router.supportsCurrentAgent(first, protocol.CapabilityAppsSandboxV1) || router.supportsCurrentAgent(replacement, protocol.CapabilityAppsSandboxV1) {
		t.Fatal("stale sandbox capability authorized replacement")
	}
}
