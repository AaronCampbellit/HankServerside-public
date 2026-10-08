package cloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestDesktopJoinCookieIsSecureAndPathLimited(t *testing.T) {
	recorder := httptest.NewRecorder()
	expiresAt := time.Now().UTC().Add(60 * time.Second)
	setDesktopJoinCookie(recorder, "browser-secret", expiresAt)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != desktopJoinCookieName || cookie.Value != "browser-secret" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != desktopJoinCookiePath || cookie.MaxAge < 1 || cookie.MaxAge > 60 {
		t.Fatalf("unsafe desktop cookie: %#v", cookie)
	}
}

func TestDesktopSessionResponseContainsNoJoinCredential(t *testing.T) {
	now := time.Now().UTC()
	session := domain.DesktopSession{ID: "desk_0001", HomeID: "home_0001", AgentID: "agent_0001", OperatorUserID: "usr_0001", State: "offered", KeyEpoch: 1, RequestedAt: now, JoinExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)}
	response := desktopSessionResponse(session, domain.DesktopIdentity{Certificate: []byte("public-certificate"), Fingerprint: "public-fingerprint"})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("browser-secret"), []byte("agent-secret"), []byte("join_credential")} {
		if bytes.Contains(bytes.ToLower(encoded), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, encoded)
		}
	}
	if !bytes.Contains(encoded, []byte("/ws/desktop/browser/desk_0001")) {
		t.Fatalf("reserved websocket path missing: %s", encoded)
	}
}

func TestDesktopSessionResponseAndEventHistoryAreMetadataOnly(t *testing.T) {
	now := time.Now().UTC()
	active := now.Add(-time.Minute)
	session := domain.DesktopSession{ID: "desk_0002", HomeID: "home_0001", AgentID: "agent_0001", OperatorUserID: "usr_0001", EffectivePermissions: []string{"desktop.view"}, State: "active", KeyEpoch: 2, RequestedAt: now.Add(-2 * time.Minute), JoinExpiresAt: now, HardExpiresAt: now.Add(time.Hour), ActiveAt: &active, BrowserToAgentBytes: 10, AgentToBrowserBytes: 20}
	response := desktopSessionResponse(session, domain.DesktopIdentity{Fingerprint: "fingerprint", Capabilities: []string{"desktop.view", "desktop.secure_desktop"}})
	encoded, _ := json.Marshal(response)
	for _, forbidden := range []string{"clipboard_text", "key_code", "frame_payload", "ciphertext"} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Fatalf("response leaked %s", forbidden)
		}
	}
	readiness := response["readiness"].(map[string]any)
	if readiness["identity_trusted"] != true || readiness["secure_desktop_supported"] != true {
		t.Fatalf("readiness = %#v", readiness)
	}
	event := desktopSessionEventResponse(domain.DesktopSessionEvent{SessionID: session.ID, Sequence: 3, EventType: "desktop.session.connected", ActorType: "agent", ActorID: "agent_0001", OccurredAt: now, ReasonCode: "", MetadataJSON: `{"epoch":"2","clipboard_text":"secret","key":"KeyA"}`})
	eventJSON, _ := json.Marshal(event)
	if bytes.Contains(eventJSON, []byte("secret")) || bytes.Contains(eventJSON, []byte("KeyA")) {
		t.Fatalf("event leaked content: %s", eventJSON)
	}
}

func TestDesktopReadinessUsesReportedEndpointStateWithoutCapabilityInference(t *testing.T) {
	checks := map[string]string{"service": "unknown", "daemon": "unknown", "host": "unknown", "indicator": "unknown", "capture": "unknown", "control": "unknown"}
	seen := map[string]bool{}
	applyDesktopReadinessEvent(checks, seen, "desktop.session.ready", map[string]string{"service": "ready", "daemon": "ready", "host": "ready", "indicator": "ready", "capture": "required", "control": "ready"})
	if checks["capture"] != "required" || checks["control"] != "ready" || checks["service"] != "ready" {
		t.Fatalf("checks = %#v", checks)
	}
	applyDesktopReadinessEvent(checks, seen, "desktop.permission.granted", map[string]string{"permission": "screen_recording"})
	if checks["capture"] != "required" {
		t.Fatalf("older event overwrote latest endpoint report: %#v", checks)
	}
	if normalizeDesktopPlatform("Windows 11") != "windows" || normalizeDesktopPlatform("Darwin") != "macos" || normalizeDesktopPlatform("linux") != "unknown" {
		t.Fatal("platform normalization is not bounded")
	}
	synthetic := map[string]string{}
	applyDesktopReadinessEvent(synthetic, map[string]bool{}, "desktop.session.ready", map[string]string{"capture": "synthetic"})
	if synthetic["capture"] != "ready" {
		t.Fatalf("synthetic capture is available and should be ready: %#v", synthetic)
	}
}
