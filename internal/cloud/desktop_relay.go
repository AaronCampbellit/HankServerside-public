package cloud

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var errDesktopRelayNotReady = errors.New("desktop relay is not available before milestone 2")

var (
	errDesktopRelayDuplicateSide = errors.New("desktop relay side already connected")
	errDesktopRelayLimit         = errors.New("desktop relay limit exceeded")
	errDesktopRelayClaimMismatch = errors.New("desktop relay claim mismatch")
)

const desktopRelayContract = "separate browser and agent authentication; exact side/session/epoch pairing; one connection per side; binary-only frames; join, idle, frame, bandwidth, duration, and process session limits; bidirectional opaque forwarding; payload-free metrics; close both sides on revoke, expiry, or failure"

type desktopRelayLimits struct {
	JoinTimeout         time.Duration
	ReconnectTimeout    time.Duration
	IdleTimeout         time.Duration
	MaxDuration         time.Duration
	SlowConsumerGrace   time.Duration
	MaxFrameBytes       int64
	MaxBytesPerSecond   int64
	MaxQueueBytes       int64
	MaxSessions         int
	MaxSessionsPerHome  int
	MaxSessionsPerAgent int
}

func defaultDesktopRelayLimits() desktopRelayLimits {
	return desktopRelayLimits{JoinTimeout: 60 * time.Second, ReconnectTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxDuration: 8 * time.Hour,
		SlowConsumerGrace: 4 * time.Second, MaxFrameBytes: protocol.DesktopMaxEncryptedFramePayload + 12, MaxBytesPerSecond: 50 << 20, MaxQueueBytes: 512 << 10,
		MaxSessions: 32, MaxSessionsPerHome: 4, MaxSessionsPerAgent: 1}
}

func (limits desktopRelayLimits) Validate() error {
	if limits.JoinTimeout <= 0 || limits.ReconnectTimeout <= 0 || limits.ReconnectTimeout > 90*time.Second || limits.IdleTimeout <= 0 || limits.MaxDuration <= 0 || limits.MaxDuration > 8*time.Hour || limits.SlowConsumerGrace <= 0 || limits.SlowConsumerGrace > 10*time.Second ||
		limits.MaxFrameBytes <= 0 || limits.MaxFrameBytes > protocol.DesktopMaxEncryptedFramePayload+12 || limits.MaxBytesPerSecond <= 0 || limits.MaxBytesPerSecond > 50<<20 ||
		limits.MaxQueueBytes <= 0 || limits.MaxQueueBytes > 16<<20 || limits.MaxSessions <= 0 || limits.MaxSessions > 32 ||
		limits.MaxSessionsPerHome <= 0 || limits.MaxSessionsPerHome > 4 || limits.MaxSessionsPerAgent <= 0 || limits.MaxSessionsPerAgent > 1 {
		return errors.New("desktop relay limits exceed the production boundary")
	}
	return nil
}

type desktopRelaySide string

const (
	desktopRelayBrowser desktopRelaySide = "browser"
	desktopRelayAgent   desktopRelaySide = "agent"
)

type desktopRelayJoinClaim struct {
	SessionID          string
	HomeID             string
	Side               desktopRelaySide
	KeyEpoch           uint32
	AgentID            string
	HardExpiresAt      time.Time
	Reconnect          bool
	ReconnectExpiresAt time.Time
}

func (claim desktopRelayJoinClaim) Validate(now time.Time) error {
	if !validDesktopResourceID(claim.SessionID) || !validDesktopResourceID(claim.HomeID) || (claim.Side != desktopRelayBrowser && claim.Side != desktopRelayAgent) || claim.KeyEpoch == 0 || !validDesktopResourceID(claim.AgentID) || !claim.HardExpiresAt.After(now) ||
		(claim.Reconnect && (!claim.ReconnectExpiresAt.After(now) || claim.ReconnectExpiresAt.After(claim.HardExpiresAt))) || (!claim.Reconnect && !claim.ReconnectExpiresAt.IsZero()) {
		return errors.New("invalid desktop relay join claim")
	}
	return nil
}

type desktopRelayLifecycleEvent struct {
	SessionID           string
	KeyEpoch            uint32
	Kind                string
	Reason              string
	BrowserToAgentBytes int64
	AgentToBrowserBytes int64
}

type desktopRelayLifecycleSink func(context.Context, desktopRelayLifecycleEvent)
type desktopRelayFactory func(desktopRelayLimits, desktopRelayLifecycleSink) desktopRelay

// desktopRelay implementations must enforce desktopRelayContract: authenticate
// browser and agent separately, pair exact side/session/epoch claims, accept one
// connection per side and binary-only opaque frames, enforce every configured
// limit, expose payload-free metrics, and close both sides on revoke or failure.
type desktopRelay interface {
	Reserve(desktopRelayJoinClaim) error
	CancelReservation(desktopRelayJoinClaim)
	Join(context.Context, desktopRelayJoinClaim, desktopRelayEndpoint) error
	Revoke(sessionID, reason string)
	Snapshot(sessionID string) desktopRelaySnapshot
}

type desktopRelayEndpoint interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(reason string) error
}

type desktopRelaySnapshot struct {
	SessionID                 string
	KeyEpoch                  uint32
	BrowserConnected          bool
	AgentConnected            bool
	BrowserToAgentBytes       int64
	AgentToBrowserBytes       int64
	BrowserToAgentQueuedBytes int64
	AgentToBrowserQueuedBytes int64
	StartedAt                 time.Time
}

type unavailableDesktopRelay struct {
	mu      sync.Mutex
	revoked map[string]string
}

func newDesktopRelay(factory desktopRelayFactory) desktopRelay {
	limits := defaultDesktopRelayLimits()
	if factory != nil {
		return factory(limits, func(context.Context, desktopRelayLifecycleEvent) {})
	}
	return newInProcessDesktopRelay(limits, func(context.Context, desktopRelayLifecycleEvent) {})
}

func (relay *unavailableDesktopRelay) Join(context.Context, desktopRelayJoinClaim, desktopRelayEndpoint) error {
	return errDesktopRelayNotReady
}
func (relay *unavailableDesktopRelay) Reserve(desktopRelayJoinClaim) error {
	return errDesktopRelayNotReady
}
func (relay *unavailableDesktopRelay) CancelReservation(desktopRelayJoinClaim) {}

func (relay *unavailableDesktopRelay) Revoke(sessionID, reason string) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	relay.revoked[sessionID] = reason
}

func (relay *unavailableDesktopRelay) Snapshot(sessionID string) desktopRelaySnapshot {
	return desktopRelaySnapshot{SessionID: sessionID}
}

type inProcessDesktopRelay struct {
	mu       sync.Mutex
	limits   desktopRelayLimits
	sink     desktopRelayLifecycleSink
	sessions map[string]*inProcessDesktopSession
}

type inProcessDesktopSession struct {
	claim           desktopRelayJoinClaim
	browser         desktopRelayEndpoint
	agent           desktopRelayEndpoint
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	started         bool
	finished        bool
	err             error
	snapshot        desktopRelaySnapshot
	browserRate     desktopRelayRateWindow
	agentRate       desktopRelayRateWindow
	browserReserved bool
	agentReserved   bool
	browserToAgent  *desktopRelayPipe
	agentToBrowser  *desktopRelayPipe
}

type desktopRelayRateWindow struct {
	startedAt time.Time
	bytes     int64
}

type desktopRelayPipe struct {
	mu          sync.Mutex
	frames      [][]byte
	queuedBytes int64
	available   chan struct{}
	space       chan struct{}
}

func newDesktopRelayPipe() *desktopRelayPipe {
	return &desktopRelayPipe{available: make(chan struct{}, 1), space: make(chan struct{}, 1)}
}

func (pipe *desktopRelayPipe) enqueue(ctx context.Context, payload []byte, limit int64, grace time.Duration, onBackpressure func()) error {
	timer := time.NewTimer(grace)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	backpressured := false
	for {
		pipe.mu.Lock()
		// A single valid frame may be larger than the interactive queue budget
		// (for example, a clipboard payload). Admit it only when the pipe is
		// empty; subsequent frames wait so ordinary video cannot build a
		// multi-second FIFO backlog behind it.
		if pipe.queuedBytes == 0 || pipe.queuedBytes+int64(len(payload)) <= limit {
			copyOfPayload := append([]byte(nil), payload...)
			pipe.frames = append(pipe.frames, copyOfPayload)
			pipe.queuedBytes += int64(len(copyOfPayload))
			pipe.mu.Unlock()
			notifyDesktopRelay(pipe.available)
			return nil
		}
		pipe.mu.Unlock()
		if !backpressured {
			backpressured = true
			onBackpressure()
			timer.Reset(grace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errDesktopRelayLimit
		case <-pipe.space:
		}
	}
}

func (pipe *desktopRelayPipe) next(ctx context.Context) ([]byte, error) {
	for {
		pipe.mu.Lock()
		if len(pipe.frames) > 0 {
			payload := pipe.frames[0]
			pipe.frames[0] = nil
			pipe.frames = pipe.frames[1:]
			more := len(pipe.frames) > 0
			pipe.mu.Unlock()
			if more {
				notifyDesktopRelay(pipe.available)
			}
			return payload, nil
		}
		pipe.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pipe.available:
		}
	}
}

func (pipe *desktopRelayPipe) complete(size int) {
	pipe.mu.Lock()
	pipe.queuedBytes -= int64(size)
	if pipe.queuedBytes < 0 {
		pipe.queuedBytes = 0
	}
	pipe.mu.Unlock()
	notifyDesktopRelay(pipe.space)
}

func (pipe *desktopRelayPipe) bytes() int64 {
	pipe.mu.Lock()
	defer pipe.mu.Unlock()
	return pipe.queuedBytes
}
func notifyDesktopRelay(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func newInProcessDesktopRelay(limits desktopRelayLimits, sink desktopRelayLifecycleSink) desktopRelay {
	if err := limits.Validate(); err != nil {
		panic(err)
	}
	if sink == nil {
		sink = func(context.Context, desktopRelayLifecycleEvent) {}
	}
	return &inProcessDesktopRelay{limits: limits, sink: sink, sessions: make(map[string]*inProcessDesktopSession)}
}

func (relay *inProcessDesktopRelay) Reserve(claim desktopRelayJoinClaim) error {
	now := time.Now().UTC()
	if err := claim.Validate(now); err != nil {
		return errDesktopRelayClaimMismatch
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	session, err := relay.sessionForJoinLocked(claim, now)
	if err != nil {
		return err
	}
	if claim.Side == desktopRelayBrowser {
		if session.browser != nil {
			return errDesktopRelayDuplicateSide
		}
		if session.browserReserved {
			return nil
		}
		session.browserReserved = true
	} else {
		if session.agent != nil {
			return errDesktopRelayDuplicateSide
		}
		if session.agentReserved {
			return nil
		}
		session.agentReserved = true
	}
	return nil
}

func (relay *inProcessDesktopRelay) CancelReservation(claim desktopRelayJoinClaim) {
	relay.mu.Lock()
	session := relay.sessions[claim.SessionID]
	if session == nil || session.claim.KeyEpoch != claim.KeyEpoch {
		relay.mu.Unlock()
		return
	}
	if claim.Side == desktopRelayBrowser {
		session.browserReserved = false
	} else {
		session.agentReserved = false
	}
	if session.browser == nil && session.agent == nil && !session.browserReserved && !session.agentReserved && !session.started && !session.finished {
		session.finished = true
		delete(relay.sessions, claim.SessionID)
		session.cancel()
		close(session.done)
	}
	relay.mu.Unlock()
}

func (relay *inProcessDesktopRelay) sessionForJoinLocked(claim desktopRelayJoinClaim, now time.Time) (*inProcessDesktopSession, error) {
	session := relay.sessions[claim.SessionID]
	if session == nil {
		homeSessions, agentSessions := 0, 0
		for _, existing := range relay.sessions {
			if existing.claim.HomeID == claim.HomeID {
				homeSessions++
			}
			if existing.claim.AgentID == claim.AgentID {
				agentSessions++
			}
		}
		if len(relay.sessions) >= relay.limits.MaxSessions || homeSessions >= relay.limits.MaxSessionsPerHome || agentSessions >= relay.limits.MaxSessionsPerAgent {
			return nil, errDesktopRelayLimit
		}
		sessionCtx, cancel := context.WithCancel(context.Background())
		session = &inProcessDesktopSession{claim: claim, ctx: sessionCtx, cancel: cancel, done: make(chan struct{}), browserToAgent: newDesktopRelayPipe(), agentToBrowser: newDesktopRelayPipe(), snapshot: desktopRelaySnapshot{SessionID: claim.SessionID, KeyEpoch: claim.KeyEpoch}}
		relay.sessions[claim.SessionID] = session
		go relay.expireJoin(session)
		go relay.expireHard(session)
	} else if session.claim.KeyEpoch != claim.KeyEpoch || session.claim.HomeID != claim.HomeID || session.claim.AgentID != claim.AgentID || session.claim.Reconnect != claim.Reconnect || !desktopRelayTimestampEqual(session.claim.ReconnectExpiresAt, claim.ReconnectExpiresAt) || !desktopRelayTimestampEqual(session.claim.HardExpiresAt, claim.HardExpiresAt) {
		return nil, errDesktopRelayClaimMismatch
	}
	return session, nil
}

// PostgreSQL stores timestamptz values with microsecond precision. Initial
// relay capacity is reserved before the session is persisted, so compare the
// later database-backed claim at that same precision.
func desktopRelayTimestampEqual(left, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return left.IsZero() && right.IsZero()
	}
	return left.UnixMicro() == right.UnixMicro()
}

func (relay *inProcessDesktopRelay) Join(ctx context.Context, claim desktopRelayJoinClaim, endpoint desktopRelayEndpoint) error {
	now := time.Now().UTC()
	if err := claim.Validate(now); err != nil || endpoint == nil {
		return errDesktopRelayClaimMismatch
	}
	relay.mu.Lock()
	session, err := relay.sessionForJoinLocked(claim, now)
	if err != nil {
		relay.mu.Unlock()
		return err
	}
	if (claim.Side == desktopRelayBrowser && (session.browser != nil || (!session.browserReserved && session.started))) || (claim.Side == desktopRelayAgent && (session.agent != nil || (!session.agentReserved && session.started))) {
		relay.mu.Unlock()
		return errDesktopRelayDuplicateSide
	}
	if claim.Side == desktopRelayBrowser {
		session.browserReserved = false
		session.browser = endpoint
		session.snapshot.BrowserConnected = true
	} else {
		session.agentReserved = false
		session.agent = endpoint
		session.snapshot.AgentConnected = true
	}
	paired := false
	if session.browser != nil && session.agent != nil && !session.started {
		session.started = true
		session.snapshot.StartedAt = now
		paired = true
		go relay.readPump(session, desktopRelayBrowser, session.browser, session.browserToAgent)
		go relay.writePump(session, desktopRelayBrowser, session.browserToAgent, session.agent)
		go relay.readPump(session, desktopRelayAgent, session.agent, session.agentToBrowser)
		go relay.writePump(session, desktopRelayAgent, session.agentToBrowser, session.browser)
	}
	relay.mu.Unlock()
	relay.sink(ctx, desktopRelayLifecycleEvent{SessionID: claim.SessionID, KeyEpoch: claim.KeyEpoch, Kind: "side_joined", Reason: string(claim.Side)})
	if paired {
		relay.sink(ctx, desktopRelayLifecycleEvent{SessionID: claim.SessionID, KeyEpoch: claim.KeyEpoch, Kind: "paired"})
	}

	select {
	case <-ctx.Done():
		relay.fail(session, "transport_closed", ctx.Err())
	case <-session.done:
	}
	relay.mu.Lock()
	result := session.err
	relay.mu.Unlock()
	return result
}

func (relay *inProcessDesktopRelay) expireJoin(session *inProcessDesktopSession) {
	timeout := relay.limits.JoinTimeout
	reason := "join_timeout"
	if session.claim.Reconnect {
		timeout = time.Until(session.claim.ReconnectExpiresAt)
		reason = "reconnect_timeout"
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-timer.C:
		relay.failUnpaired(session, reason)
	case <-session.done:
	}
}

func (relay *inProcessDesktopRelay) failUnpaired(session *inProcessDesktopSession, reason string) {
	relay.mu.Lock()
	unpaired := !session.started && !session.finished
	relay.mu.Unlock()
	if unpaired {
		relay.fail(session, reason, errDesktopRelayLimit)
	}
}

func (relay *inProcessDesktopRelay) expireHard(session *inProcessDesktopSession) {
	deadline := session.claim.HardExpiresAt
	max := time.Now().Add(relay.limits.MaxDuration)
	if max.Before(deadline) {
		deadline = max
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
		relay.fail(session, "hard_expired", errDesktopRelayLimit)
	case <-session.done:
	}
}

func (relay *inProcessDesktopRelay) readPump(session *inProcessDesktopSession, side desktopRelaySide, source desktopRelayEndpoint, pipe *desktopRelayPipe) {
	for {
		readCtx, cancel := context.WithTimeout(session.ctx, relay.limits.IdleTimeout)
		payload, err := source.Read(readCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && session.ctx.Err() != nil {
				return
			}
			reason := "transport_closed"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "idle_timeout"
			}
			relay.fail(session, reason, err)
			return
		}
		if int64(len(payload)) > relay.limits.MaxFrameBytes {
			relay.fail(session, "frame_limit", errDesktopRelayLimit)
			return
		}
		if !relay.allowBytes(session, side, int64(len(payload))) {
			relay.fail(session, "rate_limit", errDesktopRelayLimit)
			return
		}
		err = pipe.enqueue(session.ctx, payload, relay.limits.MaxQueueBytes, relay.limits.SlowConsumerGrace, func() {
			relay.sink(context.Background(), desktopRelayLifecycleEvent{SessionID: session.claim.SessionID, KeyEpoch: session.claim.KeyEpoch, Kind: "backpressure", Reason: string(side)})
		})
		if err != nil {
			if session.ctx.Err() == nil {
				relay.fail(session, "slow_consumer", err)
			}
			return
		}
	}
}

func (relay *inProcessDesktopRelay) writePump(session *inProcessDesktopSession, side desktopRelaySide, pipe *desktopRelayPipe, destination desktopRelayEndpoint) {
	for {
		payload, err := pipe.next(session.ctx)
		if err != nil {
			return
		}
		err = destination.Write(session.ctx, payload)
		pipe.complete(len(payload))
		if err != nil {
			if session.ctx.Err() == nil {
				relay.fail(session, "transport_closed", err)
			}
			return
		}
		relay.mu.Lock()
		if side == desktopRelayBrowser {
			session.snapshot.BrowserToAgentBytes += int64(len(payload))
		} else {
			session.snapshot.AgentToBrowserBytes += int64(len(payload))
		}
		relay.mu.Unlock()
	}
}

func (relay *inProcessDesktopRelay) allowBytes(session *inProcessDesktopSession, side desktopRelaySide, count int64) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	window := &session.browserRate
	if side == desktopRelayAgent {
		window = &session.agentRate
	}
	now := time.Now()
	if window.startedAt.IsZero() || now.Sub(window.startedAt) >= time.Second {
		window.startedAt, window.bytes = now, 0
	}
	window.bytes += count
	return window.bytes <= relay.limits.MaxBytesPerSecond
}

func (relay *inProcessDesktopRelay) fail(session *inProcessDesktopSession, reason string, err error) {
	relay.mu.Lock()
	if session.finished {
		relay.mu.Unlock()
		return
	}
	session.finished, session.err = true, err
	session.snapshot.BrowserConnected, session.snapshot.AgentConnected = false, false
	delete(relay.sessions, session.claim.SessionID)
	browser, agent, snapshot := session.browser, session.agent, session.snapshot
	session.cancel()
	close(session.done)
	relay.mu.Unlock()
	if browser != nil {
		_ = browser.Close(reason)
	}
	if agent != nil {
		_ = agent.Close(reason)
	}
	relay.sink(context.Background(), desktopRelayLifecycleEvent{SessionID: snapshot.SessionID, KeyEpoch: snapshot.KeyEpoch, Kind: "closed", Reason: reason, BrowserToAgentBytes: snapshot.BrowserToAgentBytes, AgentToBrowserBytes: snapshot.AgentToBrowserBytes})
}

func (relay *inProcessDesktopRelay) Revoke(sessionID, reason string) {
	relay.mu.Lock()
	session := relay.sessions[sessionID]
	relay.mu.Unlock()
	if session != nil {
		relay.fail(session, reason, errors.New(reason))
	}
}

func (relay *inProcessDesktopRelay) Snapshot(sessionID string) desktopRelaySnapshot {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if session := relay.sessions[sessionID]; session != nil {
		snapshot := session.snapshot
		snapshot.BrowserToAgentQueuedBytes = session.browserToAgent.bytes()
		snapshot.AgentToBrowserQueuedBytes = session.agentToBrowser.bytes()
		return snapshot
	}
	return desktopRelaySnapshot{SessionID: sessionID}
}
