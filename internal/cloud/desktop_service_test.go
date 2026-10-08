package cloud

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestDesktopServiceCreateUsesSixtySecondJoinAndEightHourHardLimit(t *testing.T) {
	now := time.Now().UTC()
	desktopStore := &fakeDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now)}
	tokens := []string{strings.Repeat("a", 43), strings.Repeat("b", 43)}
	service := newDesktopService(
		desktopStore,
		fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()},
		func() time.Time { return now },
		func() string { value := tokens[0]; tokens = tokens[1:]; return value },
	)
	result, err := service.Create(context.Background(), desktopCreateInput{
		HomeID: "home_1", AgentID: "agent_1", OperatorUserID: "usr_1", OperatorDeviceID: "device_1",
		Permissions: []protocol.DesktopPermission{protocol.DesktopPermissionView},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := result.Session.JoinExpiresAt.Sub(now); got != 60*time.Second {
		t.Fatalf("join TTL = %v", got)
	}
	if got := result.Session.HardExpiresAt.Sub(now); got != 8*time.Hour {
		t.Fatalf("hard TTL = %v", got)
	}
	if result.BrowserCredential == result.AgentCredential {
		t.Fatal("credential sides reused a secret")
	}
	if string(desktopStore.createdBrowser.CredentialHash) == result.BrowserCredential || string(desktopStore.createdAgent.CredentialHash) == result.AgentCredential {
		t.Fatal("plaintext credential reached persistence")
	}
}

func TestDesktopServiceRejectsAgentAndIdentityPolicyFailures(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		store  *fakeDesktopStore
		agents fakeDesktopAgents
		want   error
	}{
		{name: "offline", store: &fakeDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now)}, agents: fakeDesktopAgents{}, want: errDesktopAgentOffline},
		{name: "missing capability", store: &fakeDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now)}, agents: fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: []string{"desktop.session.open"}}, want: errDesktopCapabilityMissing},
		{name: "operator", store: &fakeDesktopStore{operatorErr: store.ErrNotFound, endpoint: activeDesktopEndpoint(now)}, agents: fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()}, want: errDesktopIdentityUntrusted},
		{name: "endpoint", store: &fakeDesktopStore{operator: activeDesktopOperator(now), endpointErr: store.ErrNotFound}, agents: fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()}, want: errDesktopIdentityUntrusted},
		{name: "existing", store: &fakeDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now), createErr: store.ErrConflict}, agents: fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()}, want: errDesktopSessionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newDesktopService(tc.store, tc.agents, func() time.Time { return now }, sequenceDesktopToken())
			_, err := service.Create(context.Background(), desktopCreateInput{
				HomeID: "home_1", AgentID: "agent_1", OperatorUserID: "usr_1", OperatorDeviceID: "device_1",
				Permissions: []protocol.DesktopPermission{protocol.DesktopPermissionView},
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Create error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDesktopServiceReconnectClampsHardExpiryAndChecksOwner(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	session := domain.DesktopSession{
		ID: "desk_reconnect", HomeID: "home_1", AgentID: "agent_1", OperatorUserID: "usr_1",
		State: string(protocol.DesktopSessionActive), KeyEpoch: 1, RequestedAt: now.Add(-8 * time.Hour),
		HardExpiresAt: now.Add(30 * time.Second),
	}
	desktopStore := &fakeDesktopStore{session: session}
	service := newDesktopService(desktopStore, fakeDesktopAgents{}, func() time.Time { return now }, sequenceDesktopToken())
	if _, err := service.Reconnect(context.Background(), session.ID, "usr_wrong"); !errors.Is(err, errDesktopIdentityUntrusted) {
		t.Fatalf("wrong-user reconnect = %v", err)
	}
	result, err := service.Reconnect(context.Background(), session.ID, "usr_1")
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if !result.Session.ReconnectExpiresAt.Equal(session.HardExpiresAt) {
		t.Fatalf("reconnect expiry = %v, want %v", *result.Session.ReconnectExpiresAt, session.HardExpiresAt)
	}
	if !desktopStore.reconnectExpiry.Equal(session.HardExpiresAt) {
		t.Fatalf("store reconnect expiry = %v", desktopStore.reconnectExpiry)
	}
}

func TestDesktopServiceRejectsCredentialGeneratorCollisionWithoutLeakingToken(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	raw := strings.Repeat("secret-value-", 4)
	service := newDesktopService(
		&fakeDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now)},
		fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()},
		func() time.Time { return now }, func() string { return raw },
	)
	_, err := service.Create(context.Background(), desktopCreateInput{
		HomeID: "home_1", AgentID: "agent_1", OperatorUserID: "usr_1", OperatorDeviceID: "device_1",
		Permissions: []protocol.DesktopPermission{protocol.DesktopPermissionView},
	})
	if !errors.Is(err, errDesktopSessionConflict) {
		t.Fatalf("collision = %v", err)
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatal("credential collision error exposed plaintext token")
	}
}

func TestDesktopServiceRejectsMismatchedIdentityResults(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	operator := activeDesktopOperator(now)
	operator.HomeID = "home_other"
	service := newDesktopService(
		&fakeDesktopStore{operator: operator, endpoint: activeDesktopEndpoint(now)},
		fakeDesktopAgents{online: true, homeID: "home_1", agentID: "agent_1", capabilities: desktopTestCapabilities()},
		func() time.Time { return now }, sequenceDesktopToken(),
	)
	_, err := service.Create(context.Background(), desktopCreateInput{
		HomeID: "home_1", AgentID: "agent_1", OperatorUserID: "usr_1", OperatorDeviceID: "device_1",
		Permissions: []protocol.DesktopPermission{protocol.DesktopPermissionView},
	})
	if !errors.Is(err, errDesktopIdentityUntrusted) {
		t.Fatalf("mismatched operator identity = %v, want errDesktopIdentityUntrusted", err)
	}
}

func TestDesktopServicePreAdmissionIsAtomicBeforeCredentialsAndPersistence(t *testing.T) {
	now := time.Now().UTC()
	limits := defaultDesktopRelayLimits()
	limits.MaxSessions, limits.MaxSessionsPerHome, limits.MaxSessionsPerAgent = 1, 1, 1
	relay := newInProcessDesktopRelay(limits, nil)
	desktopStore := &concurrentDesktopStore{operator: activeDesktopOperator(now), endpoint: activeDesktopEndpoint(now)}
	desktopStore.operator.HomeID = "home_0001"
	desktopStore.endpoint.HomeID = "home_0001"
	desktopStore.endpoint.AgentID = "agent_0001"
	var issued atomic.Int64
	token := func() string { return strings.Repeat(string(rune('a'+issued.Add(1))), 43) }
	service := newDesktopService(desktopStore,
		fakeDesktopAgents{online: true, homeID: "home_0001", agentID: "agent_0001", capabilities: desktopTestCapabilities()},
		func() time.Time { return now }, token, relay)

	const contenders = 24
	results := make(chan desktopCreateResult, contenders)
	errorsSeen := make(chan error, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Create(context.Background(), desktopCreateInput{HomeID: "home_0001", AgentID: "agent_0001", OperatorUserID: "usr_1", OperatorDeviceID: "device_1", Permissions: []protocol.DesktopPermission{protocol.DesktopPermissionView}})
			if err != nil {
				errorsSeen <- err
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	close(errorsSeen)
	var successes int
	var admitted desktopCreateResult
	for result := range results {
		successes++
		admitted = result
	}
	var capacityErrors []error
	for err := range errorsSeen {
		capacityErrors = append(capacityErrors, err)
	}
	if successes != 1 || desktopStore.created.Load() != 1 {
		t.Fatalf("successes/stored offers = %d/%d, want 1/1; errors = %v", successes, desktopStore.created.Load(), capacityErrors)
	}
	if issued.Load() != 2 {
		t.Fatalf("credentials issued = %d, want exactly two for admitted session", issued.Load())
	}
	for _, err := range capacityErrors {
		if !errors.Is(err, errDesktopRelayLimit) {
			t.Fatalf("capacity race error = %v", err)
		}
	}
	if admitted.Session.ID == "" || relay.Snapshot(admitted.Session.ID).SessionID != admitted.Session.ID {
		t.Fatal("winning persisted offer did not retain its atomic relay admission")
	}
	relay.Revoke(admitted.Session.ID, "test_complete")
}

type concurrentDesktopStore struct {
	operator domain.DesktopIdentity
	endpoint domain.DesktopIdentity
	created  atomic.Int64
}

func (value *concurrentDesktopStore) GetActiveDesktopOperatorIdentity(context.Context, string, string, string, time.Time) (domain.DesktopIdentity, error) {
	return value.operator, nil
}
func (value *concurrentDesktopStore) GetActiveDesktopEndpointIdentity(context.Context, string, string, time.Time) (domain.DesktopIdentity, error) {
	return value.endpoint, nil
}
func (value *concurrentDesktopStore) CreateDesktopSession(context.Context, domain.DesktopSession, domain.DesktopJoinCredential, domain.DesktopJoinCredential, domain.DesktopSessionEvent) error {
	value.created.Add(1)
	return nil
}
func (*concurrentDesktopStore) GetDesktopSession(context.Context, string) (domain.DesktopSession, error) {
	return domain.DesktopSession{}, store.ErrNotFound
}
func (*concurrentDesktopStore) TransitionDesktopSession(context.Context, string, []string, string, string, time.Time, domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	return domain.DesktopSession{}, store.ErrNotFound
}
func (*concurrentDesktopStore) BeginDesktopReconnect(context.Context, string, uint32, time.Time, domain.DesktopJoinCredential, domain.DesktopJoinCredential, domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	return domain.DesktopSession{}, store.ErrNotFound
}

type fakeDesktopStore struct {
	operator        domain.DesktopIdentity
	operatorErr     error
	endpoint        domain.DesktopIdentity
	endpointErr     error
	createErr       error
	session         domain.DesktopSession
	createdBrowser  domain.DesktopJoinCredential
	createdAgent    domain.DesktopJoinCredential
	reconnectExpiry time.Time
}

func (fake *fakeDesktopStore) GetActiveDesktopOperatorIdentity(context.Context, string, string, string, time.Time) (domain.DesktopIdentity, error) {
	return fake.operator, fake.operatorErr
}

func (fake *fakeDesktopStore) GetActiveDesktopEndpointIdentity(context.Context, string, string, time.Time) (domain.DesktopIdentity, error) {
	return fake.endpoint, fake.endpointErr
}

func (fake *fakeDesktopStore) CreateDesktopSession(_ context.Context, session domain.DesktopSession, browser, agent domain.DesktopJoinCredential, _ domain.DesktopSessionEvent) error {
	fake.session, fake.createdBrowser, fake.createdAgent = session, browser, agent
	return fake.createErr
}

func (fake *fakeDesktopStore) GetDesktopSession(context.Context, string) (domain.DesktopSession, error) {
	if fake.session.ID == "" {
		return domain.DesktopSession{}, store.ErrNotFound
	}
	return fake.session, nil
}

func (fake *fakeDesktopStore) TransitionDesktopSession(_ context.Context, _ string, _ []string, nextState, reason string, at time.Time, _ domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	fake.session.State = nextState
	if nextState == string(protocol.DesktopSessionTerminated) {
		fake.session.TerminatedAt = &at
		fake.session.TerminationReason = reason
	}
	return fake.session, nil
}

func (fake *fakeDesktopStore) BeginDesktopReconnect(_ context.Context, _ string, _ uint32, reconnectExpiresAt time.Time, _, _ domain.DesktopJoinCredential, _ domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	fake.reconnectExpiry = reconnectExpiresAt
	fake.session.State = string(protocol.DesktopSessionReconnecting)
	fake.session.KeyEpoch++
	fake.session.ReconnectExpiresAt = &reconnectExpiresAt
	return fake.session, nil
}

type fakeDesktopAgents struct {
	online       bool
	homeID       string
	agentID      string
	capabilities []string
}

func (fake fakeDesktopAgents) ResolveDesktopAgent(homeID, agentID string) (desktopAgentPresence, bool) {
	if !fake.online || fake.homeID != homeID || fake.agentID != agentID {
		return desktopAgentPresence{}, false
	}
	return desktopAgentPresence{HomeID: fake.homeID, AgentID: fake.agentID, Capabilities: fake.capabilities}, true
}

func activeDesktopOperator(now time.Time) domain.DesktopIdentity {
	return domain.DesktopIdentity{ID: "did_operator", HomeID: "home_1", IdentityType: domain.DesktopIdentityOperatorDevice, UserID: "usr_1", DeviceID: "device_1", ExpiresAt: now.Add(time.Hour)}
}

func activeDesktopEndpoint(now time.Time) domain.DesktopIdentity {
	return domain.DesktopIdentity{ID: "did_endpoint", HomeID: "home_1", IdentityType: domain.DesktopIdentityEndpoint, AgentID: "agent_1", ExpiresAt: now.Add(time.Hour)}
}

func desktopTestCapabilities() []string {
	return []string{"desktop.session.open", "desktop.view", "desktop.control"}
}

func sequenceDesktopToken() func() string {
	tokens := []string{strings.Repeat("a", 43), strings.Repeat("b", 43), strings.Repeat("c", 43), strings.Repeat("d", 43)}
	return func() string {
		value := tokens[0]
		tokens = tokens[1:]
		return value
	}
}
