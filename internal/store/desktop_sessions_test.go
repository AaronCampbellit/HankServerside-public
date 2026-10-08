package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestDesktopSessionCredentialIsSingleUseAndEpochBound(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, browser, agent := seedDesktopSessionRecords(t, db, "credential")
	now := session.RequestedAt.Add(time.Second)

	first, err := db.ConsumeDesktopJoinCredential(ctx, browser.CredentialHash, "browser", session.ID, 1, now)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if first.SessionID != session.ID {
		t.Fatalf("session = %q, want %q", first.SessionID, session.ID)
	}
	if _, err := db.ConsumeDesktopJoinCredential(ctx, browser.CredentialHash, "browser", session.ID, 1, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reuse = %v, want ErrNotFound", err)
	}
	if _, err := db.ConsumeDesktopJoinCredential(ctx, agent.CredentialHash, "browser", session.ID, 1, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong side = %v, want ErrNotFound", err)
	}
}

func TestDesktopSessionOnlyOneLiveOperatorPerAgent(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	first, _, _ := seedDesktopSessionRecords(t, db, "single")
	second, browser, agent := newDesktopSessionRecords(first, "desk_second_single")
	if err := db.CreateDesktopSession(ctx, second, browser, agent, requestedDesktopEvent(second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second live session = %v, want ErrConflict", err)
	}
}

func TestDesktopIdentityRevocationAtomicallyTerminatesSessionsCredentialsAndAudits(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, browser, agentCredential := seedDesktopSessionRecords(t, db, "identity_revoke")
	revokedAt := session.RequestedAt.Add(time.Second)
	changed, sessions, err := db.RevokeDesktopIdentity(ctx, session.HomeID, "did_endpoint_"+session.AgentID, "identity_replaced", revokedAt)
	if err != nil || !changed || len(sessions) != 1 || sessions[0] != session.ID {
		t.Fatalf("revoke = changed:%v sessions:%v err:%v", changed, sessions, err)
	}
	stored, err := db.GetDesktopSession(ctx, session.ID)
	if err != nil || stored.State != "terminated" || stored.TerminationReason != "identity_replaced" || stored.TerminatedAt == nil {
		t.Fatalf("terminal session = %#v err=%v", stored, err)
	}
	for _, credential := range []domain.DesktopJoinCredential{browser, agentCredential} {
		if _, err := db.ConsumeDesktopJoinCredential(ctx, credential.CredentialHash, credential.Side, session.ID, 1, revokedAt.Add(time.Second)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unused %s credential survived identity revoke: %v", credential.Side, err)
		}
	}
	events, err := db.ListDesktopSessionEvents(ctx, session.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].EventType != "desktop.identity.revoked" || events[1].ReasonCode != "identity_replaced" {
		t.Fatalf("revocation audit = %#v err=%v", events, err)
	}
}

func TestDesktopIdentityReplacementAtomicallyRevokesOldScopeAndInstallsNewIdentity(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, _, _ := seedDesktopSessionRecords(t, db, "identity_replace")
	replacement := testDesktopEndpoint(session.HomeID, session.AgentID, 1, session.RequestedAt.Add(time.Second))
	replacement.ID += "_new"
	replacement.PublicKeySPKI = []byte("replacement-endpoint-public-key")
	replacement.Certificate = []byte("replacement-endpoint-certificate")
	replacement.Fingerprint += "-new"
	sessions, err := db.ReplaceDesktopIdentity(ctx, "did_endpoint_"+session.AgentID, replacement, replacement.CreatedAt, "identity_replaced")
	if err != nil || len(sessions) != 1 || sessions[0] != session.ID {
		t.Fatalf("replace sessions=%v err=%v", sessions, err)
	}
	active, err := db.GetActiveDesktopEndpointIdentity(ctx, session.HomeID, session.AgentID, replacement.CreatedAt.Add(time.Second))
	if err != nil || active.ID != replacement.ID || active.Fingerprint != replacement.Fingerprint {
		t.Fatalf("replacement identity = %#v err=%v", active, err)
	}
	stored, err := db.GetDesktopSession(ctx, session.ID)
	if err != nil || stored.State != "terminated" || stored.TerminationReason != "identity_replaced" {
		t.Fatalf("replacement terminal session = %#v err=%v", stored, err)
	}
}

func TestDesktopOperatorIdentityReplacementAtomicallyRevokesOldScopeAndInstallsNewIdentity(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, _, _ := seedDesktopSessionRecords(t, db, "operator_identity_replace")
	deviceID := "device-operator_identity_replace"
	replacement := testDesktopOperator(session.HomeID, session.OperatorUserID, deviceID, 1, session.RequestedAt.Add(time.Second))
	replacement.ID += "_new"
	replacement.PublicKeySPKI = []byte("replacement-operator-public-key")
	replacement.Certificate = []byte("replacement-operator-certificate")
	replacement.Fingerprint += "-new"
	sessions, err := db.ReplaceDesktopIdentity(ctx, session.OperatorDeviceIdentityID, replacement, replacement.CreatedAt, "identity_replaced")
	if err != nil || len(sessions) != 1 || sessions[0] != session.ID {
		t.Fatalf("replace operator sessions=%v err=%v", sessions, err)
	}
	active, err := db.GetActiveDesktopOperatorIdentity(ctx, session.HomeID, session.OperatorUserID, deviceID, replacement.CreatedAt.Add(time.Second))
	if err != nil || active.ID != replacement.ID || active.Fingerprint != replacement.Fingerprint {
		t.Fatalf("replacement operator identity = %#v err=%v", active, err)
	}
	stored, err := db.GetDesktopSession(ctx, session.ID)
	if err != nil || stored.State != "terminated" || stored.TerminationReason != "identity_replaced" {
		t.Fatalf("replacement operator terminal session = %#v err=%v", stored, err)
	}
}

func TestDesktopSessionTerminalStateCannotReactivate(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, _, _ := seedDesktopSessionRecords(t, db, "terminal")
	at := session.RequestedAt.Add(time.Second)
	terminatedEvent := requestedDesktopEvent(session)
	terminatedEvent.EventType = "session.terminated"
	if _, err := db.TransitionDesktopSession(ctx, session.ID, []string{"requested"}, "terminated", "user_ended", at, terminatedEvent); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	activeEvent := requestedDesktopEvent(session)
	activeEvent.EventType = "session.active"
	if _, err := db.TransitionDesktopSession(ctx, session.ID, []string{"terminated"}, "active", "", at.Add(time.Second), activeEvent); !errors.Is(err, ErrConflict) {
		t.Fatalf("reactivate = %v, want ErrConflict", err)
	}
}

func TestDesktopTerminalHistoryAndReadinessArePaginatedMetadataOnly(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, _, _ := seedDesktopSessionRecords(t, db, "history")
	previous := "requested"
	for index, state := range []string{"offered", "agent_ready", "joining"} {
		event := requestedDesktopEvent(session)
		event.EventType = "desktop.session." + state
		event.ActorType = "server"
		event.OccurredAt = session.RequestedAt.Add(time.Duration(index+1) * time.Millisecond)
		if _, err := db.TransitionDesktopSession(ctx, session.ID, []string{previous}, state, "", event.OccurredAt, event); err != nil {
			t.Fatal(err)
		}
		previous = state
	}
	activeAt := session.RequestedAt.Add(time.Second)
	activeEvent := requestedDesktopEvent(session)
	activeEvent.EventType, activeEvent.ActorType, activeEvent.OccurredAt, activeEvent.MetadataJSON = "desktop.session.active", "server", activeAt, `{}`
	active, err := db.TransitionDesktopSession(ctx, session.ID, []string{"joining"}, "active", "", activeAt, activeEvent)
	if err != nil {
		t.Fatal(err)
	}
	ready := domain.DesktopSessionEvent{SessionID: session.ID, EventType: "desktop.session.ready", ActorType: "agent", ActorID: session.AgentID,
		OccurredAt: activeAt.Add(time.Second), Severity: "info", MetadataJSON: `{"platform":"windows","service":"ready","capture":"ready"}`}
	if err := db.AppendDesktopSessionEvent(ctx, ready); err != nil {
		t.Fatal(err)
	}
	terminatedAt := activeAt.Add(time.Minute)
	terminated := requestedDesktopEvent(active)
	terminated.EventType, terminated.ActorType, terminated.OccurredAt, terminated.MetadataJSON = "desktop.session.terminated", "agent", terminatedAt, `{"platform":"windows"}`
	if _, err := db.TransitionDesktopSession(ctx, session.ID, []string{"active"}, "terminated", "agent_ended", terminatedAt, terminated); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListTerminalDesktopSessions(ctx, session.HomeID, 0, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("history = %#v, err=%v", items, err)
	}
	if items[0].Platform != "windows" || items[0].DurationMilliseconds != time.Minute.Milliseconds() || items[0].TerminationReason != "agent_ended" {
		t.Fatalf("aggregate = %#v", items[0])
	}
	events, err := db.ListDesktopAgentReadinessEvents(ctx, session.HomeID, session.AgentID, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "desktop.session.ready" {
		t.Fatalf("readiness = %#v, err=%v", events, err)
	}
	counts, err := db.DesktopSessionStatePlatformCounts(ctx)
	if err != nil || counts["windows\x00terminated"] != 1 {
		t.Fatalf("platform state counts = %#v, err=%v", counts, err)
	}
}

func TestDesktopSessionConcurrentConsumptionAndReconnectChooseOneWinner(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	session, browser, _ := seedDesktopSessionRecords(t, db, "concurrent")
	now := session.RequestedAt.Add(time.Second)

	start := make(chan struct{})
	consumeErrors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := db.ConsumeDesktopJoinCredential(ctx, browser.CredentialHash, "browser", session.ID, 1, now)
			consumeErrors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(consumeErrors)
	assertOneDesktopWinner(t, consumeErrors, ErrNotFound)

	states := []string{"offered", "agent_ready", "joining", "active"}
	allowed := []string{"requested"}
	for index, state := range states {
		event := requestedDesktopEvent(session)
		event.EventType = "session." + state
		event.ActorType = "server"
		event.OccurredAt = now.Add(time.Duration(index+1) * time.Second)
		if _, err := db.TransitionDesktopSession(ctx, session.ID, allowed, state, "", event.OccurredAt, event); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
		allowed = []string{state}
	}

	reconnectAt := now.Add(10 * time.Second)
	reconnectExpiresAt := reconnectAt.Add(90 * time.Second)
	start = make(chan struct{})
	reconnectErrors := make(chan error, 2)
	for index := range 2 {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			browserHash := sha256.Sum256([]byte("browser-reconnect-" + string(rune('a'+index))))
			agentHash := sha256.Sum256([]byte("agent-reconnect-" + string(rune('a'+index))))
			browserCredential := domain.DesktopJoinCredential{
				ID: "cred_browser_reconnect_" + string(rune('a'+index)), SessionID: session.ID, Side: "browser",
				CredentialHash: browserHash[:], KeyEpoch: 2, CreatedAt: reconnectAt, ExpiresAt: reconnectExpiresAt,
			}
			agentCredential := domain.DesktopJoinCredential{
				ID: "cred_agent_reconnect_" + string(rune('a'+index)), SessionID: session.ID, Side: "agent",
				CredentialHash: agentHash[:], KeyEpoch: 2, CreatedAt: reconnectAt, ExpiresAt: reconnectExpiresAt,
			}
			event := requestedDesktopEvent(session)
			event.EventType = "session.reconnecting"
			event.ActorType = "browser"
			event.OccurredAt = reconnectAt
			<-start
			_, err := db.BeginDesktopReconnect(ctx, session.ID, 1, reconnectExpiresAt, browserCredential, agentCredential, event)
			reconnectErrors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(reconnectErrors)
	assertOneDesktopWinner(t, reconnectErrors, ErrConflict)

	events, err := db.ListDesktopSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListDesktopSessionEvents: %v", err)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event sequences are not gap-free: %#v", events)
		}
	}
}

func TestDesktopSessionOfferTransitionAndAgentJoinSerializeWithoutDatabaseAbort(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()

	for index := range 8 {
		suffix := fmt.Sprintf("offer_join_%d", index)
		session, _, agent := seedDesktopSessionRecords(t, db, suffix)
		at := session.RequestedAt.Add(time.Second)
		event := requestedDesktopEvent(session)
		event.EventType = "desktop.session.offered"
		event.ActorType = "server"
		event.OccurredAt = at

		start := make(chan struct{})
		errorsByOperation := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, err := db.TransitionDesktopSession(ctx, session.ID, []string{"requested"}, "offered", "", at, event)
			errorsByOperation <- err
		}()
		go func() {
			defer wait.Done()
			<-start
			_, err := db.ConsumeDesktopJoinCredential(ctx, agent.CredentialHash, "agent", session.ID, 1, at)
			errorsByOperation <- err
		}()
		close(start)
		wait.Wait()
		close(errorsByOperation)
		for err := range errorsByOperation {
			if err != nil {
				t.Fatalf("iteration %d: concurrent offer/join returned %v", index, err)
			}
		}
	}
}

func TestDesktopSessionRecordValidation(t *testing.T) {
	now := time.Now().UTC()
	session := domain.DesktopSession{
		ID: "desk_validation", HomeID: "home_validation", AgentID: "agent_validation",
		OperatorUserID: "usr_validation", OperatorDeviceIdentityID: "did_validation",
		RequestedPermissions: []string{"desktop.view"}, EffectivePermissions: []string{"desktop.view"},
		State: "requested", KeyEpoch: 1, RequestedAt: now,
		JoinExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(8 * time.Hour),
	}
	if err := validateDesktopSession(session); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}
	for name, mutate := range map[string]func(*domain.DesktopSession){
		"missing view": func(value *domain.DesktopSession) { value.RequestedPermissions = []string{"desktop.control"} },
		"bad state":    func(value *domain.DesktopSession) { value.State = "unknown" },
		"zero epoch":   func(value *domain.DesktopSession) { value.KeyEpoch = 0 },
		"long join":    func(value *domain.DesktopSession) { value.JoinExpiresAt = now.Add(61 * time.Second) },
		"long hard":    func(value *domain.DesktopSession) { value.HardExpiresAt = now.Add(8*time.Hour + time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := session
			mutate(&candidate)
			if err := validateDesktopSession(candidate); err == nil {
				t.Fatalf("invalid session accepted: %#v", candidate)
			}
		})
	}
}

func TestDesktopCredentialPairRequiresSHA256HashesAndSessionTimes(t *testing.T) {
	now := time.Now().UTC()
	session := domain.DesktopSession{
		ID: "desk_credential_validation", RequestedAt: now,
		JoinExpiresAt: now.Add(time.Minute), KeyEpoch: 1,
	}
	browserHash := sha256.Sum256([]byte("browser"))
	agentHash := sha256.Sum256([]byte("agent"))
	browser := domain.DesktopJoinCredential{
		ID: "cred_browser_validation", SessionID: session.ID, Side: "browser",
		CredentialHash: browserHash[:], KeyEpoch: 1, CreatedAt: now, ExpiresAt: session.JoinExpiresAt,
	}
	agent := domain.DesktopJoinCredential{
		ID: "cred_agent_validation", SessionID: session.ID, Side: "agent",
		CredentialHash: agentHash[:], KeyEpoch: 1, CreatedAt: now, ExpiresAt: session.JoinExpiresAt,
	}
	if err := validateDesktopCredentialPair(session, browser, agent); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}

	shortHash := browser
	shortHash.CredentialHash = make([]byte, sha256.Size-1)
	if err := validateDesktopCredentialPair(session, shortHash, agent); err == nil {
		t.Fatal("non-SHA-256 credential hash accepted")
	}
	wrongCreatedAt := browser
	wrongCreatedAt.CreatedAt = now.Add(-time.Second)
	agentWrongCreatedAt := agent
	agentWrongCreatedAt.CreatedAt = wrongCreatedAt.CreatedAt
	if err := validateDesktopCredentialPair(session, wrongCreatedAt, agentWrongCreatedAt); err == nil {
		t.Fatal("credential creation time outside session epoch accepted")
	}
}

func seedDesktopSessionRecords(t *testing.T, db *Store, suffix string) (domain.DesktopSession, domain.DesktopJoinCredential, domain.DesktopJoinCredential) {
	t.Helper()
	ctx := context.Background()
	home, user, agentRecord := seedDesktopOwnerAgent(t, db, suffix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := testDesktopTrustRoot(home.ID, 1, now)
	operator := testDesktopOperator(home.ID, user.ID, "device-"+suffix, 1, now)
	if err := db.BootstrapDesktopTrust(ctx, root, operator); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}
	endpoint := testDesktopEndpoint(home.ID, agentRecord.ID, 1, now)
	if err := db.CreateDesktopIdentity(ctx, endpoint); err != nil {
		t.Fatalf("CreateDesktopIdentity: %v", err)
	}
	session := domain.DesktopSession{
		ID: "desk_" + suffix, HomeID: home.ID, AgentID: agentRecord.ID,
		OperatorUserID: user.ID, OperatorDeviceIdentityID: operator.ID,
		RequestedPermissions: []string{"desktop.view"}, EffectivePermissions: []string{"desktop.view"},
		State: "requested", KeyEpoch: 1, RequestedAt: now,
		JoinExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(8 * time.Hour),
	}
	session, browser, agentCredential := newDesktopSessionRecords(session, session.ID)
	if err := db.CreateDesktopSession(ctx, session, browser, agentCredential, requestedDesktopEvent(session)); err != nil {
		t.Fatalf("CreateDesktopSession: %v", err)
	}
	return session, browser, agentCredential
}

func newDesktopSessionRecords(base domain.DesktopSession, sessionID string) (domain.DesktopSession, domain.DesktopJoinCredential, domain.DesktopJoinCredential) {
	base.ID = sessionID
	browserHash := sha256.Sum256([]byte("browser-hash-" + sessionID))
	agentHash := sha256.Sum256([]byte("agent-hash-" + sessionID))
	browser := domain.DesktopJoinCredential{
		ID: "cred_browser_" + sessionID, SessionID: sessionID, Side: "browser",
		CredentialHash: browserHash[:], KeyEpoch: base.KeyEpoch,
		CreatedAt: base.RequestedAt, ExpiresAt: base.JoinExpiresAt,
	}
	agent := domain.DesktopJoinCredential{
		ID: "cred_agent_" + sessionID, SessionID: sessionID, Side: "agent",
		CredentialHash: agentHash[:], KeyEpoch: base.KeyEpoch,
		CreatedAt: base.RequestedAt, ExpiresAt: base.JoinExpiresAt,
	}
	return base, browser, agent
}

func requestedDesktopEvent(session domain.DesktopSession) domain.DesktopSessionEvent {
	return domain.DesktopSessionEvent{
		SessionID: session.ID, EventType: "session.requested", ActorType: "user",
		ActorID: session.OperatorUserID, OccurredAt: session.RequestedAt,
		Severity: "info", MetadataJSON: `{}`,
	}
}

func assertOneDesktopWinner(t *testing.T, errorsChannel <-chan error, losingError error) {
	t.Helper()
	var successes, expectedFailures int
	for err := range errorsChannel {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, losingError):
			expectedFailures++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || expectedFailures != 1 {
		t.Fatalf("successes=%d expected failures=%d, want 1 each", successes, expectedFailures)
	}
}
