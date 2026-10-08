package assistant

import (
	"context"
	"errors"
)

// Evaluation is a bounded, in-memory driver for synthetic regression scenarios.
// Interactive requests must use the durable worker, not this driver. It stops
// at proposals, exposing the remaining calls for assertions without mutations.
type Evaluation struct {
	State    string
	Messages []Message
	Pending  []Call
	Turns    int
	Calls    int
}

func Evaluate(ctx context.Context, model Model, registry *Registry, messages []Message, maxTurns, maxCalls int) (Evaluation, error) {
	result := Evaluation{State: "running", Messages: append([]Message(nil), messages...)}
	if maxTurns < 1 || maxTurns > 100 || maxCalls < 1 || maxCalls > 1000 {
		return result, errors.New("invalid evaluation budget")
	}
	seen := map[string]bool{}
	for result.Turns < maxTurns {
		if err := ctx.Err(); err != nil {
			result.State = "cancelled"
			return result, err
		}
		turn, err := model.Next(ctx, ModelRequest{Messages: result.Messages, Tools: registry.Definitions()})
		result.Turns++
		if err != nil {
			result.State = "failed"
			return result, errors.New("model unavailable")
		}
		if err := turn.Validate(); err != nil {
			result.State = "failed"
			return result, err
		}
		for _, call := range turn.Calls {
			if seen[call.ID] {
				result.State = "failed"
				return result, errors.New("reused model call ID")
			}
			seen[call.ID] = true
		}
		result.Messages = append(result.Messages, Message{Role: "assistant", Text: turn.Text, Calls: turn.Calls})
		if turn.FinishReason != "tool_calls" {
			switch turn.FinishReason {
			case "final":
				result.State = "completed"
			case "needs_input":
				result.State = "waiting_input"
			default:
				result.State = "failed"
			}
			return result, nil
		}
		for index, call := range turn.Calls {
			if result.Calls >= maxCalls {
				result.State = "failed"
				return result, errors.New("tool budget exhausted")
			}
			observation := registry.Invoke(ctx, call)
			result.Calls++
			result.Messages = append(result.Messages, Message{Role: "tool", Result: &observation})
			if observation.Outcome == "not_started" && observation.Error == nil {
				result.State = "waiting_approval"
				result.Pending = append([]Call(nil), turn.Calls[index:]...)
				return result, nil
			}
		}
	}
	result.State = "failed"
	return result, errors.New("model budget exhausted")
}
