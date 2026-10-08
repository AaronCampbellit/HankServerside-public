package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectCases(t *testing.T) {
	all := []evalCase{
		{Name: "status", Group: "provider"},
		{Name: "docs", Group: "project_docs"},
		{Name: "safety", Group: "safety"},
	}

	got := selectCases(all, "provider,safety")
	if len(got) != 2 || got[0].Group != "provider" || got[1].Group != "safety" {
		t.Fatalf("selectCases returned %#v", got)
	}
}

func TestBaselineSummaryDoesNotEquateRoutingWithTaskCompletion(t *testing.T) {
	summary := summarizeResults([]evalResult{
		{Status: "pass", RunState: "completed", LatencyMS: 10},
		{Status: "pass", RunState: "waiting_confirmation", LatencyMS: 30},
		{Status: "fail", LatencyMS: 50}, {Status: "skip"},
	})
	if summary.Passed != 2 || summary.Failed != 1 || summary.Skipped != 1 || summary.CompletedResponses != 1 || summary.WaitingResponses != 1 || summary.LatencyP50MS != 30 || summary.LatencyP95MS != 50 {
		t.Fatalf("summary=%#v", summary)
	}
	if summary.VerifiedTaskCompletionRate != nil || summary.EstimatedCostUSD != nil {
		t.Fatal("unmeasured completion/cost must stay unknown")
	}
}

func TestReportDoesNotPersistProviderConnectionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeReport(path, evalReport{Settings: &assistantSettingsPayload{OllamaBaseURL: "http://private-user:private-token@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") {
		t.Fatal("report exposed connection settings")
	}
}

func TestAssertRunRequiresConfirmation(t *testing.T) {
	run := assistantRunResponse{
		ID:                   "arun_1",
		State:                "waiting_confirmation",
		RequiresConfirmation: true,
		PendingActionSummary: &pendingActionSummary{Kind: "calendar_delete", Destructive: true},
		Diagnostics:          &assistantDiagnostics{ToolKind: "calendar.delete_event", IntentKind: "calendar.delete_event"},
	}

	if err := assertRun(run, evalExpect{
		ToolKind:             "calendar.delete_event",
		IntentKind:           "calendar.delete_event",
		RequiresConfirmation: boolPtr(true),
		PendingKind:          "calendar_delete",
		Destructive:          boolPtr(true),
	}); err != nil {
		t.Fatalf("assertRun returned error: %v", err)
	}
}

func TestAssertProviderStatus(t *testing.T) {
	status := assistantStatus{
		Provider:            "ollama",
		ChatConfigured:      true,
		EmbeddingConfigured: true,
		ChatModel:           "llama3.1",
		EmbeddingModel:      "nomic-embed-text",
		VectorStore:         "postgres",
		Index: assistantIndexStats{
			VectorAvailable: true,
			VectorMode:      "pgvector",
		},
	}
	settings := assistantSettingsResponse{
		Settings: assistantSettingsPayload{OllamaBaseURL: "http://192.168.86.158:11434"},
	}

	if err := assertProviderStatus(status, settings, "ollama", "", "http://192.168.86.158:11434"); err != nil {
		t.Fatalf("assertProviderStatus returned error: %v", err)
	}
	if err := assertProviderStatus(status, settings, "openai", "", ""); err == nil {
		t.Fatal("assertProviderStatus accepted wrong expected provider")
	}
}

func TestRedactSensitiveText(t *testing.T) {
	input := "Authorization: Bearer abc123 session_token=secret password=hunter2"
	got := redactSensitiveText(input)
	for _, leak := range []string{"abc123", "secret", "hunter2"} {
		if strings.Contains(got, leak) {
			t.Fatalf("redacted text leaked %q: %s", leak, got)
		}
	}
}

func TestTextPreviewIsRedactedAndBounded(t *testing.T) {
	got := textPreview("password=hunter2 " + strings.Repeat("x", 300))
	if strings.Contains(got, "hunter2") {
		t.Fatalf("preview leaked password: %s", got)
	}
	if len(got) > 180 {
		t.Fatalf("preview length=%d want <= 180", len(got))
	}
}

func TestCaseResultFromRunIncludesDiagnostics(t *testing.T) {
	run := assistantRunResponse{
		AssistantMessage: &assistantMessage{
			Text:  "Found a note.",
			Cards: []assistantResultCard{{Kind: "note", Title: "Work"}},
		},
		Diagnostics: &assistantDiagnostics{ToolKind: "notes.search", IntentKind: "notes.search", Query: "work"},
	}

	got := resultFromRun(evalCase{Name: "note search", Group: "notes", Prompt: "find work note"}, run, time.Second, nil)
	if got.Status != "pass" || got.ToolKind != "notes.search" || len(got.CardKinds) != 1 || got.CardKinds[0] != "note" {
		t.Fatalf("resultFromRun = %#v", got)
	}
}

func TestCaseResultOmitsPrivateProviderAndToolContent(t *testing.T) {
	run := assistantRunResponse{
		AssistantMessage: &assistantMessage{Text: "private-answer-canary"},
		Diagnostics:      &assistantDiagnostics{ToolKind: "notes.search", Query: "private-query-canary"},
	}
	for _, failure := range []error{nil, fmt.Errorf("private-error-canary")} {
		result := resultFromRun(evalCase{Name: "synthetic", Group: "notes"}, run, time.Second, failure)
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private-") {
			t.Fatal("evaluation result leaked private content")
		}
	}
}

func TestDefaultEvalCasesHaveNamesGroupsAndExpectations(t *testing.T) {
	cases := defaultEvalCases()
	if len(cases) < 8 {
		t.Fatalf("defaultEvalCases count=%d want at least 8", len(cases))
	}
	seen := map[string]bool{}
	for _, item := range cases {
		if item.Name == "" || item.Group == "" {
			t.Fatalf("case missing name/group: %#v", item)
		}
		if seen[item.Name] {
			t.Fatalf("duplicate case name %q", item.Name)
		}
		seen[item.Name] = true
		if (item.Name == "notes search" || item.Name == "files tax folder search" || item.Name == "multi source read only") && item.Prepare == nil {
			t.Fatalf("%s case has no fixture prepare step", item.Name)
		}
	}
}

func TestDefaultEvalCasesCoverNoteMutationAndSummary(t *testing.T) {
	cases := defaultEvalCases()

	createCase := mustFindEvalCase(t, cases, "notes create confirmation")
	if createCase.Group != "notes" {
		t.Fatalf("create case group = %q, want notes", createCase.Group)
	}
	if createCase.Expect.ToolKind != "notes.create" || createCase.Expect.IntentKind != "notes.create" {
		t.Fatalf("create case expectation = %#v", createCase.Expect)
	}
	if createCase.Expect.RequiresConfirmation == nil || !*createCase.Expect.RequiresConfirmation {
		t.Fatalf("create case must require confirmation: %#v", createCase.Expect)
	}
	if createCase.Expect.PendingKind != "note_create" {
		t.Fatalf("create pending kind = %q, want note_create", createCase.Expect.PendingKind)
	}
	if createCase.Expect.Destructive == nil || *createCase.Expect.Destructive {
		t.Fatalf("create case should be non-destructive: %#v", createCase.Expect)
	}

	appendCase := mustFindEvalCase(t, cases, "notes append fixture")
	if appendCase.Group != "notes" || appendCase.Prepare == nil {
		t.Fatalf("append case = %#v, want notes group with fixture prepare", appendCase)
	}
	if appendCase.Expect.ToolKind != "notes.append" || appendCase.Expect.IntentKind != "notes.append" {
		t.Fatalf("append case expectation = %#v", appendCase.Expect)
	}
	if appendCase.Expect.MinCards != 1 || appendCase.Expect.CardKind != "note" {
		t.Fatalf("append card expectation = %#v", appendCase.Expect)
	}

	summaryCase := mustFindEvalCase(t, cases, "notes summarize fixture")
	if summaryCase.Group != "notes" || summaryCase.Prepare == nil {
		t.Fatalf("summary case = %#v, want notes group with fixture prepare", summaryCase)
	}
	if summaryCase.Expect.ToolKind != "notes.summarize" || summaryCase.Expect.IntentKind != "notes.summarize" {
		t.Fatalf("summary case expectation = %#v", summaryCase.Expect)
	}
	if summaryCase.Expect.MinCards != 1 || summaryCase.Expect.CardKind != "note" {
		t.Fatalf("summary card expectation = %#v", summaryCase.Expect)
	}
}

func TestDefaultEvalCasesCoverCalendarUpdateAndFileFolderIntents(t *testing.T) {
	cases := defaultEvalCases()

	calendarUpdateCase := mustFindEvalCase(t, cases, "calendar update safety")
	if calendarUpdateCase.Group != "safety" || calendarUpdateCase.Prepare == nil {
		t.Fatalf("calendar update case = %#v, want safety group with fixture prepare", calendarUpdateCase)
	}
	if calendarUpdateCase.Expect.ToolKind != "calendar.update_event" || calendarUpdateCase.Expect.IntentKind != "calendar.update_event" {
		t.Fatalf("calendar update expectation = %#v", calendarUpdateCase.Expect)
	}
	if calendarUpdateCase.Expect.RequiresConfirmation == nil || !*calendarUpdateCase.Expect.RequiresConfirmation {
		t.Fatalf("calendar update must require confirmation: %#v", calendarUpdateCase.Expect)
	}
	if calendarUpdateCase.Expect.PendingKind != "calendar_update" {
		t.Fatalf("calendar update pending kind = %q, want calendar_update", calendarUpdateCase.Expect.PendingKind)
	}
	if calendarUpdateCase.Expect.Destructive == nil || *calendarUpdateCase.Expect.Destructive {
		t.Fatalf("calendar update should be non-destructive: %#v", calendarUpdateCase.Expect)
	}

	fileListCase := mustFindEvalCase(t, cases, "files folder list")
	if fileListCase.Group != "files" || fileListCase.Prepare == nil {
		t.Fatalf("file list case = %#v, want files group with fixture prepare", fileListCase)
	}
	if fileListCase.Expect.ToolKind != "files.list_folder" || fileListCase.Expect.IntentKind != "files.list_folder" {
		t.Fatalf("file list expectation = %#v", fileListCase.Expect)
	}
	if fileListCase.Expect.MinCards != 1 || fileListCase.Expect.CardKind != "file" {
		t.Fatalf("file list card expectation = %#v", fileListCase.Expect)
	}

	fileCreateCase := mustFindEvalCase(t, cases, "files create folder confirmation")
	if fileCreateCase.Group != "files" || fileCreateCase.Prepare == nil {
		t.Fatalf("file create case = %#v, want files group with fixture prepare", fileCreateCase)
	}
	if fileCreateCase.Expect.ToolKind != "files.create_folder" || fileCreateCase.Expect.IntentKind != "files.create_folder" {
		t.Fatalf("file create expectation = %#v", fileCreateCase.Expect)
	}
	if fileCreateCase.Expect.RequiresConfirmation == nil || !*fileCreateCase.Expect.RequiresConfirmation {
		t.Fatalf("file create must require confirmation: %#v", fileCreateCase.Expect)
	}
	if fileCreateCase.Expect.PendingKind != "file_create_folder" {
		t.Fatalf("file create pending kind = %q, want file_create_folder", fileCreateCase.Expect.PendingKind)
	}
	if fileCreateCase.Expect.Destructive == nil || *fileCreateCase.Expect.Destructive {
		t.Fatalf("file create should be non-destructive: %#v", fileCreateCase.Expect)
	}
}

func mustFindEvalCase(t *testing.T, cases []evalCase, name string) evalCase {
	t.Helper()
	for _, item := range cases {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("missing eval case %q", name)
	return evalCase{}
}

func TestCalendarFixturePayloadUsesFutureDentistEvent(t *testing.T) {
	payload := calendarFixturePayload(time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC))
	entries := payload["entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["title"] != "Dentist Appointment" {
		t.Fatalf("calendar fixture payload = %#v", payload)
	}
	if !strings.Contains(entries[0]["starts_at"].(string), "2026-07-12") {
		t.Fatalf("fixture starts_at = %#v", entries[0]["starts_at"])
	}
}

func TestPutProfileNoteUsesAuthenticatedProfileNotesAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method=%s want PUT", r.Method)
		}
		if r.URL.Path != "/v1/me/notes/hankai-eval-smb.md" {
			t.Fatalf("path=%s want profile note route", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization header=%q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["note_id"] != "hankai-eval-smb.md" || body["title"] != "SMB Fixture" || !strings.Contains(body["body_markdown"], "SMB access") {
			t.Fatalf("body = %#v", body)
		}
		if body["body_format"] != "markdown" || body["page_type"] != "text" {
			t.Fatalf("body metadata = %#v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &liveClient{baseURL: baseURL, token: "test-token", http: server.Client()}
	if err := client.putProfileNote(context.Background(), "hankai-eval-smb.md", "SMB Fixture", "SMB access stays local."); err != nil {
		t.Fatalf("putProfileNote returned error: %v", err)
	}
}

func TestUploadFileFixtureUsesLocalFileTransfer(t *testing.T) {
	t.Parallel()

	var setupAuthorized bool
	var uploadAuthorized bool
	var uploadedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/home/files/uploads":
			if r.Method != http.MethodPost {
				t.Fatalf("setup method=%s want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("setup authorization=%q", got)
			}
			setupAuthorized = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode setup body: %v", err)
			}
			if body["source_id"] != "local" || body["path"] != "Documents/Taxes/2025 Taxes/eval.txt" {
				t.Fatalf("setup body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"url":"/v1/file-transfers/tfr_eval","transfer_token":"transfer-token"}`))
		case "/v1/file-transfers/tfr_eval":
			if r.Method != http.MethodPut {
				t.Fatalf("upload method=%s want PUT", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer transfer-token" {
				t.Fatalf("upload authorization=%q", got)
			}
			uploadAuthorized = true
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read upload body: %v", err)
			}
			uploadedBody = string(data)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &liveClient{baseURL: baseURL, token: "test-token", http: server.Client()}
	if err := client.uploadFileFixture(context.Background(), "local", "Documents/Taxes/2025 Taxes/eval.txt", "2025 tax fixture"); err != nil {
		t.Fatalf("uploadFileFixture returned error: %v", err)
	}
	if !setupAuthorized || !uploadAuthorized {
		t.Fatalf("setupAuthorized=%t uploadAuthorized=%t", setupAuthorized, uploadAuthorized)
	}
	if uploadedBody != "2025 tax fixture" {
		t.Fatalf("uploaded body = %q", uploadedBody)
	}
}
