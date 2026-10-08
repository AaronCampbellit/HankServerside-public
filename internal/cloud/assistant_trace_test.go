package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssistantProviderFailureLogDoesNotEchoPrivateInput(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private-provider-canary private-prompt-canary", http.StatusBadRequest)
	}))
	defer provider.Close()
	var logs bytes.Buffer
	s := &Server{assistantAI: AssistantAIConfig{OllamaBaseURL: provider.URL, OllamaEmbeddingModel: "synthetic", EmbeddingDimension: 4}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	_, model, _ := s.embedAssistantText(context.Background(), "", "private-prompt-canary")
	if model != "local-hash" {
		t.Fatalf("expected failure fallback, got %q", model)
	}
	if strings.Contains(logs.String(), "private-") {
		t.Fatal("provider error leaked private input into logs")
	}
	if !strings.Contains(logs.String(), "operation_failed") {
		t.Fatal("missing safe failure diagnostic")
	}
}

func TestAssistantTraceRejectsPrivateAndUnrecognizedContent(t *testing.T) {
	s := &Server{assistantTrace: newAssistantTraceLog(20)}
	s.recordAssistantTrace(context.Background(), assistantTraceEvent{
		Event: "assistant.tool.execute_done", Summary: "private-summary-canary",
		Scope: "private-scope-canary", Level: "private-level-canary",
		Details: map[string]string{
			"prompt": "private-prompt-canary", "raw_answer": "private-answer-canary",
			"query": "private-query-canary", "error": "private-error-canary",
			"filename": "private-filename-canary", "path": "private-path-canary",
			"token": "private-token-canary", "future_field": "private-future-canary",
			"model": "private-model-canary", "tool": "private-tool-canary",
			"card_count": "private-count-canary", "approved": "private-bool-canary",
			"state": "private-state-canary", "pending_action": "private-action-canary",
			"elapsed_ms": "12", "has_error": "false", "intent": "notes.search",
		},
	})
	s.recordAssistantTrace(context.Background(), assistantTraceEvent{Event: "private-event-canary"})
	events, total := s.assistantTraceSnapshot("", "", "", 20)
	if total != 1 {
		t.Fatalf("got %d events, want one known event", total)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("private content escaped the trace boundary")
	}
	if len(events[0].Details) != 3 || events[0].Details["elapsed_ms"] != "12" || events[0].Details["intent"] != "notes.search" {
		t.Fatalf("safe diagnostic metadata was lost: %#v", events[0].Details)
	}
}

func TestAssistantTraceNumericAndBooleanFieldsAreStrict(t *testing.T) {
	metadata := sanitizeTraceDetails(traceDetails(map[string]any{"tool": assistantIntentNotesSearch}))
	if metadata["tool"] != "notes.search" {
		t.Fatal("typed tool identity lost in trace conversion")
	}
	for _, value := range []string{"-1", "1 private", "1.2", "18446744073709551616", ""} {
		if got := sanitizeTraceDetails(map[string]string{"card_count": value}); len(got) != 0 {
			t.Fatalf("accepted non-counter value %q", value)
		}
	}
	for _, value := range []string{"TRUE", "1", "false private", ""} {
		if got := sanitizeTraceDetails(map[string]string{"approved": value}); len(got) != 0 {
			t.Fatalf("accepted non-boolean value %q", value)
		}
	}
}
