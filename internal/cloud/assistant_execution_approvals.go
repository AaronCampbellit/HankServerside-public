package cloud

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

// Owned snapshots expose the exact approval preview, but never arbitrary
// model arguments or the private checkpoint. Message refresh uses this same
// snapshot so a stopped uncertain operation cannot disappear on reload.
func (s *Server) assistantExecutionSnapshot(ctx context.Context, task domain.AssistantTask) (map[string]any, error) {
	result := assistantTaskSnapshot(task)
	uncertain, err := s.store.AssistantTaskHasUncertainOperations(ctx, task.HomeID, task.UserID, task.ID)
	if err != nil {
		return nil, err
	}
	result["effects_uncertain"] = uncertain
	steps, err := s.store.ListAssistantTaskStepSummaries(ctx, task.HomeID, task.UserID, task.ID)
	if err != nil {
		return nil, err
	}
	calls := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		calls = append(calls, map[string]any{"call_id": step.CallID, "tool": step.Tool, "state": step.State, "attempts": step.Attempts})
	}
	result["calls"] = calls
	if task.State != "waiting_approval" {
		return result, nil
	}
	for _, summary := range steps {
		if summary.State != "waiting_approval" {
			continue
		}
		approval, e := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, summary.CallID)
		if e != nil {
			return nil, e
		}
		if approval.State != "pending" {
			continue
		}
		step, e := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, summary.CallID)
		if e != nil {
			return nil, e
		}
		var proposal assistant.Result
		if json.Unmarshal(step.Result, &proposal) != nil {
			continue
		}
		preview := executionApprovalSummary(step, proposal)
		if preview == nil {
			continue
		}
		result["pending_approval"] = map[string]any{"schema_version": 2, "kind": "approval", "approval_id": approval.ID, "task_id": task.ID, "call_id": step.CallID, "action_digest": approval.ActionDigest, "revision": task.Revision, "expires_at": approval.ExpiresAt.UTC().Format(time.RFC3339Nano), "resources": proposal.Resources, "decision": "pending", "consumed": false, "summary": preview}
		break
	}
	return result, nil
}

func executionApprovalSummary(step domain.AssistantTaskStep, proposal assistant.Result) *assistantPendingActionSummary {
	var args struct {
		Title    string `json:"title"`
		Text     string `json:"text"`
		NoteID   string `json:"note_id"`
		Revision string `json:"revision"`
	}
	if json.Unmarshal(step.Arguments, &args) != nil {
		return nil
	}
	switch step.Tool {
	case "apps.invoke":
		var data executionData
		var input struct {
			CommandID string `json:"command_id"`
			Query     string `json:"query"`
		}
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 || json.Unmarshal(step.Arguments, &input) != nil {
			return nil
		}
		item := data.Items[0]
		return &assistantPendingActionSummary{Kind: "app_command", Title: "Run installed app command", Confirmation: "Run this exact app request once? Hank cannot independently verify the app's effects.", Details: []assistantPendingActionDetail{{Label: "App", Value: item.ID}, {Label: "Version", Value: item.Revision}, {Label: "Agent", Value: item.AgentID}, {Label: "Command", Value: input.CommandID}, {Label: "Request", Value: input.Query}}}
	case "machines.service_action":
		var data executionData
		var input struct {
			Operation string `json:"operation"`
		}
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 || data.Items[0].Service == nil || json.Unmarshal(step.Arguments, &input) != nil {
			return nil
		}
		item := data.Items[0]
		return &assistantPendingActionSummary{Kind: "machine_service", Title: "Change machine service", Confirmation: "Run this exact allowlisted service operation? The service may be interrupted.", Destructive: input.Operation != "start", Details: []assistantPendingActionDetail{{Label: "Agent", Value: item.AgentID}, {Label: "Service", Value: item.Service.Unit}, {Label: "Operation", Value: input.Operation}, {Label: "Current state", Value: item.Service.ActiveState}, {Label: "Revision", Value: item.Revision}}}

	case "homeassistant.call_service":
		var data executionData
		var input struct {
			Service string `json:"service"`
		}
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 || json.Unmarshal(step.Arguments, &input) != nil {
			return nil
		}
		item := data.Items[0]
		return &assistantPendingActionSummary{Kind: "ha_control", Title: "Control Home Assistant entity", Confirmation: "Run this exact service on this entity? Hank will check the result where the device exposes a verifiable state.", Details: []assistantPendingActionDetail{{Label: "Agent", Value: item.AgentID}, {Label: "Entity", Value: item.ID}, {Label: "Service", Value: input.Service}, {Label: "Current state", Value: item.State}}}

	case "files.upload":
		var data executionData
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 {
			return nil
		}
		item := data.Items[0]
		return &assistantPendingActionSummary{Kind: "file_upload", Title: "Upload file", Confirmation: "Upload these exact staged bytes? Existing files will not be replaced.", Details: []assistantPendingActionDetail{{Label: "Agent", Value: item.AgentID}, {Label: "Source", Value: item.SourceID}, {Label: "Path", Value: item.Path}, {Label: "Bytes", Value: strconv.FormatInt(item.SizeBytes, 10)}, {Label: "SHA-256", Value: item.ChecksumSHA256}}}
	case "files.create_folder":
		var data executionData
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 {
			return nil
		}
		item := data.Items[0]
		return &assistantPendingActionSummary{Kind: "folder_create", Title: "Create folder", Confirmation: "Create this exact folder? Existing folders will not be replaced.", Details: []assistantPendingActionDetail{{Label: "Agent", Value: item.AgentID}, {Label: "Source", Value: item.SourceID}, {Label: "Path", Value: item.Path}}}

	case "notes.create":
		summary := assistantPendingActionSummaryFromAction(assistantPendingAction{Kind: "note_create", NoteCreate: &assistantPendingNoteCreate{Title: args.Title, BodyMarkdown: args.Text, Scope: "personal"}})
		summary.Details = append(summary.Details, assistantPendingActionDetail{Label: "Exact content", Value: args.Text})
		return summary
	case "notes.append":
		var data executionData
		if json.Unmarshal(proposal.Data, &data) != nil || len(data.Items) != 1 {
			return nil
		}
		summary := assistantPendingActionSummaryFromAction(assistantPendingAction{Kind: "note_append", NoteAppend: &assistantPendingNoteAppend{TargetNoteID: args.NoteID, TargetTitle: data.Items[0].Title, TargetScope: data.Items[0].Scope, AppendedText: args.Text}})
		summary.Summary = "Hank will preserve the existing note and append this exact text after a newline."
		summary.Details = append(summary.Details, assistantPendingActionDetail{Label: "Note ID", Value: args.NoteID}, assistantPendingActionDetail{Label: "Revision", Value: args.Revision})
		return summary
	}
	return nil
}
