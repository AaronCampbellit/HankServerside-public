package agent

import (
	"context"
	"encoding/json"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssistantHAOnePostAndBoundedReadback(t *testing.T) {
	for _, scenario := range []string{"confirmed", "accepted", "stale", "press"} {
		t.Run(scenario, func(t *testing.T) {
			posts, reads := 0, 0
			entity, service, state := "light.desk", "turn_on", "off"
			if scenario == "press" {
				entity, service, state = "button.doorbell", "press", "unknown"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					w.WriteHeader(401)
					return
				}
				if r.Method == "POST" {
					posts++
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 1 || body["entity_id"] != entity {
						t.Error("wrong service target")
					}
					w.Write([]byte(`[]`))
					return
				}
				reads++
				observed := state
				if scenario == "stale" || (scenario == "confirmed" && posts > 0 && reads >= 3) {
					observed = "on"
				}
				json.NewEncoder(w).Encode(map[string]string{"entity_id": entity, "state": observed})
			}))
			defer server.Close()
			journal, err := operations.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			client := NewClient("", "agent", "", "", "", agentha.New(server.URL, "fixture-token", time.Second), nil, nil, nil, nil)
			client.SetOperationJournal(journal)
			client.registeredHomeID = "home"
			args, _ := json.Marshal(protocol.AssistantHAOperationArguments{EntityID: entity, Service: service, PriorState: state})
			identity := protocol.AssistantOperationIdentity{JournalEpoch: journal.Epoch(), OperationID: "ha-one", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "homeassistant.call_service", ToolVersion: 1}
			body, _ := json.Marshal(protocol.AssistantOperationRequest{Identity: identity, Arguments: args})
			invoke := func() (protocol.AssistantOperationStatusResponse, error) {
				return client.executeAssistantOperation(context.Background(), protocol.Envelope{HomeID: "home", AgentID: "agent"}, protocol.RoutedCommand{Command: protocol.CommandAssistantOperationExecute, Body: body})
			}
			receipt, err := invoke()
			if err != nil {
				t.Fatal(err)
			}
			expected := "accepted"
			if scenario == "confirmed" {
				expected = "confirmed"
			}
			if scenario == "stale" {
				expected = "failed"
			}
			if receipt.Outcome != expected {
				t.Fatalf("outcome %s want %s", receipt.Outcome, expected)
			}
			beforePosts, beforeReads := posts, reads
			if _, err := invoke(); err != nil {
				t.Fatal(err)
			}
			if posts != beforePosts || reads != beforeReads {
				t.Fatal("receipt recovery repeated HA command or observation")
			}
			wantPosts := 1
			if scenario == "stale" {
				wantPosts = 0
			}
			if posts != wantPosts || reads > 4 {
				t.Fatalf("unbounded activity: posts %d reads %d", posts, reads)
			}
		})
	}
}
