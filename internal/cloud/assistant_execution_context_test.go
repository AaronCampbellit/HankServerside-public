package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
)

func TestExecutionCompactionPreservesOrderedFoldersAndSelectedObjects(t *testing.T) {
	checkpoint := assistantExecutionCheckpoint{Version: 2, Messages: []assistant.Message{{Role: "user", Text: "Use the second folder after finding it."}}}
	folders := []executionItem{{Type: "folder", ID: "/first", Title: "First", AgentID: "agent", SourceID: "source"}, {Type: "folder", ID: "/second", Title: "Second", AgentID: "agent", SourceID: "source"}}
	listCall := assistant.Call{ID: "folders", Tool: "files.list", Version: 1, Arguments: json.RawMessage(`{"agent_id":"agent","source_id":"source","path":"/"}`)}
	result := executionResult(listCall, folders, 20, false)
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{listCall}}, assistant.Message{Role: "tool", Result: &result})
	selected := assistant.Call{ID: "selected", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"selected-note"}`)}
	selectedResult := executionResult(selected, []executionItem{{Type: "note", ID: "selected-note", Revision: "revision-one", Title: "Chosen note", Text: strings.Repeat("old prose ", 10000)}}, 1, false)
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{selected}}, assistant.Message{Role: "tool", Result: &selectedResult})
	for i := 0; i < 60; i++ {
		checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "assistant", Text: strings.Repeat("old explanation ", 1000)})
	}
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "user", Text: "Continue with the second folder."})
	compressExecutionContext(&checkpoint)
	if len(checkpoint.Messages) >= 40 || checkpoint.ContextNote == "" {
		t.Fatal("old prose not compressed")
	}
	foundFolders, foundSelection := false, false
	for _, message := range checkpoint.Messages {
		if message.Result == nil {
			continue
		}
		var data executionData
		must(t, json.Unmarshal(message.Result.Data, &data))
		if message.Result.CallID == "folders" {
			foundFolders = len(data.Items) == 2 && data.Items[1].ID == "/second" && data.Items[1].SourceID == "source"
		}
		if message.Result.CallID == "selected" {
			foundSelection = len(data.Items) == 1 && data.Items[0].ID == "selected-note" && data.Items[0].Revision == "revision-one" && data.Items[0].Text == ""
		}
	}
	if !foundFolders || !foundSelection {
		t.Fatal("compression lost ordered/selected identities")
	}
}
func TestExecutionContextRevocationDoesNotReachModel(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	checkpoint := assistantExecutionCheckpoint{Version: 2, Messages: []assistant.Message{{Role: "user", Text: "Continue"}, {Role: "assistant", Text: "private stale answer"}, {Role: "tool", Result: &assistant.Result{CallID: "old", Resources: []assistant.Resource{{Type: "note", ID: "deleted-note", Scope: "personal"}}, Data: json.RawMessage(`{"items":[{"id":"deleted-note","text":"private stale evidence"}]}`)}}}}
	if s.authorizeExecutionContext(ctx, task, checkpoint) {
		t.Fatal("deleted cached note still authorized")
	}
	discardExecutionEvidence(&checkpoint)
	raw, _ := json.Marshal(checkpoint)
	if strings.Contains(string(raw), "private stale") || strings.Contains(string(raw), "deleted-note") {
		t.Fatal("revoked evidence retained in model context")
	}
	if len(checkpoint.Messages) != 1 || checkpoint.Messages[0].Role != "user" {
		t.Fatal("original user instruction lost")
	}
	claimed, err := s.store.ClaimAssistantTask(ctx, "revocation-worker", time.Minute)
	must(t, err)
	checkpoint.Messages = append(checkpoint.Messages, assistant.Message{Role: "tool", Result: &assistant.Result{CallID: "revoked", Resources: []assistant.Resource{{Type: "note", ID: "deleted-note"}}, Data: json.RawMessage(`{"items":[{"text":"private stale evidence"}]}`)}})
	claimed, err = encodeExecutionCheckpoint(claimed, checkpoint)
	must(t, err)
	claimed, err = s.store.SaveAssistantTask(ctx, claimed, "running", "")
	must(t, err)
	called := false
	_, err = s.advanceAssistantExecution(ctx, claimed, executionModelFunc(func(_ context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
		called = true
		raw, _ := json.Marshal(input)
		if strings.Contains(string(raw), "private stale") || strings.Contains(string(raw), "deleted-note") {
			t.Fatal("revoked content reached provider")
		}
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "final", Text: "Please search again."}, nil
	}))
	must(t, err)
	if !called {
		t.Fatal("provider assertion not exercised")
	}
}
