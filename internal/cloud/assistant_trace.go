package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const (
	defaultAssistantTraceLimit = 250
	maxAssistantTraceLimit     = 1000
)

type assistantTraceContextKey struct{}

type assistantTraceContext struct {
	HomeID    string
	UserID    string
	SessionID string
	RunID     string
	MessageID string
	RequestID string
}

type assistantTraceEvent struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	Level     string            `json:"level"`
	Scope     string            `json:"scope"`
	Event     string            `json:"event"`
	Summary   string            `json:"summary"`
	HomeID    string            `json:"home_id,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	RunID     string            `json:"run_id,omitempty"`
	MessageID string            `json:"message_id,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

type assistantTraceLog struct {
	mu     sync.Mutex
	events []assistantTraceEvent
	limit  int
}

type assistantTraceResponse struct {
	Events []assistantTraceEvent `json:"events"`
	Total  int                   `json:"total"`
}

func newAssistantTraceLog(limit int) *assistantTraceLog {
	if limit <= 0 {
		limit = maxAssistantTraceLimit
	}
	return &assistantTraceLog{limit: limit}
}

func withAssistantTraceContext(ctx context.Context, trace assistantTraceContext) context.Context {
	return context.WithValue(ctx, assistantTraceContextKey{}, trace)
}

func assistantTraceContextFrom(ctx context.Context) assistantTraceContext {
	if ctx == nil {
		return assistantTraceContext{}
	}
	trace, _ := ctx.Value(assistantTraceContextKey{}).(assistantTraceContext)
	return trace
}

func (s *Server) handleAssistantLogs(w http.ResponseWriter, r *http.Request, home domain.Home, membership domain.HomeMembership) {
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := defaultAssistantTraceLimit
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = min(parsed, maxAssistantTraceLimit)
			}
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
		events, total := s.assistantTraceSnapshot(home.ID, sessionID, runID, limit)
		writeJSON(w, http.StatusOK, assistantTraceResponse{Events: events, Total: total})
	case http.MethodDelete:
		cleared := s.clearAssistantTrace(home.ID)
		s.recordAssistantTrace(r.Context(), assistantTraceEvent{
			Level:   "info",
			Scope:   "assistant",
			Event:   "assistant.trace.cleared",
			Summary: "Cleared HankAI workflow trace entries.",
			HomeID:  home.ID,
			Details: map[string]string{
				"cleared": strconv.Itoa(cleared),
			},
		})
		writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) recordAssistantTrace(ctx context.Context, event assistantTraceEvent) {
	if ctx != nil && ctx.Value(fileSearchIndexContextKey{}) == true {
		return
	}
	if s == nil || s.assistantTrace == nil {
		return
	}
	trace := assistantTraceContextFrom(ctx)
	if event.HomeID == "" {
		event.HomeID = trace.HomeID
	}
	if event.UserID == "" {
		event.UserID = trace.UserID
	}
	if event.SessionID == "" {
		event.SessionID = trace.SessionID
	}
	if event.RunID == "" {
		event.RunID = trace.RunID
	}
	if event.MessageID == "" {
		event.MessageID = trace.MessageID
	}
	if event.RequestID == "" {
		event.RequestID = trace.RequestID
	}
	event.ID = newID("atrace")
	event.CreatedAt = time.Now().UTC()
	event.Level = firstNonBlank(event.Level, "info")
	event.Scope = firstNonBlank(event.Scope, "assistant")
	// Only code-owned event names and summaries may enter the diagnostic log.
	summary, known := assistantTraceSummaries[event.Event]
	if !known {
		return
	}
	event.Summary = summary
	event.Scope = "assistant"
	if strings.HasPrefix(event.Event, "media.") {
		event.Scope = "media"
	}
	if strings.HasPrefix(event.Event, "agent.") {
		event.Scope = "agent"
	}
	switch event.Level {
	case "info", "warn", "error":
	default:
		event.Level = "info"
	}
	event.Details = sanitizeTraceDetails(event.Details)
	s.assistantTrace.append(event)
}

func (s *Server) assistantTraceSnapshot(homeID string, sessionID string, runID string, limit int) ([]assistantTraceEvent, int) {
	if s == nil || s.assistantTrace == nil {
		return nil, 0
	}
	return s.assistantTrace.snapshot(homeID, sessionID, runID, limit)
}

func (s *Server) clearAssistantTrace(homeID string) int {
	if s == nil || s.assistantTrace == nil {
		return 0
	}
	return s.assistantTrace.clear(homeID)
}

func (l *assistantTraceLog) append(event assistantTraceEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	if l.limit > 0 && len(l.events) > l.limit {
		l.events = append([]assistantTraceEvent(nil), l.events[len(l.events)-l.limit:]...)
	}
}

func (l *assistantTraceLog) snapshot(homeID string, sessionID string, runID string, limit int) ([]assistantTraceEvent, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 {
		limit = defaultAssistantTraceLimit
	}
	limit = min(limit, maxAssistantTraceLimit)
	filtered := make([]assistantTraceEvent, 0, len(l.events))
	for _, event := range l.events {
		if homeID != "" && event.HomeID != "" && event.HomeID != homeID {
			continue
		}
		if sessionID != "" && event.SessionID != sessionID {
			continue
		}
		if runID != "" && event.RunID != runID {
			continue
		}
		filtered = append(filtered, event)
	}
	total := len(filtered)
	if total > limit {
		filtered = filtered[total-limit:]
	}
	return append([]assistantTraceEvent(nil), filtered...), total
}

func (l *assistantTraceLog) clear(homeID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if homeID == "" {
		cleared := len(l.events)
		l.events = nil
		return cleared
	}
	kept := l.events[:0]
	cleared := 0
	for _, event := range l.events {
		if event.HomeID == "" || event.HomeID == homeID {
			cleared++
			continue
		}
		kept = append(kept, event)
	}
	l.events = kept
	return cleared
}

// Diagnostic data is an allowlist, not a best-effort secret scrubber. In
// particular, provider errors, prompts, filenames and model output are private.
func sanitizeTraceDetails(details map[string]string) map[string]string {
	sanitized := make(map[string]string)
	for key, value := range details {
		switch key {
		case "attachment_count", "card_count", "elapsed_ms", "existing_count", "fallback_quality_count", "items", "limit", "max_context", "missing_link_count", "preferred_quality_count", "result_count", "cleared", "completed_count", "total_count", "failed_count", "skipped_count":
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				sanitized[key] = strconv.FormatUint(n, 10)
			}
		case "approved", "calendar", "conversations", "files", "has_error", "home_notes", "homeassistant", "profile_notes", "project_docs", "requires_confirmation", "result_set":
			if value == "true" || value == "false" {
				sanitized[key] = value
			}
		case "tool", "intent", "selected_tool":
			if isKnownAssistantTraceTool(value) {
				sanitized[key] = value
			}
		case "state", "run_state":
			switch value {
			case assistantStateCompleted, assistantStateWaitingClientTool, assistantStateWaitingConfirm:
				sanitized[key] = value
			}
		case "pending_action":
			switch value {
			case "note_append", "note_create", "calendar_create", "calendar_update", "calendar_delete", "attachment_commit", "homeassistant_control", "media_download", "file_create_folder":
				sanitized[key] = value
			}
		}
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func isKnownAssistantTraceTool(value string) bool {
	switch assistantIntentKind(value) {
	case assistantIntentGeneral,
		assistantIntentNotesList,
		assistantIntentNotesSearch,
		assistantIntentNotesAppend,
		assistantIntentNotesCreate,
		assistantIntentNotesSummarize,
		assistantIntentFilesSearch,
		assistantIntentFilesListFolder,
		assistantIntentFilesCreateFolder,
		assistantIntentCalendarSearch,
		assistantIntentCalendarCreate,
		assistantIntentCalendarUpdate,
		assistantIntentCalendarDelete,
		assistantIntentMediaSearch,
		assistantIntentMediaSelection,
		assistantIntentGramatonCommand,
		assistantIntentHACommand,
		assistantIntentFilesCommand,
		assistantIntentNotesCommand,
		assistantIntentAppendCommand,
		assistantIntentCalendarCommand,
		assistantIntentDocsCommand,
		assistantIntentStatusCommand,
		assistantIntentHermesChat,
		assistantIntentYDownloadCommand,
		assistantIntentInstalledAppCommand,
		assistantIntentAssistantStatus,
		assistantIntentAgentStatus,
		assistantIntentSyncStatus,
		assistantIntentBackupStatus,
		assistantIntentHomeAssistantControl,
		assistantIntentHomeAssistantQuery,
		assistantIntentProjectDocs,
		assistantIntentMemorySearch,
		assistantIntentReadOnlySynthesis, "attachments.commit":
		return true
	default:
		return false
	}
}

var assistantTraceSummaries = map[string]string{
	"agent.command.encode_failed":   "Could not encode the agent command body.",
	"agent.command.envelope_failed": "Could not create the agent command envelope.",
	"agent.command.offline":         "Primary Hank Agent is offline before command dispatch.",
	"agent.command.register_failed": "Could not register the pending agent request.",
	"agent.command.start":           "Sending command to the primary Hank Agent.",
	"agent.command.timeout":         "Timed out waiting for the primary Hank Agent response.",
	"agent.command.write_failed":    "Failed while sending command to the primary Hank Agent.",
	"media.download_completed":      "Primary Hank Agent emitted a media download event.",
	"media.download_progress":       "Primary Hank Agent emitted a media download event.",

	"assistant.attachments.failed":          "Could not persist assistant attachment records.",
	"assistant.attachments.plan_failed":     "Attachment workflow planning failed.",
	"assistant.attachments.plan_handled":    "Attachment workflow handled this run.",
	"assistant.attachments.planning":        "Checking whether uploaded files need a commit workflow.",
	"assistant.calendar.plan_matched":       "Calendar creation parser matched the prompt.",
	"assistant.client_tool.completed":       "Client tool result completed the run.",
	"assistant.client_tool.finalize_failed": "Client tool result could not be finalized.",
	"assistant.client_tool.result_received": "Received client tool result for a waiting run.",
	"assistant.client_tool.waiting":         "Run is waiting for a client-side tool.",
	"assistant.confirmation.approved":       "User approved the pending action.",
	"assistant.confirmation.cancelled":      "User cancelled the pending action.",
	"assistant.confirmation.received":       "Received a confirmation response.",
	"assistant.confirmation.waiting":        "Run is waiting for user confirmation.",
	"assistant.confirmed_action.completed":  "Approved action completed.",
	"assistant.confirmed_action.failed":     "Approved action failed.",
	"assistant.generate.failed":             "Assistant workflow execution failed.",
	"assistant.index.refresh_done":          "Context refresh finished.",
	"assistant.index.refresh_start":         "Refreshing enabled HankAI context before tool execution.",
	"assistant.media.selection_resolved":    "Resolved the reply against previous media result cards.",
	"assistant.message.received":            "HankAI received a chat message.",
	"assistant.run.completed":               "Assistant run completed.",
	"assistant.run.created":                 "Created an assistant run.",
	"assistant.settings.failed":             "Could not load HankAI settings.",
	"assistant.settings.loaded":             "Loaded HankAI settings for this run.",
	"assistant.tool.execute_done":           "HankAI tool execution finished.",
	"assistant.tool.execute_failed":         "HankAI tool execution failed.",
	"assistant.tool.execute_start":          "Executing the matched HankAI tool.",
	"assistant.tool.local_planner_result":   "Local model returned a HankAI planner decision.",
	"assistant.tool.local_planner_selected": "Local model selected a more specific HankAI tool.",
	"assistant.tool.missing_executor":       "Matched tool has no executor.",
	"assistant.tool.resolved":               "Matched the prompt to a HankAI tool.",
	"assistant.trace.cleared":               "Cleared HankAI workflow trace entries.",
	"assistant.user_message.failed":         "Could not persist the user message.",
	"assistant.user_message.saved":          "Saved the user message.",
	"media.confirmation.skipped":            "Media workflow is configured to start downloads without an approval pause.",
	"media.download.failed":                 "Could not start the media download after planning.",
	"media.download.started":                "Primary Hank Agent started the media download job.",
	"media.plan.failed":                     "Could not prepare the media download plan.",
	"media.plan.prepared":                   "Prepared the media download plan.",
	"media.search.decode_failed":            "Media search response could not be decoded.",
	"media.search.empty_query":              "Media workflow matched, but no usable title was parsed.",
	"media.search.failed":                   "Media search could not reach or complete through the primary Hank Agent.",
	"media.search.results":                  "Media search returned results.",
	"media.search.start":                    "Starting media search through the primary Hank Agent.",
	"media.selection.start":                 "Preparing the selected media option.",
}

func traceDetails(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	details := make(map[string]string, len(values))
	for key, value := range values {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case assistantIntentKind:
			details[key] = string(typed)
		case string:
			details[key] = typed
		case int:
			details[key] = strconv.Itoa(typed)
		case int64:
			details[key] = strconv.FormatInt(typed, 10)
		case bool:
			details[key] = strconv.FormatBool(typed)
		case time.Duration:
			details[key] = typed.String()
		case time.Time:
			details[key] = typed.Format(time.RFC3339)
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				details[key] = "unprintable"
			} else {
				details[key] = string(encoded)
			}
		}
	}
	return details
}

func traceEventDetailsFromJSON(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return map[string]string{"payload_error": err.Error()}
	}
	keys := []string{"job_id", "title", "status", "completed_count", "total_count", "failed_count", "skipped_count", "current_file", "error_message"}
	details := make(map[string]string)
	for _, key := range keys {
		value, ok := body[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			details[key] = typed
		case float64:
			details[key] = strconv.Itoa(int(typed))
		case bool:
			details[key] = strconv.FormatBool(typed)
		default:
			encoded, _ := json.Marshal(typed)
			details[key] = string(encoded)
		}
	}
	return details
}
