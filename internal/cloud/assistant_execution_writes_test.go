package cloud

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
)

func prepareExecutionWrite(t *testing.T, s *Server, task domain.AssistantTask, call assistant.Call, approve ...bool) domain.AssistantTask {
	t.Helper()
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "prepare-writer", time.Minute)
	must(t, err)
	if call.Tool == "files.create_folder" || call.Tool == "files.upload" {
		var args map[string]any
		must(t, json.Unmarshal(call.Arguments, &args))
		profile, err := json.Marshal(map[string]any{"file_sources": []map[string]any{{"id": args["source_id"], "type": "local", "local_root_enabled": true}}})
		must(t, err)
		must(t, s.store.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{HomeID: task.HomeID, ServiceType: domain.ServiceTypeSMB, PublicConfigJSON: string(profile), UpdatedAt: time.Now(), UpdatedBy: task.UserID}))
		registry, err := s.newAssistantExecutionTools(task.HomeID, task.UserID, task.SessionID)
		must(t, err)
		request := "Upload the staged attachment"
		if call.Tool == "files.create_folder" {
			request = "Create a folder"
		}
		checkpoint := assistantExecutionCheckpoint{Version: 2, Messages: []assistant.Message{{Role: "user", Text: request}}}
		for _, name := range []string{"files.sources", "attachments.list"} {
			discovery := assistant.Call{ID: "discover-" + name, Tool: name, Version: 1, Arguments: json.RawMessage("{}")}
			result := registry.Invoke(ctx, discovery)
			if result.Error != nil {
				t.Fatalf("discover for write: %s", result.Error.Code)
			}
			checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{discovery}}, assistant.Message{Role: "tool", Result: &result})
		}
		if call.Tool == "files.upload" {
			raw, _ := json.Marshal(map[string]any{"agent_id": args["agent_id"], "source_id": args["source_id"], "path": path.Dir(executionArg(args, "path"))})
			discovery := assistant.Call{ID: "discover-parent", Tool: "files.stat", Version: 1, Arguments: raw}
			result := registry.Invoke(ctx, discovery)
			if result.Error != nil {
				t.Fatalf("discover upload parent: %s", result.Error.Code)
			}
			checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{discovery}}, assistant.Message{Role: "tool", Result: &result})
		}
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		must(t, err)
		task, err = s.store.SaveAssistantTask(ctx, task, "running", "")
		must(t, err)
	}
	model := executionModelFunc(func(context.Context, assistant.ModelRequest) (assistant.Turn, error) {
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{call}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	if task.State != "waiting_approval" {
		t.Fatal("write did not pause for exact approval")
	}
	if len(approve) > 0 && !approve[0] {
		return task
	}
	approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	must(t, err)
	task, err = s.store.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true, task.Revision)
	must(t, err)
	task, err = s.store.ClaimAssistantTask(ctx, "approved-writer", time.Minute)
	must(t, err)
	return task
}

func TestExecutionApprovedNoteCreateRecoversCommittedReceiptAndContinues(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	call := assistant.Call{ID: "create-note", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Exact title","text":"  Exact body\n- [x] Existing formatting\n"}`)}
	task = prepareExecutionWrite(t, s, task, call)
	task, err := s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
	must(t, err)
	if receipt.Outcome != "confirmed" {
		t.Fatal("note receipt not confirmed")
	}
	var result assistant.Result
	must(t, json.Unmarshal(receipt.Result, &result))
	must(t, assistantschema.Validate(result))
	if result.Verification != "observed" {
		t.Fatal("readback missing")
	}
	notes, err := s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if len(notes) != 1 || notes[0].BodyMarkdown != "  Exact body\n- [x] Existing formatting\n" || notes[0].CRDTStateJSON == "" || notes[0].CollabVersion != 1 {
		t.Fatal("literal content/collaboration state not preserved")
	}
	// Reload the persisted task as after losing the process immediately after
	// commit, before the tool observation was copied into its checkpoint.
	task, err = s.store.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, executionModelFunc(func(_ context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
		var observed *assistant.Result
		for i := len(input.Messages) - 1; i >= 0; i-- {
			if input.Messages[i].Role == "tool" {
				observed = input.Messages[i].Result
				break
			}
		}
		if observed == nil || observed.OperationID == nil || observed.Outcome != "confirmed" || observed.Verification != "observed" {
			t.Fatal("original request resumed without write result")
		}
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "final", Text: "Created the exact note."}, nil
	}))
	must(t, err)
	if task.State != "completed" {
		t.Fatal("request did not continue to completion")
	}
	notes, err = s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if len(notes) != 1 || notes[0].CollabVersion != 1 {
		t.Fatal("recovery duplicated effect")
	}
}

func TestExecutionApprovedNoteAppendLiteralAndStaleRevision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "literal", true: "stale"}[changed], func(t *testing.T) {
			s, task := executionTaskFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			before := "  Keep spaces\n- [x] Keep list\n"
			note := domain.UserNote{ID: "append-note", NoteID: "append-note", OwnerUserID: task.UserID, Title: "Original", Content: before, BodyMarkdown: before, BodyFormat: "markdown", PageType: protocol.NotePageTypeText, Revision: "original-revision", Checksum: "initial", CreatedAt: now, UpdatedAt: now, UpdatedBy: task.UserID}
			must(t, s.store.UpsertUserNote(ctx, note))
			call := assistant.Call{ID: "append", Tool: "notes.append", Version: 1, Arguments: json.RawMessage(`{"note_id":"append-note","revision":"original-revision","text":"  literal addition\nnext line"}`)}
			task = prepareExecutionWrite(t, s, task, call)
			if changed {
				note.Content = "Concurrent edit"
				note.BodyMarkdown = note.Content
				note.Revision = "new-revision"
				must(t, s.store.UpsertUserNote(ctx, note))
			}
			task, err := s.advanceAssistantExecution(ctx, task, nil)
			must(t, err)
			receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
			must(t, err)
			saved, err := s.store.GetUserNoteByID(ctx, note.ID)
			must(t, err)
			if changed {
				if receipt.Outcome != "failed" || saved.Content != "Concurrent edit" || !strings.Contains(string(receipt.Result), "revision_conflict") {
					t.Fatal("stale approval changed note")
				}
			} else if receipt.Outcome != "confirmed" || saved.Content != before+"\n  literal addition\nnext line" || saved.CollabVersion != 1 {
				t.Fatal("append rewrote existing content")
			}
		})
	}
}

func TestExecutionApprovalPreviewAndRejectionContinueWithoutWrite(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	call := assistant.Call{ID: "preview", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Reviewed title","text":"  Exact reviewed body\n"}`)}
	task = prepareExecutionWrite(t, s, task, call, false)
	snapshot, err := s.assistantExecutionSnapshot(ctx, task)
	must(t, err)
	must(t, assistantschema.Validate(snapshot))
	raw, _ := json.Marshal(snapshot)
	if !strings.Contains(string(raw), "Exact reviewed body") || strings.Contains(string(raw), "checkpoint") {
		t.Fatal("exact preview missing or checkpoint exposed")
	}
	approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	must(t, err)
	_, err = s.store.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, false, task.Revision)
	must(t, err)
	task, err = s.store.ClaimAssistantTask(ctx, "reject-worker", time.Minute)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, executionModelFunc(func(_ context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
		last := input.Messages[len(input.Messages)-1]
		if last.Result == nil || last.Result.Error == nil || last.Result.Error.Code != "approval_rejected" {
			t.Fatal("rejection not returned to model")
		}
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "final", Text: "I did not create the note."}, nil
	}))
	must(t, err)
	notes, err := s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if task.State != "completed" || len(notes) != 0 {
		t.Fatal("rejected action wrote or failed to continue")
	}
}

func TestExecutionStopAfterLocalIntentProvesNoNoteEffect(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	call := assistant.Call{ID: "stop-before-note", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Never created","text":"Stopped"}`)}
	task = prepareExecutionWrite(t, s, task, call)
	step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	must(t, err)
	approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	must(t, err)
	receipt := domain.AssistantOperationReceipt{OperationID: executionOperationID(task.ID, call.ID), HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}
	task, _, _, err = s.store.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	must(t, err)
	stopped, err := s.store.CancelAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	saved, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, receipt.OperationID)
	must(t, err)
	if saved.Outcome != "failed" || !strings.Contains(string(saved.Result), "cancelled") {
		t.Fatal("local cancellation left false uncertainty")
	}
	snapshot, err := s.assistantExecutionSnapshot(ctx, stopped)
	must(t, err)
	if snapshot["effects_uncertain"] != false {
		t.Fatal("refresh claimed uncertain local effect")
	}
	if _, err = s.advanceAssistantExecution(ctx, task, nil); err == nil {
		t.Fatal("stale worker survived stop")
	}
	notes, err := s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if len(notes) != 0 {
		t.Fatal("stopped intent created a note")
	}
}

func TestExecutionNoteApprovalRechecksRevokedPermission(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	call := assistant.Call{ID: "revoked-note", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Denied","text":"No access"}`)}
	task = prepareExecutionWrite(t, s, task, call)
	settings := defaultAssistantSettings(task.HomeID, task.UserID)
	settings.ProfileNotesEnabled = false
	settings.HomeNotesEnabled = false
	must(t, s.store.UpsertAssistantSettings(ctx, settings))
	task, err := s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
	must(t, err)
	if receipt.Outcome != "failed" || !strings.Contains(string(receipt.Result), "permission_denied") {
		t.Fatal("approval bypassed permission revocation")
	}
	notes, err := s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if len(notes) != 0 {
		t.Fatal("revoked write changed notes")
	}
}

func TestExecutionFinalCannotPromoteUnverifiedReceipt(t *testing.T) {
	id := "operation"
	for _, outcome := range []string{"accepted", "unknown"} {
		messages := []assistant.Message{{OriginTaskID: "current", Result: &assistant.Result{Outcome: outcome, OperationID: &id, Verification: "unavailable"}}}
		if text := executionVerifiedFinalText("current", messages, "Everything was changed successfully."); strings.Contains(text, "successfully") || !strings.Contains(text, "not verified") {
			t.Fatal("unverified receipt promoted to success")
		}
		if text := executionVerifiedFinalText("new-task", messages, "Read the requested note."); text != "Read the requested note." {
			t.Fatal("historical uncertainty changed a different task")
		}
	}
}

func TestExecutionOpaqueAppAnswerRemainsAttributed(t *testing.T) {
	id := "operation"
	data, _ := json.Marshal(executionData{Items: []executionItem{{Type: "app", Title: "Synthetic", Text: `{"text":"The app's answer."}`}}})
	text := executionVerifiedFinalText("task", []assistant.Message{{OriginTaskID: "task", Result: &assistant.Result{Outcome: "accepted", OperationID: &id, Verification: "unavailable", Data: data}}}, "It is verified.")
	if !strings.Contains(text, "The app's answer.") || !strings.Contains(text, "not independently verified") || strings.Contains(text, "It is verified.") {
		t.Fatal("app output lost or promoted to verified evidence")
	}
}
