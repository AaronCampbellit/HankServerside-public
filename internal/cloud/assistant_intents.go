package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

type assistantSlots struct {
	Action      string
	Object      string
	Payload     string
	Destination string
	Qualifiers  []string
}

func extractAssistantSlots(prompt string) assistantSlots {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	slots := assistantSlots{}
	actionTerms := []struct {
		action string
		terms  []string
	}{
		{"delete", []string{"delete", "remove", "cancel"}},
		{"rename", []string{"rename"}},
		{"move", []string{"move", "reschedule"}},
		{"check_off", []string{"check off", "mark done", "mark complete"}},
		{"uncheck", []string{"uncheck", "mark incomplete"}},
		{"summarize", []string{"summarize", "summary"}},
		{"create", []string{"create", "new ", "make", "schedule"}},
		{"upload", []string{"upload", "attach", "put this", "store this", "save this"}},
		{"append", []string{"append", "add"}},
		{"list", []string{"list", "show me", "show", "what do i have", "what's on", "whats on"}},
		{"open", []string{"open"}},
		{"find", []string{"find", "search", "look for", "locate", "where is", "where are", "what did we decide"}},
	}
	for _, candidate := range actionTerms {
		for _, term := range candidate.terms {
			if strings.Contains(lowered, term) {
				slots.Action = candidate.action
				break
			}
		}
		if slots.Action != "" {
			break
		}
	}
	objectTerms := []struct {
		object string
		terms  []string
	}{
		{"chat_history", []string{"chat history", "prior chat", "past chat", "conversation", "what did we decide"}},
		{"project_doc", []string{"agents.md", "readme", "server_sync", "project doc", "docs/", "runbook", "repo", "repository", "deployment", ".md"}},
		{"calendar", []string{"calendar", "calandar", "event", "events", "appointment", "meeting", "agenda", "what do i have"}},
		{"homeassistant", []string{"home assistant", "hass", "entity", "entities", "light", "sensor", "switch", "thermostat"}},
		{"file", []string{"file", "folder", "directory", "smb", "share", "pdf", "document", "tax", "taxes"}},
		{"note", []string{"note", "notes", "list", "grocery", "groceries", "todo", "to-do", "store list"}},
	}
	for _, candidate := range objectTerms {
		for _, term := range candidate.terms {
			if strings.Contains(lowered, term) {
				slots.Object = candidate.object
				break
			}
		}
		if slots.Object != "" {
			break
		}
	}
	for _, qualifier := range []string{"today", "tomorrow", "this week", "next week", "first result", "latest", "same note", "shared", "personal"} {
		if strings.Contains(lowered, qualifier) {
			slots.Qualifiers = append(slots.Qualifiers, qualifier)
		}
	}
	slots.Payload = strings.TrimSpace(trimmed)
	if itemText, noteHint := extractAppendIntent(trimmed); itemText != "" && noteHint != "" {
		slots.Payload = itemText
		slots.Destination = noteHint
	}
	if slots.Destination == "" {
		if path := attachmentSMBPath(trimmed); path != "" {
			slots.Destination = path
		}
	}
	return slots
}

func executeAssistantNotesCreateTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ProfileNotesEnabled {
		return assistantMessageContent{Text: "Personal notes are turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
		if errorsIsFeatureDenied(err) {
			return assistantMessageContent{Text: "Notes access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerNoteCreatePrompt(ctx, runtime.Prompt)
}

func executeAssistantNotesSummarizeTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ProfileNotesEnabled && !runtime.Settings.HomeNotesEnabled {
		return assistantMessageContent{Text: "Notes access is turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
		if errorsIsFeatureDenied(err) {
			return assistantMessageContent{Text: "Notes access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerNoteSummarizePrompt(ctx, runtime.Home, runtime.Auth, runtime.Settings, runtime.Prompt)
}

func executeAssistantFilesListFolderTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.FilesEnabled {
		return assistantMessageContent{Text: "File Server access is turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errorsIsFeatureDenied(err) {
			return assistantMessageContent{Text: "File access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerFileListFolderPrompt(ctx, runtime.Home, runtime.Prompt)
}

func executeAssistantFilesCreateFolderTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.FilesEnabled {
		return assistantMessageContent{Text: "File Server access is turned off in HankAI settings."}, nil
	}
	if err := server.requireHomeFeature(ctx, runtime.Home, runtime.Membership, runtime.Auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errorsIsFeatureDenied(err) {
			return assistantMessageContent{Text: "File access is disabled for your Home membership right now."}, nil
		}
		return assistantMessageContent{}, err
	}
	return server.answerFileCreateFolderPrompt(ctx, runtime.Home, runtime.Prompt)
}

func executeAssistantCalendarSearchTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.CalendarEnabled {
		return assistantMessageContent{Text: "Calendar access is turned off in HankAI settings."}, nil
	}
	prompt := assistantCommandPrompt(intent, runtime.Prompt)
	if strings.TrimSpace(prompt) == "" {
		prompt = "today"
	}
	return server.answerCalendarSearchPrompt(ctx, runtime.Home.ID, runtime.Auth.User.ID, prompt, "")
}

func executeAssistantCalendarUpdateTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.CalendarEnabled {
		return assistantMessageContent{Text: "Calendar access is turned off in HankAI settings."}, nil
	}
	return server.answerCalendarUpdatePrompt(ctx, runtime.Home.ID, runtime.Auth.User.ID, runtime.Prompt, "")
}

func executeAssistantCalendarDeleteTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.CalendarEnabled {
		return assistantMessageContent{Text: "Calendar access is turned off in HankAI settings."}, nil
	}
	return server.answerCalendarDeletePrompt(ctx, runtime.Home.ID, runtime.Auth.User.ID, runtime.Prompt, "")
}

func executeAssistantMemorySearchTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if !runtime.Settings.ConversationsEnabled {
		return assistantMessageContent{Text: "Past HankAI conversations are turned off in AI Settings."}, nil
	}
	return server.answerAssistantMemoryPrompt(ctx, runtime.Home.ID, runtime.Auth.User.ID, runtime.Session, runtime.Settings, runtime.Prompt)
}

func rankProjectDocContexts(contexts []domain.AssistantRetrievedContext, prompt string) {
	sort.SliceStable(contexts, func(i, j int) bool {
		return contexts[i].Score > contexts[j].Score
	})
}

func errorsIsFeatureDenied(err error) bool {
	return errors.Is(err, errFeaturePermissionDenied)
}

func isAssistantStatusPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if lowered == "" {
		return false
	}
	assistantTerms := []string{
		"assistant status",
		"hankai status",
		"hank ai status",
		"assistant source",
		"assistant index",
		"hankai source",
		"hankai index",
		"ai settings",
		"ai status",
		"local model",
		"ollama status",
		"model is hankai using",
		"model hankai is using",
	}
	for _, term := range assistantTerms {
		if strings.Contains(lowered, term) {
			return true
		}
	}
	return strings.Contains(lowered, "hankai") && (strings.Contains(lowered, "provider") || strings.Contains(lowered, "model") || strings.Contains(lowered, "index"))
}

func isAgentStatusPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if lowered == "" {
		return false
	}
	return strings.Contains(lowered, "agent status") ||
		strings.Contains(lowered, "primary hank agent status") ||
		strings.Contains(lowered, "primary hank agent online") ||
		strings.Contains(lowered, "is the agent online") ||
		strings.Contains(lowered, "agent capabilities")
}

func isSyncStatusPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if lowered == "" {
		return false
	}
	return strings.Contains(lowered, "sync status") ||
		strings.Contains(lowered, "notes sync") ||
		strings.Contains(lowered, "home sync") ||
		strings.Contains(lowered, "sync health")
}

func isBackupStatusPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if lowered == "" {
		return false
	}
	return strings.Contains(lowered, "backup status") ||
		strings.Contains(lowered, "backups status") ||
		strings.Contains(lowered, "storage status") ||
		strings.Contains(lowered, "restore status") ||
		strings.Contains(lowered, "restore verification") ||
		strings.Contains(lowered, "pgbackrest")
}

func isNoteCreatePrompt(prompt string) bool {
	slots := extractAssistantSlots(prompt)
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return slots.Object == "note" && slots.Action == "create" && !strings.Contains(lowered, "calendar")
}

func isNoteSummarizePrompt(prompt string) bool {
	slots := extractAssistantSlots(prompt)
	return slots.Object == "note" && slots.Action == "summarize"
}

func isFileListFolderPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if !strings.Contains(lowered, "folder") && !strings.Contains(lowered, "directory") {
		return false
	}
	if strings.Contains(lowered, "create") || strings.Contains(lowered, "new folder") || strings.Contains(lowered, "make folder") {
		return false
	}
	return strings.HasPrefix(lowered, "show ") || strings.HasPrefix(lowered, "show me ") ||
		strings.HasPrefix(lowered, "list ") || strings.HasPrefix(lowered, "open ") ||
		strings.Contains(lowered, "contents")
}

func isFileCreateFolderPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return (strings.Contains(lowered, "create") || strings.Contains(lowered, "make") || strings.Contains(lowered, "new")) &&
		(strings.Contains(lowered, "folder") || strings.Contains(lowered, "directory"))
}

func isAssistantMemoryPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return strings.Contains(lowered, "what did we decide") ||
		strings.Contains(lowered, "chat history") ||
		strings.Contains(lowered, "prior chat") ||
		strings.Contains(lowered, "past chat") ||
		strings.Contains(lowered, "find the chat") ||
		strings.Contains(lowered, "summarize this conversation") ||
		strings.Contains(lowered, "summarize the current conversation")
}

func isCalendarCreatePrompt(prompt string) bool {
	_, ok := parseCalendarCreateIntent(prompt, "")
	return ok
}

func isCalendarUpdatePrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return strings.Contains(lowered, "calendar") || strings.Contains(lowered, "event") || strings.Contains(lowered, "appointment") || strings.Contains(lowered, "meeting")
}

func isCalendarMutationUpdatePrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return (strings.HasPrefix(lowered, "move ") || strings.HasPrefix(lowered, "reschedule ") || strings.HasPrefix(lowered, "rename ")) &&
		isCalendarUpdatePrompt(prompt)
}

func isCalendarDeletePrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	return (strings.HasPrefix(lowered, "delete ") || strings.HasPrefix(lowered, "cancel ") || strings.HasPrefix(lowered, "remove ")) &&
		isCalendarUpdatePrompt(prompt)
}

func isCalendarSearchPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if isAssistantMemoryPrompt(prompt) {
		return false
	}
	if isCalendarMutationUpdatePrompt(prompt) || isCalendarDeletePrompt(prompt) || isCalendarCreatePrompt(prompt) {
		return false
	}
	if strings.Contains(lowered, "calendar") || strings.Contains(lowered, "calandar") ||
		strings.Contains(lowered, "event") || strings.Contains(lowered, "appointment") ||
		strings.Contains(lowered, "meeting") || strings.Contains(lowered, "agenda") {
		return true
	}
	return (strings.Contains(lowered, "what do i have") || strings.Contains(lowered, "what's on") || strings.Contains(lowered, "whats on")) &&
		(strings.Contains(lowered, "today") || strings.Contains(lowered, "tomorrow") || strings.Contains(lowered, "this week"))
}

func (s *Server) answerNoteCreatePrompt(ctx context.Context, prompt string) (assistantMessageContent, error) {
	title, body := parseNoteCreatePrompt(prompt)
	if title == "" {
		return assistantMessageContent{Text: "What should the new note be called?"}, nil
	}
	pending := assistantPendingAction{
		Kind: "note_create",
		NoteCreate: &assistantPendingNoteCreate{
			Title:        title,
			BodyMarkdown: body,
			Scope:        "profile",
			Confirmation: fmt.Sprintf("Confirm creating a new note named `%s`.", title),
		},
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I can create a new note named `%s`. Confirm before I continue.", title),
		Meta: map[string]interface{}{
			"pending_action": pending,
		},
	}, nil
}

func parseNoteCreatePrompt(prompt string) (string, string) {
	value := strings.TrimSpace(strings.TrimSuffix(prompt, "."))
	lowered := strings.ToLower(value)
	for _, prefix := range []string{"create a new note", "create a note", "create note", "new note", "make a note", "make note"} {
		if strings.HasPrefix(lowered, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			lowered = strings.ToLower(value)
			break
		}
	}
	for _, prefix := range []string{"called ", "named ", "titled "} {
		if strings.HasPrefix(lowered, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			lowered = strings.ToLower(value)
			break
		}
	}
	body := ""
	for _, marker := range []string{" with ", " saying ", " that says "} {
		if index := strings.Index(lowered, marker); index > 0 {
			body = strings.TrimSpace(value[index+len(marker):])
			value = strings.TrimSpace(value[:index])
			break
		}
	}
	return strings.Trim(value, "\"'` "), body
}

func (s *Server) answerNoteSummarizePrompt(ctx context.Context, home domain.Home, auth authContext, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	notes, err := s.assistantVisibleNotes(ctx, home.ID, auth.User.ID, settings)
	if err != nil {
		return assistantMessageContent{}, err
	}
	query := noteSummaryQuery(prompt)
	ranked := rankNotes(notes, query)
	if len(ranked) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I could not find notes matching `%s` to summarize.", query)}, nil
	}
	limit := min(len(ranked), 5)
	var builder strings.Builder
	if limit == 1 {
		builder.WriteString(fmt.Sprintf("Summary of `%s`:", ranked[0].Title))
	} else {
		builder.WriteString(fmt.Sprintf("Summary of %d matching notes for `%s`:", limit, query))
	}
	cards := make([]assistantResultCard, 0, limit)
	for _, note := range ranked[:limit] {
		title := firstNonBlank(note.Title, note.NoteID, "Untitled Note")
		preview := notePreview(firstNonBlank(note.BodyMarkdown, note.Content))
		builder.WriteString("\n- ")
		builder.WriteString(title)
		if preview != "" {
			builder.WriteString(": ")
			builder.WriteString(preview)
		}
		cards = append(cards, assistantResultCard{
			Kind:        "note",
			Title:       title,
			Summary:     preview,
			ActionTitle: "Open in Notes",
			NoteID:      note.NoteID,
			SearchText:  query,
		})
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func noteSummaryQuery(prompt string) string {
	query := noteSearchQuery(prompt)
	query = removeQueryWords(query, map[string]bool{
		"summarize": true, "summary": true, "my": true, "of": true, "for": true,
	})
	if query == "" {
		return strings.TrimSpace(prompt)
	}
	return query
}

func (s *Server) answerFileListFolderPrompt(ctx context.Context, home domain.Home, prompt string) (assistantMessageContent, error) {
	query := fileFolderQuery(prompt)
	query, sourceHint := stripFileSourceSuffix(query)
	path, sourceID, matches, err := s.resolveAssistantFileDirectory(ctx, home.ID, query, sourceHint)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if path == "" {
		if len(matches) > 1 {
			return assistantMessageContent{
				Text:  fmt.Sprintf("I found more than one File Server folder matching `%s`. Which one should I list?", query),
				Cards: assistantCardsFromFileIndex(matches, query),
			}, nil
		}
		return assistantMessageContent{Text: fmt.Sprintf("I could not find a File Server folder matching `%s`.", query)}, nil
	}
	return s.answerResolvedFileListFolder(ctx, home.ID, path, sourceID)
}

func (s *Server) answerResolvedFileListFolder(ctx context.Context, homeID string, path string, sourceID string) (assistantMessageContent, error) {
	envelope, err := s.sendAgentCommand(ctx, homeID, "files.list", protocol.FilesListRequest{SourceID: sourceID, Path: path})
	if err != nil || envelope.Error != nil {
		return assistantMessageContent{Text: "I found the folder, but I could not list it because the primary Hank Agent is offline or returned an error."}, nil
	}
	payload, err := protocol.DecodePayload[protocol.FilesListResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	limit := min(len(payload.Items), 20)
	cards := make([]assistantResultCard, 0, limit)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("`%s` contains %d item(s).", path, len(payload.Items)))
	for _, item := range payload.Items[:limit] {
		itemSourceID := firstNonBlank(item.SourceID, sourceID)
		builder.WriteString("\n- ")
		builder.WriteString(item.Name)
		if item.IsDirectory {
			builder.WriteString(" (folder)")
		}
		cards = append(cards, assistantFileItemCard(item, itemSourceID, item.Name))
	}
	if remaining := len(payload.Items) - limit; remaining > 0 {
		builder.WriteString(fmt.Sprintf("\n- and %d more", remaining))
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func (s *Server) answerFileCreateFolderPrompt(ctx context.Context, home domain.Home, prompt string) (assistantMessageContent, error) {
	path, sourceID := parseCreateFolderTarget(prompt)
	if path == "" {
		return assistantMessageContent{Text: "What File Server folder should I create?"}, nil
	}
	parent := parentPath(path)
	if parent != "" {
		resolvedParent, resolvedSourceID, _, err := s.resolveAssistantFileDirectory(ctx, home.ID, parent, sourceID)
		if err != nil && !errors.Is(err, errAgentOffline) {
			return assistantMessageContent{}, err
		}
		if err == nil && resolvedParent != "" {
			sourceID = firstNonBlank(sourceID, resolvedSourceID)
			path = joinFilePath(resolvedParent, filepathBase(path))
		}
	}
	request := assistantClientToolRequest{
		ToolName: "files.create_folder",
		Arguments: map[string]interface{}{
			"source_id": sourceID,
			"path":      path,
		},
	}
	pending := assistantPendingAction{
		Kind: "file_create_folder",
		FileCreateFolder: &assistantPendingFileCreateFolder{
			ToolRequest:  request,
			SourceID:     sourceID,
			Path:         path,
			Confirmation: fmt.Sprintf("Confirm creating File Server folder `%s`.", path),
		},
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I can create the File Server folder `%s`. Confirm before I continue.", path),
		Meta: map[string]interface{}{
			"pending_action": pending,
		},
	}, nil
}

func fileFolderQuery(prompt string) string {
	query := fileQuery(prompt)
	query = removeQueryWords(query, map[string]bool{
		"contents": true, "inside": true, "list": true, "show": true, "open": true,
	})
	return strings.TrimSpace(query)
}

func parseCreateFolderPath(prompt string) string {
	path, _ := parseCreateFolderTarget(prompt)
	return path
}

func parseCreateFolderTarget(prompt string) (string, string) {
	value := strings.TrimSpace(strings.TrimSuffix(prompt, "."))
	lowered := strings.ToLower(value)
	for _, prefix := range []string{"create a folder called ", "create folder called ", "create a folder named ", "create folder named ", "create a folder ", "create folder ", "make a folder ", "make folder ", "new folder "} {
		if strings.HasPrefix(lowered, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			lowered = strings.ToLower(value)
			break
		}
	}
	value, sourceID := stripFileSourceSuffix(value)
	lowered = strings.ToLower(value)
	name := value
	parent := ""
	for _, marker := range []string{" in ", " inside "} {
		if index := strings.LastIndex(lowered, marker); index > 0 {
			name = strings.TrimSpace(value[:index])
			parent = strings.TrimSpace(value[index+len(marker):])
			break
		}
	}
	name = cleanAttachmentSMBPath(name)
	parent = cleanAttachmentSMBPath(parent)
	if name == "" {
		return "", ""
	}
	if parent != "" {
		return joinFilePath(parent, name), sourceID
	}
	return name, sourceID
}

func (s *Server) resolveAssistantFileDirectory(ctx context.Context, homeID string, query string, sourceHints ...string) (string, string, []domain.AssistantFileIndex, error) {
	query = cleanAttachmentSMBPath(query)
	sourceHint := ""
	if len(sourceHints) > 0 {
		sourceHint = strings.TrimSpace(sourceHints[0])
	}
	if query == "" {
		return "", "", nil, nil
	}
	if strings.Contains(query, "/") {
		return query, sourceHint, nil, nil
	}
	matches, err := s.store.SearchAssistantFileDirectories(ctx, homeID, query, 6)
	if err != nil {
		return "", "", nil, err
	}
	if sourceHint != "" {
		filtered := matches[:0]
		for _, match := range matches {
			if strings.EqualFold(strings.TrimSpace(match.ServiceProfileID), sourceHint) {
				filtered = append(filtered, match)
			}
		}
		matches = filtered
	}
	if len(matches) == 0 {
		fileMatches, err := s.searchFiles(ctx, homeID, query)
		if err != nil {
			return "", "", nil, err
		}
		if path, sourceID, ok := inferUniqueFileMatchParentDirectory(fileMatches); ok {
			return path, sourceID, nil, nil
		}
		return "", "", nil, nil
	}
	exact := make([]domain.AssistantFileIndex, 0)
	normalized := strings.Trim(strings.ToLower(query), "/")
	for _, match := range matches {
		if strings.EqualFold(match.Name, query) || strings.EqualFold(strings.Trim(match.Path, "/"), normalized) {
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

func inferUniqueFileMatchParentDirectory(matches []protocol.FileItem) (string, string, bool) {
	type parentKey struct {
		sourceID string
		path     string
	}
	parents := make(map[parentKey]bool)
	for _, match := range matches {
		if match.IsDirectory {
			continue
		}
		parent := parentPath(match.Path)
		if parent == "" {
			continue
		}
		parents[parentKey{
			sourceID: strings.TrimSpace(match.SourceID),
			path:     strings.Trim(parent, "/"),
		}] = true
	}
	if len(parents) != 1 {
		return "", "", false
	}
	for parent := range parents {
		return parent.path, parent.sourceID, true
	}
	return "", "", false
}

func stripFileSourceSuffix(value string) (string, string) {
	cleaned := strings.TrimSpace(value)
	lowered := strings.ToLower(cleaned)
	for _, marker := range []string{" on the ", " on "} {
		index := strings.LastIndex(lowered, marker)
		if index <= 0 {
			continue
		}
		suffix := strings.TrimSpace(cleaned[index+len(marker):])
		sourceID := normalizeFileSourceHint(suffix)
		if sourceID == "" {
			continue
		}
		return strings.TrimSpace(cleaned[:index]), sourceID
	}
	return cleaned, ""
}

func normalizeFileSourceHint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	for _, phrase := range []string{"file server", "server", "smb share", "share", "source"} {
		value = strings.TrimSpace(strings.TrimSuffix(value, phrase))
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "the "))
	fields := strings.Fields(value)
	if len(fields) != 1 {
		return ""
	}
	sourceID := strings.Trim(fields[0], "\"'` ")
	if sourceID == "" || strings.ContainsAny(sourceID, "/\\") {
		return ""
	}
	return sourceID
}

func assistantCardsFromFileIndex(items []domain.AssistantFileIndex, query string) []assistantResultCard {
	cards := make([]assistantResultCard, 0, len(items))
	for _, item := range items {
		cards = append(cards, assistantResultCard{
			Kind:        "file",
			Title:       firstNonBlank(item.Name, filepathBase(item.Path), item.Path),
			Summary:     item.Path,
			ActionTitle: "Open in File Server",
			SourceID:    strings.TrimSpace(item.ServiceProfileID),
			Path:        item.Path,
			IsDirectory: item.IsDirectory,
			SearchText:  query,
		})
	}
	return cards
}

func assistantFileItemCard(item protocol.FileItem, sourceID string, title string) assistantResultCard {
	return assistantResultCard{
		Kind:        "file",
		Title:       firstNonBlank(title, item.Name, filepathBase(item.Path), item.Path),
		Summary:     item.Path,
		ActionTitle: "Open in File Server",
		SourceID:    strings.TrimSpace(sourceID),
		Path:        item.Path,
		IsDirectory: item.IsDirectory,
		SearchText:  item.Path,
	}
}

func (s *Server) resolvePreviousCardFollowup(ctx context.Context, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, bool, error) {
	if runtime.Session == nil {
		return assistantMessageContent{}, false, nil
	}
	prompt := strings.TrimSpace(runtime.Prompt)
	if prompt == "" {
		return assistantMessageContent{}, false, nil
	}
	if content, ok, err := s.resolvePreviousCalendarFollowup(ctx, runtime, intent); ok || err != nil {
		return content, ok, err
	}
	return s.resolvePreviousFileFollowup(ctx, runtime)
}

func (s *Server) resolvePreviousCalendarFollowup(ctx context.Context, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, bool, error) {
	previous, cards := s.latestAssistantCards(ctx, runtime.Session.ID, "calendar")
	if len(cards) == 0 {
		return assistantMessageContent{}, false, nil
	}
	action := ""
	switch {
	case intent.Kind == assistantIntentCalendarUpdate || isCalendarMutationUpdatePrompt(runtime.Prompt):
		action = "update"
	case intent.Kind == assistantIntentCalendarDelete || isCalendarDeletePrompt(runtime.Prompt):
		action = "delete"
	case strings.Contains(strings.ToLower(previous.Text), "which one should i update"):
		action = "update"
	case strings.Contains(strings.ToLower(previous.Text), "which one should i delete"):
		action = "delete"
	}
	if action == "" {
		return assistantMessageContent{}, false, nil
	}
	card, ok := selectAssistantCard(cards, runtime.Prompt)
	if !ok || card.EventID == "" {
		return assistantMessageContent{}, false, nil
	}
	entries, err := s.store.ListAssistantCalendarEntries(ctx, runtime.Home.ID, runtime.Auth.User.ID)
	if err != nil {
		return assistantMessageContent{}, true, err
	}
	var target domain.AssistantCalendarEntry
	for _, entry := range entries {
		if entry.ExternalEventID == card.EventID {
			target = entry
			break
		}
	}
	if target.ExternalEventID == "" {
		return assistantMessageContent{Text: "I could not find that calendar event in the latest device snapshot."}, true, nil
	}
	query := firstNonBlank(card.SearchText, target.Title)
	if action == "delete" {
		return calendarDeleteContentForEntry(target, query), true, nil
	}
	return calendarUpdateContentForEntry(target, runtime.Prompt, query, ""), true, nil
}

func (s *Server) resolvePreviousFileFollowup(ctx context.Context, runtime assistantToolRuntime) (assistantMessageContent, bool, error) {
	previous, cards := s.latestAssistantCards(ctx, runtime.Session.ID, "file")
	if len(cards) == 0 || !strings.Contains(strings.ToLower(previous.Text), "which one should i list") {
		return assistantMessageContent{}, false, nil
	}
	card, ok := selectAssistantCard(cards, runtime.Prompt)
	if !ok || !card.IsDirectory || strings.TrimSpace(card.Path) == "" {
		return assistantMessageContent{}, false, nil
	}
	content, err := s.answerResolvedFileListFolder(ctx, runtime.Home.ID, card.Path, card.SourceID)
	return content, true, err
}

type previousAssistantCards struct {
	Text  string
	Cards []assistantResultCard
}

func (s *Server) latestAssistantCards(ctx context.Context, sessionID string, kind string) (previousAssistantCards, []assistantResultCard) {
	messages, err := s.store.ListAssistantMessages(ctx, sessionID)
	if err != nil {
		return previousAssistantCards{}, nil
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != assistantRoleAssistant {
			continue
		}
		var content assistantMessageContent
		if err := json.Unmarshal([]byte(messages[i].ContentJSON), &content); err != nil {
			continue
		}
		cards := make([]assistantResultCard, 0, len(content.Cards))
		for _, card := range content.Cards {
			if card.Kind == kind {
				cards = append(cards, card)
			}
		}
		if len(cards) > 0 {
			return previousAssistantCards{Text: content.Text, Cards: cards}, cards
		}
	}
	return previousAssistantCards{}, nil
}

func selectAssistantCard(cards []assistantResultCard, prompt string) (assistantResultCard, bool) {
	if len(cards) == 0 {
		return assistantResultCard{}, false
	}
	if index, ok := assistantSelectionIndex(prompt, len(cards)); ok {
		return cards[index], true
	}
	needle := normalizedAssistantSelection(prompt)
	if needle == "" {
		if len(cards) == 1 && isAssistantSingleSelectionPrompt(prompt) {
			return cards[0], true
		}
		return assistantResultCard{}, false
	}
	for _, card := range cards {
		for _, value := range []string{card.Title, card.Summary, card.Path, card.SourceID, card.EventID} {
			candidate := normalizedAssistantSelection(value)
			if candidate != "" && (candidate == needle || strings.Contains(candidate, needle) || strings.Contains(needle, candidate)) {
				return card, true
			}
		}
	}
	return assistantResultCard{}, false
}

func assistantSelectionIndex(prompt string, count int) (int, bool) {
	lowered := " " + strings.ToLower(prompt) + " "
	ordinals := []string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}
	for index, ordinal := range ordinals {
		if index < count && strings.Contains(lowered, " "+ordinal+" ") {
			return index, true
		}
	}
	replacer := strings.NewReplacer("#", " # ", ".", " ", ",", " ", ":", " ", ";", " ", "(", " ", ")", " ")
	words := strings.Fields(replacer.Replace(strings.ToLower(prompt)))
	for index, word := range words {
		value, err := strconv.Atoi(word)
		if err != nil || value < 1 || value > count {
			continue
		}
		if len(words) == 1 || (index > 0 && assistantSelectionNumberPrefix(words[index-1])) {
			return value - 1, true
		}
	}
	if count == 1 && isAssistantSingleSelectionPrompt(prompt) {
		return 0, true
	}
	return 0, false
}

func assistantSelectionNumberPrefix(value string) bool {
	switch strings.TrimSpace(value) {
	case "#", "option", "result", "item", "number", "choice":
		return true
	default:
		return false
	}
}

func isAssistantSingleSelectionPrompt(prompt string) bool {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	for _, phrase := range []string{"that one", "this one", "same one", "that folder", "that event", "it"} {
		if lowered == phrase || strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

func normalizedAssistantSelection(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "option ")
	value = strings.TrimPrefix(value, "result ")
	var builder strings.Builder
	lastSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '/' {
			builder.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			builder.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func parentPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return ""
	}
	return path[:index]
}

func joinFilePath(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), "/")
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/")
}

func (s *Server) answerAssistantMemoryPrompt(ctx context.Context, homeID string, userID string, session *domain.AssistantSession, settings domain.AssistantSettings, prompt string) (assistantMessageContent, error) {
	lowered := strings.ToLower(strings.TrimSpace(prompt))
	if session != nil && (strings.Contains(lowered, "summarize this conversation") || strings.Contains(lowered, "summarize the current conversation")) {
		messages, err := s.store.ListAssistantMessages(ctx, session.ID)
		if err != nil {
			return assistantMessageContent{}, err
		}
		return summarizeAssistantMessages(messages), nil
	}
	query := assistantMemoryQuery(prompt)
	queryEmbedding, embeddingModel, _ := s.embedAssistantText(ctx, userID, query)
	contexts, err := s.store.SearchAssistantContext(ctx, homeID, userID, query, queryEmbedding, embeddingModel, settings.MaxContextItems)
	if err != nil {
		return assistantMessageContent{}, err
	}
	matches := make([]domain.AssistantRetrievedContext, 0)
	for _, item := range contexts {
		if item.SourceType == assistantConversationSourceType {
			matches = append(matches, item)
		}
		if len(matches) >= 5 {
			break
		}
	}
	if len(matches) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I could not find a prior HankAI conversation matching `%s`.", query)}, nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("I found %d prior HankAI conversation match(es) for `%s`:", len(matches), query))
	cards := make([]assistantResultCard, 0, len(matches))
	for _, item := range matches {
		builder.WriteString("\n- ")
		builder.WriteString(item.Title)
		cards = append(cards, assistantResultCard{
			Kind:        "assistant_conversation",
			Title:       item.Title,
			Summary:     item.Snippet,
			ActionTitle: "Open Chat",
			Path:        item.Path,
			SearchText:  query,
		})
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func assistantMemoryQuery(prompt string) string {
	query := stripAssistantSearchPrefix(prompt)
	query = removeQueryWords(query, map[string]bool{
		"what": true, "did": true, "we": true, "decide": true, "about": true,
		"find": true, "chat": true, "where": true, "i": true, "asked": true,
		"conversation": true, "history": true,
	})
	if query == "" {
		return strings.TrimSpace(prompt)
	}
	return query
}

func summarizeAssistantMessages(messages []domain.AssistantMessage) assistantMessageContent {
	var lines []string
	for _, message := range messages {
		var content assistantMessageContent
		if err := jsonUnmarshalAssistantContent(message.ContentJSON, &content); err != nil {
			continue
		}
		text := strings.TrimSpace(content.Text)
		if text == "" {
			continue
		}
		prefix := "HankAI"
		if message.Role == assistantRoleUser {
			prefix = "You"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", prefix, notePreview(text)))
	}
	if len(lines) == 0 {
		return assistantMessageContent{Text: "There is not enough text in this conversation to summarize yet."}
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return assistantMessageContent{Text: "This conversation has covered:\n- " + strings.Join(lines, "\n- ")}
}

func jsonUnmarshalAssistantContent(raw string, content *assistantMessageContent) error {
	return json.Unmarshal([]byte(raw), content)
}

func (s *Server) answerCalendarSearchPrompt(ctx context.Context, homeID string, userID string, prompt string, timezone string) (assistantMessageContent, error) {
	entries, err := s.store.ListAssistantCalendarEntries(ctx, homeID, userID)
	if err != nil {
		return assistantMessageContent{}, err
	}
	query := calendarSearchQuery(prompt)
	start, end, label, hasRange := calendarDateRange(prompt, timezone)
	matches := matchingCalendarEntries(entries, query, start, end, hasRange)
	if len(matches) == 0 {
		if hasRange {
			return assistantMessageContent{Text: fmt.Sprintf("I do not have any calendar snapshot entries for %s matching `%s`.", label, defaultString(query, "events"))}, nil
		}
		return assistantMessageContent{Text: fmt.Sprintf("I do not have any calendar snapshot entries matching `%s`.", defaultString(query, strings.TrimSpace(prompt)))}, nil
	}
	limit := min(len(matches), 12)
	var builder strings.Builder
	if hasRange {
		builder.WriteString(fmt.Sprintf("Calendar entries for %s:", label))
	} else {
		builder.WriteString(fmt.Sprintf("Calendar entries matching `%s`:", defaultString(query, strings.TrimSpace(prompt))))
	}
	cards := make([]assistantResultCard, 0, limit)
	for _, entry := range matches[:limit] {
		builder.WriteString("\n- ")
		builder.WriteString(calendarEntryLabel(entry))
		cards = append(cards, calendarEntryCard(entry, query))
	}
	if remaining := len(matches) - limit; remaining > 0 {
		builder.WriteString(fmt.Sprintf("\n- and %d more", remaining))
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func (s *Server) answerCalendarUpdatePrompt(ctx context.Context, homeID string, userID string, prompt string, timezone string) (assistantMessageContent, error) {
	entries, err := s.store.ListAssistantCalendarEntries(ctx, homeID, userID)
	if err != nil {
		return assistantMessageContent{}, err
	}
	query := calendarMutationQuery(prompt)
	start, end, _, hasRange := calendarDateRange(prompt, timezone)
	matches := matchingCalendarEntries(entries, query, start, end, hasRange)
	if len(matches) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I could not find a calendar event matching `%s` to update.", query)}, nil
	}
	if len(matches) > 1 {
		return assistantMessageContent{
			Text:  fmt.Sprintf("I found more than one calendar event matching `%s`. Which one should I update?", query),
			Cards: calendarEntryCards(matches[:min(len(matches), 5)], query),
		}, nil
	}
	target := matches[0]
	return calendarUpdateContentForEntry(target, prompt, query, timezone), nil
}

func calendarUpdateContentForEntry(target domain.AssistantCalendarEntry, prompt string, query string, timezone string) assistantMessageContent {
	newTitle := parseCalendarRenameTitle(prompt)
	newStart, ok := parseCalendarMoveTime(prompt, target.StartsAt, timezone)
	if !ok && newTitle == "" {
		return assistantMessageContent{Text: "What new time or title should I use for that calendar event?"}
	}
	args := map[string]interface{}{
		"event_id":          target.ExternalEventID,
		"calendar_id":       target.CalendarID,
		"device_id":         target.DeviceID,
		"current_starts_at": target.StartsAt.Format(time.RFC3339),
		"title":             firstNonBlank(newTitle, target.Title),
	}
	if ok {
		duration := target.EndsAt.Sub(target.StartsAt)
		if duration <= 0 {
			duration = time.Hour
		}
		args["starts_at"] = newStart.Format(time.RFC3339)
		args["ends_at"] = newStart.Add(duration).Format(time.RFC3339)
	}
	confirmationTitle := firstNonBlank(newTitle, target.Title)
	confirmationStartsAt := target.StartsAt
	if ok {
		confirmationStartsAt = newStart
	}
	request := assistantClientToolRequest{ToolName: "calendar.update_event", Arguments: args}
	pending := assistantPendingAction{
		Kind: "calendar_update",
		CalendarClient: &assistantPendingCalendarClient{
			ToolRequest:  request,
			Title:        target.Title,
			Query:        query,
			Confirmation: fmt.Sprintf("Confirm updating `%s` on %s.", confirmationTitle, calendarEntryTimeLabel(confirmationStartsAt, target.IsAllDay)),
		},
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I can update `%s`. Confirm before I ask Calendar to make the change.", target.Title),
		Cards: []assistantResultCard{
			calendarEntryCard(target, query),
		},
		Meta: map[string]interface{}{"pending_action": pending},
	}
}

func (s *Server) answerCalendarDeletePrompt(ctx context.Context, homeID string, userID string, prompt string, timezone string) (assistantMessageContent, error) {
	entries, err := s.store.ListAssistantCalendarEntries(ctx, homeID, userID)
	if err != nil {
		return assistantMessageContent{}, err
	}
	query := calendarMutationQuery(prompt)
	start, end, _, hasRange := calendarDateRange(prompt, timezone)
	matches := matchingCalendarEntries(entries, query, start, end, hasRange)
	if len(matches) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I could not find a calendar event matching `%s` to delete.", query)}, nil
	}
	if len(matches) > 1 {
		return assistantMessageContent{
			Text:  fmt.Sprintf("I found more than one calendar event matching `%s`. Which one should I delete?", query),
			Cards: calendarEntryCards(matches[:min(len(matches), 5)], query),
		}, nil
	}
	target := matches[0]
	return calendarDeleteContentForEntry(target, query), nil
}

func calendarDeleteContentForEntry(target domain.AssistantCalendarEntry, query string) assistantMessageContent {
	request := assistantClientToolRequest{
		ToolName: "calendar.delete_event",
		Arguments: map[string]interface{}{
			"event_id":    target.ExternalEventID,
			"calendar_id": target.CalendarID,
			"device_id":   target.DeviceID,
			"title":       target.Title,
			"starts_at":   target.StartsAt.Format(time.RFC3339),
		},
	}
	pending := assistantPendingAction{
		Kind: "calendar_delete",
		CalendarClient: &assistantPendingCalendarClient{
			ToolRequest:  request,
			Title:        target.Title,
			Query:        query,
			Confirmation: fmt.Sprintf("Confirm deleting `%s` on %s.", target.Title, calendarEntryTimeLabel(target.StartsAt, target.IsAllDay)),
			Destructive:  true,
		},
	}
	return assistantMessageContent{
		Text: fmt.Sprintf("I can delete `%s`. Confirm before I ask Calendar to remove it.", target.Title),
		Cards: []assistantResultCard{
			calendarEntryCard(target, query),
		},
		Meta: map[string]interface{}{"pending_action": pending},
	}
}

func matchingCalendarEntries(entries []domain.AssistantCalendarEntry, query string, start time.Time, end time.Time, hasRange bool) []domain.AssistantCalendarEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(query)
	now := time.Now().Add(-24 * time.Hour)
	matches := make([]domain.AssistantCalendarEntry, 0)
	for _, entry := range entries {
		if hasRange {
			if entry.StartsAt.Before(start) || !entry.StartsAt.Before(end) {
				continue
			}
		} else if entry.StartsAt.Before(now) {
			continue
		}
		if len(terms) > 0 {
			searchText := strings.ToLower(strings.Join([]string{entry.Title, entry.CalendarID, entry.Location, entry.Notes, entry.SearchText}, " "))
			ok := true
			for _, term := range terms {
				if !strings.Contains(searchText, term) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		matches = append(matches, entry)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].StartsAt.Equal(matches[j].StartsAt) {
			return strings.ToLower(matches[i].Title) < strings.ToLower(matches[j].Title)
		}
		return matches[i].StartsAt.Before(matches[j].StartsAt)
	})
	return matches
}

func calendarSearchQuery(prompt string) string {
	query := stripAssistantSearchPrefix(prompt)
	query = removeCalendarWords(query)
	return strings.TrimSpace(query)
}

func calendarMutationQuery(prompt string) string {
	query := strings.TrimSpace(strings.TrimSuffix(prompt, "."))
	lowered := strings.ToLower(query)
	for _, prefix := range []string{"delete ", "cancel ", "remove ", "move ", "reschedule ", "rename "} {
		if strings.HasPrefix(lowered, prefix) {
			query = strings.TrimSpace(query[len(prefix):])
			lowered = strings.ToLower(query)
			break
		}
	}
	if index := strings.LastIndex(lowered, " to "); index > 0 {
		query = strings.TrimSpace(query[:index])
	}
	query = removeCalendarWords(query)
	return strings.TrimSpace(query)
}

func removeCalendarWords(query string) string {
	return removeQueryWords(query, map[string]bool{
		"what": true, "do": true, "i": true, "have": true, "on": true, "my": true,
		"calendar": true, "calandar": true, "calendars": true, "event": true, "events": true,
		"appointment": true, "appointments": true, "meeting": true, "meetings": true,
		"agenda": true, "today": true, "tomorrow": true, "this": true, "week": true,
		"mention": true, "mentions": true,
	})
}

func calendarDateRange(prompt string, timezone string) (time.Time, time.Time, string, bool) {
	location := assistantTimeLocation(timezone)
	now := time.Now().In(location)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	lowered := strings.ToLower(prompt)
	switch {
	case strings.Contains(lowered, "tomorrow"):
		start := dayStart.AddDate(0, 0, 1)
		return start, start.AddDate(0, 0, 1), start.Format("Monday, January 2, 2006"), true
	case strings.Contains(lowered, "today"):
		return dayStart, dayStart.AddDate(0, 0, 1), dayStart.Format("Monday, January 2, 2006"), true
	case strings.Contains(lowered, "this week"):
		return dayStart, dayStart.AddDate(0, 0, 7), fmt.Sprintf("%s through %s", dayStart.Format("January 2, 2006"), dayStart.AddDate(0, 0, 6).Format("January 2, 2006")), true
	}
	if date, label, ok := parseCalendarDayReference(prompt, location); ok {
		start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
		return start, start.AddDate(0, 0, 1), label, true
	}
	return time.Time{}, time.Time{}, "", false
}

func parseCalendarDayReference(prompt string, location *time.Location) (time.Time, string, bool) {
	lowered := strings.ToLower(prompt)
	now := time.Now().In(location)
	for _, weekday := range calendarWeekdayNames() {
		if strings.Contains(lowered, weekday.name) {
			days := (int(weekday.weekday) - int(now.Weekday()) + 7) % 7
			if days == 0 {
				days = 7
			}
			date := now.AddDate(0, 0, days)
			return date, date.Format("Monday, January 2, 2006"), true
		}
	}
	for _, layout := range []string{"January 2 2006", "Jan 2 2006", "January 2", "Jan 2"} {
		if date, ok := parseMonthDayInPrompt(prompt, layout, location); ok {
			if !strings.Contains(layout, "2006") {
				date = time.Date(now.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
				if date.Before(now.Add(-24 * time.Hour)) {
					date = time.Date(now.Year()+1, date.Month(), date.Day(), 0, 0, 0, 0, location)
				}
			}
			return date, date.Format("Monday, January 2, 2006"), true
		}
	}
	return time.Time{}, "", false
}

func parseMonthDayInPrompt(prompt string, layout string, location *time.Location) (time.Time, bool) {
	words := strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(prompt))
	for start := range words {
		for size := 2; size <= 3 && start+size <= len(words); size++ {
			candidate := strings.Join(words[start:start+size], " ")
			if parsed, err := time.ParseInLocation(layout, candidate, location); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

type calendarWeekday struct {
	name    string
	weekday time.Weekday
}

func calendarWeekdayNames() []calendarWeekday {
	return []calendarWeekday{
		{"sunday", time.Sunday},
		{"monday", time.Monday},
		{"tuesday", time.Tuesday},
		{"wednesday", time.Wednesday},
		{"thursday", time.Thursday},
		{"friday", time.Friday},
		{"saturday", time.Saturday},
	}
}

func assistantTimeLocation(timezone string) *time.Location {
	if strings.TrimSpace(timezone) != "" {
		if loaded, err := time.LoadLocation(strings.TrimSpace(timezone)); err == nil {
			return loaded
		}
	}
	return time.Local
}

func calendarEntryCards(entries []domain.AssistantCalendarEntry, query string) []assistantResultCard {
	cards := make([]assistantResultCard, 0, len(entries))
	for _, entry := range entries {
		cards = append(cards, calendarEntryCard(entry, query))
	}
	return cards
}

func calendarEntryCard(entry domain.AssistantCalendarEntry, query string) assistantResultCard {
	targetDate := entry.StartsAt
	return assistantResultCard{
		Kind:        "calendar",
		Title:       firstNonBlank(entry.Title, "Calendar Event"),
		Summary:     calendarEntryLabel(entry),
		ActionTitle: "Open in Calendar",
		EventID:     entry.ExternalEventID,
		SourceID:    entry.CalendarID,
		TargetDate:  &targetDate,
		SearchText:  query,
	}
}

func calendarEntryLabel(entry domain.AssistantCalendarEntry) string {
	label := calendarEntryTimeLabel(entry.StartsAt, entry.IsAllDay)
	if entry.CalendarID != "" {
		label += " [" + entry.CalendarID + "]"
	}
	return fmt.Sprintf("%s - %s", label, firstNonBlank(entry.Title, "Calendar Event"))
}

func calendarEntryTimeLabel(startsAt time.Time, allDay bool) string {
	if allDay {
		return startsAt.Format("Monday, January 2, 2006")
	}
	return startsAt.Format("Monday, January 2, 2006 3:04 PM")
}

func parseCalendarMoveTime(prompt string, existing time.Time, timezone string) (time.Time, bool) {
	lowered := strings.ToLower(prompt)
	index := strings.LastIndex(lowered, " to ")
	if index < 0 {
		index = strings.LastIndex(lowered, " at ")
	}
	if index < 0 {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(strings.TrimSuffix(prompt[index+4:], "."))
	hour, minute, ok := parseClockText(raw)
	if !ok {
		return time.Time{}, false
	}
	location := existing.Location()
	if location == nil || location == time.UTC {
		location = assistantTimeLocation(timezone)
	}
	return time.Date(existing.In(location).Year(), existing.In(location).Month(), existing.In(location).Day(), hour, minute, 0, 0, location), true
}

func parseCalendarRenameTitle(prompt string) string {
	lowered := strings.ToLower(prompt)
	if !strings.HasPrefix(lowered, "rename ") {
		return ""
	}
	index := strings.LastIndex(lowered, " to ")
	if index < 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(prompt[index+len(" to "):]), "\"'` .")
}

func parseClockText(raw string) (int, int, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.Trim(raw, "\"'` ")
	raw = strings.TrimPrefix(raw, "at ")
	raw = strings.TrimSuffix(raw, ".")
	raw = strings.ReplaceAll(raw, " ", "")
	if raw == "" {
		return 0, 0, false
	}
	ampm := ""
	if strings.HasSuffix(raw, "am") || strings.HasSuffix(raw, "pm") {
		ampm = raw[len(raw)-2:]
		raw = raw[:len(raw)-2]
	}
	minute := 0
	if strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		raw = parts[0]
		parsedMinute, err := strconv.Atoi(parts[1])
		if err != nil || parsedMinute < 0 || parsedMinute > 59 {
			return 0, 0, false
		}
		minute = parsedMinute
	}
	hour, err := strconv.Atoi(raw)
	if err != nil {
		return 0, 0, false
	}
	if ampm == "pm" && hour < 12 {
		hour += 12
	}
	if ampm == "am" && hour == 12 {
		hour = 0
	}
	if ampm == "" && hour >= 1 && hour <= 7 {
		hour += 12
	}
	if hour < 0 || hour > 23 {
		return 0, 0, false
	}
	return hour, minute, true
}
