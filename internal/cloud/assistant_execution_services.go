package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/protocol"
	"net/url"
)

func (s *Server) executionMachineService(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	agentID, unit := executionArg(args, "agent_id"), executionArg(args, "unit")
	agent, err := s.store.GetAgentByID(ctx, agentID)
	if err != nil || agent.HomeID != rt.Home.ID {
		return assistant.Failure(call.ID, "not_found")
	}
	connection, ok := s.router.ResolveAgent(rt.Home.ID, agentID)
	if !ok {
		return assistant.Failure(call.ID, "agent_offline")
	}
	epoch := s.router.assistantJournalEpoch(connection)
	if epoch == "" || (prepared && !s.router.supportsCurrentAgent(connection, protocol.CommandAssistantOperationExecute)) {
		return assistant.Failure(call.ID, "capability_unavailable")
	}
	envelope, code := s.executionAgentRead(ctx, rt, agentID, protocol.CommandAssistantServiceInspect, protocol.AssistantServiceInspectRequest{Unit: unit})
	if code != "" {
		return assistant.Failure(call.ID, code)
	}
	response, err := protocol.DecodePayload[protocol.AssistantServiceInspectResponse](envelope)
	if err != nil || len(response.Services) > 32 {
		return assistant.Failure(call.ID, "internal_error")
	}
	items := []executionItem{}
	for _, service := range response.Services {
		if service.Unit == "" || (unit != "" && service.Unit != unit) {
			return assistant.Failure(call.ID, "internal_error")
		}
		raw, _ := json.Marshal(service)
		digest := sha256.Sum256(append([]byte(epoch+"\x00"), raw...))
		revision := hex.EncodeToString(digest[:])
		if prepared {
			allowed := false
			for _, operation := range service.AllowedOperations {
				allowed = allowed || operation == executionArg(args, "operation")
			}
			if !allowed {
				return assistant.Failure(call.ID, "permission_denied")
			}
			if revision != executionArg(args, "revision") {
				return assistant.Failure(call.ID, "revision_conflict")
			}
		}
		items = append(items, executionItem{Type: "machine", ID: agentID, AgentID: agentID, Scope: "home_admin", Title: service.Unit, State: service.ActiveState, Revision: revision, JournalEpoch: epoch, Service: &service, URI: "/dashboard/agents/" + url.PathEscape(agentID)})
	}
	if unit != "" && len(items) != 1 {
		return assistant.Failure(call.ID, "not_found")
	}
	return executionResult(call, items, 32, prepared)
}
