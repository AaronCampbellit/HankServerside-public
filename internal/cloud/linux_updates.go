package cloud

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

const linuxUpdateHealthDeadline = 5 * time.Minute

type linuxUpdateCoordinator struct {
	server *Server
	now    func() time.Time
}

func newLinuxUpdateCoordinator(server *Server) *linuxUpdateCoordinator {
	return &linuxUpdateCoordinator{server: server, now: func() time.Time { return time.Now().UTC() }}
}

func (c *linuxUpdateCoordinator) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.Reconcile(ctx); err != nil {
					c.server.logger.Warn("Linux update reconciliation failed", "error", err)
				}
			}
		}
	}()
}

func (c *linuxUpdateCoordinator) Reconcile(ctx context.Context) error {
	assignments, err := c.server.store.ListDispatchableLinuxAssignments(ctx, c.now(), 8)
	if err != nil {
		return err
	}
	var result error
	for _, assignment := range assignments {
		if err := c.dispatch(ctx, assignment); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (c *linuxUpdateCoordinator) ReconcileAgent(ctx context.Context, homeID, agentID string) {
	assignments, err := c.server.store.ListDispatchableLinuxAssignments(ctx, c.now(), 32)
	if err != nil {
		c.server.logger.Warn("failed to load reconnecting Linux assignment", "home_id", homeID, "agent_id", agentID, "error", err)
		return
	}
	for _, assignment := range assignments {
		if assignment.HomeID == homeID && assignment.AgentID == agentID {
			if err := c.dispatch(ctx, assignment); err != nil {
				c.server.logger.Warn("failed to dispatch reconnecting Linux assignment", "home_id", homeID, "agent_id", agentID, "error", err)
			}
			return
		}
	}
}

func (c *linuxUpdateCoordinator) dispatch(ctx context.Context, assignment domain.LinuxAgentUpdateAssignment) error {
	connection, ok := c.server.router.ResolveAgent(assignment.HomeID, assignment.AgentID)
	if !ok {
		_, err := c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{"pending", "delayed"}, protocol.UpdateStateWaitingOnline, "", c.now())
		return err
	}
	release, err := c.server.store.GetLinuxAgentRelease(ctx, assignment.ToVersion)
	if err != nil {
		return err
	}
	if !slices.Contains(connection.capabilities, protocol.CommandSystemUpdateApply) && slices.Contains(connection.capabilities, "shell.exec") && connection.metadata["platform"] == "linux" && connection.metadata["installation_mode"] == "system" {
		return c.dispatchLegacy(ctx, assignment, release.ManifestURL, connection.metadata["app_version"])
	}
	if !slices.Contains(connection.capabilities, protocol.CommandSystemUpdateApply) {
		_, err := c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{"pending", "delayed", "waiting_online"}, "manual_update_required", "native_update_unsupported", c.now())
		return err
	}
	changed, err := c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{"pending", "delayed", "waiting_online"}, protocol.UpdateStateDownloading, "", c.now())
	if err != nil || !changed {
		return err
	}
	command := protocol.SystemUpdateAssignment{
		RolloutID:             assignment.RolloutID,
		AssignmentID:          assignment.ID,
		Version:               assignment.ToVersion,
		ManifestURL:           release.ManifestURL,
		NotBefore:             assignment.NotBefore,
		HealthDeadlineSeconds: int(linuxUpdateHealthDeadline.Seconds()),
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	response, err := c.server.sendAgentCommandTo(dispatchCtx, assignment.HomeID, assignment.AgentID, protocol.CommandSystemUpdateApply, command)
	if err != nil || response.Error != nil {
		code := "dispatch_failed"
		if errors.Is(err, errAgentOffline) {
			code = "agent_offline"
		}
		_, transitionErr := c.server.store.TransitionLinuxAgentAssignmentForAgent(context.Background(), assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{protocol.UpdateStateDownloading}, protocol.UpdateStateFailed, code, c.now())
		return errors.Join(err, transitionErr)
	}
	accepted, err := protocol.DecodePayload[protocol.SystemUpdateAccepted](response)
	if err != nil || !accepted.Accepted || accepted.AssignmentID != assignment.ID {
		_, transitionErr := c.server.store.TransitionLinuxAgentAssignmentForAgent(context.Background(), assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{protocol.UpdateStateDownloading}, protocol.UpdateStateFailed, "update_not_accepted", c.now())
		if err == nil {
			err = errors.New("Linux update was not accepted")
		}
		return errors.Join(err, transitionErr)
	}
	_, err = c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{protocol.UpdateStateDownloading}, protocol.UpdateStateInstalling, "", c.now())
	return err
}

func (c *linuxUpdateCoordinator) dispatchLegacy(ctx context.Context, assignment domain.LinuxAgentUpdateAssignment, manifestURL, fromVersion string) error {
	changed, err := c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{"pending", "delayed", "waiting_online"}, protocol.UpdateStateDownloading, "", c.now())
	if err != nil || !changed {
		return err
	}
	command, err := legacyLinuxUpdateCommand(assignment, manifestURL, fromVersion, c.now().Add(linuxUpdateHealthDeadline))
	if err != nil {
		return err
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	response, sendErr := c.server.sendAgentCommandTo(dispatchCtx, assignment.HomeID, assignment.AgentID, "shell.exec", map[string]any{"command": command, "timeout_seconds": 150})
	if response.Error != nil {
		_, transitionErr := c.server.store.TransitionLinuxAgentAssignmentForAgent(context.Background(), assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{protocol.UpdateStateDownloading}, protocol.UpdateStateFailed, "legacy_bridge_rejected", c.now())
		return errors.Join(errors.New(response.Error.Message), transitionErr)
	}
	// A successful legacy self-update normally tears down the websocket before
	// the old agent can answer. The authenticated reconnect at the target
	// version is the authoritative success signal.
	_, transitionErr := c.server.store.TransitionLinuxAgentAssignmentForAgent(context.Background(), assignment.ID, assignment.RolloutID, assignment.HomeID, assignment.AgentID, []string{protocol.UpdateStateDownloading}, protocol.UpdateStateReconnecting, "", c.now())
	if transitionErr != nil {
		return transitionErr
	}
	if sendErr != nil && !errors.Is(sendErr, errAgentOffline) && !errors.Is(sendErr, context.Canceled) && !errors.Is(sendErr, context.DeadlineExceeded) {
		c.server.logger.Info("legacy Linux update disconnected during replacement", "agent_id", assignment.AgentID, "assignment_id", assignment.ID, "error", sendErr)
	}
	return nil
}

func (c *linuxUpdateCoordinator) RecordState(ctx context.Context, homeID, agentID string, value protocol.SystemUpdateState) error {
	from := map[string][]string{
		protocol.UpdateStateDelayed:        {"pending", protocol.UpdateStateWaitingOnline},
		protocol.UpdateStateWaitingOnline:  {"pending", protocol.UpdateStateDelayed},
		protocol.UpdateStateDownloading:    {"pending", protocol.UpdateStateDelayed, protocol.UpdateStateWaitingOnline},
		protocol.UpdateStateInstalling:     {protocol.UpdateStateDownloading},
		protocol.UpdateStateReconnecting:   {protocol.UpdateStateDownloading, protocol.UpdateStateInstalling},
		protocol.UpdateStateHealthy:        {protocol.UpdateStateDownloading, protocol.UpdateStateInstalling, protocol.UpdateStateReconnecting},
		protocol.UpdateStateFailed:         {"pending", protocol.UpdateStateDelayed, protocol.UpdateStateWaitingOnline, protocol.UpdateStateDownloading, protocol.UpdateStateInstalling, protocol.UpdateStateReconnecting},
		protocol.UpdateStateRolledBack:     {protocol.UpdateStateInstalling, protocol.UpdateStateReconnecting, protocol.UpdateStateFailed},
		protocol.UpdateStateRecoveryFailed: {protocol.UpdateStateInstalling, protocol.UpdateStateReconnecting, protocol.UpdateStateFailed},
	}[value.State]
	changed, err := c.server.store.TransitionLinuxAgentAssignmentForAgent(ctx, value.AssignmentID, value.RolloutID, homeID, agentID, from, value.State, value.ErrorCode, c.now())
	if err != nil {
		return err
	}
	if !changed {
		return store.ErrConflict
	}
	return nil
}
