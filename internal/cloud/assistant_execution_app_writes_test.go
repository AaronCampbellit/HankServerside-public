package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
)

func TestExecutionOpaqueAppApprovedOnceAndNeverReplayed(t *testing.T) {
	for _, scenario := range []string{"accepted", "result_revoked", "interrupted", "revoked", "changed_version"} {
		t.Run(scenario, func(t *testing.T) {
			s, task := executionTaskFixture(t)
			ctx := context.Background()
			home, err := s.store.GetHomeByID(ctx, task.HomeID)
			must(t, err)
			app := domain.HomeAgentApp{HomeID: home.ID, AppID: "synthetic", Name: "Synthetic", Version: "1", Enabled: true, SlashCommandsJSON: `[{"command":"/synthetic","command_id":"run"}]`, CommandsJSON: `[{"id":"run","mode":"request_response","admin_only":false}]`, UserAccess: domain.HomeAgentAppUserAccessHomeMembers, Status: "installed", UpdatedAt: time.Now(), UpdatedBy: task.UserID}
			must(t, s.store.UpsertHomeApp(ctx, app))
			var writes atomic.Int32
			executionFakeAgent(t, s, home, []string{protocol.CommandAppsInvoke, protocol.CapabilityAppsSandboxV1}, func(command protocol.RoutedCommand) any {
				if command.Command != protocol.CommandAppsInvoke {
					t.Errorf("unexpected command %s", command.Command)
				}
				writes.Add(1)
				var request protocol.AppsInvokeRequest
				if json.Unmarshal(command.Body, &request) != nil || request.AppID != app.AppID || request.CommandID != "run" {
					t.Error("wrong app target")
				}
				return protocol.AppsInvokeResponse{Output: json.RawMessage(`{"text":"Request accepted"}`)}
			})
			call := assistant.Call{ID: "explicit-app", Tool: "apps.invoke", Version: 1, Arguments: json.RawMessage(`{"app_id":"synthetic","command_id":"run","version":"1","query":"exact request"}`)}
			checkpoint := assistantExecutionCheckpoint{Version: 2, ExplicitApp: &call, Messages: []assistant.Message{{Role: "user", Text: "/synthetic exact request"}}}
			task, err = encodeExecutionCheckpoint(task, checkpoint)
			must(t, err)
			_, err = s.store.DB().ExecContext(ctx, `UPDATE assistant_tasks SET checkpoint=$1::jsonb WHERE id=$2`, string(task.Checkpoint), task.ID)
			must(t, err)
			task = prepareExecutionWrite(t, s, task, call)
			step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
			must(t, err)
			var proposal assistant.Result
			must(t, json.Unmarshal(step.Result, &proposal))
			if executionApprovalSummary(step, proposal) == nil {
				t.Fatal("missing exact approval preview")
			}
			if scenario == "interrupted" {
				approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
				must(t, e)
				task, _, _, err = s.store.BeginAssistantOperation(ctx, task, domain.AssistantOperationReceipt{OperationID: executionOperationID(task.ID, call.ID), HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}, approval.ID)
				must(t, err)
			}
			if scenario == "revoked" {
				app.Enabled = false
				must(t, s.store.UpsertHomeApp(ctx, app))
			}
			if scenario == "changed_version" {
				app.Version = "2"
				must(t, s.store.UpsertHomeApp(ctx, app))
			}
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
			must(t, err)
			want := "failed"
			count := int32(0)
			if scenario == "accepted" || scenario == "result_revoked" {
				want = "accepted"
				count = 1
			}
			if scenario == "interrupted" {
				want = "unknown"
			}
			if receipt.Outcome != want || writes.Load() != count {
				t.Fatalf("outcome %s writes %d", receipt.Outcome, writes.Load())
			}
			var result assistant.Result
			must(t, json.Unmarshal(receipt.Result, &result))
			must(t, assistantschema.Validate(result))
			if want == "accepted" && result.Verification != "unavailable" {
				t.Fatal("opaque app falsely verified")
			}
			task, err = s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			if writes.Load() != count {
				t.Fatal("recovery replayed opaque effect")
			}
			if scenario == "accepted" || scenario == "result_revoked" {
				if scenario == "result_revoked" {
					app.Enabled = false
					must(t, s.store.UpsertHomeApp(ctx, app))
				}
				task, err = s.advanceAssistantExecution(ctx, task, executionModelFunc(func(_ context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
					last := input.Messages[len(input.Messages)-1]
					if scenario == "result_revoked" {
						for _, message := range input.Messages {
							if message.Result != nil {
								t.Fatal("revoked app evidence retained")
							}
						}
					} else if last.Result == nil || last.Result.Outcome != "accepted" {
						t.Fatal("app receipt lost before continuation")
					}
					return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "final", Text: "It succeeded."}, nil
				}))
				must(t, err)
				var saved assistantExecutionCheckpoint
				must(t, json.Unmarshal(task.Checkpoint, &saved))
				if task.State != "completed" || saved.FinalText == "It succeeded." {
					t.Fatal("unverified app completion incorrectly reported")
				}
			}
		})
	}
}

func TestExecutionExplicitAppAdmissionPreparesBeforeAnyDispatch(t *testing.T) {
	s, home, user, _ := executionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	must(t, s.store.CreateSession(ctx, domain.AppSession{ID: "explicit-auth", UserID: user.ID, TokenHash: hashToken("explicit-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, s.store.CreateAssistantSession(ctx, domain.AssistantSession{ID: "explicit-session", HomeID: home.ID, UserID: user.ID, Title: "Explicit", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}))
	must(t, s.store.UpsertHomeApp(ctx, domain.HomeAgentApp{HomeID: home.ID, AppID: "synthetic", Name: "Synthetic", Version: "1", Enabled: true, SlashCommandsJSON: `[{"command":"/synthetic","command_id":"run"}]`, CommandsJSON: `[{"id":"run","mode":"request_response","admin_only":false}]`, UserAccess: domain.HomeAgentAppUserAccessHomeMembers, Status: "installed", UpdatedAt: now, UpdatedBy: user.ID}))
	var writes atomic.Int32
	executionFakeAgent(t, s, home, []string{protocol.CommandAppsInvoke, protocol.CapabilityAppsSandboxV1}, func(command protocol.RoutedCommand) any {
		writes.Add(1)
		return protocol.AppsInvokeResponse{Output: json.RawMessage(`{"text":"Synthetic reply"}`)}
	})
	s.ConfigureAssistantAI(AssistantAIConfig{ExecutionEnabled: true, Provider: "ollama", OllamaBaseURL: "http://127.0.0.1:1", OllamaChatModel: "synthetic"})
	req := httptest.NewRequest(http.MethodPost, "/v1/home/assistant/sessions/explicit-session/messages", strings.NewReader(`{"submission_id":"explicit-key","content":"/synthetic exact request"}`))
	req.Header.Set("Authorization", "Bearer explicit-token")
	req.Header.Set("X-Hank-Assistant-Execution", "2")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, req)
	if response.Code != 202 {
		t.Fatalf("admission %d: %s", response.Code, response.Body.String())
	}
	task, err := s.store.ClaimAssistantTask(ctx, "explicit-worker", time.Minute)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	if task.State != "waiting_approval" || writes.Load() != 0 {
		t.Fatal("slash command bypassed exact approval")
	}
	snapshot, err := s.assistantExecutionSnapshot(ctx, task)
	must(t, err)
	if snapshot["pending_approval"] == nil {
		t.Fatal("explicit app approval not visible")
	}
	var checkpoint assistantExecutionCheckpoint
	must(t, json.Unmarshal(task.Checkpoint, &checkpoint))
	if checkpoint.ExplicitApp == nil || len(checkpoint.Pending) != 1 || checkpoint.Pending[0].Tool != "apps.invoke" {
		t.Fatal("explicit target was not durably bound")
	}
}
