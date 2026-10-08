package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// Scripted transport uses real WebSocket envelopes and the existing
// connection-bound request registry, without contacting an enrolled machine.
func executionFakeAgent(t *testing.T, s *Server, home domain.Home, caps []string, reply func(protocol.RoutedCommand) any) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	agent := domain.Agent{ID: "agent_execution", HomeID: home.ID, Name: "Synthetic", AgentType: AgentTypePrimary, Status: domain.AgentStatusOnline, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	must(t, s.store.UpsertAgent(ctx, agent))
	ready := make(chan agentReplyBinding, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		peer := newWSPeer(conn)
		id := s.router.RegisterAgent(home.ID, agent, peer, caps, AgentTypePrimary, nil)
		ready <- agentReplyBinding{homeID: home.ID, agentID: agent.ID, peer: peer}
		<-ctx.Done()
		s.router.UnregisterAgent(home.ID, agent.ID, id)
		conn.CloseNow()
	}))
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	must(t, err)
	conn.SetReadLimit(maxWSMessageBytes)
	binding := <-ready
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var envelope protocol.Envelope
			if wsjson.Read(ctx, conn, &envelope) != nil {
				return
			}
			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil {
				return
			}
			payload, err := json.Marshal(reply(command))
			if err != nil {
				return
			}
			response := protocol.Envelope{HomeID: home.ID, AgentID: agent.ID, RequestID: envelope.RequestID, Payload: payload}
			s.agentRequests.Resolve(binding, response)
		}
	}()
	t.Cleanup(func() { cancel(); conn.CloseNow(); server.Close(); <-done })
	return agent.ID
}

func TestExecutionFileResultsRejectEscapesAndPrepareOnly(t *testing.T) {
	s, home, _, registry := executionFixture(t)
	calls := make(chan string, 10)
	agentID := executionFakeAgent(t, s, home, []string{"files.list", "files.stat", "files.create_directory", protocol.CommandAssistantOperationExecute, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}, func(command protocol.RoutedCommand) any {
		calls <- command.Command
		switch command.Command {
		case "files.list":
			return protocol.FilesListResponse{Items: []protocol.FileItem{
				{SourceID: "local", Path: "Work/one", Name: "one", IsDirectory: true},
				{SourceID: "local", Path: "Work/two", Name: "two", IsDirectory: true},
				{SourceID: "other", Path: "Work/private", Name: "foreign"},
				{SourceID: "local", Path: "../private", Name: "escape"},
				{SourceID: "local", Path: "Elsewhere/private", Name: "outside"},
			}}
		case "files.stat":
			return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: "local", Path: "Work/two", Name: "two", IsDirectory: true}}
		default:
			return map[string]any{}
		}
	})
	listing := executionItems(t, invokeExecution(t, registry, "files.list", map[string]any{"agent_id": agentID, "source_id": "local", "path": "Work", "limit": 10}))
	if len(listing.Items) != 2 {
		t.Fatalf("expected only contained source results; got %d", len(listing.Items))
	}
	// Follow-up selects the second structured result, preserving its exact IDs.
	selected := listing.Items[1]
	proposal := invokeExecution(t, registry, "files.create_folder", map[string]any{"agent_id": selected.AgentID, "source_id": selected.SourceID, "path": selected.Path + "/New"})
	executionItems(t, proposal)
	if proposal.Outcome != "not_started" {
		t.Fatal("prepared folder claimed completion")
	}
	if <-calls != "files.list" || <-calls != "files.stat" || len(calls) != 0 {
		t.Fatal("preparation dispatched a write")
	}
}

func TestExecutionHAReadbackAndProposalUseExactAgent(t *testing.T) {
	s, home, user, registry := executionFixture(t)
	calls := make(chan string, 10)
	agentID := executionFakeAgent(t, s, home, []string{"homeassistant.fetch_states", "homeassistant.call_service", protocol.CommandAssistantOperationExecute, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}, func(command protocol.RoutedCommand) any {
		calls <- command.Command
		return protocol.HomeAssistantFetchStatesResponse{States: []protocol.HomeAssistantState{{EntityID: "light.desk", State: "off"}, {EntityID: "sensor.temperature", State: "20"}}}
	})
	result := invokeExecution(t, registry, "homeassistant.call_service", map[string]any{"entity_id": "light.desk", "service": "turn_on"})
	data := executionItems(t, result)
	if result.Outcome != "not_started" || data.Items[0].AgentID != agentID || data.Items[0].State != "off" {
		t.Fatal("proposal lost exact observed state/agent")
	}
	if <-calls != "homeassistant.fetch_states" || len(calls) != 0 {
		t.Fatal("preparation sent a service call")
	}
	denied := invokeExecution(t, registry, "homeassistant.call_service", map[string]any{"entity_id": "sensor.temperature", "service": "turn_on"})
	if denied.Error == nil || denied.Error.Code != "invalid_arguments" {
		t.Fatal("unsupported entity service accepted")
	}
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.HomeAssistantEnabled = false
	must(t, s.store.UpsertAssistantSettings(context.Background(), settings))
	<-calls
	denied = invokeExecution(t, registry, "homeassistant.get", map[string]any{"entity_id": "light.desk"})
	if denied.Error == nil || denied.Error.Code != "permission_denied" || len(calls) != 0 {
		t.Fatal("revoked access dispatched to agent")
	}
}
