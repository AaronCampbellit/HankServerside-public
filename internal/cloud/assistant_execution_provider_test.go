package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
)

func executionProviderTestTools() []assistant.Definition {
	return []assistant.Definition{{Name: "notes.search", Version: 1, Description: "Search notes", Effect: "read", InputSchema: executionSchema(map[string]any{"query": executionString(200)}, "query")}, {Name: "notes.create", Version: 1, Description: "Create note", Effect: "write", InputSchema: executionSchema(map[string]any{"title": executionString(200)}, "title")}}
}
func TestExecutionProviderNativeToolRoundTrip(t *testing.T) {
	for _, provider := range []string{"ollama", "openai"} {
		t.Run(provider, func(t *testing.T) {
			round := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				round++
				var body struct {
					Options  map[string]int         `json:"options"`
					Think    *json.RawMessage       `json:"think"`
					Messages []executionWireMessage `json:"messages"`
					Tools    []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				foundWrite := false
				for _, tool := range body.Tools {
					foundWrite = foundWrite || tool.Function.Name == "notes_create_v1"
				}
				if !foundWrite {
					t.Error("implemented write tool was not advertised")
				}
				if provider == "ollama" && (body.Think == nil || string(*body.Think) != "false") {
					t.Error("bounded execution reasoning mode was not set")
				}
				if provider == "ollama" && body.Options["num_ctx"] != 16384 {
					t.Error("tool conversation context was not explicitly sized")
				}
				if round == 1 {
					if provider == "ollama" {
						writeJSON(w, 200, map[string]any{"done": true, "prompt_eval_count": 10, "eval_count": 4, "message": map[string]any{"role": "assistant", "thinking": "synthetic continuation", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "notes_search_v1", "arguments": map[string]any{"query": "Lunch"}}}}}})
					} else {
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("configured auth missing")
						}
						writeJSON(w, 200, map[string]any{"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}, "choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "provider-call", "type": "function", "function": map[string]any{"name": "notes_search_v1", "arguments": `{"query":"Lunch"}`}}}}}}})
					}
					return
				}
				previous := body.Messages[len(body.Messages)-2]
				last := body.Messages[len(body.Messages)-1]
				if previous.Role != "assistant" || len(previous.Calls) != 1 || last.Role != "tool" || !strings.Contains(last.Content, "note-id") {
					t.Error("tool protocol was flattened or lost")
				}
				if provider == "ollama" {
					if last.ToolName != "notes_search_v1" || previous.Thinking != "synthetic continuation" {
						t.Error("Ollama continuation/name lost")
					}
					writeJSON(w, 200, map[string]any{"done": true, "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "finish", "type": "function", "function": map[string]any{"name": "hank_finish", "arguments": map[string]any{"answer": "Found [Lunch](hank://notes/note-id)."}}}}}})
				} else {
					if last.ToolCallID != "provider-call" {
						t.Error("OpenAI call ID lost")
					}
					writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "finish", "type": "function", "function": map[string]any{"name": "hank_finish", "arguments": `{"answer":"Found [Lunch](hank://notes/note-id)."}`}}}}}}})
				}
			}))
			defer server.Close()
			request := assistant.ModelRequest{Messages: []assistant.Message{{Role: "user", Text: "Find my lunch note"}}, Tools: executionProviderTestTools()}
			turn, err := postAssistantExecutionTurn(context.Background(), provider, server.URL, "synthetic-token", "existing-model", request)
			must(t, err)
			if len(turn.Calls) != 1 || turn.Calls[0].Tool != "notes.search" || turn.Usage.InputTokens == nil || *turn.Usage.InputTokens != 10 {
				t.Fatal("typed model call/usage missing")
			}
			request.Messages = append(request.Messages, assistant.Message{Role: "assistant", Calls: turn.Calls, Continuation: turn.Continuation}, assistant.Message{Role: "tool", Result: &assistant.Result{SchemaVersion: 2, Kind: "tool_result", CallID: turn.Calls[0].ID, Outcome: "confirmed", Data: json.RawMessage(`{"items":[{"id":"note-id"}]}`)}})
			final, err := postAssistantExecutionTurn(context.Background(), provider, server.URL, "synthetic-token", "existing-model", request)
			must(t, err)
			if final.FinishReason != "final" || !strings.Contains(final.Text, "hank://notes/note-id") {
				t.Fatal("final result missing")
			}
		})
	}
}
func TestExecutionProviderClarificationAndUntrustedFailures(t *testing.T) {
	for _, test := range []struct {
		name, response string
		wantQuestion   bool
	}{
		{"clarification", `{"done":true,"message":{"tool_calls":[{"function":{"name":"hank_ask_user","arguments":{"question":"Which folder should I use?"}}}]}}`, true},
		{"unknown tool", `{"done":true,"message":{"tool_calls":[{"function":{"name":"calendar_delete_event_v1","arguments":{"title":"Bad"}}}]}}`, false},
		{"private error", `{"error":"private provider content"}`, false},
		{"incomplete", `{"done":false,"message":{"content":"Pretend done"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			turn, err := postAssistantExecutionTurn(context.Background(), "ollama", server.URL, "", "existing-model", assistant.ModelRequest{Tools: executionProviderTestTools()})
			if test.wantQuestion {
				if err != nil || turn.FinishReason != "needs_input" {
					t.Fatalf("question not durable: %v", err)
				}
			} else if test.name == "unknown tool" {
				if err != nil || turn.FinishReason != "error" || len(turn.Calls) != 0 {
					t.Fatal("unknown tool did not enter bounded repair")
				}
			} else if !errors.Is(err, errExecutionProviderUnavailable) || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe provider failure")
			}
		})
	}
}

func TestExecutionLiteralContextPreservesRequestAndBoundsCopyHints(t *testing.T) {
	for _, text := range []string{"No quoted content", `An unfinished "quote`, `Escaped \"delimiter`} {
		if executionLiteralContext(text) != text {
			t.Fatal("nonliteral input changed")
		}
	}
	text := `Keep "  Héllo.  "; append "line\nvalue".`
	got := executionLiteralContext(text)
	if !strings.HasPrefix(got, text+"\n\n") {
		t.Fatal("original request changed")
	}
	const marker = "Quoted literal values from this request (copy unchanged when relevant): "
	var values []string
	if err := json.Unmarshal([]byte(strings.SplitN(got, marker, 2)[1]), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "  Héllo.  " || values[1] != `line\nvalue` {
		t.Fatal("literal bytes changed")
	}
	large := `"` + strings.Repeat("x", 4097) + `"`
	if executionLiteralContext(large) != large {
		t.Fatal("oversized copy hint added")
	}
	many := strings.Repeat(`"x" `, 30)
	if err := json.Unmarshal([]byte(strings.SplitN(executionLiteralContext(many), marker, 2)[1]), &values); err != nil || len(values) != 16 {
		t.Fatal("copy hint count unbounded")
	}
}

func TestExecutionProviderStructuredDecisionRepair(t *testing.T) {
	for _, test := range []struct {
		name, response, reason string
		invalid                bool
	}{
		{"answer", `{"decision":"final","text":"Found the requested folder.","calls":[]}`, "final", false},
		{"clarification", `{"decision":"needs_input","text":"Which folder?","calls":[]}`, "needs_input", false},
		{"continue", `{"decision":"tool_calls","text":"","calls":[{"name":"notes_search_v1","arguments":{"query":"Lunch"}}]}`, "tool_calls", false},
		{"unknown tool", `{"decision":"tool_calls","text":"","calls":[{"name":"shell_exec_v1","arguments":{}}]}`, "error", false},
		{"malformed JSON", "```json\n{}\n```", "", true},
		{"unknown fields", `{"decision":"final","text":"Done","calls":[],"unexpected":true}`, "", true},
		{"ambiguous", `{"decision":"final","text":"Done","calls":[{"name":"notes_search_v1","arguments":{}}]}`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				var messages []executionWireMessage
				_ = json.Unmarshal(request["messages"], &messages)
				if len(messages) == 0 || messages[len(messages)-1].Role != "user" {
					t.Error("repair guidance would be dropped by Ollama template")
				}
				if request["format"] != nil || request["tools"] != nil || !strings.Contains(string(request["messages"]), "Return ONLY a JSON object") {
					t.Error("repair did not request a validated JSON decision")
				}
				writeJSON(w, 200, map[string]any{"done": true, "prompt_eval_count": 10, "eval_count": 4, "message": map[string]any{"role": "assistant", "content": test.response}})
			}))
			defer server.Close()
			input := assistant.ModelRequest{Tools: executionProviderTestTools(), Messages: []assistant.Message{{Role: "user", Text: "Find Lunch"}, {Role: "assistant", Text: "Found it."}, {Role: "system", Text: executionDecisionRepairInstructions}}}
			turn, err := postAssistantExecutionTurn(context.Background(), "ollama", server.URL, "", "synthetic", input)
			if test.invalid {
				if err != nil || turn.FinishReason != "error" || len(turn.Calls) != 0 {
					t.Fatal("invalid decision did not enter bounded repair")
				}
				return
			}
			if err != nil || turn.FinishReason != test.reason {
				t.Fatalf("repair: %+v %v", turn, err)
			}
			if test.reason == "tool_calls" && (len(turn.Calls) != 1 || turn.Calls[0].Tool != "notes.search") {
				t.Fatalf("lost typed call: %+v", turn)
			}
		})
	}
}

func TestExecutionProviderOllamaRuntimeGuidanceIsRendered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []executionWireMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Messages[0].Role != "system" {
			t.Error("initial system prompt lost")
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "user" || !strings.Contains(last.Content, "not a new user request") || !strings.Contains(last.Content, "Verify remaining uploads") {
			t.Error("late guidance dropped or changed")
		}
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "hank_finish", "arguments": map[string]any{"answer": "Verified."}}}}}})
	}))
	defer server.Close()
	_, err := postAssistantExecutionTurn(context.Background(), "ollama", server.URL, "", "synthetic", assistant.ModelRequest{Messages: []assistant.Message{{Role: "user", Text: "Upload these files"}, {Role: "system", Text: "Verify remaining uploads"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutionProviderMalformedDecisionsRecoverWithinWorkerBudget(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovers", false: "bounded"}[recover], func(t *testing.T) {
			s, _ := executionTaskFixture(t)
			ctx := context.Background()
			task, err := s.store.ClaimAssistantTask(ctx, "decision-worker", time.Minute)
			must(t, err)
			round := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				round++
				content := "The answer needs a structured decision."
				if round == 2 {
					content = `{"decision":"final","text":"Synthetic answer","calls":[],"unexpected":true}`
				}
				if round == 3 && recover {
					content = `{"decision":"final","text":"Synthetic answer","calls":[]}`
				}
				writeJSON(w, 200, map[string]any{"done": true, "prompt_eval_count": 10, "eval_count": 4, "message": map[string]any{"role": "assistant", "content": content}})
			}))
			defer provider.Close()
			model := executionModelFunc(func(ctx context.Context, input assistant.ModelRequest) (assistant.Turn, error) {
				return postAssistantExecutionTurn(ctx, "ollama", provider.URL, "", "synthetic", input)
			})
			for i := 0; i < 3; i++ {
				task, err = s.advanceAssistantExecution(ctx, task, model)
				must(t, err)
				if i < 2 && task.State != "running" {
					t.Fatal("repairable decision terminated task")
				}
			}
			want := "failed"
			if recover {
				want = "completed"
			}
			if task.State != want || task.Calls != 0 || task.Turns != 3 || task.Tokens != 42 {
				t.Fatalf("unbounded or unaccounted repair: state=%s calls=%d turns=%d tokens=%d", task.State, task.Calls, task.Turns, task.Tokens)
			}
		})
	}
}
