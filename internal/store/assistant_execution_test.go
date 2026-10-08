package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func assistantTaskFixture(t *testing.T) (*Store, domain.AssistantTask) {
	t.Helper()
	db := openTestStore(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_exec", Email: "exec@example.invalid", PasswordHash: "disabled", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_exec", UserID: user.ID, Name: "Execution", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	session := domain.AssistantSession{ID: "session_exec", HomeID: home.ID, UserID: user.ID, Title: "Synthetic", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateAssistantSession(ctx, session))
	task, created, err := db.CreateAssistantTask(ctx, domain.AssistantTask{ID: "task_exec", HomeID: home.ID, UserID: user.ID, SessionID: session.ID, SubmissionKey: "submission-1", RequestText: "Synthetic task"})
	mustStore(t, err)
	if !created {
		t.Fatal("task not created")
	}
	return db, task
}

func TestAssistantTaskDeduplicationScopeAndFencing(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			copy := task
			copy.ID = "different-id"
			got, created, err := db.CreateAssistantTask(ctx, copy)
			if err != nil || created || got.ID != task.ID {
				t.Errorf("duplicate submission: %v %v", created, err)
			}
		})
	}
	wg.Wait()
	changed := task
	changed.RequestText = "Different request"
	if _, _, err := db.CreateAssistantTask(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("submission key reused with different request")
	}
	if _, err := db.GetAssistantTask(ctx, task.HomeID, "foreign", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign task visible")
	}
	first, err := db.ClaimAssistantTask(ctx, "worker-one", time.Minute)
	mustStore(t, err)
	if _, err := db.ClaimAssistantTask(ctx, "worker-two", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal("live task claimed twice")
	}
	_, err = db.exec(ctx, `UPDATE assistant_tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=?`, task.ID)
	mustStore(t, err)
	second, err := db.ClaimAssistantTask(ctx, "worker-two", time.Minute)
	mustStore(t, err)
	if second.Fence <= first.Fence {
		t.Fatal("reclaimed task did not increment fence")
	}
	first.State = "completed"
	if _, err := db.SaveAssistantTask(ctx, first, "completed", ""); !errors.Is(err, ErrAssistantLeaseLost) {
		t.Fatal("stale worker committed")
	}
	second.State = "completed"
	final, err := db.SaveAssistantTask(ctx, second, "completed", "")
	mustStore(t, err)
	if final.LeaseOwner != "" || final.LeaseUntil != nil {
		t.Fatal("terminal task retained lease")
	}
	events, err := db.ListAssistantTaskEvents(ctx, task.HomeID, task.UserID, task.ID, 0)
	mustStore(t, err)
	if len(events) != 4 || events[0].Sequence != 1 || events[3].Sequence != 4 {
		t.Fatal("events not ordered atomically")
	}
}

func TestAssistantApprovalIsExactAtomicAndRevocable(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "worker", time.Minute)
	mustStore(t, err)
	step := domain.AssistantTaskStep{TaskID: task.ID, CallID: "call-1", Sequence: 1, Tool: "notes.create", ToolVersion: 1, Arguments: json.RawMessage(`{"title":"Synthetic"}`), ActionDigest: strings.Repeat("a", 64), State: "pending"}
	task, err = db.PlanAssistantTaskStep(ctx, task, step)
	mustStore(t, err)
	task, step, err = db.StartAssistantTaskStep(ctx, task, step.CallID)
	mustStore(t, err)
	step.State = "waiting_approval"
	step.Result = json.RawMessage(`{"outcome":"not_started"}`)
	approval := domain.AssistantTaskApproval{ID: "approval-1", TaskID: task.ID, CallID: step.CallID, ActionDigest: step.ActionDigest, ExpiresAt: time.Now().Add(time.Hour)}
	task, err = db.CompleteAssistantTaskStep(ctx, task, step, &approval)
	mustStore(t, err)
	if task.State != "waiting_approval" {
		t.Fatal("proposal was not persisted as waiting")
	}
	if _, err := db.DecideAssistantTaskApproval(ctx, task.HomeID, "foreign", task.ID, approval.ID, approval.ActionDigest, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign user approved")
	}
	if _, err := db.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, strings.Repeat("b", 64), true); !errors.Is(err, ErrConflict) {
		t.Fatal("changed action approved")
	}
	task, err = db.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true)
	mustStore(t, err)
	same, err := db.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true)
	mustStore(t, err)
	if same.Revision != task.Revision || task.State != "queued" {
		t.Fatal("duplicate approval was not idempotent")
	}
	task, err = db.ClaimAssistantTask(ctx, "worker-two", time.Minute)
	mustStore(t, err)
	cancelled, err := db.CancelAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if cancelled.State != "cancelled" {
		t.Fatal("stop not durable")
	}
	if _, err := db.SaveAssistantTask(ctx, task, "running", ""); !errors.Is(err, ErrAssistantLeaseLost) {
		t.Fatal("stopped worker committed")
	}
	persisted, err := db.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, step.CallID)
	mustStore(t, err)
	if persisted.State != "cancelled" {
		t.Fatal("stop retained approval")
	}
}

func TestAssistantOperationReceiptPreventsReplayAndCommitsLocalEffectAtomically(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "worker", time.Minute)
	mustStore(t, err)
	step := domain.AssistantTaskStep{TaskID: task.ID, CallID: "write", Sequence: 1, Tool: "notes.create", ToolVersion: 1, Arguments: json.RawMessage(`{}`), ActionDigest: strings.Repeat("c", 64), State: "pending"}
	task, err = db.PlanAssistantTaskStep(ctx, task, step)
	mustStore(t, err)
	task, step, err = db.StartAssistantTaskStep(ctx, task, step.CallID)
	mustStore(t, err)
	step.State = "waiting_approval"
	step.Result = json.RawMessage(`{"outcome":"not_started"}`)
	approval := domain.AssistantTaskApproval{ID: "approve-write", TaskID: task.ID, CallID: step.CallID, ActionDigest: step.ActionDigest, ExpiresAt: time.Now().Add(time.Hour)}
	task, err = db.CompleteAssistantTaskStep(ctx, task, step, &approval)
	mustStore(t, err)
	_, err = db.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true)
	mustStore(t, err)
	task, err = db.ClaimAssistantTask(ctx, "worker", time.Minute)
	mustStore(t, err)
	receipt := domain.AssistantOperationReceipt{OperationID: "op-write", HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: step.CallID, Tool: step.Tool, ActionDigest: step.ActionDigest}
	task, receipt, fresh, err := db.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	mustStore(t, err)
	if !fresh {
		t.Fatal("first operation was not dispatchable")
	}
	task, _, fresh, err = db.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	mustStore(t, err)
	if fresh {
		t.Fatal("recovery allowed duplicate dispatch")
	}
	receipt.Outcome = "confirmed"
	receipt.Result = json.RawMessage(`{"outcome":"confirmed"}`)
	crash := errors.New("injected before commit")
	if _, err := db.commitAssistantLocalOperation(ctx, task, receipt, func(tx *dbTx) error {
		_, err := tx.ExecContext(ctx, `UPDATE assistant_sessions SET title='changed' WHERE id=?`, task.SessionID)
		if err != nil {
			return err
		}
		return crash
	}); !errors.Is(err, crash) {
		t.Fatal("injected transaction failure not returned")
	}
	session, err := db.GetAssistantSession(ctx, task.SessionID)
	mustStore(t, err)
	if session.Title != "Synthetic" {
		t.Fatal("failed transaction leaked local effect")
	}
	pending, err := db.GetAssistantOperation(ctx, task.HomeID, task.UserID, receipt.OperationID)
	mustStore(t, err)
	if pending.Outcome != "accepted" {
		t.Fatal("failed transaction committed receipt")
	}
	effects := 0
	apply := func(tx *dbTx) error {
		effects++
		_, err := tx.ExecContext(ctx, `UPDATE assistant_sessions SET title='changed' WHERE id=?`, task.SessionID)
		return err
	}
	task, err = db.commitAssistantLocalOperation(ctx, task, receipt, apply)
	mustStore(t, err)
	task, err = db.commitAssistantLocalOperation(ctx, task, receipt, apply)
	mustStore(t, err)
	if effects != 1 {
		t.Fatal("completed local operation replayed")
	}
	confirmed, err := db.GetAssistantOperation(ctx, task.HomeID, task.UserID, receipt.OperationID)
	mustStore(t, err)
	if confirmed.Outcome != "confirmed" {
		t.Fatal("completion receipt missing")
	}
}

func TestAssistantFollowupFencesOldWorkerAndSurvivesSessionDeletion(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "old-worker", time.Minute)
	mustStore(t, err)
	updated, err := db.SubmitAssistantTaskInput(ctx, task.HomeID, task.UserID, task.ID, "followup-1", "Use the second folder")
	mustStore(t, err)
	duplicate, err := db.SubmitAssistantTaskInput(ctx, task.HomeID, task.UserID, task.ID, "followup-1", "Use the second folder")
	mustStore(t, err)
	if duplicate.Revision != updated.Revision {
		t.Fatal("followup duplicated")
	}
	if _, err := db.SaveAssistantTask(ctx, task, "running", ""); !errors.Is(err, ErrAssistantLeaseLost) {
		t.Fatal("followup left old worker active")
	}
	recovered, err := db.ClaimAssistantTask(ctx, "new-worker", time.Minute)
	mustStore(t, err)
	inputs, err := db.PendingAssistantTaskInputs(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if len(inputs) != 1 || inputs[0].Text != "Use the second folder" {
		t.Fatal("followup lost")
	}
	recovered.Checkpoint = json.RawMessage(`{"version":2,"followup":"Use the second folder"}`)
	recovered, err = db.ApplyAssistantTaskInputs(ctx, recovered, inputs)
	mustStore(t, err)
	inputs, err = db.PendingAssistantTaskInputs(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if len(inputs) != 0 {
		t.Fatal("followup was not consumed with checkpoint")
	}
	mustStore(t, db.DeleteAssistantSession(ctx, task.SessionID))
	deleted, err := db.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if deleted.State != "cancelled" || deleted.SessionID != "" || deleted.RequestText != "" || string(deleted.Checkpoint) != "{}" {
		t.Fatal("session deletion retained executable private task state")
	}
	if _, err := db.SaveAssistantTask(ctx, recovered, "running", ""); !errors.Is(err, ErrAssistantLeaseLost) {
		t.Fatal("deleted conversation worker remained active")
	}
}

func TestAssistantApprovalExpiryRequiresFreshProposal(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "worker", time.Minute)
	mustStore(t, err)
	step := domain.AssistantTaskStep{TaskID: task.ID, CallID: "expire", Sequence: 1, Tool: "notes.create", ToolVersion: 1, Arguments: json.RawMessage(`{}`), ActionDigest: strings.Repeat("d", 64), State: "pending"}
	task, err = db.PlanAssistantTaskStep(ctx, task, step)
	mustStore(t, err)
	task, step, err = db.StartAssistantTaskStep(ctx, task, step.CallID)
	mustStore(t, err)
	step.State = "waiting_approval"
	step.Result = json.RawMessage(`{}`)
	approval := domain.AssistantTaskApproval{ID: "expire-approval", TaskID: task.ID, CallID: step.CallID, ActionDigest: step.ActionDigest, ExpiresAt: time.Now().Add(time.Hour)}
	task, err = db.CompleteAssistantTaskStep(ctx, task, step, &approval)
	mustStore(t, err)
	_, err = db.exec(ctx, `UPDATE assistant_task_approvals SET expires_at=clock_timestamp()-interval '1 second' WHERE id=?`, approval.ID)
	mustStore(t, err)
	if _, err := db.DecideAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, approval.ID, approval.ActionDigest, true); err == nil {
		t.Fatal("expired proposal approved")
	}
	count, err := db.ExpireAssistantTaskApprovals(ctx)
	mustStore(t, err)
	if count != 1 {
		t.Fatal("expired proposal not transitioned")
	}
	current, err := db.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if current.State != "waiting_input" {
		t.Fatal("expiration lost resumable task")
	}
	count, err = db.ExpireAssistantTaskApprovals(ctx)
	mustStore(t, err)
	if count != 0 {
		t.Fatal("expiry repeated transition")
	}
}

func TestAssistantTaskSurvivesPoolReopen(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "first", time.Minute)
	mustStore(t, err)
	task.Checkpoint = json.RawMessage(`{"version":2,"selected":{"type":"folder","id":"second","source_id":"local","agent_id":"agent"}}`)
	task.State = "waiting_input"
	task.Turns = 2
	task.Calls = 3
	task.Tokens = 99
	task, err = db.SaveAssistantTask(ctx, task, "waiting_input", "")
	mustStore(t, err)
	url := db.databaseURL
	mustStore(t, db.Close())
	reopened, err := Open(ctx, url)
	mustStore(t, err)
	defer reopened.Close()
	loaded, err := reopened.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if loaded.State != "waiting_input" || loaded.Turns != 2 || loaded.Calls != 3 || loaded.Tokens != 99 || !assistantReceiptResultEqual(loaded.Checkpoint, task.Checkpoint) {
		t.Fatal("new store lost checkpoint or consumed budgets")
	}
}

func TestAssistantTaskConcurrentClaimHasSingleOwner(t *testing.T) {
	db, _ := assistantTaskFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	for _, worker := range []string{"one", "two", "three", "four"} {
		wg.Go(func() {
			_, err := db.ClaimAssistantTask(ctx, worker, time.Minute)
			if err == nil {
				mu.Lock()
				claims++
				mu.Unlock()
			} else if !errors.Is(err, ErrNotFound) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("task had %d simultaneous owners", claims)
	}
}

func TestAssistantStopPreservesUncertainDispatchedOutcome(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	task, err := db.ClaimAssistantTask(ctx, "dispatch-worker", time.Minute)
	mustStore(t, err)
	// An accepted receipt means the destination may have acted, even when no
	// result reached the server. Cancellation cannot turn that into no effect.
	_, err = db.exec(ctx, `INSERT INTO assistant_operation_receipts(operation_id,home_id,user_id,task_id,call_id,tool,action_digest,outcome) VALUES(?,?,?,?,?,?,?,'accepted')`, "stop-operation", task.HomeID, task.UserID, task.ID, "remote-call", "files.create_folder", strings.Repeat("d", 64))
	mustStore(t, err)
	stopped, err := db.CancelAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	receipt, err := db.GetAssistantOperation(ctx, task.HomeID, task.UserID, "stop-operation")
	mustStore(t, err)
	if stopped.State != "cancelled" || receipt.Outcome != "unknown" {
		t.Fatal("stop falsely resolved remote outcome")
	}
	if _, err = db.SaveAssistantTask(ctx, task, "completed", ""); !errors.Is(err, ErrAssistantLeaseLost) {
		t.Fatal("stopped worker overwrote task")
	}
	uncertain, err := db.AssistantTaskHasUncertainOperations(ctx, task.HomeID, task.UserID, task.ID)
	mustStore(t, err)
	if !uncertain {
		t.Fatal("uncertain effect not visible")
	}
	mustStore(t, db.DeleteAssistantSession(ctx, task.SessionID))
	retained, err := db.GetAssistantOperation(ctx, task.HomeID, task.UserID, "stop-operation")
	mustStore(t, err)
	if retained.Outcome != "unknown" || retained.ActionDigest != receipt.ActionDigest {
		t.Fatal("deletion discarded recovery identity")
	}
}

func TestAssistantReceiptEqualityDoesNotRoundDistinctNumbers(t *testing.T) {
	if assistantReceiptResultEqual(json.RawMessage(`{"id":9007199254740992}`), json.RawMessage(`{"id":9007199254740993}`)) {
		t.Fatal("distinct receipts treated as identical")
	}
	if !assistantReceiptResultEqual(json.RawMessage(`{"ok":true,"id":1}`), json.RawMessage(`{ "id":1,"ok":true }`)) {
		t.Fatal("equivalent JSON receipt rejected")
	}
}

func TestAssistantStageExpiryAndImmutableBinding(t *testing.T) {
	db, task := assistantTaskFixture(t)
	ctx := context.Background()
	stage := domain.AssistantStage{ID: "stage-expiry", HomeID: task.HomeID, UserID: task.UserID, SessionID: task.SessionID, ClientAttachmentID: "client-expiry", StorageKey: strings.Repeat("e", 64), Filename: "synthetic.txt", ContentType: "text/plain", SizeBytes: 1, ChecksumSHA256: strings.Repeat("f", 64)}
	saved, err := db.BindAssistantStage(ctx, stage)
	mustStore(t, err)
	replay, err := db.BindAssistantStage(ctx, stage)
	mustStore(t, err)
	if replay.ID != saved.ID {
		t.Fatal("binding replay duplicated")
	}
	if _, err = db.GetAssistantStage(ctx, task.HomeID, "other", task.SessionID, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign owner retrieved binding")
	}
	changed := stage
	changed.SizeBytes = 2
	if _, err = db.BindAssistantStage(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed binding accepted")
	}
	_, err = db.exec(ctx, `UPDATE assistant_staged_attachments SET created_at=clock_timestamp()-interval '2 days',expires_at=clock_timestamp()-interval '1 day' WHERE id=?`, saved.ID)
	mustStore(t, err)
	if _, err = db.GetAssistantStage(ctx, task.HomeID, task.UserID, task.SessionID, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired binding readable")
	}
	if _, err = db.BindAssistantStage(ctx, stage); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired binding silently revived")
	}
}
