package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Server) assistantExecutionVersions(ctx context.Context, home, user string) []int {
	if !s.assistantAI.ExecutionEnabled {
		return []int{1}
	}
	model := executionProviderModel{s, home, user}
	if _, _, _, _, err := model.configuration(ctx); err != nil {
		return []int{1}
	}
	return []int{1, 2}
}
func (s *Server) handleAssistantExecutionSubmit(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, session domain.AssistantSession) {
	if len(s.assistantExecutionVersions(r.Context(), home.ID, auth.User.ID)) < 2 {
		writeJSON(w, 409, map[string]string{"error": "execution_version_unavailable"})
		return
	}
	var input struct {
		Content            string            `json:"content"`
		SubmissionID       string            `json:"submission_id"`
		Attachments        []json.RawMessage `json:"attachments"`
		ClientCapabilities map[string]bool   `json:"client_capabilities"`
		DeviceContext      json.RawMessage   `json:"device_context"`
	}
	if !decodeAssistantTaskInput(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Content) == "" || len(input.Content) > 16384 || len(input.SubmissionID) < 1 || len(input.SubmissionID) > 128 {
		http.Error(w, "invalid task submission", 400)
		return
	}
	if len(input.Attachments) > 0 {
		writeJSON(w, 409, map[string]string{"error": "attachment_execution_unavailable"})
		return
	}
	taskID := newID("atask")
	checkpoint := assistantExecutionCheckpoint{Version: 2, Messages: []assistant.Message{}}
	if directive, explicit := s.executionSlash(r.Context(), home.ID, auth.User.ID, input.Content); explicit {
		if directive.Error != nil {
			writeJSON(w, 400, map[string]string{"error": directive.Error.Code})
			return
		}
		if directive.Call != nil {
			directive.Call.ID = newID("appcall")
			checkpoint.ExplicitApp = directive.Call
			checkpoint.Pending = []assistant.Call{*directive.Call}
		}
		checkpoint.AllowedTools = directive.Tools
	}
	// Seed ordinary dialogue, then preserve v2 structured observations through
	// the dedicated context builder. Never parse old text to choose a tool.
	messages, err := s.store.ListAssistantMessages(r.Context(), session.ID)
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	start := max(0, len(messages)-20)
	for _, message := range messages[start:] {
		var content assistantMessageContent
		if json.Unmarshal([]byte(message.ContentJSON), &content) == nil && message.Role == "user" {
			checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: message.Role, Text: executionText(content.Text)})
		}
	}
	previous, err := s.store.ListAssistantTasks(r.Context(), home.ID, auth.User.ID, session.ID)
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	if len(previous) > 0 {
		var saved assistantExecutionCheckpoint
		if json.Unmarshal(previous[0].Checkpoint, &saved) == nil && saved.Version == 2 {
			checkpoint.Messages = saved.Messages
			checkpoint.Seen = saved.Seen
			checkpoint.ContextNote = saved.ContextNote
		}
	}
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "user", Text: input.Content, OriginTaskID: taskID})
	if checkpoint.ExplicitApp != nil {
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Calls: checkpoint.Pending})
		checkpoint.Seen = append(checkpoint.Seen, checkpoint.ExplicitApp.ID)
	}
	compressExecutionContext(&checkpoint)
	task := domain.AssistantTask{ID: taskID, HomeID: home.ID, UserID: auth.User.ID, SessionID: session.ID, SubmissionKey: input.SubmissionID, RequestText: input.Content}
	if executionFileWriteRequested("files.upload", []assistant.Message{{Role: "user", Text: input.Content}}) {
		stages, err := s.store.ListAssistantStages(r.Context(), home.ID, auth.User.ID, session.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		// Each attachment needs preparation, approval and verified execution.
		// Charge bounded extra work up front, not a fresh budget on every retry.
		count := min(len(stages), 10)
		task.MaxTurns = 12 + 2*count
		task.MaxCalls = 24 + 2*count
		task.MaxTokens = 32000 + 9600*int64(count)
		task.MaxActiveMS = 300000 + 60000*int64(count)
	}
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	if err != nil {
		http.Error(w, "context exceeds task budget", 400)
		return
	}
	task, _, err = s.store.CreateAssistantTask(r.Context(), task)
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, assistantTaskSnapshot(task))
}
