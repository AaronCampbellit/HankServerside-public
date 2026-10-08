package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) forwardStorageEvents(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	seeded := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.storage == nil {
				continue
			}
			events, err := s.storage.Events(storageops.EventFilter{Limit: 50})
			if err != nil {
				continue
			}
			for _, event := range events {
				if !s.markStorageEventSeen(event.ID) {
					continue
				}
				if seeded {
					s.publishStorageEvent(ctx, event)
				}
			}
			seeded = true
		}
	}
}

func (s *Server) markStorageEventSeen(eventID string) bool {
	if strings.TrimSpace(eventID) == "" {
		return false
	}
	s.storageEventsMu.Lock()
	defer s.storageEventsMu.Unlock()
	if _, ok := s.storageEvents[eventID]; ok {
		return false
	}
	s.storageEvents[eventID] = struct{}{}
	return true
}

func (s *Server) publishStorageEvent(ctx context.Context, event storageops.Event) {
	_ = s.markStorageEventSeen(event.ID)
	auditEventID := s.auditStorageEvent(ctx, event)
	s.emitStorageEvent(ctx, storageRealtimeEventName(event), storageRealtimePayload(event))
	s.notifyStorageEvent(ctx, event, auditEventID)
}

func (s *Server) auditStorageEvent(ctx context.Context, event storageops.Event) string {
	homeID := storageEventDetailString(event, "home_id")
	if homeID == "" {
		home, err := s.store.GetSingletonHome(ctx)
		if err != nil {
			s.logger.Warn("storage audit home lookup failed", "event_id", event.ID, "error", err)
			return ""
		}
		homeID = home.ID
	}
	auditEvent := storageAuditEvent(homeID, event)
	if err := s.store.CreateAuditEvent(ctx, auditEvent); err != nil {
		s.logger.Warn("failed to record storage audit event", "event_id", event.ID, "event_type", auditEvent.EventType, "error", err)
		return ""
	}
	return auditEvent.ID
}

func storageAuditEvent(homeID string, event storageops.Event) store.AuditEvent {
	event = storageops.RedactEvent(event)
	metadata := make(map[string]any, len(event.Details)+5)
	for key, value := range event.Details {
		switch key {
		case "home_id", "requested_by", "updated_by":
			continue
		}
		metadata[key] = value
	}
	metadata["operation"] = event.Operation
	metadata["status"] = event.Status
	metadata["storage_severity"] = event.Severity
	metadata["message"] = event.Message
	if event.BackupLabel != "" {
		metadata["backup_label"] = event.BackupLabel
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		encoded = []byte(`{}`)
	}
	occurredAt := event.Time
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return store.AuditEvent{
		ID:           newID("audit"),
		OccurredAt:   occurredAt,
		ActorUserID:  nullableString(firstNonEmpty(storageEventDetailString(event, "requested_by"), storageEventDetailString(event, "updated_by"))),
		HomeID:       nullableString(homeID),
		EventType:    "storage." + storageAuditSegment(event.Operation) + "." + storageAuditSegment(event.Status),
		Severity:     storageAuditSeverity(event.Severity),
		TargetType:   "storage",
		TargetID:     event.ID,
		MetadataJSON: string(encoded),
	}
}

func storageAuditSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var segment strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '_':
			segment.WriteRune(character)
		case character == '-' || character == ' ':
			segment.WriteByte('_')
		}
	}
	if segment.Len() == 0 {
		return "unknown"
	}
	return segment.String()
}

func storageAuditSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case storageops.EventSeverityCritical:
		return auditSeverityCritical
	case storageops.EventSeverityWarning, storageops.EventSeverityError:
		return auditSeverityWarning
	default:
		return auditSeverityInfo
	}
}

func storageEventDetailString(event storageops.Event, key string) string {
	value, _ := event.Details[key].(string)
	return strings.TrimSpace(value)
}

func storageRealtimePayload(event storageops.Event) map[string]any {
	return map[string]any{
		"event_id":     event.ID,
		"operation":    event.Operation,
		"status":       event.Status,
		"severity":     event.Severity,
		"message":      storageops.RedactSensitive(event.Message),
		"backup_label": event.BackupLabel,
	}
}

func storageRealtimeEventName(event storageops.Event) string {
	switch event.Operation {
	case storageops.EventOperationBackup:
		if storageops.IsFailureEvent(event) {
			return "storage.backup.failed"
		}
	case storageops.EventOperationChecksum, storageops.EventOperationAMCheck:
		if event.Severity == storageops.EventSeverityCritical || boolFromEventDetails(event, "corruption_detected") {
			return "storage.checksum.corruption"
		}
	case storageops.EventOperationRestoreTest, storageops.EventOperationPrimaryRestore:
		switch event.Status {
		case storageops.EventStatusStarted, storageops.EventStatusPending:
			return "storage.restore.started"
		case storageops.EventStatusSuccess:
			return "storage.restore.completed"
		case storageops.EventStatusFailed:
			return "storage.restore.failed"
		}
	}
	return "storage.health.changed"
}

func boolFromEventDetails(event storageops.Event, key string) bool {
	if event.Details == nil {
		return false
	}
	value, ok := event.Details[key]
	if !ok {
		return false
	}
	boolValue, _ := value.(bool)
	return boolValue
}
