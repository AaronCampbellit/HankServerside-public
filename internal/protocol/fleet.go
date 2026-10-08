package protocol

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

const CapabilityFleetV1 = "fleet.v1"
const FleetMaxFileBytes = 512 * 1024
const FleetMaxOutputBytes = 1024 * 1024
const FleetMaxJobs = 4

var fleetIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,96}$`)
var FleetOperations = []string{"workspace.read", "workspace.write", "job.run", "job.read", "job.cancel"}

func ValidFleetID(id string) bool { return fleetIDPattern.MatchString(id) }
func ValidateFleetOperations(ops []string) error {
	if len(ops) == 0 || len(ops) > len(FleetOperations) {
		return errors.New("select one or more fleet operations")
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if !slices.Contains(FleetOperations, op) || seen[op] {
			return errors.New("invalid or duplicate fleet operation")
		}
		seen[op] = true
	}
	return nil
}

type FleetRequest struct {
	GrantID        string  `json:"grant_id"`
	WorkspaceID    string  `json:"workspace_id,omitempty"`
	JobID          string  `json:"job_id,omitempty"`
	Path           string  `json:"path,omitempty"`
	ContentBase64  string  `json:"content_base64,omitempty"`
	Revision       string  `json:"revision,omitempty"`
	Command        string  `json:"command,omitempty"`
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
	After          uint64  `json:"after,omitempty"`
}

func (r FleetRequest) Validate(command string) error {
	if !ValidFleetID(r.GrantID) {
		return errors.New("invalid grant")
	}
	switch command {
	case "fleet.workspace.create":
		if !ValidFleetID(r.WorkspaceID) {
			return errors.New("invalid workspace")
		}
	case "fleet.workspace.read", "fleet.workspace.write":
		if !ValidFleetID(r.WorkspaceID) || r.Path == "" || len(r.Path) > 1024 || strings.ContainsRune(r.Path, 0) {
			return errors.New("invalid workspace file")
		}
	case "fleet.job.start":
		if !ValidFleetID(r.WorkspaceID) || !ValidFleetID(r.JobID) {
			return errors.New("invalid job or workspace")
		}
		return (ShellExecRequest{Command: r.Command, TimeoutSeconds: r.TimeoutSeconds}).Validate()
	case "fleet.job.read", "fleet.job.cancel":
		if !ValidFleetID(r.JobID) {
			return errors.New("invalid job")
		}
	default:
		return errors.New("unknown fleet command")
	}
	return nil
}

type FleetOutput struct {
	Cursor uint64 `json:"cursor"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type FleetJob struct {
	ID          string        `json:"job_id"`
	GrantID     string        `json:"grant_id"`
	WorkspaceID string        `json:"workspace_id"`
	State       string        `json:"state"`
	ExitCode    *int          `json:"exit_code,omitempty"`
	Cursor      uint64        `json:"cursor"`
	Truncated   bool          `json:"truncated"`
	Output      []FleetOutput `json:"output,omitempty"`
}

func FleetTerminal(state string) bool {
	return slices.Contains([]string{"succeeded", "failed", "cancelled", "timed_out", "unknown"}, state)
}
