package cloud

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestExecutionSourceOwnershipAndRevocation(t *testing.T) {
	s, task := executionTaskFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	must(t, s.store.UpsertUserNote(ctx, domain.UserNote{ID: "source-note", NoteID: "11111111-1111-4111-8111-111111111111", OwnerUserID: task.UserID, Title: "Synthetic", Content: "private source text", BodyMarkdown: "private source text", BodyFormat: "markdown", PageType: protocol.NotePageTypeText, Revision: "r1", Checksum: "s", CreatedAt: now, UpdatedAt: now, UpdatedBy: task.UserID}))
	task, err := s.store.ClaimAssistantTask(ctx, "source-worker", time.Minute)
	must(t, err)
	model := executionModelFunc(func(context.Context, assistant.ModelRequest) (assistant.Turn, error) {
		return assistant.Turn{SchemaVersion: 2, Kind: "model_turn", FinishReason: "tool_calls", Calls: []assistant.Call{{ID: "source-call", Tool: "notes.get", Version: 1, Arguments: json.RawMessage(`{"note_id":"source-note"}`)}}}, nil
	})
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	task, err = s.advanceAssistantExecution(ctx, task, model)
	must(t, err)
	request := func(user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/home/assistant/tasks/"+task.ID+"/source?call_id=source-call&item=0", nil)
		w := httptest.NewRecorder()
		s.handleAssistantExecutionTask(w, r, domain.Home{ID: task.HomeID}, authContext{User: domain.User{ID: user}}, []string{task.ID, "source"})
		return w
	}
	if got := request("foreign"); got.Code != 404 {
		t.Fatal("foreign source exposed")
	}
	got := request(task.UserID)
	if got.Code != 200 || !strings.Contains(got.Body.String(), "private source text") || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("owned source unavailable or unsafe")
	}
	settings := defaultAssistantSettings(task.HomeID, task.UserID)
	settings.ProfileNotesEnabled = false
	must(t, s.store.UpsertAssistantSettings(ctx, settings))
	if got := request(task.UserID); got.Code != 404 || strings.Contains(got.Body.String(), "private source text") {
		t.Fatal("revoked source exposed")
	}
}

func TestExecutionCitationsRetainOriginalTaskAfterFollowup(t *testing.T) {
	result := executionResult(assistant.Call{ID: "original-call"}, []executionItem{{Type: "document", ID: "guide", Title: "Guide", URI: "hank://docs/guide"}}, 1, false)
	sources := executionCitations(domain.AssistantTask{ID: "new-task"}, []assistant.Message{{Role: "tool", OriginTaskID: "original-task", Result: &result}}, "Answer")
	if len(sources) != 1 || !strings.Contains(sources[0].Href, "/original-task/source?") || !strings.Contains(sources[0].Href, "call_id=original-call") {
		t.Fatal("cross-task citation lost durable origin")
	}
}

func TestExecutionFileLinksDistinguishPreviewFromFolder(t *testing.T) {
	file := executionFileURI("agent", "source", "/Reports/budget.md", false)
	folder := executionFileURI("agent", "source", "/Reports", true)
	if !strings.Contains(file, "preview=1") || strings.Contains(folder, "preview=") || !strings.Contains(file, "agent_id=agent") || !strings.Contains(file, "source_id=source") || !strings.Contains(file, "path=%2FReports%2Fbudget.md") {
		t.Fatal("file link lost preview or exact target")
	}
}

func TestExecutionNoteCitationDecodesPathBeforeQueryEncoding(t *testing.T) {
	result := executionResult(assistant.Call{ID: "note"}, []executionItem{{Type: "note", ID: "internal-note", Title: "Work Plan", URI: "hank://notes/Work%20Plan.md"}}, 1, false)
	sources := executionCitations(domain.AssistantTask{ID: "task"}, []assistant.Message{{Role: "tool", Result: &result}}, "Answer")
	if len(sources) != 1 || sources[0].Href != "/dashboard/profile-notes?note=Work+Plan.md" {
		t.Fatal("note ID was double encoded")
	}
}

func TestExecutionCalendarSourcesUseOwnedObservationLinks(t *testing.T) {
	result := executionResult(assistant.Call{ID: "calendar"}, []executionItem{
		{Type: "calendar_event", ID: "first", Title: "First", URI: "hank://calendar/shared-id?calendar_id=one&device_id=device"},
		{Type: "calendar_event", ID: "second", Title: "Second", URI: "hank://calendar/shared-id?calendar_id=two&device_id=device"},
	}, 2, false)
	sources := executionCitations(domain.AssistantTask{ID: "task"}, []assistant.Message{{Role: "tool", Result: &result}}, "Answer")
	if len(sources) != 2 || !strings.Contains(sources[0].Href, "/task/source?") || sources[0].Href == sources[1].Href {
		t.Fatal("calendar source identity or readable link lost")
	}
}
