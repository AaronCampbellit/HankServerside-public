package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

var errDesktopRelayUnauthorized = errors.New("desktop relay unauthorized")

type desktopRelayAuthorizer interface {
	PrepareDesktopRelay(context.Context, desktopRelaySide, string, string, string, time.Time) (desktopRelayJoinClaim, error)
	ConsumeDesktopRelayCredential(context.Context, desktopRelayJoinClaim, string, time.Time) error
}

type desktopRelayCredentialStore interface {
	GetDesktopSession(context.Context, string) (domain.DesktopSession, error)
	ConsumeDesktopJoinCredential(context.Context, []byte, string, string, uint32, time.Time) (domain.DesktopJoinCredential, error)
}

type storeDesktopRelayAuthorizer struct{ store desktopRelayCredentialStore }

func (authorizer storeDesktopRelayAuthorizer) PrepareDesktopRelay(ctx context.Context, side desktopRelaySide, sessionID, credential, agentID string, now time.Time) (desktopRelayJoinClaim, error) {
	if authorizer.store == nil || !validDesktopResourceID(sessionID) || strings.TrimSpace(credential) == "" {
		return desktopRelayJoinClaim{}, errDesktopRelayUnauthorized
	}
	session, err := authorizer.store.GetDesktopSession(ctx, sessionID)
	if err != nil || !session.HardExpiresAt.After(now) || session.KeyEpoch == 0 || !desktopRelayJoinableState(side, session.State) {
		return desktopRelayJoinClaim{}, errDesktopRelayUnauthorized
	}
	if side == desktopRelayAgent && (strings.TrimSpace(agentID) == "" || session.AgentID != agentID) {
		return desktopRelayJoinClaim{}, errDesktopRelayUnauthorized
	}
	claim := desktopRelayJoinClaim{SessionID: session.ID, HomeID: session.HomeID, Side: side, KeyEpoch: session.KeyEpoch, AgentID: session.AgentID, HardExpiresAt: session.HardExpiresAt, Reconnect: session.State == string(protocol.DesktopSessionReconnecting)}
	if claim.Reconnect {
		if session.ReconnectExpiresAt == nil {
			return desktopRelayJoinClaim{}, errDesktopRelayUnauthorized
		}
		claim.ReconnectExpiresAt = *session.ReconnectExpiresAt
	}
	return claim, nil
}

func (authorizer storeDesktopRelayAuthorizer) ConsumeDesktopRelayCredential(ctx context.Context, claim desktopRelayJoinClaim, credential string, now time.Time) error {
	if authorizer.store == nil || claim.Validate(now) != nil || strings.TrimSpace(credential) == "" {
		return errDesktopRelayUnauthorized
	}
	if _, err := authorizer.store.ConsumeDesktopJoinCredential(ctx, desktopCredentialHash(credential), string(claim.Side), claim.SessionID, claim.KeyEpoch, now); err != nil {
		return errDesktopRelayUnauthorized
	}
	return nil
}

func desktopRelayJoinableState(side desktopRelaySide, state string) bool {
	switch state {
	case string(protocol.DesktopSessionOffered), string(protocol.DesktopSessionAgentReady), string(protocol.DesktopSessionJoining), string(protocol.DesktopSessionReconnecting):
		return true
	default:
		return false
	}
}

func (s *Server) handleDesktopBrowserWebSocket(w http.ResponseWriter, r *http.Request) {
	if !desktopSameOrigin(r) {
		desktopRelayUnauthorized(w)
		return
	}
	cookie, err := r.Cookie(desktopJoinCookieName)
	if err != nil {
		desktopRelayUnauthorized(w)
		return
	}
	s.handleDesktopDataWebSocket(w, r, desktopRelayBrowser, strings.TrimPrefix(r.URL.Path, "/ws/desktop/browser/"), cookie.Value, "")
}

func (s *Server) handleDesktopAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	credential, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		s.logger.Warn("desktop agent relay missing bearer credential", "session_id", strings.TrimPrefix(r.URL.Path, "/ws/desktop/agent/"), "agent_id_present", strings.TrimSpace(r.Header.Get("X-Hank-Agent-ID")) != "")
		desktopRelayUnauthorized(w)
		return
	}
	s.handleDesktopDataWebSocket(w, r, desktopRelayAgent, strings.TrimPrefix(r.URL.Path, "/ws/desktop/agent/"), credential, strings.TrimSpace(r.Header.Get("X-Hank-Agent-ID")))
}

func (s *Server) handleDesktopDataWebSocket(w http.ResponseWriter, r *http.Request, side desktopRelaySide, sessionID, credential, agentID string) {
	if r.Method != http.MethodGet || s.desktopRelayAuth == nil || s.desktopRelay == nil {
		desktopRelayUnauthorized(w)
		return
	}
	now := time.Now().UTC()
	claim, err := s.desktopRelayAuth.PrepareDesktopRelay(r.Context(), side, sessionID, credential, agentID, now)
	if err != nil {
		if s.metrics != nil {
			s.metrics.IncDesktopJoin(string(side), "failed")
		}
		s.logger.Warn("desktop relay authorization rejected", "session_id", sessionID, "side", side, "agent_id_present", agentID != "", "credential_present", credential != "")
		desktopRelayUnauthorized(w)
		return
	}
	if err := s.desktopRelay.Reserve(claim); err != nil {
		if s.metrics != nil {
			s.metrics.IncDesktopJoin(string(side), "failed")
		}
		s.logger.Warn("desktop relay admission rejected", "session_id", sessionID, "side", side, "reason", desktopRelayAdmissionReason(err))
		http.Error(w, "desktop relay capacity unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.desktopRelayAuth.ConsumeDesktopRelayCredential(r.Context(), claim, credential, now); err != nil {
		if s.metrics != nil {
			s.metrics.IncDesktopJoin(string(side), "replayed")
		}
		desktopRelayUnauthorized(w)
		return
	}
	if side == desktopRelayBrowser {
		clearDesktopJoinCookie(w)
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.desktopRelay.CancelReservation(claim)
		return
	}
	conn.SetReadLimit(defaultDesktopRelayLimits().MaxFrameBytes)
	endpoint := &desktopWebSocketEndpoint{conn: conn}
	if err := s.desktopRelay.Join(r.Context(), claim, endpoint); err != nil {
		s.desktopRelay.CancelReservation(claim)
		if s.metrics != nil {
			s.metrics.IncDesktopJoin(string(side), "failed")
		}
		_ = endpoint.Close("relay_closed")
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "desktop session closed")
}

func desktopRelayAdmissionReason(err error) string {
	switch {
	case errors.Is(err, errDesktopRelayLimit):
		return "capacity"
	case errors.Is(err, errDesktopRelayDuplicateSide):
		return "duplicate_side"
	case errors.Is(err, errDesktopRelayClaimMismatch):
		return "claim_mismatch"
	case errors.Is(err, errDesktopRelayNotReady):
		return "unavailable"
	default:
		return "rejected"
	}
}

func desktopRelayUnauthorized(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func desktopSameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host {
		return false
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	return parsed.Scheme == scheme
}

type desktopWebSocketEndpoint struct{ conn *websocket.Conn }

func (endpoint *desktopWebSocketEndpoint) Read(ctx context.Context) ([]byte, error) {
	kind, payload, err := endpoint.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, errors.New("desktop relay accepts binary messages only")
	}
	return payload, nil
}

func (endpoint *desktopWebSocketEndpoint) Write(ctx context.Context, payload []byte) error {
	return endpoint.conn.Write(ctx, websocket.MessageBinary, payload)
}
func (endpoint *desktopWebSocketEndpoint) Close(reason string) error {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	return endpoint.conn.Close(websocket.StatusPolicyViolation, reason)
}

func (s *Server) recordDesktopRelayLifecycle(event desktopRelayLifecycleEvent) {
	if s.metrics != nil {
		switch event.Kind {
		case "side_joined":
			s.metrics.IncDesktopJoin(event.Reason, "success")
		case "backpressure":
			direction := "browser_to_agent"
			if event.Reason == string(desktopRelayAgent) {
				direction = "agent_to_browser"
			}
			s.metrics.IncDesktopRelayBackpressure(direction)
		case "closed":
			s.metrics.AddDesktopRelayBytes("browser_to_agent", event.BrowserToAgentBytes)
			s.metrics.AddDesktopRelayBytes("agent_to_browser", event.AgentToBrowserBytes)
			s.metrics.IncDesktopTerminated(event.Reason)
		}
	}
	if event.Kind == "backpressure" {
		direction := "browser_to_agent"
		if event.Reason == string(desktopRelayAgent) {
			direction = "agent_to_browser"
		}
		go s.sendDesktopRelayPressure(event.SessionID, event.KeyEpoch, direction)
	}
	if s.store == nil || event.SessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if event.Kind == "closed" && (event.BrowserToAgentBytes > 0 || event.AgentToBrowserBytes > 0) {
		_ = s.store.AddDesktopRelayBytes(ctx, event.SessionID, event.BrowserToAgentBytes, event.AgentToBrowserBytes)
	}
	now := time.Now().UTC()
	metadata, _ := json.Marshal(map[string]any{
		"key_epoch": event.KeyEpoch, "browser_to_agent_bytes": event.BrowserToAgentBytes, "agent_to_browser_bytes": event.AgentToBrowserBytes,
	})
	audit := domain.DesktopSessionEvent{SessionID: event.SessionID, EventType: "relay." + event.Kind, ActorType: "server", OccurredAt: now, Severity: "info", ReasonCode: event.Reason, MetadataJSON: string(metadata)}
	if event.Kind == "closed" && event.Reason == "transport_closed" {
		session, err := s.store.GetDesktopSession(ctx, event.SessionID)
		if err == nil && session.State == string(protocol.DesktopSessionActive) && session.KeyEpoch == event.KeyEpoch {
			expires := now.Add(desktopReconnectTTL)
			if expires.After(session.HardExpiresAt) {
				expires = session.HardExpiresAt
			}
			audit.EventType, audit.Severity = "relay.transport_lost", "warning"
			_, _ = s.store.MarkDesktopTransportLost(ctx, session.ID, event.KeyEpoch, expires, audit)
			return
		}
	}
	_ = s.store.AppendDesktopSessionEvent(ctx, audit)
}

func (s *Server) sendDesktopRelayPressure(sessionID string, epoch uint32, direction string) {
	if s.store == nil || s.router == nil || !validDesktopResourceID(sessionID) || epoch == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := s.store.GetDesktopSession(ctx, sessionID)
	if err != nil || session.KeyEpoch != epoch || session.State != string(protocol.DesktopSessionActive) {
		return
	}
	agent, ok := s.router.ResolveAgent(session.HomeID, session.AgentID)
	if !ok {
		return
	}
	envelope, err := desktopRelayPressureEnvelope(session, epoch, direction)
	if err == nil {
		_ = agent.peer.Write(ctx, envelope)
	}
}

func desktopRelayPressureEnvelope(session domain.DesktopSession, epoch uint32, direction string) (protocol.Envelope, error) {
	if session.State != string(protocol.DesktopSessionActive) || session.KeyEpoch != epoch ||
		(direction != "browser_to_agent" && direction != "agent_to_browser") {
		return protocol.Envelope{}, errors.New("invalid desktop relay pressure scope")
	}
	body, err := protocol.EncodeBody(map[string]any{"session_id": session.ID, "key_epoch": epoch, "direction": direction, "count": 1})
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.NewEnvelope(protocol.TypeCloudCommand, newID("deskpress"), session.AgentID, session.HomeID,
		protocol.RoutedCommand{Command: protocol.CommandDesktopSessionRelayPressure, Body: body})
}

var _ desktopRelayCredentialStore = (*store.Store)(nil)
