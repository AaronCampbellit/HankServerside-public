package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDesktopRelayJoinClaimBindsSessionSideEpochAndAgent(t *testing.T) {
	now := time.Now().UTC()
	valid := desktopRelayJoinClaim{SessionID: "desk_0001", HomeID: "home_0001", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_0001", HardExpiresAt: now.Add(time.Hour)}
	if err := valid.Validate(now); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, mutate := range []func(*desktopRelayJoinClaim){
		func(value *desktopRelayJoinClaim) { value.SessionID = "" },
		func(value *desktopRelayJoinClaim) { value.Side = "server" },
		func(value *desktopRelayJoinClaim) { value.KeyEpoch = 0 },
		func(value *desktopRelayJoinClaim) { value.AgentID = "" },
		func(value *desktopRelayJoinClaim) { value.HardExpiresAt = now.Add(-time.Second) },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(now); err == nil {
			t.Fatalf("invalid claim accepted: %#v", candidate)
		}
	}
}

func TestDesktopRelayReservationAcceptsPostgresTimestampPrecision(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout = time.Hour
	relay := newInProcessDesktopRelay(limits, nil)
	hardExpiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(405123789 * time.Nanosecond)
	claim := desktopRelayJoinClaim{
		SessionID: "desk_precision", HomeID: "home_precision", Side: desktopRelayBrowser,
		KeyEpoch: 1, AgentID: "agent_precision", HardExpiresAt: hardExpiresAt,
	}
	if err := relay.Reserve(claim); err != nil {
		t.Fatalf("reserve original claim: %v", err)
	}
	databaseClaim := claim
	databaseClaim.Side = desktopRelayAgent
	databaseClaim.HardExpiresAt = hardExpiresAt.Truncate(time.Microsecond)
	if err := relay.Reserve(databaseClaim); err != nil {
		t.Fatalf("reserve database-backed claim: %v", err)
	}
	relay.CancelReservation(databaseClaim)
	relay.CancelReservation(claim)
}

func TestDesktopRelayProductionLimitsAreBounded(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	if err := limits.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if limits.JoinTimeout != 60*time.Second || limits.ReconnectTimeout != 90*time.Second || limits.IdleTimeout != 30*time.Second || limits.MaxDuration != 8*time.Hour || limits.MaxFrameBytes != protocol.DesktopMaxEncryptedFramePayload+12 || limits.MaxBytesPerSecond != 50<<20 || limits.MaxQueueBytes != 512<<10 || limits.SlowConsumerGrace != 4*time.Second || limits.MaxSessions != 32 || limits.MaxSessionsPerHome != 4 || limits.MaxSessionsPerAgent != 1 {
		t.Fatalf("unexpected limits: %#v", limits)
	}
}

func TestDesktopRelayPipeAdmitsOneValidFrameLargerThanLatencyBudget(t *testing.T) {
	pipe := newDesktopRelayPipe()
	payload := []byte("oversized-but-valid")
	if err := pipe.enqueue(context.Background(), payload, 8, time.Second, func() {}); err != nil {
		t.Fatalf("enqueue oversized frame: %v", err)
	}
	if got := pipe.bytes(); got != int64(len(payload)) {
		t.Fatalf("queued bytes = %d, want %d", got, len(payload))
	}
}

func TestDesktopRelayHomeAndAgentLimitsRejectOnlyNewSession(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout = time.Hour
	relay := newInProcessDesktopRelay(limits, func(context.Context, desktopRelayLifecycleEvent) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for index := 0; index < 4; index++ {
		claim := desktopRelayJoinClaim{SessionID: fmt.Sprintf("desk_home_%d", index), HomeID: "home_one", Side: desktopRelayBrowser, KeyEpoch: 1,
			AgentID: fmt.Sprintf("agent_home_%d", index), HardExpiresAt: time.Now().Add(time.Hour)}
		go func() { _ = relay.Join(ctx, claim, newChannelRelayEndpoint()) }()
	}
	time.Sleep(20 * time.Millisecond)
	tooMany := desktopRelayJoinClaim{SessionID: "desk_home_5", HomeID: "home_one", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_home_5", HardExpiresAt: time.Now().Add(time.Hour)}
	if err := relay.Join(ctx, tooMany, newChannelRelayEndpoint()); !errors.Is(err, errDesktopRelayLimit) {
		t.Fatalf("fifth home session = %v", err)
	}
	duplicateAgent := desktopRelayJoinClaim{SessionID: "desk_other", HomeID: "home_two", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_home_0", HardExpiresAt: time.Now().Add(time.Hour)}
	if err := relay.Join(ctx, duplicateAgent, newChannelRelayEndpoint()); !errors.Is(err, errDesktopRelayLimit) {
		t.Fatalf("duplicate agent session = %v", err)
	}
	if got := relay.Snapshot("desk_home_0").SessionID; got != "desk_home_0" {
		t.Fatalf("existing session isolated: %q", got)
	}
}

func TestDesktopRelayContractIsOpaque(t *testing.T) {
	for _, requirement := range []string{"separate browser and agent authentication", "binary-only", "opaque", "payload-free", "close both sides"} {
		if !strings.Contains(desktopRelayContract, requirement) {
			t.Fatalf("relay contract missing %q", requirement)
		}
	}
	relay := newDesktopRelay(nil)
	snapshot := relay.Snapshot("desk_0001")
	if strings.Contains(strings.ToLower(snapshot.SessionID), "desktop-fixture-payload") {
		t.Fatal("relay snapshot exposed payload")
	}
}

func TestDesktopRelayForwardsOpaqueBinaryWithoutRetention(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout = time.Second, time.Second
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	browser, agent := newChannelRelayEndpoint(), newChannelRelayEndpoint()
	now := time.Now().UTC()
	claim := desktopRelayJoinClaim{SessionID: "desk_0001", HomeID: "home_0001", KeyEpoch: 1, AgentID: "agent_0001", HardExpiresAt: now.Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		browserErr := claim
		browserErr.Side = desktopRelayBrowser
		_ = relay.Join(ctx, browserErr, browser)
	}()
	go func() { agentErr := claim; agentErr.Side = desktopRelayAgent; _ = relay.Join(ctx, agentErr, agent) }()
	payload := []byte("opaque-ciphertext-not-plaintext")
	browser.receive <- payload
	select {
	case got := <-agent.sent:
		if !bytes.Equal(got, payload) {
			t.Fatalf("forwarded = %x", got)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not forward payload")
	}
	var snapshot desktopRelaySnapshot
	deadline := time.Now().Add(time.Second)
	for snapshot = relay.Snapshot(claim.SessionID); snapshot.BrowserToAgentBytes != int64(len(payload)) && time.Now().Before(deadline); snapshot = relay.Snapshot(claim.SessionID) {
		time.Sleep(time.Millisecond)
	}
	metadata, _ := json.Marshal(snapshot)
	if bytes.Contains(metadata, payload) || snapshot.BrowserToAgentBytes != int64(len(payload)) {
		t.Fatalf("snapshot = %s", metadata)
	}
	paired := false
	for !paired {
		select {
		case event := <-events:
			paired = event.Kind == "paired"
		case <-time.After(time.Second):
			t.Fatal("relay did not emit paired lifecycle event")
		}
	}
	relay.Revoke(claim.SessionID, "test_complete")
}

func TestDesktopRelayRejectsDuplicateSideAndOversizedFrame(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout, limits.MaxFrameBytes = time.Second, time.Second, 8
	relay := newInProcessDesktopRelay(limits, func(context.Context, desktopRelayLifecycleEvent) {})
	first, duplicate := newChannelRelayEndpoint(), newChannelRelayEndpoint()
	claim := desktopRelayJoinClaim{SessionID: "desk_0002", HomeID: "home_0001", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_0001", HardExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.Join(ctx, claim, first) }()
	time.Sleep(10 * time.Millisecond)
	if err := relay.Join(ctx, claim, duplicate); !errors.Is(err, errDesktopRelayDuplicateSide) {
		t.Fatalf("duplicate = %v", err)
	}
	agent := newChannelRelayEndpoint()
	agentClaim := claim
	agentClaim.Side = desktopRelayAgent
	go func() { _ = relay.Join(ctx, agentClaim, agent) }()
	first.receive <- make([]byte, 9)
	select {
	case <-first.closed:
	case <-time.After(time.Second):
		t.Fatal("oversized frame did not close relay")
	}
}

func TestDesktopRelaySignalsBackpressureBeforeSlowConsumerTermination(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout, limits.SlowConsumerGrace, limits.MaxFrameBytes, limits.MaxQueueBytes = time.Second, time.Second, 40*time.Millisecond, 8, 8
	events := make(chan desktopRelayLifecycleEvent, 16)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	browser, agent := newChannelRelayEndpoint(), newBlockingRelayEndpoint()
	claim := desktopRelayJoinClaim{SessionID: "desk_slow", HomeID: "home_0001", KeyEpoch: 1, AgentID: "agent_0001", HardExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		browserClaim := claim
		browserClaim.Side = desktopRelayBrowser
		_ = relay.Join(ctx, browserClaim, browser)
	}()
	go func() {
		agentClaim := claim
		agentClaim.Side = desktopRelayAgent
		_ = relay.Join(ctx, agentClaim, agent)
	}()
	browser.receive <- []byte("opaque01")
	browser.receive <- []byte("opaque02")
	seenBackpressure := false
	for {
		select {
		case event := <-events:
			if event.Kind == "backpressure" {
				seenBackpressure = true
			}
			if event.Kind == "closed" {
				if !seenBackpressure || event.Reason != "slow_consumer" {
					t.Fatalf("events out of order: backpressure=%v close=%q", seenBackpressure, event.Reason)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("slow consumer did not terminate")
		}
	}
}

func TestDesktopRelayIdleReadUsesStableIdleTimeoutReason(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout = time.Second, 25*time.Millisecond
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	claim := desktopRelayJoinClaim{SessionID: "desk_idle", HomeID: "home_idle", KeyEpoch: 1, AgentID: "agent_idle", HardExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		browser := claim
		browser.Side = desktopRelayBrowser
		_ = relay.Join(ctx, browser, newChannelRelayEndpoint())
	}()
	go func() {
		agent := claim
		agent.Side = desktopRelayAgent
		_ = relay.Join(ctx, agent, newChannelRelayEndpoint())
	}()
	for {
		select {
		case event := <-events:
			if event.Kind == "closed" {
				if event.Reason != "idle_timeout" {
					t.Fatalf("close reason = %q", event.Reason)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("idle relay did not close")
		}
	}
}

func TestDesktopRelayUsesReconnectWindowForUnpairedEpoch(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.ReconnectTimeout = 20*time.Millisecond, 80*time.Millisecond
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	now := time.Now()
	claim := desktopRelayJoinClaim{SessionID: "desk_reconnect_timeout", HomeID: "home_reconnect", Side: desktopRelayBrowser, KeyEpoch: 2, AgentID: "agent_reconnect", HardExpiresAt: now.Add(time.Hour), Reconnect: true, ReconnectExpiresAt: now.Add(80 * time.Millisecond)}
	go func() { _ = relay.Join(context.Background(), claim, newChannelRelayEndpoint()) }()
	select {
	case event := <-events:
		if event.Kind == "closed" {
			t.Fatalf("reconnect used initial join timeout: %#v", event)
		}
	case <-time.After(40 * time.Millisecond):
	}
	for {
		select {
		case event := <-events:
			if event.Kind == "closed" {
				if event.Reason != "reconnect_timeout" {
					t.Fatalf("reason = %q", event.Reason)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("reconnect window did not expire")
		}
	}
}

func TestDesktopRelayLateReconnectJoinCannotRestartNinetySecondWindow(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.ReconnectTimeout = 20*time.Millisecond, 90*time.Second
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	now := time.Now()
	claim := desktopRelayJoinClaim{SessionID: "desk_reconnect_late", HomeID: "home_reconnect", Side: desktopRelayBrowser, KeyEpoch: 2,
		AgentID: "agent_reconnect", HardExpiresAt: now.Add(time.Hour), Reconnect: true, ReconnectExpiresAt: now.Add(35 * time.Millisecond)}
	go func() { _ = relay.Join(context.Background(), claim, newChannelRelayEndpoint()) }()
	for {
		select {
		case event := <-events:
			if event.Kind == "closed" {
				if event.Reason != "reconnect_timeout" {
					t.Fatalf("reason = %q", event.Reason)
				}
				if time.Since(now) > 120*time.Millisecond {
					t.Fatalf("late join extended reconnect deadline: %v", time.Since(now))
				}
				return
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("authoritative reconnect deadline was not enforced")
		}
	}
}

func TestDesktopRelayEnforcesPerSideRateLimit(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout, limits.MaxBytesPerSecond = time.Second, time.Second, 8
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	browser, agent := newChannelRelayEndpoint(), newChannelRelayEndpoint()
	claim := desktopRelayJoinClaim{SessionID: "desk_rate", HomeID: "home_rate", KeyEpoch: 1, AgentID: "agent_rate", HardExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		browserClaim := claim
		browserClaim.Side = desktopRelayBrowser
		_ = relay.Join(ctx, browserClaim, browser)
	}()
	go func() {
		agentClaim := claim
		agentClaim.Side = desktopRelayAgent
		_ = relay.Join(ctx, agentClaim, agent)
	}()
	browser.receive <- []byte("12345678")
	browser.receive <- []byte("x")
	for {
		select {
		case event := <-events:
			if event.Kind == "closed" {
				if event.Reason != "rate_limit" {
					t.Fatalf("reason = %q", event.Reason)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("rate limit did not close relay")
		}
	}
}

func TestDesktopRelayEnforcesHardExpiry(t *testing.T) {
	limits := defaultDesktopRelayLimits()
	limits.JoinTimeout, limits.IdleTimeout, limits.MaxDuration = time.Second, time.Second, 35*time.Millisecond
	events := make(chan desktopRelayLifecycleEvent, 8)
	relay := newInProcessDesktopRelay(limits, func(_ context.Context, event desktopRelayLifecycleEvent) { events <- event })
	claim := desktopRelayJoinClaim{SessionID: "desk_hard", HomeID: "home_hard", KeyEpoch: 1, AgentID: "agent_hard", HardExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		browser := claim
		browser.Side = desktopRelayBrowser
		_ = relay.Join(ctx, browser, newChannelRelayEndpoint())
	}()
	go func() {
		agent := claim
		agent.Side = desktopRelayAgent
		_ = relay.Join(ctx, agent, newChannelRelayEndpoint())
	}()
	for {
		select {
		case event := <-events:
			if event.Kind == "closed" {
				if event.Reason != "hard_expired" {
					t.Fatalf("reason = %q", event.Reason)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("hard expiry did not close relay")
		}
	}
}

func TestDesktopRelayFakeExposesOnlyOpaqueByteCounts(t *testing.T) {
	payload := []byte("desktop-fixture-payload")
	var events []desktopRelayLifecycleEvent
	relay := &opaqueRelayFake{sink: func(_ context.Context, event desktopRelayLifecycleEvent) { events = append(events, event) }}
	claim := desktopRelayJoinClaim{SessionID: "desk_0001", HomeID: "home_0001", Side: desktopRelayBrowser, KeyEpoch: 1, AgentID: "agent_0001", HardExpiresAt: time.Now().Add(time.Hour)}
	if err := relay.Join(context.Background(), claim, &opaqueEndpointFake{payload: payload}); err != nil {
		t.Fatal(err)
	}
	encoded := []byte(relay.Snapshot(claim.SessionID).SessionID + events[0].Kind + events[0].Reason)
	if bytes.Contains(encoded, payload) || relay.Snapshot(claim.SessionID).BrowserToAgentBytes != int64(len(payload)) {
		t.Fatalf("opaque relay leaked payload or lost count: %q %#v", encoded, relay.Snapshot(claim.SessionID))
	}
}

type opaqueRelayFake struct {
	sink     desktopRelayLifecycleSink
	snapshot desktopRelaySnapshot
}

func (relay *opaqueRelayFake) Join(ctx context.Context, claim desktopRelayJoinClaim, endpoint desktopRelayEndpoint) error {
	payload, err := endpoint.Read(ctx)
	if err != nil {
		return err
	}
	relay.snapshot = desktopRelaySnapshot{SessionID: claim.SessionID, KeyEpoch: claim.KeyEpoch, BrowserConnected: claim.Side == desktopRelayBrowser, BrowserToAgentBytes: int64(len(payload)), StartedAt: time.Now().UTC()}
	relay.sink(ctx, desktopRelayLifecycleEvent{SessionID: claim.SessionID, KeyEpoch: claim.KeyEpoch, Kind: "joined", BrowserToAgentBytes: int64(len(payload))})
	return nil
}
func (relay *opaqueRelayFake) Reserve(desktopRelayJoinClaim) error     { return nil }
func (relay *opaqueRelayFake) CancelReservation(desktopRelayJoinClaim) {}
func (relay *opaqueRelayFake) Revoke(string, string)                   {}
func (relay *opaqueRelayFake) Snapshot(string) desktopRelaySnapshot    { return relay.snapshot }

type opaqueEndpointFake struct{ payload []byte }

func (endpoint *opaqueEndpointFake) Read(context.Context) ([]byte, error) {
	return append([]byte(nil), endpoint.payload...), nil
}
func (endpoint *opaqueEndpointFake) Write(context.Context, []byte) error { return nil }
func (endpoint *opaqueEndpointFake) Close(string) error                  { return nil }

type channelRelayEndpoint struct {
	receive chan []byte
	sent    chan []byte
	closed  chan string
}

type blockingRelayEndpoint struct{ closed chan string }

func newBlockingRelayEndpoint() *blockingRelayEndpoint {
	return &blockingRelayEndpoint{closed: make(chan string, 1)}
}
func (endpoint *blockingRelayEndpoint) Read(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (endpoint *blockingRelayEndpoint) Write(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}
func (endpoint *blockingRelayEndpoint) Close(reason string) error {
	endpoint.closed <- reason
	return nil
}

func newChannelRelayEndpoint() *channelRelayEndpoint {
	return &channelRelayEndpoint{receive: make(chan []byte, 2), sent: make(chan []byte, 2), closed: make(chan string, 1)}
}
func (endpoint *channelRelayEndpoint) Read(ctx context.Context) ([]byte, error) {
	select {
	case value := <-endpoint.receive:
		return append([]byte(nil), value...), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (endpoint *channelRelayEndpoint) Write(_ context.Context, value []byte) error {
	endpoint.sent <- append([]byte(nil), value...)
	return nil
}
func (endpoint *channelRelayEndpoint) Close(reason string) error {
	select {
	case endpoint.closed <- reason:
	default:
	}
	return nil
}
