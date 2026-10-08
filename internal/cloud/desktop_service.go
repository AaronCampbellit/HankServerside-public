package cloud

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

var (
	errDesktopAdminRequired     = errors.New("desktop administrator role required")
	errDesktopAgentOffline      = errors.New("desktop agent is offline")
	errDesktopCapabilityMissing = errors.New("desktop capability is unavailable")
	errDesktopIdentityUntrusted = errors.New("desktop identity is not trusted")
	errDesktopSessionConflict   = errors.New("desktop session already exists for agent")
	errDesktopSessionExpired    = errors.New("desktop session expired")
)

const (
	desktopInitialJoinTTL = 60 * time.Second
	desktopReconnectTTL   = 90 * time.Second
	desktopHardTTL        = 8 * time.Hour
)

type desktopStore interface {
	GetActiveDesktopOperatorIdentity(context.Context, string, string, string, time.Time) (domain.DesktopIdentity, error)
	GetActiveDesktopEndpointIdentity(context.Context, string, string, time.Time) (domain.DesktopIdentity, error)
	CreateDesktopSession(context.Context, domain.DesktopSession, domain.DesktopJoinCredential, domain.DesktopJoinCredential, domain.DesktopSessionEvent) error
	GetDesktopSession(context.Context, string) (domain.DesktopSession, error)
	TransitionDesktopSession(context.Context, string, []string, string, string, time.Time, domain.DesktopSessionEvent) (domain.DesktopSession, error)
	BeginDesktopReconnect(context.Context, string, uint32, time.Time, domain.DesktopJoinCredential, domain.DesktopJoinCredential, domain.DesktopSessionEvent) (domain.DesktopSession, error)
}

type desktopAgentPresence struct {
	HomeID       string
	AgentID      string
	Capabilities []string
}

type desktopAgentResolver interface {
	ResolveDesktopAgent(homeID, agentID string) (desktopAgentPresence, bool)
}

type desktopCreateInput struct {
	HomeID              string
	AgentID             string
	OperatorUserID      string
	OperatorDeviceID    string
	OperatorSessionID   string
	SourceIPHash        string
	SourceUserAgentHash string
	Permissions         []protocol.DesktopPermission
}

type desktopCreateResult struct {
	Session           domain.DesktopSession
	BrowserCredential string
	AgentCredential   string
	OperatorIdentity  domain.DesktopIdentity
	EndpointIdentity  domain.DesktopIdentity
}

type desktopReconnectResult struct {
	Session           domain.DesktopSession
	BrowserCredential string
	AgentCredential   string
}

type desktopService struct {
	store     desktopStore
	agents    desktopAgentResolver
	admission desktopRelay
	now       func() time.Time
	token     func() string
}

func newDesktopService(store desktopStore, agents desktopAgentResolver, now func() time.Time, token func() string, admission ...desktopRelay) *desktopService {
	service := &desktopService{store: store, agents: agents, now: now, token: token}
	if len(admission) > 0 {
		service.admission = admission[0]
	}
	return service
}

func (service *desktopService) Create(ctx context.Context, input desktopCreateInput) (desktopCreateResult, error) {
	if strings.TrimSpace(input.HomeID) == "" || strings.TrimSpace(input.AgentID) == "" ||
		strings.TrimSpace(input.OperatorUserID) == "" || strings.TrimSpace(input.OperatorDeviceID) == "" {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}
	if err := protocol.ValidateDesktopPermissions(input.Permissions); err != nil {
		return desktopCreateResult{}, err
	}
	presence, ok := service.agents.ResolveDesktopAgent(input.HomeID, input.AgentID)
	if !ok || presence.HomeID != input.HomeID || presence.AgentID != input.AgentID {
		return desktopCreateResult{}, errDesktopAgentOffline
	}
	requiredCapabilities := []string{"desktop.session.open"}
	for _, permission := range input.Permissions {
		requiredCapabilities = append(requiredCapabilities, string(permission))
	}
	if !hasCapabilities(presence.Capabilities, requiredCapabilities...) {
		return desktopCreateResult{}, errDesktopCapabilityMissing
	}

	now := service.now().UTC()
	operator, err := service.store.GetActiveDesktopOperatorIdentity(ctx, input.HomeID, input.OperatorUserID, input.OperatorDeviceID, now)
	if errors.Is(err, store.ErrNotFound) {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}
	if err != nil {
		return desktopCreateResult{}, err
	}
	if operator.AuthSessionID != "" && operator.AuthSessionID != input.OperatorSessionID {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}
	if operator.HomeID != input.HomeID || operator.UserID != input.OperatorUserID ||
		operator.DeviceID != input.OperatorDeviceID || operator.IdentityType != domain.DesktopIdentityOperatorDevice {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}
	endpoint, err := service.store.GetActiveDesktopEndpointIdentity(ctx, input.HomeID, input.AgentID, now)
	if errors.Is(err, store.ErrNotFound) {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}
	if err != nil {
		return desktopCreateResult{}, err
	}
	if endpoint.HomeID != input.HomeID || endpoint.AgentID != input.AgentID || endpoint.IdentityType != domain.DesktopIdentityEndpoint {
		return desktopCreateResult{}, errDesktopIdentityUntrusted
	}

	sessionID := newID("desk")
	permissionStrings := make([]string, len(input.Permissions))
	for index, permission := range input.Permissions {
		permissionStrings[index] = string(permission)
	}
	session := domain.DesktopSession{
		ID:                       sessionID,
		HomeID:                   input.HomeID,
		AgentID:                  input.AgentID,
		OperatorUserID:           input.OperatorUserID,
		OperatorDeviceIdentityID: operator.ID,
		RequestedPermissions:     append([]string(nil), permissionStrings...),
		EffectivePermissions:     append([]string(nil), permissionStrings...),
		State:                    string(protocol.DesktopSessionRequested),
		KeyEpoch:                 1,
		RequestedAt:              now,
		JoinExpiresAt:            now.Add(desktopInitialJoinTTL),
		HardExpiresAt:            now.Add(desktopHardTTL),
		SourceIPHash:             input.SourceIPHash,
		SourceUserAgentHash:      input.SourceUserAgentHash,
	}
	releaseAdmission, err := service.reserveAdmission(session)
	if err != nil {
		return desktopCreateResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			releaseAdmission()
		}
	}()
	browserRaw, agentRaw, err := service.newCredentialPair()
	if err != nil {
		return desktopCreateResult{}, err
	}
	browser := newDesktopCredential(sessionID, "browser", 1, browserRaw, now, session.JoinExpiresAt)
	agent := newDesktopCredential(sessionID, "agent", 1, agentRaw, now, session.JoinExpiresAt)
	event := domain.DesktopSessionEvent{
		SessionID: sessionID, EventType: "session.requested", ActorType: "user",
		ActorID: input.OperatorUserID, OccurredAt: now, Severity: "info", MetadataJSON: `{}`,
	}
	if err := service.store.CreateDesktopSession(ctx, session, browser, agent, event); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return desktopCreateResult{}, errDesktopSessionConflict
		}
		return desktopCreateResult{}, err
	}
	committed = true
	return desktopCreateResult{
		Session: session, BrowserCredential: browserRaw, AgentCredential: agentRaw,
		OperatorIdentity: operator, EndpointIdentity: endpoint,
	}, nil
}

func (service *desktopService) Reconnect(ctx context.Context, sessionID, userID string) (desktopReconnectResult, error) {
	session, err := service.store.GetDesktopSession(ctx, sessionID)
	if err != nil {
		return desktopReconnectResult{}, err
	}
	if session.OperatorUserID != userID {
		return desktopReconnectResult{}, errDesktopIdentityUntrusted
	}
	now := service.now().UTC()
	if !session.HardExpiresAt.After(now) {
		return desktopReconnectResult{}, errDesktopSessionExpired
	}
	if session.State != string(protocol.DesktopSessionActive) && session.State != string(protocol.DesktopSessionReconnecting) {
		return desktopReconnectResult{}, errDesktopSessionConflict
	}
	browserRaw, agentRaw, err := service.newCredentialPair()
	if err != nil {
		return desktopReconnectResult{}, err
	}
	expiresAt := now.Add(desktopReconnectTTL)
	if expiresAt.After(session.HardExpiresAt) {
		expiresAt = session.HardExpiresAt
	}
	nextEpoch := session.KeyEpoch + 1
	browser := newDesktopCredential(session.ID, "browser", nextEpoch, browserRaw, now, expiresAt)
	agent := newDesktopCredential(session.ID, "agent", nextEpoch, agentRaw, now, expiresAt)
	event := domain.DesktopSessionEvent{
		SessionID: session.ID, EventType: "session.reconnecting", ActorType: "browser",
		ActorID: userID, OccurredAt: now, Severity: "info", MetadataJSON: `{}`,
	}
	updated, err := service.store.BeginDesktopReconnect(ctx, session.ID, session.KeyEpoch, expiresAt, browser, agent, event)
	if errors.Is(err, store.ErrConflict) {
		return desktopReconnectResult{}, errDesktopSessionConflict
	}
	if err != nil {
		return desktopReconnectResult{}, err
	}
	return desktopReconnectResult{Session: updated, BrowserCredential: browserRaw, AgentCredential: agentRaw}, nil
}

func (service *desktopService) Terminate(ctx context.Context, sessionID, userID, reason string) (domain.DesktopSession, error) {
	session, err := service.store.GetDesktopSession(ctx, sessionID)
	if err != nil {
		return domain.DesktopSession{}, err
	}
	if session.OperatorUserID != userID {
		return domain.DesktopSession{}, errDesktopIdentityUntrusted
	}
	if slices.Contains([]string{
		string(protocol.DesktopSessionDenied),
		string(protocol.DesktopSessionFailed),
		string(protocol.DesktopSessionExpired),
		string(protocol.DesktopSessionTerminated),
	}, session.State) {
		return session, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "user_ended"
	}
	now := service.now().UTC()
	event := domain.DesktopSessionEvent{
		SessionID: session.ID, EventType: "session.terminated", ActorType: "user",
		ActorID: userID, OccurredAt: now, Severity: "info", ReasonCode: reason, MetadataJSON: `{}`,
	}
	updated, err := service.store.TransitionDesktopSession(
		ctx,
		session.ID,
		[]string{
			string(protocol.DesktopSessionRequested),
			string(protocol.DesktopSessionOffered),
			string(protocol.DesktopSessionAgentReady),
			string(protocol.DesktopSessionJoining),
			string(protocol.DesktopSessionActive),
			string(protocol.DesktopSessionReconnecting),
		},
		string(protocol.DesktopSessionTerminated),
		reason,
		now,
		event,
	)
	if errors.Is(err, store.ErrConflict) {
		return domain.DesktopSession{}, errDesktopSessionConflict
	}
	if err == nil && service.admission != nil {
		service.admission.Revoke(session.ID, reason)
	}
	return updated, err
}

func (service *desktopService) reserveAdmission(session domain.DesktopSession) (func(), error) {
	if service.admission == nil {
		return func() {}, nil
	}
	base := desktopRelayJoinClaim{SessionID: session.ID, HomeID: session.HomeID, KeyEpoch: session.KeyEpoch, AgentID: session.AgentID, HardExpiresAt: session.HardExpiresAt}
	browser := base
	browser.Side = desktopRelayBrowser
	if err := service.admission.Reserve(browser); err != nil {
		return nil, err
	}
	agent := base
	agent.Side = desktopRelayAgent
	if err := service.admission.Reserve(agent); err != nil {
		service.admission.CancelReservation(browser)
		return nil, err
	}
	return func() {
		service.admission.CancelReservation(agent)
		service.admission.CancelReservation(browser)
	}, nil
}

func (service *desktopService) newCredentialPair() (string, string, error) {
	browser := strings.TrimSpace(service.token())
	agent := strings.TrimSpace(service.token())
	if browser == "" || agent == "" || browser == agent {
		return "", "", errDesktopSessionConflict
	}
	return browser, agent, nil
}

func newDesktopCredential(sessionID, side string, epoch uint32, raw string, createdAt, expiresAt time.Time) domain.DesktopJoinCredential {
	return domain.DesktopJoinCredential{
		ID:             newID("deskcred"),
		SessionID:      sessionID,
		Side:           side,
		CredentialHash: desktopCredentialHash(raw),
		KeyEpoch:       epoch,
		CreatedAt:      createdAt,
		ExpiresAt:      expiresAt,
	}
}

func desktopCredentialHash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return append([]byte(nil), sum[:]...)
}

func (router *Router) ResolveDesktopAgent(homeID, agentID string) (desktopAgentPresence, bool) {
	router.mu.RLock()
	defer router.mu.RUnlock()
	connection := router.agentsByHomeID[homeID][agentID]
	if connection == nil {
		return desktopAgentPresence{}, false
	}
	return desktopAgentPresence{
		HomeID: homeID, AgentID: connection.agent.ID,
		Capabilities: append([]string(nil), connection.capabilities...),
	}, true
}
