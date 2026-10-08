package cloud

import (
	"encoding/json"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

// File targets must come from authorized observations, never model guesses.
// These observations are reauthorized by the worker before every model/tool
// turn. They supplement, rather than replace, the adapter's live checks.
func executionFileReferences(messages []assistant.Message) (map[string]bool, map[string]bool) {
	targets, stages := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		if message.Result == nil || message.Result.Error != nil || message.Result.Outcome != "confirmed" {
			continue
		}
		for _, resource := range message.Result.Resources {
			if resource.Type != "file" && resource.Type != "folder" {
				continue
			}
			if resource.AgentID != "" && resource.SourceID != "" {
				targets[resource.AgentID+"\x00"+resource.SourceID] = true
			} else if resource.Type == "file" && resource.Scope == "personal" && resource.AgentID == "" && resource.SourceID == "" {
				stages[resource.ID] = true
			}
		}
	}
	return targets, stages
}

func executionFileDefinitions(definitions []assistant.Definition, messages []assistant.Message) []assistant.Definition {
	targets, stages := executionFileReferences(messages)
	folders := executionFileFolders(messages)
	agents, sources := map[string]bool{}, map[string]bool{}
	for target := range targets {
		pair := strings.SplitN(target, "\x00", 2)
		agents[pair[0]], sources[pair[1]] = true, true
	}
	filtered := make([]assistant.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if strings.HasPrefix(definition.Name, "files.") && definition.Name != "files.sources" {
			if len(targets) == 0 || (definition.Name == "files.upload" && (len(stages) == 0 || len(folders) == 0)) {
				continue
			}
			// Provider schemas show only observed identifiers. The worker still
			// enforces exact pairs; enums are guidance, not authorization.
			if definition.InputSchema != nil {
				for name, values := range map[string]map[string]bool{"agent_id": agents, "source_id": sources, "stage_id": stages} {
					if property := definition.InputSchema.Properties[name]; property != nil {
						copy := *property
						copy.Enum = executionIdentifierEnum(values)
						definition.InputSchema.Properties[name] = &copy
					}
				}
				if definition.Name != "files.upload" && definition.Name != "files.create_folder" {
					if property := definition.InputSchema.Properties["path"]; property != nil {
						copy := *property
						paths := map[string]bool{}
						for _, message := range messages {
							if message.Result == nil || message.Result.Outcome != "confirmed" {
								continue
							}
							for _, resource := range message.Result.Resources {
								if resource.AgentID != "" && resource.SourceID != "" && (resource.Type == "folder" || resource.Type == "file") {
									paths[resource.ID] = true
								}
							}
						}
						copy.Enum = executionIdentifierEnum(paths)
						definition.InputSchema.Properties["path"] = &copy
					}
				}
			}
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func executionIdentifierEnum(values map[string]bool) []any {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]any, 0, len(keys))
	for _, key := range keys {
		result = append(result, key)
	}
	return result
}

func executionFileCallError(call assistant.Call, messages []assistant.Message) string {
	if !strings.HasPrefix(call.Tool, "files.") || call.Tool == "files.sources" {
		return ""
	}
	var args map[string]any
	if json.Unmarshal(call.Arguments, &args) != nil {
		return "invalid_arguments"
	}
	targets, stages := executionFileReferences(messages)
	if !targets[executionArg(args, "agent_id")+"\x00"+executionArg(args, "source_id")] {
		return "file_target_unavailable"
	}
	if call.Tool != "files.upload" && call.Tool != "files.create_folder" {
		found := false
		for _, message := range messages {
			if message.Result == nil || message.Result.Outcome != "confirmed" {
				continue
			}
			for _, resource := range message.Result.Resources {
				if (resource.Type == "file" || resource.Type == "folder") && resource.AgentID == executionArg(args, "agent_id") && resource.SourceID == executionArg(args, "source_id") && cleanPolicyPath(resource.ID) == cleanPolicyPath(executionArg(args, "path")) {
					found = true
				}
			}
		}
		if !found {
			return "file_path_unavailable"
		}
	}
	if call.Tool == "files.upload" && !stages[executionArg(args, "stage_id")] {
		return "attachment_unavailable"
	}
	if call.Tool == "files.upload" {
		key := executionArg(args, "agent_id") + "\x00" + executionArg(args, "source_id") + "\x00" + cleanPolicyPath(path.Dir(executionArg(args, "path")))
		if !executionFileFolders(messages)[key] {
			return "file_destination_unavailable"
		}
	}
	return ""
}

// A selection answering a clarification belongs to the same task. Preserve its
// request, but never carry write intent across a new task or a new action.
func executionFileRequestIndex(messages []assistant.Message) int {
	latest := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		if latest < 0 {
			latest = i
		}
		text := strings.ToLower(strings.TrimSpace(messages[i].Text))
		words := strings.Fields(text)
		if messages[i].OriginTaskID == "" || len(words) == 0 || strings.HasPrefix(text, "/") {
			return i
		}
		switch words[0] {
		case "upload", "save", "copy", "attach", "put", "store", "send", "mkdir", "create", "make", "please", "can", "could", "would", "will", "i", "find", "search", "look", "list", "show", "read", "do", "don't", "stop", "cancel", "instead", "no":
			return i
		}
		previous := i - 1
		for previous >= 0 && messages[previous].Role != "user" {
			previous--
		}
		if previous < 0 || messages[previous].OriginTaskID != messages[i].OriginTaskID {
			return i
		}
		question := false
		for j := previous + 1; j < i; j++ {
			question = question || messages[j].Role == "assistant" && len(messages[j].Calls) == 0 && strings.Contains(messages[j].Text, "?")
		}
		if !question {
			return i
		}
		i = previous + 1
	}
	return latest
}

// Recognize explicit file requests for admission budgets and completion checks.
// This is not a general natural-language authorization parser: exact approvals
// and slash-command scopes, rather than keyword matches, authorize writes.
func executionFileWriteRequested(tool string, messages []assistant.Message) bool {
	if i := executionFileRequestIndex(messages); i >= 0 {
		text := strings.ToLower(strings.TrimSpace(messages[i].Text))
		for _, prefix := range []string{"/files ", "/file "} {
			text = strings.TrimPrefix(text, prefix)
		}
		for {
			before := text
			for _, prefix := range []string{"please ", "can you ", "could you ", "would you ", "will you ", "i want you to ", "i need you to ", "i want to ", "i need to "} {
				text = strings.TrimPrefix(text, prefix)
			}
			if before == text {
				break
			}
		}
		words := strings.Fields(text)
		if len(words) == 0 {
			return false
		}
		switch words[0] {
		case "upload":
			return tool == "files.upload"
		case "save", "copy", "attach", "put", "store", "send":
			// These verbs also apply to notes and other domains. Require a
			// file object instead of turning every "save" into an upload.
			for _, word := range words[1:min(len(words), 5)] {
				switch strings.Trim(word, ".,:;?!") {
				case "note", "notes", "event", "message", "email":
					return false
				case "file", "files", "attachment", "attachments":
					return tool == "files.upload"
				}
			}
			return false
		case "mkdir":
			return tool == "files.create_folder"
		case "create", "make":
			for _, word := range words[1:min(len(words), 5)] {
				if word == "folder" || word == "directory" {
					return tool == "files.create_folder"
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// Configured source roots establish routing, not proof that the requested
// destination exists. An upload requires an observed folder from a live read.
func executionFileFolders(messages []assistant.Message) map[string]bool {
	folders := map[string]bool{}
	calls := map[string]string{}
	for _, message := range messages {
		for _, call := range message.Calls {
			calls[call.ID] = call.Tool
		}
		if message.Result == nil || message.Result.Error != nil || message.Result.Outcome != "confirmed" {
			continue
		}
		switch calls[message.Result.CallID] {
		case "files.search", "files.list", "files.stat":
			for _, resource := range message.Result.Resources {
				if resource.Type == "folder" && resource.AgentID != "" && resource.SourceID != "" {
					folders[resource.AgentID+"\x00"+resource.SourceID+"\x00"+cleanPolicyPath(resource.ID)] = true
				}
			}
		}
	}
	return folders
}

// Compare semantic arguments, not provider call IDs or JSON formatting. A new
// user instruction starts a new recovery window; old failures remain history.
func executionRepeatedFailure(call assistant.Call, messages []assistant.Message) bool {
	key := executionCallKey(call)
	failed := map[string]string{}
	calls := map[string]string{}
	tools := map[string]string{}
	for _, message := range messages {
		if message.Role == "user" {
			clear(failed)
			clear(calls)
		}
		for _, previous := range message.Calls {
			calls[previous.ID] = executionCallKey(previous)
			tools[previous.ID] = previous.Tool
		}
		if message.Result != nil && message.Result.Error != nil && !message.Result.Error.Retryable {
			key := calls[message.Result.CallID]
			if failed[key] == "" {
				failed[key] = message.Result.Error.Code
			}
		}
		if message.Result != nil && message.Result.Error == nil && message.Result.Outcome == "confirmed" {
			switch tools[message.Result.CallID] {
			case "files.sources", "attachments.list", "files.search", "files.list", "files.stat":
				// A successful discovery can satisfy a previously missing
				// prerequisite. Permanent execution failures still stay blocked.
				for key, code := range failed {
					switch code {
					case "file_target_unavailable", "file_path_unavailable", "attachment_unavailable", "file_destination_unavailable":
						delete(failed, key)
					}
				}
			}
		}
	}
	return key != "" && failed[key] != ""
}

func executionCallKey(call assistant.Call) string {
	var args any
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.UseNumber()
	if decoder.Decode(&args) != nil {
		return ""
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return call.Tool + "\x00" + string(raw)
}

func executionToolAllowed(tools []string, name string) bool {
	return len(tools) == 0 || slices.Contains(tools, name)
}

// A final model sentence is not evidence that a requested write occurred.
// Keep this check on receipts, and scope it to the current task/instruction.
func executionIncompleteFileWrite(task domain.AssistantTask, messages []assistant.Message) string {
	upload := executionFileWriteRequested("files.upload", messages)
	folder := executionFileWriteRequested("files.create_folder", messages)
	if !upload && !folder {
		return ""
	}
	calls := map[string]assistant.Call{}
	stages, uploaded := map[string]bool{}, map[string]bool{}
	request := ""
	confirmed := false
	requestIndex := executionFileRequestIndex(messages)
	for index, message := range messages {
		if index < requestIndex {
			continue
		}
		if index == requestIndex {
			request = strings.ToLower(message.Text)
			clear(stages)
			clear(uploaded)
			confirmed = false
		}
		for _, call := range message.Calls {
			calls[call.ID] = call
		}
		if message.Result == nil || message.Result.Error != nil || message.Result.Outcome != "confirmed" {
			continue
		}
		call := calls[message.Result.CallID]
		if call.Tool == "attachments.list" {
			for _, resource := range message.Result.Resources {
				if resource.Type == "file" && resource.Scope == "personal" && resource.AgentID == "" {
					stages[resource.ID] = true
				}
			}
		}
		if message.OriginTaskID != "" && message.OriginTaskID != task.ID {
			continue
		}
		if message.Result.Verification != "observed" || message.Result.OperationID == nil {
			continue
		}
		if upload && call.Tool == "files.upload" {
			var args map[string]any
			_ = json.Unmarshal(call.Arguments, &args)
			uploaded[executionArg(args, "stage_id")] = true
			confirmed = true
		}
		if folder && call.Tool == "files.create_folder" {
			confirmed = true
		}
	}
	if !confirmed {
		return "The requested file write has no confirmed operation receipt. Discovery, listing attachments and inspecting a destination do not upload files or create folders. Continue with the requested write tool, or call hank_ask_user if genuinely blocked. Do not finish or claim success."
	}
	// Only an explicit collective request can require every listed attachment.
	// Never broaden an unspecified subset into an instruction to upload all.
	words := strings.Fields(request)
	if upload && (slices.Contains(words, "these") || slices.Contains(words, "all")) {
		for stage := range stages {
			if !uploaded[stage] {
				return "The user requested these/all attachments, but some listed stage IDs have no confirmed upload receipt in this task. Prepare the remaining requested uploads using their exact stage IDs, or call hank_ask_user if blocked. Do not finish or repeat uploads already confirmed."
			}
		}
	}
	return ""
}
