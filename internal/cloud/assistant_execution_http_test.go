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
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
)

func TestExecutionTaskControlsOwnershipRevisionAndReplay(t *testing.T) {
	s, task := executionTaskFixture(t)
	call := func(user, method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/v1/home/assistant/tasks/"+task.ID+suffix, strings.NewReader(body))
		recorder := httptest.NewRecorder()
		parts := []string{task.ID}
		if suffix != "" {
			parts = append(parts, strings.Split(strings.TrimPrefix(suffix, "/"), "/")...)
		}
		s.handleAssistantExecutionTask(recorder, request, domain.Home{ID: task.HomeID}, authContext{User: domain.User{ID: user}}, parts)
		return recorder
	}
	for _, method := range []string{"GET", "POST"} {
		suffix := ""
		if method == "POST" {
			suffix = "/stop"
		}
		if got := call("foreign", method, suffix, ""); got.Code != 404 {
			t.Fatalf("foreign owner: %d", got.Code)
		}
	}
	snapshot := call(task.UserID, "GET", "", "")
	if snapshot.Code != 200 || strings.Contains(snapshot.Body.String(), "checkpoint") || strings.Contains(snapshot.Body.String(), "request_text") {
		t.Fatalf("unsafe snapshot: %d", snapshot.Code)
	}
	payload := map[string]any{"submission_id": "followup-1", "expected_revision": task.Revision + 1, "text": "Use the second folder"}
	raw, _ := json.Marshal(payload)
	if got := call(task.UserID, "POST", "/followups", string(raw)); got.Code != 409 {
		t.Fatalf("stale revision: %d", got.Code)
	}
	payload["expected_revision"] = task.Revision
	raw, _ = json.Marshal(payload)
	for i := 0; i < 2; i++ {
		if got := call(task.UserID, "POST", "/followups", string(raw)); got.Code != 202 {
			t.Fatalf("followup/replay: %d %s", got.Code, got.Body.String())
		}
	}
	payload["text"] = "Changed request"
	raw, _ = json.Marshal(payload)
	if got := call(task.UserID, "POST", "/followups", string(raw)); got.Code != 409 {
		t.Fatalf("changed replay: %d", got.Code)
	}
	if got := call(task.UserID, "POST", "/followups", `{"submission_id":"extra","expected_revision":1,"text":"x","checkpoint":{}}`); got.Code != 400 {
		t.Fatalf("unknown input accepted: %d", got.Code)
	}
	for i := 0; i < 2; i++ {
		if got := call(task.UserID, "POST", "/stop", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"cancelled"`) {
			t.Fatalf("stop/replay: %d", got.Code)
		}
	}
}

func TestExecutionTaskRouteAuthenticationAndCSRF(t *testing.T) {
	s, task := executionTaskFixture(t)
	now := time.Now().UTC()
	must(t, s.store.CreateSession(context.Background(), domain.AppSession{ID: "control-auth", UserID: task.UserID, TokenHash: hashToken("control-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	for _, test := range []struct {
		name                 string
		cookie, csrf, bearer bool
		want                 int
	}{
		{"anonymous", false, false, false, 401}, {"cookie without csrf", true, false, false, 403}, {"cookie with csrf", true, true, false, 200}, {"bearer", false, false, true, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/home/assistant/tasks/"+task.ID+"/stop", nil)
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "control-token"})
			}
			if test.csrf {
				request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-control"})
				request.Header.Set(csrfHeaderName, "csrf-control")
			}
			if test.bearer {
				request.Header.Set("Authorization", "Bearer control-token")
			}
			recorder := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status %d want %d: %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestExecutionTaskWireRecordsMatchReservedSchema(t *testing.T) {
	now := time.Now().UTC()
	task := domain.AssistantTask{ID: "task", SessionID: "session", State: "queued", Revision: 1, MaxTurns: 12, MaxCalls: 24, MaxTokens: 32000, MaxActiveMS: 300000, CreatedAt: now, UpdatedAt: now}
	snapshot := assistantTaskSnapshot(task)
	snapshot["effects_uncertain"] = false
	snapshot["calls"] = []map[string]any{{"call_id": "read", "tool": "notes.get", "state": "completed", "attempts": 1}}
	records := []map[string]any{snapshot}
	for _, eventType := range []string{"queued", "running", "searching", "reading", "preparing", "waiting_approval", "waiting_input", "waiting_client", "retrying", "checking", "completed", "failed", "cancelled", "followup"} {
		records = append(records, assistantTaskEventToAPI(domain.AssistantTaskEvent{TaskID: task.ID, Sequence: 1, Type: eventType, CreatedAt: now}))
	}
	for _, record := range records {
		raw, err := json.Marshal(record)
		must(t, err)
		var value any
		must(t, json.Unmarshal(raw, &value))
		if err := assistantschema.Validate(value); err != nil {
			t.Fatalf("wire schema mismatch: %v", err)
		}
	}
}
