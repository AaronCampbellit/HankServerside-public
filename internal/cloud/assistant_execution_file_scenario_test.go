package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// Real admission, staging, worker, approvals, database and agent transport.
// Only the file server is synthetic; no production files or credentials enter
// either the deterministic test or the opt-in live-model replay.
func runExecutionFileScenario(t *testing.T, live bool) {
	t.Helper()
	ctx := context.Background()
	s, home, user, _ := executionFixture(t)
	s.ConfigureNoteAttachmentStorage(t.TempDir())
	base, modelName := "http://unused.invalid", "synthetic"
	if live {
		base, modelName = os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_OLLAMA"), os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_MODEL")

	}
	s.ConfigureAssistantAI(AssistantAIConfig{ExecutionEnabled: true, Provider: "ollama", OllamaBaseURL: base, OllamaChatModel: modelName})
	now := time.Now().UTC()
	session := domain.AssistantSession{ID: "file-scenario", HomeID: home.ID, UserID: user.ID, Title: "Synthetic file replay", CreatedAt: now, UpdatedAt: now, LastMessageAt: now}
	must(t, s.store.CreateAssistantSession(ctx, session))
	must(t, s.store.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{HomeID: home.ID, ServiceType: domain.ServiceTypeSMB, PublicConfigJSON: `{"file_sources":[{"id":"local","type":"local","local_root_enabled":true},{"id":"tax-share","type":"smb","smb_enabled":true}]}`, UpdatedAt: now, UpdatedBy: user.ID}))
	var lock sync.Mutex
	staged, written := map[string][]byte{}, map[string][]byte{}
	receipts := map[string]protocol.AssistantOperationStatusResponse{}
	searches, folderWrites := 0, 0
	caps := []string{"files.search", "files.list", "files.stat", "files.upload", "files.create_directory", protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CommandAssistantOperationStage, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("b", 32)}
	agent := executionFakeAgent(t, s, home, caps, func(command protocol.RoutedCommand) any {
		lock.Lock()
		defer lock.Unlock()
		switch command.Command {
		case "files.search", "files.list":
			var args map[string]any
			_ = json.Unmarshal(command.Body, &args)
			searches++
			items := []protocol.FileItem{}
			if executionArg(args, "source_id") == "tax-share" {
				candidates := []protocol.FileItem{{SourceID: "tax-share", Path: "2025 Taxes", Name: "2025 Taxes", IsDirectory: true}}
				for destination, data := range written {
					candidates = append(candidates, protocol.FileItem{SourceID: "tax-share", Path: destination, Name: path.Base(destination), Size: int64(len(data))})
				}
				sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
				for _, item := range candidates {
					if command.Command == "files.list" && cleanPolicyPath(path.Dir(item.Path)) != cleanPolicyPath(executionArg(args, "path")) {
						continue
					}
					if command.Command == "files.search" && !strings.Contains(strings.ToLower(item.Path), strings.ToLower(executionArg(args, "query"))) {
						continue
					}
					items = append(items, item)
				}
			}
			return protocol.FilesListResponse{Items: items}
		case "files.stat":
			var args protocol.FilesStatRequest
			_ = json.Unmarshal(command.Body, &args)
			folder := strings.TrimPrefix(cleanPolicyPath(args.Path), "/")
			if data, ok := written[folder]; ok && args.SourceID == "tax-share" {
				return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: args.SourceID, Path: folder, Name: path.Base(folder), Size: int64(len(data))}}
			}
			if folder != "" && (args.SourceID != "tax-share" || folder != "2025 Taxes") {
				return nil
			}
			return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: args.SourceID, Path: folder, Name: path.Base(args.Path), IsDirectory: true}}
		case protocol.CommandAssistantOperationStage:
			var request protocol.AssistantOperationStageRequest
			_ = json.Unmarshal(command.Body, &request)
			data := staged[request.Identity.OperationID]
			if int64(len(data)) != request.Offset {
				return nil
			}
			staged[request.Identity.OperationID] = append(data, request.Data...)
			return protocol.AssistantOperationStageResponse{Identity: request.Identity, NextOffset: int64(len(staged[request.Identity.OperationID]))}
		default:
			var request protocol.AssistantOperationRequest
			_ = json.Unmarshal(command.Body, &request)
			if receipt, ok := receipts[request.Identity.OperationID]; ok {
				return receipt
			}
			if command.Command == protocol.CommandAssistantOperationStatus {
				return protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: "not_found"}
			}
			if request.Identity.Tool != "files.upload" {
				folderWrites++
				return nil
			}
			var args protocol.AssistantUploadOperationArguments
			_ = json.Unmarshal(request.Arguments, &args)
			data := staged[request.Identity.OperationID]
			sum := sha256.Sum256(data)
			if args.SourceID != "tax-share" || path.Dir(strings.TrimPrefix(cleanPolicyPath(args.Path), "/")) != "2025 Taxes" || int64(len(data)) != args.SizeBytes || hex.EncodeToString(sum[:]) != args.ChecksumSHA256 {
				return nil
			}
			if _, exists := written[strings.TrimPrefix(cleanPolicyPath(args.Path), "/")]; exists {
				return nil
			}
			written[strings.TrimPrefix(cleanPolicyPath(args.Path), "/")] = append([]byte{}, data...)
			result, _ := json.Marshal(protocol.AssistantOperationResult{Verification: "observed", Item: &protocol.FileItem{SourceID: args.SourceID, Path: args.Path, Name: path.Base(args.Path), Size: args.SizeBytes, ModifiedAt: now}})
			receipt := protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: "confirmed", Result: result}
			receipts[request.Identity.OperationID] = receipt
			return receipt
		}
	})
	expected := map[string]string{}
	for i := 1; i <= 5; i++ {
		name, content := fmt.Sprintf("receipt-%d.txt", i), fmt.Sprintf("Synthetic receipt %d\n", i)
		expected["2025 Taxes/"+name] = content
		sum := sha256.Sum256([]byte(content))
		request := httptest.NewRequest("POST", "/staging", strings.NewReader(content))
		request.Header.Set("X-Hank-Filename", url.PathEscape(name))
		request.Header.Set("X-Hank-Attachment-ID", name)
		request.Header.Set("X-Hank-Content-Type", "text/plain")
		request.Header.Set("X-Hank-Size-Bytes", strconv.Itoa(len(content)))
		request.Header.Set("X-Hank-Checksum-SHA256", hex.EncodeToString(sum[:]))
		recorder := httptest.NewRecorder()
		s.handleAssistantStageUpload(recorder, request, home, authContext{User: user}, session.ID)
		if recorder.Code != 201 {
			t.Fatalf("stage %d: %d %s", i, recorder.Code, recorder.Body.String())
		}
	}
	submit := func(t *testing.T, prompt string) domain.AssistantTask {
		body, _ := json.Marshal(map[string]any{"content": prompt, "submission_id": newID("scenario")})
		recorder := httptest.NewRecorder()
		s.handleAssistantExecutionSubmit(recorder, httptest.NewRequest("POST", "/messages", strings.NewReader(string(body))), home, authContext{User: user}, session)
		if recorder.Code != 202 {
			t.Fatalf("admission: %d %s", recorder.Code, recorder.Body.String())
		}
		task, err := s.store.ClaimAssistantTask(ctx, "file-scenario-worker", 10*time.Minute)
		must(t, err)
		return task
	}
	for _, scenario := range []struct {
		prompt string
		upload bool
	}{{"upload these to 2025 taxes folder", true}, {"/files 2025 Taxes", false}} {
		t.Run(scenario.prompt, func(t *testing.T) {
			task := submit(t, scenario.prompt)
			t.Cleanup(func() {
				if task.State != "completed" && task.State != "failed" && task.State != "cancelled" {
					_, _ = s.store.CancelAssistantTask(ctx, home.ID, user.ID, task.ID)
				}
			})
			modelCalls, scriptedRound := 0, 0
			model := executionModelFunc(func(ctx context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
				modelCalls++
				if live {
					names := []string{}
					for _, tool := range input.Tools {
						names = append(names, tool.Name)
					}
					t.Logf("model turn=%d available=%v", modelCalls, names)
					turn, err := postAssistantExecutionTurn(ctx, "ollama", base, "", modelName, input)
					if len(turn.Calls) == 0 {
						t.Logf("synthetic model decision=%s text=%s", turn.FinishReason, turn.Text)
					}
					return turn, err
				}
				newCall := func(tool string, args any) assistant.Call {
					raw, _ := json.Marshal(args)
					return assistant.Call{ID: newID("scenario-call"), Tool: tool, Version: 1, Arguments: raw}
				}
				turn := assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls"}
				scriptedRound++
				switch scriptedRound {
				case 1:
					turn.Calls = []assistant.Call{newCall("files.sources", map[string]any{}), newCall("attachments.list", map[string]any{})}
				case 2:
					turn.Calls = []assistant.Call{newCall("files.search", map[string]any{"agent_id": agent, "source_id": "tax-share", "path": "/", "query": "2025 Taxes", "limit": 10})}
				default:
					if scriptedRound == 3 && scenario.upload {
						stages, err := s.store.ListAssistantStages(ctx, home.ID, user.ID, session.ID)
						if err != nil {
							return turn, err
						}
						for _, stage := range stages {
							turn.Calls = append(turn.Calls, newCall("files.upload", map[string]any{"agent_id": agent, "source_id": "tax-share", "path": "2025 Taxes/" + stage.Filename, "stage_id": stage.ID}))
						}
					} else {
						turn.FinishReason, turn.Text = "final", "Found 2025 Taxes."
					}
				}
				return turn, nil
			})
			started := time.Now()
			approvals := 0
			for steps := 0; steps < 100 && task.State == "running"; steps++ {
				var err error
				task, err = s.advanceAssistantExecution(ctx, task, model)
				must(t, err)
				if task.State == "waiting_approval" {
					var checkpoint assistantExecutionCheckpoint
					must(t, json.Unmarshal(task.Checkpoint, &checkpoint))
					call := checkpoint.Pending[checkpoint.Next]
					if !scenario.upload || call.Tool != "files.upload" {
						t.Fatalf("unrequested approval: %s", call.Tool)
					}
					// Approve only this synthetic fixture's exact destination and
					// immutable attachment. Never auto-approve live Home actions.
					var args map[string]any
					must(t, json.Unmarshal(call.Arguments, &args))
					stage, err := s.store.GetAssistantStage(ctx, home.ID, user.ID, session.ID, executionArg(args, "stage_id"))
					must(t, err)
					if executionArg(args, "agent_id") != agent || executionArg(args, "source_id") != "tax-share" || strings.TrimPrefix(cleanPolicyPath(executionArg(args, "path")), "/") != "2025 Taxes/"+stage.Filename {
						t.Fatalf("wrong synthetic upload destination proposed: agent=%s source=%s path=%s filename=%s", executionArg(args, "agent_id"), executionArg(args, "source_id"), executionArg(args, "path"), stage.Filename)
					}
					approval, err := s.store.GetAssistantTaskApproval(ctx, home.ID, user.ID, task.ID, call.ID)
					must(t, err)
					task, err = s.store.DecideAssistantTaskApproval(ctx, home.ID, user.ID, task.ID, approval.ID, approval.ActionDigest, true, task.Revision)
					must(t, err)
					task, err = s.store.ClaimAssistantTask(ctx, "file-scenario-worker", 10*time.Minute)
					must(t, err)
					approvals++
				}
			}
			var checkpoint assistantExecutionCheckpoint
			must(t, json.Unmarshal(task.Checkpoint, &checkpoint))
			steps, err := s.store.ListAssistantTaskStepSummaries(ctx, home.ID, user.ID, task.ID)
			must(t, err)
			for _, step := range steps {
				t.Logf("tool=%s state=%s", step.Tool, step.State)
				if step.State == "failed" {
					full, err := s.store.GetAssistantTaskStep(ctx, home.ID, user.ID, task.ID, step.CallID)
					must(t, err)
					var result assistant.Result
					must(t, json.Unmarshal(full.Result, &result))
					t.Logf("synthetic failure=%+v", result.Error)
					t.Logf("synthetic arguments=%s", full.Arguments)
				}
			}
			t.Logf("state=%s error=%s turns=%d calls=%d approvals=%d elapsed=%s", task.State, checkpoint.ErrorCode, modelCalls, task.Calls, approvals, time.Since(started))
			if task.State != "completed" {
				t.Fatalf("scenario did not complete: state=%s error=%s answer=%s", task.State, checkpoint.ErrorCode, checkpoint.FinalText)
			}
			lock.Lock()
			defer lock.Unlock()
			if folderWrites != 0 || searches == 0 {
				t.Fatalf("folder writes=%d searches=%d", folderWrites, searches)
			}
			if scenario.upload {
				for destination, content := range expected {
					if string(written[destination]) != content {
						t.Errorf("missing or corrupt upload: %s", destination)
					}
				}
			}
			if scenario.upload && approvals != 5 {
				t.Errorf("wanted five exact approvals, got %d", approvals)
			}
			if !scenario.upload && len(written) > 0 {
				answer := strings.ToLower(checkpoint.FinalText)
				if strings.Contains(answer, "contains no files") || strings.Contains(answer, "folder is empty") {
					t.Error("search falsely reported empty destination")
				}
			}
			if !scenario.upload && (approvals != 0 || !strings.Contains(strings.ToLower(checkpoint.FinalText), "2025 taxes")) {
				t.Fatal("search failed or attempted writes")
			}
		})
	}
}

func TestExecutionFiveFileUploadThenSearch(t *testing.T) { runExecutionFileScenario(t, false) }

func TestExecutionProviderLiveFiveFileUploadThenSearch(t *testing.T) {
	if os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_OLLAMA") == "" {
		t.Skip("opt-in synthetic file workflow with live model")
	}
	if os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_MODEL") == "" {
		t.Fatal("explicit model required")
	}
	runExecutionFileScenario(t, true)
}
