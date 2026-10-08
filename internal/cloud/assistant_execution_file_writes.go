package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
	"io"
	"strings"
	"time"
)

func (s *Server) advanceAssistantExecutionAgentWrite(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint, call assistant.Call, step domain.AssistantTaskStep, registry *assistant.Registry) (domain.AssistantTask, error) {
	operationID := executionOperationID(task.ID, call.ID)
	var proposal assistant.Result
	var data executionData
	if json.Unmarshal(step.Result, &proposal) != nil || json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 {
		return s.failAssistantExecution(ctx, task, checkpoint, "invalid_checkpoint")
	}
	target := data.Items[0]
	epoch := strings.TrimPrefix(target.Revision, "epoch:")
	if target.JournalEpoch != "" {
		epoch = target.JournalEpoch
	}
	if len(epoch) != 32 || epoch == target.Revision {
		return s.failAssistantExecution(ctx, task, checkpoint, "capability_unavailable")
	}
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, operationID)
	if errors.Is(err, store.ErrNotFound) {
		approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID)
		if e != nil {
			return task, e
		}
		if approval.State != "approved" {
			return s.failAssistantExecution(ctx, task, checkpoint, "approval_expired")
		}
		if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS {
			return s.failAssistantExecution(ctx, task, checkpoint, "budget_exhausted")
		}
		receipt = domain.AssistantOperationReceipt{OperationID: operationID, HomeID: task.HomeID, UserID: task.UserID, TaskID: task.ID, CallID: call.ID, Tool: call.Tool, ActionDigest: step.ActionDigest}
		task, receipt, _, err = s.store.BeginAssistantOperation(ctx, task, receipt, approval.ID)
	}
	if err != nil {
		return task, err
	}
	if receipt.Outcome == "confirmed" || receipt.Outcome == "failed" {
		return task, nil
	}
	if receipt.Tool != call.Tool || receipt.ActionDigest != step.ActionDigest {
		return task, store.ErrConflict
	}
	reserveMS := int64(0)
	started := time.Now()
	finish := func(result assistant.Result, outcome string) (domain.AssistantTask, error) {
		if reserveMS > 0 {
			task.ActiveMS = task.ActiveMS - reserveMS + min(reserveMS, time.Since(started).Milliseconds())
		}
		result.OperationID = &operationID
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
	retryReconciliation := func() (domain.AssistantTask, error) {
		if step.Attempts >= 3 || task.Calls >= task.MaxCalls {
			return unknown()
		}
		if reserveMS > 0 {
			task.ActiveMS = task.ActiveMS - reserveMS + min(reserveMS, time.Since(started).Milliseconds())
			reserveMS = 0
		}
		retryAt := time.Now().UTC().Add(time.Duration(step.Attempts*5) * time.Second)
		task.RetryAt = &retryAt
		task.State = "waiting_retry"
		return s.store.SaveAssistantTask(ctx, task, "retrying", call.ID)
	}
	if task.Calls >= task.MaxCalls || task.ActiveMS >= task.MaxActiveMS || step.Attempts >= 3 {
		return unknown()
	}
	reserveMS = min(int64(30000), task.MaxActiveMS-task.ActiveMS)
	task.Calls++
	task.ActiveMS += reserveMS
	task, step, err = s.store.StartAssistantTaskStep(ctx, task, call.ID)
	if err != nil {
		return task, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(reserveMS)*time.Millisecond)
	defer cancel()
	identity := protocol.AssistantOperationIdentity{JournalEpoch: epoch, OperationID: operationID, ActionDigest: step.ActionDigest, HomeID: task.HomeID, UserID: task.UserID, AgentID: target.AgentID, Tool: call.Tool, ToolVersion: call.Version}
	send := func(command string, body any) (protocol.AssistantOperationStatusResponse, error) {
		envelope, e := s.sendAgentCommandTo(callCtx, task.HomeID, target.AgentID, command, body)
		if e != nil || envelope.Error != nil {
			return protocol.AssistantOperationStatusResponse{}, store.ErrConflict
		}
		response, e := protocol.DecodePayload[protocol.AssistantOperationStatusResponse](envelope)
		if e != nil || response.Identity != identity {
			return response, store.ErrConflict
		}
		return response, nil
	}
	// Always reconcile first. Only an explicit absence in the same durable store
	// permits dispatch; a missing/replaced store is not evidence of no effect.
	response, err := send(protocol.CommandAssistantOperationStatus, protocol.AssistantOperationStatusRequest{Identity: identity})
	if err != nil {
		return retryReconciliation()
	}
	if response.Outcome == "not_found" {
		current := registry.Invoke(callCtx, call)
		if current.Error != nil {
			return finish(assistant.Failure(call.ID, current.Error.Code), "failed")
		}
		if current.Outcome != "not_started" || assistantActionDigest(task, call, &current) != step.ActionDigest {
			return finish(assistant.Failure(call.ID, "revision_conflict"), "failed")
		}
		// StartAssistantTaskStep is fenced. A final revision check closes the normal
		// stop window before dispatch; destination receipts cover transport races.
		saved, e := s.store.GetAssistantTask(callCtx, task.HomeID, task.UserID, task.ID)
		if e != nil || saved.Revision != task.Revision || saved.State != "running" {
			return task, store.ErrAssistantLeaseLost
		}
		var args json.RawMessage
		if call.Tool == "machines.service_action" {
			var input struct {
				Operation string `json:"operation"`
			}
			if json.Unmarshal(call.Arguments, &input) != nil || target.Service == nil {
				return finish(assistant.Failure(call.ID, "invalid_arguments"), "failed")
			}
			args, _ = json.Marshal(protocol.AssistantServiceOperationArguments{Unit: target.Service.Unit, Operation: input.Operation, PriorState: *target.Service})
		} else if call.Tool == "homeassistant.call_service" {
			var input struct {
				Service string `json:"service"`
			}
			if json.Unmarshal(call.Arguments, &input) != nil {
				return finish(assistant.Failure(call.ID, "invalid_arguments"), "failed")
			}
			args, _ = json.Marshal(protocol.AssistantHAOperationArguments{EntityID: target.ID, Service: input.Service, PriorState: target.State, PriorUpdatedAt: target.UpdatedAt})
		} else if call.Tool == "files.upload" {
			file, e := s.openAssistantAttachment(callCtx, task.HomeID, task.UserID, task.SessionID, target.StageID)
			if e != nil {
				return finish(assistant.Failure(call.ID, "not_found"), "failed")
			}
			defer file.Close()
			offset := int64(0)
			buffer := make([]byte, 256<<10)
			for offset < target.SizeBytes {
				size := min(int64(len(buffer)), target.SizeBytes-offset)
				if _, e := file.ReadAt(buffer[:size], offset); e != nil && e != io.EOF {
					return unknown()
				}
				envelope, e := s.sendAgentCommandTo(callCtx, task.HomeID, target.AgentID, protocol.CommandAssistantOperationStage, protocol.AssistantOperationStageRequest{Identity: identity, ChecksumSHA256: target.ChecksumSHA256, SizeBytes: target.SizeBytes, Offset: offset, Data: buffer[:size]})
				if e != nil || envelope.Error != nil {
					return unknown()
				}
				staged, e := protocol.DecodePayload[protocol.AssistantOperationStageResponse](envelope)
				if e != nil || staged.Identity != identity || staged.NextOffset < offset+size || staged.NextOffset > target.SizeBytes {
					return unknown()
				}
				offset = staged.NextOffset
			}
			// Transport can take time: recheck current authorization and approval-bound
			// metadata immediately before the first destination effect.
			current = registry.Invoke(callCtx, call)
			if current.Error != nil {
				return finish(assistant.Failure(call.ID, current.Error.Code), "failed")
			}
			if assistantActionDigest(task, call, &current) != step.ActionDigest {
				return finish(assistant.Failure(call.ID, "revision_conflict"), "failed")
			}
			saved, e = s.store.GetAssistantTask(callCtx, task.HomeID, task.UserID, task.ID)
			if e != nil || saved.Revision != task.Revision || saved.State != "running" {
				return task, store.ErrAssistantLeaseLost
			}
			args, _ = json.Marshal(protocol.AssistantUploadOperationArguments{SourceID: target.SourceID, Path: target.Path, ChecksumSHA256: target.ChecksumSHA256, SizeBytes: target.SizeBytes})
		} else {
			args, _ = json.Marshal(protocol.AssistantFolderOperationArguments{SourceID: target.SourceID, Path: target.Path})
		}
		response, err = send(protocol.CommandAssistantOperationExecute, protocol.AssistantOperationRequest{Identity: identity, Arguments: args})
		if err != nil {
			return retryReconciliation()
		}
	}
	var result protocol.AssistantOperationResult
	if json.Unmarshal(response.Result, &result) != nil {
		return unknown()
	}
	if response.Outcome == "failed" && (result.Code == "revision_conflict" || result.Code == "capability_unavailable") {
		return finish(assistant.Failure(call.ID, result.Code), "failed")
	}
	if call.Tool == "machines.service_action" {
		if target.Service == nil || (response.Outcome != "confirmed" && response.Outcome != "accepted") {
			return unknown()
		}
		if result.Service != nil && result.Service.Unit != target.Service.Unit {
			return unknown()
		}
		var input struct {
			Operation string `json:"operation"`
		}
		if json.Unmarshal(call.Arguments, &input) != nil {
			return unknown()
		}
		if response.Outcome == "confirmed" {
			if result.Service == nil || result.Verification != "observed" {
				return unknown()
			}
			state := result.Service
			valid := (input.Operation == "stop" && state.ActiveState == "inactive") || (input.Operation == "start" && state.ActiveState == "active") || (input.Operation == "restart" && state.ActiveState == "active" && state.InvocationID != "" && state.InvocationID != target.Service.InvocationID)
			if !valid {
				return unknown()
			}
		}
		target.Service = result.Service
		if result.Service != nil {
			target.State = result.Service.ActiveState
		} else {
			target.State = ""
		}
		observed := executionResult(call, []executionItem{target}, 1, false)
		observed.Verification = "unavailable"
		if response.Outcome == "confirmed" {
			observed.Verification = "observed"
		}
		return finish(observed, response.Outcome)
	}
	if call.Tool == "homeassistant.call_service" {
		if result.EntityID != target.ID {
			return unknown()
		}
		var input struct {
			Service string `json:"service"`
		}
		if json.Unmarshal(call.Arguments, &input) != nil {
			return unknown()
		}
		expected := ""
		if input.Service == "turn_on" {
			expected = "on"
		} else if input.Service == "turn_off" {
			expected = "off"
		}
		if response.Outcome == "confirmed" && (expected == "" || result.State != expected) {
			return unknown()
		}
		if response.Outcome == "confirmed" && result.Verification != "observed" {
			return unknown()
		}
		if response.Outcome != "confirmed" && response.Outcome != "accepted" {
			return unknown()
		}
		target.State = result.State
		observed := executionResult(call, []executionItem{target}, 1, false)
		observed.Verification = result.Verification
		if response.Outcome == "accepted" {
			observed.Verification = "unavailable"
		}
		return finish(observed, response.Outcome)
	}
	if response.Outcome != "confirmed" || result.Verification != "observed" || result.Item == nil || result.Item.IsDirectory != (call.Tool == "files.create_folder") || cleanPolicyPath(result.Item.Path) != cleanPolicyPath(target.Path) || result.Item.SourceID != target.SourceID {
		return unknown()
	}
	if call.Tool == "files.upload" && result.Item.Size != target.SizeBytes {
		return unknown()
	}
	target.Revision = result.Item.ModifiedAt.Format(time.RFC3339Nano)
	target.URI = executionFileURI(target.AgentID, target.SourceID, target.Path, call.Tool == "files.upload")
	observed := executionResult(call, []executionItem{target}, 1, false)
	observed.Verification = "observed"
	return finish(observed, "confirmed")
}
