package cloud

import (
	"context"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
	"strings"
	"testing"
)

func TestExecutionServiceApprovalAndAdminBoundary(t *testing.T) {
	for _, scenario := range []string{"confirmed", "unchanged", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, task := executionTaskFixture(t)
			ctx := context.Background()
			home, err := s.store.GetHomeByID(ctx, task.HomeID)
			must(t, err)
			writes := 0
			before := protocol.AssistantServiceState{Unit: "example.service", ActiveState: "active", SubState: "running", InvocationID: "before", AllowedOperations: []string{"restart"}}
			caps := []string{protocol.CommandAssistantServiceInspect, protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}
			agentID := executionFakeAgent(t, s, home, caps, func(command protocol.RoutedCommand) any {
				if command.Command == protocol.CommandAssistantServiceInspect {
					return protocol.AssistantServiceInspectResponse{Services: []protocol.AssistantServiceState{before}}
				}
				var request protocol.AssistantOperationRequest
				if json.Unmarshal(command.Body, &request) != nil {
					return nil
				}
				outcome := "not_found"
				after := before
				if scenario != "unchanged" {
					after.InvocationID = "after"
				}
				if command.Command == protocol.CommandAssistantOperationExecute {
					writes++
					outcome = "confirmed"
				}
				raw, _ := json.Marshal(protocol.AssistantOperationResult{Service: &after, Verification: "observed"})
				return protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: outcome, Result: raw}
			})
			registry, err := s.newAssistantExecutionTools(task.HomeID, task.UserID, task.SessionID)
			must(t, err)
			observed := executionItems(t, invokeExecution(t, registry, "machines.services", map[string]any{"agent_id": agentID, "unit": "example.service"}))
			if len(observed.Items) != 1 || observed.Items[0].Service == nil {
				t.Fatal("service identity missing")
			}
			args, _ := json.Marshal(map[string]any{"agent_id": agentID, "unit": "example.service", "operation": "restart", "revision": observed.Items[0].Revision})
			call := assistant.Call{ID: "restart", Tool: "machines.service_action", Version: 1, Arguments: args}
			task = prepareExecutionWrite(t, s, task, call)
			if scenario == "revoked" {
				must(t, s.store.UpdateHomeMembershipRole(ctx, task.HomeID, task.UserID, domain.HomeRoleMember))
			}
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
			must(t, err)
			expected, wantWrites := "confirmed", 1
			if scenario == "unchanged" {
				expected = "unknown"
			}
			if scenario == "revoked" {
				expected = "failed"
				wantWrites = 0
			}
			if receipt.Outcome != expected || writes != wantWrites {
				t.Fatalf("outcome %s writes %d", receipt.Outcome, writes)
			}
			var result assistant.Result
			must(t, json.Unmarshal(receipt.Result, &result))
			must(t, assistantschema.Validate(result))
			if scenario == "revoked" {
				denied := invokeExecution(t, registry, "machines.services", map[string]any{"agent_id": agentID, "unit": "example.service"})
				if denied.Error == nil || denied.Error.Code != "permission_denied" {
					t.Fatal("non-admin inspected services")
				}
			}
		})
	}
}
