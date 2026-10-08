package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestDesktopWebSocketPairsAuthenticatedSidesAndForwardsOpaqueBinary(t *testing.T) {
	auth := newFakeDesktopRelayAuthorizer()
	claim := desktopRelayJoinClaim{SessionID: "desk_ws_0001", HomeID: "home_ws_0001", KeyEpoch: 1, AgentID: "agent_ws_0001", HardExpiresAt: time.Now().Add(time.Hour)}
	auth.allow("browser", "browser-secret", claim)
	auth.allow("agent", "agent-secret", claim)
	server := testDesktopWebSocketServer(auth)
	defer server.Close()

	browserHeaders := http.Header{"Origin": []string{server.URL}, "Cookie": []string{desktopJoinCookieName + "=browser-secret"}}
	browser, _, err := websocket.Dial(context.Background(), websocketURL(server.URL, "/ws/desktop/browser/desk_ws_0001"), &websocket.DialOptions{HTTPHeader: browserHeaders})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close(websocket.StatusNormalClosure, "test complete")
	agentHeaders := http.Header{"Authorization": []string{"Bearer agent-secret"}, "X-Hank-Agent-ID": []string{"agent_ws_0001"}}
	agent, _, err := websocket.Dial(context.Background(), websocketURL(server.URL, "/ws/desktop/agent/desk_ws_0001"), &websocket.DialOptions{HTTPHeader: agentHeaders})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(websocket.StatusNormalClosure, "test complete")

	payload := []byte("opaque ciphertext with private marker")
	if err := browser.Write(context.Background(), websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	kind, got, err := agent.Read(context.Background())
	if err != nil || kind != websocket.MessageBinary || string(got) != string(payload) {
		t.Fatalf("forwarded %d %q: %v", kind, got, err)
	}
}

func TestDesktopWebSocketRejectsWrongOriginAndCredentialReuse(t *testing.T) {
	auth := newFakeDesktopRelayAuthorizer()
	claim := desktopRelayJoinClaim{SessionID: "desk_ws_0002", HomeID: "home_ws_0001", KeyEpoch: 1, AgentID: "agent_ws_0001", HardExpiresAt: time.Now().Add(time.Hour)}
	auth.allow("browser", "one-use", claim)
	server := testDesktopWebSocketServer(auth)
	defer server.Close()
	path := websocketURL(server.URL, "/ws/desktop/browser/desk_ws_0002")
	bad := http.Header{"Origin": []string{"https://evil.example"}, "Cookie": []string{desktopJoinCookieName + "=one-use"}}
	if _, response, err := websocket.Dial(context.Background(), path, &websocket.DialOptions{HTTPHeader: bad}); err == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong origin response = %#v, %v", response, err)
	}
	good := http.Header{"Origin": []string{server.URL}, "Cookie": []string{desktopJoinCookieName + "=one-use"}}
	connection, _, err := websocket.Dial(context.Background(), path, &websocket.DialOptions{HTTPHeader: good})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "test complete")
	if _, response, err := websocket.Dial(context.Background(), path, &websocket.DialOptions{HTTPHeader: good}); err == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused response = %#v, %v", response, err)
	}
}

func TestDesktopRelayStoreAuthorizationBindsSideSessionAgentAndEpoch(t *testing.T) {
	now := time.Now().UTC()
	reconnectExpiresAt := now.Add(90 * time.Second)
	backend := &fakeDesktopCredentialStore{session: domain.DesktopSession{ID: "desk_auth_1", HomeID: "home_auth_1", AgentID: "agent_auth_1", State: "reconnecting", KeyEpoch: 2, HardExpiresAt: now.Add(time.Hour), ReconnectExpiresAt: &reconnectExpiresAt}, token: "epoch-two", side: "agent"}
	authorizer := storeDesktopRelayAuthorizer{store: backend}
	claim, err := authorizer.PrepareDesktopRelay(context.Background(), desktopRelayAgent, "desk_auth_1", "epoch-two", "agent_auth_1", now)
	if err != nil || claim.KeyEpoch != 2 || claim.Side != desktopRelayAgent {
		t.Fatalf("claim = %#v, %v", claim, err)
	}
	if err := authorizer.ConsumeDesktopRelayCredential(context.Background(), claim, "epoch-two", now); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := authorizer.ConsumeDesktopRelayCredential(context.Background(), claim, "epoch-two", now); !errors.Is(err, errDesktopRelayUnauthorized) {
		t.Fatalf("reused = %v", err)
	}
	backend.consumed = false
	if _, err := authorizer.PrepareDesktopRelay(context.Background(), desktopRelayAgent, "desk_auth_1", "epoch-two", "wrong-agent", now); !errors.Is(err, errDesktopRelayUnauthorized) {
		t.Fatalf("wrong agent = %v", err)
	}
}

func TestDesktopRelayStoreAuthorizationRejectsBothSidesBeforeOfferIsPersisted(t *testing.T) {
	now := time.Now().UTC()
	backend := &fakeDesktopCredentialStore{session: domain.DesktopSession{ID: "desk_requested_1", AgentID: "agent_requested_1", State: "requested", KeyEpoch: 1, HardExpiresAt: now.Add(time.Hour)}, token: "agent-credential", side: "agent"}
	authorizer := storeDesktopRelayAuthorizer{store: backend}
	if _, err := authorizer.PrepareDesktopRelay(context.Background(), desktopRelayAgent, "desk_requested_1", "agent-credential", "agent_requested_1", now); !errors.Is(err, errDesktopRelayUnauthorized) {
		t.Fatalf("agent requested-state join = %v", err)
	}
	backend.consumed = false
	backend.token = "browser-credential"
	backend.side = "browser"
	if _, err := authorizer.PrepareDesktopRelay(context.Background(), desktopRelayBrowser, "desk_requested_1", "browser-credential", "", now); !errors.Is(err, errDesktopRelayUnauthorized) {
		t.Fatalf("browser requested-state join = %v", err)
	}
}

func TestDesktopRelayCapacityRejectsBeforeOneTimeCredentialConsumption(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.MaxSessions = 1
	limits.JoinTimeout = time.Hour
	relay := newInProcessDesktopRelay(limits, nil)
	existing := desktopRelayJoinClaim{SessionID: "desk_existing", HomeID: "home_existing", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_existing", HardExpiresAt: time.Now().Add(time.Hour)}
	if err := relay.Reserve(existing); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	backend := &fakeDesktopCredentialStore{session: domain.DesktopSession{ID: "desk_capacity", HomeID: "home_capacity", AgentID: "agent_capacity", State: "offered", KeyEpoch: 1, HardExpiresAt: now.Add(time.Hour)}, token: "capacity-token", side: "agent"}
	server := &Server{desktopRelay: relay, desktopRelayAuth: storeDesktopRelayAuthorizer{store: backend}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/desktop/agent/", server.handleDesktopAgentWebSocket)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	headers := http.Header{"Authorization": []string{"Bearer capacity-token"}, "X-Hank-Agent-ID": []string{"agent_capacity"}}
	if _, response, err := websocket.Dial(context.Background(), websocketURL(httpServer.URL, "/ws/desktop/agent/desk_capacity"), &websocket.DialOptions{HTTPHeader: headers}); err == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("capacity response = %#v, %v", response, err)
	}
	if backend.consumed {
		t.Fatal("capacity rejection consumed the one-time credential")
	}
}

func TestDesktopRelayPressureCommandIsAuthenticatedScopedMetadataOnly(t *testing.T) {
	session := domain.DesktopSession{ID: "desk_pressure", HomeID: "home_pressure", AgentID: "agent_pressure", State: "active", KeyEpoch: 3}
	envelope, err := desktopRelayPressureEnvelope(session, 3, "agent_to_browser")
	if err != nil {
		t.Fatal(err)
	}
	var routed protocol.RoutedCommand
	if err := json.Unmarshal(envelope.Payload, &routed); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(routed.Body, &body); err != nil {
		t.Fatal(err)
	}
	if envelope.AgentID != session.AgentID || envelope.HomeID != session.HomeID || routed.Command != protocol.CommandDesktopSessionRelayPressure ||
		body["session_id"] != session.ID || body["direction"] != "agent_to_browser" || body["key_epoch"] != float64(3) || body["count"] != float64(1) {
		t.Fatalf("pressure envelope = %#v %#v", envelope, body)
	}
	encoded, _ := json.Marshal(envelope)
	for _, forbidden := range [][]byte{[]byte("frame_payload"), []byte("ciphertext"), []byte("clipboard"), []byte("key_code")} {
		if bytes.Contains(bytes.ToLower(encoded), forbidden) {
			t.Fatalf("pressure envelope leaked %q: %s", forbidden, encoded)
		}
	}
	for _, candidate := range []struct {
		epoch     uint32
		direction string
	}{{2, "agent_to_browser"}, {3, "unknown"}} {
		if _, err := desktopRelayPressureEnvelope(session, candidate.epoch, candidate.direction); err == nil {
			t.Fatalf("invalid pressure scope accepted: %#v", candidate)
		}
	}
}

func testDesktopWebSocketServer(authorizer desktopRelayAuthorizer) *httptest.Server {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout = time.Second
	server := &Server{desktopRelay: newInProcessDesktopRelay(limits, nil), desktopRelayAuth: authorizer, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/desktop/browser/", server.handleDesktopBrowserWebSocket)
	mux.HandleFunc("/ws/desktop/agent/", server.handleDesktopAgentWebSocket)
	return httptest.NewServer(mux)
}

func websocketURL(base, path string) string { return "ws" + strings.TrimPrefix(base, "http") + path }

type fakeDesktopRelayAuthorizer struct {
	mu     sync.Mutex
	claims map[string]desktopRelayJoinClaim
}

func newFakeDesktopRelayAuthorizer() *fakeDesktopRelayAuthorizer {
	return &fakeDesktopRelayAuthorizer{claims: make(map[string]desktopRelayJoinClaim)}
}
func (value *fakeDesktopRelayAuthorizer) allow(side, token string, claim desktopRelayJoinClaim) {
	value.mu.Lock()
	defer value.mu.Unlock()
	claim.Side = desktopRelaySide(side)
	value.claims[side+":"+token] = claim
}
func (value *fakeDesktopRelayAuthorizer) PrepareDesktopRelay(_ context.Context, side desktopRelaySide, sessionID, credential, agentID string, _ time.Time) (desktopRelayJoinClaim, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	key := string(side) + ":" + credential
	claim, ok := value.claims[key]
	if !ok || claim.SessionID != sessionID || (side == desktopRelayAgent && claim.AgentID != agentID) {
		return desktopRelayJoinClaim{}, errDesktopRelayUnauthorized
	}
	return claim, nil
}
func (value *fakeDesktopRelayAuthorizer) ConsumeDesktopRelayCredential(_ context.Context, claim desktopRelayJoinClaim, credential string, _ time.Time) error {
	value.mu.Lock()
	defer value.mu.Unlock()
	key := string(claim.Side) + ":" + credential
	stored, ok := value.claims[key]
	if !ok || stored.SessionID != claim.SessionID || stored.KeyEpoch != claim.KeyEpoch {
		return errDesktopRelayUnauthorized
	}
	delete(value.claims, key)
	return nil
}

type fakeDesktopCredentialStore struct {
	session     domain.DesktopSession
	token, side string
	consumed    bool
}

func (value *fakeDesktopCredentialStore) GetDesktopSession(context.Context, string) (domain.DesktopSession, error) {
	return value.session, nil
}
func (value *fakeDesktopCredentialStore) ConsumeDesktopJoinCredential(_ context.Context, hash []byte, side, sessionID string, epoch uint32, _ time.Time) (domain.DesktopJoinCredential, error) {
	if value.consumed || side != value.side || sessionID != value.session.ID || epoch != value.session.KeyEpoch || !bytes.Equal(hash, desktopCredentialHash(value.token)) {
		return domain.DesktopJoinCredential{}, store.ErrNotFound
	}
	value.consumed = true
	return domain.DesktopJoinCredential{SessionID: sessionID, Side: side, KeyEpoch: epoch}, nil
}
