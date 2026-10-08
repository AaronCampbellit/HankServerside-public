package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDesktopAgentScopeRequiresOwningHomeAgentSessionAndEpoch(t *testing.T) {
	session := domain.DesktopSession{ID: "desk_0001", HomeID: "home_0001", AgentID: "agent_0001", KeyEpoch: 2, HardExpiresAt: time.Now().Add(time.Hour)}
	if err := validateDesktopAgentScope(session, "home_0001", "agent_0001", "desk_0001", 2, time.Now()); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
	for _, candidate := range []struct {
		home, agent, session string
		epoch                uint32
	}{
		{"home_other", "agent_0001", "desk_0001", 2},
		{"home_0001", "agent_other", "desk_0001", 2},
		{"home_0001", "agent_0001", "desk_other", 2},
		{"home_0001", "agent_0001", "desk_0001", 3},
	} {
		if err := validateDesktopAgentScope(session, candidate.home, candidate.agent, candidate.session, candidate.epoch, time.Now()); err == nil {
			t.Fatalf("invalid scope accepted: %#v", candidate)
		}
	}
}

func TestDesktopTerminalAgentAcknowledgementsAreIdempotentAfterAuthoritativeClose(t *testing.T) {
	for _, state := range []string{"denied", "failed", "expired", "terminated"} {
		for _, event := range []string{protocol.EventDesktopSessionTerminated, protocol.EventDesktopSessionError} {
			if !isIdempotentLateDesktopTerminalEvent(state, event) {
				t.Fatalf("%s after %s was not idempotent", event, state)
			}
		}
	}
	if isIdempotentLateDesktopTerminalEvent("active", protocol.EventDesktopSessionTerminated) ||
		isIdempotentLateDesktopTerminalEvent("terminated", protocol.EventDesktopSessionConnected) {
		t.Fatal("non-late lifecycle transition was treated as idempotent")
	}
}

func TestDesktopTerminalReplayPolicyDiscardsOnlyLateTerminalAcknowledgements(t *testing.T) {
	for _, state := range []string{"denied", "failed", "expired", "terminated"} {
		if !isIdempotentLateDesktopTerminalEvent(state, protocol.EventDesktopSessionTerminated) || !isIdempotentLateDesktopTerminalEvent(state, protocol.EventDesktopSessionError) {
			t.Fatalf("terminal replay would be persisted after %s", state)
		}
	}
	if isIdempotentLateDesktopTerminalEvent("active", protocol.EventDesktopSessionError) {
		t.Fatal("first authoritative terminal event would be discarded")
	}
}

func TestDesktopTerminalReplayDoesNotGrowPersistedHistory(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_replay", Email: "replay@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_replay", UserID: user.ID, Name: "Replay", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_replay", HomeID: home.ID, Name: "Replay", Status: domain.AgentStatusOnline, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	root := domain.DesktopTrustRoot{HomeID: home.ID, Generation: 1, Algorithm: domain.DesktopTrustAlgorithm, PublicKeySPKI: []byte("root-public-key"), Fingerprint: "root-fingerprint", RecoveryEnvelope: []byte("encrypted-recovery-envelope"), CreatedAt: now}
	operator := domain.DesktopIdentity{ID: "did_operator_replay", HomeID: home.ID, IdentityType: domain.DesktopIdentityOperatorDevice, UserID: user.ID, DeviceID: "device_replay", PublicKeySPKI: []byte("operator"), Certificate: []byte("certificate"), Fingerprint: "operator-fingerprint", Capabilities: []string{"desktop.view"}, TrustRootGeneration: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.BootstrapDesktopTrust(ctx, root, operator); err != nil {
		t.Fatal(err)
	}
	session := domain.DesktopSession{ID: "desk_replay", HomeID: home.ID, AgentID: agent.ID, OperatorUserID: user.ID, OperatorDeviceIdentityID: operator.ID,
		RequestedPermissions: []string{"desktop.view"}, EffectivePermissions: []string{"desktop.view"}, State: "requested", KeyEpoch: 1,
		RequestedAt: now, JoinExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)}
	browserHash, agentHash := sha256.Sum256([]byte("browser")), sha256.Sum256([]byte("agent"))
	browser := domain.DesktopJoinCredential{ID: "cred_browser_replay", SessionID: session.ID, Side: "browser", CredentialHash: browserHash[:], KeyEpoch: 1, CreatedAt: now, ExpiresAt: session.JoinExpiresAt}
	agentCredential := domain.DesktopJoinCredential{ID: "cred_agent_replay", SessionID: session.ID, Side: "agent", CredentialHash: agentHash[:], KeyEpoch: 1, CreatedAt: now, ExpiresAt: session.JoinExpiresAt}
	requested := domain.DesktopSessionEvent{SessionID: session.ID, EventType: "session.requested", ActorType: "user", ActorID: user.ID, OccurredAt: now, Severity: "info", MetadataJSON: `{}`}
	if err := db.CreateDesktopSession(ctx, session, browser, agentCredential, requested); err != nil {
		t.Fatal(err)
	}
	terminal := domain.DesktopSessionEvent{SessionID: session.ID, EventType: protocol.EventDesktopSessionTerminated, ActorType: "server", OccurredAt: now.Add(time.Second), Severity: "info", ReasonCode: "user_ended", MetadataJSON: `{}`}
	if _, err := db.TransitionDesktopSession(ctx, session.ID, []string{"requested"}, "terminated", "user_ended", terminal.OccurredAt, terminal); err != nil {
		t.Fatal(err)
	}
	relay := &recordingDesktopRelay{}
	server := &Server{store: db, desktopRelay: relay}
	payload, _ := json.Marshal(protocol.DesktopSessionLifecycleEvent{Protocol: protocol.DesktopProtocolVersion, SessionID: session.ID, KeyEpoch: 1, ReasonCode: "agent_ended", Metadata: map[string]string{}})
	for range 1_000 {
		if err := server.handleDesktopAgentEvent(ctx, home.ID, agent.ID, protocol.EventDesktopSessionTerminated, payload); err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.ListDesktopSessionEvents(ctx, session.ID, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("terminal replay history = %d events, err=%v", len(events), err)
	}
	if len(relay.revocations) != 1_000 {
		t.Fatalf("terminal replay relay revocations = %d", len(relay.revocations))
	}
}

func TestDesktopTerminalEventsUseStableRelayRevocationReasons(t *testing.T) {
	if got := desktopTerminalRelayReason(protocol.EventDesktopSessionTerminated, "user_ended"); got != "user_ended" {
		t.Fatalf("explicit reason = %q", got)
	}
	if got := desktopTerminalRelayReason(protocol.EventDesktopSessionError, ""); got != "agent_error" {
		t.Fatalf("error reason = %q", got)
	}
	if got := desktopTerminalRelayReason(protocol.EventDesktopSessionTerminated, "unsafe reason text"); got != "agent_ended" {
		t.Fatalf("fallback reason = %q", got)
	}
}

func TestDesktopTrustMutationRevokesEveryAffectedRelay(t *testing.T) {
	relay := &recordingDesktopRelay{}
	server := &Server{desktopRelay: relay}
	server.revokeDesktopRelays([]string{"desk_one", "desk_two"}, "revoked")
	if len(relay.revocations) != 2 || relay.revocations[0] != "desk_one:revoked" || relay.revocations[1] != "desk_two:revoked" {
		t.Fatalf("revocations = %#v", relay.revocations)
	}
}

type recordingDesktopRelay struct{ revocations []string }

func (*recordingDesktopRelay) Reserve(desktopRelayJoinClaim) error     { return nil }
func (*recordingDesktopRelay) CancelReservation(desktopRelayJoinClaim) {}
func (*recordingDesktopRelay) Join(context.Context, desktopRelayJoinClaim, desktopRelayEndpoint) error {
	return nil
}
func (r *recordingDesktopRelay) Revoke(sessionID, reason string) {
	r.revocations = append(r.revocations, sessionID+":"+reason)
}
func (*recordingDesktopRelay) Snapshot(string) desktopRelaySnapshot { return desktopRelaySnapshot{} }

func TestDesktopAgentMetadataIsStrictlyAllowlisted(t *testing.T) {
	if _, err := sanitizeDesktopAgentMetadata(map[string]string{"fps": "60", "codec": "h264", "indicator": "ready", "data_plane": "ready"}); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	if _, err := sanitizeDesktopAgentMetadata(map[string]string{"clipboard": "secret"}); err == nil {
		t.Fatal("content-bearing metadata accepted")
	}
	if _, err := sanitizeDesktopAgentMetadata(map[string]string{"status": string(make([]byte, 257))}); err == nil {
		t.Fatal("oversized metadata accepted")
	}
}

func TestDesktopControlAuditMetadataIsContentFreeAndOperationScoped(t *testing.T) {
	valid := []struct {
		event    string
		metadata map[string]string
	}{
		{protocol.EventDesktopControlChanged, map[string]string{"enabled": "true", "success": "true"}},
		{protocol.EventDesktopDisplayChanged, map[string]string{"display_id": "display-2", "success": "true"}},
		{protocol.EventDesktopClipboard, map[string]string{"direction": "browser_to_agent", "success": "false", "reason": "clipboard_unavailable"}},
		{protocol.EventDesktopSpecialKey, map[string]string{"name": "alt_tab", "success": "true"}},
	}
	for _, candidate := range valid {
		sanitized, err := sanitizeDesktopAgentMetadata(candidate.metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateDesktopControlAuditMetadata(candidate.event, sanitized); err != nil {
			t.Fatalf("%s: %v", candidate.event, err)
		}
	}
	for _, forbidden := range []map[string]string{{"text": "secret"}, {"code": "KeyA", "success": "true"}, {"x": "0.5", "success": "true"}, {"scan_code": "30", "success": "true"}, {"button": "0", "success": "true"}, {"wheel_y": "120", "success": "true"}} {
		sanitized, err := sanitizeDesktopAgentMetadata(forbidden)
		if err == nil {
			err = validateDesktopControlAuditMetadata(protocol.EventDesktopControlChanged, sanitized)
		}
		if err == nil {
			t.Fatalf("content-bearing metadata accepted: %#v", forbidden)
		}
	}
	if err := validateDesktopControlAuditMetadata(protocol.EventDesktopSpecialKey, map[string]string{"name": "arbitrary_command", "success": "true"}); err == nil {
		t.Fatal("unknown special key audit accepted")
	}
	if err := validateDesktopControlAuditMetadata(protocol.EventDesktopClipboard, map[string]string{"direction": "browser_to_agent", "success": "false", "reason": "clipboard contained secret text"}); err == nil {
		t.Fatal("free-form clipboard reason accepted")
	}
}

func TestDesktopPrivilegedAuditMetadataIsStrictlyMetadataOnly(t *testing.T) {
	valid := []struct {
		event string
		data  map[string]string
	}{
		{protocol.EventDesktopSecureDesktopEntered, map[string]string{"platform": "windows", "state": "entered", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopSecureDesktopUnavailable, map[string]string{"platform": "windows", "state": "unavailable", "reason": "capture_unavailable", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopPermissionRequired, map[string]string{"platform": "macos", "state": "required", "permission": "screen_recording", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopPermissionGranted, map[string]string{"platform": "macos", "state": "granted", "permission": "accessibility", "duration_ms": "1200", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopConsoleLocked, map[string]string{"platform": "macos", "state": "locked", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopHelperRestarted, map[string]string{"platform": "windows", "state": "restarted", "reason": "host_crash", "session": "desk_0001", "epoch": "2"}},
		{protocol.EventDesktopIndicatorLost, map[string]string{"platform": "macos", "state": "lost", "reason": "indicator_hidden", "session": "desk_0001", "epoch": "2"}},
	}
	for _, candidate := range valid {
		sanitized, err := sanitizeDesktopAgentMetadata(candidate.data)
		if err != nil {
			t.Fatalf("%s sanitize: %v", candidate.event, err)
		}
		if err := validateDesktopControlAuditMetadata(candidate.event, sanitized); err != nil {
			t.Fatalf("%s validate: %v", candidate.event, err)
		}
	}
	for _, key := range []string{"window_title", "process_name", "frame", "input_code", "clipboard", "pipe_payload", "xpc_payload", "peer_token"} {
		if _, err := sanitizeDesktopAgentMetadata(map[string]string{key: "marker"}); err == nil {
			t.Fatalf("forbidden %s accepted", key)
		}
	}
	if err := validateDesktopControlAuditMetadata(protocol.EventDesktopPermissionRequired, map[string]string{"platform": "macos", "state": "required", "permission": "screen_recording", "name": "Secret Window"}); err == nil {
		t.Fatal("content-bearing privileged audit metadata accepted")
	}
	if err := validateDesktopControlAuditMetadata(protocol.EventDesktopSecureDesktopEntered, map[string]string{"platform": "windows", "state": "exited", "session": "desk_0001", "epoch": "2"}); err == nil {
		t.Fatal("mismatched privileged state accepted")
	}
}
