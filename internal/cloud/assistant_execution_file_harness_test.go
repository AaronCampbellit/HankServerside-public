package cloud

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
)

func TestExecutionFileWriteIntentDoesNotExpandFromConversation(t *testing.T) {
	for _, test := range []struct{ text, allowed string }{
		{"upload these to the existing tax folder", "files.upload"},
		{"Could you please upload these files?", "files.upload"},
		{"Please create a new folder named Receipts", "files.create_folder"},
		{"/files mkdir Receipts", "files.create_folder"},
		{"/files 2025 Taxes", ""},
		{"Find the create folder instructions", ""},
		{"Do not create a folder, just search", ""},
		{"Save this note titled Files", ""},
		{"Save \"Keep this.\"", ""},
		{"Copy the note to my files", ""},
		{"Save these attachments to Taxes", "files.upload"},
	} {
		messages := []assistant.Message{{Role: "user", Text: "Create a folder"}, {Role: "assistant", Text: "Create another folder to repair the search."}, {Role: "user", Text: test.text}}
		for _, tool := range []string{"files.upload", "files.create_folder"} {
			if got := executionFileWriteRequested(tool, messages); got != (test.allowed == tool) {
				t.Errorf("%q: %s allowed=%v", test.text, tool, got)
			}
		}
	}
}

func TestExecutionFileCompletionRequiresCurrentVerifiedReceipts(t *testing.T) {
	task := domain.AssistantTask{ID: "current"}
	messages := []assistant.Message{{Role: "user", Text: "Upload all these attachments to Taxes"}}
	if executionIncompleteFileWrite(task, messages) == "" {
		t.Fatal("prose-only completion accepted")
	}
	list := assistant.Call{ID: "stages", Tool: "attachments.list", Version: 1}
	listed := executionResult(list, []executionItem{{Type: "file", ID: "one", Scope: "personal", Title: "one.txt"}, {Type: "file", ID: "two", Scope: "personal", Title: "two.txt"}}, 50, false)
	messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{list}}, assistant.Message{Role: "tool", Result: &listed})
	appendUpload := func(stage, origin, outcome string) {
		call := assistant.Call{ID: "upload-" + stage, Tool: "files.upload", Version: 1, Arguments: json.RawMessage(`{"stage_id":"` + stage + `"}`)}
		result := executionResult(call, []executionItem{{Type: "file", ID: stage + ".txt", Scope: "home", Title: stage + ".txt"}}, 1, false)
		operation := "operation-" + stage
		result.OperationID, result.Verification, result.Outcome = &operation, "observed", outcome
		messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{call}}, assistant.Message{Role: "tool", OriginTaskID: origin, Result: &result})
	}
	appendUpload("one", task.ID, "confirmed")
	if executionIncompleteFileWrite(task, messages) == "" {
		t.Fatal("partial upload accepted as all")
	}
	appendUpload("two", "previous-task", "confirmed")
	if executionIncompleteFileWrite(task, messages) == "" {
		t.Fatal("old receipt counted for current upload")
	}
	appendUpload("two", task.ID, "accepted")
	if executionIncompleteFileWrite(task, messages) == "" {
		t.Fatal("unverified upload counted")
	}
	appendUpload("two", task.ID, "confirmed")
	if executionIncompleteFileWrite(task, messages) != "" {
		t.Fatal("verified complete upload blocked")
	}
	messages = append(messages, assistant.Message{Role: "user", Text: "/files Taxes"})
	if executionIncompleteFileWrite(task, messages) != "" {
		t.Fatal("search inherited prior upload requirement")
	}
}

func TestExecutionFileDiscoveryAndFailureRecovery(t *testing.T) {
	source := assistant.Call{ID: "sources", Tool: "files.sources", Version: 1, Arguments: json.RawMessage("{}")}
	sourceResult := executionResult(source, []executionItem{{Type: "folder", ID: "/", Scope: "home", Title: "Share", AgentID: "real-agent", SourceID: "real-share", Path: "/"}}, 50, false)
	stage := assistant.Call{ID: "attachments", Tool: "attachments.list", Version: 1, Arguments: json.RawMessage("{}")}
	stageResult := executionResult(stage, []executionItem{{Type: "file", ID: "real-stage", StageID: "real-stage", Scope: "personal", Title: "receipt.txt"}}, 50, false)
	messages := []assistant.Message{{Role: "user", Text: "Upload this to Taxes"}, {Role: "assistant", Calls: []assistant.Call{source}}, {Role: "tool", Result: &sourceResult}}
	defs := []assistant.Definition{{Name: "files.sources"}, {Name: "files.search"}, {Name: "attachments.list"}, {Name: "files.upload"}}
	names := func(messages []assistant.Message) []string {
		names := []string{}
		for _, d := range executionFileDefinitions(defs, messages) {
			names = append(names, d.Name)
		}
		return names
	}
	if got := names(nil); slices.Contains(got, "files.search") || slices.Contains(got, "files.upload") || !slices.Contains(got, "files.sources") {
		t.Fatalf("undiscovered file tools exposed: %v", got)
	}
	if got := names(messages); !slices.Contains(got, "files.search") || slices.Contains(got, "files.upload") {
		t.Fatalf("attachment discovery not required: %v", got)
	}
	read := assistant.Call{ID: "read", Tool: "files.list", Version: 1, Arguments: json.RawMessage(`{"agent_id":"real-agent","source_id":"real-share","path":"taxes","limit":10}`)}
	if code := executionFileCallError(read, messages); code != "file_path_unavailable" {
		t.Fatalf("invented read path accepted: %s", code)
	}
	read.Arguments = json.RawMessage(`{"agent_id":"real-agent","source_id":"real-share","path":"/","limit":10}`)
	if code := executionFileCallError(read, messages); code != "" {
		t.Fatalf("discovered source root rejected: %s", code)
	}
	call := assistant.Call{ID: "upload", Tool: "files.upload", Version: 1, Arguments: json.RawMessage(`{"agent_id":"primary","source_id":"local","stage_id":"current","path":"Taxes"}`)}
	if code := executionFileCallError(call, messages); code != "file_target_unavailable" {
		t.Fatalf("invented target: %s", code)
	}
	call.Arguments = json.RawMessage(`{"agent_id":"real-agent","source_id":"real-share","stage_id":"current","path":"Taxes/receipt.txt"}`)
	if code := executionFileCallError(call, messages); code != "attachment_unavailable" {
		t.Fatalf("invented attachment: %s", code)
	}
	messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{stage}}, assistant.Message{Role: "tool", Result: &stageResult})
	call.Arguments = json.RawMessage(`{"agent_id":"real-agent","source_id":"real-share","stage_id":"real-stage","path":"Taxes/receipt.txt"}`)
	if code := executionFileCallError(call, messages); code != "file_destination_unavailable" {
		t.Fatalf("unobserved destination accepted: %s", code)
	}
	folderCall := assistant.Call{ID: "folder", Tool: "files.search", Version: 1}
	folderResult := executionResult(folderCall, []executionItem{{Type: "folder", ID: "Taxes", Scope: "home", Title: "Taxes", AgentID: "real-agent", SourceID: "real-share", Path: "Taxes"}}, 50, false)
	messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{folderCall}}, assistant.Message{Role: "tool", Result: &folderResult})
	if code := executionFileCallError(call, messages); code != "" || !slices.Contains(names(messages), "files.upload") {
		t.Fatalf("discovered upload unavailable: %s", code)
	}
	failed := assistant.Failure(call.ID, "not_found")
	messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{call}}, assistant.Message{Role: "tool", Result: &failed})
	repeated := call
	repeated.ID = "new-provider-id"
	repeated.Arguments = json.RawMessage(`{ "path":"Taxes/receipt.txt", "stage_id":"real-stage", "source_id":"real-share", "agent_id":"real-agent" }`)
	if !executionRepeatedFailure(repeated, messages) {
		t.Fatal("same failed operation bypassed recovery through new call ID/formatting")
	}
	messages = append(messages, assistant.Message{Role: "user", Text: "I repaired the destination; try again."})
	if executionRepeatedFailure(repeated, messages) {
		t.Fatal("explicit followup could not retry")
	}
	for _, code := range []string{"file_target_unavailable", "file_path_unavailable", "attachment_unavailable", "file_destination_unavailable", "action_not_requested", "repeated_call", "approval_rejected"} {
		must(t, assistantschema.Validate(assistant.Failure("failure", code)))
	}
}

func TestExecutionFileRootSearchAndListing(t *testing.T) {
	s, home, _, registry := executionFixture(t)
	agent := executionFakeAgent(t, s, home, []string{"files.search", "files.list"}, func(command protocol.RoutedCommand) any {
		return protocol.FilesListResponse{Items: []protocol.FileItem{
			{SourceID: "share", Path: "2025 Taxes", Name: "2025 Taxes", IsDirectory: true},
			{SourceID: "share", Path: "2025 Taxes/receipt.txt", Name: "receipt.txt"},
			{SourceID: "other-share", Path: "private.txt", Name: "private.txt"},
			{SourceID: "share", Path: "../private.txt", Name: "private.txt"},
		}}
	})
	for _, tool := range []string{"files.search", "files.list"} {
		args := map[string]any{"agent_id": agent, "source_id": "share", "path": "/", "limit": 10}
		if tool == "files.search" {
			args["query"] = "2025 Taxes"
		}
		items := executionItems(t, invokeExecution(t, registry, tool, args)).Items
		if len(items) != 2 || items[0].Path != "2025 Taxes" {
			t.Fatalf("%s dropped root results or leaked foreign paths: %+v", tool, items)
		}
		args["path"] = "2025 Taxes"
		if items = executionItems(t, invokeExecution(t, registry, tool, args)).Items; len(items) != 2 {
			t.Fatalf("%s broke subtree results: %+v", tool, items)
		}
		args["path"] = "Unrelated"
		if items = executionItems(t, invokeExecution(t, registry, tool, args)).Items; len(items) != 0 {
			t.Fatalf("%s leaked another subtree", tool)
		}
	}
}

func TestExecutionFileSearchCannotPrepareWrites(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	directive, _ := s.executionSlash(ctx, task.HomeID, task.UserID, "/files 2025 Taxes")
	for _, write := range []string{"files.create_folder", "files.upload"} {
		if slices.Contains(directive.Tools, write) {
			t.Fatal("search advertised write", write)
		}
	}
	var err error
	task, err = s.store.ClaimAssistantTask(ctx, "search-worker", time.Minute)
	must(t, err)
	call := assistant.Call{ID: "unrequested", Tool: "files.create_folder", Version: 1, Arguments: json.RawMessage(`{"agent_id":"primary","source_id":"local","path":"2025 Taxes"}`)}
	checkpoint := assistantExecutionCheckpoint{Version: 2, AllowedTools: directive.Tools, Messages: []assistant.Message{{Role: "user", Text: "/files 2025 Taxes"}, {Role: "assistant", Calls: []assistant.Call{call}}}, Pending: []assistant.Call{call}}
	task, err = encodeExecutionCheckpoint(task, checkpoint)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	step, err := s.store.GetAssistantTaskStep(ctx, task.HomeID, task.UserID, task.ID, call.ID)
	must(t, err)
	var result assistant.Result
	must(t, json.Unmarshal(step.Result, &result))
	if step.State != "failed" || result.Error == nil || result.Error.Code != "action_not_requested" || task.State != "running" {
		t.Fatalf("unrequested write escaped or killed search: %+v state=%s", result.Error, task.State)
	}
	if _, err := s.store.GetAssistantTaskApproval(ctx, task.HomeID, task.UserID, task.ID, call.ID); err == nil {
		t.Fatal("unrequested write produced an approval")
	}
}

func TestExecutionFileSlashAndUnknownFollowup(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	for _, input := range []string{"/file 2025 Taxes", "/files 2025 Taxes", "/files", "/files search upload records"} {
		d, explicit := s.executionSlash(ctx, task.HomeID, task.UserID, input)
		if !explicit || d.Error != nil || slices.Contains(d.Tools, "files.upload") || slices.Contains(d.Tools, "files.create_folder") {
			t.Fatalf("search command not read-only: %s", input)
		}
	}
	for _, input := range []struct{ text, tool string }{{"/files upload these to Taxes", "files.upload"}, {"/files mkdir New", "files.create_folder"}, {"/files create a folder New", "files.create_folder"}} {
		d, _ := s.executionSlash(ctx, task.HomeID, task.UserID, input.text)
		if !slices.Contains(d.Tools, input.tool) {
			t.Fatalf("explicit write missing: %s", input.text)
		}
	}
	body, _ := json.Marshal(map[string]any{"submission_id": "typo", "expected_revision": task.Revision, "text": "/filse Taxes"})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/followups", strings.NewReader(string(body)))
	s.handleAssistantExecutionTask(recorder, request, domain.Home{ID: task.HomeID}, authContext{User: domain.User{ID: task.UserID}}, []string{task.ID, "followups"})
	if recorder.Code != 400 {
		t.Fatalf("unsupported followup: %d", recorder.Code)
	}
	saved, err := s.store.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	if saved.State != task.State || saved.Revision != task.Revision {
		t.Fatal("typo mutated task")
	}
	// Old deployments could already have persisted unsupported inputs.
	_, err = s.store.SubmitAssistantTaskInput(ctx, task.HomeID, task.UserID, task.ID, "legacy-typo", "/filse Taxes")
	must(t, err)
	task, err = s.store.ClaimAssistantTask(ctx, "recover-typo", time.Minute)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	if task.State != "waiting_input" {
		t.Fatalf("queued typo killed task: %s", task.State)
	}
	inputs, err := s.store.PendingAssistantTaskInputs(ctx, task.HomeID, task.UserID, task.ID)
	must(t, err)
	if len(inputs) != 0 {
		t.Fatal("queued typo remains in a recovery loop")
	}
}

func TestExecutionFileClarificationRetainsRequestWithinTask(t *testing.T) {
	messages := []assistant.Message{
		{Role: "user", Text: "Upload these files to Taxes", OriginTaskID: "upload"},
		{Role: "assistant", Text: "Which of the two folders?"},
		{Role: "user", Text: "The second one", OriginTaskID: "upload"},
	}
	if !executionFileWriteRequested("files.upload", messages) {
		t.Fatal("selection lost upload request")
	}
	messages = append(messages, assistant.Message{Role: "user", Text: "Find the folder instead", OriginTaskID: "upload"})
	if executionFileWriteRequested("files.upload", messages) {
		t.Fatal("search inherited upload")
	}
	messages = messages[:3]
	messages[2].OriginTaskID = "new-task"
	if executionFileWriteRequested("files.upload", messages) {
		t.Fatal("new task inherited upload")
	}
}

func TestExecutionFileDiscoveryAllowsResolvedPrerequisiteRetry(t *testing.T) {
	call := assistant.Call{ID: "read", Tool: "files.list", Version: 1, Arguments: json.RawMessage(`{"path":"/"}`)}
	failed := assistant.Failure(call.ID, "file_target_unavailable")
	messages := []assistant.Message{{Role: "assistant", Calls: []assistant.Call{call}}, {Role: "tool", Result: &failed}}
	if !executionRepeatedFailure(call, messages) {
		t.Fatal("unchanged failure can loop")
	}
	source := assistant.Call{ID: "sources", Tool: "files.sources", Version: 1}
	result := executionResult(source, nil, 50, false)
	messages = append(messages, assistant.Message{Role: "assistant", Calls: []assistant.Call{source}}, assistant.Message{Role: "tool", Result: &result})
	if executionRepeatedFailure(call, messages) {
		t.Fatal("discovery could not repair missing prerequisite")
	}
}
