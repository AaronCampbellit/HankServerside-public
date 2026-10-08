package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func executionOperationID(taskID, callID string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + callID))
	return "aop_" + hex.EncodeToString(sum[:])
}

// Only explicitly implemented receipt-aware writes enter this dispatcher.
// All writes consume exact approvals before entering their owning adapter.
func (s *Server) advanceAssistantExecutionNoteWrite(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint, call assistant.Call, step domain.AssistantTaskStep, registry *assistant.Registry) (domain.AssistantTask, error) {
	operationID := executionOperationID(task.ID, call.ID)
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, operationID)
	if errors.Is(err, store.ErrNotFound) {
		approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
		if e != nil {
			return task, e
		}
		if approval.State != "approved" {
			return s.failAssistantExecution(ctx, task, checkpoint, "approval_expired")
		}
		if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS {
			return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
		}
		receipt = domain.AssistantOperationReceipt{OperationID: operationID, HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}
		task, receipt, _, err = s.store.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	}
	if err != nil {
		return task, err
	}
	if receipt.Outcome == "confirmed" || receipt.Outcome == "failed" {
		return task, nil
	}
	if receipt.Tool != call.Tool || receipt.ActionDigest != step.ActionDigest {
		return task, store.ErrConflict
	}
	reserveMS := int64(0)
	started := time.Now()
	settleBudget := func() {
		if reserveMS > 0 {
			task.ActiveMS = task.ActiveMS - reserveMS + min(reserveMS, time.Since(started).Milliseconds())
			reserveMS = 0
		}
	}
	fail := func(code string) (domain.AssistantTask, error) {
		settleBudget()
		result := assistant.Failure(call.ID, code)
		result.OperationID = &operationID
		receipt.Outcome = "failed"
		receipt.Result, _ = json.Marshal(result)
		return s.store.FinishAssistantOperation(ctx, task, receipt)
	}
	if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS || step.Attempts >= 3 {
		return fail("capability_unavailable")
	}
	reserveMS = min(int64(30000), task.MaxActiveMS-task.ActiveMS)
	task.ActiveMS += reserveMS
	task.Calls++
	task, step, err = s.store.StartAssistantTaskStep(ctx, task, call.ID)
	if err != nil {
		return task, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(reserveMS)*time.Millisecond)
	defer cancel()
	ctx = callCtx
	// Re-prepare with current permissions and revision before any effect. The
	// approval covers the entire proposal, not just a title or tool name.
	proposal := registry.Invoke(ctx, call)
	if proposal.Error != nil {
		return fail(proposal.Error.Code)
	}
	if proposal.Outcome != "not_started" || assistantActionDigest(task, call, &proposal) != step.ActionDigest {
		return fail("revision_conflict")
	}
	var args struct {
		Title    string `json:"title"`
		Text     string `json:"text"`
		NoteID   string `json:"note_id"`
		Revision string `json:"revision"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return fail("invalid_arguments")
	}
	now := time.Now().UTC()
	note := domain.UserNote{ID: operationID + "_note", NoteID: operationID + "_note", OwnerUserID: task.UserID, CreatedAt: now, PageType: protocol.NotePageTypeText}
	state := collabState{Title: collabScalar{Value: args.Title, Version: 1, UserID: task.UserID}, PageType: collabScalar{Value: protocol.NotePageTypeText, Version: 1, UserID: task.UserID}, Content: args.Text, CollabVersion: 1}
	if call.Tool == "notes.append" {
		note, err = s.store.GetUserNoteByID(ctx, args.NoteID)
		if err != nil {
			return fail("not_found")
		}
		if note.Revision != args.Revision || note.DeletedAt != nil {
			return fail("revision_conflict")
		}
		if normalizePageType(note.PageType) != protocol.NotePageTypeText {
			return fail("invalid_arguments")
		}
		state, err = decodeCollabState(note)
		if err != nil {
			return fail("internal_error")
		}
		state.Content, err = appendNoteContent(noteBodyText(note), protocol.NotesAppendRequest{Content: args.Text})
		if err != nil {
			return fail("invalid_arguments")
		}
		state.CollabVersion = note.CollabVersion + 1
	}
	baseVersion := note.CollabVersion
	note, opJSON, err := materializeNoteFromState(note, state, task.UserID, now)
	if err != nil {
		return fail("internal_error")
	}
	scope := "personal"
	if note.HomeID != "" {
		scope = "home"
	}
	result := executionResult(call, []executionItem{{Type: "note", ID: note.ID, Scope: scope, Title: note.Title, Text: executionText(note.BodyMarkdown), Revision: note.Revision, URI: "hank://notes/" + url.PathEscape(note.NoteID)}}, 1, false)
	result.OperationID = &operationID
	result.Verification = "observed"
	receipt.Outcome = "confirmed"
	receipt.Result, _ = json.Marshal(result)
	operation := domain.NoteOperation{NoteID: note.ID, OpID: operationID, ActorUserID: task.UserID, BaseVersion: baseVersion, AppliedVersion: note.CollabVersion, OpJSON: opJSON, CreatedAt: now}
	settleBudget()
	saved, err := s.store.CommitAssistantNoteOperation(ctx, task, receipt, note, args.Revision, operation)
	if errors.Is(err, store.ErrConflict) {
		return fail("revision_conflict")
	}
	if errors.Is(err, store.ErrNotFound) {
		return fail("permission_denied")
	}
	return saved, err
}
