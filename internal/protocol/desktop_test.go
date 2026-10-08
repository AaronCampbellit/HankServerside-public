package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

const testDesktopSessionID = "desk_01J1V2J4S5Q6R7T8V9W0X1Y2Z3"

func TestDesktopPermissionSetRejectsInvalidCombinations(t *testing.T) {
	t.Parallel()

	valid := []DesktopPermission{
		DesktopPermissionView,
		DesktopPermissionControl,
		DesktopPermissionClipboardRead,
		DesktopPermissionClipboardWrite,
		DesktopPermissionElevate,
		DesktopPermissionSecureDesktop,
		DesktopPermissionUnattended,
	}
	if err := ValidateDesktopPermissions(valid); err != nil {
		t.Fatalf("valid permissions rejected: %v", err)
	}

	for _, permissions := range [][]DesktopPermission{
		nil,
		{DesktopPermissionControl},
		{DesktopPermissionView, "desktop.unknown"},
		{DesktopPermissionView, DesktopPermissionView},
		{DesktopPermissionView, DesktopPermissionElevate},
		{DesktopPermissionView, DesktopPermissionSecureDesktop},
	} {
		if err := ValidateDesktopPermissions(permissions); err == nil {
			t.Fatalf("invalid permissions accepted: %#v", permissions)
		}
	}
}

func TestDesktopSessionStateTransitions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		from DesktopSessionState
		to   DesktopSessionState
		ok   bool
	}{
		{DesktopSessionRequested, DesktopSessionOffered, true},
		{DesktopSessionJoining, DesktopSessionActive, true},
		{DesktopSessionActive, DesktopSessionReconnecting, true},
		{DesktopSessionReconnecting, DesktopSessionActive, true},
		{DesktopSessionTerminated, DesktopSessionActive, false},
		{DesktopSessionRequested, DesktopSessionActive, false},
	} {
		if got := CanTransitionDesktopSession(tc.from, tc.to); got != tc.ok {
			t.Fatalf("CanTransitionDesktopSession(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.ok)
		}
	}

	states := []DesktopSessionState{
		DesktopSessionRequested,
		DesktopSessionOffered,
		DesktopSessionAgentReady,
		DesktopSessionJoining,
		DesktopSessionActive,
		DesktopSessionReconnecting,
		DesktopSessionDenied,
		DesktopSessionFailed,
		DesktopSessionExpired,
		DesktopSessionTerminated,
	}
	for _, terminal := range []DesktopSessionState{
		DesktopSessionDenied,
		DesktopSessionFailed,
		DesktopSessionExpired,
		DesktopSessionTerminated,
	} {
		for _, next := range states {
			if CanTransitionDesktopSession(terminal, next) {
				t.Fatalf("terminal transition %q -> %q accepted", terminal, next)
			}
		}
	}
}

func TestDesktopOfferValidationBindsSessionScopeCredentialAndEpoch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	valid := validDesktopOffer(now)
	if err := valid.Validate(now); err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}

	for name, mutate := range map[string]func(*DesktopSessionOffer){
		"protocol":                func(value *DesktopSessionOffer) { value.Protocol = "desktop.v2" },
		"session":                 func(value *DesktopSessionOffer) { value.SessionID = "bad id" },
		"home":                    func(value *DesktopSessionOffer) { value.HomeID = " " },
		"agent":                   func(value *DesktopSessionOffer) { value.AgentID = "" },
		"operator user":           func(value *DesktopSessionOffer) { value.OperatorUserID = "" },
		"operator device":         func(value *DesktopSessionOffer) { value.OperatorDeviceID = "" },
		"epoch":                   func(value *DesktopSessionOffer) { value.KeyEpoch = 0 },
		"credential":              func(value *DesktopSessionOffer) { value.AgentJoinCredential = "" },
		"operator certificate":    func(value *DesktopSessionOffer) { value.OperatorCertificate = "" },
		"certificate fingerprint": func(value *DesktopSessionOffer) { value.OperatorCertificateFingerprint = "" },
		"root generation":         func(value *DesktopSessionOffer) { value.TrustRootGeneration = 0 },
		"root key":                func(value *DesktopSessionOffer) { value.TrustRootPublicKeySPKI = "" },
		"root fingerprint":        func(value *DesktopSessionOffer) { value.TrustRootFingerprint = "" },
		"revoked operator":        func(value *DesktopSessionOffer) { value.OperatorIdentityStatus = "revoked" },
		"stale operator check":    func(value *DesktopSessionOffer) { value.OperatorIdentityCheckedAt = now.Add(-3 * time.Minute) },
		"expired join":            func(value *DesktopSessionOffer) { value.JoinExpiresAt = now },
		"hard expiry before join": func(value *DesktopSessionOffer) { value.HardExpiresAt = value.JoinExpiresAt },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(now); err == nil {
				t.Fatalf("invalid offer accepted: %#v", candidate)
			}
		})
	}
}

func TestDesktopHandshakeReadyAndLifecycleValidation(t *testing.T) {
	t.Parallel()

	party := DesktopHandshakeParty{
		IdentityID:         "identity_01",
		Certificate:        "certificate",
		EphemeralPublicKey: "ephemeral-key",
		Signature:          "signature",
	}
	if err := party.Validate(); err != nil {
		t.Fatalf("valid handshake party rejected: %v", err)
	}
	for _, mutate := range []func(*DesktopHandshakeParty){
		func(value *DesktopHandshakeParty) { value.IdentityID = "" },
		func(value *DesktopHandshakeParty) { value.Certificate = "" },
		func(value *DesktopHandshakeParty) { value.EphemeralPublicKey = "" },
		func(value *DesktopHandshakeParty) { value.Signature = "" },
	} {
		candidate := party
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid handshake party accepted: %#v", candidate)
		}
	}

	ready := DesktopSessionReady{
		Protocol:                       DesktopProtocolVersion,
		SessionID:                      testDesktopSessionID,
		KeyEpoch:                       1,
		EndpointCertificate:            "certificate",
		EndpointCertificateFingerprint: "fingerprint",
		Readiness:                      map[string]string{"screen_recording": "ready"},
	}
	if err := ready.Validate(); err != nil {
		t.Fatalf("valid ready rejected: %v", err)
	}
	ready.EndpointCertificate = ""
	if err := ready.Validate(); err == nil {
		t.Fatal("ready event without endpoint certificate accepted")
	}

	event := DesktopSessionLifecycleEvent{
		Protocol:   DesktopProtocolVersion,
		SessionID:  testDesktopSessionID,
		KeyEpoch:   1,
		ReasonCode: "transport_closed",
		Metadata:   map[string]string{"display_id": "primary"},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid lifecycle event rejected: %v", err)
	}
	event.Protocol = "desktop.v2"
	if err := event.Validate(); err == nil {
		t.Fatal("unknown lifecycle protocol accepted")
	}
}

func TestDesktopControlNamesAreAllowlisted(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		CommandDesktopStatus,
		CommandDesktopSessionOffer,
		CommandDesktopSessionActivate,
		CommandDesktopSessionClose,
		CommandDesktopSessionSetControl,
		CommandDesktopSessionSetDisplay,
		CommandDesktopSessionSetQuality,
		CommandDesktopSessionRelayPressure,
	} {
		if !IsDesktopCommand(command) {
			t.Fatalf("desktop command %q is not allowlisted", command)
		}
	}
	for _, event := range []string{
		EventDesktopSessionReady,
		EventDesktopSessionConnected,
		EventDesktopSessionDisconnected,
		EventDesktopDisplayChanged,
		EventDesktopPermissionRequired,
		EventDesktopSecureDesktopEntered,
		EventDesktopSecureDesktopExited,
		EventDesktopSessionStats,
		EventDesktopSessionError,
		EventDesktopSessionTerminated,
	} {
		if !IsDesktopEvent(event) {
			t.Fatalf("desktop event %q is not allowlisted", event)
		}
	}
	if IsDesktopCommand("desktop.session.delete") || IsDesktopEvent("desktop.frame") {
		t.Fatal("unknown desktop control name accepted")
	}
}

func TestDesktopPayloadsUseStableWireNames(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "handshake party",
			in: DesktopHandshakeParty{
				IdentityID: "identity_01", Certificate: "certificate",
				EphemeralPublicKey: "ephemeral-key", Signature: "signature",
			},
			want: `{"identity_id":"identity_01","certificate":"certificate","ephemeral_public_key":"ephemeral-key","signature":"signature"}`,
		},
		{
			name: "offer",
			in:   validDesktopOffer(now),
			want: `{"protocol":"desktop.v1","session_id":"desk_01J1V2J4S5Q6R7T8V9W0X1Y2Z3","home_id":"home_1","agent_id":"agent_1","operator_user_id":"usr_1","operator_device_id":"device_1","permissions":["desktop.view"],"key_epoch":1,"join_expires_at":"2026-07-21T12:01:00Z","hard_expires_at":"2026-07-21T13:00:00Z","agent_join_credential":"agent-credential","operator_certificate":"certificate","operator_certificate_fingerprint":"fingerprint","trust_root_generation":1,"trust_root_public_key_spki":"root-public-key","trust_root_fingerprint":"root-fingerprint","operator_identity_status":"active","operator_identity_checked_at":"2026-07-21T12:00:00Z"}`,
		},
		{
			name: "ready",
			in: DesktopSessionReady{
				Protocol: DesktopProtocolVersion, SessionID: testDesktopSessionID, KeyEpoch: 1,
				EndpointCertificate: "certificate", EndpointCertificateFingerprint: "fingerprint",
				Readiness: map[string]string{"screen_recording": "ready"},
			},
			want: `{"protocol":"desktop.v1","session_id":"desk_01J1V2J4S5Q6R7T8V9W0X1Y2Z3","key_epoch":1,"endpoint_certificate":"certificate","endpoint_certificate_fingerprint":"fingerprint","readiness":{"screen_recording":"ready"}}`,
		},
		{
			name: "lifecycle event",
			in: DesktopSessionLifecycleEvent{
				Protocol: DesktopProtocolVersion, SessionID: testDesktopSessionID, KeyEpoch: 1,
				ReasonCode: "transport_closed", Metadata: map[string]string{"display_id": "primary"},
			},
			want: `{"protocol":"desktop.v1","session_id":"desk_01J1V2J4S5Q6R7T8V9W0X1Y2Z3","key_epoch":1,"reason_code":"transport_closed","metadata":{"display_id":"primary"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(encoded); got != tc.want {
				t.Fatalf("payload = %s, want %s", got, tc.want)
			}
		})
	}
}

func validDesktopOffer(now time.Time) DesktopSessionOffer {
	return DesktopSessionOffer{
		Protocol:                       DesktopProtocolVersion,
		SessionID:                      testDesktopSessionID,
		HomeID:                         "home_1",
		AgentID:                        "agent_1",
		OperatorUserID:                 "usr_1",
		OperatorDeviceID:               "device_1",
		Permissions:                    []DesktopPermission{DesktopPermissionView},
		KeyEpoch:                       1,
		JoinExpiresAt:                  now.Add(time.Minute),
		HardExpiresAt:                  now.Add(time.Hour),
		AgentJoinCredential:            "agent-credential",
		OperatorCertificate:            "certificate",
		OperatorCertificateFingerprint: "fingerprint",
		TrustRootGeneration:            1,
		TrustRootPublicKeySPKI:         "root-public-key",
		TrustRootFingerprint:           "root-fingerprint",
		OperatorIdentityStatus:         "active",
		OperatorIdentityCheckedAt:      now,
	}
}
