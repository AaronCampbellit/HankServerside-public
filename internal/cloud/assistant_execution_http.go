package cloud

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

// Task controls are independently authenticated by the existing Home router.
// Admission is separately gated by execution configuration. Never expose
// raw checkpoints: they contain model context and private retrieved evidence.
func (s *Server) handleAssistantExecutionTask(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, parts []string) {
	task, err := s.store.GetAssistantTask(r.Context(), home.ID, auth.User.ID, parts[0])
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	if task.SessionID == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	uncertain, err := s.store.AssistantTaskHasUncertainOperations(r.Context(), home.ID, auth.User.ID, task.ID)
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	snapshot := func(task domain.AssistantTask) map[string]any {
		result := assistantTaskSnapshot(task)
		result["effects_uncertain"] = uncertain
		return result
	}
	switch {
	case len(parts) == 2 && parts[1] == "source" && r.Method == http.MethodGet:
		s.handleAssistantExecutionSource(w, r, task)
	case len(parts) == 1 && r.Method == http.MethodGet:
		result, err := s.assistantExecutionSnapshot(r.Context(), task)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet:
		after, limit := int64(0), 100
		if value := r.URL.Query().Get("after_sequence"); value != "" {
			after, err = strconv.ParseInt(value, 10, 64)
			if err != nil || after < 0 {
				http.Error(w, "invalid cursor", 400)
				return
			}
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 100 {
				http.Error(w, "invalid limit", 400)
				return
			}
		}
		events, err := s.store.ListAssistantTaskEvents(r.Context(), home.ID, auth.User.ID, task.ID, after)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		more := len(events) > limit
		if more {
			events = events[:limit]
		}
		next := after
		if len(events) > 0 {
			next = events[len(events)-1].Sequence
		}
		wireEvents := make([]map[string]any, 0, len(events))
		for _, event := range events {
			wireEvents = append(wireEvents, assistantTaskEventToAPI(event))
		}
		writeJSON(w, 200, map[string]any{"events": wireEvents, "next_sequence": next, "has_more": more, "snapshot_required": false})
	case len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost:
		task, err = s.store.CancelAssistantTask(r.Context(), home.ID, auth.User.ID, task.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		// Re-read after the stop transaction: a dispatch could have acquired
		// the task lock between our initial snapshot and cancellation.
		uncertain, err = s.store.AssistantTaskHasUncertainOperations(r.Context(), home.ID, auth.User.ID, task.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		writeJSON(w, 200, snapshot(task))
	case len(parts) == 2 && parts[1] == "followups" && r.Method == http.MethodPost:
		var input struct {
			SubmissionID     string `json:"submission_id"`
			ExpectedRevision int64  `json:"expected_revision"`
			Text             string `json:"text"`
		}
		if !decodeAssistantTaskInput(w, r, &input) {
			return
		}
		if len(input.SubmissionID) < 1 || len(input.SubmissionID) > 128 || len(input.Text) > 16384 || strings.TrimSpace(input.Text) == "" || input.ExpectedRevision < 1 {
			http.Error(w, "invalid followup", 400)
			return
		}
		// Reject unsupported directives before cancelling a proposal or
		// changing task state. The user can correct one on the same task.
		if directive, explicit := s.executionSlash(r.Context(), home.ID, auth.User.ID, input.Text); explicit && (directive.Error != nil || directive.Call != nil) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_followup_command", "message": "Use /files to search files, or send the installed app command as a new task."})
			return
		}
		task, err = s.store.SubmitAssistantTaskInput(r.Context(), home.ID, auth.User.ID, task.ID, input.SubmissionID, input.Text, input.ExpectedRevision)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		writeJSON(w, 202, snapshot(task))
	case len(parts) == 3 && parts[1] == "approvals" && r.Method == http.MethodPost:
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			ActionDigest     string `json:"action_digest"`
			Approved         *bool  `json:"approved"`
		}
		if !decodeAssistantTaskInput(w, r, &input) {
			return
		}
		if input.ExpectedRevision < 1 || len(input.ActionDigest) != 64 || input.Approved == nil {
			http.Error(w, "invalid decision", 400)
			return
		}
		// The immutable approval ID is the idempotency identity. Retries of the
		// same decision succeed; opposite decisions and changed digests conflict.
		task, err = s.store.DecideAssistantTaskApproval(r.Context(), home.ID, auth.User.ID, task.ID, parts[2], input.ActionDigest, *input.Approved, input.ExpectedRevision)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		writeJSON(w, 200, snapshot(task))
	default:
		http.Error(w, "method or route unavailable", http.StatusMethodNotAllowed)
	}
}

func decodeAssistantTaskInput(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}

func assistantTaskHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrAssistantLeaseLost):
		http.Error(w, "task_state_conflict", 409)
	default:
		http.Error(w, "task_unavailable", 500)
	}
}

func assistantTaskSnapshot(task domain.AssistantTask) map[string]any {
	var checkpoint struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(task.Checkpoint, &checkpoint)
	switch checkpoint.ErrorCode {
	case "", "budget_exhausted", "invalid_checkpoint", "duplicate_call", "permission_denied", "invalid_tool_selection", "capability_unavailable", "model_unavailable", "outcome_unknown":
	default:
		checkpoint.ErrorCode = "execution_failed"
	}
	return map[string]any{"error_code": checkpoint.ErrorCode, "schema_version": 2, "kind": "task", "engine_version": 2, "task_id": task.ID, "session_id": task.SessionID, "state": task.State, "revision": task.Revision, "last_event_sequence": task.EventSequence, "cancel_requested": task.CancelRequested, "budget": map[string]any{"max_turns": task.MaxTurns, "max_calls": task.MaxCalls, "max_tokens": task.MaxTokens, "max_active_ms": task.MaxActiveMS, "turns_used": task.Turns, "calls_used": task.Calls, "tokens_used": task.Tokens, "active_ms_used": task.ActiveMS}, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt}
}
func assistantTaskEventToAPI(event domain.AssistantTaskEvent) map[string]any {
	eventType := event.Type
	switch eventType {
	case "running", "preparing":
		eventType = "planning"
	case "checking":
		eventType = "verifying"
	case "followup":
		eventType = "queued"
	}
	var callID any
	if event.CallID != "" {
		callID = event.CallID
	}
	return map[string]any{"schema_version": 2, "kind": "event", "task_id": event.TaskID, "sequence": event.Sequence, "type": eventType, "call_id": callID, "created_at": event.CreatedAt.UTC().Format(time.RFC3339Nano)}
}
