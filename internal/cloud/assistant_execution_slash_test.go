package cloud

import (
	"context"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestExecutionSlashKeepsCommandsExplicitAndRechecksAppAccess(t *testing.T) {
	s, home, user, registry := executionFixture(t)
	ctx := context.Background()
	if _, ok := s.executionSlash(ctx, home.ID, user.ID, "please search notes"); ok {
		t.Fatal("ordinary language became a slash directive")
	}
	directive, ok := s.executionSlash(ctx, home.ID, user.ID, "/notes find work")
	if !ok || directive.Query != "find work" || len(directive.Tools) != 4 || directive.Error != nil {
		t.Fatal("notes directive unavailable")
	}
	app := domain.HomeAgentApp{HomeID: home.ID, AppID: "synthetic", Name: "Synthetic", Version: "1", Enabled: true, SlashCommandsJSON: `[{"command":"/synthetic","command_id":"run"}]`, CommandsJSON: `[{"id":"run","mode":"request_response","admin_only":false}]`, UserAccess: domain.HomeAgentAppUserAccessHomeMembers, Status: "installed", UpdatedAt: time.Now(), UpdatedBy: user.ID}
	must(t, s.store.UpsertHomeApp(ctx, app))
	directive, ok = s.executionSlash(ctx, home.ID, user.ID, "/synthetic exact request")
	if !ok || directive.Call == nil || directive.Call.Tool != "apps.invoke" {
		t.Fatal("installed command not routed to registry")
	}
	// No command is sent: preparation checks live capability but has no executor.
	agent := domain.Agent{ID: "synthetic-agent", HomeID: home.ID}
	connection := s.router.RegisterAgent(home.ID, agent, nil, []string{protocol.CommandAppsInvoke, protocol.CapabilityAppsSandboxV1}, AgentTypePrimary, nil)
	defer s.router.UnregisterAgent(home.ID, agent.ID, connection)
	proposal := registry.Invoke(ctx, *directive.Call)
	if proposal.Error != nil || proposal.Outcome != "not_started" {
		t.Fatalf("proposal failed: %#v", proposal.Error)
	}
	app.Enabled = false
	must(t, s.store.UpsertHomeApp(ctx, app))
	denied := registry.Invoke(ctx, *directive.Call)
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatal("cached slash directive bypassed revoked app access")
	}
	if _, err := s.executionToolContext(ctx, home.ID, user.ID, "unknown_policy"); err == nil {
		t.Fatal("unknown policy allowed")
	}
}
