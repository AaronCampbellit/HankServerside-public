package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestExecutionAdmissionRunsExistingProviderAndPersistsConversation(t *testing.T) {
	s, home, user, _ := executionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	must(t, s.store.CreateSession(ctx, domain.AppSession{ID: "exec-auth", UserID: user.ID, TokenHash: hashToken("exec-token"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))
	must(t, s.store.CreateAssistantSession(ctx, domain.AssistantSession{ID: "exec-session", HomeID: home.ID, UserID: user.ID, Title: "New Conversation", CreatedAt: now, UpdatedAt: now, LastMessageAt: now}))
	rounds := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rounds++
		if rounds == 1 {
			writeJSON(w, 200, map[string]any{"done": true, "message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "machines_list_v1", "arguments": map[string]any{}}}}}})
			return
		}
		var body struct {
			Messages []executionWireMessage `json:"messages"`
		}
		must(t, json.NewDecoder(r.Body).Decode(&body))
		if body.Messages[len(body.Messages)-1].Role != "tool" {
			t.Error("provider did not receive tool observation")
		}
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "hank_finish", "arguments": map[string]any{"answer": "No machines are enrolled."}}}}}})
	}))
	defer provider.Close()
	s.ConfigureAssistantAI(AssistantAIConfig{ExecutionEnabled: true, Provider: "ollama", OllamaBaseURL: provider.URL, OllamaChatModel: "existing-model"})
	request := func(version, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/home/assistant/sessions/exec-session/messages", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer exec-token")
		r.Header.Set("Content-Type", "application/json")
		if version != "" {
			r.Header.Set("X-Hank-Assistant-Execution", version)
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		return w
	}
	first := request("2", `{"submission_id":"stable-key","content":"List enrolled machines"}`)
	if first.Code != 202 {
		t.Fatalf("admission: %d %s", first.Code, first.Body.String())
	}
	var admitted struct {
		TaskID string `json:"task_id"`
	}
	must(t, json.Unmarshal(first.Body.Bytes(), &admitted))
	replay := request("2", `{"submission_id":"stable-key","content":"List enrolled machines"}`)
	if replay.Code != 202 || !strings.Contains(replay.Body.String(), admitted.TaskID) {
		t.Fatal("lost-response replay created another task")
	}
	if conflict := request("2", `{"submission_id":"another-key","content":"Different task"}`); conflict.Code != 409 {
		t.Fatal("concurrent task admitted")
	}
	if legacy := request("", `{"content":"create a note"}`); legacy.Code != 409 {
		t.Fatal("v1 entered a pinned v2 session")
	}
	messages, err := s.store.ListAssistantMessages(ctx, "exec-session")
	must(t, err)
	if len(messages) != 1 {
		t.Fatal("submission did not atomically persist one user message")
	}
	task, err := s.store.ClaimAssistantTask(ctx, "http-worker", time.Minute)
	must(t, err)
	model, err := s.newAssistantExecutionModel(ctx, task)
	must(t, err)
	for i := 0; i < 5 && task.State == "running"; i++ {
		task, err = s.advanceAssistantExecutionWithCancellation(ctx, task, model)
		must(t, err)
	}
	if task.State != "completed" || rounds != 2 {
		t.Fatal("native read loop did not complete")
	}
	messages, err = s.store.ListAssistantMessages(ctx, "exec-session")
	must(t, err)
	if len(messages) != 2 || messages[1].Role != "assistant" || !strings.Contains(messages[1].ContentJSON, "No machines") {
		t.Fatal("terminal response was not durable")
	}
	version, err := s.store.AssistantSessionExecutionVersion(ctx, home.ID, user.ID, "exec-session")
	must(t, err)
	if version != 2 {
		t.Fatal("conversation lost execution version")
	}
}
