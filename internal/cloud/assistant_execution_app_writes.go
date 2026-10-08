package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

// Opaque apps have no destination receipt contract. Only a freshly committed
// intent can dispatch; recovery never guesses whether an app effect happened.
func (s *Server) advanceAssistantExecutionAppWrite(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint, call assistant.Call, step domain.AssistantTaskStep, registry *assistant.Registry) (domain.AssistantTask, error) {
	id := executionOperationID(task.ID, call.ID)
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, id)
	fresh := false
	if errors.Is(err, store.ErrNotFound) {
		approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
		if e != nil {
			return task, e
		}
		if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS {
			return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
		}
		receipt = domain.AssistantOperationReceipt{OperationID: id, HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}
		task, receipt, fresh, err = s.store.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	}
	if err != nil {
		return task, err
	}
	if receipt.Tool != call.Tool || receipt.ActionDigest != step.ActionDigest {
		return task, store.ErrConflict
	}
	if receipt.Outcome == "confirmed" || receipt.Outcome == "failed" {
		return task, nil
	}
	reserve := int64(0)
	started := time.Now()
	finish := func(result assistant.Result, outcome string) (domain.AssistantTask, error) {
		if reserve > 0 {
			task.ActiveMS = task.ActiveMS - reserve + min(reserve, time.Since(started).Milliseconds())
		}
		result.OperationID = &id
		result.Outcome = outcome
		receipt.Outcome = outcome
		receipt.Result, _ = json.Marshal(result)
		return s.store.FinishAssistantOperation(ctx, task, receipt)
	}
	unknown := func() (domain.AssistantTask, error) {
		result := assistant.Failure(call.ID, "outcome_unknown")
		result.Verification = "unavailable"
		return finish(result, "unknown")
	}
	if !fresh {
		return unknown()
	}
	reserve = min(int64(30000), task.MaxActiveMS-task.ActiveMS)
	task.ActiveMS += reserve
	task.Calls++
	task, _, err = s.store.StartAssistantTaskStep(ctx, task, call.ID)
	if err != nil {
		return task, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(reserve)*time.Millisecond)
	defer cancel()
	proposal := registry.Invoke(callCtx, call)
	if proposal.Error != nil {
		return finish(assistant.Failure(call.ID, proposal.Error.Code), "failed")
	}
	if proposal.Outcome != "not_started" || assistantActionDigest(task, call, &proposal) != step.ActionDigest {
		return finish(assistant.Failure(call.ID, "revision_conflict"), "failed")
	}
	var args struct {
		AppID     string `json:"app_id"`
		CommandID string `json:"command_id"`
		Query     string `json:"query"`
	}
	var data executionData
	if json.Unmarshal(call.Arguments, &args) != nil || json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 {
		return finish(assistant.Failure(call.ID, "invalid_arguments"), "failed")
	}
	slash := ""
	if fields := strings.Fields(task.RequestText); len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		slash = fields[0]
	}
	input, e := installedAppSlashInput(assistantIntent{AppID: args.AppID, CommandID: args.CommandID, Query: args.Query, SlashCommand: slash})
	if e != nil {
		return finish(assistant.Failure(call.ID, "invalid_arguments"), "failed")
	}
	saved, e := s.store.GetAssistantTask(callCtx, task.HomeID, task.UserID, task.ID)
	if e != nil || saved.Revision != task.Revision || saved.State != "running" {
		return task, store.ErrAssistantLeaseLost
	}
	callCtx = context.WithValue(callCtx, appActorContextKey{}, task.UserID)
	envelope, e := s.sendAgentCommandTo(callCtx, task.HomeID, data.Items[0].AgentID, protocol.CommandAppsInvoke, protocol.AppsInvokeRequest{AppID: args.AppID, CommandID: args.CommandID, Input: input})
	if e != nil || envelope.Error != nil {
		return unknown()
	}
	payload, e := protocol.DecodePayload[protocol.AppsInvokeResponse](envelope)
	if e != nil {
		return unknown()
	}
	item := data.Items[0]
	item.Text = executionText(string(payload.Output))
	item.State = "accepted"
	result := executionResult(call, []executionItem{item}, 1, false)
	result.Verification = "unavailable"
	return finish(result, "accepted")
}
