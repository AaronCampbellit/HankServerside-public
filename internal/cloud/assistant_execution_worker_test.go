package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

type executionModelFunc func(context.Context, assistant.ModelRequest) (assistant.Turn, error)

func (model executionModelFunc) Next(ctx context.Context, request assistant.ModelRequest) (assistant.Turn, error) {
	return model(ctx, request)
}

func executionTaskFixture(t *testing.T) (*Server, domain.AssistantTask) {
	t.Helper()
	s, home, user, _ := executionFixture(t)
	now := time.Now().UTC()
	must(t, s.store.CreateAssistantSession(context.Background(), domain.AssistantSession{ID: "session_worker", HomeID: home.ID, UserID: user.ID, Title: "Worker", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}))
	task, _, err := s.store.CreateAssistantTask(context.Background(), domain.AssistantTask{ID: "task_worker", HomeID: home.ID, UserID: user.ID, SessionID: "session_worker", SubmissionKey: "submit_worker", RequestText: "Inspect my machines"})
	must(t, err)
	return s, task
}

func TestExecutionWorkerRecoversPendingModelCallAfterServerRestart(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "first-process", time.Second)
	must(t, err)
	modelCalls := 0
	model := executionModelFunc(func(_ context.Context, request assistant.ModelRequest) (assistant.Turn, error) {
		modelCalls++
		if modelCalls == 1 {
			return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{{ID: "inspect", Tool: "machines.list", Version: 1, Arguments: json.RawMessage(`{}`)}}}, nil
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Result == nil || last.Result.CallID != "inspect" || last.Result.Outcome != "confirmed" {
			t.Fatal("recovered observation was lost")
		}
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "final", Text: "No enrolled machines.", Calls: []assistant.Call{}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	if modelCalls != 1 || task.State != "running" {
		t.Fatal("model decision was not checkpointed")
	}
	// A new server object has no in-memory task state. Wait only for the short
	// lease; no production database or process is stopped by this test.
	time.Sleep(1100 * time.Millisecond)
	restarted := NewServer("127.0.0.1:0", s.store, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer restarted.Shutdown(ctx)
	recovered, err := restarted.store.ClaimAssistantTask(ctx, "second-process", time.Minute)
	must(t, err)
	if recovered.Fence <= task.Fence {
		t.Fatal("restart did not fence old worker")
	}
	recovered, err = restarted.advanceAssistantExecution(ctx, recovered, model)
	must(t, err)
	if modelCalls != 1 || recovered.Calls != 1 {
		t.Fatal("restart repeated model instead of pending tool")
	}
	recovered, err = restarted.advanceAssistantExecution(ctx, recovered, model)
	must(t, err)
	if recovered.State != "completed" || modelCalls != 2 {
		t.Fatal("recovered task did not finish")
	}
	var checkpoint assistantExecutionCheckpoint
	must(t, json.Unmarshal(recovered.Checkpoint, &checkpoint))
	if checkpoint.FinalText != "No enrolled machines." {
		t.Fatal("final result not persisted")
	}
}

func TestExecutionWorkerPersistsApprovalWithoutWriting(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "worker", time.Minute)
	must(t, err)
	model := executionModelFunc(func(context.Context, assistant.ModelRequest) (assistant.Turn, error) {
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{{ID: "create", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Synthetic","text":"Pending only"}`)}}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	if task.State != "waiting_approval" || task.LeaseUntil != nil {
		t.Fatal("approval wait did not release lease")
	}
	approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, "create")
	must(t, err)
	step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, "create")
	must(t, err)
	if approval.ActionDigest != step.ActionDigest || len(step.Result) == 0 {
		t.Fatal("exact proposal not durable")
	}
	notes, err := s.store.ListProfileNotes(ctx, task.UserID, false)
	must(t, err)
	if len(notes) != 0 {
		t.Fatal("preparation wrote a note")
	}
	if _, err := s.store.ClaimAssistantTask(ctx, "second-worker", time.Minute); err == nil {
		t.Fatal("worker claimed an approval wait")
	}
}

func TestExecutionFollowupReplacesPendingProposal(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "worker", time.Minute)
	must(t, err)
	model := executionModelFunc(func(context.Context, assistant.ModelRequest) (assistant.Turn, error) {
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{{ID: "proposal", Tool: "notes.create", Version: 1, Arguments: json.RawMessage(`{"title":"Old title","text":"Pending"}`)}}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, "proposal")
	must(t, err)
	_, err = s.store.SubmitAssistantTaskInput(ctx, task.HomeID, task.UserID, task.ID, "edit-1", "Change the title to New title")
	must(t, err)
	if _, err := s.store.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true); err == nil {
		t.Fatal("edited proposal retained approvable action")
	}
	task, err = s.store.ClaimAssistantTask(ctx, "worker-two", time.Minute)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	var checkpoint assistantExecutionCheckpoint
	must(t, json.Unmarshal(task.Checkpoint, &checkpoint))
	if len(checkpoint.Pending) != 0 || checkpoint.Messages[len(checkpoint.Messages)-1].Text != "Change the title to New title" {
		t.Fatal("followup did not preserve updated conversation")
	}
	cancelled := checkpoint.Messages[len(checkpoint.Messages)-2].Result
	if cancelled == nil || cancelled.Error == nil || cancelled.Error.Code != "cancelled" {
		t.Fatal("discarded proposal was not resolved in tool protocol")
	}
}

func TestExecutionActionDigestPreservesExactNumericArguments(t *testing.T) {
	task := domain.AssistantTask{ID: "task", HomeID: "home", UserID: "owner"}
	first := assistant.Call{ID: "call", Tool: "apps.invoke", Version: 1, Arguments: json.RawMessage(`{"value":9007199254740992}`)}
	second := first
	second.Arguments = json.RawMessage(`{"value":9007199254740993}`)
	if assistantActionDigest(task, first, nil) == assistantActionDigest(task, second, nil) {
		t.Fatal("approval digest rounded distinct numeric actions")
	}
	spaced := first
	spaced.Arguments = json.RawMessage(`{ "value": 9007199254740992 }`)
	if assistantActionDigest(task, first, nil) != assistantActionDigest(task, spaced, nil) {
		t.Fatal("whitespace changed exact action identity")
	}
}

func TestExecutionDurableStopCancelsProviderIO(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := s.store.ClaimAssistantTask(ctx, "cancel-worker", time.Minute)
	must(t, err)
	started := make(chan struct{})
	observed := make(chan struct{})
	model := executionModelFunc(func(callCtx context.Context, _ assistant.ModelRequest) (assistant.Turn, error) {
		close(started)
		<-callCtx.Done()
		close(observed)
		return assistant.Turn{}, callCtx.Err()
	})
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = s.advanceAssistantExecutionWithCancellation(ctx, task, model) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	_, err = s.store.CancelAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("stop did not cancel provider")
	}
	<-finished
	saved, err := s.store.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	if saved.State != "cancelled" {
		t.Fatal("provider completion overwrote stop")
	}
}

func TestExecutionWorkerRepairsCompletionProtocolWithBoundedDurableTurns(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "format-worker", time.Minute)
	must(t, err)
	malformed := executionModelFunc(func(context.Context, assistant.ModelRequest) (assistant.Turn, error) {
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "error", Text: "Which note?"}, nil
	})
	for attempt := 0; attempt < 3; attempt++ {
		task, err = s.advanceAssistantExecution(ctx, task, malformed)
		must(t, err)
		if attempt < 2 && task.State != "running" {
			t.Fatal("format repair stopped too early")
		}
	}
	if task.State != "failed" || task.Turns != 3 {
		t.Fatal("malformed completion not bounded")
	}
	messages, err := s.store.ListAssistantMessages(ctx, task.SessionID)
	must(t, err)
	for _, message := range messages {
		if message.Role == "assistant" {
			t.Fatal("unclassified prose published as completion")
		}
	}
}

func TestExecutionWorkerExplicitSlashConstrainsModelAndDispatch(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	task, err := s.store.ClaimAssistantTask(ctx, "slash-worker", time.Minute)
	must(t, err)
	checkpoint := assistantExecutionCheckpoint{Version: 2, AllowedTools: []string{"notes.search", "notes.get"}, Messages: []assistant.Message{{Role: "user", Text: "/notes find work"}}}
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	must(t, err)
	task, err = s.store.SaveAssistantTask(ctx, task, "running", "")
	must(t, err)
	model := executionModelFunc(func(_ context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
		for _, tool := range input.Tools {
			if tool.Name != "notes.search" && tool.Name != "notes.get" {
				t.Fatal("slash exposed unrelated capability")
			}
		}
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{{ID: "outside", Tool: "machines.list", Version: 1, Arguments: json.RawMessage(`{}`)}}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	if task.State != "failed" || task.Calls != 0 {
		t.Fatal("model escaped explicit slash constraint")
	}
}
