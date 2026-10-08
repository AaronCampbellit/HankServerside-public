package agent

import (
	"context"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"regexp"
	"strings"
	"time"
)

var assistantHAEntityPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

func (c *Client) executeAssistantHAOperation(ctx context.Context, request protocol.AssistantOperationRequest) (protocol.AssistantOperationStatusResponse, error) {
	var args protocol.AssistantHAOperationArguments
	if strictOperationJSON(request.Arguments, &args) != nil || !assistantHAEntityPattern.MatchString(args.EntityID) {
		return protocol.AssistantOperationStatusResponse{}, operations.ErrConflict
	}
	domain := strings.SplitN(args.EntityID, ".", 2)[0]
	if !protocol.AssistantHAServiceAllowed(domain, args.Service) {
		return protocol.AssistantOperationStatusResponse{}, operations.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return protocol.AssistantOperationStatusResponse{}, err
	}
	receipt, fresh, err := c.operationJournal.Begin(request.Identity)
	if err != nil || !fresh {
		return receipt, err
	}
	result := protocol.AssistantOperationResult{EntityID: args.EntityID, Verification: "unavailable"}
	finish := func(outcome, code string) (protocol.AssistantOperationStatusResponse, error) {
		receipt.Outcome = outcome
		result.Code = code
		receipt.Result, _ = json.Marshal(result)
		return receipt, c.operationJournal.Complete(receipt)
	}
	before, err := c.dispatcher.ha.FetchState(ctx, args.EntityID)
	if err != nil {
		return finish("failed", "capability_unavailable")
	}
	updated := ""
	if before.LastUpdated != nil {
		updated = before.LastUpdated.UTC().Format(time.RFC3339Nano)
	}
	if before.EntityID != args.EntityID || before.State != args.PriorState || updated != args.PriorUpdatedAt {
		return finish("failed", "revision_conflict")
	}
	body, _ := json.Marshal(map[string]string{"entity_id": args.EntityID})
	if _, err := c.dispatcher.ha.CallService(ctx, domain, args.Service, body); err != nil {
		return finish("unknown", "outcome_unknown")
	}
	expected := ""
	switch args.Service {
	case "turn_on":
		if domain != "scene" && domain != "script" {
			expected = "on"
		}
	case "turn_off":
		expected = "off"
	}
	if expected == "" {
		return finish("accepted", "")
	}
	// Retry observations only. The service POST is never repeated.
	for attempt := 0; attempt < 3; attempt++ {
		after, err := c.dispatcher.ha.FetchState(ctx, args.EntityID)
		if err == nil && after.EntityID == args.EntityID {
			result.State = after.State
			if after.State == expected {
				result.Verification = "observed"
				return finish("confirmed", "")
			}
		}
		if attempt < 2 {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return finish("accepted", "")
			case <-timer.C:
			}
		}
	}
	return finish("accepted", "")
}
