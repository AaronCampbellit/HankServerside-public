package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestAgentLifecycleReplacementOwnsPresenceAndHeartbeat(t *testing.T) {
	db, server, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	agent.Status = domain.AgentStatusOnline
	payload := protocol.AgentRegister{Capabilities: []string{"current"}}
	original, err := server.registerAgentConnection(ctx, home.ID, agent, &wsPeer{}, payload, "")
	must(t, err)
	replacement, err := server.registerAgentConnection(ctx, home.ID, agent, &wsPeer{}, payload, "")
	must(t, err)
	for _, disconnected := range []string{"", original} {
		server.disconnectAgentConnection(home.ID, agent.ID, disconnected, false)
		current, ok := server.router.ResolveAgent(home.ID, agent.ID)
		if !ok || current.connectionID != replacement {
			t.Fatal("old/unregistered socket removed the replacement")
		}
		stored, err := db.GetAgentByID(ctx, agent.ID)
		must(t, err)
		if stored.Status != domain.AgentStatusOnline {
			t.Fatal("old/unregistered socket marked replacement offline")
		}
		if server.recordAgentHeartbeat(ctx, home.ID, agent.ID, disconnected, protocol.AgentHeartbeat{Capabilities: []string{"stale"}, Metrics: json.RawMessage(`{"cpu_load_1m":99}`)}, time.Now()) {
			t.Fatal("stale heartbeat accepted")
		}
	}
	if !server.recordAgentHeartbeat(ctx, home.ID, agent.ID, replacement, protocol.AgentHeartbeat{Capabilities: []string{"fresh"}}, time.Now()) {
		t.Fatal("current heartbeat rejected")
	}
	server.disconnectAgentConnection(home.ID, agent.ID, replacement, false)
	stored, err := db.GetAgentByID(ctx, agent.ID)
	must(t, err)
	if stored.Status != domain.AgentStatusOffline || server.router.AgentCount() != 0 {
		t.Fatal("current disconnect did not clear presence")
	}
	if server.recordAgentHeartbeat(ctx, home.ID, agent.ID, replacement, payloadHeartbeat(), time.Now()) {
		t.Fatal("closed socket revived agent")
	}
}

func payloadHeartbeat() protocol.AgentHeartbeat {
	return protocol.AgentHeartbeat{Capabilities: []string{"stale"}}
}

func TestAgentLifecycleConcurrentReplacementAndDisconnect(t *testing.T) {
	db, server, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	agent.Status = domain.AgentStatusOnline
	previous, err := server.registerAgentConnection(ctx, home.ID, agent, &wsPeer{}, protocol.AgentRegister{}, "")
	must(t, err)
	for range 12 {
		var next string
		var registerErr error
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			<-start
			next, registerErr = server.registerAgentConnection(ctx, home.ID, agent, &wsPeer{}, protocol.AgentRegister{}, "")
		}()
		go func() {
			defer done.Done()
			<-start
			server.disconnectAgentConnection(home.ID, agent.ID, previous, false)
		}()
		close(start)
		done.Wait()
		must(t, registerErr)
		stored, err := db.GetAgentByID(ctx, agent.ID)
		must(t, err)
		current, ok := server.router.ResolveAgent(home.ID, agent.ID)
		if !ok || current.connectionID != next || stored.Status != domain.AgentStatusOnline {
			t.Fatal("concurrent old disconnect overrode new registration")
		}
		previous = next
	}
	// Administrator revocation may have removed the route before socket teardown.
	server.router.UnregisterAgent(home.ID, agent.ID, previous)
	server.disconnectAgentConnection(home.ID, agent.ID, previous, false)
	stored, err := db.GetAgentByID(ctx, agent.ID)
	must(t, err)
	if stored.Status != domain.AgentStatusOffline {
		t.Fatal("explicit disconnection retained online status")
	}
}

func TestPeerWriteQueueHonorsCancellation(t *testing.T) {
	peer := &wsPeer{}
	peer.writeOnce.Do(func() { peer.writeGate = make(chan struct{}, 1) })
	peer.writeGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- peer.Write(ctx, protocol.Envelope{}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("write wait = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued writer ignored cancellation")
	}
	<-peer.writeGate
}

func TestAgentRegistrationReplacesStoredRuntimeMetadata(t *testing.T) {
	db, server, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	must(t, db.UpdateAgentRuntimeMetadata(ctx, home.ID, agent.ID, "linux", "amd64", "0.3.1", "system", []string{"old.capability"}, time.Now()))
	stale, err := db.GetAgentByID(ctx, agent.ID)
	must(t, err)
	payload := protocol.AgentRegister{Capabilities: []string{"fleet.v1"}, Metadata: map[string]string{"platform": "linux", "architecture": "amd64", "app_version": "0.3.2", "installation_mode": "system"}}
	_, err = server.registerAgentConnection(ctx, home.ID, stale, &wsPeer{}, payload, "")
	must(t, err)
	stored, err := db.GetAgentByID(ctx, agent.ID)
	must(t, err)
	if stored.AppVersion != "0.3.2" || !slices.Contains(stored.Capabilities, "fleet.v1") || slices.Contains(stored.Capabilities, "old.capability") {
		t.Fatalf("registration retained stale runtime metadata: version=%s capabilities=%v", stored.AppVersion, stored.Capabilities)
	}
}
