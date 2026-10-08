package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	assistantRoleUser      = "user"
	assistantRoleAssistant = "assistant"

	assistantStateCompleted         = "completed"
	assistantStateWaitingClientTool = "waiting_client_tool"
	assistantStateWaitingConfirm    = "waiting_confirmation"

	assistantModelName = "hank-assistant-v1"
)

type assistantMessageContent struct {
	Sources     []assistantExecutionSource   `json:"sources,omitempty"`
	Text        string                       `json:"text"`
	Cards       []assistantResultCard        `json:"cards,omitempty"`
	Attachments []assistantMessageAttachment `json:"attachments,omitempty"`
	Meta        map[string]interface{}       `json:"meta,omitempty"`
}

type assistantMessageAttachment struct {
	ClientAttachmentID string `json:"client_attachment_id"`
	Filename           string `json:"filename"`
	ContentType        string `json:"content_type"`
	SizeBytes          int64  `json:"size_bytes"`
	ChecksumSHA256     string `json:"checksum_sha256"`
	Kind               string `json:"kind"`
}

type assistantResultCard struct {
	Kind          string                       `json:"kind"`
	Title         string                       `json:"title"`
	Summary       string                       `json:"summary"`
	ActionTitle   string                       `json:"action_title"`
	NoteID        string                       `json:"note_id,omitempty"`
	EventID       string                       `json:"event_id,omitempty"`
	SourceID      string                       `json:"source_id,omitempty"`
	TargetDate    *time.Time                   `json:"target_date,omitempty"`
	Path          string                       `json:"path,omitempty"`
	IsDirectory   bool                         `json:"is_directory,omitempty"`
	SearchText    string                       `json:"search_text,omitempty"`
	ImageURL      string                       `json:"image_url,omitempty"`
	MediaOptionID string                       `json:"media_option_id,omitempty"`
	MediaType     string                       `json:"media_type,omitempty"`
	MediaFilter   *protocol.MediaEpisodeFilter `json:"media_filter,omitempty"`
	EpisodeCount  int                          `json:"episode_count,omitempty"`
	Year          int                          `json:"year,omitempty"`
	JobID         string                       `json:"job_id,omitempty"`
}

type assistantClientToolRequest struct {
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type assistantPendingAction struct {
	Kind             string                            `json:"kind"`
	NoteAppend       *assistantPendingNoteAppend       `json:"note_append,omitempty"`
	NoteCreate       *assistantPendingNoteCreate       `json:"note_create,omitempty"`
	CalendarCreate   *assistantPendingCalendarCreate   `json:"calendar_create,omitempty"`
	CalendarClient   *assistantPendingCalendarClient   `json:"calendar_client,omitempty"`
	AttachmentCommit *assistantPendingAttachmentCommit `json:"attachment_commit,omitempty"`
	FileCreateFolder *assistantPendingFileCreateFolder `json:"file_create_folder,omitempty"`
	MediaDownload    *assistantPendingMediaDownload    `json:"media_download,omitempty"`
	HomeAssistant    *assistantPendingHAControl        `json:"homeassistant_control,omitempty"`
}

type assistantPendingNoteAppend struct {
	TargetNoteID  string `json:"target_note_id"`
	TargetNoteKey string `json:"target_note_key"`
	TargetTitle   string `json:"target_title"`
	TargetScope   string `json:"target_scope,omitempty"`
	AppendedText  string `json:"appended_text"`
	MatchHint     string `json:"match_hint"`
	Confirmation  string `json:"confirmation_message"`
}

type assistantPendingNoteCreate struct {
	Title        string `json:"title"`
	BodyMarkdown string `json:"body_markdown,omitempty"`
	Scope        string `json:"scope"`
	Confirmation string `json:"confirmation_message"`
}

type assistantPendingCalendarCreate struct {
	ToolRequest  assistantClientToolRequest `json:"tool_request"`
	Title        string                     `json:"title"`
	DateText     string                     `json:"date_text"`
	Confirmation string                     `json:"confirmation_message"`
}

type assistantPendingCalendarClient struct {
	ToolRequest  assistantClientToolRequest `json:"tool_request"`
	Title        string                     `json:"title"`
	Query        string                     `json:"query,omitempty"`
	Confirmation string                     `json:"confirmation_message"`
	Destructive  bool                       `json:"is_destructive"`
}

type assistantPendingAttachmentCommit struct {
	ToolRequest     assistantClientToolRequest `json:"tool_request"`
	AttachmentIDs   []string                   `json:"attachment_ids"`
	DestinationKind string                     `json:"destination_kind"`
	SourceID        string                     `json:"source_id,omitempty"`
	TargetNoteID    string                     `json:"target_note_id,omitempty"`
	TargetNoteKey   string                     `json:"target_note_key,omitempty"`
	TargetTitle     string                     `json:"target_title,omitempty"`
	TargetScope     string                     `json:"target_scope,omitempty"`
	TargetPath      string                     `json:"target_path,omitempty"`
	ConflictMode    string                     `json:"conflict_mode"`
	Confirmation    string                     `json:"confirmation_message"`
}

type assistantPendingFileCreateFolder struct {
	ToolRequest  assistantClientToolRequest `json:"tool_request"`
	SourceID     string                     `json:"source_id,omitempty"`
	Path         string                     `json:"path"`
	Confirmation string                     `json:"confirmation_message"`
}

type assistantPendingMediaDownload struct {
	Selection             protocol.MediaSearchResult  `json:"selection"`
	Filter                protocol.MediaEpisodeFilter `json:"filter,omitempty"`
	Title                 string                      `json:"title"`
	MediaType             string                      `json:"media_type"`
	ItemCount             int                         `json:"item_count"`
	PreferredQualityCount int                         `json:"preferred_quality_count"`
	FallbackQualityCount  int                         `json:"fallback_quality_count"`
	MissingLinkCount      int                         `json:"missing_link_count"`
	ExistingCount         int                         `json:"existing_count"`
	DestinationPath       string                      `json:"destination_path"`
	Confirmation          string                      `json:"confirmation_message"`
}

type assistantPendingHAControl struct {
	EntityID     string `json:"entity_id"`
	EntityName   string `json:"entity_name"`
	CurrentState string `json:"current_state,omitempty"`
	Domain       string `json:"domain"`
	Service      string `json:"service"`
	Confirmation string `json:"confirmation_message"`
	HighImpact   bool   `json:"is_high_impact"`
}

type assistantPendingActionSummary struct {
	Kind         string                         `json:"kind"`
	Title        string                         `json:"title"`
	Summary      string                         `json:"summary,omitempty"`
	Confirmation string                         `json:"confirmation_message"`
	Details      []assistantPendingActionDetail `json:"details,omitempty"`
	Destructive  bool                           `json:"is_destructive"`
}

type assistantPendingActionDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type assistantRankedNote struct {
	Note      domain.UserNote
	Score     int
	MatchTier int
}

type assistantIntentKind string

const (
	assistantIntentGeneral              assistantIntentKind = "general"
	assistantIntentNotesList            assistantIntentKind = "notes.list"
	assistantIntentNotesSearch          assistantIntentKind = "notes.search"
	assistantIntentNotesAppend          assistantIntentKind = "notes.append"
	assistantIntentNotesCreate          assistantIntentKind = "notes.create"
	assistantIntentNotesSummarize       assistantIntentKind = "notes.summarize"
	assistantIntentFilesSearch          assistantIntentKind = "files.search"
	assistantIntentFilesListFolder      assistantIntentKind = "files.list_folder"
	assistantIntentFilesCreateFolder    assistantIntentKind = "files.create_folder"
	assistantIntentCalendarSearch       assistantIntentKind = "calendar.search"
	assistantIntentCalendarCreate       assistantIntentKind = "calendar.create_event"
	assistantIntentCalendarUpdate       assistantIntentKind = "calendar.update_event"
	assistantIntentCalendarDelete       assistantIntentKind = "calendar.delete_event"
	assistantIntentMediaSearch          assistantIntentKind = "media.search"
	assistantIntentMediaSelection       assistantIntentKind = "media.selection"
	assistantIntentGramatonCommand      assistantIntentKind = "gramaton.command"
	assistantIntentHACommand            assistantIntentKind = "ha.command"
	assistantIntentFilesCommand         assistantIntentKind = "files.command"
	assistantIntentNotesCommand         assistantIntentKind = "notes.command"
	assistantIntentAppendCommand        assistantIntentKind = "append.command"
	assistantIntentCalendarCommand      assistantIntentKind = "calendar.command"
	assistantIntentDocsCommand          assistantIntentKind = "docs.command"
	assistantIntentStatusCommand        assistantIntentKind = "status.command"
	assistantIntentHermesChat           assistantIntentKind = "hermes.chat"
	assistantIntentYDownloadCommand     assistantIntentKind = "ydownload.command"
	assistantIntentInstalledAppCommand  assistantIntentKind = "installed_app.command"
	assistantIntentAssistantStatus      assistantIntentKind = "assistant.status"
	assistantIntentAgentStatus          assistantIntentKind = "agent.status"
	assistantIntentSyncStatus           assistantIntentKind = "sync.status"
	assistantIntentBackupStatus         assistantIntentKind = "backup.status"
	assistantIntentHomeAssistantControl assistantIntentKind = "homeassistant.control"
	assistantIntentHomeAssistantQuery   assistantIntentKind = "homeassistant.query"
	assistantIntentProjectDocs          assistantIntentKind = "project_docs"
	assistantIntentMemorySearch         assistantIntentKind = "assistant.memory_search"
	assistantIntentReadOnlySynthesis    assistantIntentKind = "read_only.synthesis"
)

type assistantIntent struct {
	Kind           assistantIntentKind
	Query          string
	MediaSelection *assistantResultCard
	AppID          string
	AppName        string
	CommandID      string
	SlashCommand   string
	AppUnavailable bool
}

type assistantDiagnostics struct {
	ToolKind            string `json:"tool_kind,omitempty"`
	IntentKind          string `json:"intent_kind,omitempty"`
	Query               string `json:"query,omitempty"`
	AppID               string `json:"app_id,omitempty"`
	CommandID           string `json:"command_id,omitempty"`
	SlashCommand        string `json:"slash_command,omitempty"`
	MediaSelectionTitle string `json:"media_selection_title,omitempty"`
	MediaSelectionPath  string `json:"media_selection_path,omitempty"`
}

type assistantRunResponse struct {
	ID                   string                         `json:"id"`
	State                string                         `json:"state"`
	RequiresClientTools  bool                           `json:"requires_client_tools"`
	RequiresConfirmation bool                           `json:"requires_confirmation"`
	AssistantMessage     *assistantAPIMessage           `json:"assistant_message,omitempty"`
	ClientToolRequest    *assistantClientToolRequest    `json:"client_tool_request,omitempty"`
	PendingActionSummary *assistantPendingActionSummary `json:"pending_action_summary,omitempty"`
	Diagnostics          *assistantDiagnostics          `json:"diagnostics,omitempty"`
}

type assistantAPISession struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	LastMessageAt time.Time `json:"last_message_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type assistantAPIMessage struct {
	Sources     []assistantExecutionSource `json:"sources,omitempty"`
	ID          string                     `json:"id"`
	Role        string                     `json:"role"`
	Text        string                     `json:"text"`
	CreatedAt   time.Time                  `json:"created_at"`
	Cards       []assistantResultCard      `json:"cards,omitempty"`
	Diagnostics *assistantDiagnostics      `json:"diagnostics,omitempty"`
}

type assistantSessionListResponse struct {
	Sessions []assistantAPISession `json:"sessions"`
}

type assistantMessageListResponse struct {
	Messages         []assistantAPIMessage `json:"messages"`
	ActiveRun        *assistantRunResponse `json:"active_run,omitempty"`
	ActiveTask       map[string]any        `json:"active_task,omitempty"`
	ExecutionVersion int                   `json:"execution_version"`
}

func (s *Server) handleHomeAssistant(w http.ResponseWriter, r *http.Request, home domain.Home, membership domain.HomeMembership, auth authContext, parts []string) bool {
	if len(parts) == 0 || parts[0] != "assistant" {
		return false
	}

	switch {
	case len(parts) >= 3 && parts[1] == "tasks":
		s.handleAssistantExecutionTask(w, r, home, auth, parts[2:])
		return true
	case len(parts) == 4 && parts[1] == "sessions" && parts[3] == "staging":
		s.handleAssistantStageUpload(w, r, home, auth, parts[2])
		return true
	case len(parts) == 2 && parts[1] == "sessions":
		s.handleAssistantSessions(w, r, home, auth)
		return true
	case len(parts) == 2 && parts[1] == "status":
		s.handleAssistantStatus(w, r, home, auth)
		return true
	case len(parts) == 2 && parts[1] == "models":
		s.handleAssistantModels(w, r, home, auth)
		return true
	case len(parts) == 2 && parts[1] == "settings":
		s.handleAssistantSettings(w, r, home, auth)
		return true
	case len(parts) == 2 && parts[1] == "logs":
		s.handleAssistantLogs(w, r, home, membership)
		return true
	case len(parts) == 2 && parts[1] == "media-settings":
		s.handleAssistantMediaSettings(w, r, home, membership)
		return true
	case len(parts) == 2 && parts[1] == "media-image":
		s.handleAssistantMediaImage(w, r, home, membership)
		return true
	case len(parts) == 3 && parts[1] == "media-jobs":
		s.handleAssistantMediaJobStatus(w, r, home, membership, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "media-jobs" && parts[3] == "cancel":
		s.handleAssistantMediaJobCancel(w, r, home, membership, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "sessions" && parts[3] == "messages":
		s.handleAssistantSessionMessages(w, r, home, membership, auth, parts[2])
		return true
	case len(parts) == 6 && parts[1] == "sessions" && parts[3] == "attachments" && parts[5] == "discard":
		s.handleAssistantAttachmentDiscard(w, r, home, auth, parts[2], parts[4])
		return true
	case len(parts) == 3 && parts[1] == "sessions":
		s.handleAssistantSession(w, r, home, auth, parts[2])
		return true
	case len(parts) == 3 && parts[1] == "runs" && r.Method == http.MethodGet:
		s.handleAssistantRun(w, r, home, auth, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "runs" && parts[3] == "client-tool-results":
		s.handleAssistantClientToolResults(w, r, home, auth, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "runs" && parts[3] == "confirm":
		s.handleAssistantConfirm(w, r, home, auth, parts[2])
		return true
	case len(parts) == 2 && parts[1] == "calendar-index":
		s.handleAssistantCalendarIndex(w, r, home, auth)
		return true
	default:
		http.NotFound(w, r)
		return true
	}
}

func (s *Server) handleAssistantStatus(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings, err := s.currentAssistantSettings(r.Context(), home.ID, auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := s.assistantStatusWithSettings(r.Context(), auth.User.ID, settings)
	indexStats, err := s.store.AssistantIndexStats(r.Context(), home.ID, auth.User.ID)
	if err != nil {
		s.logger.Warn("assistant index stats unavailable", "home_id", home.ID, "user_id", auth.User.ID, "error_code", "operation_failed")
		indexStats = domain.AssistantIndexStats{
			VectorAvailable: s.store.VectorAvailable(),
			VectorMode:      "unavailable",
		}
		if indexStats.VectorAvailable {
			indexStats.VectorMode = "pgvector"
		}
	}
	statusPayload := map[string]any{
		"execution_versions":   s.assistantExecutionVersions(r.Context(), home.ID, auth.User.ID),
		"execution_mode":       "approved_tools",
		"home_id":              home.ID,
		"provider":             status.Provider,
		"chat_configured":      status.ChatConfigured,
		"embedding_configured": status.EmbeddingConfigured,
		"chat_model":           status.ChatModel,
		"chat_model_default":   status.DefaultChatModel,
		"chat_model_override":  status.ChatModelOverride,
		"chat_model_options":   status.ChatModelOptions,
		"embedding_model":      status.EmbeddingModel,
		"vector_store":         status.VectorStore,
		"index":                indexStats,
	}
	if !status.EmbeddingConfigured || status.EmbeddingModel == "local-hash" {
		statusPayload["embedding_warning"] = "local_hash_fallback"
	}
	writeJSON(w, http.StatusOK, statusPayload)
}

func (s *Server) handleAssistantSessions(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	switch r.Method {
	case http.MethodGet:
		sessions, err := s.store.ListAssistantSessions(r.Context(), home.ID, auth.User.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response := assistantSessionListResponse{Sessions: make([]assistantAPISession, 0, len(sessions))}
		for _, session := range sessions {
			response.Sessions = append(response.Sessions, assistantSessionToAPI(session))
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		now := time.Now().UTC()
		session := domain.AssistantSession{
			ID:            newID("asess"),
			HomeID:        home.ID,
			UserID:        auth.User.ID,
			Title:         "New Conversation",
			LastMessageAt: now,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := s.store.CreateAssistantSession(r.Context(), session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, assistantSessionToAPI(session))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAssistantSession(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, sessionID string) {
	session, err := s.store.GetAssistantSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, assistantSessionToAPI(session))
	case http.MethodDelete:
		if err := s.store.DeleteAssistantSession(r.Context(), session.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true, "id": session.ID})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAssistantAttachmentDiscard(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, sessionID string, clientAttachmentID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, err := s.store.GetAssistantSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		http.NotFound(w, r)
		return
	}
	clientAttachmentID = strings.TrimSpace(clientAttachmentID)
	if clientAttachmentID == "" {
		http.Error(w, "attachment id is required", http.StatusBadRequest)
		return
	}
	if err := s.store.MarkAssistantAttachmentsExpired(r.Context(), session.ID, []string{clientAttachmentID}, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "expired"})
}

func (s *Server) handleAssistantSessionMessages(w http.ResponseWriter, r *http.Request, home domain.Home, membership domain.HomeMembership, auth authContext, sessionID string) {
	session, err := s.store.GetAssistantSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		messages, err := s.store.ListAssistantMessages(r.Context(), sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response := assistantMessageListResponse{Messages: make([]assistantAPIMessage, 0, len(messages))}
		for _, message := range messages {
			response.Messages = append(response.Messages, assistantMessageToAPI(message))
		}
		if run, err := s.store.GetPendingAssistantRun(r.Context(), session.ID); err == nil {
			activeRun := s.assistantRunResponseForSession(r.Context(), session, run)
			response.ActiveRun = &activeRun
		} else if !errors.Is(err, store.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.ExecutionVersion, err = s.store.AssistantSessionExecutionVersion(r.Context(), home.ID, auth.User.ID, session.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		tasks, err := s.store.ListAssistantTasks(r.Context(), home.ID, auth.User.ID, session.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		if len(tasks) > 0 {
			response.ActiveTask, err = s.assistantExecutionSnapshot(r.Context(), tasks[0])
			if err != nil {
				assistantTaskHTTPError(w, r, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		versions := r.Header.Values("X-Hank-Assistant-Execution")
		if len(versions) == 1 && versions[0] == "2" {
			s.handleAssistantExecutionSubmit(w, r, home, auth, session)
			return
		}
		if len(versions) > 1 || (len(versions) == 1 && versions[0] != "1") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "execution_version_unavailable"})
			return
		}
		version, err := s.store.AssistantSessionExecutionVersion(r.Context(), home.ID, auth.User.ID, session.ID)
		if err != nil {
			assistantTaskHTTPError(w, r, err)
			return
		}
		if version != 1 {
			writeJSON(w, 409, map[string]string{"error": "execution_version_required"})
			return
		}
		var body struct {
			Content            string                       `json:"content"`
			Attachments        []assistantMessageAttachment `json:"attachments"`
			ClientCapabilities map[string]bool              `json:"client_capabilities"`
			DeviceContext      struct {
				DeviceID string `json:"device_id"`
				Timezone string `json:"timezone"`
			} `json:"device_context"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content := strings.TrimSpace(body.Content)
		if content == "" && len(body.Attachments) == 0 {
			http.Error(w, "content is required", http.StatusBadRequest)
			return
		}
		if content == "" {
			content = fmt.Sprintf("Uploaded %d attachment(s).", len(body.Attachments))
		}

		runResponse, err := s.processAssistantMessageWithAttachments(r.Context(), home, membership, auth, session, content, body.Attachments, body.DeviceContext.DeviceID, body.DeviceContext.Timezone)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, runResponse)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAssistantRun(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, runID string) {
	run, err := s.store.GetAssistantRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, err := s.store.GetAssistantSession(r.Context(), run.SessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.assistantRunResponseForSession(r.Context(), session, run))
}

func (s *Server) handleAssistantClientToolResults(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run, err := s.store.GetAssistantRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, err := s.store.GetAssistantSession(r.Context(), run.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		http.NotFound(w, r)
		return
	}
	ctx := withAssistantTraceContext(r.Context(), assistantTraceContext{
		HomeID:    home.ID,
		UserID:    auth.User.ID,
		SessionID: session.ID,
		RunID:     run.ID,
		MessageID: run.MessageID,
	})

	var request struct {
		ToolName string                 `json:"tool_name"`
		Result   map[string]interface{} `json:"result"`
		Error    string                 `json:"error"`
		Results  []struct {
			ToolName string                 `json:"tool_name"`
			Result   map[string]interface{} `json:"result"`
			Error    string                 `json:"error"`
		} `json:"results"`
	}
	if err := parseJSON(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.ToolName == "" && len(request.Results) > 0 {
		request.ToolName = request.Results[0].ToolName
		request.Result = request.Results[0].Result
		request.Error = request.Results[0].Error
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.client_tool.result_received",
		Summary: "Received client tool result for a waiting run.",
		Details: traceDetails(map[string]any{
			"tool":       request.ToolName,
			"has_error":  request.Error != "",
			"result_set": len(request.Result) > 0 || len(request.Results) > 0,
		}),
	})

	var pending assistantClientToolRequest
	if err := json.Unmarshal([]byte(run.PendingActionJSON), &pending); err != nil {
		http.Error(w, "run is not waiting for a client tool", http.StatusBadRequest)
		return
	}
	if request.ToolName != pending.ToolName {
		http.Error(w, "client tool does not match pending run", http.StatusBadRequest)
		return
	}

	var content assistantMessageContent
	if strings.TrimSpace(request.Error) != "" {
		content, err = s.finalizeAssistantClientToolErrorRun(ctx, session, request.ToolName, request.Result, request.Error)
	} else {
		content, err = s.finalizeAssistantClientToolRun(ctx, session, run, request.ToolName, request.Result)
	}
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.client_tool.finalize_failed",
			Summary: "Client tool result could not be finalized.",
			Details: traceDetails(map[string]any{
				"tool": request.ToolName,
			}),
		})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	completedAt := time.Now().UTC()
	run.State = assistantStateCompleted
	run.RequiresClientTools = false
	run.PendingActionJSON = ""
	run.CompletedAt = &completedAt
	if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings, err := s.currentAssistantSettings(ctx, home.ID, auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:     "assistant",
		Event:     "assistant.client_tool.completed",
		Summary:   "Client tool result completed the run.",
		MessageID: message.ID,
		Details: traceDetails(map[string]any{
			"tool": request.ToolName,
		}),
	})

	writeJSON(w, http.StatusOK, assistantRunResponse{
		ID:               run.ID,
		State:            run.State,
		AssistantMessage: &message,
	})
}

func (s *Server) handleAssistantConfirm(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run, session, err := s.authorizedAssistantRun(r.Context(), home, auth, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var request struct {
		Approved bool `json:"approved"`
	}
	if err := parseJSON(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !run.RequiresConfirmation {
		response := s.assistantRunResponseForSession(r.Context(), session, run)
		writeJSON(w, http.StatusOK, response)
		return
	}
	ctx := withAssistantTraceContext(r.Context(), assistantTraceContext{
		HomeID:    home.ID,
		UserID:    auth.User.ID,
		SessionID: session.ID,
		RunID:     run.ID,
		MessageID: run.MessageID,
	})
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.confirmation.received",
		Summary: "Received a confirmation response.",
		Details: traceDetails(map[string]any{
			"approved": request.Approved,
		}),
	})

	if !request.Approved {
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
			Text: "Cancelled the pending action. No changes were made.",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(r.Context(), run); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		settings, err := s.currentAssistantSettings(ctx, home.ID, auth.User.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:   "assistant",
			Event:   "assistant.confirmation.cancelled",
			Summary: "User cancelled the pending action.",
		})
		writeJSON(w, http.StatusOK, assistantRunResponse{
			ID:                   run.ID,
			State:                run.State,
			RequiresClientTools:  false,
			RequiresConfirmation: false,
			AssistantMessage:     &message,
		})
		return
	}

	var pending assistantPendingAction
	if err := json.Unmarshal([]byte(run.PendingActionJSON), &pending); err != nil {
		http.Error(w, "run is not waiting for a confirmation action", http.StatusBadRequest)
		return
	}

	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.confirmation.approved",
		Summary: "User approved the pending action.",
		Details: traceDetails(map[string]any{
			"pending_action": pending.Kind,
		}),
	})
	response, err := s.executeConfirmedAssistantAction(ctx, session, run, pending, auth.User.ID)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.confirmed_action.failed",
			Summary: "Approved action failed.",
			Details: traceDetails(map[string]any{
				"pending_action": pending.Kind,
			}),
		})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.confirmed_action.completed",
		Summary: "Approved action completed.",
		Details: traceDetails(map[string]any{
			"pending_action": pending.Kind,
			"state":          response.State,
		}),
	})
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAssistantCalendarIndex(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		DeviceID string `json:"device_id"`
		Timezone string `json:"timezone"`
		Entries  []struct {
			ID              string                 `json:"id"`
			ExternalEventID string                 `json:"external_event_id"`
			CalendarID      string                 `json:"calendar_id"`
			CalendarTitle   string                 `json:"calendar_title"`
			Title           string                 `json:"title"`
			Location        string                 `json:"location"`
			Notes           string                 `json:"notes"`
			StartsAt        time.Time              `json:"starts_at"`
			EndsAt          time.Time              `json:"ends_at"`
			IsAllDay        bool                   `json:"is_all_day"`
			Metadata        map[string]interface{} `json:"metadata"`
		} `json:"entries"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	deviceID := strings.TrimSpace(body.DeviceID)
	if deviceID == "" {
		deviceID = "ios"
	}

	now := time.Now().UTC()
	entries := make([]domain.AssistantCalendarEntry, 0, len(body.Entries))
	for _, item := range body.Entries {
		metadataJSON, _ := json.Marshal(item.Metadata)
		searchText := strings.TrimSpace(strings.Join([]string{item.Title, item.CalendarTitle, item.Location, item.Notes}, " "))
		calendarID := strings.TrimSpace(item.CalendarID)
		if calendarID == "" {
			calendarID = strings.TrimSpace(item.CalendarTitle)
		}
		entries = append(entries, domain.AssistantCalendarEntry{
			ID:              defaultString(strings.TrimSpace(item.ID), newID("acal")),
			HomeID:          home.ID,
			UserID:          auth.User.ID,
			DeviceID:        deviceID,
			ExternalEventID: strings.TrimSpace(item.ExternalEventID),
			CalendarID:      calendarID,
			Title:           strings.TrimSpace(item.Title),
			Location:        strings.TrimSpace(item.Location),
			Notes:           strings.TrimSpace(item.Notes),
			StartsAt:        item.StartsAt,
			EndsAt:          item.EndsAt,
			IsAllDay:        item.IsAllDay,
			SearchText:      searchText,
			MetadataJSON:    string(metadataJSON),
			UpdatedAt:       now,
		})
	}

	if err := s.store.UpsertAssistantCalendarEntries(r.Context(), entries); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.enqueueAssistantIndexJob(r.Context(), home.ID, auth.User.ID, assistantIndexSourceCalendar, "")
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "entry_count": len(entries)})
}

func (s *Server) processAssistantMessage(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, session domain.AssistantSession, content string, deviceID string, timezone string) (assistantRunResponse, error) {
	return s.processAssistantMessageWithAttachments(ctx, home, membership, auth, session, content, nil, deviceID, timezone)
}

func (s *Server) processAssistantMessageWithAttachments(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, session domain.AssistantSession, content string, attachments []assistantMessageAttachment, deviceID string, timezone string) (assistantRunResponse, error) {
	ctx = withAssistantTraceContext(ctx, assistantTraceContext{
		HomeID:    home.ID,
		UserID:    auth.User.ID,
		SessionID: session.ID,
	})
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.message.received",
		Summary: "HankAI received a chat message.",
		Details: traceDetails(map[string]any{
			"attachment_count": len(attachments),
		}),
	})
	settings, err := s.currentAssistantSettings(ctx, home.ID, auth.User.ID)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.settings.failed",
			Summary: "Could not load HankAI settings.",
			Details: traceDetails(map[string]any{"error": err.Error()}),
		})
		return assistantRunResponse{}, err
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.settings.loaded",
		Summary: "Loaded HankAI settings for this run.",
		Details: traceDetails(map[string]any{
			"profile_notes": settings.ProfileNotesEnabled,
			"home_notes":    settings.HomeNotesEnabled,
			"files":         settings.FilesEnabled,
			"calendar":      settings.CalendarEnabled,
			"homeassistant": settings.HomeAssistantEnabled,
			"project_docs":  settings.ProjectDocsEnabled,
			"conversations": settings.ConversationsEnabled,
			"max_context":   settings.MaxContextItems,
		}),
	})
	if strings.TrimSpace(session.Title) == "" || session.Title == "New Conversation" {
		session.Title = assistantSessionTitle(content)
	}
	cleanAttachments := normalizedAssistantMessageAttachments(attachments)
	defaultAttachmentDestination := ""
	if len(cleanAttachments) == 0 {
		stagedAttachments, destination, err := s.reusableAssistantAttachments(ctx, session, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		cleanAttachments = assistantMessageAttachmentsFromRecords(stagedAttachments)
		defaultAttachmentDestination = destination
	}
	userContent := assistantMessageContent{Text: content, Attachments: cleanAttachments}
	userMessage, err := s.persistAssistantMessage(ctx, session, assistantRoleUser, userContent)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.user_message.failed",
			Summary: "Could not persist the user message.",
			Details: traceDetails(map[string]any{"error": err.Error()}),
		})
		return assistantRunResponse{}, err
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:     "assistant",
		Event:     "assistant.user_message.saved",
		Summary:   "Saved the user message.",
		MessageID: userMessage.ID,
		Details: traceDetails(map[string]any{
			"attachment_count": len(cleanAttachments),
		}),
	})
	if err := s.persistAssistantAttachments(ctx, session, auth.User.ID, cleanAttachments); err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.attachments.failed",
			Summary: "Could not persist assistant attachment records.",
			Details: traceDetails(map[string]any{"error": err.Error()}),
		})
		return assistantRunResponse{}, err
	}

	run := domain.AssistantRun{
		ID:                   newID("arun"),
		SessionID:            session.ID,
		MessageID:            userMessage.ID,
		State:                assistantStateCompleted,
		RequiresClientTools:  false,
		RequiresConfirmation: false,
		PendingActionJSON:    "",
		CreatedAt:            time.Now().UTC(),
	}
	trace := assistantTraceContextFrom(ctx)
	trace.RunID = run.ID
	trace.MessageID = userMessage.ID
	ctx = withAssistantTraceContext(ctx, trace)
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.run.created",
		Summary: "Created an assistant run.",
		Details: traceDetails(map[string]any{
			"run_state": run.State,
		}),
	})

	if len(cleanAttachments) > 0 {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:   "assistant",
			Event:   "assistant.attachments.planning",
			Summary: "Checking whether uploaded files need a commit workflow.",
			Details: traceDetails(map[string]any{
				"attachment_count": len(cleanAttachments),
			}),
		})
		if runResponse, handled, err := s.planAttachmentCommit(ctx, home, membership, auth, session, settings, run, cleanAttachments, content, defaultAttachmentDestination); handled || err != nil {
			if err != nil {
				s.recordAssistantTrace(ctx, assistantTraceEvent{
					Level:   "error",
					Scope:   "assistant",
					Event:   "assistant.attachments.plan_failed",
					Summary: "Attachment workflow planning failed.",
					Details: traceDetails(map[string]any{"error": err.Error()}),
				})
			} else {
				s.recordAssistantTrace(ctx, assistantTraceEvent{
					Scope:   "assistant",
					Event:   "assistant.attachments.plan_handled",
					Summary: "Attachment workflow handled this run.",
					Details: traceDetails(map[string]any{
						"state": runResponse.State,
					}),
				})
			}
			return runResponse, err
		}
	}

	if pending, ok := s.planCalendarTool(content, timezone, deviceID); ok {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:   "assistant",
			Event:   "assistant.calendar.plan_matched",
			Summary: "Calendar creation parser matched the prompt.",
			Details: traceDetails(map[string]any{
				"requires_confirmation": pending.requiresConfirmation,
			}),
		})
		if !settings.CalendarEnabled {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: "Calendar access is turned off in HankAI settings.",
			})
			if err != nil {
				return assistantRunResponse{}, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, err
			}
			return assistantRunResponse{
				ID:               run.ID,
				State:            run.State,
				AssistantMessage: &message,
			}, nil
		}
		if pending.requiresConfirmation {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: pending.confirmationMessage,
			})
			if err != nil {
				return assistantRunResponse{}, err
			}
			pendingAction := assistantPendingAction{
				Kind: "calendar_create",
				CalendarCreate: &assistantPendingCalendarCreate{
					ToolRequest:  pending.request,
					Title:        pending.title,
					DateText:     pending.dateText,
					Confirmation: pending.confirmationMessage,
				},
			}
			payload, err := json.Marshal(pendingAction)
			if err != nil {
				return assistantRunResponse{}, err
			}
			run.State = assistantStateWaitingConfirm
			run.MessageID = message.ID
			run.RequiresConfirmation = true
			run.PendingActionJSON = string(payload)
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, err
			}
			if err := s.store.TouchAssistantSession(ctx, session.ID, session.Title, run.CreatedAt); err != nil {
				return assistantRunResponse{}, err
			}
			s.recordAssistantTrace(ctx, assistantTraceEvent{
				Scope:   "assistant",
				Event:   "assistant.confirmation.waiting",
				Summary: "Run is waiting for user confirmation.",
				Details: traceDetails(map[string]any{
					"pending_action": "calendar_create",
				}),
			})
			return assistantRunResponse{
				ID:                   run.ID,
				State:                run.State,
				RequiresConfirmation: true,
				AssistantMessage:     &message,
				PendingActionSummary: assistantPendingActionSummaryFromAction(pendingAction),
			}, nil
		}

		payload, err := json.Marshal(pending.request)
		if err != nil {
			return assistantRunResponse{}, err
		}
		run.State = assistantStateWaitingClientTool
		run.RequiresClientTools = true
		run.PendingActionJSON = string(payload)
		if err := s.store.CreateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.store.TouchAssistantSession(ctx, session.ID, session.Title, run.CreatedAt); err != nil {
			return assistantRunResponse{}, err
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:   "assistant",
			Event:   "assistant.client_tool.waiting",
			Summary: "Run is waiting for a client-side tool.",
			Details: traceDetails(map[string]any{
				"tool": pending.request.ToolName,
			}),
		})
		return assistantRunResponse{
			ID:                  run.ID,
			State:               run.State,
			RequiresClientTools: true,
			ClientToolRequest:   &pending.request,
		}, nil
	}

	assistantContent, err := s.generateAssistantResponseForSession(ctx, home, membership, auth, settings, &session, content, deviceID, timezone)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.generate.failed",
			Summary: "Assistant workflow execution failed.",
			Details: traceDetails(map[string]any{"error": err.Error()}),
		})
		return assistantRunResponse{}, err
	}
	message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantContent)
	if err != nil {
		return assistantRunResponse{}, err
	}

	if pendingAction := assistantPendingActionFromContent(assistantContent); pendingAction != nil {
		payload, err := json.Marshal(pendingAction)
		if err != nil {
			return assistantRunResponse{}, err
		}
		run.State = assistantStateWaitingConfirm
		run.MessageID = message.ID
		run.RequiresConfirmation = true
		run.PendingActionJSON = string(payload)
		if err := s.store.CreateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.store.TouchAssistantSession(ctx, session.ID, session.Title, run.CreatedAt); err != nil {
			return assistantRunResponse{}, err
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:     "assistant",
			Event:     "assistant.confirmation.waiting",
			Summary:   "Run is waiting for user confirmation.",
			MessageID: message.ID,
			Details: traceDetails(map[string]any{
				"pending_action": pendingAction.Kind,
			}),
		})
		return assistantRunResponse{
			ID:                   run.ID,
			State:                run.State,
			RequiresConfirmation: true,
			AssistantMessage:     &message,
			PendingActionSummary: assistantPendingActionSummaryFromAction(*pendingAction),
			Diagnostics:          message.Diagnostics,
		}, nil
	}

	completedAt := time.Now().UTC()
	run.MessageID = message.ID
	run.CompletedAt = &completedAt
	if err := s.store.CreateAssistantRun(ctx, run); err != nil {
		return assistantRunResponse{}, err
	}
	if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
		return assistantRunResponse{}, err
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:     "assistant",
		Event:     "assistant.run.completed",
		Summary:   "Assistant run completed.",
		MessageID: message.ID,
		Details: traceDetails(map[string]any{
			"card_count": len(message.Cards),
		}),
	})

	return assistantRunResponse{
		ID:               run.ID,
		State:            run.State,
		AssistantMessage: &message,
		Diagnostics:      message.Diagnostics,
	}, nil
}

func normalizedAssistantMessageAttachments(attachments []assistantMessageAttachment) []assistantMessageAttachment {
	clean := make([]assistantMessageAttachment, 0, len(attachments))
	seen := map[string]bool{}
	for _, attachment := range attachments {
		clientID := strings.TrimSpace(attachment.ClientAttachmentID)
		if clientID == "" || seen[clientID] {
			continue
		}
		seen[clientID] = true
		filename := strings.TrimSpace(attachment.Filename)
		if filename == "" {
			filename = "Attachment"
		}
		contentType := strings.TrimSpace(attachment.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		kind := strings.TrimSpace(attachment.Kind)
		if kind == "" {
			kind = assistantAttachmentKind(contentType)
		}
		clean = append(clean, assistantMessageAttachment{
			ClientAttachmentID: clientID,
			Filename:           filename,
			ContentType:        contentType,
			SizeBytes:          attachment.SizeBytes,
			ChecksumSHA256:     strings.TrimSpace(attachment.ChecksumSHA256),
			Kind:               kind,
		})
	}
	return clean
}

func assistantAttachmentKind(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.Contains(contentType, "pdf"):
		return "pdf"
	default:
		return "document"
	}
}

func (s *Server) persistAssistantAttachments(ctx context.Context, session domain.AssistantSession, userID string, attachments []assistantMessageAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	now := time.Now().UTC()
	records := make([]domain.AssistantAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		records = append(records, domain.AssistantAttachment{
			ID:                 newID("aat"),
			SessionID:          session.ID,
			UserID:             userID,
			ClientAttachmentID: attachment.ClientAttachmentID,
			Filename:           attachment.Filename,
			ContentType:        attachment.ContentType,
			Kind:               attachment.Kind,
			SizeBytes:          attachment.SizeBytes,
			ChecksumSHA256:     attachment.ChecksumSHA256,
			Status:             "staged",
			CreatedAt:          now,
			UpdatedAt:          now,
		})
	}
	return s.store.UpsertAssistantAttachments(ctx, records)
}

func (s *Server) reusableAssistantAttachments(ctx context.Context, session domain.AssistantSession, prompt string) ([]domain.AssistantAttachment, string, error) {
	destination := attachmentDestinationKind(prompt)
	clarificationDestination, err := s.latestAttachmentClarificationDestination(ctx, session.ID)
	if err != nil {
		return nil, "", err
	}
	if destination == "" {
		destination = clarificationDestination
	}
	if destination == "" && clarificationDestination == "" {
		return nil, "", nil
	}
	attachments, err := s.store.ListStagedAssistantAttachments(ctx, session.ID)
	if err != nil {
		return nil, "", err
	}
	return attachments, destination, nil
}

func (s *Server) latestAttachmentClarificationDestination(ctx context.Context, sessionID string) (string, error) {
	messages, err := s.store.ListAssistantMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != assistantRoleAssistant {
			continue
		}
		var content assistantMessageContent
		if err := json.Unmarshal([]byte(messages[i].ContentJSON), &content); err != nil || content.Meta == nil {
			continue
		}
		clarification, _ := content.Meta["attachment_clarification"].(bool)
		if !clarification {
			continue
		}
		destination, _ := content.Meta["attachment_clarification_destination"].(string)
		return strings.TrimSpace(destination), nil
	}
	return "", nil
}

func assistantMessageAttachmentsFromRecords(records []domain.AssistantAttachment) []assistantMessageAttachment {
	attachments := make([]assistantMessageAttachment, 0, len(records))
	for _, record := range records {
		attachments = append(attachments, assistantMessageAttachment{
			ClientAttachmentID: record.ClientAttachmentID,
			Filename:           record.Filename,
			ContentType:        record.ContentType,
			SizeBytes:          record.SizeBytes,
			ChecksumSHA256:     record.ChecksumSHA256,
			Kind:               record.Kind,
		})
	}
	return attachments
}

func (s *Server) planAttachmentCommit(
	ctx context.Context,
	home domain.Home,
	_ domain.HomeMembership,
	auth authContext,
	session domain.AssistantSession,
	settings domain.AssistantSettings,
	run domain.AssistantRun,
	attachments []assistantMessageAttachment,
	prompt string,
	defaultDestination string,
) (assistantRunResponse, bool, error) {
	destination := attachmentDestinationKind(prompt)
	if destination == "" {
		destination = defaultDestination
	}
	if destination == "" {
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
			Text: "Where should I store the uploaded file: a note, or a folder in File Server?",
			Meta: map[string]interface{}{
				"attachment_clarification": true,
			},
		})
		if err != nil {
			return assistantRunResponse{}, true, err
		}
		completedAt := time.Now().UTC()
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.CreateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, true, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, true, err
		}
		return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
	}

	attachmentIDs := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		attachmentIDs = append(attachmentIDs, attachment.ClientAttachmentID)
	}
	fileLabel := assistantAttachmentListLabel(attachments)

	switch destination {
	case "note_attachment":
		if !settings.ProfileNotesEnabled && !settings.HomeNotesEnabled {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: "Notes access is turned off in HankAI settings.",
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}
		query := attachmentNoteQuery(prompt)
		if query == "" {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: "Which note should I attach the uploaded file to?",
				Meta: map[string]interface{}{
					"attachment_clarification":             true,
					"attachment_clarification_destination": "note_attachment",
				},
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}
		notes, err := s.assistantVisibleNotes(ctx, home.ID, auth.User.ID, settings)
		if err != nil {
			return assistantRunResponse{}, true, err
		}
		ranked := rankScoredNotes(notes, query)
		if len(ranked) == 0 || needsNoteAppendConfirmation(ranked) {
			messageText := fmt.Sprintf("I need a clearer note destination for `%s` before I can store %s.", query, fileLabel)
			if len(ranked) > 0 {
				messageText = fmt.Sprintf("I found more than one likely note for `%s`. Which note should receive %s?", query, fileLabel)
			}
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: messageText,
				Meta: map[string]interface{}{
					"attachment_clarification":             true,
					"attachment_clarification_destination": "note_attachment",
				},
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}

		target := ranked[0].Note
		scope := "profile"
		scopeLabel := "Personal note"
		if target.HomeID != "" {
			scope = "home"
			scopeLabel = "Shared Home note"
		}
		arguments := map[string]interface{}{
			"attachment_ids":   attachmentIDs,
			"destination_kind": "note_attachment",
			"note_scope":       scope,
			"note_id":          target.NoteID,
			"note_title":       target.Title,
			"conflict_mode":    "rename",
		}
		pending := assistantPendingAction{
			Kind: "attachment_commit",
			AttachmentCommit: &assistantPendingAttachmentCommit{
				ToolRequest: assistantClientToolRequest{
					ToolName:  "attachments.commit",
					Arguments: arguments,
				},
				AttachmentIDs:   attachmentIDs,
				DestinationKind: "note_attachment",
				TargetNoteID:    target.ID,
				TargetNoteKey:   target.NoteID,
				TargetTitle:     target.Title,
				TargetScope:     scopeLabel,
				ConflictMode:    "rename",
				Confirmation:    fmt.Sprintf("Confirm attaching %s to `%s`.", fileLabel, target.Title),
			},
		}
		return s.createAttachmentConfirmationRun(ctx, session, run, pending, fmt.Sprintf("I can attach %s to `%s`. Confirm before I continue.", fileLabel, target.Title))

	case "smb":
		if !settings.FilesEnabled {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: "File Server access is turned off in HankAI settings.",
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}
		targetPath := attachmentSMBPath(prompt)
		if targetPath == "" && defaultDestination == "smb" {
			targetPath = cleanAttachmentSMBPath(prompt)
		}
		if targetPath == "" {
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: "Which File Server folder should receive the uploaded file?",
				Meta: map[string]interface{}{
					"attachment_clarification":             true,
					"attachment_clarification_destination": "smb",
				},
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}
		resolvedPath, sourceID, ambiguousMatches, err := s.resolveAssistantSMBTargetPath(ctx, home.ID, targetPath)
		if err != nil {
			return assistantRunResponse{}, true, err
		}
		if resolvedPath == "" {
			messageText := fmt.Sprintf("I need a clearer File Server folder destination for `%s` before I can store %s.", targetPath, fileLabel)
			if len(ambiguousMatches) > 0 {
				messageText = fmt.Sprintf("I found more than one File Server folder matching `%s`. Which folder should receive %s?", targetPath, fileLabel)
			}
			message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{
				Text: messageText,
				Meta: map[string]interface{}{
					"attachment_clarification":             true,
					"attachment_clarification_destination": "smb",
				},
			})
			if err != nil {
				return assistantRunResponse{}, true, err
			}
			completedAt := time.Now().UTC()
			run.MessageID = message.ID
			run.CompletedAt = &completedAt
			if err := s.store.CreateAssistantRun(ctx, run); err != nil {
				return assistantRunResponse{}, true, err
			}
			if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
				return assistantRunResponse{}, true, err
			}
			return assistantRunResponse{ID: run.ID, State: run.State, AssistantMessage: &message}, true, nil
		}
		targetPath = resolvedPath
		arguments := map[string]interface{}{
			"attachment_ids":   attachmentIDs,
			"destination_kind": "smb",
			"target_path":      targetPath,
			"conflict_mode":    "rename",
		}
		if sourceID != "" {
			arguments["source_id"] = sourceID
		}
		pending := assistantPendingAction{
			Kind: "attachment_commit",
			AttachmentCommit: &assistantPendingAttachmentCommit{
				ToolRequest: assistantClientToolRequest{
					ToolName:  "attachments.commit",
					Arguments: arguments,
				},
				AttachmentIDs:   attachmentIDs,
				DestinationKind: "smb",
				SourceID:        sourceID,
				TargetPath:      targetPath,
				ConflictMode:    "rename",
				Confirmation:    fmt.Sprintf("Confirm storing %s in `%s`.", fileLabel, targetPath),
			},
		}
		return s.createAttachmentConfirmationRun(ctx, session, run, pending, fmt.Sprintf("I can store %s in `%s` on File Server. Confirm before I continue.", fileLabel, targetPath))
	default:
		return assistantRunResponse{}, false, nil
	}
}

func (s *Server) createAttachmentConfirmationRun(ctx context.Context, session domain.AssistantSession, run domain.AssistantRun, pending assistantPendingAction, text string) (assistantRunResponse, bool, error) {
	message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, assistantMessageContent{Text: text})
	if err != nil {
		return assistantRunResponse{}, true, err
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return assistantRunResponse{}, true, err
	}
	run.State = assistantStateWaitingConfirm
	run.MessageID = message.ID
	run.RequiresConfirmation = true
	run.PendingActionJSON = string(payload)
	if err := s.store.CreateAssistantRun(ctx, run); err != nil {
		return assistantRunResponse{}, true, err
	}
	if err := s.store.TouchAssistantSession(ctx, session.ID, session.Title, run.CreatedAt); err != nil {
		return assistantRunResponse{}, true, err
	}
	return assistantRunResponse{
		ID:                   run.ID,
		State:                run.State,
		RequiresConfirmation: true,
		AssistantMessage:     &message,
		PendingActionSummary: assistantPendingActionSummaryFromAction(pending),
	}, true, nil
}

func (s *Server) resolveAssistantSMBTargetPath(ctx context.Context, homeID string, targetPath string) (string, string, []domain.AssistantFileIndex, error) {
	targetPath = cleanAttachmentSMBPath(targetPath)
	if targetPath == "" {
		return "", "", nil, nil
	}
	if strings.Contains(targetPath, "/") {
		return targetPath, "", nil, nil
	}
	matches, err := s.store.SearchAssistantFileDirectories(ctx, homeID, targetPath, 6)
	if err != nil {
		return "", "", nil, err
	}
	if len(matches) == 0 {
		return "", "", nil, nil
	}
	exact := make([]domain.AssistantFileIndex, 0, len(matches))
	normalizedTarget := strings.Trim(strings.ToLower(targetPath), "/")
	for _, match := range matches {
		if strings.EqualFold(match.Name, targetPath) || strings.EqualFold(strings.Trim(match.Path, "/"), normalizedTarget) {
			exact = append(exact, match)
		}
	}
	if len(exact) == 1 {
		return strings.Trim(exact[0].Path, "/"), strings.TrimSpace(exact[0].ServiceProfileID), nil, nil
	}
	if len(exact) > 1 {
		return "", "", exact, nil
	}
	if len(matches) == 1 {
		return strings.Trim(matches[0].Path, "/"), strings.TrimSpace(matches[0].ServiceProfileID), nil, nil
	}
	return "", "", matches, nil
}

func (s *Server) persistAssistantMessage(ctx context.Context, session domain.AssistantSession, role string, content assistantMessageContent) (assistantAPIMessage, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return assistantAPIMessage{}, err
	}
	message := domain.AssistantMessage{
		ID:          newID("amsg"),
		SessionID:   session.ID,
		Role:        role,
		Status:      assistantStateCompleted,
		ContentJSON: string(encoded),
		ModelName:   assistantModelName,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.store.CreateAssistantMessage(ctx, message); err != nil {
		return assistantAPIMessage{}, err
	}
	return assistantMessageToAPI(message), nil
}

func (s *Server) touchAssistantSessionAndMemory(ctx context.Context, session domain.AssistantSession, settings domain.AssistantSettings, updatedAt time.Time) error {
	if err := s.store.TouchAssistantSession(ctx, session.ID, session.Title, updatedAt); err != nil {
		return err
	}
	settings = normalizeAssistantSettings(settings)
	if !settings.ConversationsEnabled {
		return nil
	}
	session.LastMessageAt = updatedAt
	session.UpdatedAt = updatedAt
	if err := s.indexAssistantConversation(ctx, session, session.UserID, settings); err != nil {
		s.logger.Warn("assistant conversation indexing failed", "session_id", session.ID, "error_code", "operation_failed")
	}
	return nil
}

func (s *Server) generateAssistantResponse(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	return s.generateAssistantResponseForSession(ctx, home, membership, auth, settings, nil, prompt, "", "")
}

func (s *Server) generateAssistantResponseForSession(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settings domain.AssistantSettings, session *domain.AssistantSession, prompt string, deviceID string, timezone string) (assistantMessageContent, error) {
	settings = normalizeAssistantSettings(settings)
	tool, intent := s.resolveAssistantTool(ctx, home, membership, prompt)
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.tool.resolved",
		Summary: "Matched the prompt to a HankAI tool.",
		Details: traceDetails(map[string]any{
			"tool":   tool.Kind,
			"intent": intent.Kind,
		}),
	})
	if session != nil {
		if selected, ok := s.resolvePreviousMediaSelection(ctx, session.ID, prompt); ok {
			intent = assistantIntent{Kind: assistantIntentMediaSelection, MediaSelection: &selected}
			for _, candidate := range assistantToolRegistry {
				if candidate.Kind == assistantIntentMediaSearch {
					tool = candidate
					break
				}
			}
			s.recordAssistantTrace(ctx, assistantTraceEvent{
				Scope:   "assistant",
				Event:   "assistant.media.selection_resolved",
				Summary: "Resolved the reply against previous media result cards.",
			})
		}
	}
	runtime := assistantToolRuntime{
		Home:       home,
		Membership: membership,
		Auth:       auth,
		Settings:   settings,
		Prompt:     prompt,
		Session:    session,
		DeviceID:   deviceID,
		Timezone:   timezone,
	}
	if session != nil {
		if content, handled, err := s.resolvePreviousCardFollowup(ctx, runtime, intent); handled || err != nil {
			if err != nil {
				return assistantMessageContent{}, err
			}
			switch {
			case len(content.Cards) > 0 && content.Cards[0].Kind == "calendar":
				action := assistantIntentCalendarSearch
				if pending := assistantPendingActionFromContent(content); pending != nil && pending.Kind == "calendar_update" {
					action = assistantIntentCalendarUpdate
				} else if pending != nil && pending.Kind == "calendar_delete" {
					action = assistantIntentCalendarDelete
				}
				attachAssistantDiagnostics(&content, assistantTool{Kind: action}, assistantIntent{Kind: action})
			case len(content.Cards) > 0 && content.Cards[0].Kind == "file":
				attachAssistantDiagnostics(&content, assistantTool{Kind: assistantIntentFilesListFolder}, assistantIntent{Kind: assistantIntentFilesListFolder})
			}
			return content, nil
		}
	}
	if tool.Kind == assistantIntentGeneral {
		if plannedTool, plannedIntent, ok := s.resolveAssistantToolWithLocalModel(ctx, settings, auth.User.ID, prompt); ok {
			tool = plannedTool
			intent = plannedIntent
			s.recordAssistantTrace(ctx, assistantTraceEvent{
				Scope:   "assistant",
				Event:   "assistant.tool.local_planner_selected",
				Summary: "Local model selected a more specific HankAI tool.",
				Details: traceDetails(map[string]any{
					"tool":   tool.Kind,
					"intent": intent.Kind,
				}),
			})
		}
	}
	indexStartedAt := time.Now()
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.index.refresh_start",
		Summary: "Refreshing enabled HankAI context before tool execution.",
		Details: traceDetails(map[string]any{
			"tool":   tool.Kind,
			"intent": intent.Kind,
		}),
	})
	s.refreshAssistantIndex(ctx, runtime)
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.index.refresh_done",
		Summary: "Context refresh finished.",
		Details: traceDetails(map[string]any{
			"elapsed_ms": time.Since(indexStartedAt).Milliseconds(),
		}),
	})
	if tool.Execute == nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.tool.missing_executor",
			Summary: "Matched tool has no executor.",
			Details: traceDetails(map[string]any{
				"tool": tool.Kind,
			}),
		})
		return assistantMessageContent{
			Text: "This HankAI tool is registered but does not have an executor yet.",
		}, nil
	}
	toolStartedAt := time.Now()
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.tool.execute_start",
		Summary: "Executing the matched HankAI tool.",
		Details: traceDetails(map[string]any{
			"tool":   tool.Kind,
			"intent": intent.Kind,
		}),
	})
	content, err := tool.Execute(ctx, s, runtime, intent)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "assistant",
			Event:   "assistant.tool.execute_failed",
			Summary: "HankAI tool execution failed.",
			Details: traceDetails(map[string]any{
				"tool":       tool.Kind,
				"intent":     intent.Kind,
				"elapsed_ms": time.Since(toolStartedAt).Milliseconds(),
			}),
		})
		return assistantMessageContent{}, err
	}
	attachAssistantDiagnostics(&content, tool, intent)
	pendingKind := ""
	if pending := assistantPendingActionFromContent(content); pending != nil {
		pendingKind = pending.Kind
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.tool.execute_done",
		Summary: "HankAI tool execution finished.",
		Details: traceDetails(map[string]any{
			"tool":           tool.Kind,
			"intent":         intent.Kind,
			"card_count":     len(content.Cards),
			"pending_action": pendingKind,
			"elapsed_ms":     time.Since(toolStartedAt).Milliseconds(),
		}),
	})
	return content, nil
}

func (s *Server) answerNoteListPrompt(ctx context.Context, home domain.Home, auth authContext, settings domain.AssistantSettings) (assistantMessageContent, error) {
	notes, err := s.assistantVisibleNotes(ctx, home.ID, auth.User.ID, settings)
	if err != nil {
		return assistantMessageContent{}, err
	}
	notes = uniqueAssistantNotes(notes)
	if len(notes) == 0 {
		return assistantMessageContent{
			Text: "I couldn't find any notes you can access in Hank yet.",
		}, nil
	}
	sort.Slice(notes, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(notes[i].Title))
		right := strings.ToLower(strings.TrimSpace(notes[j].Title))
		if left == right {
			return notes[i].UpdatedAt.After(notes[j].UpdatedAt)
		}
		return left < right
	})

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("You have access to %d notes:", len(notes)))
	cards := make([]assistantResultCard, 0, len(notes))
	for _, note := range notes {
		title := firstNonBlank(note.Title, note.NoteID, "Untitled Note")
		builder.WriteString("\n- ")
		builder.WriteString(title)
		builder.WriteString(" (")
		builder.WriteString(noteAccessLabel(note))
		builder.WriteString(")")
		cards = append(cards, assistantResultCard{
			Kind:        "note",
			Title:       title,
			Summary:     notePreview(note.Content),
			ActionTitle: "Open in Notes",
			NoteID:      note.NoteID,
			SearchText:  title,
		})
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func (s *Server) answerNoteSearchPrompt(ctx context.Context, home domain.Home, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	notes, err := s.assistantVisibleNotes(ctx, home.ID, auth.User.ID, settings)
	if err != nil {
		return assistantMessageContent{}, err
	}
	query := noteSearchQuery(prompt)
	results := rankNotes(notes, query)
	if len(results) == 0 {
		return assistantMessageContent{
			Text: "I couldn't find a matching note in Hank yet. Try the note title or ask me to add something to a specific list.",
		}, nil
	}

	top := results[0]
	if assistantPromptWantsAll(strings.ToLower(prompt)) && len(results) > 1 {
		limit := min(len(results), 12)
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("I found %d matching notes for `%s`:", len(results), query))
		cards := make([]assistantResultCard, 0, limit)
		for _, note := range results[:limit] {
			title := firstNonBlank(note.Title, note.NoteID, "Untitled Note")
			builder.WriteString("\n- ")
			builder.WriteString(title)
			builder.WriteString(" (")
			builder.WriteString(noteAccessLabel(note))
			builder.WriteString(")")
			cards = append(cards, assistantResultCard{
				Kind:        "note",
				Title:       title,
				Summary:     notePreview(note.Content),
				ActionTitle: "Open in Notes",
				NoteID:      note.NoteID,
				SearchText:  query,
			})
		}
		if remaining := len(results) - limit; remaining > 0 {
			builder.WriteString(fmt.Sprintf("\n- and %d more", remaining))
		}
		return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
	}

	return assistantMessageContent{
		Text: fmt.Sprintf("I found `%s` in Notes.", top.Title),
		Cards: []assistantResultCard{
			{
				Kind:        "note",
				Title:       top.Title,
				Summary:     notePreview(top.Content),
				ActionTitle: "Open in Notes",
				NoteID:      top.NoteID,
				SearchText:  query,
			},
		},
	}, nil
}

func (s *Server) answerAppendNotePrompt(ctx context.Context, home domain.Home, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	itemText, noteHint := extractAppendIntent(prompt)
	if noteHint == "" {
		return s.answerNoteSearchPrompt(ctx, home, auth, settings, prompt)
	}

	notes, err := s.assistantVisibleNotes(ctx, home.ID, auth.User.ID, settings)
	if err != nil {
		return assistantMessageContent{}, err
	}
	matchHint := noteSearchQuery(noteHint)
	ranked := rankScoredNotes(notes, matchHint)
	if len(ranked) == 0 {
		return assistantMessageContent{
			Text: fmt.Sprintf("I couldn't find a note matching `%s`.", matchHint),
		}, nil
	}

	target := ranked[0].Note
	if needsNoteAppendConfirmation(ranked) {
		targetScope := "Personal note"
		if target.HomeID != "" {
			targetScope = "Shared Home note"
		}
		return assistantMessageContent{
			Text: fmt.Sprintf("I found more than one likely note for `%s`. Confirm before I add `%s` to `%s`.", matchHint, itemText, target.Title),
			Cards: []assistantResultCard{
				{
					Kind:        "note",
					Title:       target.Title,
					Summary:     notePreview(target.Content),
					ActionTitle: "Review in Notes",
					NoteID:      target.NoteID,
					SearchText:  matchHint,
				},
			},
			Meta: map[string]interface{}{
				"pending_action": assistantPendingAction{
					Kind: "note_append",
					NoteAppend: &assistantPendingNoteAppend{
						TargetNoteID:  target.ID,
						TargetNoteKey: target.NoteID,
						TargetTitle:   target.Title,
						TargetScope:   targetScope,
						AppendedText:  itemText,
						MatchHint:     matchHint,
						Confirmation:  fmt.Sprintf("Confirm adding `%s` to `%s`.", itemText, target.Title),
					},
				},
			},
		}, nil
	}

	newContent := appendAssistantNoteText(target.Content, itemText)
	revision, checksum, err := revisionAndChecksum(newContent, target.PageType, target.BoardJSON)
	if err != nil {
		return assistantMessageContent{}, err
	}
	target.Content = newContent
	target.BodyMarkdown = newContent
	if target.BodyFormat == "" {
		target.BodyFormat = "markdown"
	}
	target.Revision = revision
	target.Checksum = checksum
	target.UpdatedAt = time.Now().UTC()
	target.UpdatedBy = auth.User.ID
	if err := s.store.UpsertUserNote(ctx, target); err != nil {
		return assistantMessageContent{}, err
	}
	s.enqueueAssistantIndexJob(ctx, home.ID, auth.User.ID, assistantNoteSourceType(target), target.NoteID)

	return assistantMessageContent{
		Text: fmt.Sprintf("Added `%s` to `%s`.", itemText, target.Title),
		Cards: []assistantResultCard{
			{
				Kind:        "note",
				Title:       target.Title,
				Summary:     notePreview(newContent),
				ActionTitle: "Open in Notes",
				NoteID:      target.NoteID,
				SearchText:  itemText,
			},
		},
	}, nil
}

func (s *Server) answerFilePrompt(ctx context.Context, home domain.Home, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	settings = normalizeAssistantSettings(settings)
	query := fileQuery(prompt)
	queryEmbedding, embeddingModel, _ := s.embedAssistantText(ctx, "", query)
	wantsAll := assistantPromptWantsAll(strings.ToLower(prompt))
	contextLimit := 5
	if wantsAll {
		contextLimit = 25
	}
	if contexts, err := s.store.SearchAssistantContext(ctx, home.ID, "", query, queryEmbedding, embeddingModel, contextLimit); err == nil {
		fileCards := make([]assistantResultCard, 0, min(len(contexts), 12))
		for _, contextItem := range contexts {
			if contextItem.SourceType == "file" && assistantSettingsAllowSource(settings, contextItem.SourceType) {
				fileCards = append(fileCards, assistantResultCardFromContext(contextItem))
			}
			if !wantsAll && len(fileCards) >= 1 {
				break
			}
			if len(fileCards) >= 12 {
				break
			}
		}
		if len(fileCards) > 0 {
			if wantsAll {
				return assistantMessageContent{
					Text:  fmt.Sprintf("I found %d SMB matches for `%s`.", len(fileCards), query),
					Cards: fileCards,
				}, nil
			}
			return assistantMessageContent{
				Text:  fmt.Sprintf("I found the closest SMB match for `%s`.", query),
				Cards: fileCards[:1],
			}, nil
		}
	}
	results, err := s.searchFiles(ctx, home.ID, query)
	if err != nil {
		return assistantMessageContent{
			Text: "I couldn't search SMB files because the primary Hank Agent is offline right now.",
		}, nil
	}
	if len(results) == 0 {
		return assistantMessageContent{
			Text: fmt.Sprintf("I couldn't find an SMB file or folder matching `%s`.", query),
		}, nil
	}

	best := results[0]
	if wantsAll && len(results) > 1 {
		limit := min(len(results), 12)
		cards := make([]assistantResultCard, 0, limit)
		for _, item := range results[:limit] {
			cards = append(cards, assistantResultCard{
				Kind:        "file",
				Title:       item.Name,
				Summary:     item.Path,
				ActionTitle: "Open in File Server",
				SourceID:    item.SourceID,
				Path:        item.Path,
				IsDirectory: item.IsDirectory,
			})
		}
		return assistantMessageContent{
			Text:  fmt.Sprintf("I found %d SMB matches for `%s`.", len(results), query),
			Cards: cards,
		}, nil
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I found the closest SMB match for `%s`.", query),
		Cards: []assistantResultCard{
			{
				Kind:        "file",
				Title:       best.Name,
				Summary:     best.Path,
				ActionTitle: "Open in File Server",
				SourceID:    best.SourceID,
				Path:        best.Path,
				IsDirectory: best.IsDirectory,
			},
		},
	}, nil
}

func (s *Server) answerHomeAssistantPrompt(ctx context.Context, home domain.Home, prompt string) (assistantMessageContent, error) {
	envelope, err := s.sendAgentCommand(ctx, home.ID, "homeassistant.fetch_states", map[string]any{})
	if err != nil || envelope.Error != nil {
		return assistantMessageContent{
			Text: "I couldn't reach Home Assistant through the primary Hank Agent right now.",
		}, nil
	}
	payload, err := protocol.DecodePayload[protocol.HomeAssistantFetchStatesResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if len(payload.States) == 0 {
		return assistantMessageContent{
			Text: "Home Assistant did not return any entities yet.",
		}, nil
	}

	query := parseHomeAssistantQuery(prompt)
	matches := matchingHomeAssistantStates(payload.States, query)
	if len(matches) == 0 {
		if query.OnlyOn {
			return assistantMessageContent{
				Text: fmt.Sprintf("I checked Home Assistant, but I did not find any entities matching `%s` that are on.", query.Display),
			}, nil
		}
		return assistantMessageContent{
			Text: fmt.Sprintf("I checked Home Assistant, but I did not find any entities matching `%s`.", query.Display),
		}, nil
	}

	limit := min(len(matches), 12)
	if query.WantsAll {
		limit = min(len(matches), 50)
	}
	var builder strings.Builder
	if query.OnlyOn {
		builder.WriteString("These Home Assistant entities are on:")
	} else if query.Display != "" && query.Display != "entities" {
		builder.WriteString(fmt.Sprintf("I found these Home Assistant entities matching `%s`:", query.Display))
	} else {
		builder.WriteString("I found these Home Assistant entities:")
	}
	for _, state := range matches[:limit] {
		builder.WriteString("\n- ")
		builder.WriteString(homeAssistantStateLabel(state))
		builder.WriteString(": ")
		builder.WriteString(strings.TrimSpace(state.State))
	}
	if remaining := len(matches) - limit; remaining > 0 {
		builder.WriteString(fmt.Sprintf("\n- and %d more", remaining))
	}
	cards := make([]assistantResultCard, 0, limit)
	for _, state := range matches[:limit] {
		cards = append(cards, assistantResultCardFromHomeAssistantState(state))
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

type assistantHomeAssistantControlRequest struct {
	Service        string
	Target         string
	AllowedDomains []string
	HighImpact     bool
}

type assistantHomeAssistantControlCandidate struct {
	State protocol.HomeAssistantState
	Score int
}

func parseHomeAssistantControlPrompt(prompt string) (assistantHomeAssistantControlRequest, bool) {
	value := normalizeHomeAssistantActionPrompt(prompt)
	if value == "" {
		return assistantHomeAssistantControlRequest{}, false
	}

	type action struct {
		Prefix         string
		Service        string
		AllowedDomains []string
		HighImpact     bool
	}
	actions := []action{
		{Prefix: "turn on ", Service: "turn_on", AllowedDomains: []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"}},
		{Prefix: "switch on ", Service: "turn_on", AllowedDomains: []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"}},
		{Prefix: "turn off ", Service: "turn_off", AllowedDomains: []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"}},
		{Prefix: "switch off ", Service: "turn_off", AllowedDomains: []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"}},
		{Prefix: "open ", Service: "open_cover", AllowedDomains: []string{"cover"}, HighImpact: true},
		{Prefix: "close ", Service: "close_cover", AllowedDomains: []string{"cover"}},
		{Prefix: "unlock ", Service: "unlock", AllowedDomains: []string{"lock"}, HighImpact: true},
		{Prefix: "lock ", Service: "lock", AllowedDomains: []string{"lock"}},
		{Prefix: "run ", Service: "turn_on", AllowedDomains: []string{"scene", "script"}},
		{Prefix: "activate ", Service: "turn_on", AllowedDomains: []string{"scene", "script"}},
		{Prefix: "press ", Service: "press", AllowedDomains: []string{"button", "input_button"}},
	}
	lowered := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range actions {
		if !strings.HasPrefix(lowered, candidate.Prefix) {
			continue
		}
		target := strings.TrimSpace(value[len(candidate.Prefix):])
		target = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(target), "the "))
		if target == "" {
			return assistantHomeAssistantControlRequest{}, false
		}
		return assistantHomeAssistantControlRequest{
			Service:        candidate.Service,
			Target:         target,
			AllowedDomains: candidate.AllowedDomains,
			HighImpact:     candidate.HighImpact,
		}, true
	}

	for _, suffix := range []struct {
		Suffix  string
		Service string
	}{
		{Suffix: " on", Service: "turn_on"},
		{Suffix: " off", Service: "turn_off"},
	} {
		if !strings.HasPrefix(lowered, "turn ") || !strings.HasSuffix(lowered, suffix.Suffix) {
			continue
		}
		target := strings.TrimSpace(value[len("turn ") : len(value)-len(suffix.Suffix)])
		target = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(target), "the "))
		if target != "" {
			return assistantHomeAssistantControlRequest{
				Service:        suffix.Service,
				Target:         target,
				AllowedDomains: []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"},
			}, true
		}
	}
	return assistantHomeAssistantControlRequest{}, false
}

func normalizeHomeAssistantActionPrompt(prompt string) string {
	value := strings.TrimSpace(prompt)
	if slashPrompt, ok := slashCommandPrompt(value, "ha"); ok {
		value = slashPrompt
	}
	for {
		trimmed := strings.TrimSpace(value)
		lowered := strings.ToLower(trimmed)
		matched := false
		for _, prefix := range []string{"please, ", "please ", "can you ", "could you ", "would you ", "hank, ", "hank ", "home assistant, ", "home assistant "} {
			if strings.HasPrefix(lowered, prefix) {
				value = strings.TrimSpace(trimmed[len(prefix):])
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	value = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(value), ".!?"))
	lowered := strings.ToLower(value)
	if strings.HasSuffix(lowered, " please") {
		value = strings.TrimSpace(value[:len(value)-len(" please")])
	}
	return strings.TrimSpace(strings.TrimRight(value, ".!?"))
}

func (s *Server) planHomeAssistantControl(ctx context.Context, home domain.Home, prompt string) (assistantMessageContent, error) {
	control, ok := parseHomeAssistantControlPrompt(prompt)
	if !ok {
		return assistantMessageContent{Text: "I could not identify a supported Home Assistant control in that request."}, nil
	}
	envelope, err := s.sendAgentCommand(ctx, home.ID, "homeassistant.fetch_states", map[string]any{})
	if err != nil || envelope.Error != nil {
		return assistantMessageContent{Text: "I couldn't reach Home Assistant through the home agent right now."}, nil
	}
	payload, err := protocol.DecodePayload[protocol.HomeAssistantFetchStatesResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	candidates := matchingHomeAssistantControlStates(payload.States, control)
	if len(candidates) == 0 {
		return assistantMessageContent{
			Text: fmt.Sprintf("I checked Home Assistant, but I could not find a controllable entity matching `%s`.", control.Target),
		}, nil
	}
	if len(candidates) > 1 && candidates[0].Score < candidates[1].Score+5 {
		limit := min(len(candidates), 6)
		cards := make([]assistantResultCard, 0, limit)
		for _, candidate := range candidates[:limit] {
			cards = append(cards, assistantResultCardFromHomeAssistantState(candidate.State))
		}
		return assistantMessageContent{
			Text:  fmt.Sprintf("I found more than one controllable Home Assistant entity matching `%s`. Use the exact entity name or ID before I run an action.", control.Target),
			Cards: cards,
		}, nil
	}

	target := candidates[0].State
	entityName := firstNonBlank(homeAssistantFriendlyName(target), target.EntityID)
	confirmation := fmt.Sprintf("Confirm %s `%s` (%s).", homeAssistantControlGerund(control.Service), entityName, target.EntityID)
	pending := assistantPendingAction{
		Kind: "homeassistant_control",
		HomeAssistant: &assistantPendingHAControl{
			EntityID:     target.EntityID,
			EntityName:   entityName,
			CurrentState: target.State,
			Domain:       homeAssistantEntityDomain(target.EntityID),
			Service:      control.Service,
			Confirmation: confirmation,
			HighImpact:   control.HighImpact,
		},
	}
	return assistantMessageContent{
		Text:  fmt.Sprintf("I found `%s` in Home Assistant. Review the action before I send the command.", entityName),
		Cards: []assistantResultCard{assistantResultCardFromHomeAssistantState(target)},
		Meta: map[string]interface{}{
			"pending_action": pending,
		},
	}, nil
}

func matchingHomeAssistantControlStates(states []protocol.HomeAssistantState, control assistantHomeAssistantControlRequest) []assistantHomeAssistantControlCandidate {
	query := parseHomeAssistantQuery(control.Target)
	query.OnlyOn = false
	if domain := explicitHomeAssistantControlDomain(control.Target); domain != "" && assistantStringInList(domain, control.AllowedDomains) {
		query.Domain = domain
	}
	targetIdentity := compactSearchText(control.Target)
	candidates := make([]assistantHomeAssistantControlCandidate, 0)
	for _, state := range states {
		domain := homeAssistantEntityDomain(state.EntityID)
		if !assistantStringInList(domain, control.AllowedDomains) {
			continue
		}
		score := homeAssistantStateMatchScore(state, query)
		if score <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(state.EntityID), strings.TrimSpace(control.Target)) {
			score += 140
		}
		if targetIdentity != "" && targetIdentity == compactSearchText(homeAssistantFriendlyName(state)) {
			score += 120
		}
		candidates = append(candidates, assistantHomeAssistantControlCandidate{State: state, Score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return strings.ToLower(candidates[i].State.EntityID) < strings.ToLower(candidates[j].State.EntityID)
		}
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}

func explicitHomeAssistantControlDomain(target string) string {
	for _, token := range strings.Fields(strings.NewReplacer(".", " ", ",", " ", "-", " ").Replace(strings.ToLower(target))) {
		if domain, ok := homeAssistantDomainToken(token); ok {
			return domain
		}
		switch token {
		case "scene", "scenes":
			return "scene"
		case "script", "scripts":
			return "script"
		case "button", "buttons":
			return "button"
		}
	}
	return ""
}

func homeAssistantEntityDomain(entityID string) string {
	entityID = strings.TrimSpace(entityID)
	if dot := strings.Index(entityID, "."); dot >= 0 {
		return entityID[:dot]
	}
	return entityID
}

func assistantStringInList(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func homeAssistantControlGerund(service string) string {
	switch service {
	case "turn_on":
		return "turning on"
	case "turn_off":
		return "turning off"
	case "open_cover":
		return "opening"
	case "close_cover":
		return "closing"
	case "unlock":
		return "unlocking"
	case "lock":
		return "locking"
	case "press":
		return "pressing"
	default:
		return "running"
	}
}

type hermesChatAppRequest struct {
	Prompt         string `json:"prompt"`
	ConversationID string `json:"conversation_id,omitempty"`
	SessionKey     string `json:"session_key,omitempty"`
}

type hermesChatAppResponse struct {
	Text           string `json:"text"`
	Model          string `json:"model,omitempty"`
	ResponseID     string `json:"response_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type ydownloadAppRequest struct {
	URL string `json:"url"`
}

type ydownloadAppResponse struct {
	Text            string `json:"text"`
	SourceID        string `json:"source_id,omitempty"`
	DestinationPath string `json:"destination_path,omitempty"`
	Files           []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files,omitempty"`
}

type genericInstalledAppInput struct {
	RawText      string `json:"raw_text"`
	SlashCommand string `json:"slash_command"`
}

type genericInstalledAppOutput struct {
	Text        string                 `json:"text"`
	Cards       []assistantResultCard  `json:"cards,omitempty"`
	JobID       string                 `json:"job_id,omitempty"`
	Diagnostics map[string]interface{} `json:"diagnostics,omitempty"`
}

func (s *Server) answerHermesAppPrompt(ctx context.Context, home domain.Home, auth authContext, session *domain.AssistantSession, prompt string) (assistantMessageContent, error) {
	input, err := json.Marshal(hermesChatAppRequest{
		Prompt:         strings.TrimSpace(prompt),
		ConversationID: assistantHermesConversationID(home.ID, auth.User.ID, session),
		SessionKey:     assistantHermesSessionKey(home.ID, auth.User.ID, session),
	})
	if err != nil {
		return assistantMessageContent{}, err
	}
	envelope, err := s.sendAgentCommand(ctx, home.ID, protocol.CommandAppsInvoke, protocol.AppsInvokeRequest{
		AppID:     "hermes",
		CommandID: "chat",
		Input:     input,
	})
	if err != nil {
		return assistantMessageContent{
			Text: "I couldn't reach Hermes through the primary Hank Agent right now.",
		}, nil
	}
	if envelope.Error != nil {
		switch envelope.Error.Code {
		case "app_not_found", "app_disabled", "app_command_not_found", "unsupported_command":
			return assistantMessageContent{
				Text: "Hermes chat is not configured on the primary Hank Agent yet.",
			}, nil
		case "request_timeout":
			return assistantMessageContent{
				Text: "Hermes did not respond before the request timed out.",
			}, nil
		default:
			return assistantMessageContent{
				Text: "Hermes returned an error before it could answer.",
			}, nil
		}
	}
	payload, err := protocol.DecodePayload[protocol.AppsInvokeResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if len(payload.Output) == 0 {
		return assistantMessageContent{Text: "Hermes returned an empty response."}, nil
	}
	var hermesResponse hermesChatAppResponse
	if err := json.Unmarshal(payload.Output, &hermesResponse); err != nil {
		return assistantMessageContent{}, err
	}
	return assistantContentFromHermesResponse(hermesResponse), nil
}

func (s *Server) answerYDownloadAppPrompt(ctx context.Context, home domain.Home, url string) (assistantMessageContent, error) {
	input, err := json.Marshal(ydownloadAppRequest{URL: strings.TrimSpace(url)})
	if err != nil {
		return assistantMessageContent{}, err
	}
	envelope, err := s.sendAgentCommand(ctx, home.ID, protocol.CommandAppsInvoke, protocol.AppsInvokeRequest{
		AppID:     "ydownload",
		CommandID: "download",
		Input:     input,
	})
	if err != nil {
		return assistantMessageContent{
			Text: "I couldn't reach YDownload through the primary Hank Agent right now.",
		}, nil
	}
	if envelope.Error != nil {
		switch envelope.Error.Code {
		case "app_not_found", "app_disabled", "app_command_not_found", "unsupported_command":
			return assistantMessageContent{
				Text: "YDownload is not configured on the primary Hank Agent yet.",
			}, nil
		case "request_timeout":
			return assistantMessageContent{
				Text: "YDownload did not finish before the request timed out.",
			}, nil
		default:
			return assistantMessageContent{
				Text: "YDownload returned an error before it could finish the download.",
			}, nil
		}
	}
	payload, err := protocol.DecodePayload[protocol.AppsInvokeResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if len(payload.Output) == 0 {
		return assistantMessageContent{Text: "YDownload returned an empty response."}, nil
	}
	var response ydownloadAppResponse
	if err := json.Unmarshal(payload.Output, &response); err != nil {
		return assistantMessageContent{}, err
	}
	return assistantContentFromYDownloadResponse(response), nil
}

func (s *Server) answerGenericInstalledAppPrompt(ctx context.Context, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	appName := defaultString(intent.AppName, intent.AppID)
	checkBody, err := protocol.EncodeBody(protocol.AppsInvokeRequest{AppID: intent.AppID, CommandID: intent.CommandID})
	if err != nil {
		return assistantMessageContent{}, err
	}
	if err := s.authorizeAppsInvokeCommand(ctx, runtime.Home.ID, runtime.Membership, protocol.RoutedCommand{Command: protocol.CommandAppsInvoke, Body: checkBody}); err != nil {
		return assistantMessageContent{Text: fmt.Sprintf("%s is not available to your Home account.", appName)}, nil
	}
	input, err := installedAppSlashInput(intent)
	if err != nil {
		return assistantMessageContent{}, err
	}
	appContext := map[string]interface{}{
		"home_id": runtime.Home.ID,
		"user_id": runtime.Auth.User.ID,
		"role":    runtime.Membership.Role,
	}
	if runtime.Timezone != "" {
		appContext["timezone"] = runtime.Timezone
	}
	if runtime.Session != nil {
		appContext["session_id"] = runtime.Session.ID
	}
	if runtime.DeviceID != "" {
		appContext["device"] = map[string]interface{}{"device_id": runtime.DeviceID}
	}
	contextPayload, err := json.Marshal(appContext)
	if err != nil {
		return assistantMessageContent{}, err
	}
	envelope, err := s.sendAgentCommand(ctx, runtime.Home.ID, protocol.CommandAppsInvoke, protocol.AppsInvokeRequest{
		AppID:     intent.AppID,
		CommandID: intent.CommandID,
		Input:     input,
		Context:   contextPayload,
	})
	if err != nil {
		return assistantMessageContent{
			Text: fmt.Sprintf("I couldn't reach %s through the primary Hank Agent right now.", appName),
		}, nil
	}
	if envelope.Error != nil {
		switch envelope.Error.Code {
		case "app_not_found", "app_disabled", "app_command_not_found", "app_unavailable", "unsupported_command":
			return assistantMessageContent{
				Text: fmt.Sprintf("%s is not configured on the primary Hank Agent yet.", appName),
			}, nil
		case "request_timeout":
			return assistantMessageContent{
				Text: fmt.Sprintf("%s did not respond before the request timed out.", appName),
			}, nil
		case "permission_denied":
			return assistantMessageContent{
				Text: fmt.Sprintf("%s is not available to your Home account.", appName),
			}, nil
		default:
			return assistantMessageContent{
				Text: fmt.Sprintf("%s returned an error before it could finish.", appName),
			}, nil
		}
	}
	payload, err := protocol.DecodePayload[protocol.AppsInvokeResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if intent.AppID == "gramaton" && intent.CommandID == "search" {
		var searchResponse protocol.MediaSearchResponse
		if err := json.Unmarshal(payload.Output, &searchResponse); err != nil {
			return assistantMessageContent{}, err
		}
		return s.assistantContentFromMediaSearchResponse(ctx, runtime.Home, intent.Query, searchResponse)
	}
	content, err := assistantContentFromGenericAppOutput(appName, payload.Output)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if payload.JobID != "" {
		if content.Meta == nil {
			content.Meta = make(map[string]interface{})
		}
		content.Meta["app_job_id"] = payload.JobID
	}
	return content, nil
}

func installedAppSlashInput(intent assistantIntent) (json.RawMessage, error) {
	query := strings.TrimSpace(intent.Query)
	var input any
	switch {
	case intent.AppID == "gramaton" && intent.CommandID == "search":
		input = protocol.MediaSearchRequest{Query: query, Limit: 10}
	case intent.AppID == "ydownload" && intent.CommandID == "download":
		input = ydownloadAppRequest{URL: query}
	case intent.AppID == "hermes" && intent.CommandID == "chat":
		input = hermesChatAppRequest{Prompt: query}
	default:
		input = genericInstalledAppInput{
			RawText:      query,
			SlashCommand: strings.TrimSpace(intent.SlashCommand),
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func assistantContentFromHermesResponse(payload hermesChatAppResponse) assistantMessageContent {
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		text = "Hermes returned an empty response."
	}
	return assistantMessageContent{
		Text: text,
		Meta: map[string]interface{}{
			"hermes": map[string]interface{}{
				"model":           payload.Model,
				"response_id":     payload.ResponseID,
				"conversation_id": payload.ConversationID,
			},
		},
	}
}

func assistantContentFromYDownloadResponse(payload ydownloadAppResponse) assistantMessageContent {
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		text = "YDownload finished."
	}
	return assistantMessageContent{Text: text}
}

func assistantContentFromGenericAppOutput(appName string, output json.RawMessage) (assistantMessageContent, error) {
	appName = defaultString(appName, "The app")
	trimmed := strings.TrimSpace(string(output))
	if len(output) == 0 || trimmed == "" || trimmed == "null" {
		return assistantMessageContent{Text: fmt.Sprintf("%s returned an empty response.", appName)}, nil
	}
	var textOutput string
	if err := json.Unmarshal(output, &textOutput); err == nil {
		textOutput = strings.TrimSpace(textOutput)
		if textOutput == "" {
			textOutput = fmt.Sprintf("%s returned an empty response.", appName)
		}
		return assistantMessageContent{Text: textOutput}, nil
	}
	var payload genericInstalledAppOutput
	if err := json.Unmarshal(output, &payload); err != nil {
		return assistantMessageContent{}, err
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" && len(payload.Cards) == 0 && strings.TrimSpace(payload.JobID) == "" && len(payload.Diagnostics) == 0 {
		text = fmt.Sprintf("%s returned an empty response.", appName)
	}
	meta := map[string]interface{}{}
	if len(payload.Diagnostics) > 0 {
		meta["app_diagnostics"] = payload.Diagnostics
	}
	if jobID := strings.TrimSpace(payload.JobID); jobID != "" {
		meta["app_job_id"] = jobID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return assistantMessageContent{Text: text, Cards: payload.Cards, Meta: meta}, nil
}

func (s *Server) answerProjectDocPrompt(ctx context.Context, home domain.Home, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	settings = normalizeAssistantSettings(settings)
	queryEmbedding, embeddingModel, _ := s.embedAssistantText(ctx, auth.User.ID, prompt, settings)
	contexts, err := s.store.SearchAssistantContext(ctx, home.ID, auth.User.ID, prompt, queryEmbedding, embeddingModel, settings.MaxContextItems)
	if err != nil {
		return assistantMessageContent{}, err
	}
	projectContexts := make([]domain.AssistantRetrievedContext, 0, len(contexts))
	for _, item := range contexts {
		if item.SourceType == assistantProjectDocSourceType && assistantSettingsAllowSource(settings, item.SourceType) {
			projectContexts = append(projectContexts, item)
		}
		if len(projectContexts) >= settings.MaxContextItems {
			break
		}
	}
	if len(projectContexts) == 0 {
		return assistantMessageContent{
			Text: "I could not find matching Hank project documentation for that.",
		}, nil
	}
	rankProjectDocContexts(projectContexts, prompt)

	answer := fallbackRetrievedAnswer(prompt, projectContexts)
	if providerAnswer, modelName, err := s.generateAssistantLLMResponseWithSettings(ctx, auth.User.ID, settings, []assistantLLMMessage{
		{
			Role:    "system",
			Content: settings.SystemPrompt,
		},
		{
			Role:    "user",
			Content: assistantPromptWithContext(prompt, projectContexts),
		},
	}); err == nil && strings.TrimSpace(providerAnswer) != "" {
		answer = strings.TrimSpace(providerAnswer)
		_ = modelName
	} else if errors.Is(err, errChatGPTRelinkRequired) {
		return assistantMessageContent{
			Text: "ChatGPT/Codex needs to be linked again before HankAI can use your ChatGPT plan for chat. Open AI Settings and relink ChatGPT/Codex.",
		}, nil
	} else if err != nil {
		s.logger.Warn("assistant project docs provider answer failed; using retrieved fallback", "error_code", "operation_failed")
	}

	cards := make([]assistantResultCard, 0, min(len(projectContexts), 3))
	for _, item := range projectContexts {
		card := assistantResultCardFromContext(item)
		if card.Kind != "" {
			cards = append(cards, card)
		}
		if len(cards) >= 3 {
			break
		}
	}
	return assistantMessageContent{Text: answer, Cards: cards}, nil
}

func (s *Server) answerRetrievedPrompt(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	settings = normalizeAssistantSettings(settings)
	queryEmbedding, embeddingModel, _ := s.embedAssistantText(ctx, auth.User.ID, prompt, settings)
	searchLimit := settings.MaxContextItems * 3
	if searchLimit < 20 {
		searchLimit = 20
	}
	if searchLimit > 60 {
		searchLimit = 60
	}
	contexts, err := s.store.SearchAssistantContext(ctx, home.ID, auth.User.ID, prompt, queryEmbedding, embeddingModel, searchLimit)
	if err != nil {
		return assistantMessageContent{}, err
	}
	contexts, err = s.filterAssistantContexts(ctx, home, membership, auth.User.ID, settings, contexts, assistantPromptAllowsProjectDocs(prompt))
	if err != nil {
		return assistantMessageContent{}, err
	}
	if len(contexts) == 0 {
		return assistantMessageContent{
			Text: fmt.Sprintf("I could not find matching Hank context in the sources HankAI can currently use: %s.", assistantSettingsEnabledSourceLabels(settings)),
		}, nil
	}

	answer := fallbackRetrievedAnswer(prompt, contexts)
	if providerAnswer, modelName, err := s.generateAssistantLLMResponseWithSettings(ctx, auth.User.ID, settings, []assistantLLMMessage{
		{
			Role:    "system",
			Content: settings.SystemPrompt,
		},
		{
			Role:    "user",
			Content: assistantPromptWithContext(prompt, contexts),
		},
	}); err == nil && strings.TrimSpace(providerAnswer) != "" {
		answer = strings.TrimSpace(providerAnswer)
		_ = modelName
	} else if errors.Is(err, errChatGPTRelinkRequired) {
		return assistantMessageContent{
			Text: "ChatGPT/Codex needs to be linked again before HankAI can use your ChatGPT plan for chat. Open AI Settings and relink ChatGPT/Codex.",
		}, nil
	} else if err != nil {
		s.logger.Warn("assistant provider answer failed; using retrieved fallback", "error_code", "operation_failed")
	}

	cards := make([]assistantResultCard, 0, min(len(contexts), 3))
	for _, item := range contexts {
		card := assistantResultCardFromContext(item)
		if card.Kind != "" {
			cards = append(cards, card)
		}
		if len(cards) >= 3 {
			break
		}
	}
	return assistantMessageContent{Text: answer, Cards: cards}, nil
}

func (s *Server) assistantVisibleNotes(ctx context.Context, homeID string, userID string, settings domain.AssistantSettings) ([]domain.UserNote, error) {
	settings = normalizeAssistantSettings(settings)
	notes := make([]domain.UserNote, 0)
	if settings.ProfileNotesEnabled {
		profileNotes, err := s.store.ListProfileNotes(ctx, userID, false)
		if err != nil {
			return nil, err
		}
		notes = append(notes, profileNotes...)
	}
	if settings.HomeNotesEnabled {
		homeNotes, err := s.store.ListVisibleHomeNotes(ctx, homeID, userID, false)
		if err != nil {
			return nil, err
		}
		notes = append(notes, homeNotes...)
	}
	return notes, nil
}

func uniqueAssistantNotes(notes []domain.UserNote) []domain.UserNote {
	seen := map[string]bool{}
	unique := make([]domain.UserNote, 0, len(notes))
	for _, note := range notes {
		key := firstNonBlank(note.ID, note.NoteID)
		if key == "" {
			key = fmt.Sprintf("%s:%s:%s", note.OwnerUserID, note.HomeID, note.Title)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, note)
	}
	return unique
}

func noteAccessLabel(note domain.UserNote) string {
	if strings.TrimSpace(note.HomeID) != "" {
		return "shared"
	}
	return "personal"
}

func (s *Server) filterAssistantContexts(ctx context.Context, home domain.Home, membership domain.HomeMembership, userID string, settings domain.AssistantSettings, contexts []domain.AssistantRetrievedContext, includeProjectDocs bool) ([]domain.AssistantRetrievedContext, error) {
	featureAllowed := map[string]bool{}
	for _, feature := range []string{domain.HomePermissionFeatureNotes, domain.HomePermissionFeatureFiles, domain.HomePermissionFeatureHomeAssistant} {
		allowed, err := s.homeFeatureAllowed(ctx, home, membership, userID, feature)
		if err != nil {
			return nil, err
		}
		featureAllowed[feature] = allowed
	}

	filtered := make([]domain.AssistantRetrievedContext, 0, len(contexts))
	for _, item := range contexts {
		if !assistantSettingsAllowSource(settings, item.SourceType) {
			continue
		}
		if item.SourceType == assistantProjectDocSourceType && !includeProjectDocs {
			continue
		}
		switch item.SourceType {
		case "profile_note", "shared_note":
			if !featureAllowed[domain.HomePermissionFeatureNotes] {
				continue
			}
		case "file":
			if !featureAllowed[domain.HomePermissionFeatureFiles] {
				continue
			}
		case "homeassistant_entity":
			if !featureAllowed[domain.HomePermissionFeatureHomeAssistant] {
				continue
			}
		}
		filtered = append(filtered, item)
		if len(filtered) >= settings.MaxContextItems {
			break
		}
	}
	return filtered, nil
}

func (s *Server) finalizeAssistantClientToolRun(ctx context.Context, session domain.AssistantSession, run domain.AssistantRun, toolName string, result map[string]interface{}) (assistantMessageContent, error) {
	switch toolName {
	case "calendar.create_event":
		title, _ := result["title"].(string)
		startsAt, _ := result["starts_at"].(string)
		eventID, _ := result["event_id"].(string)
		if title == "" {
			title = "Calendar Event"
		}
		if startsAt == "" {
			startsAt = "the requested time"
		}
		targetDate := parseAssistantResultTime(startsAt)
		return assistantMessageContent{
			Text: fmt.Sprintf("Created `%s` on your device calendar for %s.", title, startsAt),
			Cards: []assistantResultCard{
				{
					Kind:        "calendar",
					Title:       title,
					Summary:     startsAt,
					ActionTitle: "Open in Calendar",
					EventID:     eventID,
					TargetDate:  targetDate,
				},
			},
		}, nil
	case "calendar.update_event":
		title, _ := result["title"].(string)
		startsAt, _ := result["starts_at"].(string)
		eventID, _ := result["event_id"].(string)
		targetDate := parseAssistantResultTime(startsAt)
		if title == "" {
			title = "Updated Calendar Event"
		}
		return assistantMessageContent{
			Text: fmt.Sprintf("Updated `%s` on your device calendar.", title),
			Cards: []assistantResultCard{
				{
					Kind:        "calendar",
					Title:       title,
					Summary:     startsAt,
					ActionTitle: "Open in Calendar",
					EventID:     eventID,
					TargetDate:  targetDate,
				},
			},
		}, nil
	case "calendar.delete_event":
		title, _ := result["title"].(string)
		startsAt, _ := result["starts_at"].(string)
		eventID, _ := result["event_id"].(string)
		targetDate := parseAssistantResultTime(startsAt)
		if title == "" {
			title = "Deleted Calendar Event"
		}
		return assistantMessageContent{
			Text: fmt.Sprintf("Deleted `%s` from your device calendar.", title),
			Cards: []assistantResultCard{
				{
					Kind:        "calendar",
					Title:       title,
					Summary:     startsAt,
					ActionTitle: "Calendar",
					EventID:     eventID,
					TargetDate:  targetDate,
				},
			},
		}, nil
	case "calendar.search":
		query, _ := result["query"].(string)
		if matches, ok := result["matches"].([]interface{}); ok && len(matches) > 0 {
			first, _ := matches[0].(map[string]interface{})
			title, _ := first["title"].(string)
			startsAt, _ := first["starts_at"].(string)
			eventID, _ := first["event_id"].(string)
			targetDate := parseAssistantResultTime(startsAt)
			return assistantMessageContent{
				Text: fmt.Sprintf("I found a calendar match for `%s`.", query),
				Cards: []assistantResultCard{
					{
						Kind:        "calendar",
						Title:       defaultString(title, "Calendar Match"),
						Summary:     startsAt,
						ActionTitle: "Open in Calendar",
						EventID:     eventID,
						TargetDate:  targetDate,
						SearchText:  query,
					},
				},
			}, nil
		}
		return assistantMessageContent{
			Text: fmt.Sprintf("I checked your device calendar but did not find a match for `%s`.", query),
		}, nil
	case "attachments.commit":
		return s.finalizeAssistantAttachmentCommit(ctx, session, result)
	default:
		_ = ctx
		_ = session
		_ = run
		return assistantMessageContent{
			Text: "The client tool finished, but I do not have a formatter for that result yet.",
		}, nil
	}
}

func (s *Server) finalizeAssistantClientToolErrorRun(ctx context.Context, session domain.AssistantSession, toolName string, result map[string]interface{}, toolError string) (assistantMessageContent, error) {
	toolError = strings.TrimSpace(toolError)
	if toolName == "attachments.commit" {
		return s.finalizeAssistantAttachmentCommitError(ctx, session, result, toolError)
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("The `%s` action could not be completed: %s", toolName, toolError),
	}, nil
}

func (s *Server) finalizeAssistantAttachmentCommit(ctx context.Context, session domain.AssistantSession, result map[string]interface{}) (assistantMessageContent, error) {
	destinationKind, _ := result["destination_kind"].(string)
	files := assistantToolResultFiles(result)
	clientIDs := make([]string, 0, len(files))
	for _, file := range files {
		if id := strings.TrimSpace(file["client_attachment_id"]); id != "" {
			clientIDs = append(clientIDs, id)
		}
	}
	if err := s.store.MarkAssistantAttachmentsCommitted(ctx, session.ID, clientIDs, time.Now().UTC()); err != nil {
		return assistantMessageContent{}, err
	}

	switch destinationKind {
	case "note_attachment":
		noteTitle := assistantResultString(result, "note_title", "Note")
		noteID := assistantResultString(result, "note_id", "")
		text := fmt.Sprintf("Stored %d attachment(s) in `%s`.", max(1, len(files)), noteTitle)
		cards := []assistantResultCard{
			{
				Kind:        "note",
				Title:       noteTitle,
				Summary:     "Attachment stored in Notes.",
				ActionTitle: "Open in Notes",
				NoteID:      noteID,
			},
		}
		return assistantMessageContent{Text: text, Cards: cards}, nil
	case "smb":
		cards := make([]assistantResultCard, 0, len(files))
		for _, file := range files {
			path := strings.TrimSpace(file["path"])
			title := strings.TrimSpace(file["filename"])
			if title == "" {
				title = filepathBase(path)
			}
			cards = append(cards, assistantResultCard{
				Kind:        "file",
				Title:       defaultString(title, "Uploaded file"),
				Summary:     path,
				ActionTitle: "Open in File Server",
				SourceID:    strings.TrimSpace(file["source_id"]),
				Path:        path,
			})
		}
		targetPath := assistantResultString(result, "target_path", "File Server")
		return assistantMessageContent{
			Text:  fmt.Sprintf("Stored %d attachment(s) in `%s`.", max(1, len(files)), targetPath),
			Cards: cards,
		}, nil
	default:
		return assistantMessageContent{
			Text: fmt.Sprintf("Stored %d attachment(s).", max(1, len(files))),
		}, nil
	}
}

func (s *Server) finalizeAssistantAttachmentCommitError(ctx context.Context, session domain.AssistantSession, result map[string]interface{}, toolError string) (assistantMessageContent, error) {
	files := assistantToolResultFiles(result)
	clientIDs := make([]string, 0, len(files))
	for _, file := range files {
		if id := strings.TrimSpace(file["client_attachment_id"]); id != "" {
			clientIDs = append(clientIDs, id)
		}
	}
	if err := s.store.MarkAssistantAttachmentsCommitted(ctx, session.ID, clientIDs, time.Now().UTC()); err != nil {
		return assistantMessageContent{}, err
	}
	if code := assistantResultString(result, "error_code", ""); code == "missing_staged_attachment" {
		expiredIDs := assistantStringListFromResult(result["expired_attachment_ids"])
		if len(expiredIDs) == 0 {
			expiredIDs = assistantStringListFromResult(result["attachment_ids"])
		}
		if err := s.store.MarkAssistantAttachmentsExpired(ctx, session.ID, expiredIDs, time.Now().UTC()); err != nil {
			return assistantMessageContent{}, err
		}
	}
	if len(files) > 0 {
		content, err := s.attachmentCommitResultContent(result, files)
		if err != nil {
			return assistantMessageContent{}, err
		}
		content.Text = fmt.Sprintf("%s Some files still need attention: %s", content.Text, toolError)
		return content, nil
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I could not store the uploaded attachment(s): %s", toolError),
	}, nil
}

func (s *Server) attachmentCommitResultContent(result map[string]interface{}, files []map[string]string) (assistantMessageContent, error) {
	destinationKind, _ := result["destination_kind"].(string)
	switch destinationKind {
	case "note_attachment":
		noteTitle := assistantResultString(result, "note_title", "Note")
		noteID := assistantResultString(result, "note_id", "")
		text := fmt.Sprintf("Stored %d attachment(s) in `%s`.", max(1, len(files)), noteTitle)
		cards := []assistantResultCard{
			{
				Kind:        "note",
				Title:       noteTitle,
				Summary:     "Attachment stored in Notes.",
				ActionTitle: "Open in Notes",
				NoteID:      noteID,
			},
		}
		return assistantMessageContent{Text: text, Cards: cards}, nil
	case "smb":
		cards := make([]assistantResultCard, 0, len(files))
		for _, file := range files {
			path := strings.TrimSpace(file["path"])
			title := strings.TrimSpace(file["filename"])
			if title == "" {
				title = filepathBase(path)
			}
			cards = append(cards, assistantResultCard{
				Kind:        "file",
				Title:       defaultString(title, "Uploaded file"),
				Summary:     path,
				ActionTitle: "Open in File Server",
				SourceID:    strings.TrimSpace(file["source_id"]),
				Path:        path,
			})
		}
		targetPath := assistantResultString(result, "target_path", "File Server")
		return assistantMessageContent{
			Text:  fmt.Sprintf("Stored %d attachment(s) in `%s`.", max(1, len(files)), targetPath),
			Cards: cards,
		}, nil
	default:
		return assistantMessageContent{
			Text: fmt.Sprintf("Stored %d attachment(s).", max(1, len(files))),
		}, nil
	}
}

func assistantToolResultFiles(result map[string]interface{}) []map[string]string {
	raw, ok := result["files"].([]interface{})
	if !ok {
		return nil
	}
	files := make([]map[string]string, 0, len(raw))
	for _, item := range raw {
		values, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		file := map[string]string{}
		for key, value := range values {
			file[key] = strings.TrimSpace(fmt.Sprint(value))
		}
		files = append(files, file)
	}
	return files
}

func assistantStringListFromResult(value interface{}) []string {
	if raw, ok := value.([]string); ok {
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			if text := strings.TrimSpace(item); text != "" {
				values = append(values, text)
			}
		}
		return values
	}
	raw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
			values = append(values, text)
		}
	}
	return values
}

func assistantResultString(result map[string]interface{}, key string, fallback string) string {
	if value, ok := result[key]; ok {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return fallback
}

func filepathBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimRight(path, "/")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func (s *Server) executeConfirmedAssistantAction(
	ctx context.Context,
	session domain.AssistantSession,
	run domain.AssistantRun,
	pending assistantPendingAction,
	userID string,
) (assistantRunResponse, error) {
	switch pending.Kind {
	case "note_append":
		if pending.NoteAppend == nil {
			return assistantRunResponse{}, errors.New("pending note append is missing")
		}
		note, err := s.store.GetUserNoteByID(ctx, pending.NoteAppend.TargetNoteID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		newContent := appendAssistantNoteText(note.Content, pending.NoteAppend.AppendedText)
		revision, checksum, err := revisionAndChecksum(newContent, note.PageType, note.BoardJSON)
		if err != nil {
			return assistantRunResponse{}, err
		}
		note.Content = newContent
		note.BodyMarkdown = newContent
		if note.BodyFormat == "" {
			note.BodyFormat = "markdown"
		}
		note.Revision = revision
		note.Checksum = checksum
		note.UpdatedAt = time.Now().UTC()
		note.UpdatedBy = userID
		if err := s.store.UpsertUserNote(ctx, note); err != nil {
			return assistantRunResponse{}, err
		}
		s.enqueueAssistantIndexJob(ctx, session.HomeID, userID, assistantNoteSourceType(note), note.NoteID)
		content := assistantMessageContent{
			Text: fmt.Sprintf("Added `%s` to `%s`.", pending.NoteAppend.AppendedText, note.Title),
			Cards: []assistantResultCard{
				{
					Kind:        "note",
					Title:       note.Title,
					Summary:     notePreview(newContent),
					ActionTitle: "Open in Notes",
					NoteID:      note.NoteID,
					SearchText:  pending.NoteAppend.AppendedText,
				},
			},
		}
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		settings, err := s.currentAssistantSettings(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:               run.ID,
			State:            run.State,
			AssistantMessage: &message,
		}, nil

	case "note_create":
		if pending.NoteCreate == nil {
			return assistantRunResponse{}, errors.New("pending note create is missing")
		}
		body := strings.TrimSpace(pending.NoteCreate.BodyMarkdown)
		revision, checksum, err := revisionAndChecksum(body, protocol.NotePageTypeText, "")
		if err != nil {
			return assistantRunResponse{}, err
		}
		now := time.Now().UTC()
		note := domain.UserNote{
			ID:           newID("note"),
			NoteID:       newID("note"),
			OwnerUserID:  userID,
			Title:        pending.NoteCreate.Title,
			Content:      body,
			BodyMarkdown: body,
			BodyFormat:   "markdown",
			PageType:     protocol.NotePageTypeText,
			Revision:     revision,
			Checksum:     checksum,
			CreatedAt:    now,
			UpdatedAt:    now,
			UpdatedBy:    userID,
		}
		if err := s.store.UpsertUserNote(ctx, note); err != nil {
			return assistantRunResponse{}, err
		}
		s.enqueueAssistantIndexJob(ctx, session.HomeID, userID, assistantNoteSourceType(note), note.NoteID)
		content := assistantMessageContent{
			Text: fmt.Sprintf("Created `%s`.", note.Title),
			Cards: []assistantResultCard{
				{
					Kind:        "note",
					Title:       note.Title,
					Summary:     notePreview(note.Content),
					ActionTitle: "Open in Notes",
					NoteID:      note.NoteID,
				},
			},
		}
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		settings, err := s.currentAssistantSettings(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:               run.ID,
			State:            run.State,
			AssistantMessage: &message,
		}, nil

	case "calendar_create":
		if pending.CalendarCreate == nil {
			return assistantRunResponse{}, errors.New("pending calendar create is missing")
		}
		payload, err := json.Marshal(pending.CalendarCreate.ToolRequest)
		if err != nil {
			return assistantRunResponse{}, err
		}
		run.State = assistantStateWaitingClientTool
		run.RequiresConfirmation = false
		run.RequiresClientTools = true
		run.PendingActionJSON = string(payload)
		run.CompletedAt = nil
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:                  run.ID,
			State:               run.State,
			RequiresClientTools: true,
			ClientToolRequest:   &pending.CalendarCreate.ToolRequest,
		}, nil
	case "calendar_update", "calendar_delete":
		if pending.CalendarClient == nil {
			return assistantRunResponse{}, errors.New("pending calendar client action is missing")
		}
		payload, err := json.Marshal(pending.CalendarClient.ToolRequest)
		if err != nil {
			return assistantRunResponse{}, err
		}
		run.State = assistantStateWaitingClientTool
		run.RequiresConfirmation = false
		run.RequiresClientTools = true
		run.PendingActionJSON = string(payload)
		run.CompletedAt = nil
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:                  run.ID,
			State:               run.State,
			RequiresClientTools: true,
			ClientToolRequest:   &pending.CalendarClient.ToolRequest,
		}, nil
	case "attachment_commit":
		if pending.AttachmentCommit == nil {
			return assistantRunResponse{}, errors.New("pending attachment commit is missing")
		}
		payload, err := json.Marshal(pending.AttachmentCommit.ToolRequest)
		if err != nil {
			return assistantRunResponse{}, err
		}
		run.State = assistantStateWaitingClientTool
		run.RequiresConfirmation = false
		run.RequiresClientTools = true
		run.PendingActionJSON = string(payload)
		run.CompletedAt = nil
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:                  run.ID,
			State:               run.State,
			RequiresClientTools: true,
			ClientToolRequest:   &pending.AttachmentCommit.ToolRequest,
		}, nil
	case "homeassistant_control":
		if pending.HomeAssistant == nil {
			return assistantRunResponse{}, errors.New("pending Home Assistant control is missing")
		}
		action := pending.HomeAssistant
		if !allowedAssistantHomeAssistantControl(action.Domain, action.Service) ||
			strings.TrimSpace(action.EntityID) == "" ||
			homeAssistantEntityDomain(action.EntityID) != strings.TrimSpace(action.Domain) {
			return assistantRunResponse{}, errors.New("pending Home Assistant control is invalid")
		}
		home, err := s.store.GetHomeByID(ctx, session.HomeID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		membership, err := s.store.GetHomeMembership(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.requireHomeFeature(ctx, home, membership, userID, domain.HomePermissionFeatureHomeAssistant); err != nil {
			return assistantRunResponse{}, err
		}
		settings, err := s.currentAssistantSettings(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if !settings.HomeAssistantEnabled {
			return assistantRunResponse{}, errors.New("Home Assistant access is turned off in HankAI settings")
		}
		body, err := json.Marshal(map[string]interface{}{"entity_id": action.EntityID})
		if err != nil {
			return assistantRunResponse{}, err
		}
		envelope, err := s.sendAgentCommand(ctx, session.HomeID, "homeassistant.call_service", protocol.HomeAssistantCallServiceRequest{
			Domain:  action.Domain,
			Service: action.Service,
			Body:    body,
		})
		if err != nil {
			s.auditAssistantHomeAssistantControl(ctx, session.HomeID, userID, run.ID, action, "failed", "agent_command_failed")
			return assistantRunResponse{}, err
		}
		if envelope.Error != nil {
			s.auditAssistantHomeAssistantControl(ctx, session.HomeID, userID, run.ID, action, "failed", envelope.Error.Code)
			return assistantRunResponse{}, errors.New(envelope.Error.Message)
		}
		s.auditAssistantHomeAssistantControl(ctx, session.HomeID, userID, run.ID, action, "succeeded", "")
		entityName := firstNonBlank(action.EntityName, action.EntityID)
		card := assistantResultCardFromHomeAssistantState(protocol.HomeAssistantState{
			EntityID:   action.EntityID,
			State:      action.CurrentState,
			Attributes: map[string]interface{}{"friendly_name": entityName},
		})
		card.Summary = fmt.Sprintf("%s command accepted for %s", strings.ReplaceAll(action.Service, "_", " "), action.EntityID)
		content := assistantMessageContent{
			Text:  fmt.Sprintf("Sent `%s` to `%s` in Home Assistant.", strings.ReplaceAll(action.Service, "_", " "), entityName),
			Cards: []assistantResultCard{card},
		}
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:               run.ID,
			State:            run.State,
			AssistantMessage: &message,
		}, nil
	case "media_download":
		if pending.MediaDownload == nil {
			return assistantRunResponse{}, errors.New("pending media download is missing")
		}
		response, err := s.startMediaDownload(ctx, session.HomeID, pending.MediaDownload.Selection, pending.MediaDownload.Filter)
		if err != nil {
			return assistantRunResponse{}, err
		}
		job := response.Job
		content := mediaDownloadStartedContent(pending.MediaDownload.Title, pending.MediaDownload.Selection, job, pending.MediaDownload.DestinationPath)
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		settings, err := s.currentAssistantSettings(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:               run.ID,
			State:            run.State,
			AssistantMessage: &message,
		}, nil
	case "file_create_folder":
		if pending.FileCreateFolder == nil {
			return assistantRunResponse{}, errors.New("pending file create folder is missing")
		}
		home, err := s.store.GetHomeByID(ctx, session.HomeID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		membership, err := s.store.GetHomeMembership(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.requireHomeFeature(ctx, home, membership, userID, domain.HomePermissionFeatureFiles); err != nil {
			return assistantRunResponse{}, err
		}
		envelope, err := s.sendAgentCommand(ctx, session.HomeID, "files.create_directory", protocol.FilesCreateDirectoryRequest{
			SourceID: pending.FileCreateFolder.SourceID,
			Path:     pending.FileCreateFolder.Path,
		})
		if err != nil {
			return assistantRunResponse{}, err
		}
		if envelope.Error != nil {
			return assistantRunResponse{}, errors.New(envelope.Error.Message)
		}
		content := assistantMessageContent{
			Text: fmt.Sprintf("Created File Server folder `%s`.", pending.FileCreateFolder.Path),
			Cards: []assistantResultCard{
				{
					Kind:        "file",
					Title:       filepathBase(pending.FileCreateFolder.Path),
					Summary:     pending.FileCreateFolder.Path,
					ActionTitle: "Open in File Server",
					SourceID:    pending.FileCreateFolder.SourceID,
					Path:        pending.FileCreateFolder.Path,
					IsDirectory: true,
				},
			},
		}
		message, err := s.persistAssistantMessage(ctx, session, assistantRoleAssistant, content)
		if err != nil {
			return assistantRunResponse{}, err
		}
		completedAt := time.Now().UTC()
		run.State = assistantStateCompleted
		run.RequiresConfirmation = false
		run.PendingActionJSON = ""
		run.MessageID = message.ID
		run.CompletedAt = &completedAt
		if err := s.store.UpdateAssistantRun(ctx, run); err != nil {
			return assistantRunResponse{}, err
		}
		settings, err := s.currentAssistantSettings(ctx, session.HomeID, userID)
		if err != nil {
			return assistantRunResponse{}, err
		}
		if err := s.touchAssistantSessionAndMemory(ctx, session, settings, completedAt); err != nil {
			return assistantRunResponse{}, err
		}
		return assistantRunResponse{
			ID:               run.ID,
			State:            run.State,
			AssistantMessage: &message,
		}, nil
	default:
		return assistantRunResponse{}, fmt.Errorf("unsupported pending action kind %q", pending.Kind)
	}
}

func allowedAssistantHomeAssistantControl(domainValue string, service string) bool {
	domainValue = strings.TrimSpace(domainValue)
	service = strings.TrimSpace(service)
	switch service {
	case "turn_on", "turn_off":
		return assistantStringInList(domainValue, []string{"light", "switch", "fan", "input_boolean", "automation", "humidifier"}) ||
			(service == "turn_on" && assistantStringInList(domainValue, []string{"scene", "script"}))
	case "open_cover", "close_cover":
		return domainValue == "cover"
	case "lock", "unlock":
		return domainValue == "lock"
	case "press":
		return domainValue == "button" || domainValue == "input_button"
	default:
		return false
	}
}

func (s *Server) auditAssistantHomeAssistantControl(ctx context.Context, homeID string, userID string, requestID string, action *assistantPendingHAControl, outcome string, reason string) {
	if action == nil || strings.TrimSpace(action.EntityID) == "" {
		return
	}
	metadata := map[string]any{
		"initiator": "hank_ai",
		"operation": strings.Trim(strings.TrimSpace(action.Domain)+"."+strings.TrimSpace(action.Service), "."),
		"outcome":   outcome,
	}
	if strings.TrimSpace(reason) != "" {
		metadata["reason"] = strings.TrimSpace(reason)
	}
	severity := auditSeverityInfo
	if outcome != "succeeded" {
		severity = auditSeverityWarning
	}
	s.audit(ctx, "homeassistant.control."+outcome, severity, userID, "", homeID, requestID, "homeassistant_entity", action.EntityID, metadata)
}

func (s *Server) searchFiles(ctx context.Context, homeID string, query string) ([]protocol.FileItem, error) {
	sourceIDs := s.assistantFileIndexSourceIDs(ctx, homeID)
	matches := make([]protocol.FileItem, 0)
	var firstErr error
	for _, sourceID := range sourceIDs {
		sourceMatches, err := s.searchFilesSource(ctx, homeID, sourceID, query)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.logger.Warn("assistant file source search failed", "error_code", "operation_failed")
			continue
		}
		matches = append(matches, sourceMatches...)
	}
	if len(matches) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(matches, func(i, j int) bool {
		left := fileMatchScore(matches[i], query)
		right := fileMatchScore(matches[j], query)
		if left == right {
			if matches[i].SourceID == matches[j].SourceID {
				return matches[i].Path < matches[j].Path
			}
			return matches[i].SourceID < matches[j].SourceID
		}
		return left > right
	})
	return dedupeFileItems(matches), nil
}

func (s *Server) searchFilesSource(ctx context.Context, homeID string, sourceID string, query string) ([]protocol.FileItem, error) {
	envelope, err := s.sendAgentCommand(ctx, homeID, "files.search", protocol.FilesSearchRequest{SourceID: strings.TrimSpace(sourceID), Query: query, Limit: 25})
	if err != nil {
		return nil, err
	}
	if envelope.Error == nil {
		payload, err := protocol.DecodePayload[protocol.FilesSearchResponse](envelope)
		if err != nil {
			return nil, err
		}
		sort.Slice(payload.Items, func(i, j int) bool {
			left := fileMatchScore(payload.Items[i], query)
			right := fileMatchScore(payload.Items[j], query)
			if left == right {
				return payload.Items[i].Path < payload.Items[j].Path
			}
			return left > right
		})
		return payload.Items, nil
	}

	type queueItem struct {
		path  string
		depth int
	}
	queue := []queueItem{{path: "", depth: 0}}
	var matches []protocol.FileItem
	visited := 0

	for len(queue) > 0 && visited < 200 {
		current := queue[0]
		queue = queue[1:]
		visited++

		envelope, err := s.sendAgentCommand(ctx, homeID, "files.list", protocol.FilesListRequest{SourceID: strings.TrimSpace(sourceID), Path: current.path})
		if err != nil {
			return nil, err
		}
		if envelope.Error != nil {
			return nil, errors.New(envelope.Error.Message)
		}
		payload, err := protocol.DecodePayload[protocol.FilesListResponse](envelope)
		if err != nil {
			return nil, err
		}

		for _, item := range payload.Items {
			score := fileMatchScore(item, query)
			if score > 0 {
				matches = append(matches, item)
			}
			if item.IsDirectory && current.depth < 3 {
				queue = append(queue, queueItem{path: item.Path, depth: current.depth + 1})
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		left := fileMatchScore(matches[i], query)
		right := fileMatchScore(matches[j], query)
		if left == right {
			return matches[i].Path < matches[j].Path
		}
		return left > right
	})
	return matches, nil
}

func dedupeFileItems(items []protocol.FileItem) []protocol.FileItem {
	seen := map[string]bool{}
	deduped := make([]protocol.FileItem, 0, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.SourceID) + "\x00" + strings.TrimSpace(item.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, item)
	}
	return deduped
}

func (s *Server) planCalendarTool(prompt string, timezone string, deviceID string) (assistantCalendarPlan, bool) {
	intent, ok := parseCalendarCreateIntent(prompt, timezone)
	if !ok {
		return assistantCalendarPlan{}, false
	}
	endsAt := intent.endsAt
	if endsAt.IsZero() {
		endsAt = intent.startsAt.Add(24 * time.Hour)
	}
	arguments := map[string]interface{}{
		"title":      intent.title,
		"starts_at":  intent.startsAt.Format(time.RFC3339),
		"ends_at":    endsAt.Format(time.RFC3339),
		"is_all_day": intent.allDay,
	}
	if deviceID != "" {
		arguments["device_id"] = deviceID
	}
	return assistantCalendarPlan{
		request: assistantClientToolRequest{
			ToolName:  "calendar.create_event",
			Arguments: arguments,
		},
		title:                intent.title,
		dateText:             intent.rawDateText,
		requiresConfirmation: !intent.explicitYear,
		confirmationMessage:  fmt.Sprintf("Confirm creating `%s` on %s.", intent.title, intent.rawDateText),
	}, true
}

func assistantSessionToAPI(session domain.AssistantSession) assistantAPISession {
	return assistantAPISession{
		ID:            session.ID,
		Title:         session.Title,
		LastMessageAt: session.LastMessageAt,
		CreatedAt:     session.CreatedAt,
		UpdatedAt:     session.UpdatedAt,
	}
}

func assistantMessageToAPI(message domain.AssistantMessage) assistantAPIMessage {
	api := assistantAPIMessage{
		ID:        message.ID,
		Role:      message.Role,
		CreatedAt: message.CreatedAt,
	}
	var content assistantMessageContent
	if err := json.Unmarshal([]byte(message.ContentJSON), &content); err == nil {
		api.Text = content.Text
		api.Cards = content.Cards
		api.Sources = content.Sources
		api.Diagnostics = assistantDiagnosticsFromContent(content)
	}
	return api
}

func attachAssistantDiagnostics(content *assistantMessageContent, tool assistantTool, intent assistantIntent) {
	if content == nil {
		return
	}
	if content.Meta == nil {
		content.Meta = make(map[string]interface{})
	}
	content.Meta["diagnostics"] = assistantDiagnosticsForIntent(tool, intent)
}

func assistantDiagnosticsForIntent(tool assistantTool, intent assistantIntent) assistantDiagnostics {
	diagnostics := assistantDiagnostics{
		ToolKind:   string(tool.Kind),
		IntentKind: string(intent.Kind),
		Query:      strings.TrimSpace(intent.Query),
	}
	if intent.MediaSelection != nil {
		diagnostics.MediaSelectionTitle = intent.MediaSelection.Title
		diagnostics.MediaSelectionPath = intent.MediaSelection.Path
	}
	if intent.AppID != "" {
		diagnostics.AppID = intent.AppID
	}
	if intent.CommandID != "" {
		diagnostics.CommandID = intent.CommandID
	}
	if intent.SlashCommand != "" {
		diagnostics.SlashCommand = intent.SlashCommand
	}
	return diagnostics
}

func assistantDiagnosticsFromContent(content assistantMessageContent) *assistantDiagnostics {
	if content.Meta == nil {
		return nil
	}
	raw, ok := content.Meta["diagnostics"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var diagnostics assistantDiagnostics
	if err := json.Unmarshal(encoded, &diagnostics); err != nil {
		return nil
	}
	if diagnostics.ToolKind == "" && diagnostics.IntentKind == "" && diagnostics.Query == "" {
		return nil
	}
	return &diagnostics
}

func assistantPendingActionFromContent(content assistantMessageContent) *assistantPendingAction {
	if content.Meta == nil {
		return nil
	}
	raw, ok := content.Meta["pending_action"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var pending assistantPendingAction
	if err := json.Unmarshal(encoded, &pending); err != nil {
		return nil
	}
	if pending.Kind == "" {
		return nil
	}
	return &pending
}

func assistantPendingActionSummaryFromAction(pending assistantPendingAction) *assistantPendingActionSummary {
	switch pending.Kind {
	case "note_append":
		if pending.NoteAppend == nil {
			return nil
		}
		action := pending.NoteAppend
		details := []assistantPendingActionDetail{
			{Label: "Target note", Value: action.TargetTitle},
			{Label: "Text to add", Value: action.AppendedText},
		}
		if action.TargetScope != "" {
			details = append(details, assistantPendingActionDetail{Label: "Scope", Value: action.TargetScope})
		}
		if action.MatchHint != "" {
			details = append(details, assistantPendingActionDetail{Label: "Matched search", Value: action.MatchHint})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Add to note",
			Summary:      "Hank will append text to an existing note after you approve it.",
			Confirmation: defaultString(action.Confirmation, fmt.Sprintf("Confirm adding `%s` to `%s`.", action.AppendedText, action.TargetTitle)),
			Details:      details,
			Destructive:  false,
		}
	case "note_create":
		if pending.NoteCreate == nil {
			return nil
		}
		action := pending.NoteCreate
		details := []assistantPendingActionDetail{
			{Label: "Title", Value: action.Title},
			{Label: "Scope", Value: defaultString(action.Scope, "profile")},
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Create note",
			Summary:      "Hank will create a new personal note after you approve it.",
			Confirmation: defaultString(action.Confirmation, fmt.Sprintf("Confirm creating `%s`.", action.Title)),
			Details:      details,
			Destructive:  false,
		}
	case "calendar_create":
		if pending.CalendarCreate == nil {
			return nil
		}
		action := pending.CalendarCreate
		details := []assistantPendingActionDetail{
			{Label: "Event", Value: action.Title},
			{Label: "Requested date", Value: action.DateText},
		}
		if startsAt := assistantToolArgumentString(action.ToolRequest.Arguments, "starts_at"); startsAt != "" {
			details = append(details, assistantPendingActionDetail{Label: "Starts", Value: startsAt})
		}
		if endsAt := assistantToolArgumentString(action.ToolRequest.Arguments, "ends_at"); endsAt != "" {
			details = append(details, assistantPendingActionDetail{Label: "Ends", Value: endsAt})
		}
		if isAllDay, ok := action.ToolRequest.Arguments["is_all_day"].(bool); ok {
			value := "No"
			if isAllDay {
				value = "Yes"
			}
			details = append(details, assistantPendingActionDetail{Label: "All day", Value: value})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Create calendar event",
			Summary:      "Hank will ask this device to create the calendar event after you approve it.",
			Confirmation: defaultString(action.Confirmation, fmt.Sprintf("Confirm creating `%s` on %s.", action.Title, action.DateText)),
			Details:      details,
			Destructive:  false,
		}
	case "calendar_update", "calendar_delete":
		if pending.CalendarClient == nil {
			return nil
		}
		action := pending.CalendarClient
		title := "Update calendar event"
		summary := "Hank will ask this device to update the calendar event after you approve it."
		if pending.Kind == "calendar_delete" {
			title = "Delete calendar event"
			summary = "Hank will ask this device to delete the calendar event after you approve it."
		}
		details := []assistantPendingActionDetail{
			{Label: "Event", Value: action.Title},
			{Label: "Tool", Value: action.ToolRequest.ToolName},
		}
		if action.Query != "" {
			details = append(details, assistantPendingActionDetail{Label: "Matched search", Value: action.Query})
		}
		if startsAt := assistantToolArgumentString(action.ToolRequest.Arguments, "starts_at"); startsAt != "" {
			details = append(details, assistantPendingActionDetail{Label: "Starts", Value: startsAt})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        title,
			Summary:      summary,
			Confirmation: action.Confirmation,
			Details:      details,
			Destructive:  action.Destructive,
		}
	case "attachment_commit":
		if pending.AttachmentCommit == nil {
			return nil
		}
		action := pending.AttachmentCommit
		title := "Store attachment"
		summary := "Hank will ask this device to store the staged upload after you approve it."
		details := []assistantPendingActionDetail{
			{Label: "Files", Value: fmt.Sprintf("%d", len(action.AttachmentIDs))},
			{Label: "Conflict mode", Value: defaultString(action.ConflictMode, "rename")},
		}
		if action.DestinationKind == "note_attachment" {
			title = "Attach to note"
			details = append(details,
				assistantPendingActionDetail{Label: "Target note", Value: action.TargetTitle},
				assistantPendingActionDetail{Label: "Scope", Value: action.TargetScope},
			)
		}
		if action.DestinationKind == "smb" {
			title = "Store in File Server"
			details = append(details, assistantPendingActionDetail{Label: "Target folder", Value: action.TargetPath})
			if action.SourceID != "" {
				details = append(details, assistantPendingActionDetail{Label: "Source", Value: action.SourceID})
			}
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        title,
			Summary:      summary,
			Confirmation: action.Confirmation,
			Details:      details,
			Destructive:  false,
		}
	case "homeassistant_control":
		if pending.HomeAssistant == nil {
			return nil
		}
		action := pending.HomeAssistant
		details := []assistantPendingActionDetail{
			{Label: "Entity", Value: firstNonBlank(action.EntityName, action.EntityID)},
			{Label: "Entity ID", Value: action.EntityID},
			{Label: "Command", Value: strings.ReplaceAll(action.Service, "_", " ")},
		}
		if action.CurrentState != "" {
			details = append(details, assistantPendingActionDetail{Label: "Current state", Value: action.CurrentState})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Control Home Assistant entity",
			Summary:      "Hank will send this exact service call through the primary Hank Agent after you approve it.",
			Confirmation: action.Confirmation,
			Details:      details,
			Destructive:  action.HighImpact,
		}
	case "file_create_folder":
		if pending.FileCreateFolder == nil {
			return nil
		}
		action := pending.FileCreateFolder
		details := []assistantPendingActionDetail{
			{Label: "Path", Value: action.Path},
		}
		if action.SourceID != "" {
			details = append(details, assistantPendingActionDetail{Label: "Source", Value: action.SourceID})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Create File Server folder",
			Summary:      "Hank will ask the primary Hank Agent to create this folder after you approve it.",
			Confirmation: action.Confirmation,
			Details:      details,
			Destructive:  false,
		}
	case "media_download":
		if pending.MediaDownload == nil {
			return nil
		}
		action := pending.MediaDownload
		details := []assistantPendingActionDetail{
			{Label: "Title", Value: action.Title},
			{Label: "Items", Value: fmt.Sprintf("%d", action.ItemCount)},
			{Label: "Destination", Value: defaultString(action.DestinationPath, "Media root")},
			{Label: "1080p", Value: fmt.Sprintf("%d", action.PreferredQualityCount)},
		}
		if scope := mediaFilterSummary(action.Filter); scope != "" {
			details = append(details, assistantPendingActionDetail{Label: "Scope", Value: scope})
		}
		if action.FallbackQualityCount > 0 {
			details = append(details, assistantPendingActionDetail{Label: "720p fallbacks", Value: fmt.Sprintf("%d", action.FallbackQualityCount)})
		}
		if action.MissingLinkCount > 0 {
			details = append(details, assistantPendingActionDetail{Label: "Missing links", Value: fmt.Sprintf("%d", action.MissingLinkCount)})
		}
		if action.ExistingCount > 0 {
			details = append(details, assistantPendingActionDetail{Label: "Already present", Value: fmt.Sprintf("%d", action.ExistingCount)})
		}
		return &assistantPendingActionSummary{
			Kind:         pending.Kind,
			Title:        "Download media",
			Summary:      "Hank will start an agent-side background download after you approve it.",
			Confirmation: defaultString(action.Confirmation, fmt.Sprintf("Confirm downloading `%s`.", action.Title)),
			Details:      details,
			Destructive:  false,
		}
	default:
		return nil
	}
}

func assistantToolArgumentString(arguments map[string]interface{}, key string) string {
	value, ok := arguments[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func assistantPromptWithContext(prompt string, contexts []domain.AssistantRetrievedContext) string {
	var builder strings.Builder
	builder.WriteString("Answer using only the Hank context below. When a source path or title is available, cite it naturally in the answer. If the context is incomplete, say what is missing instead of filling gaps.\n\n")
	builder.WriteString("User request:\n")
	builder.WriteString(prompt)
	builder.WriteString("\n\nHank context:\n")
	for index, item := range contexts {
		builder.WriteString(fmt.Sprintf("%d. Source: %s\n", index+1, item.SourceType))
		if item.Title != "" {
			builder.WriteString("Title: " + item.Title + "\n")
		}
		if item.Path != "" {
			builder.WriteString("Path: " + item.Path + "\n")
		}
		if item.SourceID != "" {
			builder.WriteString("Source ID: " + item.SourceID + "\n")
		}
		builder.WriteString(fmt.Sprintf("Relevance: %.3f\n", item.Score))
		if item.Snippet != "" {
			builder.WriteString("Snippet: " + item.Snippet + "\n")
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

func fallbackRetrievedAnswer(prompt string, contexts []domain.AssistantRetrievedContext) string {
	if len(contexts) == 0 {
		return "I could not find matching Hank context yet."
	}
	top := contexts[0]
	label := firstNonBlank(top.Path, top.Title, top.SourceID, top.SourceType)
	return fmt.Sprintf("I found `%s` as the closest HankAI match for `%s`.", label, strings.TrimSpace(prompt))
}

func assistantResultCardFromContext(item domain.AssistantRetrievedContext) assistantResultCard {
	switch item.SourceType {
	case "profile_note", "shared_note":
		return assistantResultCard{
			Kind:        "note",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open in Notes",
			NoteID:      item.Path,
			SearchText:  item.Snippet,
		}
	case "calendar_event":
		targetDate := parseAssistantResultTime(item.Path)
		return assistantResultCard{
			Kind:        "calendar",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open in Calendar",
			EventID:     item.SourceID,
			TargetDate:  targetDate,
		}
	case "file":
		return assistantResultCard{
			Kind:        "file",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open in File Server",
			SourceID:    item.SourceID,
			Path:        item.Path,
		}
	case "homeassistant_entity":
		return assistantResultCard{
			Kind:        "homeassistant",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open Dashboard",
			SearchText:  item.SourceID,
		}
	case assistantProjectDocSourceType:
		return assistantResultCard{
			Kind:        "project_doc",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open Project Doc",
			Path:        item.Path,
			SearchText:  item.SourceID,
		}
	default:
		return assistantResultCard{}
	}
}

func assistantResultCardFromHomeAssistantState(state protocol.HomeAssistantState) assistantResultCard {
	entityID := strings.TrimSpace(state.EntityID)
	title := homeAssistantFriendlyName(state)
	if title == "" {
		title = entityID
	}
	stateText := strings.TrimSpace(state.State)
	summary := entityID
	if stateText != "" {
		summary = fmt.Sprintf("%s: %s", entityID, stateText)
	}
	return assistantResultCard{
		Kind:        "homeassistant",
		Title:       title,
		Summary:     summary,
		ActionTitle: "Open in Dashboard",
		Path:        entityID,
		SearchText:  entityID,
	}
}

func (s *Server) authorizedAssistantRun(ctx context.Context, home domain.Home, auth authContext, runID string) (domain.AssistantRun, domain.AssistantSession, error) {
	run, err := s.store.GetAssistantRun(ctx, runID)
	if err != nil {
		return domain.AssistantRun{}, domain.AssistantSession{}, err
	}
	session, err := s.store.GetAssistantSession(ctx, run.SessionID)
	if err != nil {
		return domain.AssistantRun{}, domain.AssistantSession{}, err
	}
	if session.HomeID != home.ID || session.UserID != auth.User.ID {
		return domain.AssistantRun{}, domain.AssistantSession{}, store.ErrNotFound
	}
	return run, session, nil
}

func (s *Server) assistantRunResponseForSession(ctx context.Context, session domain.AssistantSession, run domain.AssistantRun) assistantRunResponse {
	response := assistantRunResponse{
		ID:                   run.ID,
		State:                run.State,
		RequiresClientTools:  run.RequiresClientTools,
		RequiresConfirmation: run.RequiresConfirmation,
	}
	if run.State == assistantStateCompleted || run.RequiresConfirmation {
		if messages, err := s.store.ListAssistantMessages(ctx, session.ID); err == nil {
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == assistantRoleAssistant {
					message := assistantMessageToAPI(messages[i])
					response.AssistantMessage = &message
					response.Diagnostics = message.Diagnostics
					break
				}
			}
		}
		if run.RequiresConfirmation && run.PendingActionJSON != "" {
			var pending assistantPendingAction
			if json.Unmarshal([]byte(run.PendingActionJSON), &pending) == nil {
				response.PendingActionSummary = assistantPendingActionSummaryFromAction(pending)
			}
		}
	} else if run.PendingActionJSON != "" {
		var pending assistantClientToolRequest
		if json.Unmarshal([]byte(run.PendingActionJSON), &pending) == nil {
			response.ClientToolRequest = &pending
		}
	}
	return response
}

func needsNoteAppendConfirmation(ranked []assistantRankedNote) bool {
	if len(ranked) == 0 {
		return false
	}
	if len(ranked) > 1 &&
		ranked[1].MatchTier == ranked[0].MatchTier &&
		ranked[1].Score >= ranked[0].Score-50 {
		return true
	}
	return false
}

func assistantSessionTitle(prompt string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(prompt, "\n", " "))
	if len(trimmed) <= 42 {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:42]) + "..."
}

func classifyAssistantIntent(prompt string) assistantIntent {
	if isCalendarCreatePrompt(prompt) {
		return assistantIntent{Kind: assistantIntentCalendarCreate, Query: strings.TrimSpace(prompt)}
	}
	_, intent := resolveAssistantTool(prompt)
	return intent
}

func hermesCommandPrompt(prompt string) (string, bool) {
	return slashCommandPrompt(prompt, "hermes")
}

func gramatonCommandPrompt(prompt string) (string, bool) {
	return slashCommandPrompt(prompt, "gramaton")
}

func ydownloadCommandPrompt(prompt string) (string, bool) {
	return slashCommandPrompt(prompt, "ydownload")
}

func slashCommandPrompt(prompt string, command string) (string, bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "", false
	}
	command = "/" + strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command, "/")))
	lowered := strings.ToLower(trimmed)
	if lowered != command && !strings.HasPrefix(lowered, command+" ") && !strings.HasPrefix(lowered, command+"\n") && !strings.HasPrefix(lowered, command+"\t") {
		return "", false
	}
	return strings.TrimSpace(trimmed[len(command):]), true
}

func assistantHermesConversationID(homeID string, userID string, session *domain.AssistantSession) string {
	parts := []string{"hank", sanitizeHermesScopePart(homeID), sanitizeHermesScopePart(userID)}
	if session != nil {
		parts = append(parts, sanitizeHermesScopePart(session.ID))
	}
	return strings.Join(parts, ":")
}

func assistantHermesSessionKey(homeID string, userID string, session *domain.AssistantSession) string {
	parts := []string{"agent", "main", "hank", "home", sanitizeHermesScopePart(homeID), "user", sanitizeHermesScopePart(userID)}
	if session != nil {
		parts = append(parts, "session", sanitizeHermesScopePart(session.ID))
	}
	return strings.Join(parts, ":")
}

func sanitizeHermesScopePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func isNoteAppendPrompt(prompt string) bool {
	itemText, noteHint := extractAppendIntent(prompt)
	if strings.TrimSpace(itemText) == "" || strings.TrimSpace(noteHint) == "" {
		return false
	}
	loweredHint := strings.ToLower(noteHint)
	return strings.Contains(loweredHint, "note") ||
		strings.Contains(loweredHint, "list") ||
		strings.Contains(loweredHint, "grocery") ||
		strings.Contains(loweredHint, "groceries") ||
		strings.Contains(loweredHint, "shopping") ||
		strings.Contains(loweredHint, "store") ||
		strings.Contains(loweredHint, "todo") ||
		strings.Contains(loweredHint, "to-do")
}

func isProjectDocsPrompt(lowered string) bool {
	if strings.Contains(lowered, "product intent") || strings.Contains(lowered, "project intent") {
		return true
	}
	if strings.Contains(lowered, "source path") && (strings.Contains(lowered, "cite") || strings.Contains(lowered, "source")) {
		return true
	}
	if strings.Contains(lowered, "source path") && strings.Contains(lowered, "hank") {
		return true
	}
	if strings.Contains(lowered, "hank context") && (strings.Contains(lowered, "hank") || strings.Contains(lowered, "project") || strings.Contains(lowered, "supposed to do")) {
		return true
	}
	projectTerms := []string{
		"agents.md",
		"readme",
		"server_sync",
		"server sync",
		"project doc",
		"docs/",
		"runbook",
		"hankserverside",
		"hank project",
		"hank server",
		"deployment",
		"deploy",
		"docker",
		"compose",
		"cloudflare",
		"postgres",
		"pgvector",
		"setup",
		"architecture.md",
		"codebase",
		"repo",
		"repository",
	}
	for _, term := range projectTerms {
		if strings.Contains(lowered, term) {
			return true
		}
	}
	return strings.Contains(lowered, ".md") && (strings.Contains(lowered, "what") || strings.Contains(lowered, "say") || strings.Contains(lowered, "docs"))
}

func isHomeAssistantPrompt(lowered string) bool {
	if strings.Contains(lowered, "home assistant") || strings.Contains(lowered, "hass") || strings.Contains(lowered, " entit") {
		return true
	}
	terms := []string{
		"light", "lights",
		"sensor", "sensors",
		"switch", "switches",
		"fan", "fans",
		"thermostat", "thermostats",
		"cover", "covers",
		"lock", "locks",
		"garage door",
	}
	for _, term := range terms {
		if strings.Contains(lowered, term) {
			return true
		}
	}
	return false
}

func isHomeAssistantMutationPrompt(prompt string) bool {
	if _, ok := parseHomeAssistantControlPrompt(prompt); ok {
		return true
	}
	lowered := strings.ToLower(normalizeHomeAssistantActionPrompt(prompt))
	mutationPrefixes := []string{"turn ", "switch ", "set ", "open ", "close ", "lock ", "unlock ", "run ", "activate ", "press "}
	for _, prefix := range mutationPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

func isNoteListPrompt(lowered string) bool {
	if !(strings.Contains(lowered, "note") || strings.Contains(lowered, "notes")) {
		return false
	}
	return strings.Contains(lowered, "what notes") ||
		strings.Contains(lowered, "which notes") ||
		strings.Contains(lowered, "all notes") ||
		strings.Contains(lowered, "list notes") ||
		strings.Contains(lowered, "list my notes") ||
		strings.Contains(lowered, "notes do i have") ||
		strings.Contains(lowered, "notes can i access") ||
		strings.Contains(lowered, "notes are there")
}

func isNoteSearchPrompt(lowered string) bool {
	if !(strings.Contains(lowered, "note") ||
		strings.Contains(lowered, "notes") ||
		strings.Contains(lowered, "grocery") ||
		strings.Contains(lowered, "groceries") ||
		strings.Contains(lowered, "shopping list") ||
		strings.Contains(lowered, "store list") ||
		strings.Contains(lowered, "todo list") ||
		strings.Contains(lowered, "to-do list")) {
		return false
	}
	return hasSearchVerb(lowered) ||
		strings.Contains(lowered, "where is") ||
		strings.Contains(lowered, "where are") ||
		strings.Contains(lowered, "show me")
}

func isFileSearchPrompt(lowered string) bool {
	if strings.Contains(lowered, "file") ||
		strings.Contains(lowered, "folder") ||
		strings.Contains(lowered, "directory") ||
		strings.Contains(lowered, "smb") ||
		strings.Contains(lowered, "share") ||
		strings.Contains(lowered, "document") ||
		strings.Contains(lowered, "pdf") ||
		strings.Contains(lowered, " tax") ||
		strings.Contains(lowered, "tax ") ||
		strings.Contains(lowered, "taxes") {
		return true
	}
	return false
}

func hasSearchVerb(lowered string) bool {
	searchPrefixes := []string{"find ", "search ", "search for ", "look for ", "open ", "show ", "show me ", "locate ", "where is ", "where are "}
	for _, prefix := range searchPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

func assistantPromptAllowsProjectDocs(prompt string) bool {
	return isProjectDocsPrompt(strings.ToLower(prompt))
}

func rankScoredNotes(notes []domain.UserNote, query string) []assistantRankedNote {
	query = normalizeAssistantNoteQuery(query)
	if query == "" {
		return nil
	}
	queryTokens := assistantQueryTokens(query)
	scoredNotes := make([]assistantRankedNote, 0, len(notes))
	for _, note := range notes {
		tier, score := scoreAssistantNoteMatch(note, query, queryTokens)
		if score > 0 {
			scoredNotes = append(scoredNotes, assistantRankedNote{Note: note, Score: score, MatchTier: tier})
		}
	}
	sort.Slice(scoredNotes, func(i, j int) bool {
		if scoredNotes[i].MatchTier != scoredNotes[j].MatchTier {
			return scoredNotes[i].MatchTier > scoredNotes[j].MatchTier
		}
		if scoredNotes[i].Score == scoredNotes[j].Score {
			return scoredNotes[i].Note.UpdatedAt.After(scoredNotes[j].Note.UpdatedAt)
		}
		return scoredNotes[i].Score > scoredNotes[j].Score
	})
	return scoredNotes
}

func scoreAssistantNoteMatch(note domain.UserNote, query string, queryTokens []string) (int, int) {
	title := normalizeAssistantNoteQuery(note.Title)
	noteKey := normalizeAssistantNoteQuery(note.NoteID)
	content := normalizeAssistantNoteQuery(strings.Join([]string{note.Content, note.BodyMarkdown}, " "))

	switch {
	case title == query || noteKey == query:
		return 4, 4000 + noteTitleTokenScore(title, queryTokens)
	case strings.Contains(title, query):
		return 3, 3000 + noteTitleTokenScore(title, queryTokens)
	case allAssistantTokensInText(queryTokens, title):
		return 3, 2600 + noteTitleTokenScore(title, queryTokens)
	case strings.Contains(content, query):
		return 2, 1800 + noteContentTokenScore(content, queryTokens)
	default:
		contentScore := noteContentTokenScore(content, queryTokens)
		if contentScore > 0 {
			return 1, 900 + contentScore
		}
	}
	return 0, 0
}

func normalizeAssistantNoteQuery(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", " ", "-", " ", "/", " ").Replace(value)
	value = removeQueryWords(value, map[string]bool{
		"a": true, "an": true, "the": true,
		"note": true, "notes": true,
		"list": true, "called": true, "named": true, "titled": true,
	})
	return strings.Join(strings.Fields(value), " ")
}

func assistantQueryTokens(query string) []string {
	seen := map[string]bool{}
	tokens := make([]string, 0)
	for _, token := range strings.Fields(query) {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

func noteTitleTokenScore(title string, tokens []string) int {
	score := 0
	for _, token := range tokens {
		if strings.Contains(title, token) {
			score += 20
		}
	}
	return score
}

func noteContentTokenScore(content string, tokens []string) int {
	score := 0
	for _, token := range tokens {
		if strings.Contains(content, token) {
			score += 5
		}
	}
	return score
}

func allAssistantTokensInText(tokens []string, text string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}

func rankNotes(notes []domain.UserNote, query string) []domain.UserNote {
	scored := rankScoredNotes(notes, query)
	results := make([]domain.UserNote, 0, len(scored))
	for _, item := range scored {
		results = append(results, item.Note)
	}
	return results
}

func extractAppendIntent(prompt string) (string, string) {
	trimmed := strings.TrimSpace(prompt)
	if itemText, noteHint, ok := extractLeadingAppendIntent(trimmed, trimmed); ok {
		return itemText, noteHint
	}
	for _, candidate := range appendInstructionCandidates(trimmed) {
		if itemText, noteHint, ok := extractLeadingAppendIntent(candidate, trimmed); ok {
			return itemText, noteHint
		}
	}
	return trimmed, ""
}

func extractLeadingAppendIntent(candidate string, fullPrompt string) (string, string, bool) {
	candidate = strings.TrimSpace(candidate)
	lowered := strings.ToLower(candidate)
	prefixLength := 0
	switch {
	case strings.HasPrefix(lowered, "add "):
		prefixLength = len("add ")
	case strings.HasPrefix(lowered, "append "):
		prefixLength = len("append ")
	case strings.HasPrefix(lowered, "attach "):
		prefixLength = len("attach ")
	default:
		return "", "", false
	}
	rest := strings.TrimSpace(candidate[prefixLength:])
	loweredRest := strings.ToLower(rest)
	for _, delimiter := range []string{" to the ", " to ", " into the ", " into ", " onto the ", " onto ", " the the "} {
		if index := strings.Index(loweredRest, delimiter); index >= 0 {
			itemText := strings.TrimSpace(strings.TrimSuffix(rest[:index], "."))
			noteHint := cleanNoteHint(removeAssistantURLs(rest[index+len(delimiter):]))
			if noteHint == "" {
				return "", "", false
			}
			return resolveReferencedAppendText(fullPrompt, itemText), noteHint, true
		}
	}
	return "", "", false
}

func appendInstructionCandidates(value string) []string {
	seen := map[string]bool{}
	var candidates []string
	addCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	for _, line := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		addCandidate(line)
	}
	lowered := strings.ToLower(value)
	for _, prefix := range []string{"add ", "append ", "attach "} {
		start := 0
		for {
			index := strings.Index(lowered[start:], prefix)
			if index < 0 {
				break
			}
			index += start
			if index == 0 || isAssistantInstructionBoundary(value[index-1]) {
				addCandidate(value[index:])
			}
			start = index + len(prefix)
		}
	}
	return candidates
}

func isAssistantInstructionBoundary(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '.' || value == ';' || value == ':'
}

func resolveReferencedAppendText(prompt string, itemText string) string {
	itemText = strings.TrimSpace(itemText)
	if !isReferencedAppendText(itemText) {
		return itemText
	}
	urls := extractAssistantURLs(prompt)
	if len(urls) == 0 {
		return itemText
	}
	return strings.Join(urls, "\n")
}

func isReferencedAppendText(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.Trim(value, "\"'` .")))
	switch normalized {
	case "it", "this", "that",
		"link", "this link", "that link", "the link",
		"url", "this url", "that url", "the url",
		"website", "this website", "that website", "the website":
		return true
	default:
		return false
	}
}

func extractAssistantURLs(value string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(value) {
		candidate := cleanAssistantURLToken(field)
		lowered := strings.ToLower(candidate)
		if !(strings.HasPrefix(lowered, "https://") || strings.HasPrefix(lowered, "http://")) {
			continue
		}
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		urls = append(urls, candidate)
	}
	return urls
}

func removeAssistantURLs(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		candidate := cleanAssistantURLToken(field)
		lowered := strings.ToLower(candidate)
		if strings.HasPrefix(lowered, "https://") || strings.HasPrefix(lowered, "http://") {
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(kept, " ")
}

func cleanAssistantURLToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'`<>()[]{}")
	value = strings.TrimRight(value, ".,;!?")
	return strings.TrimSpace(value)
}

func cleanNoteHint(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, "."))
	if strings.HasPrefix(strings.ToLower(value), "the ") {
		value = strings.TrimSpace(value[len("the "):])
	}
	return strings.TrimSpace(value)
}

func noteSearchQuery(prompt string) string {
	query := stripAssistantSearchPrefix(prompt)
	query = strings.TrimSuffix(strings.TrimSpace(query), ".")
	if explicit := explicitNotesAboutQuery(query); explicit != "" {
		return explicit
	}
	query = removeQueryWords(query, map[string]bool{
		"a": true, "an": true, "the": true,
		"note": true, "notes": true,
		"called": true, "named": true, "titled": true,
	})
	if strings.TrimSpace(query) == "" {
		return strings.TrimSpace(prompt)
	}
	return query
}

func explicitNotesAboutQuery(value string) string {
	trimmed := strings.TrimSpace(value)
	lowered := strings.ToLower(trimmed)
	for _, marker := range []string{"notes about ", "note about ", "notes on ", "note on "} {
		if index := strings.Index(lowered, marker); index >= 0 {
			query := strings.TrimSpace(trimmed[index+len(marker):])
			query = strings.TrimRight(query, ".,;!?")
			return strings.TrimSpace(query)
		}
	}
	return ""
}

func attachmentDestinationKind(prompt string) string {
	lowered := strings.ToLower(prompt)
	if strings.Contains(lowered, "note") || strings.Contains(lowered, "notes") {
		return "note_attachment"
	}
	if strings.Contains(lowered, "smb") ||
		strings.Contains(lowered, "file server") ||
		strings.Contains(lowered, "share") ||
		strings.Contains(lowered, "folder") ||
		strings.Contains(lowered, "directory") {
		return "smb"
	}
	return ""
}

func attachmentNoteQuery(prompt string) string {
	query := noteSearchQuery(prompt)
	query = removeQueryWords(query, map[string]bool{
		"add": true, "append": true, "put": true, "store": true, "save": true, "upload": true,
		"attach": true, "attachment": true, "file": true, "image": true,
		"photo": true, "picture": true, "link": true, "url": true, "uploaded": true,
		"document": true, "this": true, "these": true, "my": true, "in": true,
		"into": true, "to": true, "on": true,
	})
	query = removeAssistantURLs(query)
	return strings.TrimSpace(query)
}

func attachmentSMBPath(prompt string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(prompt, "."))
	if trimmed == "" {
		return ""
	}
	if quoted := firstQuotedValue(trimmed); quoted != "" {
		return cleanAttachmentSMBPath(quoted)
	}
	lowered := strings.ToLower(trimmed)
	if folderIndex := strings.Index(lowered, " folder"); folderIndex > 0 {
		prefix := strings.TrimSpace(trimmed[:folderIndex])
		prefixLowered := strings.ToLower(prefix)
		if inIndex := strings.LastIndex(prefixLowered, " in "); inIndex >= 0 {
			prefix = prefix[inIndex+len(" in "):]
		} else if intoIndex := strings.LastIndex(prefixLowered, " into "); intoIndex >= 0 {
			prefix = prefix[intoIndex+len(" into "):]
		} else if toIndex := strings.LastIndex(prefixLowered, " to "); toIndex >= 0 {
			prefix = prefix[toIndex+len(" to "):]
		}
		prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "the ")
		prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "my ")
		if cleaned := cleanAttachmentSMBPath(prefix); cleaned != "" {
			return cleaned
		}
	}
	for _, marker := range []string{" folder on", " folder in", " folder", " directory", " smb share", " share", " file server", " in ", " into ", " to "} {
		index := strings.Index(lowered, marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(trimmed[index+len(marker):])
		value = strings.TrimPrefix(value, "the ")
		value = strings.TrimPrefix(value, "my ")
		value = strings.TrimSuffix(value, " on the SMB share")
		value = strings.TrimSuffix(value, " on smb share")
		value = strings.TrimSuffix(value, " in file server")
		value = strings.TrimSuffix(value, " on file server")
		if cleaned := cleanAttachmentSMBPath(value); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

func firstQuotedValue(value string) string {
	for _, quote := range []string{"\"", "'", "`"} {
		start := strings.Index(value, quote)
		if start < 0 {
			continue
		}
		end := strings.Index(value[start+1:], quote)
		if end < 0 {
			continue
		}
		return value[start+1 : start+1+end]
	}
	return ""
}

func cleanAttachmentSMBPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'` ")
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimSuffix(value, "/")
	lowered := strings.ToLower(value)
	for _, suffix := range []string{" folder", " directory", " smb share", " share"} {
		if strings.HasSuffix(lowered, suffix) {
			value = strings.TrimSpace(value[:len(value)-len(suffix)])
			lowered = strings.ToLower(value)
		}
	}
	return strings.TrimSpace(value)
}

func assistantAttachmentListLabel(attachments []assistantMessageAttachment) string {
	if len(attachments) == 1 {
		return fmt.Sprintf("`%s`", attachments[0].Filename)
	}
	return fmt.Sprintf("%d uploaded files", len(attachments))
}

func fileQuery(prompt string) string {
	query := stripAssistantSearchPrefix(prompt)
	query = strings.TrimSuffix(strings.TrimSpace(query), ".")
	query = removeQueryWords(query, map[string]bool{
		"a": true, "an": true, "the": true, "all": true, "any": true, "every": true, "me": true,
		"file": true, "files": true,
		"folder": true, "folders": true,
		"directory": true, "directories": true,
		"smb": true, "share": true,
		"called": true, "named": true, "labeled": true,
	})
	if strings.TrimSpace(query) == "" {
		return strings.TrimSpace(prompt)
	}
	return query
}

func stripAssistantSearchPrefix(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	prefixes := []string{"can you find all ", "can you find ", "can you show me ", "can you show ", "please find all ", "please find ", "search for ", "look for ", "show me ", "where is ", "where are ", "find all ", "find ", "search ", "open ", "show ", "locate ", "get "}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lowered, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

func removeQueryWords(query string, stopWords map[string]bool) string {
	parts := strings.Fields(query)
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := strings.Trim(strings.ToLower(part), ".,?!:;\"'`()[]{}")
		if stopWords[clean] {
			continue
		}
		filtered = append(filtered, strings.Trim(part, ".,?!:;\"'`()[]{}"))
	}
	return strings.TrimSpace(strings.Join(filtered, " "))
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func assistantNoteSourceType(note domain.UserNote) string {
	if strings.TrimSpace(note.HomeID) != "" {
		return "shared_note"
	}
	return "profile_note"
}

type assistantHomeAssistantQuery struct {
	OnlyOn   bool
	Domain   string
	Terms    []string
	Display  string
	WantsAll bool
}

func parseHomeAssistantQuery(prompt string) assistantHomeAssistantQuery {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	normalized := strings.NewReplacer("?", " ", ".", " ", ",", " ", ":", " ", ";", " ").Replace(lowered)
	tokens := strings.Fields(normalized)
	query := assistantHomeAssistantQuery{
		OnlyOn: strings.Contains(lowered, " are on") ||
			strings.Contains(lowered, " is on") ||
			strings.Contains(lowered, " currently on") ||
			strings.HasSuffix(lowered, " on"),
		WantsAll: assistantPromptWantsAll(lowered),
	}
	stopWords := map[string]bool{
		"what": true, "which": true, "show": true, "list": true, "find": true,
		"can": true, "you": true, "please": true, "me": true, "all": true, "any": true, "are": true, "is": true,
		"there": true, "the": true, "a": true, "an": true, "in": true,
		"of": true, "for": true, "home": true, "assistant": true, "hass": true,
		"entity": true, "entities": true, "entitied": true, "currently": true,
		"on": true, "state": true, "states": true,
	}
	domainCandidates := make([]string, 0, 1)
	for _, token := range tokens {
		if domain, ok := homeAssistantDomainToken(token); ok {
			domainCandidates = append(domainCandidates, domain)
			query.Terms = append(query.Terms, domain)
			continue
		}
		if stopWords[token] {
			continue
		}
		query.Terms = append(query.Terms, token)
	}
	query.Terms = uniqueStrings(query.Terms)
	if len(domainCandidates) > 0 && len(query.Terms) == len(domainCandidates) {
		query.Domain = domainCandidates[0]
		query.Terms = nil
	}
	query.Display = homeAssistantQueryDisplay(prompt)
	if query.Display == "" {
		query.Display = "entities"
	}
	return query
}

func homeAssistantQueryDisplay(prompt string) string {
	query := stripAssistantSearchPrefix(prompt)
	query = removeQueryWords(query, map[string]bool{
		"what": true, "which": true, "show": true, "list": true, "find": true,
		"can": true, "you": true, "please": true, "me": true, "all": true, "any": true, "are": true, "is": true,
		"there": true, "the": true, "a": true, "an": true, "in": true,
		"of": true, "for": true, "home": true, "assistant": true, "hass": true,
		"entity": true, "entities": true, "entitied": true, "currently": true,
		"on": true, "state": true, "states": true,
	})
	return strings.TrimSpace(query)
}

func assistantPromptWantsAll(lowered string) bool {
	return strings.Contains(lowered, " all ") ||
		strings.HasPrefix(lowered, "all ") ||
		strings.Contains(lowered, " every ") ||
		strings.HasPrefix(lowered, "every ") ||
		strings.Contains(lowered, "what ") ||
		strings.Contains(lowered, "which ") ||
		strings.Contains(lowered, "there")
}

func homeAssistantDomainToken(token string) (string, bool) {
	switch strings.TrimSpace(token) {
	case "light", "lights":
		return "light", true
	case "switch", "switches":
		return "switch", true
	case "sensor", "sensors":
		return "sensor", true
	case "binary_sensor", "binary_sensors":
		return "binary_sensor", true
	case "fan", "fans":
		return "fan", true
	case "thermostat", "thermostats", "climate":
		return "climate", true
	case "cover", "covers", "shade", "shades":
		return "cover", true
	case "lock", "locks":
		return "lock", true
	default:
		return "", false
	}
}

func matchingHomeAssistantStates(states []protocol.HomeAssistantState, query assistantHomeAssistantQuery) []protocol.HomeAssistantState {
	type scoredState struct {
		State protocol.HomeAssistantState
		Score int
	}
	scored := make([]scoredState, 0, len(states))
	for _, state := range states {
		if query.OnlyOn && strings.ToLower(strings.TrimSpace(state.State)) != "on" {
			continue
		}
		if query.Domain != "" && !homeAssistantDomainMatches(state.EntityID, query.Domain) {
			continue
		}
		score := homeAssistantStateMatchScore(state, query)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredState{State: state, Score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return strings.ToLower(homeAssistantStateLabel(scored[i].State)) < strings.ToLower(homeAssistantStateLabel(scored[j].State))
		}
		return scored[i].Score > scored[j].Score
	})
	matches := make([]protocol.HomeAssistantState, 0, len(scored))
	for _, item := range scored {
		matches = append(matches, item.State)
	}
	return matches
}

func homeAssistantDomainMatches(entityID string, domain string) bool {
	entityDomain := entityID
	if dot := strings.Index(entityID, "."); dot >= 0 {
		entityDomain = entityID[:dot]
	}
	if domain == "sensor" {
		return entityDomain == "sensor" || entityDomain == "binary_sensor"
	}
	if domain == "button" {
		return entityDomain == "button" || entityDomain == "input_button"
	}
	return entityDomain == domain
}

func homeAssistantStateMatchScore(state protocol.HomeAssistantState, query assistantHomeAssistantQuery) int {
	if len(query.Terms) == 0 {
		return 1
	}
	searchText := homeAssistantSearchText(state)
	compactText := compactSearchText(searchText)
	score := 0
	matchedTerms := 0
	for _, term := range query.Terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		compactTerm := compactSearchText(term)
		switch {
		case strings.Contains(searchText, term):
			score += 5
			matchedTerms++
		case compactTerm != "" && strings.Contains(compactText, compactTerm):
			score += 4
			matchedTerms++
		case fuzzyTokenMatch(searchText, term):
			score += 2
			matchedTerms++
		}
	}
	if matchedTerms == 0 {
		return 0
	}
	if matchedTerms < len(query.Terms) {
		return 0
	}
	score += matchedTerms * 2
	if homeAssistantDomainMatches(state.EntityID, query.Domain) {
		score += 3
	}
	return score
}

func homeAssistantSearchText(state protocol.HomeAssistantState) string {
	entityDomain := state.EntityID
	if dot := strings.Index(state.EntityID, "."); dot >= 0 {
		entityDomain = state.EntityID[:dot]
	}
	return strings.ToLower(strings.Join([]string{
		state.EntityID,
		strings.ReplaceAll(state.EntityID, "_", " "),
		entityDomain,
		state.State,
		homeAssistantFriendlyName(state),
		assistantAttributesText(state.Attributes),
	}, "\n"))
}

func compactSearchText(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func fuzzyTokenMatch(searchText string, term string) bool {
	if len(term) < 4 {
		return false
	}
	for _, token := range strings.Fields(searchText) {
		token = strings.Trim(token, ".,?!:;\"'`()[]{}_-")
		if len(token) < 4 {
			continue
		}
		if strings.HasPrefix(token, term) || strings.HasPrefix(term, token) {
			return true
		}
	}
	return false
}

func homeAssistantStateLabel(state protocol.HomeAssistantState) string {
	friendlyName := homeAssistantFriendlyName(state)
	if friendlyName == "" || friendlyName == state.EntityID {
		return state.EntityID
	}
	return fmt.Sprintf("%s (%s)", friendlyName, state.EntityID)
}

func homeAssistantFriendlyName(state protocol.HomeAssistantState) string {
	if state.Attributes == nil {
		return ""
	}
	if value, ok := state.Attributes["friendly_name"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func parseAssistantResultTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed
	}
	return nil
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func fileMatchScore(item protocol.FileItem, query string) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	name := strings.ToLower(item.Name)
	fullPath := strings.ToLower(item.Path)
	score := 0
	if strings.Contains(name, query) {
		score += 8
	}
	if strings.Contains(fullPath, query) {
		score += 5
	}
	for _, token := range strings.Fields(query) {
		if strings.Contains(name, token) {
			score += 3
		}
		if strings.Contains(fullPath, token) {
			score++
		}
	}
	if score > 0 && item.IsDirectory {
		score++
	}
	return score
}

func notePreview(content string) string {
	content = strings.TrimSpace(content)
	if len(content) <= 120 {
		return content
	}
	return strings.TrimSpace(content[:120]) + "..."
}

func appendAssistantNoteText(content string, itemText string) string {
	content = strings.TrimRight(strings.TrimSpace(content), "\n")
	itemText = strings.TrimSpace(itemText)
	prefix := "- "
	lastLine := ""
	if content != "" {
		lines := strings.Split(content, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if line := strings.TrimSpace(lines[i]); line != "" {
				lastLine = line
				break
			}
		}
	}
	switch {
	case strings.HasPrefix(lastLine, "- [ ] ") || strings.HasPrefix(strings.ToLower(lastLine), "- [x] "):
		prefix = "- [ ] "
	case strings.HasPrefix(lastLine, "* "):
		prefix = "* "
	case strings.HasPrefix(lastLine, "- "):
		prefix = "- "
	case leadingNumberedListValue(lastLine) > 0:
		prefix = fmt.Sprintf("%d. ", leadingNumberedListValue(lastLine)+1)
	}
	if content == "" {
		return prefix + itemText
	}
	return content + "\n" + prefix + itemText
}

func leadingNumberedListValue(line string) int {
	line = strings.TrimSpace(line)
	dot := strings.Index(line, ".")
	if dot <= 0 {
		return 0
	}
	for _, char := range line[:dot] {
		if char < '0' || char > '9' {
			return 0
		}
	}
	if dot+1 >= len(line) || line[dot+1] != ' ' {
		return 0
	}
	value, err := strconv.Atoi(line[:dot])
	if err != nil {
		return 0
	}
	return value
}

type assistantCalendarIntent struct {
	title        string
	startsAt     time.Time
	endsAt       time.Time
	rawDateText  string
	explicitYear bool
	allDay       bool
}

type assistantCalendarPlan struct {
	request              assistantClientToolRequest
	title                string
	dateText             string
	requiresConfirmation bool
	confirmationMessage  string
}

func parseCalendarCreateIntent(prompt string, timezone string) (assistantCalendarIntent, bool) {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	if !strings.HasPrefix(lowered, "add ") && !strings.HasPrefix(lowered, "create ") && !strings.HasPrefix(lowered, "schedule ") {
		return assistantCalendarIntent{}, false
	}

	location := assistantTimeLocation(timezone)
	if intent, ok := parseCalendarCreateIntentFromNaturalText(trimmed, location); ok {
		return intent, true
	}

	var splitToken string
	switch {
	case strings.Contains(lowered, " to "):
		splitToken = " to "
	case strings.Contains(lowered, " on "):
		splitToken = " on "
	default:
		return assistantCalendarIntent{}, false
	}

	pieces := strings.SplitN(strings.TrimSpace(prompt), splitToken, 2)
	if len(pieces) != 2 {
		return assistantCalendarIntent{}, false
	}

	title := cleanCalendarCreateTitle(pieces[0])
	dateText := strings.TrimSpace(strings.TrimSuffix(pieces[1], "."))
	if title == "" || dateText == "" {
		return assistantCalendarIntent{}, false
	}

	now := time.Now().In(location)
	for _, layout := range []string{"January 2 2006", "Jan 2 2006", "January 2", "Jan 2"} {
		parsed, err := time.ParseInLocation(layout, dateText, location)
		if err != nil {
			continue
		}
		if !strings.Contains(layout, "2006") {
			parsed = time.Date(now.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, location)
		}
		if parsed.Before(now.Add(-24 * time.Hour)) {
			parsed = time.Date(now.Year()+1, parsed.Month(), parsed.Day(), 0, 0, 0, 0, location)
		}
		return assistantCalendarIntent{
			title:        title,
			startsAt:     parsed,
			endsAt:       parsed.Add(24 * time.Hour),
			rawDateText:  dateText,
			explicitYear: strings.Contains(layout, "2006"),
			allDay:       true,
		}, true
	}
	return assistantCalendarIntent{}, false
}

func parseCalendarCreateIntentFromNaturalText(prompt string, location *time.Location) (assistantCalendarIntent, bool) {
	originalEventText := cleanCalendarCreateTitle(prompt)
	for _, prefix := range []string{"calendar event for ", "event for "} {
		if strings.HasPrefix(strings.ToLower(originalEventText), prefix) {
			originalEventText = strings.TrimSpace(originalEventText[len(prefix):])
			break
		}
	}
	date, label, ok := parseCalendarDayReference(originalEventText, location)
	if !ok {
		return assistantCalendarIntent{}, false
	}
	lowered := strings.ToLower(originalEventText)
	dateIndex := len(originalEventText)
	for _, token := range append([]string{"today", "tomorrow"}, calendarDayReferenceTokens()...) {
		if index := strings.Index(lowered, token); index >= 0 && index < dateIndex {
			dateIndex = index
		}
	}
	if dateIndex == len(originalEventText) {
		return assistantCalendarIntent{}, false
	}
	title := strings.Trim(strings.TrimSpace(originalEventText[:dateIndex]), "\"'` ,")
	title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(title), " on"), " for"))
	title = cleanCalendarCreateTitle(title)
	if title == "" {
		return assistantCalendarIntent{}, false
	}
	timeText := originalEventText[dateIndex:]
	if atIndex := strings.LastIndex(strings.ToLower(timeText), " at "); atIndex >= 0 {
		timeText = timeText[atIndex+len(" at "):]
	}
	hour, minute, hasTime := parseClockText(timeText)
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
	allDay := true
	end := start.Add(24 * time.Hour)
	if hasTime {
		start = time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, location)
		end = start.Add(time.Hour)
		allDay = false
	}
	return assistantCalendarIntent{
		title:        title,
		startsAt:     start,
		endsAt:       end,
		rawDateText:  label,
		explicitYear: assistantPromptHasExplicitYear(originalEventText),
		allDay:       allDay,
	}, true
}

func assistantPromptHasExplicitYear(prompt string) bool {
	for _, field := range strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(prompt)) {
		field = strings.Trim(field, " ")
		if len(field) != 4 {
			continue
		}
		year, err := strconv.Atoi(field)
		if err == nil && year >= 1900 && year <= 2500 {
			return true
		}
	}
	return false
}

func calendarDayReferenceTokens() []string {
	tokens := make([]string, 0, len(calendarWeekdayNames())+24)
	for _, weekday := range calendarWeekdayNames() {
		tokens = append(tokens, weekday.name)
	}
	for _, month := range []string{
		"january", "jan", "february", "feb", "march", "mar", "april", "apr", "may", "june", "jun",
		"july", "jul", "august", "aug", "september", "sep", "october", "oct", "november", "nov", "december", "dec",
	} {
		tokens = append(tokens, month)
	}
	return tokens
}

func cleanCalendarCreateTitle(value string) string {
	value = strings.TrimSpace(value)
	lowered := strings.ToLower(value)
	for _, prefix := range []string{"add ", "create ", "schedule "} {
		if strings.HasPrefix(lowered, prefix) {
			return strings.TrimSpace(value[len(prefix):])
		}
	}
	return value
}
