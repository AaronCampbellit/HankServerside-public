package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/observability"
)

var (
	errMCPSubscriptionTokenLimit  = errors.New("MCP subscription token limit reached")
	errMCPSubscriptionGlobalLimit = errors.New("MCP subscription global limit reached")
	errMCPSubscriptionHubClosed   = errors.New("MCP subscription hub is closed")
)

type mcpSubscriptionConfig struct {
	MaxPerToken             int
	MaxTotal                int
	HeartbeatInterval       time.Duration
	CredentialCheckInterval time.Duration
	WriteTimeout            time.Duration
}

func defaultMCPSubscriptionConfig() mcpSubscriptionConfig {
	return mcpSubscriptionConfig{
		MaxPerToken:             4,
		MaxTotal:                128,
		HeartbeatInterval:       15 * time.Second,
		CredentialCheckInterval: time.Minute,
		WriteTimeout:            10 * time.Second,
	}
}

type mcpSubscription struct {
	requestID        json.RawMessage
	tokenID          string
	userID           string
	toolsListChanged bool
	toolsChanged     chan struct{}
	closeRequested   chan string
	done             chan struct{}
	doneOnce         sync.Once
}

type mcpSubscriptionHub struct {
	mu      sync.Mutex
	config  mcpSubscriptionConfig
	metrics *observability.Metrics
	all     map[*mcpSubscription]struct{}
	byToken map[string]map[*mcpSubscription]struct{}
	closed  bool
	peak    int64
	wg      sync.WaitGroup
}

func newMCPSubscriptionHub(config mcpSubscriptionConfig, metrics *observability.Metrics) *mcpSubscriptionHub {
	return &mcpSubscriptionHub{
		config:  config,
		metrics: metrics,
		all:     make(map[*mcpSubscription]struct{}),
		byToken: make(map[string]map[*mcpSubscription]struct{}),
	}
}

func (h *mcpSubscriptionHub) register(requestID json.RawMessage, tokenID, userID string, toolsListChanged bool) (*mcpSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		h.metrics.IncMCPSubscriptionRejected("hub_closed")
		return nil, errMCPSubscriptionHubClosed
	}
	if len(h.all) >= h.config.MaxTotal {
		h.metrics.IncMCPSubscriptionRejected("global_limit")
		return nil, errMCPSubscriptionGlobalLimit
	}
	if len(h.byToken[tokenID]) >= h.config.MaxPerToken {
		h.metrics.IncMCPSubscriptionRejected("token_limit")
		return nil, errMCPSubscriptionTokenLimit
	}

	subscription := &mcpSubscription{
		requestID:        append(json.RawMessage(nil), requestID...),
		tokenID:          tokenID,
		userID:           userID,
		toolsListChanged: toolsListChanged,
		toolsChanged:     make(chan struct{}, 1),
		closeRequested:   make(chan string, 1),
		done:             make(chan struct{}),
	}
	h.all[subscription] = struct{}{}
	if h.byToken[tokenID] == nil {
		h.byToken[tokenID] = make(map[*mcpSubscription]struct{})
	}
	h.byToken[tokenID][subscription] = struct{}{}
	h.wg.Add(1)
	active := int64(len(h.all))
	if active > h.peak {
		h.peak = active
	}
	h.metrics.SetMCPSubscriptionCounts(active, h.peak)
	h.metrics.IncMCPSubscriptionOpened()
	return subscription, nil
}

func (h *mcpSubscriptionHub) unregister(subscription *mcpSubscription, reason string) {
	if subscription == nil {
		return
	}
	subscription.doneOnce.Do(func() {
		h.mu.Lock()
		delete(h.all, subscription)
		if subscriptions := h.byToken[subscription.tokenID]; subscriptions != nil {
			delete(subscriptions, subscription)
			if len(subscriptions) == 0 {
				delete(h.byToken, subscription.tokenID)
			}
		}
		active := int64(len(h.all))
		peak := h.peak
		h.mu.Unlock()

		close(subscription.done)
		h.wg.Done()
		h.metrics.SetMCPSubscriptionCounts(active, peak)
		h.metrics.IncMCPSubscriptionClosed(reason)
	})
}

func (h *mcpSubscriptionHub) activeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.all)
}

func (h *mcpSubscriptionHub) publishToolsChanged() {
	h.mu.Lock()
	targets := make([]*mcpSubscription, 0, len(h.all))
	for subscription := range h.all {
		if subscription.toolsListChanged {
			targets = append(targets, subscription)
		}
	}
	h.mu.Unlock()

	for _, subscription := range targets {
		select {
		case subscription.toolsChanged <- struct{}{}:
			h.metrics.IncMCPSubscriptionNotification("tools_changed")
		default:
			h.metrics.IncMCPSubscriptionCoalesced("tools_changed")
		}
	}
}

func (h *mcpSubscriptionHub) closeToken(tokenID, reason string) {
	h.mu.Lock()
	targets := make([]*mcpSubscription, 0, len(h.byToken[tokenID]))
	for subscription := range h.byToken[tokenID] {
		targets = append(targets, subscription)
	}
	h.mu.Unlock()
	h.signalClose(targets, reason)
}

func (h *mcpSubscriptionHub) closeAll(reason string) {
	h.mu.Lock()
	h.closed = true
	targets := make([]*mcpSubscription, 0, len(h.all))
	for subscription := range h.all {
		targets = append(targets, subscription)
	}
	h.mu.Unlock()
	h.signalClose(targets, reason)
}

func (h *mcpSubscriptionHub) signalClose(targets []*mcpSubscription, reason string) {
	for _, subscription := range targets {
		select {
		case subscription.closeRequested <- reason:
		default:
		}
	}
}

func (h *mcpSubscriptionHub) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
