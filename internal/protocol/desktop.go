package protocol

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

const DesktopProtocolVersion = "desktop.v1"

type DesktopPermission string

const (
	DesktopPermissionView           DesktopPermission = "desktop.view"
	DesktopPermissionControl        DesktopPermission = "desktop.control"
	DesktopPermissionClipboardRead  DesktopPermission = "desktop.clipboard.read"
	DesktopPermissionClipboardWrite DesktopPermission = "desktop.clipboard.write"
	DesktopPermissionElevate        DesktopPermission = "desktop.elevate"
	DesktopPermissionSecureDesktop  DesktopPermission = "desktop.secure_desktop"
	DesktopPermissionUnattended     DesktopPermission = "desktop.unattended"
)

type DesktopSessionState string

const (
	DesktopSessionRequested    DesktopSessionState = "requested"
	DesktopSessionOffered      DesktopSessionState = "offered"
	DesktopSessionAgentReady   DesktopSessionState = "agent_ready"
	DesktopSessionJoining      DesktopSessionState = "joining"
	DesktopSessionActive       DesktopSessionState = "active"
	DesktopSessionReconnecting DesktopSessionState = "reconnecting"
	DesktopSessionDenied       DesktopSessionState = "denied"
	DesktopSessionFailed       DesktopSessionState = "failed"
	DesktopSessionExpired      DesktopSessionState = "expired"
	DesktopSessionTerminated   DesktopSessionState = "terminated"
)

type DesktopIdentityType string

const (
	DesktopIdentityOperatorDevice DesktopIdentityType = "operator_device"
	DesktopIdentityEndpoint       DesktopIdentityType = "endpoint"
)

const (
	CommandDesktopStatus               = "desktop.status"
	CommandDesktopSessionOffer         = "desktop.session.offer"
	CommandDesktopSessionActivate      = "desktop.session.activate"
	CommandDesktopSessionClose         = "desktop.session.close"
	CommandDesktopSessionSetControl    = "desktop.session.set_control"
	CommandDesktopSessionSetDisplay    = "desktop.session.set_display"
	CommandDesktopSessionSetQuality    = "desktop.session.set_quality"
	CommandDesktopSessionRelayPressure = "desktop.session.relay_pressure"

	EventDesktopSessionReady             = "desktop.session.ready"
	EventDesktopSessionConnected         = "desktop.session.connected"
	EventDesktopSessionDisconnected      = "desktop.session.disconnected"
	EventDesktopDisplayChanged           = "desktop.display.changed"
	EventDesktopPermissionRequired       = "desktop.permission.required"
	EventDesktopSecureDesktopEntered     = "desktop.secure_desktop.entered"
	EventDesktopSecureDesktopExited      = "desktop.secure_desktop.exited"
	EventDesktopSecureDesktopUnavailable = "desktop.secure_desktop.unavailable"
	EventDesktopPermissionGranted        = "desktop.permission.granted"
	EventDesktopPermissionLost           = "desktop.permission.lost"
	EventDesktopConsoleLocked            = "desktop.console.locked"
	EventDesktopConsoleSwitched          = "desktop.console.switched"
	EventDesktopHelperRestarted          = "desktop.helper.restarted"
	EventDesktopHelperFailed             = "desktop.helper.failed"
	EventDesktopIndicatorLost            = "desktop.indicator.lost"
	EventDesktopIndicatorRestored        = "desktop.indicator.restored"
	EventDesktopSessionStats             = "desktop.session.stats"
	EventDesktopSessionError             = "desktop.session.error"
	EventDesktopSessionTerminated        = "desktop.session.terminated"
	EventDesktopControlChanged           = "desktop.control.changed"
	EventDesktopClipboard                = "desktop.clipboard"
	EventDesktopSpecialKey               = "desktop.special_key"
)

var desktopIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type DesktopHandshakeParty struct {
	IdentityID         string `json:"identity_id"`
	Certificate        string `json:"certificate"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Signature          string `json:"signature"`
}

type DesktopSessionOffer struct {
	Protocol                       string              `json:"protocol"`
	SessionID                      string              `json:"session_id"`
	HomeID                         string              `json:"home_id"`
	AgentID                        string              `json:"agent_id"`
	OperatorUserID                 string              `json:"operator_user_id"`
	OperatorDeviceID               string              `json:"operator_device_id"`
	Permissions                    []DesktopPermission `json:"permissions"`
	KeyEpoch                       uint32              `json:"key_epoch"`
	JoinExpiresAt                  time.Time           `json:"join_expires_at"`
	HardExpiresAt                  time.Time           `json:"hard_expires_at"`
	AgentJoinCredential            string              `json:"agent_join_credential"`
	OperatorCertificate            string              `json:"operator_certificate"`
	OperatorCertificateFingerprint string              `json:"operator_certificate_fingerprint"`
	TrustRootGeneration            uint32              `json:"trust_root_generation"`
	TrustRootPublicKeySPKI         string              `json:"trust_root_public_key_spki"`
	TrustRootFingerprint           string              `json:"trust_root_fingerprint"`
	OperatorIdentityStatus         string              `json:"operator_identity_status"`
	OperatorIdentityCheckedAt      time.Time           `json:"operator_identity_checked_at"`
}

type DesktopSessionReady struct {
	Protocol                       string            `json:"protocol"`
	SessionID                      string            `json:"session_id"`
	KeyEpoch                       uint32            `json:"key_epoch"`
	EndpointCertificate            string            `json:"endpoint_certificate"`
	EndpointCertificateFingerprint string            `json:"endpoint_certificate_fingerprint"`
	Readiness                      map[string]string `json:"readiness"`
}

type DesktopSessionLifecycleEvent struct {
	Protocol   string            `json:"protocol"`
	SessionID  string            `json:"session_id"`
	KeyEpoch   uint32            `json:"key_epoch"`
	ReasonCode string            `json:"reason_code,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

func ValidateDesktopPermissions(values []DesktopPermission) error {
	if len(values) == 0 || !slices.Contains(values, DesktopPermissionView) {
		return errors.New("desktop.view is required")
	}

	allowed := map[DesktopPermission]bool{
		DesktopPermissionView:           true,
		DesktopPermissionControl:        true,
		DesktopPermissionClipboardRead:  true,
		DesktopPermissionClipboardWrite: true,
		DesktopPermissionElevate:        true,
		DesktopPermissionSecureDesktop:  true,
		DesktopPermissionUnattended:     true,
	}
	seen := make(map[DesktopPermission]bool, len(values))
	for _, value := range values {
		if !allowed[value] {
			return errors.New("unsupported desktop permission")
		}
		if seen[value] {
			return errors.New("duplicate desktop permission")
		}
		seen[value] = true
	}
	if (seen[DesktopPermissionElevate] || seen[DesktopPermissionSecureDesktop]) && !seen[DesktopPermissionControl] {
		return errors.New("elevated and secure-desktop access require desktop.control")
	}
	return nil
}

func CanTransitionDesktopSession(from, to DesktopSessionState) bool {
	allowed := map[DesktopSessionState][]DesktopSessionState{
		DesktopSessionRequested: {
			DesktopSessionOffered,
			DesktopSessionDenied,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
		DesktopSessionOffered: {
			DesktopSessionAgentReady,
			DesktopSessionDenied,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
		DesktopSessionAgentReady: {
			DesktopSessionJoining,
			DesktopSessionActive,
			DesktopSessionDenied,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
		DesktopSessionJoining: {
			DesktopSessionActive,
			DesktopSessionDenied,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
		DesktopSessionActive: {
			DesktopSessionReconnecting,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
		DesktopSessionReconnecting: {
			DesktopSessionActive,
			DesktopSessionFailed,
			DesktopSessionExpired,
			DesktopSessionTerminated,
		},
	}
	return slices.Contains(allowed[from], to)
}

func IsDesktopCommand(command string) bool {
	switch command {
	case CommandDesktopStatus,
		CommandDesktopSessionOffer,
		CommandDesktopSessionActivate,
		CommandDesktopSessionClose,
		CommandDesktopSessionSetControl,
		CommandDesktopSessionSetDisplay,
		CommandDesktopSessionSetQuality,
		CommandDesktopSessionRelayPressure:
		return true
	default:
		return false
	}
}

func IsDesktopEvent(event string) bool {
	switch event {
	case EventDesktopSessionReady,
		EventDesktopSessionConnected,
		EventDesktopSessionDisconnected,
		EventDesktopDisplayChanged,
		EventDesktopPermissionRequired,
		EventDesktopSecureDesktopEntered,
		EventDesktopSecureDesktopExited,
		EventDesktopSecureDesktopUnavailable,
		EventDesktopPermissionGranted,
		EventDesktopPermissionLost,
		EventDesktopConsoleLocked,
		EventDesktopConsoleSwitched,
		EventDesktopHelperRestarted,
		EventDesktopHelperFailed,
		EventDesktopIndicatorLost,
		EventDesktopIndicatorRestored,
		EventDesktopSessionStats,
		EventDesktopSessionError,
		EventDesktopSessionTerminated:
		return true
	case EventDesktopControlChanged,
		EventDesktopClipboard,
		EventDesktopSpecialKey:
		return true
	default:
		return false
	}
}

func (party DesktopHandshakeParty) Validate() error {
	if !desktopIDPattern.MatchString(party.IdentityID) {
		return errors.New("invalid desktop identity id")
	}
	if strings.TrimSpace(party.Certificate) == "" ||
		strings.TrimSpace(party.EphemeralPublicKey) == "" ||
		strings.TrimSpace(party.Signature) == "" {
		return errors.New("desktop handshake identity material is incomplete")
	}
	return nil
}

func (offer DesktopSessionOffer) Validate(now time.Time) error {
	if offer.Protocol != DesktopProtocolVersion || !desktopIDPattern.MatchString(offer.SessionID) {
		return errors.New("invalid desktop offer identity")
	}
	if strings.TrimSpace(offer.HomeID) == "" ||
		strings.TrimSpace(offer.AgentID) == "" ||
		strings.TrimSpace(offer.OperatorUserID) == "" ||
		strings.TrimSpace(offer.OperatorDeviceID) == "" {
		return errors.New("desktop offer scope is incomplete")
	}
	if offer.KeyEpoch == 0 ||
		strings.TrimSpace(offer.AgentJoinCredential) == "" ||
		strings.TrimSpace(offer.OperatorCertificate) == "" ||
		strings.TrimSpace(offer.OperatorCertificateFingerprint) == "" ||
		offer.TrustRootGeneration == 0 ||
		strings.TrimSpace(offer.TrustRootPublicKeySPKI) == "" ||
		strings.TrimSpace(offer.TrustRootFingerprint) == "" ||
		offer.OperatorIdentityStatus != "active" {
		return errors.New("desktop offer credential, certificate, or epoch is invalid")
	}
	if offer.OperatorIdentityCheckedAt.IsZero() || offer.OperatorIdentityCheckedAt.After(now.Add(time.Minute)) || now.Sub(offer.OperatorIdentityCheckedAt) > 2*time.Minute {
		return errors.New("desktop operator identity status is stale")
	}
	if !offer.JoinExpiresAt.After(now) || !offer.HardExpiresAt.After(offer.JoinExpiresAt) {
		return errors.New("desktop offer timestamps are invalid")
	}
	return ValidateDesktopPermissions(offer.Permissions)
}

func (ready DesktopSessionReady) Validate() error {
	if ready.Protocol != DesktopProtocolVersion || !desktopIDPattern.MatchString(ready.SessionID) {
		return errors.New("invalid desktop ready identity")
	}
	if ready.KeyEpoch == 0 ||
		strings.TrimSpace(ready.EndpointCertificate) == "" ||
		strings.TrimSpace(ready.EndpointCertificateFingerprint) == "" {
		return errors.New("desktop ready certificate or epoch is invalid")
	}
	if len(ready.Readiness) == 0 {
		return errors.New("desktop readiness is required")
	}
	return nil
}

func (event DesktopSessionLifecycleEvent) Validate() error {
	if event.Protocol != DesktopProtocolVersion || !desktopIDPattern.MatchString(event.SessionID) {
		return errors.New("invalid desktop lifecycle identity")
	}
	if event.KeyEpoch == 0 {
		return errors.New("desktop lifecycle epoch is invalid")
	}
	return nil
}
