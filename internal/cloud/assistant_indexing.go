package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const assistantConversationSourceType = "assistant_conversation"
const maxAssistantHomeAssistantIndexEntities = 500

func (s *Server) refreshAssistantIndex(ctx context.Context, runtime assistantToolRuntime) {
	settings := runtime.Settings
	settings = normalizeAssistantSettings(settings)
	if settings.CalendarEnabled {
		s.enqueueAssistantIndexJob(ctx, runtime.Home.ID, runtime.Auth.User.ID, assistantIndexSourceCalendar, "")
	}
	if settings.ProjectDocsEnabled {
		s.enqueueAssistantIndexJob(ctx, runtime.Home.ID, runtime.Auth.User.ID, assistantIndexSourceProjectDocs, "")
	}
	if settings.HomeAssistantEnabled {
		s.enqueueAssistantIndexJob(ctx, runtime.Home.ID, runtime.Auth.User.ID, assistantIndexSourceHomeAssistant, "")
	}
	if settings.FilesEnabled {
		s.enqueueAssistantIndexJob(ctx, runtime.Home.ID, runtime.Auth.User.ID, assistantIndexSourceFiles, "")
	}
}

func (s *Server) indexAssistantConversation(ctx context.Context, session domain.AssistantSession, userID string, settingsOverride ...domain.AssistantSettings) error {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(userID) == "" {
		return nil
	}
	messages, err := s.store.ListAssistantMessages(ctx, session.ID)
	if err != nil {
		return err
	}
	searchText := assistantConversationSearchText(session, messages)
	if strings.TrimSpace(searchText) == "" {
		return nil
	}
	metadata, _ := json.Marshal(map[string]any{
		"session_id": session.ID,
		"title":      session.Title,
	})
	userIDCopy := userID
	sourceKey := strings.Join([]string{assistantConversationSourceType, session.HomeID, userID, session.ID}, ":")
	updatedAt := firstTime(session.UpdatedAt, time.Now().UTC())
	document := domain.AssistantDocument{
		ID:           stableAssistantID("adoc", sourceKey),
		HomeID:       session.HomeID,
		UserID:       &userIDCopy,
		SourceType:   assistantConversationSourceType,
		SourceID:     session.ID,
		SourceKey:    sourceKey,
		Title:        firstNonBlank(session.Title, "HankAI Conversation"),
		Path:         session.ID,
		CanonicalURI: "hank://assistant/sessions/" + session.ID,
		MetadataJSON: string(metadata),
		SearchText:   searchText,
		UpdatedAt:    updatedAt,
	}
	chunks, err := s.assistantChunksForText(ctx, userID, document.ID, searchText, updatedAt, settingsOverride...)
	if err != nil {
		return err
	}
	return s.store.UpsertAssistantDocumentWithChunks(ctx, document, chunks)
}

func assistantConversationSearchText(session domain.AssistantSession, messages []domain.AssistantMessage) string {
	var builder strings.Builder
	if strings.TrimSpace(session.Title) != "" {
		builder.WriteString("Conversation: " + strings.TrimSpace(session.Title) + "\n")
	}
	for _, message := range messages {
		var content assistantMessageContent
		if err := json.Unmarshal([]byte(message.ContentJSON), &content); err != nil {
			continue
		}
		text := strings.TrimSpace(content.Text)
		if text == "" && len(content.Cards) == 0 {
			continue
		}
		switch message.Role {
		case assistantRoleUser:
			builder.WriteString("User: ")
		case assistantRoleAssistant:
			builder.WriteString("HankAI: ")
		default:
			builder.WriteString(strings.TrimSpace(message.Role) + ": ")
		}
		builder.WriteString(text)
		builder.WriteString("\n")
		for _, card := range content.Cards {
			cardText := strings.TrimSpace(strings.Join([]string{
				card.Kind,
				card.Title,
				card.Summary,
				card.NoteID,
				card.EventID,
				card.SourceID,
				card.Path,
				card.SearchText,
			}, " "))
			if cardText != "" {
				builder.WriteString("Result: " + cardText + "\n")
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func (s *Server) indexAssistantNotes(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settings domain.AssistantSettings) error {
	if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
		return nil
	}
	if settings.ProfileNotesEnabled {
		profileNotes, err := s.store.ListProfileNotes(ctx, auth.User.ID, false)
		if err != nil {
			return err
		}
		for _, note := range profileNotes {
			if note.DeletedAt != nil {
				continue
			}
			if err := s.indexAssistantNote(ctx, home.ID, auth.User.ID, "profile_note", note, settings); err != nil {
				return err
			}
		}
	}
	if !settings.HomeNotesEnabled {
		return nil
	}
	homeNotes, err := s.store.ListVisibleHomeNotes(ctx, home.ID, auth.User.ID, false)
	if err != nil {
		return err
	}
	for _, note := range homeNotes {
		if note.DeletedAt != nil {
			continue
		}
		if err := s.indexAssistantNote(ctx, home.ID, auth.User.ID, "shared_note", note, settings); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) indexAssistantNote(ctx context.Context, homeID string, userID string, sourceType string, note domain.UserNote, settingsOverride ...domain.AssistantSettings) error {
	body := firstNonBlank(note.BodyMarkdown, note.Content)
	metadata, _ := json.Marshal(map[string]any{
		"page_type": note.PageType,
		"note_id":   note.NoteID,
	})
	userIDCopy := userID
	sourceKey := strings.Join([]string{sourceType, userID, note.ID}, ":")
	document := domain.AssistantDocument{
		ID:           stableAssistantID("adoc", sourceKey),
		HomeID:       homeID,
		UserID:       &userIDCopy,
		SourceType:   sourceType,
		SourceID:     note.ID,
		SourceKey:    sourceKey,
		Title:        firstNonBlank(note.Title, note.NoteID),
		Path:         note.NoteID,
		CanonicalURI: "hank://notes/" + note.NoteID,
		MetadataJSON: string(metadata),
		SearchText:   strings.TrimSpace(note.Title + "\n" + note.NoteID + "\n" + body),
		UpdatedAt:    note.UpdatedAt,
	}
	chunks, err := s.assistantChunksForText(ctx, userID, document.ID, document.SearchText, note.UpdatedAt, settingsOverride...)
	if err != nil {
		return err
	}
	return s.store.UpsertAssistantDocumentWithChunks(ctx, document, chunks)
}

func (s *Server) indexAssistantCalendarSnapshot(ctx context.Context, homeID string, userID string, settingsOverride ...domain.AssistantSettings) error {
	entries, err := s.store.ListAssistantCalendarEntries(ctx, homeID, userID)
	if err != nil {
		return err
	}
	return s.indexAssistantCalendarEntries(ctx, homeID, userID, entries, settingsOverride...)
}

func (s *Server) indexAssistantCalendarEntries(ctx context.Context, homeID string, userID string, entries []domain.AssistantCalendarEntry, settingsOverride ...domain.AssistantSettings) error {
	for _, entry := range entries {
		metadata, _ := json.Marshal(map[string]any{
			"calendar_id":       entry.CalendarID,
			"device_id":         entry.DeviceID,
			"external_event_id": entry.ExternalEventID,
			"starts_at":         entry.StartsAt,
			"ends_at":           entry.EndsAt,
			"is_all_day":        entry.IsAllDay,
		})
		userIDCopy := userID
		sourceKey := strings.Join([]string{"calendar_event", userID, entry.DeviceID, entry.ExternalEventID}, ":")
		searchText := strings.TrimSpace(strings.Join([]string{
			entry.Title,
			entry.CalendarID,
			entry.Location,
			entry.Notes,
			entry.StartsAt.Format(time.RFC3339),
			entry.SearchText,
		}, "\n"))
		document := domain.AssistantDocument{
			ID:           stableAssistantID("adoc", sourceKey),
			HomeID:       homeID,
			UserID:       &userIDCopy,
			SourceType:   "calendar_event",
			SourceID:     entry.ExternalEventID,
			SourceKey:    sourceKey,
			Title:        firstNonBlank(entry.Title, "Calendar Event"),
			Path:         entry.StartsAt.Format(time.RFC3339),
			CanonicalURI: "hank://calendar/" + entry.ExternalEventID,
			MetadataJSON: string(metadata),
			SearchText:   searchText,
			UpdatedAt:    entry.UpdatedAt,
		}
		chunks, err := s.assistantChunksForText(ctx, userID, document.ID, searchText, entry.UpdatedAt, settingsOverride...)
		if err != nil {
			return err
		}
		if err := s.store.UpsertAssistantDocumentWithChunks(ctx, document, chunks); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) indexAssistantHomeAssistantStates(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settingsOverride ...domain.AssistantSettings) error {
	if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureHomeAssistant); err != nil {
		return nil
	}
	envelope, err := s.sendAgentCommand(ctx, home.ID, "homeassistant.fetch_states", map[string]any{})
	if err != nil || envelope.Error != nil {
		return err
	}
	payload, err := protocol.DecodePayload[protocol.HomeAssistantFetchStatesResponse](envelope)
	if err != nil {
		return err
	}
	for index, state := range payload.States {
		if index >= maxAssistantHomeAssistantIndexEntities {
			break
		}
		metadata, _ := json.Marshal(map[string]any{
			"state":        state.State,
			"attributes":   state.Attributes,
			"last_changed": state.LastChanged,
			"last_updated": state.LastUpdated,
		})
		sourceKey := "homeassistant_entity:" + home.ID + ":" + state.EntityID
		searchText := state.EntityID + "\n" + state.State + "\n" + assistantAttributesText(state.Attributes)
		document := domain.AssistantDocument{
			ID:           stableAssistantID("adoc", sourceKey),
			HomeID:       home.ID,
			SourceType:   "homeassistant_entity",
			SourceID:     state.EntityID,
			SourceKey:    sourceKey,
			Title:        state.EntityID,
			Path:         state.EntityID,
			CanonicalURI: "hank://homeassistant/" + state.EntityID,
			MetadataJSON: string(metadata),
			SearchText:   searchText,
			UpdatedAt:    firstTime(state.LastUpdated, state.LastChanged, time.Now().UTC()),
		}
		chunks, err := s.assistantChunksForText(ctx, auth.User.ID, document.ID, searchText, document.UpdatedAt, settingsOverride...)
		if err != nil {
			return err
		}
		if err := s.store.UpsertAssistantDocumentWithChunks(ctx, document, chunks); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) indexAssistantFiles(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, settingsOverride ...domain.AssistantSettings) error {
	if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		return nil
	}
	sourceIDs := s.assistantFileIndexSourceIDs(ctx, home.ID)
	items := make([]protocol.FileItem, 0)
	var firstErr error
	for _, sourceID := range sourceIDs {
		sourceItems, err := s.crawlFilesForAssistantIndex(ctx, home.ID, sourceID, 300)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.logger.Warn("assistant file source indexing failed", "error_code", "operation_failed")
			continue
		}
		items = append(items, sourceItems...)
	}
	if len(items) == 0 && firstErr != nil {
		return firstErr
	}
	now := time.Now().UTC()
	for _, item := range items {
		embedding, model, version, err := s.embedAssistantIndexText(ctx, auth.User.ID, item.Path+" "+item.Name, settingsOverride...)
		if err != nil {
			return err
		}
		embeddingJSON, _ := json.Marshal(embedding)
		metadata, _ := json.Marshal(map[string]any{"is_directory": item.IsDirectory})
		modifiedAt := item.ModifiedAt
		if modifiedAt.IsZero() {
			modifiedAt = now
		}
		if err := s.store.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
			ID:               stableAssistantID("afile", home.ID+":"+item.SourceID+":"+item.Path),
			HomeID:           home.ID,
			ServiceProfileID: strings.TrimSpace(item.SourceID),
			Path:             item.Path,
			Name:             item.Name,
			IsDirectory:      item.IsDirectory,
			SizeBytes:        item.Size,
			ModifiedAt:       &modifiedAt,
			SearchText:       item.Path + "\n" + item.Name,
			MetadataJSON:     string(metadata),
			EmbeddingJSON:    string(embeddingJSON),
			EmbeddingModel:   model,
			EmbeddingVersion: version,
			UpdatedAt:        now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) assistantFileIndexSourceIDs(ctx context.Context, homeID string) []string {
	profile, err := s.store.GetHomeServiceProfile(ctx, homeID, domain.ServiceTypeSMB)
	if err != nil {
		return []string{""}
	}
	return assistantFileIndexSourceIDsFromProfileConfig(profile.PublicConfigJSON)
}

type assistantFileSourceProfile struct {
	ActiveSourceID   string                      `json:"active_source_id"`
	FileSources      []assistantFileSourceConfig `json:"file_sources"`
	Sources          []assistantFileSourceConfig `json:"sources"`
	Shares           []assistantFileSourceConfig `json:"shares"`
	LocalRootEnabled bool                        `json:"local_root_enabled"`
}

type assistantFileSourceConfig struct {
	ID               string `json:"id"`
	SourceID         string `json:"source_id"`
	Type             string `json:"type"`
	SMBEnabled       bool   `json:"smb_enabled"`
	LocalRootEnabled bool   `json:"local_root_enabled"`
	Host             string `json:"host"`
	Share            string `json:"share"`
	SMBHost          string `json:"smb_host"`
	SMBShare         string `json:"smb_share"`
}

func assistantFileIndexSourceIDsFromProfileConfig(raw string) []string {
	var profile assistantFileSourceProfile
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &profile); err != nil {
		return []string{""}
	}
	seen := map[string]bool{}
	sourceIDs := make([]string, 0)
	localFound := false
	appendSourceID := func(sourceID string) {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "" || seen[sourceID] {
			return
		}
		seen[sourceID] = true
		sourceIDs = append(sourceIDs, sourceID)
	}

	for _, item := range append(append(profile.FileSources, profile.Sources...), profile.Shares...) {
		sourceID := firstNonBlank(item.SourceID, item.ID)
		sourceType := strings.ToLower(strings.TrimSpace(item.Type))
		switch sourceType {
		case "local":
			if item.LocalRootEnabled || sourceID == "local" {
				appendSourceID(sourceID)
				localFound = true
			}
		case "smb":
			if item.SMBEnabled || strings.TrimSpace(firstNonBlank(item.Host, item.SMBHost)) != "" || strings.TrimSpace(firstNonBlank(item.Share, item.SMBShare)) != "" {
				appendSourceID(sourceID)
			}
		default:
			if sourceID != "" && (item.SMBEnabled || item.LocalRootEnabled || strings.TrimSpace(firstNonBlank(item.Host, item.SMBHost)) != "" || strings.TrimSpace(firstNonBlank(item.Share, item.SMBShare)) != "") {
				appendSourceID(sourceID)
				if item.LocalRootEnabled {
					localFound = true
				}
			}
		}
	}
	// Legacy fallback: older snapshots flagged a single host root at the top
	// level without enumerating it as a source. Only synthesize the "local" id
	// when no host folder source was already discovered.
	if profile.LocalRootEnabled && !localFound {
		appendSourceID("local")
	}
	if len(sourceIDs) == 0 {
		appendSourceID(profile.ActiveSourceID)
	}
	if len(sourceIDs) == 0 {
		return []string{""}
	}
	return sourceIDs
}

func assistantFileIndexListRequest(sourceID string, path string) protocol.FilesListRequest {
	return protocol.FilesListRequest{SourceID: strings.TrimSpace(sourceID), Path: path}
}

func (s *Server) crawlFilesForAssistantIndex(ctx context.Context, homeID string, sourceID string, maxItems int) ([]protocol.FileItem, error) {
	type queueItem struct {
		path  string
		depth int
	}
	queue := []queueItem{{path: "", depth: 0}}
	items := make([]protocol.FileItem, 0)
	visited := 0
	for len(queue) > 0 && len(items) < maxItems && visited < maxItems {
		current := queue[0]
		queue = queue[1:]
		visited++
		envelope, err := s.sendAgentCommand(ctx, homeID, "files.list", assistantFileIndexListRequest(sourceID, current.path))
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
			items = append(items, item)
			if len(items) >= maxItems {
				break
			}
			if item.IsDirectory && current.depth < 4 {
				queue = append(queue, queueItem{path: item.Path, depth: current.depth + 1})
			}
		}
	}
	return items, nil
}

func (s *Server) assistantChunksForText(ctx context.Context, userID string, documentID string, text string, updatedAt time.Time, settingsOverride ...domain.AssistantSettings) ([]domain.AssistantChunk, error) {
	parts := chunkAssistantText(text, 1800)
	chunks := make([]domain.AssistantChunk, 0, len(parts))
	for index, part := range parts {
		embedding, model, version, err := s.embedAssistantIndexText(ctx, userID, part, settingsOverride...)
		if err != nil {
			return nil, err
		}
		embeddingJSON, _ := json.Marshal(embedding)
		chunks = append(chunks, domain.AssistantChunk{
			ID:               stableAssistantID("achunk", fmt.Sprintf("%s:%d:%x", documentID, index, embeddingJSON[:min(len(embeddingJSON), 16)])),
			DocumentID:       documentID,
			ChunkIndex:       index,
			Content:          part,
			TokenCount:       len(strings.Fields(part)),
			EmbeddingJSON:    string(embeddingJSON),
			EmbeddingModel:   model,
			EmbeddingVersion: version,
			UpdatedAt:        updatedAt,
		})
	}
	return chunks, nil
}

func (s *Server) embedAssistantIndexText(ctx context.Context, userID string, text string, settingsOverride ...domain.AssistantSettings) ([]float64, string, string, error) {
	cfg := s.assistantAI
	if len(settingsOverride) > 0 {
		cfg = assistantAIConfigWithSettings(cfg, settingsOverride[0])
	}
	cfg.normalize()
	embedding, model, version := s.embedAssistantText(ctx, userID, text, settingsOverride...)
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	if model == "local-hash" && (cfg.OllamaBaseURL != "" || cfg.OpenAIAPIKey != "") {
		return nil, "", "", errors.New("configured embedding provider unavailable")
	}
	return embedding, model, version, nil
}

func chunkAssistantText(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	var chunks []string
	for len(text) > maxChars {
		cut := strings.LastIndex(text[:maxChars], "\n")
		if cut < maxChars/3 {
			cut = strings.LastIndex(text[:maxChars], " ")
		}
		if cut < maxChars/3 {
			cut = maxChars
		}
		chunks = append(chunks, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

func shouldIndexFiles(prompt string) bool {
	return classifyAssistantIntent(prompt).Kind == assistantIntentFilesSearch
}

func shouldIndexHomeAssistant(prompt string) bool {
	return classifyAssistantIntent(prompt).Kind == assistantIntentHomeAssistantQuery
}

func assistantAttributesText(attributes map[string]any) string {
	if len(attributes) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(attributes)
	return string(encoded)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstTime(values ...any) time.Time {
	for _, value := range values {
		switch typed := value.(type) {
		case *time.Time:
			if typed != nil && !typed.IsZero() {
				return *typed
			}
		case time.Time:
			if !typed.IsZero() {
				return typed
			}
		}
	}
	return time.Now().UTC()
}
