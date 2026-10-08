package cloud

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var errAgentOffline = errors.New("agent offline")

type pendingAgentRequest struct {
	target   agentReplyBinding
	response chan protocol.Envelope
}

type agentRequestRegistry struct {
	mu      sync.Mutex
	pending map[string]pendingAgentRequest
}

func newAgentRequestRegistry() *agentRequestRegistry {
	return &agentRequestRegistry{pending: make(map[string]pendingAgentRequest)}
}

func (r *agentRequestRegistry) Register(requestID string, target agentReplyBinding) (<-chan protocol.Envelope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[requestID]; ok {
		return nil, errors.New("duplicate agent request")
	}
	ch := make(chan protocol.Envelope, 1)
	r.pending[requestID] = pendingAgentRequest{target: target, response: ch}
	return ch, nil
}

func (r *agentRequestRegistry) Resolve(sender agentReplyBinding, envelope protocol.Envelope) bool {
	r.mu.Lock()
	pending, ok := r.pending[envelope.RequestID]
	ok = ok && pending.target.accepts(sender, envelope)
	if ok {
		delete(r.pending, envelope.RequestID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	envelope.HomeID, envelope.AgentID = sender.homeID, sender.agentID
	pending.response <- envelope
	return true
}

func (r *agentRequestRegistry) Cancel(requestID string) {
	r.mu.Lock()
	delete(r.pending, requestID)
	r.mu.Unlock()
}

func (s *Server) sendAgentCommand(ctx context.Context, homeID string, command string, body any) (protocol.Envelope, error) {
	return s.sendAgentCommandTo(ctx, homeID, "", command, body)
}

func (s *Server) sendAgentCommandTo(ctx context.Context, homeID, agentID, command string, body any) (protocol.Envelope, error) {
	agentConn, ok := s.router.ResolveAgent(homeID, agentID)
	if !ok {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.offline",
			Summary: "Primary Hank Agent is offline before command dispatch.",
			HomeID:  homeID,
			Details: traceDetails(map[string]any{
				"command": command,
			}),
		})
		return protocol.Envelope{}, errAgentOffline
	}

	if protocol.RequiresAppSandbox(command) && !s.router.supportsCurrentAgent(agentConn, protocol.CapabilityAppsSandboxV1) {
		return protocol.Envelope{Error: &protocol.ErrorPayload{Code: "agent_update_required", Message: "Update the Hank Agent to use restricted apps"}}, nil
	}
	requestID := newID("sync")
	startedAt := time.Now()
	traceCtx := assistantTraceContextFrom(ctx)
	traceCtx.HomeID = firstNonBlank(traceCtx.HomeID, homeID)
	traceCtx.RequestID = requestID
	ctx = withAssistantTraceContext(ctx, traceCtx)
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "agent",
		Event:   "agent.command.start",
		Summary: "Sending command to the primary Hank Agent.",
		Details: traceDetails(map[string]any{
			"command":  command,
			"agent_id": agentConn.agent.ID,
		}),
	})
	responseCh, err := s.agentRequests.Register(requestID, agentConn.replyBinding())
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.register_failed",
			Summary: "Could not register the pending agent request.",
			Details: traceDetails(map[string]any{
				"command": command,
				"error":   err.Error(),
			}),
		})
		return protocol.Envelope{}, err
	}
	defer s.agentRequests.Cancel(requestID)

	commandBody, err := protocol.EncodeBody(body)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.encode_failed",
			Summary: "Could not encode the agent command body.",
			Details: traceDetails(map[string]any{
				"command": command,
				"error":   err.Error(),
			}),
		})
		return protocol.Envelope{}, err
	}
	if command == protocol.CommandAppsInvoke {
		actorID, _ := ctx.Value(appActorContextKey{}).(string)
		if actorID == "" {
			actorID = assistantTraceContextFrom(ctx).UserID
		}
		membership, err := s.store.GetHomeMembership(ctx, homeID, actorID)
		if err != nil || actorID == "" {
			return protocol.Envelope{Error: &protocol.ErrorPayload{Code: "permission_denied", Message: "App invocation requires a current Home membership"}}, nil
		}
		commandBody, err = stampAppInvocation(commandBody, homeID, actorID, membership.Role)
		if err != nil {
			return protocol.Envelope{}, err
		}
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeCloudCommand, requestID, agentConn.agent.ID, homeID, protocol.RoutedCommand{
		Command: command,
		Body:    commandBody,
	})
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.envelope_failed",
			Summary: "Could not create the agent command envelope.",
			Details: traceDetails(map[string]any{
				"command": command,
				"error":   err.Error(),
			}),
		})
		return protocol.Envelope{}, err
	}
	if err := agentConn.peer.Write(ctx, envelope); err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.write_failed",
			Summary: "Failed while sending command to the primary Hank Agent.",
			Details: traceDetails(map[string]any{
				"command":    command,
				"error":      err.Error(),
				"elapsed_ms": time.Since(startedAt).Milliseconds(),
			}),
		})
		return protocol.Envelope{}, err
	}

	timeout := s.timeoutForCommand(command)
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-waitCtx.Done():
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "agent",
			Event:   "agent.command.timeout",
			Summary: "Timed out waiting for the primary Hank Agent response.",
			Details: traceDetails(map[string]any{
				"command":    command,
				"error":      waitCtx.Err().Error(),
				"elapsed_ms": time.Since(startedAt).Milliseconds(),
			}),
		})
		return protocol.Envelope{}, waitCtx.Err()
	case response := <-responseCh:
		level := "info"
		event := "agent.command.completed"
		summary := "Primary Hank Agent returned a response."
		details := traceDetails(map[string]any{
			"command":    command,
			"elapsed_ms": time.Since(startedAt).Milliseconds(),
		})
		if response.Error != nil {
			level = "error"
			event = "agent.command.error"
			summary = "Primary Hank Agent returned an error."
			if strings.HasPrefix(command, "apps.") {
				details["error"] = "App request failed"
			} else {
				details["error"] = response.Error.Message
			}
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   level,
			Scope:   "agent",
			Event:   event,
			Summary: summary,
			Details: details,
		})
		return response, nil
	}
}
