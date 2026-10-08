package agent

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"reflect"
	"sort"
	"time"
)

func (c *Client) handleAssistantServiceInspect(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	var request protocol.AssistantServiceInspectRequest
	if c.registeredHomeID == "" || envelope.HomeID != c.registeredHomeID || envelope.AgentID != c.agentID || c.operationJournal == nil || !c.assistantServices.available() || strictOperationJSON(command.Body, &request) != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "capability_unavailable", "Allowlisted service inspection is unavailable.", nil)
	}
	states := []protocol.AssistantServiceState{}
	if request.Unit != "" {
		state, err := c.assistantServices.inspect(ctx, request.Unit)
		if err != nil {
			return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "not_found", "Allowlisted service is unavailable.", nil)
		}
		states = append(states, state)
	} else {
		units := []string{}
		for unit := range c.assistantServices.grants {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			state := protocol.AssistantServiceState{Unit: unit, AllowedOperations: []string{}}
			for _, operation := range []string{"start", "stop", "restart"} {
				if c.assistantServices.grants[unit][operation] {
					state.AllowedOperations = append(state.AllowedOperations, operation)
				}
			}
			states = append(states, state)
		}
	}
	raw, _ := json.Marshal(protocol.AssistantServiceInspectResponse{Services: states})
	return c.writeJSON(ctx, conn, protocol.Envelope{Version: protocol.Version, Type: protocol.TypeCloudResponse, RequestID: envelope.RequestID, HomeID: envelope.HomeID, AgentID: c.agentID, Timestamp: time.Now().UTC(), Payload: raw})
}

func (c *Client) executeAssistantServiceOperation(ctx context.Context, request protocol.AssistantOperationRequest) (protocol.AssistantOperationStatusResponse, error) {
	var args protocol.AssistantServiceOperationArguments
	if strictOperationJSON(request.Arguments, &args) != nil || c.assistantServices == nil || !c.assistantServices.grants[args.Unit][args.Operation] {
		return protocol.AssistantOperationStatusResponse{}, operations.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return protocol.AssistantOperationStatusResponse{}, err
	}
	receipt, fresh, err := c.operationJournal.Begin(request.Identity)
	if err != nil || !fresh {
		return receipt, err
	}
	result := protocol.AssistantOperationResult{Verification: "unavailable"}
	finish := func(outcome, code string) (protocol.AssistantOperationStatusResponse, error) {
		receipt.Outcome = outcome
		result.Code = code
		receipt.Result, _ = json.Marshal(result)
		return receipt, c.operationJournal.Complete(receipt)
	}
	before, err := c.assistantServices.inspect(ctx, args.Unit)
	if err != nil {
		return finish("failed", "capability_unavailable")
	}
	if !reflect.DeepEqual(before, args.PriorState) {
		return finish("failed", "revision_conflict")
	}
	if _, err := c.assistantServices.run(ctx, "--system", "--no-pager", args.Operation, "--", args.Unit); err != nil {
		return finish("unknown", "outcome_unknown")
	}
	after, err := c.assistantServices.inspect(ctx, args.Unit)
	if err != nil {
		return finish("accepted", "")
	}
	result.Service = &after
	confirmed := (args.Operation == "stop" && after.ActiveState == "inactive") || (args.Operation == "start" && after.ActiveState == "active") || (args.Operation == "restart" && after.ActiveState == "active" && after.InvocationID != "" && after.InvocationID != before.InvocationID)
	if confirmed {
		result.Verification = "observed"
		return finish("confirmed", "")
	}
	return finish("accepted", "")
}
