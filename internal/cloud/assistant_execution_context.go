package cloud

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// Compression keeps entire tool-call/result groups. The latest ordered result
// set and inspected object of each resource type survive older prose removal.
func compressExecutionContext(checkpoint *assistantExecutionCheckpoint) {
	raw, _ := json.Marshal(checkpoint.Messages)
	if len(raw) < 96<<10 && len(checkpoint.Messages) < 48 {
		return
	}
	protected := map[string]string{}
	calls := map[string]assistant.Call{}
	for _, message := range checkpoint.Messages {
		for _, call := range message.Calls {
			calls[call.ID] = call
		}
		if message.Result == nil {
			continue
		}
		call := calls[message.Result.CallID]
		mode := "selected"
		if strings.HasSuffix(call.Tool, ".search") || strings.HasSuffix(call.Tool, ".list") || strings.HasSuffix(call.Tool, ".sources") {
			mode = "ordered"
		}
		for _, resource := range message.Result.Resources {
			protected[resource.Type+":"+mode] = message.Result.CallID
		}
	}
	keepCalls := map[string]bool{}
	for _, id := range protected {
		keepCalls[id] = true
	}
	for _, call := range checkpoint.Pending {
		keepCalls[call.ID] = true
	}
	cutoff := max(0, len(checkpoint.Messages)-24)
	for index, message := range checkpoint.Messages {
		if index >= cutoff {
			for _, call := range message.Calls {
				keepCalls[call.ID] = true
			}
		}
	}
	for _, message := range checkpoint.Messages {
		keep := false
		for _, call := range message.Calls {
			keep = keep || keepCalls[call.ID]
		}
		if keep {
			for _, call := range message.Calls {
				keepCalls[call.ID] = true
			}
		}
	}
	kept := make([]assistant.Message, 0, len(checkpoint.Messages))
	for index, message := range checkpoint.Messages {
		switch message.Role {
		case "assistant":
			if len(message.Calls) > 0 {
				if !keepCalls[message.Calls[0].ID] {
					continue
				}
			} else if index < cutoff {
				continue
			}
			if index < cutoff {
				message.Text = ""
				message.Continuation = ""
			}
		case "tool":
			if message.Result == nil || !keepCalls[message.Result.CallID] {
				continue
			}
			if index < cutoff {
				var data executionData
				if json.Unmarshal(message.Result.Data, &data) == nil {
					for i := range data.Items {
						data.Items[i].Text = ""
					}
					copy := *message.Result
					copy.Data, _ = json.Marshal(data)
					message.Result = &copy
				}
			}
		case "user":
			// Original task instructions and user selections are not summarized by a
			// model or discarded; they remain authoritative across compression.
		}
		kept = append(kept, message)
	}
	checkpoint.Messages = kept
	checkpoint.ContextNote = "Older prose was shortened. Ordered result items, exact identifiers, inspected objects and user instructions are retained. Read sources again when their full text is needed."
}

func discardExecutionEvidence(checkpoint *assistantExecutionCheckpoint) {
	messages := []assistant.Message{}
	for _, message := range checkpoint.Messages {
		if message.Role == "user" {
			messages = append(messages, message)
		}
	}
	checkpoint.Messages = messages
	checkpoint.FinalText = ""
	checkpoint.ContextNote = "Previously retrieved sources are no longer authorized or available. Search current permitted sources again; do not rely on prior answers."
}

// Recheck stored observations before sending them to a provider, including
// after a model response arrives. A revoked source must not survive as memory.
func (s *Server) authorizeExecutionContext(ctx context.Context, task domain.AssistantTask, checkpoint assistantExecutionCheckpoint) bool {
	rt, err := s.executionToolContext(ctx, task.HomeID, task.UserID, "home_member")
	if err != nil {
		return false
	}
	seen := map[assistant.Resource]bool{}
	var notes map[string]bool
	var calendars map[string]bool
	var docs map[string]bool
	for _, message := range checkpoint.Messages {
		if message.Result == nil {
			continue
		}
		for _, resource := range message.Result.Resources {
			if seen[resource] && resource.Type != "app" {
				continue
			}
			seen[resource] = true
			switch resource.Type {
			case "app":
				origin := message.OriginTaskID
				if origin == "" {
					origin = task.ID
				}
				step, e := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, origin, message.Result.CallID)
				if e != nil || step.Tool != "apps.invoke" {
					return false
				}
				var input struct {
					AppID     string `json:"app_id"`
					CommandID string `json:"command_id"`
				}
				if json.Unmarshal(step.Arguments, &input) != nil || input.AppID != resource.ID {
					return false
				}
				app, e := s.store.GetHomeApp(ctx, task.HomeID, resource.ID)
				if e != nil || !canUseHomeAgentAppCommand(app, rt.Member, input.CommandID) {
					return false
				}
			case "note":
				if notes == nil {
					notes = map[string]bool{}
					if _, err = s.executionToolContext(ctx, task.HomeID, task.UserID, "notes_read"); err != nil {
						return false
					}
					visible, e := s.assistantVisibleNotes(ctx, task.HomeID, task.UserID, rt.Settings)
					if e != nil {
						return false
					}
					for _, note := range visible {
						if note.DeletedAt == nil && ((note.HomeID == "" && rt.Settings.ProfileNotesEnabled) || (note.HomeID == task.HomeID && rt.Settings.HomeNotesEnabled)) {
							notes[note.ID] = true
						}
					}
				}
				if !notes[resource.ID] {
					return false
				}
			case "calendar_event", "calendar":
				if !rt.Settings.CalendarEnabled {
					return false
				}
				if calendars == nil {
					calendars = map[string]bool{}
					entries, e := s.store.ListAssistantCalendarEntries(ctx, task.HomeID, task.UserID)
					if e != nil {
						return false
					}
					for _, entry := range entries {
						calendars[entry.ID+"\x00"+entry.DeviceID] = true
						calendars[entry.CalendarID+"\x00"+entry.DeviceID] = true
					}
				}
				if !calendars[resource.ID+"\x00"+resource.DeviceID] {
					return false
				}
			case "file", "folder":
				if resource.Type == "file" && resource.Scope == "personal" && resource.AgentID == "" && resource.SourceID == "" {
					if _, e := s.store.GetAssistantStage(ctx, task.HomeID, task.UserID, task.SessionID, resource.ID); e != nil {
						return false
					}
					continue
				}
				if _, err = s.executionToolContext(ctx, task.HomeID, task.UserID, "files_read"); err != nil {
					return false
				}
				agent, e := s.store.GetAgentByID(ctx, resource.AgentID)
				if e != nil || agent.HomeID != task.HomeID || !validExecutionPath(resource.ID) {
					return false
				}
				profile, e := s.store.GetHomeServiceProfile(ctx, task.HomeID, domain.ServiceTypeSMB)
				if e != nil {
					return false
				}
				found := false
				for _, id := range assistantFileIndexSourceIDsFromProfileConfig(profile.PublicConfigJSON) {
					found = found || id == resource.SourceID
				}
				if !found {
					return false
				}
				body, _ := json.Marshal(protocol.FilesStatRequest{SourceID: resource.SourceID, Path: resource.ID})
				if s.authorizeFileCommandPolicy(ctx, task.HomeID, protocol.RoutedCommand{Command: "files.stat", Body: body}) != nil {
					return false
				}
			case "homeassistant_entity":
				if _, err = s.executionToolContext(ctx, task.HomeID, task.UserID, "homeassistant_read"); err != nil {
					return false
				}
				agent, e := s.store.GetAgentByID(ctx, resource.AgentID)
				if e != nil || agent.HomeID != task.HomeID {
					return false
				}
			case "machine":
				if resource.Scope == "home_admin" {
					if _, e := s.executionToolContext(ctx, task.HomeID, task.UserID, "machine_admin"); e != nil {
						return false
					}
				}
				agent, e := s.store.GetAgentByID(ctx, resource.ID)
				if e != nil || agent.HomeID != task.HomeID {
					return false
				}
			case "project_doc":
				if !rt.Settings.ProjectDocsEnabled {
					return false
				}
				if docs == nil {
					docs = map[string]bool{}
					entries, e := loadAssistantProjectDocs(s.assistantAI.ProjectDocsDir)
					if e != nil {
						return false
					}
					for _, entry := range entries {
						docs[entry.Path] = true
					}
				}
				if !docs[resource.ID] {
					return false
				}
			case "conversation":
				if !rt.Settings.ConversationsEnabled {
					return false
				}
				session, e := s.store.GetAssistantSession(ctx, resource.ID)
				if e != nil || session.HomeID != task.HomeID || session.UserID != task.UserID {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
