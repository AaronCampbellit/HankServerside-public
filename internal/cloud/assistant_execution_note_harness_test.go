package cloud

import (
	"github.com/dropfile/HankServerside/internal/assistant"
	"testing"
)

func TestExecutionNoteDefinitionsRequireObservedIDsAndRevisions(t *testing.T) {
	defs := []assistant.Definition{
		{Name: "notes.search"}, {Name: "notes.create"},
		{Name: "notes.get", InputSchema: executionSchema(map[string]any{"note_id": executionString(256)}, "note_id")},
		{Name: "notes.append", InputSchema: executionSchema(map[string]any{"note_id": executionString(256), "revision": executionString(256)}, "note_id", "revision")},
	}
	for _, test := range []struct {
		name   string
		result *assistant.Result
		count  int
	}{
		{"undiscovered", nil, 2},
		{"failed", &assistant.Result{Outcome: "failed", Resources: []assistant.Resource{{Type: "note", ID: "invented", Revision: "invented"}}}, 2},
		{"prepared", &assistant.Result{Outcome: "not_started", Resources: []assistant.Resource{{Type: "note", ID: "invented", Revision: "invented"}}}, 2},
		{"read without revision", &assistant.Result{Outcome: "confirmed", Resources: []assistant.Resource{{Type: "note", ID: "observed"}}}, 3},
		{"discovered", &assistant.Result{Outcome: "confirmed", Resources: []assistant.Resource{{Type: "note", ID: "observed", Revision: "revision"}}}, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := executionNoteDefinitions(defs, []assistant.Message{{Role: "tool", Result: test.result}})
			if len(got) != test.count {
				t.Fatalf("tools=%d want=%d", len(got), test.count)
			}
			for _, def := range got {
				if def.Name == "notes.get" || def.Name == "notes.append" {
					if values := def.InputSchema.Properties["note_id"].Enum; len(values) != 1 || values[0] != "observed" {
						t.Fatal("unobserved ID advertised")
					}
				}
				if def.Name == "notes.append" {
					if values := def.InputSchema.Properties["revision"].Enum; len(values) != 1 || values[0] != "revision" {
						t.Fatal("unobserved revision advertised")
					}
				}
			}
			if len(defs[2].InputSchema.Properties["note_id"].Enum) != 0 {
				t.Fatal("shared registry schema mutated")
			}
		})
	}
}
