package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	notificationKindMonitoring     = "monitoring.alert"
	notificationKindNoteChanged    = "notes.changed"
	notificationKindEntityChanged  = "dashboard_entities.changed"
	notificationKindAgentOffline   = "agent.offline"
	notificationKindAgentRecovered = "agent.recovered"
	notificationKindDiskLow        = "agent.disk_low"
	notificationKindDiskRecovered  = "agent.disk_recovered"
	notificationKindQuickLinkDown  = "quick_links.down"
	notificationKindQuickLinkUp    = "quick_links.recovered"
)

type NotificationEvent struct {
	Kind             string
	HomeID           string
	AuditEventID     string
	ActorUserID      string
	ResourceID       string
	SourceEventKey   string
	CollapseKey      string
	Outcome          string
	DisplayName      string
	State            string
	Operation        string
	Status           string
	Severity         string
	IntegrityWarning bool
	OccurredAt       time.Time
}

type NotificationEmitResult struct {
	Created   int
	Coalesced int
}

type notificationTemplate struct {
	category string
	kind     string
	severity string
	title    string
	body     string
	target   string
	collapse string
	outcome  string
	apnsURL  string
}

type notificationService struct {
	store          *store.Store
	logger         *slog.Logger
	apns           func(context.Context, []string, PushNotification)
	webPushEnabled bool
}

func newNotificationService(db *store.Store, logger *slog.Logger, apns func(context.Context, []string, PushNotification)) *notificationService {
	return &notificationService{store: db, logger: logger, apns: apns}
}

func (s *Server) flushPendingNotificationSourceEvents(ctx context.Context) error {
	if s.notificationService == nil {
		return errors.New("notification service is unavailable")
	}
	events, err := s.store.ListPendingNotificationSourceEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, event := range events {
		_, err := s.notificationService.Emit(ctx, NotificationEvent{
			Kind: event.EventKind, HomeID: event.HomeID, ResourceID: event.ResourceID,
			SourceEventKey: event.EventKey, CollapseKey: event.SourceKey, Outcome: event.Outcome,
			DisplayName: event.DisplayName, Severity: event.Severity, OccurredAt: event.OccurredAt,
		})
		if err != nil {
			return err
		}
		if err := s.store.MarkNotificationSourceEventEmitted(ctx, event.ID, time.Now().UTC()); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (s *notificationService) Emit(ctx context.Context, event NotificationEvent) (NotificationEmitResult, error) {
	template, err := renderNotificationTemplate(event)
	if err != nil {
		return NotificationEmitResult{}, err
	}
	occurredAt := event.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	auditEventID := strings.TrimSpace(event.AuditEventID)
	if auditEventID == "" {
		auditEvent := notificationAuditEvent(event, template, occurredAt)
		if err := s.store.EnsureAuditEvent(ctx, auditEvent); err != nil {
			return NotificationEmitResult{}, fmt.Errorf("record notification audit event: %w", err)
		}
		auditEventID = auditEvent.ID
	}
	recipients, err := s.resolveRecipients(ctx, event, template.category)
	if err != nil {
		return NotificationEmitResult{}, err
	}
	recipients = cleanUserIDs(recipients)
	if len(recipients) == 0 {
		return NotificationEmitResult{}, nil
	}
	settings, err := s.store.ListNotificationSettingsForUsers(ctx, recipients)
	if err != nil {
		return NotificationEmitResult{}, err
	}
	subscriptionsByUser := map[string][]string{}
	if s.webPushEnabled {
		subscriptions, err := s.store.ListActiveWebPushSubscriptionsForUsers(ctx, recipients)
		if err != nil {
			return NotificationEmitResult{}, err
		}
		for _, subscription := range subscriptions {
			subscriptionsByUser[subscription.UserID] = append(subscriptionsByUser[subscription.UserID], subscription.ID)
		}
	}

	result := NotificationEmitResult{}
	pushRecipients := make([]string, 0, len(recipients))
	for _, userID := range recipients {
		deliveryIDs := []string(nil)
		if s.webPushEnabled && userSettingsAllowCategory(settings[userID], template.category) {
			deliveryIDs = subscriptionsByUser[userID]
		}
		input := domain.CreateUserNotificationInput{
			EventKey:                strings.TrimSpace(event.SourceEventKey),
			DeliverySubscriptionIDs: deliveryIDs,
			Notification: domain.UserNotification{
				ID:              newID("ntf"),
				UserID:          userID,
				HomeID:          strings.TrimSpace(event.HomeID),
				AuditEventID:    auditEventID,
				Category:        template.category,
				EventKind:       template.kind,
				Severity:        template.severity,
				Title:           template.title,
				Body:            template.body,
				TargetPath:      template.target,
				CollapseKey:     template.collapse,
				Outcome:         template.outcome,
				FirstOccurredAt: occurredAt,
				LastOccurredAt:  occurredAt,
				CreatedAt:       occurredAt,
			},
		}
		_, created, err := s.store.CreateOrCoalesceUserNotification(ctx, input)
		if err != nil {
			return result, err
		}
		if created {
			result.Created++
			pushRecipients = append(pushRecipients, userID)
		} else {
			result.Coalesced++
		}
	}
	if len(pushRecipients) > 0 && s.apns != nil {
		s.apns(ctx, pushRecipients, PushNotification{Category: template.category, Title: template.title, Body: template.body, URL: template.apnsURL, ThreadID: template.collapse})
	}
	return result, nil
}

func notificationAuditEvent(event NotificationEvent, template notificationTemplate, occurredAt time.Time) store.AuditEvent {
	digest := sha256.Sum256([]byte(strings.TrimSpace(event.HomeID) + "\x00" + strings.TrimSpace(event.SourceEventKey)))
	return store.AuditEvent{
		ID:           "audit_notification_" + hex.EncodeToString(digest[:16]),
		OccurredAt:   occurredAt,
		ActorUserID:  nullableString(event.ActorUserID),
		HomeID:       nullableString(event.HomeID),
		EventType:    strings.TrimSpace(event.Kind),
		Severity:     template.severity,
		TargetType:   notificationAuditTargetType(template.category),
		TargetID:     strings.TrimSpace(event.ResourceID),
		MetadataJSON: "{}",
	}
}

func notificationAuditTargetType(category string) string {
	switch category {
	case domain.NotificationCategoryMonitoring:
		return "monitoring_alert"
	case domain.NotificationCategoryStorage:
		return "storage"
	case domain.NotificationCategoryNotes:
		return "note"
	case domain.NotificationCategoryDashboardEntities:
		return "dashboard_entity"
	case domain.NotificationCategoryAgentHealth:
		return "agent"
	case domain.NotificationCategoryQuickLinks:
		return "quick_link"
	default:
		return "notification"
	}
}

func (s *notificationService) resolveRecipients(ctx context.Context, event NotificationEvent, category string) ([]string, error) {
	switch category {
	case domain.NotificationCategoryMonitoring:
		settings, err := s.store.GetMonitoringSettings(ctx, event.HomeID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil && !settings.InboxEnabled {
			return nil, nil
		}
		if err == nil && settings.InboxAudience == "members" {
			members, err := s.store.ListHomeMembers(ctx, event.HomeID)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(members))
			for _, member := range members {
				ids = append(ids, member.UserID)
			}
			return ids, nil
		}
		return s.store.ListStorageNotificationUserIDs(ctx, event.HomeID)
	case domain.NotificationCategoryStorage:
		return s.store.ListStorageNotificationUserIDs(ctx, event.HomeID)
	case domain.NotificationCategoryNotes:
		return s.store.ListNoteNotificationUserIDs(ctx, event.ResourceID, event.ActorUserID)
	case domain.NotificationCategoryDashboardEntities:
		return s.store.ListDashboardEntityNotificationUserIDs(ctx, event.HomeID, event.ResourceID)
	case domain.NotificationCategoryAgentHealth, domain.NotificationCategoryQuickLinks:
		members, err := s.store.ListHomeMembers(ctx, event.HomeID)
		if err != nil {
			return nil, err
		}
		userIDs := make([]string, 0, len(members))
		for _, member := range members {
			userIDs = append(userIDs, member.UserID)
		}
		return userIDs, nil
	default:
		return nil, errors.New("unsupported notification category")
	}
}

func renderNotificationTemplate(event NotificationEvent) (notificationTemplate, error) {
	resourceID := strings.TrimSpace(event.ResourceID)
	displayName := boundedNotificationText(event.DisplayName, 80)
	state := boundedNotificationText(event.State, 80)
	template := notificationTemplate{kind: strings.TrimSpace(event.Kind), severity: "info", outcome: boundedNotificationText(event.Outcome, 80)}
	switch {
	case event.Kind == notificationKindMonitoring:
		template.category = domain.NotificationCategoryMonitoring
		template.collapse = "monitoring-" + resourceID
		template.title = "Monitoring alert"
		template.body = displayName
		template.severity = notificationSeverity(event.Severity)
		if event.Status == "resolved" {
			template.title = "Monitoring alert resolved"
			template.severity = "info"
		}
		template.target = "/dashboard/settings/logs"
		template.apnsURL = template.target
	case strings.HasPrefix(event.Kind, "storage."):
		notification, ok := storageNotificationFromFacts(event.Operation, event.Status, event.Severity, event.IntegrityWarning)
		if !ok {
			return notificationTemplate{}, errors.New("unsupported storage notification event")
		}
		template.category = domain.NotificationCategoryStorage
		template.severity = notificationSeverity(event.Severity)
		template.title, template.body = notification.Title, notification.Body
		template.target = "/dashboard/settings/backups"
		template.collapse = firstNonEmpty(strings.TrimSpace(event.CollapseKey), notification.ThreadID)
		template.apnsURL = notification.URL
	case event.Kind == notificationKindNoteChanged:
		template.category = domain.NotificationCategoryNotes
		template.title = "Note Edited"
		template.body = "A shared Hank note was updated."
		template.target = "/dashboard/profile-notes?note=" + url.QueryEscape(resourceID)
		template.collapse = firstNonEmpty(strings.TrimSpace(event.CollapseKey), "notes-"+resourceID)
		template.apnsURL = "hank://notifications/notes/" + url.PathEscape(resourceID)
	case event.Kind == notificationKindEntityChanged:
		template.category = domain.NotificationCategoryDashboardEntities
		template.title = "Dashboard Entity Changed"
		if displayName != "" {
			template.title = displayName + " Changed"
		}
		template.body = "A dashboard entity changed state."
		if state != "" {
			template.body = "Current state: " + state
		}
		template.target = "/dashboard/home-assistant?entity=" + url.QueryEscape(resourceID)
		template.collapse = firstNonEmpty(strings.TrimSpace(event.CollapseKey), "dashboard-"+resourceID)
		template.apnsURL = "hank://notifications/dashboard/" + url.PathEscape(resourceID)
	case event.Kind == notificationKindAgentOffline || event.Kind == notificationKindAgentRecovered || event.Kind == notificationKindDiskLow || event.Kind == notificationKindDiskRecovered:
		template.category = domain.NotificationCategoryAgentHealth
		template.collapse = firstNonEmpty(strings.TrimSpace(event.CollapseKey), "agent-"+resourceID)
		template.target = "/dashboard/agents/" + url.PathEscape(resourceID)
		template.apnsURL = template.target
		if event.Kind == notificationKindAgentOffline {
			template.severity, template.title, template.body = "warning", "Agent offline", "A Hank Agent is offline."
		} else if event.Kind == notificationKindAgentRecovered {
			template.title, template.body = "Agent recovered", "A Hank Agent is online again."
		} else if event.Kind == notificationKindDiskLow {
			template.severity, template.title, template.body = "warning", "Agent disk space low", "A Hank Agent is running low on disk space."
		} else {
			template.title, template.body = "Agent disk recovered", "A Hank Agent has sufficient disk space again."
		}
	case event.Kind == notificationKindQuickLinkDown || event.Kind == notificationKindQuickLinkUp:
		template.category = domain.NotificationCategoryQuickLinks
		template.collapse = firstNonEmpty(strings.TrimSpace(event.CollapseKey), "quick-link-"+resourceID)
		template.target = "/dashboard/settings/quick-links"
		template.apnsURL = template.target
		if event.Kind == notificationKindQuickLinkDown {
			template.severity, template.title = "warning", "Quick link is down"
			template.body = firstNonEmpty(displayName, "A quick link") + " is failing health checks."
		} else {
			template.title = "Quick link recovered"
			template.body = firstNonEmpty(displayName, "A quick link") + " is available again."
		}
	default:
		return notificationTemplate{}, fmt.Errorf("unsupported notification event kind %q", event.Kind)
	}
	if template.outcome == "" {
		template.outcome = boundedNotificationText(event.Status, 80)
	}
	if template.kind == "" || template.category == "" || strings.TrimSpace(event.SourceEventKey) == "" || strings.TrimSpace(event.HomeID) == "" || resourceID == "" && template.category != domain.NotificationCategoryStorage {
		return notificationTemplate{}, errors.New("notification event is incomplete")
	}
	if !strings.HasPrefix(template.target, "/dashboard") {
		return notificationTemplate{}, errors.New("notification target must be a dashboard path")
	}
	template.title = boundedNotificationText(template.title, 160)
	template.body = boundedNotificationText(template.body, 512)
	return template, nil
}

func boundedNotificationText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func notificationSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "error":
		return "critical"
	case "warning":
		return "warning"
	default:
		return "info"
	}
}
