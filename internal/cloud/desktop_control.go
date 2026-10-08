package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

var desktopEventTransitions = map[string]struct {
	From []string
	To   string
}{
	protocol.EventDesktopSessionReady:        {[]string{"offered"}, "agent_ready"},
	protocol.EventDesktopSessionConnected:    {[]string{"agent_ready", "joining", "reconnecting"}, "active"},
	protocol.EventDesktopSessionDisconnected: {[]string{"active"}, "reconnecting"},
	protocol.EventDesktopSessionError:        {[]string{"requested", "offered", "agent_ready", "joining", "active", "reconnecting"}, "failed"},
	protocol.EventDesktopSessionTerminated:   {[]string{"requested", "offered", "agent_ready", "joining", "active", "reconnecting"}, "terminated"},
}

var desktopAgentMetadataKeys = map[string]bool{
	"status": true, "reason": true, "code": true, "display_id": true, "display_count": true,
	"width": true, "height": true, "scale": true, "fps": true, "codec": true,
	"bitrate_kbps": true, "capture_backend": true, "permission": true, "control_available": true,
	"secure_desktop": true, "browser_connected": true, "agent_connected": true, "indicator": true,
	"data_plane": true, "service": true, "daemon": true, "host": true, "capture": true, "control": true, "identity": true, "certificate": true,
	"platform": true, "state": true, "session": true, "epoch": true, "duration_ms": true,
	"enabled": true, "direction": true, "success": true, "name": true,
}

func desktopSessionTopic(sessionID string) string {
	return "desktop.session:" + strings.TrimSpace(sessionID)
}

func (s *Server) handleDesktopAgentEvent(ctx context.Context, homeID, agentID, eventName string, body json.RawMessage) error {
	if !protocol.IsDesktopEvent(eventName) {
		return errors.New("unsupported desktop agent event")
	}
	var sessionID string
	var epoch uint32
	var reason string
	var metadata map[string]string
	if eventName == protocol.EventDesktopSessionReady {
		var ready protocol.DesktopSessionReady
		if err := json.Unmarshal(body, &ready); err != nil || ready.Validate() != nil {
			return errors.New("invalid desktop ready event")
		}
		sessionID, epoch, metadata = ready.SessionID, ready.KeyEpoch, ready.Readiness
		session, err := s.store.GetDesktopSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateDesktopAgentScope(session, homeID, agentID, sessionID, epoch, time.Now().UTC()); err != nil {
			return err
		}
		endpoint, err := s.store.GetActiveDesktopEndpointIdentity(ctx, homeID, agentID, time.Now().UTC())
		if err != nil || ready.EndpointCertificateFingerprint != endpoint.Fingerprint || ready.EndpointCertificate != encodeDesktopCertificate(endpoint.Certificate) {
			return errors.New("desktop ready endpoint certificate mismatch")
		}
	} else {
		var lifecycle protocol.DesktopSessionLifecycleEvent
		if err := json.Unmarshal(body, &lifecycle); err != nil || lifecycle.Validate() != nil {
			return errors.New("invalid desktop lifecycle event")
		}
		sessionID, epoch, reason, metadata = lifecycle.SessionID, lifecycle.KeyEpoch, strings.TrimSpace(lifecycle.ReasonCode), lifecycle.Metadata
		if reason != "" && !validDesktopReasonCode(reason) {
			return errors.New("invalid desktop lifecycle reason code")
		}
	}
	sanitized, err := sanitizeDesktopAgentMetadata(metadata)
	if err != nil {
		return err
	}
	if err := validateDesktopControlAuditMetadata(eventName, sanitized); err != nil {
		return err
	}
	if isDesktopPrivilegedEvent(eventName) && (sanitized["session"] != sessionID || sanitized["epoch"] != strconv.FormatUint(uint64(epoch), 10)) {
		return errors.New("desktop privileged audit scope mismatch")
	}
	session, err := s.store.GetDesktopSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := validateDesktopAgentScope(session, homeID, agentID, sessionID, epoch, time.Now().UTC()); err != nil {
		return err
	}
	metadataJSON, _ := json.Marshal(sanitized)
	event := domain.DesktopSessionEvent{
		SessionID: sessionID, EventType: eventName, ActorType: "agent", ActorID: agentID,
		OccurredAt: time.Now().UTC(), Severity: "info", ReasonCode: reason, MetadataJSON: string(metadataJSON),
	}
	if eventName == protocol.EventDesktopSessionError && s.logger != nil {
		s.logger.Warn("desktop agent session error", "session_id", sessionID, "agent_id", agentID, "key_epoch", epoch, "reason_code", reason, "diagnostic_code", sanitized["code"])
	}
	if isIdempotentLateDesktopTerminalEvent(session.State, eventName) {
		if s.desktopRelay != nil {
			s.desktopRelay.Revoke(sessionID, desktopTerminalRelayReason(eventName, reason))
		}
		// Terminal acknowledgement replays are intentionally discarded. The
		// authoritative terminal transition already persisted one audit event;
		// appending every retry would create an unbounded replay sink.
		return nil
	}
	if transition, ok := desktopEventTransitions[eventName]; ok {
		if transition.To == "failed" && reason == "" {
			reason, event.ReasonCode, event.Severity = "agent_error", "agent_error", "error"
		}
		if transition.To == "terminated" && reason == "" {
			reason, event.ReasonCode = "agent_terminated", "agent_terminated"
		}
		if _, err := s.store.TransitionDesktopSession(ctx, sessionID, transition.From, transition.To, reason, event.OccurredAt, event); err != nil {
			return err
		}
		if transition.To == "failed" || transition.To == "terminated" {
			if s.desktopRelay != nil {
				s.desktopRelay.Revoke(sessionID, desktopTerminalRelayReason(eventName, reason))
			}
		}
	} else if err := s.store.AppendDesktopSessionEvent(ctx, event); err != nil {
		return err
	}
	publicBody, _ := json.Marshal(protocol.DesktopSessionLifecycleEvent{
		Protocol: protocol.DesktopProtocolVersion, SessionID: sessionID, KeyEpoch: epoch, ReasonCode: event.ReasonCode, Metadata: sanitized,
	})
	s.broadcastRawAppEventOnKey(ctx, desktopSessionTopic(sessionID), desktopSessionTopic(sessionID), eventName, publicBody)
	s.recordDesktopReadinessMetrics(eventName, sanitized)
	return nil
}

func desktopTerminalRelayReason(eventName, reason string) string {
	if validDesktopReasonCode(reason) && reason != "" {
		return reason
	}
	if eventName == protocol.EventDesktopSessionError {
		return "agent_error"
	}
	return "agent_ended"
}

func (s *Server) recordDesktopReadinessMetrics(eventName string, metadata map[string]string) {
	if s.metrics == nil {
		return
	}
	platform := metadata["platform"]
	if platform != "windows" && platform != "macos" {
		return
	}
	ready := func(value string) bool {
		return value == "ready" || value == "true" || value == "available" || value == "visible" || value == "granted" || value == "restarted" || value == "restored"
	}
	if eventName == protocol.EventDesktopSessionReady {
		for _, check := range []string{"service", "daemon", "host", "indicator", "capture", "control", "identity", "certificate"} {
			if value, ok := metadata[check]; ok {
				s.metrics.SetDesktopReadiness(platform, check, ready(value))
			}
		}
		if value := metadata["capture_backend"]; value != "" {
			s.metrics.SetDesktopReadiness(platform, "capture", value != "unavailable")
		}
		if value := metadata["control_available"]; value != "" {
			s.metrics.SetDesktopReadiness(platform, "control", value == "true")
		}
		if value := metadata["data_plane"]; value != "" {
			s.metrics.SetDesktopReadiness(platform, "host", ready(value))
		}
		s.metrics.SetDesktopReadiness(platform, "identity", true)
		s.metrics.SetDesktopReadiness(platform, "certificate", true)
		return
	}
	switch eventName {
	case protocol.EventDesktopPermissionRequired, protocol.EventDesktopPermissionLost:
		if metadata["permission"] == "screen_recording" {
			s.metrics.SetDesktopReadiness(platform, "capture", false)
		} else {
			s.metrics.SetDesktopReadiness(platform, "control", false)
		}
	case protocol.EventDesktopPermissionGranted:
		if metadata["permission"] == "screen_recording" {
			s.metrics.SetDesktopReadiness(platform, "capture", true)
		} else {
			s.metrics.SetDesktopReadiness(platform, "control", true)
		}
	case protocol.EventDesktopIndicatorLost:
		s.metrics.SetDesktopReadiness(platform, "indicator", false)
	case protocol.EventDesktopIndicatorRestored:
		s.metrics.SetDesktopReadiness(platform, "indicator", true)
	case protocol.EventDesktopHelperFailed:
		s.metrics.SetDesktopReadiness(platform, "host", false)
	case protocol.EventDesktopHelperRestarted:
		s.metrics.SetDesktopReadiness(platform, "host", true)
	case protocol.EventDesktopSecureDesktopUnavailable:
		s.metrics.SetDesktopReadiness(platform, "capture", false)
	}
}

func isIdempotentLateDesktopTerminalEvent(state, eventName string) bool {
	if eventName != protocol.EventDesktopSessionTerminated && eventName != protocol.EventDesktopSessionError {
		return false
	}
	switch protocol.DesktopSessionState(state) {
	case protocol.DesktopSessionDenied, protocol.DesktopSessionFailed, protocol.DesktopSessionExpired, protocol.DesktopSessionTerminated:
		return true
	default:
		return false
	}
}

func validateDesktopControlAuditMetadata(eventName string, metadata map[string]string) error {
	boolean := func(key string) bool { return metadata[key] == "true" || metadata[key] == "false" }
	validReason := func() bool { value := metadata["reason"]; return value == "" || validDesktopReasonCode(value) }
	only := func(keys ...string) bool {
		allowed := make(map[string]bool, len(keys))
		for _, key := range keys {
			allowed[key] = true
		}
		for key := range metadata {
			if !allowed[key] {
				return false
			}
		}
		return true
	}
	switch eventName {
	case protocol.EventDesktopControlChanged:
		if !only("enabled", "success", "reason") || !boolean("enabled") || !boolean("success") || !validReason() {
			return errors.New("invalid desktop control audit metadata")
		}
	case protocol.EventDesktopDisplayChanged:
		if !only("display_id", "success", "reason") || strings.TrimSpace(metadata["display_id"]) == "" || !boolean("success") || !validReason() {
			return errors.New("invalid desktop display audit metadata")
		}
	case protocol.EventDesktopClipboard:
		if !only("direction", "success", "reason") || (metadata["direction"] != "browser_to_agent" && metadata["direction"] != "agent_to_browser") || !boolean("success") || !validReason() {
			return errors.New("invalid desktop clipboard audit metadata")
		}
	case protocol.EventDesktopSpecialKey:
		if !only("name", "success", "reason") || (&protocol.DesktopSpecialKey{Name: metadata["name"]}).Validate() != nil || !boolean("success") || !validReason() {
			return errors.New("invalid desktop special-key audit metadata")
		}
	case protocol.EventDesktopSecureDesktopEntered, protocol.EventDesktopSecureDesktopExited, protocol.EventDesktopSecureDesktopUnavailable:
		expected := map[string]string{protocol.EventDesktopSecureDesktopEntered: "entered", protocol.EventDesktopSecureDesktopExited: "exited", protocol.EventDesktopSecureDesktopUnavailable: "unavailable"}[eventName]
		if !only("platform", "state", "reason", "session", "epoch", "duration_ms") || !validPrivilegedMetadata(metadata, "secure", expected) {
			return errors.New("invalid desktop secure-desktop audit metadata")
		}
	case protocol.EventDesktopPermissionRequired, protocol.EventDesktopPermissionGranted, protocol.EventDesktopPermissionLost:
		expected := map[string]string{protocol.EventDesktopPermissionRequired: "required", protocol.EventDesktopPermissionGranted: "granted", protocol.EventDesktopPermissionLost: "lost"}[eventName]
		if !only("platform", "state", "permission", "reason", "session", "epoch", "duration_ms") || !validPrivilegedMetadata(metadata, "permission", expected) {
			return errors.New("invalid desktop permission audit metadata")
		}
	case protocol.EventDesktopConsoleLocked, protocol.EventDesktopConsoleSwitched:
		expected := map[string]string{protocol.EventDesktopConsoleLocked: "locked", protocol.EventDesktopConsoleSwitched: "switched"}[eventName]
		if !only("platform", "state", "reason", "session", "epoch", "duration_ms") || !validPrivilegedMetadata(metadata, "console", expected) {
			return errors.New("invalid desktop console audit metadata")
		}
	case protocol.EventDesktopHelperRestarted, protocol.EventDesktopHelperFailed:
		expected := map[string]string{protocol.EventDesktopHelperRestarted: "restarted", protocol.EventDesktopHelperFailed: "failed"}[eventName]
		if !only("platform", "state", "reason", "session", "epoch", "duration_ms") || !validPrivilegedMetadata(metadata, "helper", expected) {
			return errors.New("invalid desktop helper audit metadata")
		}
	case protocol.EventDesktopIndicatorLost, protocol.EventDesktopIndicatorRestored:
		expected := map[string]string{protocol.EventDesktopIndicatorLost: "lost", protocol.EventDesktopIndicatorRestored: "restored"}[eventName]
		if !only("platform", "state", "reason", "session", "epoch", "duration_ms") || !validPrivilegedMetadata(metadata, "indicator", expected) {
			return errors.New("invalid desktop indicator audit metadata")
		}
	}
	return nil
}

func validPrivilegedMetadata(metadata map[string]string, family, expectedState string) bool {
	if metadata["platform"] != "windows" && metadata["platform"] != "macos" {
		return false
	}
	if metadata["state"] != expectedState || strings.TrimSpace(metadata["session"]) == "" {
		return false
	}
	if _, err := strconv.ParseUint(metadata["epoch"], 10, 32); err != nil || metadata["epoch"] == "0" {
		return false
	}
	if value := metadata["duration_ms"]; value != "" {
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return false
		}
	}
	if value := metadata["reason"]; value != "" && !validDesktopReasonCode(value) {
		return false
	}
	if family == "permission" {
		return metadata["permission"] == "screen_recording" || metadata["permission"] == "accessibility"
	}
	return metadata["permission"] == ""
}

func isDesktopPrivilegedEvent(eventName string) bool {
	switch eventName {
	case protocol.EventDesktopSecureDesktopEntered, protocol.EventDesktopSecureDesktopExited, protocol.EventDesktopSecureDesktopUnavailable,
		protocol.EventDesktopPermissionRequired, protocol.EventDesktopPermissionGranted, protocol.EventDesktopPermissionLost,
		protocol.EventDesktopConsoleLocked, protocol.EventDesktopConsoleSwitched, protocol.EventDesktopHelperRestarted,
		protocol.EventDesktopHelperFailed, protocol.EventDesktopIndicatorLost, protocol.EventDesktopIndicatorRestored:
		return true
	default:
		return false
	}
}

func validDesktopReasonCode(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func validateDesktopAgentScope(session domain.DesktopSession, homeID, agentID, sessionID string, epoch uint32, now time.Time) error {
	if session.ID != sessionID || session.HomeID != homeID || session.AgentID != agentID || session.KeyEpoch != epoch || !session.HardExpiresAt.After(now) {
		return errors.New("desktop event scope mismatch")
	}
	return nil
}

func sanitizeDesktopAgentMetadata(metadata map[string]string) (map[string]string, error) {
	if len(metadata) > 32 {
		return nil, errors.New("too many desktop metadata values")
	}
	sanitized := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if !desktopAgentMetadataKeys[key] || len(value) > 256 {
			return nil, fmt.Errorf("unsupported desktop metadata key or value: %s", key)
		}
		sanitized[key] = value
	}
	return sanitized, nil
}

func encodeDesktopCertificate(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}
