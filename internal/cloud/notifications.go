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
	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) notificationSettingsOrDefault(ctx context.Context, userID string) domain.NotificationSettings {
	settings, err := s.store.GetNotificationSettings(ctx, userID)
	if err == nil {
		return settings
	}
	return domain.NotificationSettings{
		UserID:                   userID,
		AgentHealthEnabled:       true,
		QuickLinksEnabled:        true,
		StorageEnabled:           true,
		MonitoringEnabled:        true,
		NotesEnabled:             true,
		DashboardEntitiesEnabled: true,
		UpdatedAt:                time.Now().UTC(),
	}
}

func sanitizeNotificationCategories(values []string) []string {
	allowed := map[string]struct{}{
		domain.NotificationCategoryMonitoring:        {},
		domain.NotificationCategoryAgentHealth:       {},
		domain.NotificationCategoryQuickLinks:        {},
		domain.NotificationCategoryStorage:           {},
		domain.NotificationCategoryNotes:             {},
		domain.NotificationCategoryDashboardEntities: {},
	}
	if len(values) == 0 {
		return []string{
			domain.NotificationCategoryMonitoring,
			domain.NotificationCategoryAgentHealth,
			domain.NotificationCategoryQuickLinks,
			domain.NotificationCategoryStorage,
			domain.NotificationCategoryNotes,
			domain.NotificationCategoryDashboardEntities,
		}
	}
	seen := map[string]struct{}{}
	var categories []string
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		categories = append(categories, value)
	}
	return categories
}

func (s *Server) sendNotificationToUsers(ctx context.Context, userIDs []string, notification PushNotification) {
	userIDs = cleanUserIDs(userIDs)
	if len(userIDs) == 0 || s.pushSender == nil {
		return
	}
	settings, err := s.store.ListNotificationSettingsForUsers(ctx, userIDs)
	if err != nil {
		s.logger.Warn("notification settings lookup failed", "category", notification.Category, "error", err)
		return
	}
	devices, err := s.store.ListActiveAPNSDevicesForUsers(ctx, userIDs)
	if err != nil {
		s.logger.Warn("notification device lookup failed", "category", notification.Category, "error", err)
		return
	}
	for _, device := range devices {
		if !userSettingsAllowCategory(settings[device.UserID], notification.Category) || !deviceAllowsCategory(device, notification.Category) {
			continue
		}
		if err := s.pushSender.Send(ctx, device, notification); err != nil {
			s.logger.Warn("push notification send failed", "category", notification.Category, "user_id", device.UserID, "device_id", device.DeviceID, "error", err)
		}
	}
}

func userSettingsAllowCategory(settings domain.NotificationSettings, category string) bool {
	if settings.UserID == "" {
		return true
	}
	switch category {
	case domain.NotificationCategoryMonitoring:
		return settings.MonitoringEnabled
	case domain.NotificationCategoryAgentHealth:
		return settings.AgentHealthEnabled
	case domain.NotificationCategoryQuickLinks:
		return settings.QuickLinksEnabled
	case domain.NotificationCategoryStorage:
		return settings.StorageEnabled
	case domain.NotificationCategoryNotes:
		return settings.NotesEnabled
	case domain.NotificationCategoryDashboardEntities:
		return settings.DashboardEntitiesEnabled
	default:
		return false
	}
}

func deviceAllowsCategory(device domain.APNSDevice, category string) bool {
	if len(device.EnabledCategories) == 0 {
		return true
	}
	var categories []string
	if err := json.Unmarshal(device.EnabledCategories, &categories); err != nil || len(categories) == 0 {
		return true
	}
	for _, item := range categories {
		if strings.TrimSpace(item) == category {
			return true
		}
	}
	return false
}

func (s *Server) notifyStorageEvent(ctx context.Context, event storageops.Event, auditEventIDs ...string) {
	notification, ok := storageNotification(event)
	if !ok {
		return
	}
	home, err := s.store.GetSingletonHome(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("storage notification home lookup failed", "error", err)
		}
		return
	}
	_, err = s.notificationService.Emit(ctx, NotificationEvent{
		Kind:             "storage." + strings.TrimSpace(event.Operation) + "." + strings.TrimSpace(event.Status),
		HomeID:           home.ID,
		AuditEventID:     firstNonEmpty(auditEventIDs...),
		ResourceID:       firstNonEmpty(strings.TrimSpace(event.ID), strings.TrimSpace(event.Operation)),
		SourceEventKey:   "storage:" + strings.TrimSpace(event.ID),
		CollapseKey:      notification.ThreadID,
		Outcome:          event.Status,
		Operation:        event.Operation,
		Status:           event.Status,
		Severity:         event.Severity,
		IntegrityWarning: boolFromEventDetails(event, "corruption_detected"),
		OccurredAt:       event.Time,
	})
	if err != nil {
		s.logger.Warn("storage notification emit failed", "event_id", event.ID, "error", err)
	}
}

func storageNotificationFromFacts(operation, status, severity string, integrityWarning bool) (PushNotification, bool) {
	event := storageops.Event{Operation: operation, Status: status, Severity: severity}
	if integrityWarning {
		event.Details = map[string]any{"corruption_detected": true}
	}
	return storageNotification(event)
}

func storageNotification(event storageops.Event) (PushNotification, bool) {
	operation := strings.TrimSpace(event.Operation)
	status := strings.TrimSpace(event.Status)
	switch operation {
	case storageops.EventOperationBackup:
		switch status {
		case storageops.EventStatusPending, storageops.EventStatusStarted:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Backup Started",
				Body:     "A Hank database backup started.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-backup",
			}, true
		case storageops.EventStatusSuccess:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Backup Completed",
				Body:     "A Hank database backup completed.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-backup",
			}, true
		case storageops.EventStatusFailed:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Backup Failed",
				Body:     "A Hank database backup needs attention.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-backup",
			}, true
		}
	case storageops.EventOperationChecksum, storageops.EventOperationAMCheck:
		if event.Severity == storageops.EventSeverityCritical || boolFromEventDetails(event, "corruption_detected") {
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Storage Alert",
				Body:     "Hank detected a storage integrity problem.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-integrity",
			}, true
		}
	case storageops.EventOperationRestoreTest, storageops.EventOperationPrimaryRestore:
		switch status {
		case storageops.EventStatusPending, storageops.EventStatusStarted:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Restore Started",
				Body:     "A Hank restore operation started.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-restore",
			}, true
		case storageops.EventStatusSuccess:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Restore Completed",
				Body:     "A Hank restore operation completed.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-restore",
			}, true
		case storageops.EventStatusFailed:
			return PushNotification{
				Category: domain.NotificationCategoryStorage,
				Title:    "Restore Failed",
				Body:     "A Hank restore operation needs attention.",
				URL:      "hank://notifications/storage",
				ThreadID: "storage-restore",
			}, true
		}
	}
	return PushNotification{}, false
}

func (s *Server) notifyNoteChanged(ctx context.Context, noteInternalID string, noteKey string, actorUserID string, versions ...string) {
	deepLinkID := strings.TrimSpace(noteInternalID)
	if deepLinkID == "" {
		deepLinkID = strings.TrimSpace(noteKey)
	}
	version := strings.Join(versions, ":")
	if strings.TrimSpace(version) == "" {
		version = strings.TrimSpace(noteKey) + ":" + strings.TrimSpace(actorUserID)
	}
	homeID := ""
	if note, err := s.store.GetUserNoteByID(ctx, noteInternalID); err == nil {
		homeID = note.HomeID
	}
	if homeID == "" {
		if home, err := s.store.GetSingletonHome(ctx); err == nil {
			homeID = home.ID
		}
	}
	_, err := s.notificationService.Emit(ctx, NotificationEvent{
		Kind:           notificationKindNoteChanged,
		HomeID:         homeID,
		ActorUserID:    actorUserID,
		ResourceID:     deepLinkID,
		SourceEventKey: "notes:" + strings.TrimSpace(noteInternalID) + ":" + version,
		CollapseKey:    "notes-" + strings.TrimSpace(noteInternalID),
		Outcome:        "changed",
		OccurredAt:     time.Now().UTC(),
	})
	if err != nil {
		s.logger.Warn("note notification emit failed", "note_id", noteKey, "error", err)
	}
}

func (s *Server) claimNotificationEvent(key string, window time.Duration) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	now := time.Now().UTC()
	s.notificationEventsMu.Lock()
	defer s.notificationEventsMu.Unlock()
	if s.notificationEvents == nil {
		s.notificationEvents = make(map[string]time.Time)
	}
	if seenAt, ok := s.notificationEvents[key]; ok && now.Sub(seenAt) < window {
		return false
	}
	for eventKey, seenAt := range s.notificationEvents {
		if now.Sub(seenAt) > window*4 {
			delete(s.notificationEvents, eventKey)
		}
	}
	s.notificationEvents[key] = now
	return true
}

func (s *Server) notifyDashboardEntityChanged(ctx context.Context, homeID string, body json.RawMessage) {
	var payload struct {
		State protocol.HomeAssistantState `json:"state"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Warn("dashboard entity notification payload decode failed", "error", err)
		return
	}
	entityID := strings.TrimSpace(payload.State.EntityID)
	if entityID == "" {
		return
	}
	friendlyName, _ := payload.State.Attributes["friendly_name"].(string)
	occurredAt := time.Now().UTC()
	if payload.State.LastUpdated != nil {
		occurredAt = payload.State.LastUpdated.UTC()
	} else if payload.State.LastChanged != nil {
		occurredAt = payload.State.LastChanged.UTC()
	}
	eventKey := fmt.Sprintf("dashboard:%s:%s:%s:%d", homeID, entityID, strings.TrimSpace(payload.State.State), occurredAt.UnixNano())
	_, err := s.notificationService.Emit(ctx, NotificationEvent{
		Kind:           notificationKindEntityChanged,
		HomeID:         homeID,
		ResourceID:     entityID,
		SourceEventKey: eventKey,
		CollapseKey:    "dashboard-" + entityID,
		Outcome:        strings.TrimSpace(payload.State.State),
		DisplayName:    friendlyName,
		State:          payload.State.State,
		OccurredAt:     occurredAt,
	})
	if err != nil {
		s.logger.Warn("dashboard entity notification emit failed", "entity_id", entityID, "error", err)
	}
}

func cleanUserIDs(values []string) []string {
	seen := map[string]struct{}{}
	var cleaned []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned
}
