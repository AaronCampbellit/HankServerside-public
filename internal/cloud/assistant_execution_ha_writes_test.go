package cloud

import (
	"context"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
	"strings"
	"testing"
)

func TestExecutionHAApprovedResultDistinguishesAcceptedAndObserved(t *testing.T) {
	for _, outcome := range []string{"confirmed", "accepted", "wrong-state", "wrong-entity"} {
		t.Run(outcome, func(t *testing.T) {
			s, task := executionTaskFixture(t)
			ctx := context.Background()
			home, err := s.store.GetHomeByID(ctx, task.HomeID)
			must(t, err)
			writes := 0
			caps := []string{"homeassistant.fetch_states", "homeassistant.call_service", protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}
			executionFakeAgent(t, s, home, caps, func(command protocol.RoutedCommand) any {
				if command.Command == "homeassistant.fetch_states" {
					return protocol.HomeAssistantFetchStatesResponse{States: []protocol.HomeAssistantState{{EntityID: "light.desk", State: "off"}}}
				}
				var request protocol.AssistantOperationRequest
				if json.Unmarshal(command.Body, &request) != nil {
					return nil
				}
				result := protocol.AssistantOperationResult{EntityID: "light.desk", State: "on", Verification: "observed"}
				status := "not_found"
				if command.Command == protocol.CommandAssistantOperationExecute {
					writes++
					status = outcome
					if outcome == "accepted" {
						result.Verification = "unavailable"
						result.State = "off"
					}
					if outcome == "wrong-state" {
						status = "confirmed"
						result.State = "off"
					}
					if outcome == "wrong-entity" {
						status = "confirmed"
						result.EntityID = "light.other"
					}
				}
				raw, _ := json.Marshal(result)
				return protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: status, Result: raw}
			})
			call := assistant.Call{ID: "ha", Tool: "homeassistant.call_service", Version: 1, Arguments: json.RawMessage(`{"entity_id":"light.desk","service":"turn_on"}`)}
			task = prepareExecutionWrite(t, s, task, call)
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
			must(t, err)
			expected := outcome
			if strings.HasPrefix(outcome, "wrong-") {
				expected = "unknown"
			}
			if writes != 1 || receipt.Outcome != expected {
				t.Fatalf("writes %d outcome %s", writes, receipt.Outcome)
			}
			var result assistant.Result
			must(t, json.Unmarshal(receipt.Result, &result))
			must(t, assistantschema.Validate(result))
			_, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			if writes != 1 {
				t.Fatal("HA mutation repeated")
			}
		})
	}
}
