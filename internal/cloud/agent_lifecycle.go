package cloud

import (
	"context"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// Serialize routing and durable presence updates so a reconnect cannot race an
// older connection's offline write. Replies retain their separate peer binding.
func (s *Server) registerAgentConnection(ctx context.Context, homeID string, agent domain.Agent, peer *wsPeer, payload protocol.AgentRegister, previousID string) (string, error) {
	s.agentLifecycleMu.Lock()
	defer s.agentLifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.store.UpsertAgent(ctx, agent); err != nil {
		return "", err
	}
	if err := s.store.UpdateAgentRuntimeMetadata(ctx, homeID, agent.ID, payload.Metadata["platform"], payload.Metadata["architecture"], payload.Metadata["app_version"], payload.Metadata["installation_mode"], payload.Capabilities, agent.UpdatedAt); err != nil {
		return "", err
	}
	if previousID != "" {
		_ = s.store.MarkAgentConnection(ctx, previousID, s.runtimeID, homeID, agent.ID, nil, false)
	}
	connectionID := s.router.RegisterAgent(homeID, agent, peer, payload.Capabilities, agent.AgentType, payload.Metadata)
	_ = s.store.MarkAgentConnection(ctx, connectionID, s.runtimeID, homeID, agent.ID, nil, true)
	s.metrics.SetOnlineAgents(s.router.AgentCount())
	s.emitHomeStatus(ctx, homeID, map[string]any{"home_id": homeID, "agent_id": agent.ID, "status": domain.AgentStatusOnline})
	return connectionID, nil
}

func (s *Server) disconnectAgentConnection(homeID, agentID, connectionID string, primary bool) {
	s.agentLifecycleMu.Lock()
	defer s.agentLifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	removed := s.router.UnregisterAgent(homeID, agentID, connectionID)
	if connectionID != "" {
		_ = s.store.MarkAgentConnection(ctx, connectionID, s.runtimeID, homeID, agentID, nil, false)
	}
	s.metrics.SetOnlineAgents(s.router.AgentCount())
	// Explicit credential revocation can already have removed the router entry.
	// A never-registered socket cannot change presence, and a replacement owns it.
	_, replacement := s.router.ResolveAgent(homeID, agentID)
	if connectionID == "" || (!removed && replacement) {
		return
	}
	now := time.Now().UTC()
	_ = s.store.SetAgentStatus(ctx, agentID, domain.AgentStatusOffline, &now)
	if primary {
		// A different primary can also have taken over default Home routing.
		if current, ok := s.router.ResolveAgent(homeID, ""); !ok || current.agent.ID == agentID {
			s.markHomeSyncOffline(ctx, homeID, agentID)
		}
	}
	s.emitHomeStatus(ctx, homeID, map[string]any{"home_id": homeID, "agent_id": agentID, "status": domain.AgentStatusOffline})
}

func (s *Server) recordAgentHeartbeat(ctx context.Context, homeID, agentID, connectionID string, payload protocol.AgentHeartbeat, now time.Time) bool {
	s.agentLifecycleMu.Lock()
	defer s.agentLifecycleMu.Unlock()
	if !s.router.UpdateAgentHeartbeat(homeID, agentID, connectionID, payload.Capabilities, payload.Metrics) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = s.store.SetAgentStatus(ctx, agentID, domain.AgentStatusOnline, &now)
	_ = s.store.HeartbeatAgentConnection(ctx, connectionID, payload.Capabilities)
	return true
}
