package assistant

import (
	"context"
	"encoding/json"
	"testing"
)

type scriptedModel func(context.Context, ModelRequest) (Turn, error)

func (model scriptedModel) Next(ctx context.Context, request ModelRequest) (Turn, error) {
	return model(ctx, request)
}
func toolTurn(call Call) Turn {
	return Turn{SchemaVersion: 2, Kind: "model_turn", Calls: []Call{call}, FinishReason: "tool_calls"}
}

func TestEvaluationInspectsObservationsAndStopsAtApproval(t *testing.T) {
	r := NewRegistry()
	read := testDefinition(t)
	if err := r.Register(read, func(_ context.Context, call Call) Result {
		return Success(call.ID, map[string]string{"text": "resolved exact ID"}, []Resource{{Type: "note", ID: "owned-note", Scope: "personal"}})
	}); err != nil {
		t.Fatal(err)
	}
	write := testDefinition(t)
	write.Name, write.Effect, write.Approval, write.Retry, write.Idempotency, write.Verification = "notes.append", "write", "required", "reconcile", "reconcile_only", "readback"
	if err := r.Register(write, func(_ context.Context, call Call) Result {
		result := Success(call.ID, map[string]string{"text": "proposal"}, nil)
		result.Outcome = "not_started"
		result.Verification = "pending"
		return result
	}); err != nil {
		t.Fatal(err)
	}
	turns := 0
	model := scriptedModel(func(_ context.Context, request ModelRequest) (Turn, error) {
		turns++
		if turns == 1 {
			return toolTurn(Call{ID: "read", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"owned-note"}`)}), nil
		}
		result := request.Messages[len(request.Messages)-1].Result
		if result == nil || result.Resources[0].ID != "owned-note" {
			t.Fatal("model did not receive structured observation")
		}
		return toolTurn(Call{ID: "prepare", Tool: "notes.append", Version: 1, Arguments: json.RawMessage(`{"note_id":"owned-note"}`)}), nil
	})
	result, err := Evaluate(context.Background(), model, r, []Message{{Role: "user", Text: "append after checking my note"}}, 5, 5)
	if err != nil || result.State != "waiting_approval" || result.Turns != 2 || result.Calls != 2 || len(result.Pending) != 1 {
		t.Fatalf("unexpected execution: %#v %v", result, err)
	}
}

func TestEvaluationRejectsDuplicateCallsAndBoundsLoop(t *testing.T) {
	r := NewRegistry()
	calls := 0
	if err := r.Register(testDefinition(t), func(_ context.Context, call Call) Result {
		calls++
		return Success(call.ID, map[string]string{"text": "ok"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	model := scriptedModel(func(context.Context, ModelRequest) (Turn, error) {
		return toolTurn(Call{ID: "duplicate", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"one"}`)}), nil
	})
	result, err := Evaluate(context.Background(), model, r, nil, 10, 10)
	if err == nil || calls != 1 || result.State != "failed" {
		t.Fatal("replayed call reached adapter")
	}
	result, err = Evaluate(context.Background(), model, r, nil, 1, 10)
	if err == nil || result.Turns != 1 {
		t.Fatal("model exceeded turn budget")
	}
}
