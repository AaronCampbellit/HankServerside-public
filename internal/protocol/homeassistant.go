package protocol

import (
	"encoding/json"
	"time"
)

type HomeAssistantHealthResponse struct {
	OK        bool      `json:"ok"`
	CheckedAt time.Time `json:"checked_at"`
}

type HomeAssistantState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes,omitempty"`
	LastChanged *time.Time     `json:"last_changed,omitempty"`
	LastUpdated *time.Time     `json:"last_updated,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

type HomeAssistantFetchStateRequest struct {
	EntityID string `json:"entity_id"`
}

type HomeAssistantFetchStatesResponse struct {
	States []HomeAssistantState `json:"states"`
}

type HomeAssistantFetchStateResponse struct {
	State HomeAssistantState `json:"state"`
}

type HomeAssistantCallServiceRequest struct {
	Domain  string          `json:"domain"`
	Service string          `json:"service"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type HomeAssistantCallServiceResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
}
