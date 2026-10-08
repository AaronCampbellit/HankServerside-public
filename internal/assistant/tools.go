// Package assistant defines provider-independent, schema-validated capabilities.
// It does not authorize users or dispatch mutations: adapters must authorize
// every invocation, and writes only produce proposals until an executor exists.
package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
	"github.com/google/jsonschema-go/jsonschema"
)

const MaxArgumentBytes = 32 << 10
const MaxResultBytes = 256 << 10

type Call struct {
	ID        string          `json:"call_id"`
	Tool      string          `json:"tool"`
	Version   int             `json:"tool_version"`
	Arguments json.RawMessage `json:"arguments"`
}

type Resource struct {
	DeviceID string `json:"device_id,omitempty"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	Scope    string `json:"scope"`
	AgentID  string `json:"agent_id,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type ToolError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	NextAction string `json:"next_action"`
}

func (e *ToolError) Error() string { return e.Code }

type Result struct {
	SchemaVersion int             `json:"schema_version"`
	Kind          string          `json:"kind"`
	CallID        string          `json:"call_id"`
	OperationID   *string         `json:"operation_id"`
	Outcome       string          `json:"outcome"`
	Data          json.RawMessage `json:"data"`
	Resources     []Resource      `json:"resources"`
	EvidenceIDs   []string        `json:"evidence_ids"`
	Verification  string          `json:"verification"`
	Error         *ToolError      `json:"error"`
}

func Failure(callID, code string) Result {
	message, action, retry := "The operation could not be completed.", "Try again or inspect the task.", false
	switch code {
	case "cancelled":
		message, action = "This call was cancelled.", "Follow the user's latest instruction."
	case "approval_rejected":
		message, action = "The user rejected this action.", "Acknowledge the decision and follow the user’s instructions; do not repeat the rejected action."
	case "outcome_unknown":
		message, action = "The action may have happened but could not be verified.", "Inspect its destination receipt or state before attempting any write."
	case "approval_expired":
		message, action = "The approval expired.", "Prepare a fresh exact proposal."
	case "invalid_arguments":
		message, action = "The tool arguments are invalid.", "Supply fields matching the tool schema."
	case "permission_denied":
		message, action = "Access is not permitted.", "Choose an authorized resource."
	case "not_found":
		message, action = "The resource is unavailable.", "Search again for an accessible resource."
	case "file_target_unavailable":
		message, action = "The agent/source pair has not been resolved. No file or folder lookup was performed.", "Call files.sources and use an exact returned agent_id, source_id and starting path. Do not infer that the folder is missing."
	case "attachment_unavailable":
		message, action = "The staged attachment is unavailable in this conversation.", "Call attachments.list and use an exact returned stage_id. Never use placeholders such as current."
	case "file_destination_unavailable":
		message, action = "The upload destination folder has not been observed.", "Find the requested existing folder with files.search, files.list or files.stat. Copy its exact path and append the attachment filename. Do not substitute a source root or create a folder."
	case "file_path_unavailable":
		message, action = "This path has not been discovered; no filesystem lookup was performed.", "Use files.search with a starting path returned by files.sources (usually /) and the requested folder/file name as query. Copy exact paths from its results, preserving case. Do not guess paths or assume a folder is missing."
	case "action_not_requested":
		message, action = "The current user instruction does not explicitly request this file write.", "Continue the requested investigation. If a write is needed, ask the user to explicitly request upload or folder creation; never infer permission from a failed search."
	case "repeated_call":
		message, action = "This exact call already failed without a retryable error.", "Do not repeat it. Discover valid targets, change the arguments using observed evidence, or ask the user what is missing."
	case "capability_unavailable":
		message, action = "This capability is unavailable.", "Use a supported tool or connect a capable device."
	case "revision_conflict":
		message, action = "The resource changed.", "Read it again and prepare a new action."
	case "agent_offline":
		message, action, retry = "The target agent is offline.", "Wait for the agent to reconnect.", true
	case "client_unavailable":
		message, action = "A capable client is required.", "Open a client that owns this resource."
	case "timeout":
		message, action, retry = "The read timed out.", "Retry within the task budget.", true
	default:
		code = "internal_error"
	}
	return Result{SchemaVersion: 2, Kind: "tool_result", CallID: callID, Outcome: "failed", Data: json.RawMessage(`{}`), Resources: []Resource{}, EvidenceIDs: []string{}, Verification: "not_applicable", Error: &ToolError{Code: code, Message: message, Retryable: retry, NextAction: action}}
}

func Success(callID string, data any, resources []Resource) Result {
	raw, err := json.Marshal(data)
	if err != nil {
		return Failure(callID, "internal_error")
	}
	if resources == nil {
		resources = []Resource{}
	}
	return Result{SchemaVersion: 2, Kind: "tool_result", CallID: callID, Outcome: "confirmed", Data: raw, Resources: resources, EvidenceIDs: []string{}, Verification: "not_applicable"}
}

type Definition struct {
	SchemaVersion        int                `json:"schema_version"`
	Kind                 string             `json:"kind"`
	Name                 string             `json:"name"`
	Version              int                `json:"version"`
	Description          string             `json:"description"`
	InputSchema          *jsonschema.Schema `json:"input_schema"`
	OutputSchema         *jsonschema.Schema `json:"output_schema"`
	Effect               string             `json:"effect"`
	Approval             string             `json:"approval"`
	PermissionPolicy     string             `json:"permission_policy"`
	Retry                string             `json:"retry"`
	Idempotency          string             `json:"idempotency"`
	Verification         string             `json:"verification"`
	RequiredCapabilities []string           `json:"required_capabilities"`
	TimeoutMS            int                `json:"timeout_ms"`
	MaxResultBytes       int                `json:"max_result_bytes"`
}

type Handler func(context.Context, Call) Result

type entry struct {
	definition    Definition
	input, output *jsonschema.Resolved
	handler       Handler
}
type Registry struct{ entries map[string]entry }

func NewRegistry() *Registry { return &Registry{entries: map[string]entry{}} }

func cloneDefinition(def Definition) Definition {
	raw, _ := json.Marshal(def)
	var copied Definition
	_ = json.Unmarshal(raw, &copied)
	return copied
}

func (r *Registry) Register(def Definition, handler Handler) error {
	def = cloneDefinition(def)
	def.SchemaVersion, def.Kind = 2, "tool_definition"
	if def.RequiredCapabilities == nil {
		def.RequiredCapabilities = []string{}
	}

	if def.Name == "" || len(def.Name) > 100 || def.Version < 1 || handler == nil || def.TimeoutMS <= 0 || def.TimeoutMS > 300000 || def.MaxResultBytes <= 0 || def.MaxResultBytes > MaxResultBytes || def.PermissionPolicy == "" {
		return errors.New("invalid tool definition")
	}
	if _, exists := r.entries[def.Name]; exists {
		return errors.New("duplicate tool name")
	}
	if !slices.Contains([]string{"never", "required"}, def.Approval) || !slices.Contains([]string{"safe_read", "idempotent", "reconcile", "never"}, def.Retry) || !slices.Contains([]string{"read_only", "server_transaction", "destination_receipt", "reconcile_only"}, def.Idempotency) || !slices.Contains([]string{"not_applicable", "readback", "receipt", "unavailable"}, def.Verification) {
		return errors.New("invalid safety policy")
	}
	if def.Effect == "read" && (def.Approval != "never" || def.Idempotency != "read_only" || def.Retry != "safe_read" || def.Verification != "not_applicable") {
		return errors.New("invalid read policy")
	}
	if def.Effect != "read" && def.Effect != "write" {
		return errors.New("invalid tool effect")
	}
	if def.Effect == "write" && (def.Approval != "required" || def.Retry == "safe_read" || def.Idempotency == "read_only" || def.Verification == "not_applicable") {
		return errors.New("unsafe write definition")
	}
	for _, schema := range []*jsonschema.Schema{def.InputSchema, def.OutputSchema} {
		if schema == nil || schema.Type != "object" || schema.AdditionalProperties == nil || !reflect.DeepEqual(schema.AdditionalProperties, &jsonschema.Schema{Not: &jsonschema.Schema{}}) {
			return errors.New("tool schema must be a closed object")
		}
	}
	if err := assistantschema.Validate(def); err != nil {
		return errors.New("invalid tool contract")
	}
	input, err := def.InputSchema.Resolve(nil)
	if err != nil {
		return errors.New("invalid input schema")
	}
	output, err := def.OutputSchema.Resolve(nil)
	if err != nil {
		return errors.New("invalid output schema")
	}
	r.entries[def.Name] = entry{def, input, output, handler}
	return nil
}

func (r *Registry) Definitions() []Definition {
	defs := make([]Definition, 0, len(r.entries))
	for _, e := range r.entries {
		defs = append(defs, cloneDefinition(e.definition))
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *Registry) Invoke(ctx context.Context, call Call) Result {
	e, ok := r.entries[call.Tool]
	if !ok || call.Version != e.definition.Version {
		return Failure(call.ID, "capability_unavailable")
	}
	if call.ID == "" || len(call.ID) > 256 || len(call.Arguments) > MaxArgumentBytes {
		return Failure(call.ID, "invalid_arguments")
	}
	value, err := decodeUniqueJSON(call.Arguments)
	if err != nil || e.input.Validate(value) != nil {
		return invalidArgumentsResult(call.ID, e.definition.InputSchema, value)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.definition.TimeoutMS)*time.Millisecond)
	defer cancel()
	if ctx.Err() != nil {
		return Failure(call.ID, "timeout")
	}
	result := e.handler(ctx, call)
	if ctx.Err() != nil {
		return Failure(call.ID, "timeout")
	}
	// A tool never controls the identity of the result or invents an operation receipt.
	result.CallID = call.ID
	result.SchemaVersion = 2
	result.Kind = "tool_result"
	if result.Error != nil {
		return Failure(call.ID, result.Error.Code)
	}
	if e.definition.Effect == "write" && (result.Outcome != "not_started" || result.OperationID != nil || result.Verification != "pending") {
		return Failure(call.ID, "internal_error")
	}
	if e.definition.Effect == "read" && (result.Outcome != "confirmed" || result.OperationID != nil || (result.Verification != "not_applicable" && result.Verification != "observed")) {
		return Failure(call.ID, "internal_error")
	}
	value, err = decodeUniqueJSON(result.Data)
	if err != nil || e.output.Validate(value) != nil {
		return Failure(call.ID, "internal_error")
	}
	if err := assistantschema.Validate(result); err != nil {
		return Failure(call.ID, "internal_error")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > e.definition.MaxResultBytes {
		return Failure(call.ID, "internal_error")
	}
	return result
}

// Explain schema failures using only schema-owned field names and constraints.
// Validator error strings can contain submitted private values, so never copy
// them into diagnostics or provider feedback.
func invalidArgumentsResult(callID string, schema *jsonschema.Schema, value any) Result {
	result := Failure(callID, "invalid_arguments")
	object, ok := value.(map[string]any)
	if !ok {
		return result
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	hints := []string{}
	for _, name := range names {
		property := schema.Properties[name]
		field, exists := object[name]
		missing := !exists && slices.Contains(schema.Required, name)
		invalid := false
		if exists {
			if resolved, err := property.Resolve(nil); err == nil {
				invalid = resolved.Validate(field) != nil
			}
		}
		if !missing && !invalid {
			continue
		}
		hint := name + " must be " + property.Type
		if missing {
			hint = name + " is required (" + property.Type + ")"
		}
		if property.Minimum != nil {
			hint += fmt.Sprintf(", minimum %g", *property.Minimum)
		}
		if property.Maximum != nil {
			hint += fmt.Sprintf(", maximum %g", *property.Maximum)
		}
		if property.MinLength != nil {
			hint += fmt.Sprintf(", minimum length %d", *property.MinLength)
		}
		if property.MaxLength != nil {
			hint += fmt.Sprintf(", maximum length %d", *property.MaxLength)
		}
		if len(property.Enum) > 0 {
			hint += ", use a listed enum value"
		}
		hints = append(hints, hint)
	}
	for name := range object {
		if _, exists := schema.Properties[name]; !exists {
			hints = append(hints, "Remove fields not listed in the tool schema")
			break
		}
	}
	if len(hints) > 0 {
		message := strings.Join(hints, "; ") + ". Correct the arguments before retrying."
		if len(message) <= 1800 {
			result.Error.NextAction = message
		}
	}
	return result
}

// Duplicate keys are rejected before decoding so a target cannot be interpreted
// differently by validation, approval, hashing and execution layers.
func decodeUniqueJSON(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("JSON nesting exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			out := map[string]any{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if _, ok := out[key]; ok {
					return nil, errors.New("duplicate object key")
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out[key] = v
			}
			_, err = d.Token()
			return out, err
		case '[':
			out := []any{}
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err = d.Token()
			return out, err
		default:
			return nil, errors.New("unexpected delimiter")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	// jsonschema validates JSON numeric values represented as float64.
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err = json.Unmarshal(canonical, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
