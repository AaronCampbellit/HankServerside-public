package cloud

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func replyTestTarget() agentReplyBinding {
	return agentReplyBinding{homeID: "home", agentID: "assigned-agent", peer: &wsPeer{}}
}

func rejectedReplies(target agentReplyBinding, envelope protocol.Envelope) []struct {
	name     string
	sender   agentReplyBinding
	envelope protocol.Envelope
} {
	type reply = struct {
		name     string
		sender   agentReplyBinding
		envelope protocol.Envelope
	}
	foreignHome, foreignAgent, replacement := target, target, target
	foreignHome.homeID = "other-home"
	foreignAgent.agentID = "other-agent"
	replacement.peer = &wsPeer{}
	claimedHome, claimedAgent := envelope, envelope
	claimedHome.HomeID = "other-home"
	claimedAgent.AgentID = "other-agent"
	return []reply{
		{"foreign home", foreignHome, envelope}, {"foreign agent", foreignAgent, envelope},
		{"replacement socket", replacement, envelope}, {"missing identity", agentReplyBinding{}, envelope},
		{"forged home claim", target, claimedHome}, {"forged agent claim", target, claimedAgent},
	}
}

func TestAgentReplyBindingSynchronousResponses(t *testing.T) {
	for _, withError := range []bool{false, true} {
		target := replyTestTarget()
		s := &Server{agentRequests: newAgentRequestRegistry(), router: NewRouter()}
		ch, err := s.agentRequests.Register("known-request", target)
		must(t, err)
		envelope := protocol.Envelope{RequestID: "known-request", HomeID: target.homeID, AgentID: target.agentID}
		if withError {
			envelope.Error = &protocol.ErrorPayload{Code: "command_failed", Message: "expected failure"}
		}
		for _, tc := range rejectedReplies(target, envelope) {
			s.handleAgentResponse(context.Background(), tc.sender, tc.envelope)
			select {
			case <-ch:
				t.Fatalf("%s consumed the pending request", tc.name)
			default:
			}
		}
		// Optional wire IDs are filled from authenticated identity, including errors.
		envelope.HomeID, envelope.AgentID = "", ""
		s.handleAgentResponse(context.Background(), target, envelope)
		select {
		case got := <-ch:
			if got.HomeID != target.homeID || got.AgentID != target.agentID || (got.Error != nil) != withError {
				t.Fatalf("unexpected response: %+v", got)
			}
		default:
			t.Fatal("assigned agent's response was lost")
		}
		if s.agentRequests.Resolve(target, envelope) {
			t.Fatal("duplicate response accepted")
		}
	}
}

func TestAgentReplyBindingAppPending(t *testing.T) {
	router := NewRouter()
	app := router.RegisterApp("session", "user", nil)
	target := replyTestTarget()
	_, err := router.AddPending(context.Background(), "client-request-id", target.homeID, protocol.CommandSystemPing, "", "", target, app, time.Minute, func(context.Context, *pendingRequest) { t.Error("unexpected timeout") })
	must(t, err)
	defer router.ResolvePending("client-request-id")
	s := &Server{agentRequests: newAgentRequestRegistry(), router: router}
	envelope := protocol.Envelope{RequestID: "client-request-id", HomeID: target.homeID, AgentID: target.agentID, Error: &protocol.ErrorPayload{Code: "forged_error"}}
	for _, tc := range rejectedReplies(target, envelope) {
		s.handleAgentResponse(context.Background(), tc.sender, tc.envelope)
		if router.PendingCount() != 1 || app.inFlight != 1 {
			t.Fatalf("%s consumed app request or released its slot", tc.name)
		}
	}
	router.UnregisterApp(app.connectionID)
	envelope.HomeID, envelope.AgentID = "", ""
	if pending, ok := router.ResolveAgentPending(target, envelope); !ok || pending.app != app {
		t.Fatal("assigned reply lost after app disconnect")
	}
	if router.PendingCount() != 0 || app.inFlight != 0 {
		t.Fatal("accepted reply did not release pending state")
	}
}

func TestAgentReplyBindingTransferFrames(t *testing.T) {
	cases := []struct {
		name      string
		handler   func(*Server, agentReplyBinding, protocol.Envelope)
		payload   string
		withError bool
	}{
		{"ready", (*Server).handleTransferReady, `{"operation":"download","offset":0,"size":4,"flow_control":"ack.v1","window_bytes":262144}`, false},
		{"ready malformed", (*Server).handleTransferReady, `[]`, false},
		{"ready error", (*Server).handleTransferReady, `{}`, true},
		{"data", (*Server).handleTransferData, `{"offset":0,"content_base64":"aGFuaw=="}`, false},
		{"data malformed", (*Server).handleTransferData, `[]`, false},
		{"data bad base64", (*Server).handleTransferData, `{"content_base64":"!"}`, false},
		{"complete", (*Server).handleTransferComplete, `{"operation":"download","offset":4,"size":4}`, false},
		{"complete malformed", (*Server).handleTransferComplete, `[]`, false},
		{"complete error", (*Server).handleTransferComplete, `{}`, true},
		{"error", (*Server).handleTransferError, `{}`, true},
		{"error missing payload", (*Server).handleTransferError, `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{transfers: newTransferRegistry()}
			target := replyTestTarget()
			session, _ := s.transfers.Create(target.homeID, target.agentID, "job", protocol.FileTransferOperationDownload, "source", "file", time.Minute)
			attempt, err := s.transfers.BeginAttempt(session, 0, target)
			must(t, err)
			if tc.name != "ready" {
				attempt.negotiated = true
				attempt.expectedEnd = 4
				if tc.name == "complete" {
					attempt.received = 4
				}
			}
			envelope := protocol.Envelope{RequestID: attempt.ID, Payload: json.RawMessage(tc.payload), HomeID: target.homeID, AgentID: target.agentID}
			if tc.withError {
				envelope.Error = &protocol.ErrorPayload{Code: "transfer_failed"}
			}
			for _, bad := range rejectedReplies(target, envelope) {
				tc.handler(s, bad.sender, bad.envelope)
				if len(attempt.ReadyCh)+len(attempt.DataCh)+len(attempt.CompleteCh) != 0 {
					t.Fatalf("%s injected a transfer frame", bad.name)
				}
				select {
				case <-attempt.done:
					t.Fatalf("%s aborted another agent transfer", bad.name)
				default:
				}
			}
			envelope.HomeID, envelope.AgentID = "", ""
			tc.handler(s, target, envelope)
			if len(attempt.ReadyCh)+len(attempt.DataCh)+len(attempt.CompleteCh) == 0 {
				select {
				case <-attempt.done:
					if attempt.failureResult() == nil {
						t.Fatal("missing protocol failure")
					}
				default:
					t.Fatal("assigned frame was lost")
				}
			}
			s.transfers.EndAttempt(attempt.ID)
			resumed := target
			resumed.peer = &wsPeer{}
			next, err := s.transfers.BeginAttempt(session, 0, resumed)
			must(t, err)
			defer s.transfers.EndAttempt(next.ID)
			if tc.name != "ready" {
				next.negotiated = true
				next.expectedEnd = 4
				if tc.name == "complete" {
					next.received = 4
				}
			}
			envelope.RequestID = next.ID
			tc.handler(s, target, envelope)
			if len(next.ReadyCh)+len(next.DataCh)+len(next.CompleteCh) != 0 {
				t.Fatal("old socket injected into resumed transfer")
			}
			select {
			case <-next.done:
				t.Fatal("old socket aborted resumed transfer")
			default:
			}
			tc.handler(s, resumed, envelope)
			if len(next.ReadyCh)+len(next.DataCh)+len(next.CompleteCh) == 0 {
				select {
				case <-next.done:
				default:
					t.Fatal("resumed transfer rejected its new socket")
				}
			}
		})
	}
}

func TestAgentReplyBindingReconnectAndReregistration(t *testing.T) {
	router := NewRouter()
	peer := &wsPeer{}
	agent := domain.Agent{ID: "agent", HomeID: "home"}
	router.RegisterAgent("home", agent, peer, nil, AgentTypeWorker, nil)
	original, _ := router.ResolveAgent("home", agent.ID)
	target := original.replyBinding()
	router.RegisterAgent("home", agent, peer, nil, AgentTypeWorker, nil)
	repeated, _ := router.ResolveAgent("home", agent.ID)
	envelope := protocol.Envelope{}
	if !target.accepts(repeated.replyBinding(), envelope) {
		t.Fatal("same socket re-registration lost pending work")
	}
	router.RegisterAgent("home", agent, &wsPeer{}, nil, AgentTypeWorker, nil)
	replacement, _ := router.ResolveAgent("home", agent.ID)
	if target.accepts(replacement.replyBinding(), envelope) || replacement.replyBinding().accepts(target, envelope) {
		t.Fatal("replacement socket can complete another socket's work")
	}
	if !target.accepts(original.replyBinding(), envelope) {
		t.Fatal("original socket cannot finish its own in-flight work")
	}
}

func TestAgentReplyBindingAuthenticatedWebSocket(t *testing.T) {
	db, server, home, agent, _, token := credentialHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	foreign := agent
	foreign.ID = "foreign-worker"
	foreignToken := "foreign-worker-test-token"
	must(t, db.UpsertAgent(ctx, foreign))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "foreign-token", HomeID: home.ID, AgentID: foreign.ID, TokenHash: hashToken(foreignToken), CreatedAt: time.Now().UTC()}))
	dial := func(agentID, rawToken string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/agent"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + rawToken}, "X-Hank-Agent-ID": {agentID}}})
		must(t, err)
		register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agentID, "", protocol.AgentRegister{AgentID: agentID, AgentType: AgentTypeWorker})
		must(t, err)
		must(t, wsjson.Write(ctx, conn, register))
		readUntilAgentRegistered(t, ctx, conn)
		return conn
	}
	original := dial(agent.ID, token)
	defer original.CloseNow()
	attacker := dial(foreign.ID, foreignToken)
	defer attacker.CloseNow()
	type result struct {
		envelope protocol.Envelope
		err      error
	}
	dispatch := func(conn *websocket.Conn) (protocol.Envelope, <-chan result) {
		t.Helper()
		done := make(chan result, 1)
		go func() {
			envelope, err := server.sendAgentCommandTo(ctx, home.ID, agent.ID, protocol.CommandSystemPing, struct{}{})
			done <- result{envelope, err}
		}()
		var command protocol.Envelope
		must(t, wsjson.Read(ctx, conn, &command))
		if command.Type != protocol.TypeCloudCommand {
			t.Fatalf("unexpected command: %s", command.Type)
		}
		return command, done
	}
	reject := func(conn *websocket.Conn, envelope protocol.Envelope, done <-chan result) {
		t.Helper()
		must(t, wsjson.Write(ctx, conn, envelope))
		// An explicit unsupported-message reply is a read-loop barrier, avoiding
		// timing-based assertions that could pass before the forged frame is read.
		must(t, wsjson.Write(ctx, conn, protocol.Envelope{Type: "audit.test.barrier"}))
		var barrier protocol.Envelope
		must(t, wsjson.Read(ctx, conn, &barrier))
		if barrier.Error == nil || barrier.Error.Code != "unsupported_message" {
			t.Fatalf("unexpected barrier: %+v", barrier)
		}
		select {
		case got := <-done:
			t.Fatalf("forged socket completed request: %+v", got)
		default:
		}
	}
	command, done := dispatch(original)
	response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, command.RequestID, agent.ID, home.ID, protocol.SystemPingResponse{Message: "pong"})
	must(t, err)
	reject(attacker, response, done)
	replacement := dial(agent.ID, token)
	defer replacement.CloseNow()
	reject(replacement, response, done)
	response.AgentID, response.HomeID = "", ""
	must(t, wsjson.Write(ctx, original, response))
	select {
	case got := <-done:
		must(t, got.err)
		if got.envelope.HomeID != home.ID || got.envelope.AgentID != agent.ID || got.envelope.Error != nil {
			t.Fatalf("original response not preserved: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	command, done = dispatch(replacement)
	response = protocol.NewErrorEnvelope(protocol.TypeCloudResponse, command.RequestID, agent.ID, home.ID, "expected_failure", "expected failure", nil)
	reject(original, response, done)
	reject(attacker, response, done)
	must(t, wsjson.Write(ctx, replacement, response))
	select {
	case got := <-done:
		must(t, got.err)
		if got.envelope.Error == nil || got.envelope.Error.Code != "expected_failure" {
			t.Fatalf("legitimate error was lost: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
