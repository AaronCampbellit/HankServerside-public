package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// A slash directive constrains the model's tool set. It does not interpret
// natural-language arguments or bypass registry validation and authorization.
// This is staged for execution v2; v1 keeps its existing explicit commands.
type executionSlashDirective struct {
	Query string
	Tools []string
	Call  *assistant.Call
	Error *assistant.ToolError
}

func (s *Server) executionSlash(ctx context.Context, homeID, userID, prompt string) (executionSlashDirective, bool) {
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, "/") {
		return executionSlashDirective{}, false
	}
	command, query := prompt[1:], ""
	if split := strings.IndexFunc(command, unicode.IsSpace); split >= 0 {
		query = strings.TrimSpace(command[split:])
		command = command[:split]
	}
	command = strings.ToLower(command)
	directive := executionSlashDirective{Query: query}
	switch command {
	case "notes":
		directive.Tools = []string{"notes.search", "notes.get", "notes.create", "notes.append"}
	case "append":
		directive.Tools = []string{"notes.search", "notes.get", "notes.append"}
	case "files", "file":
		directive.Tools = []string{"machines.list", "files.sources", "files.search", "files.list", "files.stat", "files.read", "attachments.list"}
		// A bare /files query is a read-only search, including when previous
		// dialogue requested an upload. Write subcommands must be explicit.
		words := strings.Fields(strings.ToLower(query))
		if len(words) > 0 {
			switch words[0] {
			case "upload":
				directive.Tools = append(directive.Tools, "files.upload")
			case "mkdir":
				directive.Tools = append(directive.Tools, "files.create_folder")
			case "create", "make":
				if len(words) > 1 && (words[1] == "folder" || words[1] == "directory" || words[1] == "a" && len(words) > 2 && (words[2] == "folder" || words[2] == "directory")) {
					directive.Tools = append(directive.Tools, "files.create_folder")
				}
			}
		}
	case "ha":
		directive.Tools = []string{"homeassistant.search", "homeassistant.get", "homeassistant.call_service"}
	case "calendar":
		directive.Tools = []string{"calendar.search", "calendar.get"}
	case "docs":
		directive.Tools = []string{"evidence.search", "evidence.read"}
	case "status":
		directive.Tools = []string{"machines.list", "machines.status"}
	default:
		rt, err := s.executionToolContext(ctx, homeID, userID, "app_command")
		if err != nil {
			directive.Error = assistant.Failure("", "permission_denied").Error
			return directive, true
		}
		apps, err := s.store.ListHomeApps(ctx, homeID)
		if err != nil {
			directive.Error = assistant.Failure("", "internal_error").Error
			return directive, true
		}
		for _, app := range apps {
			var commands []protocol.AppSlashCommand
			if json.Unmarshal([]byte(defaultJSONArray(app.SlashCommandsJSON)), &commands) != nil {
				continue
			}
			for _, cmd := range commands {
				if !strings.EqualFold(strings.TrimPrefix(cmd.Command, "/"), command) {
					continue
				}
				if !canUseHomeAgentAppCommand(app, rt.Member, cmd.CommandID) {
					directive.Error = assistant.Failure("", "permission_denied").Error
					return directive, true
				}
				args, _ := json.Marshal(map[string]string{"app_id": app.AppID, "command_id": cmd.CommandID, "version": app.Version, "query": query})
				directive.Tools = []string{"apps.invoke"}
				directive.Call = &assistant.Call{ID: "slash", Tool: "apps.invoke", Version: 1, Arguments: args}
				return directive, true
			}
		}
		directive.Error = assistant.Failure("", "capability_unavailable").Error
	}
	return directive, true
}
