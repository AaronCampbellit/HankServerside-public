package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestRegistryArgumentFeedbackNamesConstraintsWithoutPrivateValues(t *testing.T) {
	def := testDefinition(t)
	def.InputSchema = testSchema(t, `{"type":"object","additionalProperties":false,"required":["path","limit"],"properties":{"path":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":50}}}`)
	registry := NewRegistry()
	calls := 0
	if err := registry.Register(def, func(_ context.Context, call Call) Result {
		calls++
		return Success(call.ID, map[string]string{"text": "ok"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"path":"private-canary","limit":100}`, `{"path":"","private-canary":"private-value"}`} {
		result := registry.Invoke(context.Background(), Call{ID: "bad", Tool: def.Name, Version: 1, Arguments: json.RawMessage(args)})
		if result.Error == nil || result.Error.Code != "invalid_arguments" || !strings.Contains(result.Error.NextAction, "limit") || !strings.Contains(result.Error.NextAction, "maximum 50") {
			t.Fatalf("missing correction guidance: %+v", result.Error)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "private-canary") || strings.Contains(string(raw), "private-value") {
			t.Fatal("argument feedback echoed private data")
		}
	}
	if calls != 0 {
		t.Fatal("invalid arguments dispatched")
	}
}

func testSchema(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}
func testDefinition(t *testing.T) Definition {
	return Definition{Name: "notes.get", Version: 1, Description: "Read a note", PermissionPolicy: "notes_read", Effect: "read", Approval: "never", Retry: "safe_read", Idempotency: "read_only", Verification: "not_applicable", TimeoutMS: 1000, MaxResultBytes: 4096, InputSchema: testSchema(t, `{"type":"object","additionalProperties":false,"required":["note_id"],"properties":{"note_id":{"type":"string","minLength":1,"maxLength":64}}}`), OutputSchema: testSchema(t, `{"type":"object","additionalProperties":false,"required":["text"],"properties":{"text":{"type":"string","maxLength":64}}}`)}
}

func TestRegistryRejectsUntrustedCallsBeforeHandler(t *testing.T) {
	r := NewRegistry()
	calls := 0
	if err := r.Register(testDefinition(t), func(ctx context.Context, c Call) Result {
		calls++
		return Success(c.ID, map[string]string{"text": "synthetic"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"note_id":"one","note_id":"two"}`, `{"note_id":"one","user_id":"victim"}`, `{"note_id":5}`, `{}`, `{"note_id":"one"} {}`, `null`, `[1]`} {
		result := r.Invoke(context.Background(), Call{ID: "c1", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(raw)})
		if result.Error == nil || result.Error.Code != "invalid_arguments" {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached executor")
	}
	result := r.Invoke(context.Background(), Call{ID: "c2", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"one"}`)})
	if result.Error != nil || calls != 1 {
		t.Fatalf("valid invocation failed: %#v", result)
	}
}

func TestRegistryRejectsBadOutputAndWriteSuccessClaim(t *testing.T) {
	for _, write := range []bool{false, true} {
		def := testDefinition(t)
		if write {
			def.Effect = "write"
			def.Approval = "required"
			def.Retry = "reconcile"
			def.Idempotency = "reconcile_only"
			def.Verification = "readback"
		}
		r := NewRegistry()
		if err := r.Register(def, func(ctx context.Context, c Call) Result {
			data := map[string]string{"text": "synthetic"}
			if !write {
				data["unexpected"] = "private"
			}
			return Success("forged-call", data, nil)
		}); err != nil {
			t.Fatal(err)
		}
		result := r.Invoke(context.Background(), Call{ID: "c1", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"one"}`)})
		if result.Error == nil || result.Error.Code != "internal_error" || result.CallID != "c1" {
			t.Fatal("invalid output accepted")
		}
	}
}

func TestRegistryWriteOnlyPreparesAndCannotDowngradeVersion(t *testing.T) {
	def := testDefinition(t)
	def.Effect = "write"
	def.Approval = "required"
	def.Retry = "reconcile"
	def.Idempotency = "reconcile_only"
	def.Verification = "readback"
	r := NewRegistry()
	invoked := 0
	if err := r.Register(def, func(ctx context.Context, c Call) Result {
		invoked++
		v := Success(c.ID, map[string]string{"text": "proposal"}, nil)
		v.Outcome = "not_started"
		v.Verification = "pending"
		return v
	}); err != nil {
		t.Fatal(err)
	}
	call := Call{ID: "c1", Tool: def.Name, Version: 2, Arguments: json.RawMessage(`{"note_id":"one"}`)}
	if r.Invoke(context.Background(), call).Error.Code != "capability_unavailable" || invoked != 0 {
		t.Fatal("unsupported version reached handler")
	}
	call.Version = 1
	result := r.Invoke(context.Background(), call)
	if result.Error != nil || result.Outcome != "not_started" || result.OperationID != nil {
		t.Fatal("prepared write did not remain pending")
	}
}

func TestRegistryRejectsUnsafeDefinition(t *testing.T) {
	def := testDefinition(t)
	def.Effect = "write"
	if err := NewRegistry().Register(def, func(context.Context, Call) Result { return Result{} }); err == nil {
		t.Fatal("write with read safety policy accepted")
	}
	def = testDefinition(t)
	def.InputSchema.AdditionalProperties = nil
	if err := NewRegistry().Register(def, func(context.Context, Call) Result { return Result{} }); err == nil {
		t.Fatal("open input schema accepted")
	}
}

func TestRegistryDefinitionsCannotMutateValidation(t *testing.T) {
	def := testDefinition(t)
	r := NewRegistry()
	if err := r.Register(def, func(ctx context.Context, c Call) Result { return Success(c.ID, map[string]string{"text": "ok"}, nil) }); err != nil {
		t.Fatal(err)
	}
	def.InputSchema.Required = nil
	exported := r.Definitions()
	exported[0].InputSchema.Required = nil
	exported[0].InputSchema.Properties["note_id"].Type = "integer"
	result := r.Invoke(context.Background(), Call{ID: "c1", Tool: def.Name, Version: 1, Arguments: json.RawMessage(`{}`)})
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatal("caller changed the registered schema")
	}
	if r.Definitions()[0].SchemaVersion != 2 || r.Definitions()[0].Kind != "tool_definition" {
		t.Fatal("missing contract identity")
	}
}
