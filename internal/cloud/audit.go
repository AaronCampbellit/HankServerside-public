package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	auditSeverityInfo     = "info"
	auditSeverityWarning  = "warning"
	auditSeverityCritical = "critical"
)

func (s *Server) audit(ctx context.Context, eventType string, severity string, actorUserID string, actorAgentID string, homeID string, requestID string, targetType string, targetID string, metadata map[string]any) {
	if strings.TrimSpace(severity) == "" {
		severity = auditSeverityInfo
	}
	data := "{}"
	if metadata != nil {
		if encoded, err := json.Marshal(metadata); err == nil {
			data = string(encoded)
		}
	}
	event := store.AuditEvent{
		ID:           newID("audit"),
		OccurredAt:   time.Now().UTC(),
		ActorUserID:  nullableString(actorUserID),
		ActorAgentID: nullableString(actorAgentID),
		HomeID:       nullableString(homeID),
		EventType:    eventType,
		Severity:     severity,
		RequestID:    requestID,
		TargetType:   targetType,
		TargetID:     targetID,
		MetadataJSON: data,
	}
	if err := s.store.CreateAuditEvent(ctx, event); err != nil {
		s.logger.Warn("failed to record audit event", "event_type", eventType, "error", err)
	}
}

func nullableString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func stableAuditTarget(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(value))))
	return hex.EncodeToString(sum[:12])
}

func auditPathMetadata(sourceID string, path string) map[string]any {
	metadata := map[string]any{
		"path_hash": stableAuditTarget(path),
	}
	if strings.TrimSpace(sourceID) != "" {
		metadata["source_id"] = strings.TrimSpace(sourceID)
	}
	return metadata
}

func (s *Server) auditHomeIDForUser(ctx context.Context, userID string) string {
	home, _, err := s.requireSingletonHomeMembership(ctx, userID)
	if err != nil {
		return ""
	}
	return home.ID
}

func (s *Server) auditHomeIDForEmail(ctx context.Context, email string) string {
	user, err := s.store.GetUserByEmail(ctx, strings.TrimSpace(strings.ToLower(email)))
	if err != nil {
		return ""
	}
	return s.auditHomeIDForUser(ctx, user.ID)
}

func (s *Server) handleHomeAuditEvents(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "audit-events" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.ListAuditEvents(
		r.Context(),
		home.ID,
		strings.TrimSpace(r.URL.Query().Get("event_type")),
		strings.TrimSpace(r.URL.Query().Get("severity")),
		strings.TrimSpace(r.URL.Query().Get("target_type")),
		limit,
		strings.TrimSpace(r.URL.Query().Get("sort")),
		strings.TrimSpace(r.URL.Query().Get("order")),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	_ = auth
	writeJSON(w, http.StatusOK, map[string]any{"events": nonNilSlice(s.auditEventSnapshots(r.Context(), home.ID, events))})
	return true
}

type auditPresentationLabels struct {
	actors  map[string]string
	targets map[string]string
}

func (s *Server) auditEventSnapshots(ctx context.Context, homeID string, events []store.AuditEvent) []map[string]any {
	labels := s.auditPresentationLabels(ctx, homeID, events)
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		metadata := redactAuditMetadata(event.MetadataJSON)
		actorLabel := "System"
		if event.ActorUserID != nil {
			actorLabel = labels.actors["user:"+*event.ActorUserID]
		} else if event.ActorAgentID != nil {
			actorLabel = labels.actors["agent:"+*event.ActorAgentID]
		}
		if strings.TrimSpace(actorLabel) == "" {
			actorLabel = "Unknown actor"
		}
		if auditMetadataString(metadata, "initiator") == "hank_ai" {
			if actorLabel == "Unknown actor" {
				actorLabel = "HankAI"
			} else {
				actorLabel = "HankAI (approved by " + actorLabel + ")"
			}
		}
		out = append(out, map[string]any{
			"id":             event.ID,
			"occurred_at":    event.OccurredAt,
			"actor_user_id":  event.ActorUserID,
			"actor_agent_id": event.ActorAgentID,
			"home_id":        event.HomeID,
			"event_type":     event.EventType,
			"severity":       event.Severity,
			"request_id":     event.RequestID,
			"ip_hash":        event.IPHash,
			"target_type":    event.TargetType,
			"target_id":      event.TargetID,
			"actor_label":    actorLabel,
			"target_label":   labels.targets[event.TargetType+":"+event.TargetID],
			"helper_text":    auditEventHelperText(event, metadata),
			"metadata":       metadata,
		})
	}
	return out
}

func (s *Server) auditPresentationLabels(ctx context.Context, homeID string, events []store.AuditEvent) auditPresentationLabels {
	labels := auditPresentationLabels{actors: map[string]string{}, targets: map[string]string{}}
	members, err := s.store.ListHomeMembers(ctx, homeID)
	if err != nil {
		return labels
	}
	memberIDs := make(map[string]bool, len(members))
	for _, member := range members {
		memberIDs[member.UserID] = true
		label := strings.TrimSpace(member.DisplayName)
		if label == "" {
			label = strings.TrimSpace(member.Email)
		}
		labels.actors["user:"+member.UserID] = label
	}

	seenAgents := map[string]bool{}
	seenNotes := map[string]bool{}
	for _, event := range events {
		if event.ActorAgentID != nil && !seenAgents[*event.ActorAgentID] {
			seenAgents[*event.ActorAgentID] = true
			if agent, err := s.store.GetAgentByID(ctx, *event.ActorAgentID); err == nil && agent.HomeID == homeID {
				label := strings.TrimSpace(agent.Name)
				if label == "" {
					label = "Hank Agent"
				}
				labels.actors["agent:"+agent.ID] = label
			}
		}
		if event.TargetType != "note" || strings.TrimSpace(event.TargetID) == "" || seenNotes[event.TargetID] {
			continue
		}
		seenNotes[event.TargetID] = true
		note, err := s.store.GetUserNoteByID(ctx, event.TargetID)
		if err != nil || (note.HomeID != homeID && !(note.HomeID == "" && memberIDs[note.OwnerUserID])) {
			continue
		}
		title := strings.TrimSpace(note.Title)
		if title == "" {
			title = "Untitled note"
		}
		labels.targets["note:"+event.TargetID] = title
	}
	return labels
}

func auditEventHelperText(event store.AuditEvent, metadata map[string]any) string {
	reason := auditMetadataString(metadata, "reason")
	operation := auditMetadataString(metadata, "operation")
	if strings.HasPrefix(event.EventType, "storage.") {
		if message := auditMetadataString(metadata, "message"); message != "" {
			return message
		}
		return "Storage operation recorded. Review its status and integrity metadata."
	}
	switch event.EventType {
	case "login.succeeded":
		return "User signed in successfully."
	case "login.failed":
		switch reason {
		case "unknown_user":
			return "Login failed because the email does not match a Hank account."
		case "bad_password":
			return "Login failed because the password did not match."
		case "login_backoff":
			return "Login was blocked by repeated failed attempts. Wait for the retry window or reset the password."
		case "rate_limited":
			return "Login was rate limited from this client. Wait before retrying and check for repeated attempts."
		default:
			return "Login failed. Check the account, password, and rate-limit state."
		}
	case "session.revoked":
		return "User session was signed out or revoked."
	case "password.changed":
		return "User password was changed."
	case "user.display_name_updated":
		return "A member display name was updated."
	case "invitation.created":
		return "An admin created a one-time home invitation."
	case "invitation.cancelled":
		return "An admin cancelled a pending home invitation."
	case "invitation.accepted":
		return "An existing user accepted a home invitation."
	case "invitation.signup":
		return "A user accepted an invitation and signed in."
	case "file_transfer.requested":
		return "A file " + auditOperationLabel(operation) + " was requested. Use the transfer ID to follow progress."
	case "file_transfer.setup_failed":
		switch reason {
		case "upload_size_limit":
			return "File upload was larger than the configured source policy allows."
		case "agent_offline":
			return "File transfer could not start because the primary Hank Agent was offline."
		default:
			return "File transfer setup failed before streaming began."
		}
	case "file_transfer.failed":
		switch reason {
		case "agent_offline":
			return "File transfer failed because the primary Hank Agent was offline."
		case "ready_timeout", "complete_timeout":
			return "File transfer timed out waiting for the primary Hank Agent."
		case "upload_too_large":
			return "File upload exceeded the configured source policy size limit."
		case "transfer_offset_mismatch":
			return "File transfer stopped because client and agent offsets did not match."
		default:
			return "File transfer failed. Check agent status, source policy, and transfer metadata."
		}
	case "file_operation.denied":
		return "File action was blocked by source policy. Check allowed prefixes, blocked prefixes, and permissions."
	case "file_operation.requested":
		return "A managed file operation was queued. Check the related file job for progress or rollback."
	case "agent.restart_requested":
		return "Agent restart was requested from the dashboard."
	case "app_package.previewed":
		return "App package preview completed and is ready for review before activation."
	case "app_package.preview_failed":
		switch reason {
		case "admin_required":
			return "App package preview was blocked because the user is not a home admin."
		case "agent_offline":
			return "App package preview could not run because the primary Hank Agent was offline."
		case "package_too_large":
			return "App package upload exceeded the package size limit."
		case "app_package_invalid":
			return "App package validation failed. Check the package manifest and schema."
		default:
			return "App package preview failed. Check package validity and agent status."
		}
	case "app_package.activated":
		return "App package was installed or activated for this home."
	case "app_package.activate_failed":
		switch reason {
		case "admin_required":
			return "App activation was blocked because the user is not a home admin."
		case "app_staging_missing", "app_staging_expired":
			return "App activation failed because the staged package was missing or expired."
		case "app_package_invalid":
			return "App activation failed because the package did not pass validation."
		default:
			return "App activation failed. Preview the package again and check agent status."
		}
	case "app.uninstalled":
		return "App package, settings, and stored secrets were removed from the home connector."
	case "app.uninstall_failed":
		switch reason {
		case "admin_required":
			return "App uninstall was blocked because the user is not a home admin."
		case "agent_offline":
			return "App uninstall could not run because the home connector was offline."
		case "agent_response_mismatch":
			return "App uninstall stopped because the connector response did not match the requested app."
		case "persist_delete_failed":
			return "The connector removed the app, but Hank could not remove its server record. Retry the uninstall."
		default:
			return "App uninstall failed. Check connector status and retry."
		}
	case "service_profile.changed":
		return "Connection settings changed. Verify the primary Hank Agent can still reach the service."
	case "permission.changed":
		return "Home permissions changed. Review member access if behavior changed."
	case "homeassistant.control.succeeded":
		return "Home Assistant control succeeded: " + auditHomeAssistantOperationLabel(operation) + "."
	case "homeassistant.control.failed":
		return "Home Assistant control failed: " + auditHomeAssistantOperationLabel(operation) + "."
	case "password.reset":
		return "An admin reset a user password."
	default:
		if event.Severity == auditSeverityCritical {
			return "Critical security event. Review metadata and recent user actions."
		}
		if event.Severity == auditSeverityWarning {
			return "Warning event. Review metadata and related agent or policy state."
		}
		return "Informational event. Review metadata for context."
	}
}

func auditMetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func auditOperationLabel(operation string) string {
	switch strings.TrimSpace(operation) {
	case "upload":
		return "upload"
	case "download":
		return "download"
	default:
		return "transfer"
	}
}

func auditHomeAssistantOperationLabel(operation string) string {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return "requested action"
	}
	return strings.ReplaceAll(operation, "_", " ")
}

func redactAuditMetadata(raw string) map[string]any {
	var metadata map[string]any
	if json.Unmarshal([]byte(raw), &metadata) != nil {
		return map[string]any{}
	}
	for key, value := range metadata {
		lowered := strings.ToLower(key)
		if strings.Contains(lowered, "token") || strings.Contains(lowered, "secret") || strings.Contains(lowered, "password") {
			metadata[key] = "[redacted]"
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			encoded, _ := json.Marshal(nested)
			metadata[key] = redactAuditMetadata(string(encoded))
		}
	}
	return metadata
}
