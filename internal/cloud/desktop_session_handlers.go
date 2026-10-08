package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	desktopJoinCookieName = "hank_desktop_join"
	desktopJoinCookiePath = "/ws/desktop/browser/"
)

type createDesktopSessionRequest struct {
	OperatorDeviceID string                       `json:"operator_device_id"`
	Permissions      []protocol.DesktopPermission `json:"permissions"`
}

func (s *Server) handleAgentResourceRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitResourcePath(r.URL.Path, "/v1/agents/")
	if len(parts) != 2 || !validDesktopResourceID(parts[0]) || (parts[1] != "desktop-sessions" && parts[1] != "desktop-readiness") {
		http.NotFound(w, r)
		return
	}
	if (parts[1] == "desktop-sessions" && r.Method != http.MethodPost) || (parts[1] == "desktop-readiness" && r.Method != http.MethodGet) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	home, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errDesktopAdminRequired.Error(), http.StatusForbidden)
		return
	}
	if parts[1] == "desktop-readiness" {
		s.handleDesktopAgentReadiness(w, r, home, parts[0])
		return
	}
	var body createDesktopSessionRequest
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.desktop.Create(r.Context(), desktopCreateInput{
		HomeID: home.ID, AgentID: parts[0], OperatorUserID: auth.User.ID, OperatorDeviceID: strings.TrimSpace(body.OperatorDeviceID), OperatorSessionID: auth.Session.ID,
		SourceIPHash: stableAuditTarget(clientIP(r)), SourceUserAgentHash: stableAuditTarget(r.UserAgent()), Permissions: body.Permissions,
	})
	if err != nil {
		writeDesktopSessionError(w, r, err)
		return
	}
	now := time.Now().UTC()
	offered, err := s.store.TransitionDesktopSession(r.Context(), result.Session.ID, []string{string(protocol.DesktopSessionRequested)}, string(protocol.DesktopSessionOffered), "", now, domain.DesktopSessionEvent{
		SessionID: result.Session.ID, EventType: "desktop.session.offered", ActorType: "server", OccurredAt: now, Severity: "info", MetadataJSON: `{}`,
	})
	if err != nil {
		_ = s.sendDesktopClose(r.Context(), result.Session, "offer_transition_failed")
		_, _ = s.desktop.Terminate(r.Context(), result.Session.ID, auth.User.ID, "offer_transition_failed")
		clearDesktopJoinCookie(w)
		http.Error(w, "desktop session unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.sendDesktopOffer(r.Context(), offered, result.AgentCredential, result.OperatorIdentity); err != nil {
		s.failDesktopOffer(r.Context(), offered, "agent_offer_failed")
		clearDesktopJoinCookie(w)
		http.Error(w, "desktop agent offer unavailable", http.StatusServiceUnavailable)
		return
	}
	setDesktopJoinCookie(w, result.BrowserCredential, offered.JoinExpiresAt)
	writeJSON(w, http.StatusCreated, desktopSessionResponse(offered, result.EndpointIdentity, s.desktopAgentReadiness(r.Context(), home, offered.AgentID)))
}

func (s *Server) handleDesktopAgentReadiness(w http.ResponseWriter, r *http.Request, home domain.Home, agentID string) {
	if _, err := s.store.GetAgentByID(r.Context(), agentID); err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.desktopAgentReadiness(r.Context(), home, agentID))
}

func (s *Server) desktopAgentReadiness(ctx context.Context, home domain.Home, agentID string) map[string]any {
	var live *AgentSnapshot
	for _, candidate := range s.router.AgentsForHome(home.ID) {
		if candidate.AgentID == agentID {
			copy := candidate
			live = &copy
			break
		}
	}
	platform := "unknown"
	checks := map[string]string{"service": "unknown", "daemon": "unknown", "host": "unknown", "indicator": "unknown", "capture": "unknown", "control": "unknown", "identity": "not_trusted", "certificate": "not_trusted"}
	if live != nil {
		platform = normalizeDesktopPlatform(live.Metadata["platform"])
		if platform == "unknown" {
			platform = normalizeDesktopPlatform(live.Metadata["os"])
		}
		for _, check := range []string{"service", "daemon", "host", "indicator", "capture", "control"} {
			if value := live.Metadata["desktop_"+check]; value != "" {
				checks[check] = value
			}
		}
	}
	endpoint, endpointErr := s.store.GetActiveDesktopEndpointIdentity(ctx, home.ID, agentID, time.Now().UTC())
	trusted := false
	if endpointErr == nil {
		identities, listErr := s.store.ListDesktopIdentities(ctx, home.ID)
		root, rootErr := s.store.GetDesktopTrustRoot(ctx, home.ID)
		trusted = listErr == nil && rootErr == nil && endpoint.TrustRootGeneration == root.Generation && desktopCertificateChainIsActive(endpoint.Certificate, home.ID, root.Generation, time.Now().UTC(), identities)
	}
	if trusted {
		checks["identity"] = "ready"
		checks["certificate"] = "ready"
	}
	events, _ := s.store.ListDesktopAgentReadinessEvents(ctx, home.ID, agentID, 100)
	var reportedAt *time.Time
	seen := map[string]bool{}
	for _, event := range events {
		if reportedAt == nil {
			at := event.OccurredAt
			reportedAt = &at
		}
		var metadata map[string]string
		_ = json.Unmarshal([]byte(event.MetadataJSON), &metadata)
		if platform == "unknown" {
			platform = normalizeDesktopPlatform(metadata["platform"])
		}
		applyDesktopReadinessEvent(checks, seen, event.EventType, metadata)
	}
	liveSessions, _ := s.store.ListLiveDesktopSessionIDs(ctx, home.ID, "", agentID)
	activeSessionID := ""
	if len(liveSessions) > 0 {
		activeSessionID = liveSessions[0]
	}
	capabilities := []string{}
	online := false
	if live != nil {
		capabilities = nonNilSlice(live.Capabilities)
		online = true
	}
	return map[string]any{"agent_id": agentID, "online": online, "platform": platform, "trusted": trusted, "checks": checks, "capabilities": capabilities, "active_session": len(liveSessions) > 0, "active_session_id": activeSessionID, "reported_at": reportedAt}
}

func normalizeDesktopPlatform(value string) string {
	value = strings.ToLower(value)
	if strings.Contains(value, "mac") || strings.Contains(value, "darwin") {
		return "macos"
	}
	if strings.Contains(value, "win") {
		return "windows"
	}
	return "unknown"
}
func applyDesktopReadinessEvent(checks map[string]string, seen map[string]bool, eventName string, metadata map[string]string) {
	set := func(check, value string) {
		if !seen[check] && value != "" {
			checks[check] = value
			seen[check] = true
		}
	}
	switch eventName {
	case protocol.EventDesktopSessionReady:
		for _, check := range []string{"service", "daemon", "host", "indicator", "capture", "control"} {
			value := metadata[check]
			if check == "capture" && value == "synthetic" {
				value = "ready"
			}
			set(check, value)
		}
		if value := metadata["capture_backend"]; value != "" {
			set("capture", map[bool]string{true: "ready", false: "unavailable"}[value != "unavailable"])
		}
		if value := metadata["data_plane"]; value != "" {
			set("host", value)
		}
	case protocol.EventDesktopPermissionRequired, protocol.EventDesktopPermissionLost:
		if metadata["permission"] == "screen_recording" {
			set("capture", "required")
		} else {
			set("control", "required")
		}
	case protocol.EventDesktopPermissionGranted:
		if metadata["permission"] == "screen_recording" {
			set("capture", "ready")
		} else {
			set("control", "ready")
		}
	case protocol.EventDesktopIndicatorLost:
		set("indicator", "unavailable")
	case protocol.EventDesktopIndicatorRestored:
		set("indicator", "ready")
	case protocol.EventDesktopHelperFailed:
		set("host", "unavailable")
	case protocol.EventDesktopHelperRestarted:
		set("host", "ready")
	case protocol.EventDesktopSecureDesktopUnavailable:
		set("capture", "unavailable")
	}
}

func (s *Server) handleDesktopSessionRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitResourcePath(r.URL.Path, "/v1/desktop-sessions/")
	if len(parts) < 1 || len(parts) > 2 || !validDesktopResourceID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	session, endpoint, ok := s.desktopSessionForCurrentMember(w, r, auth.User.ID, parts[0])
	if !ok {
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		home := domain.Home{ID: session.HomeID}
		writeJSON(w, http.StatusOK, desktopSessionResponse(session, endpoint, s.desktopAgentReadiness(r.Context(), home, session.AgentID)))
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after_sequence"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := s.store.ListDesktopSessionEvents(r.Context(), session.ID, after, limit)
		if err != nil {
			http.Error(w, "desktop session events unavailable", http.StatusInternalServerError)
			return
		}
		items := make([]map[string]any, 0, len(events))
		for _, event := range events {
			items = append(items, desktopSessionEventResponse(event))
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": items, "next_after_sequence": func() int64 {
			if len(events) == 0 {
				return after
			}
			return events[len(events)-1].Sequence
		}()})
		return
	}
	if len(parts) == 2 && parts[1] == "reconnect" && r.Method == http.MethodPost {
		result, err := s.desktop.Reconnect(r.Context(), session.ID, auth.User.ID)
		if err != nil {
			if s.metrics != nil {
				outcome := "failed"
				if errors.Is(err, errDesktopSessionExpired) {
					outcome = "expired"
				}
				s.metrics.IncDesktopReconnect(outcome)
			}
			writeDesktopSessionError(w, r, err)
			return
		}
		operator, ok := s.desktopIdentityByID(r.Context(), session.HomeID, session.OperatorDeviceIdentityID)
		if !ok || s.sendDesktopOffer(r.Context(), result.Session, result.AgentCredential, operator) != nil {
			if s.metrics != nil {
				s.metrics.IncDesktopReconnect("failed")
			}
			s.failDesktopOffer(r.Context(), result.Session, "agent_offer_failed")
			clearDesktopJoinCookie(w)
			http.Error(w, "desktop reconnect unavailable", http.StatusServiceUnavailable)
			return
		}
		expiresAt := result.Session.HardExpiresAt
		if result.Session.ReconnectExpiresAt != nil {
			expiresAt = *result.Session.ReconnectExpiresAt
		}
		setDesktopJoinCookie(w, result.BrowserCredential, expiresAt)
		if s.metrics != nil {
			s.metrics.IncDesktopReconnect("success")
		}
		writeJSON(w, http.StatusOK, desktopSessionResponse(result.Session, endpoint, s.desktopAgentReadiness(r.Context(), domain.Home{ID: session.HomeID}, session.AgentID)))
		return
	}
	if len(parts) == 2 && parts[1] == "terminate" && r.Method == http.MethodPost {
		updated, err := s.desktop.Terminate(r.Context(), session.ID, auth.User.ID, "user_ended")
		if err != nil {
			writeDesktopSessionError(w, r, err)
			return
		}
		_ = s.sendDesktopClose(r.Context(), updated, "user_ended")
		s.desktopRelay.Revoke(session.ID, "user_ended")
		clearDesktopJoinCookie(w)
		writeJSON(w, http.StatusOK, desktopSessionResponse(updated, endpoint, s.desktopAgentReadiness(r.Context(), domain.Home{ID: session.HomeID}, session.AgentID)))
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleHomeDesktopSessions(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "desktop-sessions" {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errDesktopAdminRequired.Error(), http.StatusForbidden)
		return true
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListTerminalDesktopSessions(r.Context(), home.ID, after, limit)
	if err != nil {
		http.Error(w, "desktop session history unavailable", http.StatusInternalServerError)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items, "next_after": after + len(items)})
	return true
}

func (s *Server) desktopSessionForCurrentMember(w http.ResponseWriter, r *http.Request, userID, sessionID string) (domain.DesktopSession, domain.DesktopIdentity, bool) {
	session, err := s.store.GetDesktopSessionForUser(r.Context(), sessionID, userID)
	if err != nil {
		http.NotFound(w, r)
		return domain.DesktopSession{}, domain.DesktopIdentity{}, false
	}
	if _, err := s.store.GetHomeMembership(r.Context(), session.HomeID, userID); err != nil {
		http.NotFound(w, r)
		return domain.DesktopSession{}, domain.DesktopIdentity{}, false
	}
	endpoint, err := s.store.GetActiveDesktopEndpointIdentity(r.Context(), session.HomeID, session.AgentID, time.Now().UTC())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "desktop session unavailable", http.StatusInternalServerError)
		return domain.DesktopSession{}, domain.DesktopIdentity{}, false
	}
	if endpoint.ID != "" {
		identities, listErr := s.store.ListDesktopIdentities(r.Context(), session.HomeID)
		if listErr != nil || !desktopCertificateChainIsActive(endpoint.Certificate, session.HomeID, endpoint.TrustRootGeneration, time.Now().UTC(), identities) {
			return session, domain.DesktopIdentity{}, true
		}
	}
	return session, endpoint, true
}

func (s *Server) sendDesktopOffer(ctx context.Context, session domain.DesktopSession, agentCredential string, operator domain.DesktopIdentity) error {
	agent, ok := s.router.ResolveAgent(session.HomeID, session.AgentID)
	if !ok {
		return errDesktopAgentOffline
	}
	root, err := s.store.GetDesktopTrustRoot(ctx, session.HomeID)
	identities, identitiesErr := s.store.ListDesktopIdentities(ctx, session.HomeID)
	endpoint, endpointErr := s.store.GetActiveDesktopEndpointIdentity(ctx, session.HomeID, session.AgentID, time.Now().UTC())
	if err != nil || identitiesErr != nil || root.Generation != operator.TrustRootGeneration || operator.RevokedAt != nil || !operator.ExpiresAt.After(time.Now().UTC()) ||
		!desktopCertificateChainIsActive(operator.Certificate, session.HomeID, root.Generation, time.Now().UTC(), identities) || endpointErr != nil ||
		endpoint.TrustRootGeneration != root.Generation || !desktopCertificateChainIsActive(endpoint.Certificate, session.HomeID, root.Generation, time.Now().UTC(), identities) {
		return errors.New("desktop session trust is not active")
	}
	joinExpiresAt := session.JoinExpiresAt
	if session.State == string(protocol.DesktopSessionReconnecting) && session.ReconnectExpiresAt != nil {
		joinExpiresAt = *session.ReconnectExpiresAt
	}
	permissions := make([]protocol.DesktopPermission, len(session.EffectivePermissions))
	for index, permission := range session.EffectivePermissions {
		permissions[index] = protocol.DesktopPermission(permission)
	}
	checkedAt := time.Now().UTC()
	offer := protocol.DesktopSessionOffer{
		Protocol: protocol.DesktopProtocolVersion, SessionID: session.ID, HomeID: session.HomeID, AgentID: session.AgentID,
		OperatorUserID: session.OperatorUserID, OperatorDeviceID: session.OperatorDeviceIdentityID, Permissions: permissions,
		KeyEpoch: session.KeyEpoch, JoinExpiresAt: joinExpiresAt, HardExpiresAt: session.HardExpiresAt,
		AgentJoinCredential: agentCredential, OperatorCertificate: base64.RawURLEncoding.EncodeToString(operator.Certificate), OperatorCertificateFingerprint: operator.Fingerprint,
		TrustRootGeneration: uint32(root.Generation), TrustRootPublicKeySPKI: base64.RawURLEncoding.EncodeToString(root.PublicKeySPKI), TrustRootFingerprint: root.Fingerprint,
		OperatorIdentityStatus: "active", OperatorIdentityCheckedAt: checkedAt,
	}
	if err := offer.Validate(checkedAt); err != nil {
		return err
	}
	body, err := protocol.EncodeBody(offer)
	if err != nil {
		return err
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeCloudCommand, newID("deskreq"), session.AgentID, session.HomeID, protocol.RoutedCommand{Command: protocol.CommandDesktopSessionOffer, Body: body})
	if err != nil {
		return err
	}
	return agent.peer.Write(ctx, envelope)
}

func desktopCertificateChainIsActive(certificate []byte, homeID string, generation int, now time.Time, identities []domain.DesktopIdentity) bool {
	current := append([]byte(nil), certificate...)
	seen := map[string]bool{}
	for depth := 0; depth <= 8; depth++ {
		if len(current) == 0 || len(current) > desktopTrustMaxBlobBytes {
			return false
		}
		var envelope desktopSignedCertificateEnvelope
		if json.Unmarshal(current, &envelope) != nil {
			return false
		}
		claimsBytes, err := desktopcrypto.DecodeRawBase64URL(envelope.Claims)
		if err != nil {
			return false
		}
		claims, err := desktopcrypto.DecodeIdentityCertificate(claimsBytes)
		if err != nil || claims.HomeID != homeID || int(claims.TrustRootGeneration) != generation || seen[claims.IdentityID] {
			return false
		}
		seen[claims.IdentityID] = true
		active := false
		for _, identity := range identities {
			if identity.ID == claims.IdentityID && identity.HomeID == homeID && identity.TrustRootGeneration == generation && identity.RevokedAt == nil &&
				identity.ExpiresAt.After(now) && bytes.Equal(identity.Certificate, current) && bytes.Equal(identity.PublicKeySPKI, claims.PublicKeySPKI) {
				active = true
				break
			}
		}
		if !active {
			return false
		}
		if envelope.IssuerCertificate == "" {
			return true
		}
		current, err = desktopcrypto.DecodeRawBase64URL(envelope.IssuerCertificate)
		if err != nil {
			return false
		}
	}
	return false
}

func (s *Server) sendDesktopClose(ctx context.Context, session domain.DesktopSession, reason string) error {
	agent, ok := s.router.ResolveAgent(session.HomeID, session.AgentID)
	if !ok {
		return errDesktopAgentOffline
	}
	body, err := protocol.EncodeBody(protocol.DesktopSessionLifecycleEvent{Protocol: protocol.DesktopProtocolVersion, SessionID: session.ID, KeyEpoch: session.KeyEpoch, ReasonCode: reason})
	if err != nil {
		return err
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeCloudCommand, newID("deskreq"), session.AgentID, session.HomeID, protocol.RoutedCommand{Command: protocol.CommandDesktopSessionClose, Body: body})
	if err != nil {
		return err
	}
	return agent.peer.Write(ctx, envelope)
}

func (s *Server) failDesktopOffer(ctx context.Context, session domain.DesktopSession, reason string) {
	now := time.Now().UTC()
	_, _ = s.store.TransitionDesktopSession(ctx, session.ID, []string{string(protocol.DesktopSessionRequested), string(protocol.DesktopSessionOffered), string(protocol.DesktopSessionReconnecting)}, string(protocol.DesktopSessionFailed), reason, now, domain.DesktopSessionEvent{
		SessionID: session.ID, EventType: "desktop.session.failed", ActorType: "server", OccurredAt: now, Severity: "error", ReasonCode: reason, MetadataJSON: `{}`,
	})
	if s.desktopRelay != nil {
		s.desktopRelay.Revoke(session.ID, reason)
	}
}

func (s *Server) desktopIdentityByID(ctx context.Context, homeID, identityID string) (domain.DesktopIdentity, bool) {
	identities, err := s.store.ListDesktopIdentities(ctx, homeID)
	if err != nil {
		return domain.DesktopIdentity{}, false
	}
	for _, identity := range identities {
		if identity.ID == identityID && identity.RevokedAt == nil && identity.ExpiresAt.After(time.Now().UTC()) {
			return identity, true
		}
	}
	return domain.DesktopIdentity{}, false
}

func desktopSessionResponse(session domain.DesktopSession, endpoint domain.DesktopIdentity, agentReadiness ...map[string]any) map[string]any {
	active := session.State == string(protocol.DesktopSessionActive) || session.State == string(protocol.DesktopSessionReconnecting)
	secureDesktopSupported := false
	for _, capability := range endpoint.Capabilities {
		if capability == "desktop.secure_desktop" {
			secureDesktopSupported = true
			break
		}
	}
	readiness := map[string]any{"identity_trusted": endpoint.ID != "" || endpoint.Fingerprint != "", "secure_desktop_supported": secureDesktopSupported}
	if len(agentReadiness) > 0 && agentReadiness[0] != nil {
		readiness = agentReadiness[0]
		readiness["identity_trusted"] = readiness["trusted"]
		readiness["secure_desktop_supported"] = secureDesktopSupported
	}
	return map[string]any{
		"session_id": session.ID, "home_id": session.HomeID, "agent_id": session.AgentID,
		"operator_user_id": session.OperatorUserID, "operator_device_id": session.OperatorDeviceIdentityID, "permissions": nonNilSlice(session.EffectivePermissions),
		"state": session.State, "key_epoch": session.KeyEpoch, "join_expires_at": session.JoinExpiresAt,
		"reconnect_expires_at": session.ReconnectExpiresAt, "hard_expires_at": session.HardExpiresAt,
		"termination_reason":               session.TerminationReason,
		"endpoint_certificate":             base64.RawURLEncoding.EncodeToString(endpoint.Certificate),
		"endpoint_certificate_fingerprint": endpoint.Fingerprint,
		"websocket_path":                   desktopJoinCookiePath + session.ID,
		"readiness":                        readiness,
		"active_session":                   map[string]any{"active": active, "state": session.State, "key_epoch": session.KeyEpoch, "hard_expires_at": session.HardExpiresAt, "reconnect_expires_at": session.ReconnectExpiresAt},
	}
}

func desktopSessionEventResponse(event domain.DesktopSessionEvent) map[string]any {
	metadata := map[string]string{}
	_ = json.Unmarshal([]byte(event.MetadataJSON), &metadata)
	safe := map[string]string{}
	for _, key := range []string{"epoch", "duration_ms", "platform", "state", "permission", "direction", "success", "display_count", "width", "height", "fps", "codec", "bitrate_kbps", "indicator", "data_plane"} {
		if value := metadata[key]; value != "" {
			safe[key] = value
		}
	}
	return map[string]any{"session_id": event.SessionID, "sequence": event.Sequence, "event_type": event.EventType, "actor_type": event.ActorType, "occurred_at": event.OccurredAt, "severity": event.Severity, "reason_code": event.ReasonCode, "metadata": safe}
}

func setDesktopJoinCookie(w http.ResponseWriter, credential string, expiresAt time.Time) {
	seconds := int(time.Until(expiresAt).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if seconds > int(desktopReconnectTTL/time.Second) {
		seconds = int(desktopReconnectTTL / time.Second)
	}
	http.SetCookie(w, &http.Cookie{Name: desktopJoinCookieName, Value: credential, Path: desktopJoinCookiePath, Expires: expiresAt, MaxAge: seconds, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearDesktopJoinCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: desktopJoinCookieName, Value: "", Path: desktopJoinCookiePath, Expires: time.Unix(1, 0), MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func splitResourcePath(path, prefix string) []string {
	if !strings.HasPrefix(path, prefix) {
		return nil
	}
	trimmed := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func validDesktopResourceID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func writeDesktopSessionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, errDesktopIdentityUntrusted):
		http.NotFound(w, r)
	case errors.Is(err, errDesktopAdminRequired):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, errDesktopAgentOffline), errors.Is(err, errDesktopCapabilityMissing), errors.Is(err, errDesktopRelayLimit):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, errDesktopSessionConflict), errors.Is(err, errDesktopSessionExpired), errors.Is(err, store.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "desktop session operation failed", http.StatusBadRequest)
	}
}
