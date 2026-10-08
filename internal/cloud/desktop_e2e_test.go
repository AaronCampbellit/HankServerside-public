package cloud

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const (
	syntheticScreenSecret    = "synthetic-screen-secret"
	syntheticKeystrokeSecret = "synthetic-keystroke-secret"
	syntheticClipboardSecret = "synthetic-clipboard-secret"
)

func TestDesktopSyntheticProcessHarness(t *testing.T) {
	environment := newSyntheticDesktopEnvironment(t)
	defer environment.close()
	first := environment.join(t, 1, "browser-join-epoch-1", "agent-join-epoch-1")

	keyboard := mustInner(t, protocol.DesktopMessageKeyboard, []byte(`{"code":"KeyS","key":"`+syntheticKeystrokeSecret+`","down":true}`))
	clipboard := mustInner(t, protocol.DesktopMessageClipboardText, []byte(`{"text":"`+syntheticClipboardSecret+`"}`))
	first.browserSend(t, keyboard)
	if got := first.agentReceive(t); !bytes.Equal(got, keyboard) {
		t.Fatalf("keyboard plaintext mismatch")
	}
	first.browserSend(t, clipboard)
	if got := first.agentReceive(t); !bytes.Equal(got, clipboard) {
		t.Fatalf("clipboard plaintext mismatch")
	}

	codec := mustInner(t, protocol.DesktopMessageCodecConfig, []byte(`{"codec":"avc1.64001f","width":640,"height":360}`))
	video := mustInner(t, protocol.DesktopMessageVideoAccessUnit, []byte(syntheticScreenSecret))
	stats := mustInner(t, protocol.DesktopMessageStatistics, []byte(`{"frames":60,"bytes":94095,"rtt_ms":4}`))
	for _, message := range [][]byte{codec, video, stats} {
		first.agentSend(t, message)
	}
	for _, want := range [][]byte{codec, video, stats} {
		if got := first.browserReceive(t); !bytes.Equal(got, want) {
			t.Fatalf("agent plaintext mismatch")
		}
	}

	firstCiphertext := first.browserCiphertext(t, keyboard)
	firstBrowserKey := append([]byte(nil), first.browserEphemeral...)
	environment.relay.Revoke(environment.sessionID, "transport_reconnect")
	first.waitClosed(t)
	second := environment.join(t, 2, "browser-join-epoch-2", "agent-join-epoch-2")
	secondCiphertext := second.browserCiphertext(t, keyboard)
	if bytes.Equal(firstBrowserKey, second.browserEphemeral) || bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("reconnect reused ephemeral key or encrypted record")
	}
	if binary.BigEndian.Uint32(secondCiphertext[14:18]) == 0 || binary.BigEndian.Uint64(secondCiphertext[6:14]) != 0 {
		t.Fatal("reconnect did not reset the outer record sequence")
	}
	second.browserSendFrame(t, secondCiphertext)
	_ = second.agentReceive(t)

	snapshotJSON, _ := json.Marshal(environment.relay.Snapshot(environment.sessionID))
	eventsJSON, _ := json.Marshal(environment.eventSnapshot())
	controlPlane := []byte(`{"session_id":"` + environment.sessionID + `","key_epoch":2,"state":"active"}`)
	persistedRows := []byte(`{"session_id":"` + environment.sessionID + `","bytes_browser_to_agent":1,"bytes_agent_to_browser":1}`)
	for label, value := range map[string][]byte{"relay snapshot": snapshotJSON, "audit events": eventsJSON, "logs": environment.logs.Bytes(), "HTTP/control response": controlPlane, "SQL metadata rows": persistedRows} {
		for _, marker := range [][]byte{[]byte(syntheticScreenSecret), []byte(syntheticKeystrokeSecret), []byte(syntheticClipboardSecret)} {
			if bytes.Contains(value, marker) {
				t.Fatalf("%s retained plaintext marker %q", label, marker)
			}
		}
	}
	if !bytes.Contains(video, []byte(syntheticScreenSecret)) || !bytes.Contains(keyboard, []byte(syntheticKeystrokeSecret)) || !bytes.Contains(clipboard, []byte(syntheticClipboardSecret)) {
		t.Fatal("synthetic clients did not recover all private markers")
	}

	environment.relay.Revoke(environment.sessionID, "browser_terminated")
	second.waitClosed(t)
}

func TestDesktopSyntheticTerminationActorsCloseBothSockets(t *testing.T) {
	for _, actor := range []string{"browser", "server", "endpoint"} {
		t.Run(actor, func(t *testing.T) {
			environment := newSyntheticDesktopEnvironment(t)
			defer environment.close()
			pair := environment.join(t, 1, "browser-token-"+actor, "agent-token-"+actor)
			switch actor {
			case "browser":
				_ = pair.browser.Close(websocket.StatusNormalClosure, "browser terminated")
			case "server":
				environment.relay.Revoke(environment.sessionID, "server terminated")
			case "endpoint":
				_ = pair.agent.Close(websocket.StatusNormalClosure, "endpoint terminated")
			}
			pair.waitClosed(t)
		})
	}
}

type syntheticDesktopEnvironment struct {
	t         *testing.T
	sessionID string
	homeID    string
	agentID   string
	auth      *fakeDesktopRelayAuthorizer
	relay     desktopRelay
	server    *httptest.Server
	logs      lockedBuffer
	eventsMu  sync.Mutex
	events    []desktopRelayLifecycleEvent
}

func newSyntheticDesktopEnvironment(t *testing.T) *syntheticDesktopEnvironment {
	t.Helper()
	value := &syntheticDesktopEnvironment{t: t, sessionID: "desk_synthetic_e2e", homeID: "home_synthetic", agentID: "agent_synthetic_e2e", auth: newFakeDesktopRelayAuthorizer()}
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout = time.Second, 2*time.Second
	value.relay = newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) {
		value.eventsMu.Lock()
		value.events = append(value.events, event)
		value.eventsMu.Unlock()
	})
	server := &Server{desktopRelay: value.relay, desktopRelayAuth: value.auth, logger: slog.New(slog.NewTextHandler(&value.logs, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/desktop/browser/", server.handleDesktopBrowserWebSocket)
	mux.HandleFunc("/ws/desktop/agent/", server.handleDesktopAgentWebSocket)
	value.server = httptest.NewServer(mux)
	return value
}

func (value *syntheticDesktopEnvironment) close() {
	value.relay.Revoke(value.sessionID, "test_complete")
	value.server.Close()
}
func (value *syntheticDesktopEnvironment) eventSnapshot() []desktopRelayLifecycleEvent {
	value.eventsMu.Lock()
	defer value.eventsMu.Unlock()
	return append([]desktopRelayLifecycleEvent(nil), value.events...)
}

func (value *syntheticDesktopEnvironment) join(t *testing.T, epoch uint32, browserToken, agentToken string) *syntheticPair {
	t.Helper()
	claim := desktopRelayJoinClaim{SessionID: value.sessionID, HomeID: value.homeID, KeyEpoch: epoch, AgentID: value.agentID, HardExpiresAt: time.Now().Add(time.Hour)}
	value.auth.allow("browser", browserToken, claim)
	value.auth.allow("agent", agentToken, claim)
	browserHeader := http.Header{"Origin": []string{value.server.URL}, "Cookie": []string{desktopJoinCookieName + "=" + browserToken}}
	browser, _, err := websocket.Dial(context.Background(), websocketURL(value.server.URL, "/ws/desktop/browser/"+value.sessionID), &websocket.DialOptions{HTTPHeader: browserHeader})
	if err != nil {
		t.Fatal(err)
	}
	agentHeader := http.Header{"Authorization": []string{"Bearer " + agentToken}, "X-Hank-Agent-ID": []string{value.agentID}}
	agent, _, err := websocket.Dial(context.Background(), websocketURL(value.server.URL, "/ws/desktop/agent/"+value.sessionID), &websocket.DialOptions{HTTPHeader: agentHeader})
	if err != nil {
		t.Fatal(err)
	}

	operatorKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	endpointIdentity, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	browserPrivate, _ := ecdh.P256().GenerateKey(rand.Reader)
	transcript, err := desktopcrypto.EncodeHandshakeTranscript(desktopcrypto.HandshakeTranscript{
		HomeID: "home_synthetic", SessionID: value.sessionID, AgentID: value.agentID, OperatorUserID: "user_synthetic",
		OperatorDeviceID: "device_synthetic", Permissions: []string{"desktop.view", "desktop.control", "desktop.clipboard.write"},
		BrowserEphemeralPublicKey: browserPrivate.PublicKey().Bytes(), JoinExpiresAtUnixMS: time.Now().Add(time.Minute).UnixMilli(), HardExpiresAtUnixMS: claim.HardExpiresAt.UnixMilli(), KeyEpoch: epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(transcript)
	operatorSignature, _ := ecdsa.SignASN1(rand.Reader, operatorKey, digest[:])
	operatorSPKI, _ := x509.MarshalPKIXPublicKey(&operatorKey.PublicKey)
	browserHandshake, _ := protocol.EncodeDesktopHandshake(protocol.DesktopBrowserHandshake{TranscriptBase64URL: base64.RawURLEncoding.EncodeToString(transcript), BrowserEphemeralPublicKeyBase64URL: base64.RawURLEncoding.EncodeToString(browserPrivate.PublicKey().Bytes()), OperatorSignatureBase64URL: base64.RawURLEncoding.EncodeToString(operatorSignature)})
	writeDesktopFrame(t, browser, protocol.DesktopFrameBrowserHandshake, browserHandshake)

	gotBrowserHandshake := readDesktopFrame(t, agent, protocol.DesktopFrameBrowserHandshake)
	var decodedBrowser protocol.DesktopBrowserHandshake
	if err := json.Unmarshal(gotBrowserHandshake, &decodedBrowser); err != nil {
		t.Fatal(err)
	}
	decodedTranscript, _ := base64.RawURLEncoding.DecodeString(decodedBrowser.TranscriptBase64URL)
	decodedSignature, _ := base64.RawURLEncoding.DecodeString(decodedBrowser.OperatorSignatureBase64URL)
	publicAny, _ := x509.ParsePKIXPublicKey(operatorSPKI)
	if !ecdsa.VerifyASN1(publicAny.(*ecdsa.PublicKey), sha256Bytes(decodedTranscript), decodedSignature) {
		t.Fatal("operator handshake signature rejected")
	}
	endpointPrivate, _ := ecdh.P256().GenerateKey(rand.Reader)
	sharedAgent, _ := endpointPrivate.ECDH(browserPrivate.PublicKey())
	signedEndpoint := append(append([]byte(nil), transcript...), endpointPrivate.PublicKey().Bytes()...)
	endpointSignature, _ := ecdsa.SignASN1(rand.Reader, endpointIdentity, sha256Bytes(signedEndpoint))
	agentHandshake, _ := protocol.EncodeDesktopHandshake(protocol.DesktopAgentHandshake{EndpointEphemeralPublicKeyBase64URL: base64.RawURLEncoding.EncodeToString(endpointPrivate.PublicKey().Bytes()), EndpointHandshakeSignatureBase64URL: base64.RawURLEncoding.EncodeToString(endpointSignature)})
	writeDesktopFrame(t, agent, protocol.DesktopFrameAgentHandshake, agentHandshake)

	gotAgentHandshake := readDesktopFrame(t, browser, protocol.DesktopFrameAgentHandshake)
	var decodedAgent protocol.DesktopAgentHandshake
	if err := json.Unmarshal(gotAgentHandshake, &decodedAgent); err != nil {
		t.Fatal(err)
	}
	endpointPublicBytes, _ := base64.RawURLEncoding.DecodeString(decodedAgent.EndpointEphemeralPublicKeyBase64URL)
	decodedEndpointSignature, _ := base64.RawURLEncoding.DecodeString(decodedAgent.EndpointHandshakeSignatureBase64URL)
	if !ecdsa.VerifyASN1(&endpointIdentity.PublicKey, sha256Bytes(append(append([]byte(nil), transcript...), endpointPublicBytes...)), decodedEndpointSignature) {
		t.Fatal("endpoint handshake signature rejected")
	}
	endpointPublic, _ := ecdh.P256().NewPublicKey(endpointPublicBytes)
	sharedBrowser, _ := browserPrivate.ECDH(endpointPublic)
	if !bytes.Equal(sharedBrowser, sharedAgent) {
		t.Fatal("ECDH secrets differ")
	}
	keys, err := desktopcrypto.DeriveDirectionalKeys(sharedBrowser, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return &syntheticPair{browser: browser, agent: agent, epoch: epoch, keys: keys, browserEphemeral: browserPrivate.PublicKey().Bytes()}
}

type syntheticPair struct {
	browser, agent                                                                       *websocket.Conn
	epoch                                                                                uint32
	keys                                                                                 desktopcrypto.DirectionalKeys
	browserSendSequence, browserReceiveSequence, agentSendSequence, agentReceiveSequence uint64
	browserEphemeral                                                                     []byte
}

func (pair *syntheticPair) browserCiphertext(t *testing.T, plaintext []byte) []byte {
	return encryptSyntheticRecord(t, desktopcrypto.DirectionBrowserToAgent, pair.epoch, pair.browserSendSequence, pair.keys.BrowserToAgent[:], pair.keys.BrowserNoncePrefix, plaintext)
}
func (pair *syntheticPair) browserSend(t *testing.T, plaintext []byte) {
	frame := pair.browserCiphertext(t, plaintext)
	pair.browserSendFrame(t, frame)
}
func (pair *syntheticPair) browserSendFrame(t *testing.T, record []byte) {
	writeDesktopFrame(t, pair.browser, protocol.DesktopFrameEncryptedRecord, record)
	pair.browserSendSequence++
}
func (pair *syntheticPair) agentReceive(t *testing.T) []byte {
	record := readDesktopFrame(t, pair.agent, protocol.DesktopFrameEncryptedRecord)
	value := decryptSyntheticRecord(t, desktopcrypto.DirectionBrowserToAgent, pair.epoch, pair.agentReceiveSequence, pair.keys.BrowserToAgent[:], pair.keys.BrowserNoncePrefix, record)
	pair.agentReceiveSequence++
	return value
}
func (pair *syntheticPair) agentSend(t *testing.T, plaintext []byte) {
	record := encryptSyntheticRecord(t, desktopcrypto.DirectionAgentToBrowser, pair.epoch, pair.agentSendSequence, pair.keys.AgentToBrowser[:], pair.keys.AgentNoncePrefix, plaintext)
	writeDesktopFrame(t, pair.agent, protocol.DesktopFrameEncryptedRecord, record)
	pair.agentSendSequence++
}
func (pair *syntheticPair) browserReceive(t *testing.T) []byte {
	record := readDesktopFrame(t, pair.browser, protocol.DesktopFrameEncryptedRecord)
	value := decryptSyntheticRecord(t, desktopcrypto.DirectionAgentToBrowser, pair.epoch, pair.browserReceiveSequence, pair.keys.AgentToBrowser[:], pair.keys.AgentNoncePrefix, record)
	pair.browserReceiveSequence++
	return value
}
func (pair *syntheticPair) waitClosed(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, browserErr := pair.browser.Read(ctx)
	_, _, agentErr := pair.agent.Read(ctx)
	if browserErr == nil || agentErr == nil {
		t.Fatalf("termination left a socket open: browser=%v agent=%v", browserErr, agentErr)
	}
}

func encryptSyntheticRecord(t *testing.T, direction byte, epoch uint32, sequence uint64, key []byte, prefix [4]byte, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, _ := cipher.NewGCM(block)
	header, err := (desktopcrypto.RecordHeader{Version: desktopcrypto.RecordVersion, Direction: direction, KeyEpoch: epoch, Sequence: sequence, CiphertextLength: uint32(len(plaintext) + aead.Overhead())}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	nonce := desktopcrypto.Nonce(prefix, sequence)
	return append(header, aead.Seal(nil, nonce[:], plaintext, header)...)
}
func decryptSyntheticRecord(t *testing.T, direction byte, epoch uint32, sequence uint64, key []byte, prefix [4]byte, record []byte) []byte {
	t.Helper()
	if len(record) < 34 || record[1] != direction || binary.BigEndian.Uint32(record[2:6]) != epoch || binary.BigEndian.Uint64(record[6:14]) != sequence || int(binary.BigEndian.Uint32(record[14:18])) != len(record)-18 {
		t.Fatal("invalid encrypted record header")
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := desktopcrypto.Nonce(prefix, sequence)
	plaintext, err := aead.Open(nil, nonce[:], record[18:], record[:18])
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}
func mustInner(t *testing.T, kind uint16, payload []byte) []byte {
	t.Helper()
	value, err := protocol.EncodeDesktopInnerMessage(kind, 0, payload)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func writeDesktopFrame(t *testing.T, connection *websocket.Conn, kind byte, payload []byte) {
	t.Helper()
	frame, err := protocol.EncodeDesktopDataFrame(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
}
func readDesktopFrame(t *testing.T, connection *websocket.Conn, want byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	kind, value, err := connection.Read(ctx)
	if err != nil || kind != websocket.MessageBinary {
		t.Fatalf("read desktop frame: kind=%d err=%v", kind, err)
	}
	frame, err := protocol.DecodeDesktopDataFrame(value)
	if err != nil || frame.Kind != want {
		t.Fatalf("decode desktop frame: kind=%d err=%v", frame.Kind, err)
	}
	return frame.Payload
}
func sha256Bytes(value []byte) []byte { digest := sha256.Sum256(value); return digest[:] }

type lockedBuffer struct {
	mu    sync.Mutex
	value bytes.Buffer
}

func (value *lockedBuffer) Write(data []byte) (int, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	return value.value.Write(data)
}
func (value *lockedBuffer) Bytes() []byte {
	value.mu.Lock()
	defer value.mu.Unlock()
	return append([]byte(nil), value.value.Bytes()...)
}
