package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

// Checkpoints keep protocol observations and pending calls independently of
// conversation text. Only the fenced worker may advance this state.
type assistantExecutionCheckpoint struct {
	ExplicitApp       *assistant.Call            `json:"explicit_app,omitempty"`
	FormatRepairs     int                        `json:"format_repairs,omitempty"`
	CompletionRepairs int                        `json:"completion_repairs,omitempty"`
	AllowedTools      []string                   `json:"allowed_tools,omitempty"`
	Sources           []assistantExecutionSource `json:"sources,omitempty"`
	ContextNote       string                     `json:"context_note,omitempty"`
	Version           int                        `json:"version"`
	Messages          []assistant.Message        `json:"messages"`
	Pending           []assistant.Call           `json:"pending"`
	Next              int                        `json:"next"`
	Seen              []string                   `json:"seen"`
	FinalText         string                     `json:"final_text,omitempty"`
	ErrorCode         string                     `json:"error_code,omitempty"`
}

func assistantActionDigest(task domain.AssistantTask, call assistant.Call, proposal *assistant.Result) string {
	// json.Marshal sorts map keys. Decode arguments first to avoid whitespace
	// changing the digest while preserving the exact typed values.
	var args any
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.UseNumber()
	_ = decoder.Decode(&args)
	raw, _ := json.Marshal(struct {
		Home, User, Task, Tool string
		Version                int
		Arguments              any
		Proposal               *assistant.Result
	}{task.HomeID, task.UserID, task.ID, call.Tool, call.Version, args, proposal})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func encodeExecutionCheckpoint(task domain.AssistantTask, checkpoint assistantExecutionCheckpoint) (domain.AssistantTask, error) {
	raw, err := json.Marshal(checkpoint)
	if err != nil || len(raw) > 1<<20 {
		return task, errors.New("assistant checkpoint exceeds budget")
	}
	task.Checkpoint = raw
	return task, nil
}

// advanceAssistantExecution performs one recoverable unit for the runtime
// v2 worker; v1 HTTP handlers do not invoke it. A caller claims the lease
// through Store before entering and supplies the configured model adapter.
func (s *Server) advanceAssistantExecution(ctx context.Context, task domain.AssistantTask, model assistant.Model) (domain.AssistantTask, error) {
	checkpoint := assistantExecutionCheckpoint{Version: 2, Messages: []assistant.Message{{Role: "user", Text: task.RequestText}}}
	if string(task.Checkpoint) != "{}" && len(task.Checkpoint) > 0 {
		if json.Unmarshal(task.Checkpoint, &checkpoint) != nil || checkpoint.Version != 2 {
			return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
		}
	}
	if task.State != "running" {
		return task, store.ErrAssistantLeaseLost
	}
	if _, err := s.executionToolContext(ctx, task.HomeID, task.UserID, "home_member"); err != nil {
		return s.failAssistantExecution(ctx, task, checkpoint, "permission_denied")
	}
	inputs, err := s.store.PendingAssistantTaskInputs(ctx, task.HomeID, task.UserID, task.ID)
	if err != nil {
		return task, err
	}
	if len(inputs) > 0 {
		uncertain, err := s.store.AssistantTaskHasUncertainOperations(ctx, task.HomeID, task.UserID, task.ID)
		if err != nil {
			return task, err
		}
		if uncertain {
			task.State = "waiting_input"
			checkpoint.ErrorCode = "outcome_unknown"
			task, err = encodeExecutionCheckpoint(task, checkpoint)
			if err != nil {
				return task, err
			}
			return s.store.SaveAssistantTask(ctx, task, "waiting_input", "")
		}
		if checkpoint.Next < 0 || checkpoint.Next > len(checkpoint.Pending) {
			return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
		}
		for _, call := range checkpoint.Pending[checkpoint.Next:] {
			observation := assistant.Failure(call.ID, "cancelled")
			step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return task, err
			}
			if err == nil && (step.State == "completed" || step.State == "failed") {
				if json.Unmarshal(step.Result, &observation) != nil {
					return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
				}
			}
			checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "tool", Result: &observation, OriginTaskID: task.ID})
		}
		checkpoint.Pending = nil
		checkpoint.Next = 0
		checkpoint.FinalText = ""
		checkpoint.ErrorCode = ""
		checkpoint.CompletionRepairs = 0
		for _, input := range inputs {
			task.State = "running"
			checkpoint.FinalText = ""
			checkpoint.AllowedTools = nil
			checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "user", Text: input.Text, OriginTaskID: task.ID})
			if directive, explicit := s.executionSlash(ctx, task.HomeID, task.UserID, input.Text); explicit {
				if directive.Error != nil || directive.Call != nil {
					// Recover queued inputs accepted by an older server, too.
					task.State = "waiting_input"
					checkpoint.FinalText = "That command is unavailable here. Use /files to search files, or describe your request in ordinary language."
					continue
				}
				checkpoint.AllowedTools = directive.Tools
			}
		}
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		if err != nil {
			return task, err
		}
		return s.store.ApplyAssistantTaskInputs(ctx, task, inputs)
	}
	if !s.authorizeExecutionContext(ctx, task, checkpoint) {
		pending := checkpoint.Next < len(checkpoint.Pending)
		discardExecutionEvidence(&checkpoint)
		if pending {
			return s.failAssistantExecution(ctx, task, checkpoint, "permission_denied")
		}
	}
	compressExecutionContext(&checkpoint)
	registry, err := s.newAssistantExecutionTools(task.HomeID, task.UserID, task.SessionID)
	if err != nil {
		return s.failAssistantExecution(ctx, task, checkpoint, "capability_unavailable")
	}
	if checkpoint.Next < 0 || checkpoint.Next > len(checkpoint.Pending) {
		return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
	}
	if checkpoint.Next < len(checkpoint.Pending) {
		return s.advanceAssistantExecutionTool(ctx, task, checkpoint, registry)
	}
	if task.Turns >= task.MaxTurns || task.Tokens >= task.MaxTokens || task.ActiveMS >= task.MaxActiveMS {
		return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
	}
	if model == nil {
		return s.failAssistantExecution(ctx, task, checkpoint, "capability_unavailable")
	}
	// Reserve one turn and a bounded active interval before contacting the
	// provider, so a process crash cannot reset consumption indefinitely.
	reserveMS := min(int64(90000), task.MaxActiveMS-task.ActiveMS)
	reserveTokens := min(int64(4096), task.MaxTokens-task.Tokens)
	task.Turns++
	task.ActiveMS += reserveMS
	task.Tokens += reserveTokens
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	if err != nil {
		return task, err
	}
	task, err = s.store.SaveAssistantTask(ctx, task, "running", "")
	if err != nil {
		return task, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(reserveMS)*time.Millisecond)
	started := time.Now()
	modelMessages := append([]assistant.Message{}, checkpoint.Messages...)
	if checkpoint.ContextNote != "" {
		modelMessages = append([]assistant.Message{{Role: "system", Text: checkpoint.ContextNote}}, modelMessages...)
	}
	definitions := executionNoteDefinitions(executionFileDefinitions(registry.Definitions(), checkpoint.Messages), checkpoint.Messages)
	if len(checkpoint.AllowedTools) > 0 {
		filtered := []assistant.Definition{}
		for _, def := range definitions {
			if slices.Contains(checkpoint.AllowedTools, def.Name) {
				filtered = append(filtered, def)
			}
		}
		definitions = filtered
	}
	for _, definition := range definitions {
		if definition.Name == "notes.append" {
			// Keep parameter semantics beside the current observations. Some
			// models otherwise add a second separator for "on a new line".
			guidance := assistant.Message{Role: "system", Text: "Only append when the latest user request asks for it. For notes.append, text is only the requested new content, copied verbatim. The server inserts the newline separating it from the existing note. A request to put text on a new line does not require a leading newline in text. Add leading newlines only when the user explicitly requests extra blank lines. Never include the existing body."}
			at := len(modelMessages)
			if at > 0 && modelMessages[at-1].Role == "system" && modelMessages[at-1].Text == executionDecisionRepairInstructions {
				at-- // Preserve the response-adapter repair marker at the end.
			}
			modelMessages = append(modelMessages, assistant.Message{})
			copy(modelMessages[at+1:], modelMessages[at:])
			modelMessages[at] = guidance
			break
		}
	}
	turn, modelErr := model.Next(callCtx, assistant.ModelRequest{Messages: modelMessages, Tools: definitions})
	modelExpired := callCtx.Err() != nil
	cancel()
	task.ActiveMS = task.ActiveMS - reserveMS + min(reserveMS, time.Since(started).Milliseconds())
	if modelErr != nil || modelExpired || turn.Validate() != nil {
		return s.failAssistantExecution(ctx, task, checkpoint, "model_unavailable")
	}
	if turn.Usage.InputTokens != nil && turn.Usage.OutputTokens != nil {
		if *turn.Usage.OutputTokens > task.MaxTokens || *turn.Usage.InputTokens > task.MaxTokens-*turn.Usage.OutputTokens {
			task.Tokens = task.MaxTokens + 1
		} else {
			task.Tokens = task.Tokens - reserveTokens + *turn.Usage.InputTokens + *turn.Usage.OutputTokens
		}
	}
	if task.Tokens > task.MaxTokens {
		return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
	}
	if !s.authorizeExecutionContext(ctx, task, checkpoint) {
		discardExecutionEvidence(&checkpoint)
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		if err != nil {
			return task, err
		}
		return s.store.SaveAssistantTask(ctx, task, "running", "")
	}
	if turn.FinishReason == "error" {
		checkpoint.FormatRepairs++
		if checkpoint.FormatRepairs > 2 {
			return s.failAssistantExecution(ctx, task, checkpoint, "model_unavailable")
		}
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Text: turn.Text}, assistant.Message{Role: "system", Text: executionDecisionRepairInstructions})
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		if err != nil {
			return task, err
		}
		return s.store.SaveAssistantTask(ctx, task, "running", "")
	}
	checkpoint.FormatRepairs = 0
	seen := map[string]bool{}
	for _, id := range checkpoint.Seen {
		seen[id] = true
	}
	for _, call := range turn.Calls {
		if seen[call.ID] {
			return s.failAssistantExecution(ctx, task, checkpoint, "duplicate_call")
		}
		seen[call.ID] = true
		checkpoint.Seen = append(checkpoint.Seen, call.ID)
	}
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Text: turn.Text, Calls: turn.Calls, Continuation: turn.Continuation})
	checkpoint.Pending = turn.Calls
	checkpoint.Next = 0
	event := "running"
	switch turn.FinishReason {
	case "final":
		uncertain, e := s.store.AssistantTaskHasUncertainOperations(ctx, task.HomeID, task.UserID, task.ID)
		if e != nil {
			return task, e
		}
		if guidance := executionIncompleteFileWrite(task, checkpoint.Messages); !uncertain && guidance != "" {
			checkpoint.CompletionRepairs++
			if checkpoint.CompletionRepairs <= 2 {
				checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "system", Text: guidance})
			} else {
				task.State = "waiting_input"
				checkpoint.FinalText = "The requested file operation is not complete. I could not verify completion of the requested changes. Please clarify the remaining action or retry."
				event = "waiting_input"
			}
			break
		}

		if uncertain {
			turn.Text = executionUnverifiedFinalText
		}
		task.State = "completed"
		checkpoint.FinalText = executionVerifiedFinalText(task.ID, checkpoint.Messages, turn.Text)
		checkpoint.Sources = executionCitations(task, checkpoint.Messages, turn.Text)
		event = "completed"
	case "needs_input":
		task.State = "waiting_input"
		checkpoint.FinalText = turn.Text
		event = "waiting_input"
	case "tool_calls":
	default:
		return s.failAssistantExecution(ctx, task, checkpoint, "model_unavailable")
	}
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	if err != nil {
		return task, err
	}
	return s.store.SaveAssistantTask(ctx, task, event, "")
}

func (s *Server) advanceAssistantExecutionTool(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint, registry *assistant.Registry) (domain.AssistantTask, error) {
	call := checkpoint.Pending[checkpoint.Next]
	if call.Tool == "apps.invoke" && (checkpoint.ExplicitApp == nil || checkpoint.ExplicitApp.ID != call.ID || !bytes.Equal(checkpoint.ExplicitApp.Arguments, call.Arguments)) {
		return s.failAssistantExecution(ctx, task, checkpoint, "invalid_tool_selection")
	}
	if !executionToolAllowed(checkpoint.AllowedTools, call.Tool) && call.Tool != "files.create_folder" && call.Tool != "files.upload" {
		return s.failAssistantExecution(ctx, task, checkpoint, "invalid_tool_selection")
	}
	step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	if errors.Is(err, store.ErrNotFound) {
		step = domain.AssistantTaskStep{TaskID: task.ID, CallID: call.ID, Sequence: int64(task.Calls + 1), Tool: call.Tool, ToolVersion: call.Version, Arguments: call.Arguments, ActionDigest: assistantActionDigest(task, call, nil), State: "pending"}
		task, err = s.store.PlanAssistantTaskStep(ctx, task, step)
	}
	if err != nil {
		return task, err
	}
	if step.State == "waiting_approval" {
		approval, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
		if err != nil {
			return task, err
		}
		if approval.State == "pending" {
			task.State = "waiting_approval"
			return s.store.SaveAssistantTask(ctx, task, "waiting_approval", call.ID)
		}
		if call.Tool == "files.create_folder" || call.Tool == "files.upload" || call.Tool == "homeassistant.call_service" || call.Tool == "machines.service_action" {
			return s.advanceAssistantExecutionAgentWrite(ctx, task, checkpoint, call, step, registry)
		}
		if call.Tool == "apps.invoke" {
			return s.advanceAssistantExecutionAppWrite(ctx, task, checkpoint, call, step, registry)
		}
		if call.Tool == "notes.create" || call.Tool == "notes.append" {
			return s.advanceAssistantExecutionNoteWrite(ctx, task, checkpoint, call, step, registry)
		}
		return s.failAssistantExecution(ctx, task, checkpoint, "capability_unavailable")
	}
	if step.State == "cancelled" {
		code := "cancelled"
		if approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID); e == nil && approval.State == "rejected" {
			code = "approval_rejected"
		}
		observation := assistant.Failure(call.ID, code)
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "tool", Result: &observation, OriginTaskID: task.ID})
		checkpoint.Next++
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		if err != nil {
			return task, err
		}
		return s.store.SaveAssistantTask(ctx, task, "checking", call.ID)
	}
	if step.State == "completed" || step.State == "failed" {
		var observation assistant.Result
		if json.Unmarshal(step.Result, &observation) != nil {
			return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
		}
		if observation.Outcome == "confirmed" && (call.Tool == "notes.create" || call.Tool == "notes.append") {
			for _, resource := range observation.Resources {
				if note, e := s.store.GetUserNoteByID(ctx, resource.ID); e == nil {
					s.enqueueAssistantIndexJob(ctx, task.HomeID, task.UserID, assistantNoteSourceType(note), note.NoteID)
				}
			}
		}
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "tool", Result: &observation, OriginTaskID: task.ID})
		checkpoint.Next++
		task, err = encodeExecutionCheckpoint(task, checkpoint)
		if err != nil {
			return task, err
		}
		return s.store.SaveAssistantTask(ctx, task, "checking", call.ID)
	}
	if step.State == "unknown" {
		return s.failAssistantExecution(ctx, task, checkpoint, "outcome_unknown")
	}
	if step.State == "running" && call.Tool == "apps.invoke" {
		return s.advanceAssistantExecutionAppWrite(ctx, task, checkpoint, call, step, registry)
	}
	if step.State == "running" && (call.Tool == "files.create_folder" || call.Tool == "files.upload" || call.Tool == "homeassistant.call_service" || call.Tool == "machines.service_action") {
		if _, receiptErr := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID)); receiptErr == nil {
			return s.advanceAssistantExecutionAgentWrite(ctx, task, checkpoint, call, step, registry)
		} else if !errors.Is(receiptErr, store.ErrNotFound) {
			return task, receiptErr
		}
	}
	if step.State == "running" && (call.Tool == "notes.create" || call.Tool == "notes.append") {
		if _, receiptErr := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID)); receiptErr == nil {
			return s.advanceAssistantExecutionNoteWrite(ctx, task, checkpoint, call, step, registry)
		} else if !errors.Is(receiptErr, store.ErrNotFound) {
			return task, receiptErr
		}
	}
	if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS || step.Attempts >= 3 {
		return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
	}
	reserveMS := min(int64(30000), task.MaxActiveMS-task.ActiveMS)
	task.Calls++
	task.ActiveMS += reserveMS
	task, step, err = s.store.StartAssistantTaskStep(ctx, task, call.ID)
	if err != nil {
		return task, err
	}
	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(reserveMS)*time.Millisecond)
	var observation assistant.Result
	switch {
	case !executionToolAllowed(checkpoint.AllowedTools, call.Tool):
		observation = assistant.Failure(call.ID, "action_not_requested")
	case executionRepeatedFailure(call, checkpoint.Messages):
		observation = assistant.Failure(call.ID, "repeated_call")
	default:
		if code := executionFileCallError(call, checkpoint.Messages); code != "" {
			observation = assistant.Failure(call.ID, code)
		} else {
			observation = registry.Invoke(callCtx, call)
		}
	}
	cancel()
	task.ActiveMS = task.ActiveMS - reserveMS + min(reserveMS, time.Since(started).Milliseconds())
	step.Result, err = json.Marshal(observation)
	if err != nil {
		return task, err
	}
	step.State = "completed"
	if observation.Error != nil {
		step.State = "failed"
	}
	var approval *domain.AssistantTaskApproval
	if observation.Outcome == "not_started" && observation.Error == nil {
		step.State = "waiting_approval"
		step.ActionDigest = assistantActionDigest(task, call, &observation)
		approval = &domain.AssistantTaskApproval{ID: newID("approval"), TaskID: task.ID, CallID: call.ID, ActionDigest: step.ActionDigest, ExpiresAt: time.Now().Add(15 * time.Minute)}
	} else {
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "tool", Result: &observation, OriginTaskID: task.ID})
		checkpoint.Next++
	}
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	if err != nil {
		return task, err
	}
	return s.store.CompleteAssistantTaskStep(ctx, task, step, approval)
}

func (s *Server) failAssistantExecution(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint, code string) (domain.AssistantTask, error) {
	task.State = "failed"
	checkpoint.ErrorCode = code
	task, err := encodeExecutionCheckpoint(task, checkpoint)
	if err != nil {
		task.Checkpoint, _ = json.Marshal(assistantExecutionCheckpoint{Version: 2, ErrorCode: code})
	}
	return s.store.SaveAssistantTask(ctx, task, "failed", "")
}

type assistantExecutionModelFactory func(context.Context, domain.AssistantTask) (assistant.Model, error)

// Explicit activation is owned by the v2 rollout. Shutdown cancels work and
// leaves checkpoints/leases for safe recovery; no startup schema mutation.
func (s *Server) runAssistantExecutionWorker(ctx context.Context, workerID string, factory assistantExecutionModelFactory) {
	if workerID == "" || factory == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_, _ = s.store.ExpireAssistantTaskApprovals(ctx)
		task, err := s.store.ClaimAssistantTask(ctx, workerID, 2*time.Minute)
		if err == nil {
			// Receipt recovery must not depend on a configured or reachable model.
			model := &deferredExecutionModel{factory: factory, task: task}
			for task.State == "running" && ctx.Err() == nil {
				if err := s.store.RenewAssistantTaskLease(ctx, task, 2*time.Minute); err != nil {
					break
				}
				advanced, advanceErr := s.advanceAssistantExecutionWithCancellation(ctx, task, model)
				if advanceErr != nil {
					break
				}
				task = advanced
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type deferredExecutionModel struct {
	factory assistantExecutionModelFactory
	task    domain.AssistantTask
	model   assistant.Model
}

func (m *deferredExecutionModel) Next(ctx context.Context, request assistant.ModelRequest) (assistant.Turn, error) {
	if m.model == nil {
		model, err := m.factory(ctx, m.task)
		if err != nil {
			return assistant.Turn{}, err
		}
		if model == nil {
			return assistant.Turn{}, errExecutionProviderUnavailable
		}
		m.model = model
	}
	return m.model.Next(ctx, request)
}

// A durable stop/follow-up fences this lease. Cancel active provider/agent IO as
// soon as that transition is observed, without relying on a browser connection.
func (s *Server) advanceAssistantExecutionWithCancellation(ctx context.Context, task domain.AssistantTask, model assistant.Model) (domain.AssistantTask, error) {
	unitCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-unitCtx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(unitCtx, time.Second)
				current, err := s.store.GetAssistantTask(checkCtx, task.HomeID, task.UserID, task.ID)
				checkCancel()
				if err != nil || current.Fence != task.Fence || current.State != "running" {
					cancel()
					return
				}
			}
		}
	}()
	result, err := s.advanceAssistantExecution(unitCtx, task, model)
	cancel()
	<-done
	return result, err
}

const executionUnverifiedFinalText = "The action's result is not verified. Check the target before requesting the action again; Hank has not automatically repeated it."

// Unverified receipts cannot be promoted into a success claim by model prose.
// Historical observations from other tasks do not change this task's outcome.
func executionVerifiedFinalText(taskID string, messages []assistant.Message, text string) string {
	for _, message := range messages {
		if message.OriginTaskID != taskID || message.Result == nil || message.Result.OperationID == nil {
			continue
		}
		result := message.Result
		if result.Outcome == "accepted" || result.Outcome == "unknown" || (result.Outcome == "confirmed" && result.Verification != "observed") {
			// An opaque app can still provide a useful answer. Attribute its
			// reported text without promoting its side effects to verified facts.
			var data executionData
			if result.Outcome == "accepted" && json.Unmarshal(result.Data, &data) == nil && len(data.Items) == 1 && data.Items[0].Type == "app" {
				item := data.Items[0]
				if content, err := assistantContentFromGenericAppOutput(item.Title, json.RawMessage(item.Text)); err == nil && content.Text != "" {
					return executionUnverifiedFinalText + "\n\nApp response (not independently verified):\n" + executionText(content.Text)
				}
			}
			return executionUnverifiedFinalText
		}
	}
	return text
}
