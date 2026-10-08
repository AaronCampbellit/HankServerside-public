package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/observability"
)

func testMCPSubscriptionConfig() mcpSubscriptionConfig {
	return mcpSubscriptionConfig{
		MaxPerToken:             2,
		MaxTotal:                3,
		HeartbeatInterval:       20 * time.Millisecond,
		CredentialCheckInterval: 20 * time.Millisecond,
		WriteTimeout:            time.Second,
	}
}

func TestMCPSubscriptionHubLimitsAndDeduplicates(t *testing.T) {
	hub := newMCPSubscriptionHub(testMCPSubscriptionConfig(), observability.NewMetrics())
	first, err := hub.register(json.RawMessage(`"one"`), "token-a", "user-a", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.register(json.RawMessage(`"two"`), "token-a", "user-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.register(json.RawMessage(`"three"`), "token-a", "user-a", true); !errors.Is(err, errMCPSubscriptionTokenLimit) {
		t.Fatalf("third token subscription error = %v", err)
	}

	hub.publishToolsChanged()
	hub.publishToolsChanged()
	for _, subscription := range []*mcpSubscription{first, second} {
		select {
		case <-subscription.toolsChanged:
		default:
			t.Fatal("tools change signal missing")
		}
		select {
		case <-subscription.toolsChanged:
			t.Fatal("duplicate tools change signal was not coalesced")
		default:
		}
		hub.unregister(subscription, "client_cancelled")
	}
}

func TestMCPSubscriptionHubGlobalLimitAndCloseIsolation(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.MaxPerToken = 3
	hub := newMCPSubscriptionHub(config, observability.NewMetrics())
	a, _ := hub.register(json.RawMessage(`1`), "token-a", "user-a", true)
	b, _ := hub.register(json.RawMessage(`2`), "token-b", "user-b", true)
	c, _ := hub.register(json.RawMessage(`3`), "token-c", "user-c", true)
	if _, err := hub.register(json.RawMessage(`4`), "token-d", "user-d", true); !errors.Is(err, errMCPSubscriptionGlobalLimit) {
		t.Fatalf("fourth global subscription error = %v", err)
	}

	hub.closeToken("token-b", "token_revoked")
	select {
	case reason := <-b.closeRequested:
		if reason != "token_revoked" {
			t.Fatalf("close reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("token-b was not closed")
	}
	for _, subscription := range []*mcpSubscription{a, c} {
		select {
		case reason := <-subscription.closeRequested:
			t.Fatalf("unrelated subscription closed with %q", reason)
		default:
		}
	}

	hub.unregister(a, "client_cancelled")
	hub.unregister(a, "client_cancelled")
	hub.unregister(b, "token_revoked")
	hub.unregister(c, "client_cancelled")
	if got := hub.activeCount(); got != 0 {
		t.Fatalf("active subscriptions = %d", got)
	}
}

func TestMCPSubscriptionHubCloseAllAndWait(t *testing.T) {
	hub := newMCPSubscriptionHub(testMCPSubscriptionConfig(), observability.NewMetrics())
	first, _ := hub.register(json.RawMessage(`1`), "token-a", "user-a", true)
	second, _ := hub.register(json.RawMessage(`2`), "token-b", "user-b", true)

	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := hub.wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait before unregister = %v", err)
	}

	hub.closeAll("shutdown")
	for _, subscription := range []*mcpSubscription{first, second} {
		select {
		case reason := <-subscription.closeRequested:
			if reason != "shutdown" {
				t.Fatalf("close reason = %q", reason)
			}
		case <-time.After(time.Second):
			t.Fatal("subscription was not closed")
		}
		hub.unregister(subscription, "shutdown")
	}
	if _, err := hub.register(json.RawMessage(`3`), "token-c", "user-c", true); !errors.Is(err, errMCPSubscriptionHubClosed) {
		t.Fatalf("register after closeAll = %v", err)
	}
	if err := hub.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMCPSubscriptionHubConcurrentLifecycle(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.MaxPerToken = 64
	config.MaxTotal = 64
	hub := newMCPSubscriptionHub(config, observability.NewMetrics())

	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			subscription, err := hub.register(json.RawMessage(`1`), "token", "user", true)
			if err != nil {
				return
			}
			hub.publishToolsChanged()
			hub.closeToken("token", "token_rotated")
			hub.unregister(subscription, "token_rotated")
		}(index)
	}
	wait.Wait()
	hub.closeAll("shutdown")
	if got := hub.activeCount(); got != 0 {
		t.Fatalf("active subscriptions = %d", got)
	}
}
