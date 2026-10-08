package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const topicAgentsHealth = "agents.health"

// isManagementCommand reports whether a routed command is an RMM management
// action that must be admin-gated (device control, wake-on-LAN, remote shell).
// File and Home Assistant commands have their own dedicated policy checks and
// are intentionally excluded here.
func isManagementCommand(command string) bool {
	return (strings.HasPrefix(command, "apps.") && command != protocol.CommandAppsInvoke) ||
		strings.HasPrefix(command, "host.") ||
		strings.HasPrefix(command, "shell.") ||
		strings.HasPrefix(command, "wol.") ||
		command == protocol.CommandSystemRestart
}

// agentHealthMonitor watches connected agents and emits alerts on the
// agents.health realtime topic: an agent going offline, or a worker reporting
// low free disk. State is per-agent so each condition fires once per edge.
type agentHealthMonitor struct {
	diskFreePct float64
}

func newAgentHealthMonitor() *agentHealthMonitor {
	return &agentHealthMonitor{
		diskFreePct: 0.10, // alert when free disk drops below 10%
	}
}

func (s *Server) runAgentHealthMonitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkAgentHealth(ctx)
		}
	}
}

func (s *Server) checkAgentHealth(ctx context.Context) {
	home, err := s.store.GetSingletonHome(ctx)
	if err != nil || home.ID == "" {
		return
	}
	homeID := home.ID
	snapshots := s.router.AgentsForHome(homeID)
	online := make(map[string]AgentSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		online[snapshot.AgentID] = snapshot
		s.evaluateDiskAlert(ctx, homeID, snapshot)
	}
	agents, err := s.store.ListAgentsByHome(ctx, homeID)
	if err != nil {
		s.logger.Warn("agent health inventory lookup failed", "home_id", homeID, "error", err)
		return
	}
	for _, agent := range agents {
		state, kind, healthKind, outcome, severity, summary := "offline", notificationKindAgentOffline, "agent.offline", "offline", "warning", "An agent disconnected from Hank."
		if _, ok := online[agent.ID]; ok {
			state, kind, healthKind, outcome, severity, summary = "online", notificationKindAgentRecovered, "agent.recovered", "recovered", "info", "An agent reconnected to Hank."
		}
		transitioned, err := s.store.ObserveNotificationSource(ctx, domain.NotificationSourceObservation{
			SourceKey: "agent-connectivity:" + homeID + ":" + agent.ID, HomeID: homeID, ResourceID: agent.ID,
			State: state, EventKind: kind, Outcome: outcome, Severity: severity, OccurredAt: time.Now().UTC(),
		})
		if err != nil {
			s.logger.Warn("agent health transition persistence failed", "agent_id", agent.ID, "error", err)
			continue
		}
		if transitioned {
			s.recordAgentHealthTransition(ctx, homeID, agent.ID, healthKind, severity, summary, nil)
		}
	}
	if err := s.flushPendingNotificationSourceEvents(ctx); err != nil {
		s.logger.Warn("agent health notification flush failed", "home_id", homeID, "error", err)
	}
}

func (s *Server) evaluateDiskAlert(ctx context.Context, homeID string, snapshot AgentSnapshot) {
	if len(snapshot.Metrics) == 0 {
		return
	}
	var metrics protocol.HostMetrics
	if err := json.Unmarshal(snapshot.Metrics, &metrics); err != nil {
		return
	}
	if metrics.DiskTotalBytes <= 0 {
		return
	}
	freePct := float64(metrics.DiskTotalBytes-metrics.DiskUsedBytes) / float64(metrics.DiskTotalBytes)

	low := freePct < s.health.diskFreePct
	state, kind, outcome, severity := "healthy", notificationKindDiskRecovered, "recovered", "info"
	if low {
		state, kind, outcome, severity = "low", notificationKindDiskLow, "low", "warning"
	}
	transitioned, err := s.store.ObserveNotificationSource(ctx, domain.NotificationSourceObservation{
		SourceKey: "agent-disk:" + homeID + ":" + snapshot.AgentID, HomeID: homeID, ResourceID: snapshot.AgentID,
		State: state, EventKind: kind, Outcome: outcome, Severity: severity, OccurredAt: time.Now().UTC(), NotifyInitial: low,
	})
	if err != nil {
		s.logger.Warn("agent disk transition persistence failed", "agent_id", snapshot.AgentID, "error", err)
		return
	}
	if transitioned && low {
		s.recordAgentHealthTransition(ctx, homeID, snapshot.AgentID, "agent.disk_low", "warning", "An agent is low on disk space.", map[string]any{
			"free_percent": int(freePct * 100), "disk_total_bytes": metrics.DiskTotalBytes, "disk_used_bytes": metrics.DiskUsedBytes,
		})
	} else if transitioned {
		s.recordAgentHealthTransition(ctx, homeID, snapshot.AgentID, "agent.disk_recovered", "info", "An agent has sufficient disk space again.", nil)
	}
}

func (s *Server) recordAgentHealthTransition(ctx context.Context, homeID string, agentID string, kind string, severity string, summary string, details map[string]any) {
	observedAt := time.Now().UTC()
	payload := map[string]any{
		"home_id":  homeID,
		"agent_id": agentID,
		"kind":     kind,
		"severity": severity,
		"summary":  summary,
		"time":     observedAt,
	}
	if details != nil {
		payload["details"] = details
	}
	s.broadcastAppEventOnKey(ctx, scopedHomeTopic(homeID, topicAgentsHealth), topicAgentsHealth, kind, payload)
	s.audit(ctx, "agent.health."+strings.TrimPrefix(kind, "agent."), auditSeverityWarning, "", "", homeID, "", "agent", agentID, details)
}
