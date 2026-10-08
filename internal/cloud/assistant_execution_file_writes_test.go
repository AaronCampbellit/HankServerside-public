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
	"time"
)

func TestExecutionApprovedFolderReconcilesBeforeDispatch(t *testing.T) {
	for _, prior := range []string{"not_found", "confirmed", "unknown"} {
		t.Run(prior, func(t *testing.T) {
			s, task := executionTaskFixture(t)
			ctx := context.Background()
			home, err := s.store.GetHomeByID(ctx, task.HomeID)
			must(t, err)
			writes, statuses := 0, 0
			caps := []string{"files.stat", "files.create_directory", protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}
			agentID := executionFakeAgent(t, s, home, caps, func(command protocol.RoutedCommand) any {
				if command.Command == "files.stat" {
					return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: "local", Path: "Work", IsDirectory: true}}
				}
				var request protocol.AssistantOperationRequest
				if json.Unmarshal(command.Body, &request) != nil {
					return nil
				}
				outcome := prior
				if command.Command == protocol.CommandAssistantOperationExecute {
					writes++
					outcome = "confirmed"
				} else {
					statuses++
				}
				raw, _ := json.Marshal(protocol.AssistantOperationResult{Verification: "observed", Item: &protocol.FileItem{SourceID: "local", Path: "Work/New", IsDirectory: true, ModifiedAt: time.Now().UTC()}})
				return protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: outcome, Result: raw}
			})
			args, _ := json.Marshal(map[string]any{"agent_id": agentID, "source_id": "local", "path": "Work/New"})
			call := assistant.Call{ID: "folder", Tool: "files.create_folder", Version: 1, Arguments: args}
			task = prepareExecutionWrite(t, s, task, call)
			// Persist intent before re-entering the worker, as on process loss between
			// approval consumption and transport acknowledgement.
			step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
			must(t, err)
			approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
			must(t, err)
			task, _, _, err = s.store.BeginAssistantOperation(ctx, task, domain.AssistantOperationReceipt{OperationID: executionOperationID(task.ID, call.ID), HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}, approval.ID)
			must(t, err)
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
			must(t, err)
			expected := "confirmed"
			if prior == "unknown" {
				expected = "unknown"
			}
			if receipt.Outcome != expected || statuses != 1 {
				t.Fatalf("reconciliation: %s, statuses %d", receipt.Outcome, statuses)
			}
			wantWrites := 0
			if prior == "not_found" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("writes %d want %d", writes, wantWrites)
			}
			var result assistant.Result
			must(t, json.Unmarshal(receipt.Result, &result))
			must(t, assistantschema.Validate(result))
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			if prior == "unknown" && task.State != "failed" {
				t.Fatal("uncertain effect did not stop execution")
			}
			if writes != wantWrites {
				t.Fatal("checkpoint replayed effect")
			}
		})
	}
}

func TestExecutionAgentTransportRetriesOnlyReconciliation(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	home, err := s.store.GetHomeByID(ctx, task.HomeID)
	must(t, err)
	statuses, writes := 0, 0
	agentID := executionFakeAgent(t, s, home, []string{"files.stat", "files.create_directory", protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}, func(command protocol.RoutedCommand) any {
		if command.Command == "files.stat" {
			return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: "local", Path: "Work", IsDirectory: true}}
		}
		if command.Command == protocol.CommandAssistantOperationExecute {
			writes++
		}
		statuses++
		// A response with no matching identity cannot prove receipt absence.
		return protocol.AssistantOperationStatusResponse{}
	})
	args, _ := json.Marshal(map[string]string{"agent_id": agentID, "source_id": "local", "path": "Work/New"})
	call := assistant.Call{ID: "retry-folder", Tool: "files.create_folder", Version: 1, Arguments: args}
	task = prepareExecutionWrite(t, s, task, call)
	for attempt := 1; attempt <= 2; attempt++ {
		task, err = s.advanceAssistantExecution(ctx, task, nil)
		must(t, err)
		if attempt < 2 {
			if task.State != "waiting_retry" || task.RetryAt == nil {
				t.Fatal("transport failure was not durably pending")
			}
			_, err = s.store.DB().ExecContext(ctx, `UPDATE assistant_tasks SET retry_at=clock_timestamp() WHERE id=$1`, task.ID)
			must(t, err)
			task, err = s.store.ClaimAssistantTask(ctx, "reconnected-worker", time.Minute)
			must(t, err)
		}
	}
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
	must(t, err)
	if statuses != 2 || writes != 0 || receipt.Outcome != "unknown" {
		t.Fatalf("statuses=%d writes=%d outcome=%s", statuses, writes, receipt.Outcome)
	}
}
