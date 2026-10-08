package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	topicHomeAssistantStates = "homeassistant.states"
	topicNotesHome           = "notes.home"
	topicNotesProfile        = "notes.profile"
	topicHomeStatus          = "home.status"
	topicHomeSettings        = "home.settings"
	topicHomeMembers         = "home.members"
	topicHomePermissions     = "home.permissions"
	topicStorage             = "storage.health"
	topicMediaDownloads      = "media.downloads"
)

func fileDirectoryTopic(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	return "files.directory:" + path
}

func noteCollabTopic(noteID string) string {
	return "notes.collab:" + strings.TrimSpace(noteID)
}

func scopedNoteCollabTopic(scope string, noteID string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return noteCollabTopic(noteID)
	}
	return "notes.collab:" + scope + ":" + strings.TrimSpace(noteID)
}

func scopedHomeTopic(homeID string, topic string) string {
	return topic + "@home:" + strings.TrimSpace(homeID)
}

func scopedUserTopic(userID string, topic string) string {
	return topic + "@user:" + strings.TrimSpace(userID)
}

func (s *Server) forwardStoreNotifications(ctx context.Context) {
	notifications, err := s.store.Listen(ctx, store.NotificationChannelNotes, store.NotificationChannelProfiles)
	if err != nil {
		s.logger.Warn("postgres realtime listener unavailable", "error", err)
		return
	}
	for notification := range notifications {
		switch notification.Channel {
		case store.NotificationChannelNotes:
			s.forwardNoteNotification(ctx, notification.Payload)
		case store.NotificationChannelProfiles:
			s.forwardProfileNotification(ctx, notification.Payload)
		}
	}
}

func (s *Server) forwardNoteNotification(ctx context.Context, payload json.RawMessage) {
	var event struct {
		Event          string `json:"event"`
		NoteID         string `json:"note_id"`
		NoteInternalID string `json:"note_internal_id"`
		HomeID         string `json:"home_id"`
		OwnerUserID    string `json:"owner_user_id"`
		UpdatedBy      string `json:"updated_by"`
		CollabVersion  int64  `json:"collab_version"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		s.logger.Warn("bad note notification payload", "error", err)
		return
	}
	if strings.TrimSpace(event.Event) == "" {
		event.Event = "notes.changed"
	}
	body := map[string]any{
		"note_id":          event.NoteID,
		"note_internal_id": event.NoteInternalID,
		"home_id":          event.HomeID,
		"user_id":          event.OwnerUserID,
		"owner_user_id":    event.OwnerUserID,
		"updated_by":       event.UpdatedBy,
		"collab_version":   event.CollabVersion,
	}
	s.broadcastAppEventOnKey(ctx, scopedUserTopic(event.OwnerUserID, topicNotesProfile), topicNotesProfile, event.Event, body)
	if event.HomeID != "" {
		s.broadcastAppEventOnKey(ctx, scopedHomeTopic(event.HomeID, topicNotesHome), topicNotesHome, event.Event, body)
		s.broadcastAppEventOnKey(ctx, scopedHomeTopic(event.HomeID, scopedNoteCollabTopic("home", event.NoteID)), scopedNoteCollabTopic("home", event.NoteID), event.Event, body)
	}
	s.broadcastAppEventOnKey(ctx, scopedUserTopic(event.OwnerUserID, noteCollabTopic(event.NoteID)), noteCollabTopic(event.NoteID), event.Event, body)
	s.broadcastAppEventOnKey(ctx, scopedUserTopic(event.OwnerUserID, scopedNoteCollabTopic("profile", event.NoteID)), scopedNoteCollabTopic("profile", event.NoteID), event.Event, body)
	if event.Event == "notes.changed" || event.Event == "notes.collab.ops" {
		s.notifyNoteChanged(ctx, event.NoteInternalID, event.NoteID, event.UpdatedBy, event.Event, fmt.Sprint(event.CollabVersion))
	}
}

func (s *Server) forwardProfileNotification(ctx context.Context, payload json.RawMessage) {
	var event struct {
		Event    string `json:"event"`
		UserID   string `json:"user_id"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		s.logger.Warn("bad profile notification payload", "error", err)
		return
	}
	if strings.TrimSpace(event.Event) == "" {
		event.Event = "profile.changed"
	}
	topic := "profile.settings"
	if event.Event == "profile.secret_vault_changed" {
		topic = "profile.secret_vault"
	}
	s.broadcastAppEventOnKey(ctx, scopedUserTopic(event.UserID, topic), topic, event.Event, map[string]any{
		"user_id":  event.UserID,
		"revision": event.Revision,
	})
}

func (s *Server) handleRealtimeCommand(ctx context.Context, app *appConnection, peer *wsPeer, envelope protocol.Envelope, auth authContext, command protocol.RoutedCommand) bool {
	switch command.Command {
	case "app.subscribe", "app.unsubscribe":
		var request protocol.AppSubscribeRequest
		if len(command.Body) > 0 {
			if err := json.Unmarshal(command.Body, &request); err != nil {
				_ = s.writeAppCommandError(ctx, peer, envelope, "bad_subscribe_payload", err.Error(), nil)
				return true
			}
		}
		topics := cleanTopics(request.Topics)
		authorizedTopics, err := s.authorizeRealtimeTopics(ctx, auth, topics)
		if err != nil {
			_ = s.writeAppCommandError(ctx, peer, envelope, "permission_denied", err.Error(), nil)
			return true
		}
		var current []string
		if command.Command == "app.subscribe" {
			current = app.subscribe(authorizedTopics)
		} else {
			current = app.unsubscribe(authorizedTopics)
		}
		s.logger.Info("app realtime subscription changed", "session_id", app.sessionID, "user_id", app.userID, "command", command.Command, "topics", strings.Join(topics, ","), "current_topics", strings.Join(current, ","))
		_ = writeAppResponse(ctx, peer, envelope, protocol.AppSubscribeResponse{Topics: current})
		return true
	default:
		return false
	}
}

func (s *Server) authorizeRealtimeTopics(ctx context.Context, auth authContext, topics []string) ([]string, error) {
	authorized := make([]string, 0, len(topics))
	var home domain.Home
	var membership domain.HomeMembership
	var resolved bool
	resolveHome := func() error {
		if resolved {
			return nil
		}
		var err error
		home, membership, err = s.requireSingletonHomeMembership(ctx, auth.User.ID)
		if err != nil {
			return err
		}
		resolved = true
		return nil
	}
	for _, topic := range topics {
		switch {
		case strings.HasPrefix(topic, "desktop.session:"):
			sessionID := strings.TrimSpace(strings.TrimPrefix(topic, "desktop.session:"))
			if !validDesktopResourceID(sessionID) {
				return nil, errors.New("unsupported realtime topic")
			}
			session, err := s.store.GetDesktopSessionForUser(ctx, sessionID, auth.User.ID)
			if err != nil {
				return nil, errors.New("desktop session topic is not owned by this user")
			}
			if _, err := s.store.GetHomeMembership(ctx, session.HomeID, auth.User.ID); err != nil {
				return nil, errors.New("desktop session topic is not owned by this user")
			}
			authorized = append(authorized, topic)
		case strings.HasPrefix(topic, shellSessionTopicPrefix):
			if err := resolveHome(); err != nil {
				return nil, err
			}
			if membership.Role != domain.HomeRoleAdmin || !s.shellSessions.ownsTopic(topic, home.ID, auth.User.ID) {
				return nil, errors.New("shell session topic is not owned by this user")
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
		case topic == "profile.settings" || topic == "profile.secret_vault" || topic == topicNotesProfile:
			authorized = append(authorized, scopedUserTopic(auth.User.ID, topic))
		case topic == topicHomeAssistantStates:
			if err := resolveHome(); err != nil {
				return nil, err
			}
			if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureHomeAssistant); err != nil {
				return nil, err
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
		case strings.HasPrefix(topic, "notes.collab:"):
			scope, noteID := parseRealtimeNoteCollabTopic(topic)
			if noteID == "" {
				return nil, errors.New("unsupported realtime topic")
			}
			if scope == "profile" {
				if _, err := s.store.GetProfileNote(ctx, auth.User.ID, noteID); err != nil {
					return nil, err
				}
				authorized = append(authorized, scopedUserTopic(auth.User.ID, topic))
				continue
			}
			if err := resolveHome(); err != nil {
				return nil, err
			}
			if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
				return nil, err
			}
			if _, err := s.store.GetHomeNoteVisibleToUser(ctx, home.ID, auth.User.ID, noteID); err != nil {
				return nil, err
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
		case topic == topicNotesHome:
			if err := resolveHome(); err != nil {
				return nil, err
			}
			if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureNotes); err != nil {
				return nil, err
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
		case strings.HasPrefix(topic, "files.directory:") || topic == "files.jobs":
			if err := resolveHome(); err != nil {
				return nil, err
			}
			if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
				return nil, err
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
		case topic == topicHomeStatus || topic == topicHomeSettings || topic == topicHomeMembers || topic == topicHomePermissions || topic == topicStorage || topic == topicMediaDownloads || topic == topicAgentsHealth:
			if err := resolveHome(); err != nil {
				return nil, err
			}
			authorized = append(authorized, scopedHomeTopic(home.ID, topic))
			if topic == topicStorage {
				authorized = append(authorized, topic)
			}
		default:
			return nil, errors.New("unsupported realtime topic")
		}
	}
	return authorized, nil
}

func parseRealtimeNoteCollabTopic(topic string) (string, string) {
	rest := strings.TrimPrefix(topic, "notes.collab:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) == 2 && (parts[0] == "home" || parts[0] == "profile") {
		return parts[0], strings.TrimSpace(parts[1])
	}
	return "home", strings.TrimSpace(rest)
}

func cleanTopics(topics []string) []string {
	cleaned := make([]string, 0, len(topics))
	seen := map[string]struct{}{}
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		if _, ok := seen[topic]; ok {
			continue
		}
		seen[topic] = struct{}{}
		cleaned = append(cleaned, topic)
	}
	return cleaned
}

func (s *Server) broadcastAppEvent(ctx context.Context, topic string, event string, payload any) {
	s.broadcastAppEventOnKey(ctx, topic, topic, event, payload)
}

func (s *Server) broadcastAppEventOnKey(ctx context.Context, subscriptionKey string, publicTopic string, event string, payload any) {
	apps := s.router.AppsForTopic(subscriptionKey)
	if strings.HasPrefix(publicTopic, "notes.") {
		s.logger.Info("broadcasting notes realtime event", "topic", publicTopic, "event", event, "subscriber_count", len(apps))
	}
	for _, app := range apps {
		_ = writeAppEvent(ctx, app.peer, event, publicTopic, payload)
	}
}

func writeAppResponse(ctx context.Context, peer *wsPeer, envelope protocol.Envelope, payload any) error {
	body, err := protocol.EncodeBody(payload)
	if err != nil {
		return err
	}
	return peer.Write(ctx, protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeAppResponse,
		RequestID: envelope.RequestID,
		AgentID:   envelope.AgentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   body,
	})
}

func (s *Server) emitHomeStatus(ctx context.Context, homeID string, payload any) {
	s.broadcastAppEventOnKey(ctx, scopedHomeTopic(homeID, topicHomeStatus), topicHomeStatus, "home.status_changed", payload)
}

func (s *Server) emitSyncStatus(ctx context.Context, homeID string) {
	s.broadcastAppEventOnKey(ctx, scopedHomeTopic(homeID, topicHomeStatus), topicHomeStatus, "sync.status_changed", map[string]any{"home_id": homeID})
}

func (s *Server) emitSettingsChanged(ctx context.Context, event string, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicHomeSettings, event, payload)
}

func (s *Server) emitMembersChanged(ctx context.Context, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicHomeMembers, "members.changed", payload)
}

func (s *Server) emitPermissionsChanged(ctx context.Context, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicHomePermissions, "permissions.changed", payload)
}

func (s *Server) emitHomeNotesChanged(ctx context.Context, event string, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicNotesHome, event, payload)
}

func (s *Server) emitProfileNotesChanged(ctx context.Context, payload any) {
	userID := userIDFromPayload(payload)
	if userID == "" {
		s.broadcastAppEvent(ctx, topicNotesProfile, "notes.changed", payload)
		return
	}
	s.broadcastAppEventOnKey(ctx, scopedUserTopic(userID, topicNotesProfile), topicNotesProfile, "notes.changed", payload)
}

func (s *Server) emitFileDirectoryChanged(ctx context.Context, path string, payload any) {
	topic := fileDirectoryTopic(path)
	homeID := homeIDFromPayload(payload)
	if homeID == "" {
		s.broadcastAppEvent(ctx, topic, "files.directory_changed", payload)
		return
	}
	s.broadcastAppEventOnKey(ctx, scopedHomeTopic(homeID, topic), topic, "files.directory_changed", payload)
}

func (s *Server) emitHomeAssistantStateChanged(ctx context.Context, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicHomeAssistantStates, "homeassistant.state_changed", payload)
}

func (s *Server) emitStorageEvent(ctx context.Context, event string, payload any) {
	s.broadcastHomeScopedEvent(ctx, topicStorage, event, payload)
}

func (s *Server) broadcastHomeScopedEvent(ctx context.Context, topic string, event string, payload any) {
	homeID := homeIDFromPayload(payload)
	if homeID == "" {
		s.broadcastAppEvent(ctx, topic, event, payload)
		return
	}
	s.broadcastAppEventOnKey(ctx, scopedHomeTopic(homeID, topic), topic, event, payload)
}

func homeIDFromPayload(payload any) string {
	if values, ok := payload.(map[string]any); ok {
		if value, ok := values["home_id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func userIDFromPayload(payload any) string {
	if values, ok := payload.(map[string]any); ok {
		if value, ok := values["user_id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) emitCommandSideEffect(ctx context.Context, command string, payload json.RawMessage) {
	switch command {
	case "homeassistant.fetch_state":
		s.broadcastRawAppEvent(ctx, topicHomeAssistantStates, "homeassistant.state_changed", payload)
	case "files.create_directory", "files.upload", "files.rename", "files.move", "files.delete":
		s.emitFileDirectoryChanged(ctx, "/", map[string]any{"path": "/"})
	}
}

func (s *Server) handleAgentEvent(ctx context.Context, homeID string, envelope protocol.Envelope) {
	event, err := protocol.DecodePayload[protocol.AgentEvent](envelope)
	if err != nil {
		s.logger.Warn("bad agent event payload", "home_id", homeID, "error", err)
		return
	}
	if strings.HasPrefix(event.Event, "desktop.") {
		if envelope.HomeID != "" && envelope.HomeID != homeID {
			s.logger.Warn("desktop agent event rejected for wrong home", "home_id", homeID, "agent_id", envelope.AgentID)
			return
		}
		if err := s.handleDesktopAgentEvent(ctx, homeID, envelope.AgentID, event.Event, event.Body); err != nil {
			s.logger.Warn("desktop agent event rejected", "home_id", homeID, "agent_id", envelope.AgentID, "event", event.Event, "error", err)
		}
		return
	}
	if event.Event == protocol.EventSystemUpdateState {
		var state protocol.SystemUpdateState
		if err := json.Unmarshal(event.Body, &state); err != nil || state.Validate(envelope.AgentID) != nil {
			s.logger.Warn("invalid Linux update event", "home_id", homeID, "agent_id", envelope.AgentID, "error", err)
			return
		}
		if err := s.linuxUpdates.RecordState(ctx, homeID, envelope.AgentID, state); err != nil {
			s.logger.Warn("Linux update event rejected", "home_id", homeID, "agent_id", envelope.AgentID, "assignment_id", state.AssignmentID, "error", err)
			return
		}
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, protocol.TopicAgentUpdates), protocol.TopicAgentUpdates, event.Event, event.Body)
		return
	}

	switch event.Event {
	case "shell.session.output", "shell.session.exited":
		topic := event.Topic
		if !strings.HasPrefix(topic, shellSessionTopicPrefix) {
			s.logger.Warn("bad shell session event topic", "home_id", homeID, "topic", topic)
			return
		}
		if !s.shellSessions.allowsAgentEvent(topic, homeID, envelope.AgentID) {
			s.logger.Warn("shell session event rejected for wrong agent", "home_id", homeID, "agent_id", envelope.AgentID, "topic", topic)
			return
		}
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, topic), topic, event.Event, event.Body)
	case "homeassistant.state_changed":
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, topicHomeAssistantStates), topicHomeAssistantStates, event.Event, event.Body)
		s.notifyDashboardEntityChanged(ctx, homeID, event.Body)
	case "files.directory_changed":
		topic := event.Topic
		if !strings.HasPrefix(topic, "files.directory:") {
			topic = fileDirectoryTopic("/")
		}
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, topic), topic, event.Event, event.Body)
	case "files.move_progress", "files.move_completed", "files.move_failed":
		s.handleFileMoveJobEvent(ctx, homeID, envelope.AgentID, event.Event, event.Body)
	case "sync.status_changed":
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, topicHomeStatus), topicHomeStatus, event.Event, event.Body)
	case "media.download_progress", "media.download_completed":
		topic := event.Topic
		if !strings.HasPrefix(topic, "media.downloads") {
			topic = topicMediaDownloads
		}
		level := "info"
		if strings.Contains(string(event.Body), `"failed_count":`) {
			details := traceEventDetailsFromJSON(event.Body)
			if failed, _ := strconv.Atoi(details["failed_count"]); failed > 0 {
				level = "error"
			}
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   level,
			Scope:   "media",
			Event:   event.Event,
			Summary: "Primary Hank Agent emitted a media download event.",
			HomeID:  homeID,
			Details: traceEventDetailsFromJSON(event.Body),
		})
		s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, topic), topic, event.Event, event.Body)
	default:
		s.logger.Debug("ignored agent event", "home_id", homeID, "event", event.Event)
	}
}

func (s *Server) broadcastRawAppEvent(ctx context.Context, topic string, event string, body json.RawMessage) {
	s.broadcastRawAppEventOnKey(ctx, topic, topic, event, body)
}

func (s *Server) broadcastRawAppEventOnKey(ctx context.Context, subscriptionKey string, publicTopic string, event string, body json.RawMessage) {
	for _, app := range s.router.AppsForTopic(subscriptionKey) {
		_ = app.peer.Write(ctx, protocol.Envelope{
			Version:   protocol.Version,
			Type:      protocol.TypeAppEvent,
			Timestamp: time.Now().UTC(),
			Payload:   mustEventBody(event, publicTopic, body),
		})
	}
}
