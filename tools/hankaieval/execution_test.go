package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestExecutionDriverChecksEffectsAndExactApproval(t *testing.T) {
	for _, scenario := range []string{"create", "reject", "append", "read", "wrong_proposal", "quoted_content", "extra_newline", "duplicate_effect", "submission_replay"} {
		t.Run(scenario, func(t *testing.T) {
			mode := scenario
			if mode == "wrong_proposal" || mode == "quoted_content" || mode == "extra_newline" || mode == "duplicate_effect" || mode == "submission_replay" {
				mode = "create"
			}
			test := executionScenario{name: scenario, mode: mode, title: "Synthetic fixture", body: "Ready.", noteID: "fixture", approve: mode != "reject"}
			decided, exists := false, false
			approvals, submissions, stops := 0, 0, 0
			body := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("missing authentication")
				}
				write := func(code int, value any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(value) }
				switch {
				case r.Method == "PUT" && r.URL.Path == "/v1/me/notes/fixture":
					exists = true
					body = "Original. Thursday."
					write(200, map[string]any{})
				case r.Method == "GET" && r.URL.Path == "/v1/me/notes":
					notes := []executionNote{}
					if exists {
						notes = append(notes, executionNote{ID: "fixture", Title: test.title})
					}
					write(200, map[string]any{"notes": notes})
				case r.Method == "GET" && r.URL.Path == "/v1/me/notes/fixture":
					write(200, executionNote{NoteID: "fixture", Title: test.title, Body: body, Revision: "r1"})
				case r.Method == "POST" && r.URL.Path == "/v1/home/assistant/sessions":
					write(201, map[string]string{"id": "session"})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/messages"):
					if r.Header.Get("X-Hank-Assistant-Execution") != "2" {
						t.Error("missing execution negotiation")
					}
					submissions++
					id := "task"
					if scenario == "submission_replay" && submissions == 2 {
						id = "duplicate"
					}
					write(202, map[string]string{"task_id": id})
				case r.Method == "GET" && r.URL.Path == "/v1/home/assistant/tasks/task":
					state := "waiting_approval"
					if decided || mode == "read" {
						state = "completed"
					}
					details := []map[string]string{{"label": "Title", "value": test.title}, {"label": "Exact content", "value": test.body}, {"label": "Scope", "value": "personal"}}
					kind := "note_create"
					if mode == "append" {
						kind = "note_append"
						details = append(details, map[string]string{"label": "Target note", "value": test.title}, map[string]string{"label": "Text to add", "value": test.body}, map[string]string{"label": "Revision", "value": "r1"}, map[string]string{"label": "Note ID", "value": "internal-fixture"})
					}
					if scenario == "quoted_content" {
						details[1]["value"] = `"Ready."`
					}
					if scenario == "extra_newline" {
						details[1]["value"] = "\nReady."
					}
					if scenario == "wrong_proposal" {
						kind = "ha_control"
					}
					write(200, map[string]any{"task_id": "task", "state": state, "revision": 3, "budget": map[string]int{"turns_used": 2, "calls_used": 2, "tokens_used": 100, "active_ms_used": 30}, "pending_approval": map[string]any{"approval_id": "approval", "action_digest": "exact", "summary": map[string]any{"kind": kind, "details": details}}})
				case strings.HasSuffix(r.URL.Path, "/approvals/approval"):
					var input struct {
						Approved bool   `json:"approved"`
						Digest   string `json:"action_digest"`
						Revision int    `json:"expected_revision"`
					}
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input.Digest != "exact" || input.Revision != 3 {
						t.Error("approval identity was lost")
					}
					approvals++
					decided = true
					if input.Approved {
						exists = true
						if mode == "append" {
							body += "\n" + test.body
						} else {
							body = test.body
						}
						if scenario == "duplicate_effect" {
							body += test.body
						}
					}
					write(200, map[string]any{})
				case strings.HasSuffix(r.URL.Path, "/stop"):
					stops++
					write(200, map[string]any{})
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/messages"):
					write(200, map[string]any{"messages": []any{map[string]any{"role": "assistant", "text": "Thursday", "sources": []any{map[string]string{"uri": "hank://notes/fixture"}}}}})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					write(500, map[string]any{})
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := &liveClient{baseURL: base, token: "synthetic", http: server.Client()}
			result := runExecutionScenario(context.Background(), client, test)
			bad := scenario == "wrong_proposal" || scenario == "quoted_content" || scenario == "extra_newline" || scenario == "duplicate_effect" || scenario == "submission_replay"
			if (result.Status == "pass") == bad || result.TaskVerified == nil || *result.TaskVerified == bad {
				t.Fatalf("bad result: %#v", result)
			}
			if (scenario == "wrong_proposal" || scenario == "quoted_content" || scenario == "extra_newline") && (approvals != 0 || stops != 1 || result.UnsafeProposals != 1) {
				t.Fatal("unexpected action was approved")
			}
			if scenario == "duplicate_effect" && result.IncorrectActions != 1 {
				t.Fatal("duplicate write not counted")
			}
			if !bad && mode != "read" && approvals != 1 {
				t.Fatal("missing single approval")
			}
		})
	}
}

func TestExecutionSummaryReportsMeasuredCompletionAndUnknownCost(t *testing.T) {
	summary := summarizeResults([]evalResult{{Status: "pass", TaskVerified: boolPtr(true), ActiveMS: 10}, {Status: "fail", TaskVerified: boolPtr(false), ActiveMS: 30, IncorrectActions: 1}})
	if summary.VerifiedTaskCompletionRate == nil || *summary.VerifiedTaskCompletionRate != 0.5 || summary.IncorrectActions != 1 || summary.ActiveLatencyP95MS != 30 || summary.EstimatedCostUSD != nil {
		t.Fatalf("summary=%#v", summary)
	}
	t.Setenv("HANK_HANKAI_TOKEN_COST_PER_MILLION_USD", "0")
	t.Setenv("HANK_HANKAI_COST_BASIS", "Local Ollama API fee only; hardware and energy unmeasured")
	result := evalResult{Tokens: 100}
	setExecutionCost(&result)
	if result.EstimatedCostUSD == nil || *result.EstimatedCostUSD != 0 || result.CostBasis == "" {
		t.Fatal("cost scope missing")
	}
}
