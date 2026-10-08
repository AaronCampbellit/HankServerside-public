package protocol

import "encoding/json"

// Advertise only when the agent/client can persist operation receipts before
// effects and report their status after reconnect. Older peers remain v1.
const CapabilityAssistantOperationsV2 = "assistant.operations.v2"
const CommandAssistantOperationStage = "assistant.operation_stage"
const CommandAssistantOperationExecute = "assistant.operation_execute"
const CapabilityAssistantJournalEpochPrefix = "assistant.receipts.epoch."
const CommandAssistantOperationStatus = "assistant.operation_status"

type AssistantOperationIdentity struct {
	JournalEpoch string `json:"journal_epoch,omitempty"`
	OperationID  string `json:"operation_id"`
	ActionDigest string `json:"action_digest"`
	HomeID       string `json:"home_id"`
	UserID       string `json:"user_id"`
	AgentID      string `json:"agent_id,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	Tool         string `json:"tool"`
	ToolVersion  int    `json:"tool_version"`
}

type AssistantOperationRequest struct {
	Identity  AssistantOperationIdentity `json:"identity"`
	Arguments json.RawMessage            `json:"arguments"`
}

type AssistantOperationStatusRequest struct {
	Identity AssistantOperationIdentity `json:"identity"`
}

type AssistantOperationStatusResponse struct {
	Identity AssistantOperationIdentity `json:"identity"`
	Outcome  string                     `json:"outcome"`
	Result   json.RawMessage            `json:"result,omitempty"`
}

// AssistantFolderOperationArguments names one exact destination. No implicit
// parent creation or replacement is permitted.
type AssistantFolderOperationArguments struct {
	SourceID string `json:"source_id"`
	Path     string `json:"path"`
}

type AssistantOperationResult struct {
	Service      *AssistantServiceState `json:"service,omitempty"`
	EntityID     string                 `json:"entity_id,omitempty"`
	State        string                 `json:"state,omitempty"`
	Code         string                 `json:"code,omitempty"`
	Verification string                 `json:"verification"`
	Item         *FileItem              `json:"item,omitempty"`
}

type AssistantUploadOperationArguments struct {
	SourceID       string `json:"source_id"`
	Path           string `json:"path"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	SizeBytes      int64  `json:"size_bytes"`
}

type AssistantOperationStageRequest struct {
	Identity       AssistantOperationIdentity `json:"identity"`
	ChecksumSHA256 string                     `json:"checksum_sha256"`
	SizeBytes      int64                      `json:"size_bytes"`
	Offset         int64                      `json:"offset"`
	Data           []byte                     `json:"data"`
}

type AssistantOperationStageResponse struct {
	Identity   AssistantOperationIdentity `json:"identity"`
	NextOffset int64                      `json:"next_offset"`
}

type AssistantHAOperationArguments struct {
	EntityID       string `json:"entity_id"`
	Service        string `json:"service"`
	PriorState     string `json:"prior_state"`
	PriorUpdatedAt string `json:"prior_updated_at,omitempty"`
}

// AssistantHAServiceAllowed is intentionally narrower than raw HA service APIs.
func AssistantHAServiceAllowed(domain, service string) bool {
	switch service {
	case "turn_on", "turn_off":
		switch domain {
		case "light", "switch", "fan", "input_boolean", "automation", "humidifier":
			return true
		case "scene", "script":
			return service == "turn_on"
		}
	case "press":
		return domain == "button" || domain == "input_button"
	}
	return false
}

const CommandAssistantServiceInspect = "assistant.service_inspect"

type AssistantServiceGrant struct {
	Unit       string   `json:"unit"`
	Operations []string `json:"operations"`
}

type AssistantServiceState struct {
	Unit              string   `json:"unit"`
	ActiveState       string   `json:"active_state"`
	SubState          string   `json:"sub_state"`
	InvocationID      string   `json:"invocation_id"`
	AllowedOperations []string `json:"allowed_operations"`
}

type AssistantServiceInspectRequest struct {
	Unit string `json:"unit"`
}
type AssistantServiceInspectResponse struct {
	Services []AssistantServiceState `json:"services"`
}
type AssistantServiceOperationArguments struct {
	Unit       string                `json:"unit"`
	Operation  string                `json:"operation"`
	PriorState AssistantServiceState `json:"prior_state"`
}
