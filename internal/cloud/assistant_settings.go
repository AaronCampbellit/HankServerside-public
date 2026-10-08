package cloud

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	legacyAssistantSystemPrompt         = "You are HankAI inside Hank. Answer only from the provided Hank context. If the context is not enough, say what is missing. Do not claim you changed notes, files, calendars, or Home Assistant unless a typed tool result says it already happened."
	defaultAssistantSystemPrompt        = chatGPTAssistantSystemPrompt
	chatGPTAssistantSystemPrompt        = "You are HankAI, the assistant inside Hank. Prefer typed Hank tools over guessing whenever the user asks about Notes, File Server or SMB shares, Calendar, Home Assistant, Hank project docs, or prior Hank chat. Treat those source names as user concepts and keep them distinct in answers. Stay grounded in the supplied Hank context; if a target note, folder, event, entity, share, calendar, or source is ambiguous, ask one short clarification. Never claim a write happened until a typed tool result or confirmed client-tool result says it happened. For attachments, distinguish staged metadata from raw bytes that the app or primary Hank Agent must commit. For Calendar, use device timezone context when present and ask for missing date, time, duration, calendar, or ambiguous event matches before writes. For project-doc answers, prefer current README, AGENTS, SERVER_SYNC, docs, deployment docs, and runbooks; cite the doc path or title. Treat external provider access as a privacy boundary: only use context included in the request and do not ask for secrets, passwords, API keys, or private tokens. For destructive or high-impact actions, explain the intended target and require Hank confirmation before execution."
	localAssistantSystemPrompt          = "You are HankAI running with a local model inside Hank. Use the supplied Hank context aggressively, but keep every answer grounded in that context and in typed Hank tool results. You may reason about likely user intent, propose a short tool plan, and ask one focused clarification when a note, folder, event, entity, share, calendar, or source is ambiguous. Never claim a write happened until a typed tool result or confirmed client-tool result says it happened. Do not invent file contents, calendar entries, Home Assistant states, or note text that is not present in the provided context. For destructive or high-impact actions, identify the intended target and require Hank confirmation before execution. Prefer concise practical answers over generic assistant prose."
	qwenLocalAssistantSystemPrompt      = localAssistantSystemPrompt + " For Qwen-style reasoning models, keep private reasoning out of the final answer, follow JSON-only planner instructions exactly, and cite Hank source paths when provided."
	llamaLocalAssistantSystemPrompt     = localAssistantSystemPrompt + " For Llama-style local models, be direct, use short grounded answers, and prefer explicit Hank source labels over broad summaries."
	smallFastLocalAssistantSystemPrompt = "You are HankAI using a small fast local model. Be concise. Prefer typed Hank tools and visible Hank context. Return strict JSON when asked to plan. Ask one clarification when needed. Never claim writes or device changes unless a typed result confirms them."

	maxAssistantContextItems       = 20
	maxAssistantSystemPromptBytes  = 6000
	maxAssistantChatModelBytes     = 120
	maxAssistantProviderBytes      = 40
	maxAssistantPromptProfileBytes = 40
	maxAssistantURLBytes           = 300
)

type assistantSettingsSource struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
}

type assistantSettingsTool struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Enabled      bool     `json:"enabled"`
	Status       string   `json:"status"`
	Description  string   `json:"description"`
	Requirements []string `json:"requirements,omitempty"`
}

type assistantSettingsResponse struct {
	Settings domain.AssistantSettings  `json:"settings"`
	Defaults map[string]any            `json:"defaults"`
	Sources  []assistantSettingsSource `json:"sources"`
	Tools    []assistantSettingsTool   `json:"tools"`
}

type assistantSettingsUpdateRequest struct {
	ProfileNotesEnabled  *bool   `json:"profile_notes_enabled"`
	HomeNotesEnabled     *bool   `json:"home_notes_enabled"`
	FilesEnabled         *bool   `json:"files_enabled"`
	CalendarEnabled      *bool   `json:"calendar_enabled"`
	HomeAssistantEnabled *bool   `json:"homeassistant_enabled"`
	ProjectDocsEnabled   *bool   `json:"project_docs_enabled"`
	ConversationsEnabled *bool   `json:"conversations_enabled"`
	SystemPrompt         *string `json:"system_prompt"`
	AIProvider           *string `json:"ai_provider"`
	OllamaBaseURL        *string `json:"ollama_base_url"`
	ChatModel            *string `json:"chat_model"`
	EmbeddingModel       *string `json:"embedding_model"`
	PromptProfile        *string `json:"prompt_profile"`
	PlannerEnabled       *bool   `json:"planner_enabled"`
	PlannerModel         *string `json:"planner_model"`
}

func (s *Server) handleAssistantSettings(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.currentAssistantSettings(r.Context(), home.ID, auth.User.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.assistantSettingsPayload(settings))
	case http.MethodPut:
		current, err := s.currentAssistantSettings(r.Context(), home.ID, auth.User.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var request assistantSettingsUpdateRequest
		if err := parseJSON(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := applyAssistantSettingsUpdate(current, request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		updated.UpdatedAt = now
		updated.UpdatedBy = auth.User.ID
		if updated.CreatedAt.IsZero() {
			updated.CreatedAt = now
		}
		if err := s.store.UpsertAssistantSettings(r.Context(), updated); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.assistantSettingsPayload(updated))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) currentAssistantSettings(ctx context.Context, homeID string, userID string) (domain.AssistantSettings, error) {
	settings, err := s.store.GetAssistantSettings(ctx, homeID, userID)
	if err == nil {
		normalized := normalizeAssistantSettings(settings)
		if assistantSettingsNeedPersistence(settings, normalized) {
			now := time.Now().UTC()
			normalized.UpdatedAt = now
			normalized.UpdatedBy = userID
			if normalized.CreatedAt.IsZero() {
				normalized.CreatedAt = now
			}
			if err := s.store.UpsertAssistantSettings(ctx, normalized); err != nil {
				return domain.AssistantSettings{}, err
			}
		}
		return normalized, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		defaults := defaultAssistantSettings(homeID, userID)
		if err := s.store.UpsertAssistantSettings(ctx, defaults); err != nil {
			return domain.AssistantSettings{}, err
		}
		return defaults, nil
	}
	return domain.AssistantSettings{}, err
}

func assistantSettingsNeedPersistence(current domain.AssistantSettings, normalized domain.AssistantSettings) bool {
	return current.SystemPrompt != normalized.SystemPrompt ||
		current.MaxContextItems != normalized.MaxContextItems ||
		current.AIProvider != normalized.AIProvider ||
		current.OllamaBaseURL != normalized.OllamaBaseURL ||
		current.ChatModel != normalized.ChatModel ||
		current.EmbeddingModel != normalized.EmbeddingModel ||
		current.PromptProfile != normalized.PromptProfile ||
		current.PlannerEnabled != normalized.PlannerEnabled ||
		current.PlannerModel != normalized.PlannerModel
}

func defaultAssistantSettings(homeID string, userID string) domain.AssistantSettings {
	now := time.Now().UTC()
	return domain.AssistantSettings{
		HomeID:               homeID,
		UserID:               userID,
		ProfileNotesEnabled:  true,
		HomeNotesEnabled:     true,
		FilesEnabled:         true,
		CalendarEnabled:      true,
		HomeAssistantEnabled: true,
		ProjectDocsEnabled:   true,
		ConversationsEnabled: true,
		SystemPrompt:         defaultAssistantSystemPrompt,
		MaxContextItems:      maxAssistantContextItems,
		PromptProfile:        "chatgpt",
		PlannerEnabled:       true,
		CreatedAt:            now,
		UpdatedAt:            now,
		UpdatedBy:            userID,
	}
}

func applyAssistantSettingsUpdate(settings domain.AssistantSettings, request assistantSettingsUpdateRequest) (domain.AssistantSettings, error) {
	if request.ProfileNotesEnabled != nil {
		settings.ProfileNotesEnabled = *request.ProfileNotesEnabled
	}
	if request.HomeNotesEnabled != nil {
		settings.HomeNotesEnabled = *request.HomeNotesEnabled
	}
	if request.FilesEnabled != nil {
		settings.FilesEnabled = *request.FilesEnabled
	}
	if request.CalendarEnabled != nil {
		settings.CalendarEnabled = *request.CalendarEnabled
	}
	if request.HomeAssistantEnabled != nil {
		settings.HomeAssistantEnabled = *request.HomeAssistantEnabled
	}
	if request.ProjectDocsEnabled != nil {
		settings.ProjectDocsEnabled = *request.ProjectDocsEnabled
	}
	if request.ConversationsEnabled != nil {
		settings.ConversationsEnabled = *request.ConversationsEnabled
	}
	if request.SystemPrompt != nil {
		settings.SystemPrompt = strings.TrimSpace(*request.SystemPrompt)
	}
	if request.AIProvider != nil {
		settings.AIProvider = strings.ToLower(strings.TrimSpace(*request.AIProvider))
	}
	if request.OllamaBaseURL != nil {
		settings.OllamaBaseURL = strings.TrimRight(strings.TrimSpace(*request.OllamaBaseURL), "/")
	}
	if request.ChatModel != nil {
		settings.ChatModel = strings.TrimSpace(*request.ChatModel)
	}
	if request.EmbeddingModel != nil {
		settings.EmbeddingModel = strings.TrimSpace(*request.EmbeddingModel)
	}
	if request.PromptProfile != nil {
		settings.PromptProfile = strings.ToLower(strings.TrimSpace(*request.PromptProfile))
		if settings.PromptProfile != "custom" {
			settings.SystemPrompt = assistantPromptForProfile(settings.PromptProfile)
		}
	}
	if request.PlannerEnabled != nil {
		settings.PlannerEnabled = *request.PlannerEnabled
	}
	if request.PlannerModel != nil {
		settings.PlannerModel = strings.TrimSpace(*request.PlannerModel)
	}
	settings = normalizeAssistantSettings(settings)
	if len(settings.SystemPrompt) > maxAssistantSystemPromptBytes {
		return domain.AssistantSettings{}, errors.New("system_prompt is too long")
	}
	if len(settings.AIProvider) > maxAssistantProviderBytes {
		return domain.AssistantSettings{}, errors.New("ai_provider is too long")
	}
	if len(settings.OllamaBaseURL) > maxAssistantURLBytes {
		return domain.AssistantSettings{}, errors.New("ollama_base_url is too long")
	}
	if len(settings.ChatModel) > maxAssistantChatModelBytes {
		return domain.AssistantSettings{}, errors.New("chat_model is too long")
	}
	if len(settings.EmbeddingModel) > maxAssistantChatModelBytes {
		return domain.AssistantSettings{}, errors.New("embedding_model is too long")
	}
	if len(settings.PromptProfile) > maxAssistantPromptProfileBytes {
		return domain.AssistantSettings{}, errors.New("prompt_profile is too long")
	}
	if len(settings.PlannerModel) > maxAssistantChatModelBytes {
		return domain.AssistantSettings{}, errors.New("planner_model is too long")
	}
	if !assistantProviderAllowed(settings.AIProvider) {
		return domain.AssistantSettings{}, errors.New("ai_provider is not supported")
	}
	if settings.OllamaBaseURL != "" && !assistantHTTPURLAllowed(settings.OllamaBaseURL) {
		return domain.AssistantSettings{}, errors.New("ollama_base_url must be an http or https URL")
	}
	if !assistantPromptProfileAllowed(settings.PromptProfile) {
		return domain.AssistantSettings{}, errors.New("prompt_profile is not supported")
	}
	if strings.ContainsAny(settings.ChatModel, " \t\r\n") {
		return domain.AssistantSettings{}, errors.New("chat_model cannot contain whitespace")
	}
	if strings.ContainsAny(settings.EmbeddingModel, " \t\r\n") {
		return domain.AssistantSettings{}, errors.New("embedding_model cannot contain whitespace")
	}
	if strings.ContainsAny(settings.PlannerModel, " \t\r\n") {
		return domain.AssistantSettings{}, errors.New("planner_model cannot contain whitespace")
	}
	return settings, nil
}

func normalizeAssistantSettings(settings domain.AssistantSettings) domain.AssistantSettings {
	trimmedPrompt := strings.TrimSpace(settings.SystemPrompt)
	if trimmedPrompt == "" || trimmedPrompt == legacyAssistantSystemPrompt {
		settings.SystemPrompt = defaultAssistantSystemPrompt
	} else {
		settings.SystemPrompt = trimmedPrompt
	}
	settings.AIProvider = strings.ToLower(strings.TrimSpace(settings.AIProvider))
	settings.OllamaBaseURL = strings.TrimRight(strings.TrimSpace(settings.OllamaBaseURL), "/")
	settings.ChatModel = strings.TrimSpace(settings.ChatModel)
	settings.EmbeddingModel = strings.TrimSpace(settings.EmbeddingModel)
	settings.PromptProfile = strings.ToLower(strings.TrimSpace(settings.PromptProfile))
	settings.PlannerModel = strings.TrimSpace(settings.PlannerModel)
	if settings.PromptProfile == "" {
		settings.PromptProfile = assistantPromptProfileForPrompt(settings.SystemPrompt)
	}
	settings.MaxContextItems = maxAssistantContextItems
	return settings
}

func assistantHTTPURLAllowed(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func assistantProviderAllowed(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "auto", "ollama", "openai", assistantProviderChatGPTCodex, "disabled":
		return true
	default:
		return false
	}
}

func assistantPromptProfileAllowed(profile string) bool {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", "chatgpt", "local", "qwen3-local", "llama-local", "small-fast-local", "custom":
		return true
	default:
		return false
	}
}

func assistantPromptProfileIsLocal(profile string) bool {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "local", "qwen3-local", "llama-local", "small-fast-local":
		return true
	default:
		return false
	}
}

func assistantPromptForProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "qwen3-local":
		return qwenLocalAssistantSystemPrompt
	case "llama-local":
		return llamaLocalAssistantSystemPrompt
	case "small-fast-local":
		return smallFastLocalAssistantSystemPrompt
	case "local":
		return localAssistantSystemPrompt
	case "chatgpt":
		return chatGPTAssistantSystemPrompt
	default:
		return defaultAssistantSystemPrompt
	}
}

func assistantPromptProfileForPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	switch trimmed {
	case "", legacyAssistantSystemPrompt, chatGPTAssistantSystemPrompt:
		return "chatgpt"
	case localAssistantSystemPrompt:
		return "local"
	case qwenLocalAssistantSystemPrompt:
		return "qwen3-local"
	case llamaLocalAssistantSystemPrompt:
		return "llama-local"
	case smallFastLocalAssistantSystemPrompt:
		return "small-fast-local"
	default:
		return "custom"
	}
}

func assistantPromptProfiles() []map[string]string {
	return []map[string]string{
		{
			"key":         "chatgpt",
			"label":       "ChatGPT/Codex",
			"description": "Strict external-provider prompt that minimizes private context and keeps typed-tool boundaries explicit.",
			"prompt":      chatGPTAssistantSystemPrompt,
		},
		{
			"key":         "local",
			"label":       "Local model",
			"description": "Planner-friendly prompt for local models while preserving Hank validation and confirmation boundaries.",
			"prompt":      localAssistantSystemPrompt,
		},
		{
			"key":         "qwen3-local",
			"label":       "Qwen local",
			"description": "Local prompt tuned for Qwen reasoning models, strict planner JSON, and no visible thinking.",
			"prompt":      qwenLocalAssistantSystemPrompt,
		},
		{
			"key":         "llama-local",
			"label":       "Llama local",
			"description": "Local prompt tuned for concise Llama-family answers and explicit source labels.",
			"prompt":      llamaLocalAssistantSystemPrompt,
		},
		{
			"key":         "small-fast-local",
			"label":       "Small fast local",
			"description": "Short prompt for small planner or utility models.",
			"prompt":      smallFastLocalAssistantSystemPrompt,
		},
		{
			"key":         "custom",
			"label":       "Custom",
			"description": "Use the edited prompt exactly as saved.",
			"prompt":      "",
		},
	}
}

func assistantProviderOptions() []map[string]string {
	return []map[string]string{
		{"key": "", "label": "Configured default"},
		{"key": "auto", "label": "Auto"},
		{"key": "ollama", "label": "Local Ollama"},
		{"key": "chatgpt_codex", "label": "Linked ChatGPT/Codex"},
		{"key": "openai", "label": "OpenAI API key"},
		{"key": "disabled", "label": "Local fallback only"},
	}
}

func (s *Server) assistantSettingsPayload(settings domain.AssistantSettings) assistantSettingsResponse {
	settings = normalizeAssistantSettings(settings)
	cfg := s.assistantAI
	cfg.normalize()
	return assistantSettingsResponse{
		Settings: settings,
		Defaults: map[string]any{
			"system_prompt":           defaultAssistantSystemPrompt,
			"prompt_profiles":         assistantPromptProfiles(),
			"ai_provider":             "",
			"provider_options":        assistantProviderOptions(),
			"ollama_base_url":         cfg.OllamaBaseURL,
			"max_context_items":       maxAssistantContextItems,
			"chat_model":              "",
			"chat_model_options":      assistantChatModelOptions(cfg),
			"embedding_model":         "",
			"planner_enabled":         true,
			"planner_model":           "",
			"embedding_model_options": assistantEmbeddingModelOptions(cfg),
		},
		Sources: assistantSettingsSources(settings),
		Tools:   assistantSettingsTools(settings),
	}
}

func (s *Server) agentCapabilities(homeID string) []string {
	if s == nil || s.router == nil {
		return nil
	}
	if agent, ok := s.router.GetAgent(homeID); ok {
		return append([]string(nil), agent.capabilities...)
	}
	return nil
}

func assistantSettingsSources(settings domain.AssistantSettings) []assistantSettingsSource {
	settings = normalizeAssistantSettings(settings)
	return []assistantSettingsSource{
		{
			Key:         "project_docs",
			Label:       "Project docs",
			Enabled:     settings.ProjectDocsEnabled,
			Description: "README, AGENTS, SERVER_SYNC, and docs markdown from HankServerside.",
		},
		{
			Key:         "assistant_conversation",
			Label:       "Past conversations",
			Enabled:     settings.ConversationsEnabled,
			Description: "Your private HankAI conversation history.",
		},
		{
			Key:         "profile_notes",
			Label:       "Personal notes",
			Enabled:     settings.ProfileNotesEnabled,
			Description: "Notes stored in your Hank profile.",
		},
		{
			Key:         "home_notes",
			Label:       "Shared notes",
			Enabled:     settings.HomeNotesEnabled,
			Description: "Notes shared with your Home.",
		},
		{
			Key:         "files",
			Label:       "Files",
			Enabled:     settings.FilesEnabled,
			Description: "File and folder names indexed from the primary Hank Agent.",
		},
		{
			Key:         "calendar",
			Label:       "Calendar",
			Enabled:     settings.CalendarEnabled,
			Description: "Calendar entries synced from Hank clients.",
		},
		{
			Key:         "homeassistant",
			Label:       "Home Assistant",
			Enabled:     settings.HomeAssistantEnabled,
			Description: "Home Assistant entity names and states.",
		},
	}
}

func assistantSettingsTools(settings domain.AssistantSettings) []assistantSettingsTool {
	settings = normalizeAssistantSettings(settings)
	notesReady := settings.ProfileNotesEnabled || settings.HomeNotesEnabled
	return []assistantSettingsTool{
		{
			Key:         "notes",
			Label:       "Notes",
			Enabled:     notesReady,
			Status:      enabledStatus(notesReady),
			Description: "Create, search, and update enabled note spaces after confirmation when required.",
		},
		{
			Key:         "files",
			Label:       "Files",
			Enabled:     settings.FilesEnabled,
			Status:      enabledStatus(settings.FilesEnabled),
			Description: "Search file names and route approved file work through the primary Hank Agent.",
		},
		{
			Key:         "calendar",
			Label:       "Calendar",
			Enabled:     settings.CalendarEnabled,
			Status:      enabledStatus(settings.CalendarEnabled),
			Description: "Prepare calendar actions for the Hank app confirmation flow.",
		},
		{
			Key:         "homeassistant",
			Label:       "Home Assistant",
			Enabled:     settings.HomeAssistantEnabled,
			Status:      enabledStatus(settings.HomeAssistantEnabled),
			Description: "Read Home Assistant state through the primary Hank Agent.",
		},
	}
}

func enabledStatus(enabled bool) string {
	if enabled {
		return "Ready"
	}
	return "Off"
}

func hasCapabilities(capabilities []string, required ...string) bool {
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		seen[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := seen[capability]; !ok {
			return false
		}
	}
	return true
}

func assistantSettingsAllowSource(settings domain.AssistantSettings, sourceType string) bool {
	settings = normalizeAssistantSettings(settings)
	switch sourceType {
	case "profile_note":
		return settings.ProfileNotesEnabled
	case "shared_note":
		return settings.HomeNotesEnabled
	case "file":
		return settings.FilesEnabled
	case "calendar_event":
		return settings.CalendarEnabled
	case "homeassistant_entity":
		return settings.HomeAssistantEnabled
	case assistantProjectDocSourceType:
		return settings.ProjectDocsEnabled
	case assistantConversationSourceType:
		return settings.ConversationsEnabled
	default:
		return false
	}
}

func assistantSettingsHasEnabledSources(settings domain.AssistantSettings) bool {
	for _, source := range assistantSettingsSources(settings) {
		if source.Enabled {
			return true
		}
	}
	return false
}

func assistantSettingsEnabledSourceLabels(settings domain.AssistantSettings) string {
	sources := assistantSettingsSources(settings)
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		if source.Enabled {
			labels = append(labels, source.Label)
		}
	}
	if len(labels) == 0 {
		return "no Hank sources"
	}
	return strings.Join(labels, ", ")
}
