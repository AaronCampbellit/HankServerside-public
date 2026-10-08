package assistant

import (
	"context"
	"encoding/json"
	"errors"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
)

// Model implementations adapt the configured provider. Tools and observations
// remain structured; a provider must not flatten tool results into instructions.
type Model interface {
	Next(context.Context, ModelRequest) (Turn, error)
}
type ModelRequest struct {
	Messages []Message    `json:"messages"`
	Tools    []Definition `json:"tools"`
}
type Message struct {
	OriginTaskID string  `json:"origin_task_id,omitempty"`
	Role         string  `json:"role"`
	Continuation string  `json:"continuation,omitempty"`
	Text         string  `json:"text,omitempty"`
	Calls        []Call  `json:"calls,omitempty"`
	Result       *Result `json:"result,omitempty"`
}
type Usage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}
type Turn struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Text          string `json:"text"`
	Calls         []Call `json:"calls"`
	FinishReason  string `json:"finish_reason"`
	Usage         Usage  `json:"usage"`
	Continuation  string `json:"continuation"`
}

func (turn Turn) Validate() error {
	if turn.Calls == nil {
		turn.Calls = []Call{}
	}
	if err := assistantschema.Validate(turn); err != nil {
		return errors.New("invalid model contract")
	}
	if turn.SchemaVersion != 2 || turn.Kind != "model_turn" || len(turn.Text) > 32768 || len(turn.Calls) > 24 || len(turn.Continuation) > 131072 {
		return errors.New("invalid model turn")
	}
	for _, count := range []*int64{turn.Usage.InputTokens, turn.Usage.OutputTokens} {
		if count != nil && *count < 0 {
			return errors.New("invalid usage")
		}
	}
	switch turn.FinishReason {
	case "tool_calls":
		if len(turn.Calls) == 0 || turn.Text != "" {
			return errors.New("ambiguous tool turn")
		}
	case "final", "needs_input", "refused", "length", "error":
		if len(turn.Calls) != 0 {
			return errors.New("ambiguous final turn")
		}
	default:
		return errors.New("invalid finish reason")
	}
	if turn.FinishReason == "final" && turn.Text == "" {
		return errors.New("empty final turn")
	}
	seen := map[string]bool{}
	for _, call := range turn.Calls {
		if call.ID == "" || len(call.ID) > 256 || seen[call.ID] || call.Tool == "" || call.Version < 1 || len(call.Arguments) > MaxArgumentBytes || !json.Valid(call.Arguments) {
			return errors.New("invalid model call")
		}
		seen[call.ID] = true
	}
	return nil
}
