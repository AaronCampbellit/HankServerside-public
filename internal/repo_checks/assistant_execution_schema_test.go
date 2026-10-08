package repo_checks

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestAssistantExecutionSchemaRejectsAmbiguousAndInjectedRecords(t *testing.T) {
	raw, err := os.ReadFile("../../schemas/assistant/execution-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, record string
		valid        bool
	}{
		{"final", `{"schema_version":2,"kind":"model_turn","text":"Done","calls":[],"finish_reason":"final","usage":{"input_tokens":null,"output_tokens":null},"continuation":""}`, true},
		{"call", `{"schema_version":2,"kind":"model_turn","text":"","calls":[{"call_id":"c1","tool":"notes.get","tool_version":1,"arguments":{"note_id":"n1"}}],"finish_reason":"tool_calls","usage":{"input_tokens":100,"output_tokens":20},"continuation":""}`, true},
		{"missing version", `{"kind":"event","task_id":"t1","sequence":1,"type":"searching","call_id":null,"created_at":"2026-09-19T00:00:00Z"}`, false},
		{"private event text", `{"schema_version":2,"kind":"event","task_id":"t1","sequence":1,"type":"searching","call_id":null,"created_at":"2026-09-19T00:00:00Z","text":"private"}`, false},
		{"event", `{"schema_version":2,"kind":"event","task_id":"t1","sequence":1,"type":"searching","call_id":null,"created_at":"2026-09-19T00:00:00Z"}`, true},
		{"ambiguous final", `{"schema_version":2,"kind":"model_turn","text":"Done","calls":[{"call_id":"c1","tool":"notes.get","tool_version":1,"arguments":{}}],"finish_reason":"final","usage":{"input_tokens":null,"output_tokens":null},"continuation":""}`, false},
		{"negative usage", `{"schema_version":2,"kind":"model_turn","text":"Done","calls":[],"finish_reason":"final","usage":{"input_tokens":-1,"output_tokens":null},"continuation":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.record), &value); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("valid=%v want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
