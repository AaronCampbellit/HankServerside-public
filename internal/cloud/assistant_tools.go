package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

type assistantToolRuntime struct {
	Home       domain.Home
	Membership domain.HomeMembership
	Auth       authContext
	Settings   domain.AssistantSettings
	Prompt     string
	Session    *domain.AssistantSession
	DeviceID   string
	Timezone   string
}

type assistantTool struct {
	Kind        assistantIntentKind
	Description string
	Match       func(prompt string) (assistantIntent, bool)
	Execute     func(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error)
}

func assistantSlashTool(kind assistantIntentKind, command string, description string, execute func(context.Context, *Server, assistantToolRuntime, assistantIntent) (assistantMessageContent, error)) assistantTool {
	return assistantTool{
		Kind:        kind,
		Description: description,
		Match: func(prompt string) (assistantIntent, bool) {
			query, ok := slashCommandPrompt(prompt, command)
			if !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: kind, Query: query}, true
		},
		Execute: execute,
	}
}

var assistantToolRegistry = []assistantTool{
	{
		Kind:        assistantIntentHermesChat,
		Description: "Route an explicit /Hermes prompt to the Hermes agent through the primary Hank Agent.",
		Match: func(prompt string) (assistantIntent, bool) {
			query, ok := hermesCommandPrompt(prompt)
			if !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentHermesChat, Query: query}, true
		},
		Execute: executeAssistantHermesChatTool,
	},
	{
		Kind:        assistantIntentGramatonCommand,
		Description: "Route an explicit /gramaton title search to the media download workflow.",
		Match: func(prompt string) (assistantIntent, bool) {
			query, ok := gramatonCommandPrompt(prompt)
			if !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentGramatonCommand, Query: query}, true
		},
		Execute: executeAssistantMediaSearchTool,
	},
	{
		Kind:        assistantIntentYDownloadCommand,
		Description: "Route an explicit /ydownload URL to the installed YDownload app.",
		Match: func(prompt string) (assistantIntent, bool) {
			query, ok := ydownloadCommandPrompt(prompt)
			if !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentYDownloadCommand, Query: query}, true
		},
		Execute: executeAssistantYDownloadTool,
	},
	assistantSlashTool(assistantIntentHACommand, "ha", "Route an explicit /ha query to Home Assistant state lookup.", executeAssistantHomeAssistantQueryTool),
	assistantSlashTool(assistantIntentFilesCommand, "files", "Route an explicit /files query to File Server search.", executeAssistantFilesSearchTool),
	assistantSlashTool(assistantIntentNotesCommand, "notes", "Route an explicit /notes query to note search or note listing.", executeAssistantNotesTool),
	assistantSlashTool(assistantIntentAppendCommand, "append", "Route an explicit /append instruction to the note append workflow.", executeAssistantNotesAppendTool),
	assistantSlashTool(assistantIntentCalendarCommand, "calendar", "Route an explicit /calendar query to calendar snapshot search.", executeAssistantCalendarSearchTool),
	assistantSlashTool(assistantIntentDocsCommand, "docs", "Route an explicit /docs query to Hank project documentation.", executeAssistantProjectDocsTool),
	assistantSlashTool(assistantIntentStatusCommand, "status", "Show enabled HankAI workflow surfaces and source status.", executeAssistantStatusCommandTool),
	{
		Kind:        assistantIntentAssistantStatus,
		Description: "Show HankAI provider, model, source, and index status.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isAssistantStatusPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentAssistantStatus, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantAssistantStatusTool,
	},
	{
		Kind:        assistantIntentAgentStatus,
		Description: "Show the current primary Hank Agent connection and capability status.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isAgentStatusPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentAgentStatus, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantAgentStatusTool,
	},
	{
		Kind:        assistantIntentSyncStatus,
		Description: "Show Home note sync status between cloud and primary Hank Agent.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isSyncStatusPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentSyncStatus, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantSyncStatusTool,
	},
	{
		Kind:        assistantIntentBackupStatus,
		Description: "Show storage, backup, and restore verification status for Home admins.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isBackupStatusPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentBackupStatus, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantBackupStatusTool,
	},
	{
		Kind:        assistantIntentNotesAppend,
		Description: "Append a short item to a uniquely matched Hank note or list.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isNoteAppendPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentNotesAppend, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantNotesAppendTool,
	},
	{
		Kind:        assistantIntentNotesCreate,
		Description: "Create a new personal Hank note after confirmation.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isNoteCreatePrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentNotesCreate, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantNotesCreateTool,
	},
	{
		Kind:        assistantIntentNotesSummarize,
		Description: "Summarize a uniquely matched note or a small matched set of notes.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isNoteSummarizePrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentNotesSummarize, Query: noteSummaryQuery(prompt)}, true
		},
		Execute: executeAssistantNotesSummarizeTool,
	},
	{
		Kind:        assistantIntentCalendarUpdate,
		Description: "Plan a confirmed device calendar event update.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isCalendarMutationUpdatePrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentCalendarUpdate, Query: calendarMutationQuery(prompt)}, true
		},
		Execute: executeAssistantCalendarUpdateTool,
	},
	{
		Kind:        assistantIntentCalendarDelete,
		Description: "Plan a confirmed device calendar event delete.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isCalendarDeletePrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentCalendarDelete, Query: calendarMutationQuery(prompt)}, true
		},
		Execute: executeAssistantCalendarDeleteTool,
	},
	{
		Kind:        assistantIntentReadOnlySynthesis,
		Description: "Read across multiple enabled Hank sources and synthesize a grounded answer.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isReadOnlyMultiSourcePrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentReadOnlySynthesis, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantReadOnlySynthesisTool,
	},
	{
		Kind:        assistantIntentCalendarSearch,
		Description: "Search indexed device calendar snapshots.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isCalendarSearchPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentCalendarSearch, Query: calendarSearchQuery(prompt)}, true
		},
		Execute: executeAssistantCalendarSearchTool,
	},
	{
		Kind:        assistantIntentMemorySearch,
		Description: "Search private HankAI conversation memory.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isAssistantMemoryPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentMemorySearch, Query: assistantMemoryQuery(prompt)}, true
		},
		Execute: executeAssistantMemorySearchTool,
	},
	{
		Kind:        assistantIntentProjectDocs,
		Description: "Answer questions about Hank project documentation.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isProjectDocsPrompt(strings.ToLower(strings.TrimSpace(prompt))) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentProjectDocs, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantProjectDocsTool,
	},
	{
		Kind:        assistantIntentHomeAssistantControl,
		Description: "Plan a confirmed Home Assistant entity control through the home agent.",
		Match: func(prompt string) (assistantIntent, bool) {
			if _, ok := parseHomeAssistantControlPrompt(prompt); !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentHomeAssistantControl, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantHomeAssistantQueryTool,
	},
	{
		Kind:        assistantIntentHomeAssistantQuery,
		Description: "Read Home Assistant entity state through the primary Hank Agent.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isHomeAssistantPrompt(strings.ToLower(strings.TrimSpace(prompt))) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentHomeAssistantQuery, Query: homeAssistantQueryDisplay(prompt)}, true
		},
		Execute: executeAssistantHomeAssistantQueryTool,
	},
	{
		Kind:        assistantIntentNotesList,
		Description: "List all visible personal and shared Hank notes.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isNoteListPrompt(strings.ToLower(strings.TrimSpace(prompt))) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentNotesList, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantNotesTool,
	},
	{
		Kind:        assistantIntentNotesSearch,
		Description: "Search visible personal and shared Hank notes.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isNoteSearchPrompt(strings.ToLower(strings.TrimSpace(prompt))) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentNotesSearch, Query: noteSearchQuery(prompt)}, true
		},
		Execute: executeAssistantNotesTool,
	},
	{
		Kind:        assistantIntentMediaSearch,
		Description: "Search authorized media downloads through the primary Hank Agent.",
		Match: func(prompt string) (assistantIntent, bool) {
			query, ok := mediaAvailabilityQuery(prompt)
			if !ok {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentMediaSearch, Query: query}, true
		},
		Execute: executeAssistantMediaSearchTool,
	},
	{
		Kind:        assistantIntentFilesCreateFolder,
		Description: "Create a File Server folder after Hank confirmation.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isFileCreateFolderPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentFilesCreateFolder, Query: parseCreateFolderPath(prompt)}, true
		},
		Execute: executeAssistantFilesCreateFolderTool,
	},
	{
		Kind:        assistantIntentFilesListFolder,
		Description: "List a File Server folder's immediate contents.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isFileListFolderPrompt(prompt) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentFilesListFolder, Query: fileFolderQuery(prompt)}, true
		},
		Execute: executeAssistantFilesListFolderTool,
	},
	{
		Kind:        assistantIntentFilesSearch,
		Description: "Search indexed SMB files and folders through Hank.",
		Match: func(prompt string) (assistantIntent, bool) {
			if !isFileSearchPrompt(strings.ToLower(strings.TrimSpace(prompt))) {
				return assistantIntent{}, false
			}
			return assistantIntent{Kind: assistantIntentFilesSearch, Query: fileQuery(prompt)}, true
		},
		Execute: executeAssistantFilesSearchTool,
	},
	{
		Kind:        assistantIntentGeneral,
		Description: "Answer from already enabled Hank context when no specific tool matches.",
		Match: func(prompt string) (assistantIntent, bool) {
			return assistantIntent{Kind: assistantIntentGeneral, Query: strings.TrimSpace(prompt)}, true
		},
		Execute: executeAssistantGeneralTool,
	},
}

func resolveAssistantTool(prompt string) (assistantTool, assistantIntent) {
	for _, tool := range assistantToolRegistry {
		if tool.Match == nil {
			continue
		}
		intent, ok := tool.Match(prompt)
		if !ok {
			continue
		}
		if intent.Kind == "" {
			intent.Kind = tool.Kind
		}
		return tool, intent
	}
	fallback := assistantToolRegistry[len(assistantToolRegistry)-1]
	return fallback, assistantIntent{Kind: assistantIntentGeneral, Query: strings.TrimSpace(prompt)}
}

func (s *Server) resolveAssistantTool(ctx context.Context, home domain.Home, membership domain.HomeMembership, prompt string) (assistantTool, assistantIntent) {
	if tool, intent, ok := s.resolveInstalledAppSlashTool(ctx, home, membership, prompt); ok {
		return tool, intent
	}
	return resolveAssistantTool(prompt)
}

func (s *Server) resolveInstalledAppSlashTool(ctx context.Context, home domain.Home, membership domain.HomeMembership, prompt string) (assistantTool, assistantIntent, bool) {
	apps, err := s.store.ListHomeApps(ctx, home.ID)
	if err != nil {
		s.logger.Warn("assistant installed app metadata lookup failed", "home_id", home.ID, "error_code", "operation_failed")
		return assistantTool{}, assistantIntent{}, false
	}
	for _, app := range apps {
		var slashCommands []protocol.AppSlashCommand
		if err := json.Unmarshal([]byte(defaultJSONArray(app.SlashCommandsJSON)), &slashCommands); err != nil {
			continue
		}
		for _, slashCommand := range slashCommands {
			query, ok := slashCommandPrompt(prompt, slashCommand.Command)
			if !ok {
				continue
			}
			intent := assistantIntent{
				Kind:           assistantIntentInstalledAppCommand,
				Query:          query,
				AppID:          app.AppID,
				AppName:        app.Name,
				CommandID:      slashCommand.CommandID,
				SlashCommand:   slashCommand.Command,
				AppUnavailable: !canUseHomeAgentAppCommand(app, membership, slashCommand.CommandID),
			}
			return assistantTool{
				Kind:        assistantIntentInstalledAppCommand,
				Description: "Route an installed first-party app slash command through the primary Hank Agent.",
				Execute:     executeAssistantInstalledAppTool,
			}, intent, true
		}
	}
	return assistantTool{}, assistantIntent{}, false
}

func (s *Server) resolveAssistantToolWithLocalModel(ctx context.Context, settings domain.AssistantSettings, userID string, prompt string) (assistantTool, assistantIntent, bool) {
	if !settings.PlannerEnabled {
		return assistantTool{}, assistantIntent{}, false
	}
	if settings.AIProvider != "ollama" && !assistantPromptProfileIsLocal(settings.PromptProfile) {
		return assistantTool{}, assistantIntent{}, false
	}
	choices := make([]string, 0, len(assistantToolRegistry))
	allowed := map[assistantIntentKind]assistantTool{}
	for _, tool := range assistantToolRegistry {
		if tool.Kind == assistantIntentGeneral || tool.Kind == assistantIntentMediaSelection || strings.HasSuffix(string(tool.Kind), ".command") {
			continue
		}
		choices = append(choices, fmt.Sprintf("- %s: %s", tool.Kind, tool.Description))
		allowed[tool.Kind] = tool
	}
	plannerPrompt := strings.Join([]string{
		"Choose the best Hank tool for this user request.",
		"Return only compact JSON with keys tool and query.",
		"Use tool \"general\" when no listed tool clearly fits.",
		"Do not invent tools. Do not include explanations.",
		"Prefer project_docs for product intent, architecture, deployment, source path, README, AGENTS, runbook, repo, or codebase questions.",
		"Prefer assistant.memory_search only when the user asks what HankAI previously said, decided, remembered, or discussed.",
		"",
		"Available tools:",
		strings.Join(choices, "\n"),
		"",
		"User request:",
		strings.TrimSpace(prompt),
	}, "\n")
	plannerSettings := settings
	if strings.TrimSpace(plannerSettings.PlannerModel) != "" {
		plannerSettings.ChatModel = strings.TrimSpace(plannerSettings.PlannerModel)
	}
	answer, _, err := s.generateAssistantLLMResponseWithSettings(ctx, userID, plannerSettings, []assistantLLMMessage{
		{Role: "system", Content: "You classify HankAI requests into existing typed tools. Return JSON only."},
		{Role: "user", Content: plannerPrompt},
	})
	if err != nil {
		s.logger.Warn("assistant local planner failed; using deterministic tool", "error_code", "operation_failed")
		return assistantTool{}, assistantIntent{}, false
	}
	toolName, query := assistantPlannerChoice(answer)
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "assistant",
		Event:   "assistant.tool.local_planner_result",
		Summary: "Local model returned a HankAI planner decision.",
		Details: traceDetails(map[string]any{
			"selected_tool": toolName,
		}),
	})
	if toolName == assistantIntentGeneral || toolName == "" {
		return assistantTool{}, assistantIntent{}, false
	}
	tool, ok := allowed[toolName]
	if !ok {
		s.logger.Warn("assistant local planner selected unknown tool")
		return assistantTool{}, assistantIntent{}, false
	}
	if strings.TrimSpace(query) == "" {
		query = strings.TrimSpace(prompt)
	}
	return tool, assistantIntent{Kind: tool.Kind, Query: query}, true
}

func assistantPlannerChoice(answer string) (assistantIntentKind, string) {
	type plannerResponse struct {
		Tool  string `json:"tool"`
		Query string `json:"query"`
	}
	trimmed := strings.TrimSpace(answer)
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			trimmed = trimmed[start : end+1]
		}
	}
	var response plannerResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return "", ""
	}
	return assistantIntentKind(strings.TrimSpace(response.Tool)), strings.TrimSpace(response.Query)
}

func isReadOnlyMultiSourcePrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if lowered == "" {
		return false
	}
	writeTerms := []string{" add ", "append ", "create ", "delete ", "move ", "rename ", "update ", "change ", "download ", "turn on", "turn off", "set "}
	padded := " " + lowered + " "
	for _, term := range writeTerms {
		if strings.Contains(padded, term) {
			return false
		}
	}
	sourceHits := 0
	sourceGroups := [][]string{
		{"calendar", "schedule", "event", "tomorrow", "today", "appointment"},
		{"note", "notes", "list"},
		{"file", "folder", "smb", "share", "document"},
		{"home assistant", "entity", "entities", "light", "sensor", "thermostat"},
		{"project doc", "docs", "readme", "agents.md", "repo", "codebase", "source path"},
		{"remember", "previously", "prior chat", "decide", "decided", "discussed"},
	}
	for _, group := range sourceGroups {
		for _, term := range group {
			if strings.Contains(lowered, term) {
				sourceHits++
				break
			}
		}
	}
	if sourceHits < 2 {
		return false
	}
	connectors := []string{" and ", " also ", " plus ", " with ", " compared to ", " alongside "}
	for _, connector := range connectors {
		if strings.Contains(lowered, connector) {
			return true
		}
	}
	return strings.Contains(lowered, "?") && sourceHits >= 3
}

func executeAssistantReadOnlySynthesisTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	content, err := server.answerRetrievedPrompt(ctx, runtime.Home, runtime.Membership, runtime.Auth, runtime.Settings, runtime.Prompt)
	if err != nil {
		return assistantMessageContent{}, err
	}
	settings := normalizeAssistantSettings(runtime.Settings)
	if synthesisPromptMentionsCalendar(runtime.Prompt) && settings.CalendarEnabled {
		calendarPrompt := synthesisCalendarPrompt(runtime.Prompt)
		if calendarContent, err := server.answerCalendarSearchPrompt(ctx, runtime.Home.ID, runtime.Auth.User.ID, calendarPrompt, runtime.Timezone); err == nil {
			content.Cards = appendUniqueAssistantCards(content.Cards, calendarContent.Cards, 6)
		}
	}
	if synthesisPromptMentionsNotes(runtime.Prompt) && (settings.ProfileNotesEnabled || settings.HomeNotesEnabled) {
		notePrompt := synthesisNotePrompt(runtime.Prompt)
		noteRuntime := runtime
		noteContent, err := executeAssistantNotesTool(ctx, server, noteRuntime, assistantIntent{Kind: assistantIntentNotesSearch, Query: notePrompt})
		if err == nil {
			content.Cards = appendUniqueAssistantCards(content.Cards, noteContent.Cards, 6)
		}
	}
	return content, nil
}

func synthesisPromptMentionsCalendar(prompt string) bool {
	lowered := strings.ToLower(prompt)
	for _, term := range []string{"calendar", "schedule", "event", "tomorrow", "today", "appointment", "meeting"} {
		if strings.Contains(lowered, term) {
			return true
		}
	}
	return false
}

func synthesisPromptMentionsNotes(prompt string) bool {
	lowered := strings.ToLower(prompt)
	for _, term := range []string{"note", "notes", "list"} {
		if strings.Contains(lowered, term) {
			return true
		}
	}
	return false
}

func synthesisCalendarPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	for _, connector := range []string{" and ", " also ", " plus ", " alongside "} {
		if index := strings.Index(lowered, connector); index > 0 {
			left := strings.TrimSpace(trimmed[:index])
			if synthesisPromptMentionsCalendar(left) {
				return left
			}
		}
	}
	return trimmed
}

func synthesisNotePrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	for _, marker := range []string{"notes mention ", "note mention ", "notes about ", "note about ", "notes on ", "note on "} {
		if index := strings.Index(lowered, marker); index >= 0 {
			query := strings.TrimSpace(trimmed[index+len(marker):])
			return trimSynthesisSubquery(query)
		}
	}
	for _, connector := range []string{" and ", " also ", " plus ", " alongside "} {
		parts := strings.Split(trimmed, connector)
		for _, part := range parts {
			if synthesisPromptMentionsNotes(part) {
				return noteSearchQuery(part)
			}
		}
	}
	return noteSearchQuery(trimmed)
}

func trimSynthesisSubquery(query string) string {
	query = strings.TrimSpace(query)
	lowered := strings.ToLower(query)
	for _, connector := range []string{" and ", " also ", " plus ", " alongside ", " compared to "} {
		if index := strings.Index(lowered, connector); index > 0 {
			query = strings.TrimSpace(query[:index])
			break
		}
	}
	return strings.TrimRight(query, ".,;!?")
}

func appendUniqueAssistantCards(existing []assistantResultCard, extras []assistantResultCard, limit int) []assistantResultCard {
	if limit <= 0 {
		limit = len(existing) + len(extras)
	}
	seen := map[string]bool{}
	result := make([]assistantResultCard, 0, min(limit, len(existing)+len(extras)))
	for _, card := range append(existing, extras...) {
		if card.Kind == "" {
			continue
		}
		key := assistantCardKey(card)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, card)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func assistantCardKey(card assistantResultCard) string {
	return strings.Join([]string{
		card.Kind,
		card.NoteID,
		card.EventID,
		card.SourceID,
		card.Path,
		card.Title,
	}, "\x00")
}

func assistantCommandPrompt(intent assistantIntent, fallback string) string {
	if strings.TrimSpace(intent.Query) != "" || strings.HasSuffix(string(intent.Kind), ".command") {
		return strings.TrimSpace(intent.Query)
	}
	return strings.TrimSpace(fallback)
}

func executeAssistantStatusCommandTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	settings := normalizeAssistantSettings(runtime.Settings)
	rows := []string{
		fmt.Sprintf("- Chat: %s", enabledLabel(assistantSettingsHasEnabledSources(settings))),
		fmt.Sprintf("- Home Assistant: %s", enabledLabel(settings.HomeAssistantEnabled)),
		fmt.Sprintf("- Files: %s", enabledLabel(settings.FilesEnabled)),
		fmt.Sprintf("- Notes: %s", enabledLabel(settings.ProfileNotesEnabled || settings.HomeNotesEnabled)),
		fmt.Sprintf("- Calendar: %s", enabledLabel(settings.CalendarEnabled)),
		fmt.Sprintf("- Project docs: %s", enabledLabel(settings.ProjectDocsEnabled)),
		fmt.Sprintf("- Media downloads: %s", enabledLabel(settings.FilesEnabled)),
	}
	return assistantMessageContent{Text: "HankAI workflow status:\n" + strings.Join(rows, "\n")}, nil
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "off"
}

func executeAssistantFilesSearchTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.FilesEnabled {
		return assistantMessageContent{Text: "File access is turned off in HankAI settings."}, nil
	}
	prompt := runtime.Prompt
	if intent.Kind == assistantIntentFilesCommand {
		prompt = assistantCommandPrompt(intent, runtime.Prompt)
	}
	if strings.TrimSpace(prompt) == "" {
		return assistantMessageContent{Text: "Send `/files` followed by a file or folder name to search."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "File access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerFilePrompt(ctx, runtime.Home, runtime.Settings, prompt)
}

func executeAssistantMediaSearchTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.FilesEnabled {
		return assistantMessageContent{Text: "File access is turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "File access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	if intent.Kind == assistantIntentMediaSelection {
		if intent.MediaSelection == nil {
			return assistantMessageContent{Text: "I need a media option number or title from the last result list."}, nil
		}
		return server.answerMediaSelection(ctx, runtime.Home, *intent.MediaSelection)
	}
	if jobID, latest, ok := mediaCancelPrompt(intent.Query); ok {
		return server.answerMediaCancel(ctx, runtime.Home, jobID, latest)
	}
	if strings.TrimSpace(intent.Query) == "" {
		return assistantMessageContent{Text: "Send `/gramaton` followed by a movie or TV show title."}, nil
	}
	return server.answerMediaSearch(ctx, runtime.Home, intent.Query)
}

func executeAssistantNotesAppendTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ProfileNotesEnabled && !runtime.Settings.HomeNotesEnabled {
		return assistantMessageContent{Text: "Notes access is turned off in HankAI settings."}, nil
	}
	prompt := assistantCommandPrompt(intent, runtime.Prompt)
	if intent.Kind == assistantIntentAppendCommand {
		if strings.TrimSpace(prompt) == "" {
			return assistantMessageContent{Text: "Send `/append` followed by the text and target note, like `/append buy eggs to grocery list`."}, nil
		}
		prompt = "add " + prompt
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "Notes access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerAppendNotePrompt(ctx, runtime.Home, runtime.Auth, runtime.Settings, prompt)
}

func executeAssistantNotesTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ProfileNotesEnabled && !runtime.Settings.HomeNotesEnabled {
		return assistantMessageContent{Text: "Notes access is turned off in HankAI settings."}, nil
	}
	prompt := assistantCommandPrompt(intent, runtime.Prompt)
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "Notes access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	if intent.Kind == assistantIntentNotesList || (intent.Kind == assistantIntentNotesCommand && strings.TrimSpace(prompt) == "") {
		return server.answerNoteListPrompt(ctx, runtime.Home, runtime.Auth, runtime.Settings)
	}
	return server.answerNoteSearchPrompt(ctx, runtime.Home, runtime.Auth, runtime.Settings, prompt)
}

func executeAssistantHomeAssistantQueryTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.HomeAssistantEnabled {
		return assistantMessageContent{Text: "Home Assistant access is turned off in HankAI settings."}, nil
	}
	prompt := assistantCommandPrompt(intent, runtime.Prompt)
	if strings.TrimSpace(prompt) == "" {
		prompt = "entities"
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureHomeAssistant); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "Home Assistant access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	if _, ok := parseHomeAssistantControlPrompt(prompt); ok {
		return server.planHomeAssistantControl(ctx, runtime.Home, prompt)
	}
	if isHomeAssistantMutationPrompt(prompt) {
		return assistantMessageContent{
			Text: "I can safely control a specific Home Assistant entity with confirmation. Try `turn on the porch light`, `close the garage door`, `lock the front door`, `run the evening scene`, or `press the doorbell button`.",
		}, nil
	}
	return server.answerHomeAssistantPrompt(ctx, runtime.Home, prompt)
}

func executeAssistantHermesChatTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if runtime.Membership.Role != domain.HomeRoleAdmin {
		return assistantMessageContent{Text: "Hermes chat is only available to Home admins right now."}, nil
	}
	if strings.TrimSpace(intent.Query) == "" {
		return assistantMessageContent{Text: "Send `/Hermes` followed by the message you want Hermes to handle."}, nil
	}
	if server.agentHasCapability(runtime.Home.ID, "apps.hermes.chat") {
		return server.answerHermesAppPrompt(ctx, runtime.Home, runtime.Auth, runtime.Session, intent.Query)
	}
	return assistantMessageContent{Text: "Hermes chat is not configured on the primary Hank Agent yet."}, nil
}

func executeAssistantYDownloadTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if runtime.Membership.Role != domain.HomeRoleAdmin {
		return assistantMessageContent{Text: "YDownload is only available to Home admins right now."}, nil
	}
	if !runtime.Settings.FilesEnabled {
		return assistantMessageContent{Text: "File access is turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			return assistantMessageContent{Text: "File access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	if strings.TrimSpace(intent.Query) == "" {
		return assistantMessageContent{Text: "Send `/ydownload` followed by a YouTube URL."}, nil
	}
	if server.agentHasCapability(runtime.Home.ID, "apps.ydownload.download") {
		return server.answerYDownloadAppPrompt(ctx, runtime.Home, intent.Query)
	}
	return assistantMessageContent{Text: "YDownload is not configured on the primary Hank Agent yet."}, nil
}

func executeAssistantInstalledAppTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	appName := defaultString(intent.AppName, intent.AppID)
	if intent.AppUnavailable {
		return assistantMessageContent{Text: fmt.Sprintf("%s is not available to your Home account.", appName)}, nil
	}
	return server.answerGenericInstalledAppPrompt(ctx, runtime, intent)
}

func (s *Server) agentHasCapability(homeID string, capability string) bool {
	return hasCapabilities(s.agentCapabilities(homeID), capability)
}

func executeAssistantProjectDocsTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ProjectDocsEnabled {
		return assistantMessageContent{Text: "Project docs access is turned off in HankAI settings."}, nil
	}
	prompt := assistantCommandPrompt(intent, runtime.Prompt)
	if strings.TrimSpace(prompt) == "" {
		return assistantMessageContent{Text: "Send `/docs` followed by what you want to find in Hank docs."}, nil
	}
	return server.answerProjectDocPrompt(ctx, runtime.Home, runtime.Auth, runtime.Settings, prompt)
}

func executeAssistantGeneralTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !assistantSettingsHasEnabledSources(runtime.Settings) {
		return assistantMessageContent{Text: "All HankAI sources are turned off in AI Settings."}, nil
	}
	return server.answerRetrievedPrompt(ctx, runtime.Home, runtime.Membership, runtime.Auth, runtime.Settings, runtime.Prompt)
}
