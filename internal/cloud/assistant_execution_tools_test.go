package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/google/jsonschema-go/jsonschema"
)

func executionFixture(t *testing.T) (*Server, domain.Home, domain.User, *assistant.Registry) {
	t.Helper()
	ctx := context.Background()
	db := storeForTest(t)
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC()
	user := domain.User{ID: "usr_tools", Email: "tools@example.invalid", PasswordHash: "disabled", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_tools", UserID: user.ID, Name: "Tools", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	registry, err := s.newAssistantExecutionTools(home.ID, user.ID)
	must(t, err)
	return s, home, user, registry
}
func invokeExecution(t *testing.T, r *assistant.Registry, name string, args any) assistant.Result {
	t.Helper()
	raw, err := json.Marshal(args)
	must(t, err)
	return r.Invoke(context.Background(), assistant.Call{ID: "call_test", Tool: name, Version: 1, Arguments: raw})
}
func executionItems(t *testing.T, result assistant.Result) executionData {
	t.Helper()
	if result.Error != nil {
		t.Fatalf("tool failed: %s", result.Error.Code)
	}
	var data executionData
	must(t, json.Unmarshal(result.Data, &data))
	return data
}

func TestExecutionToolsNotesFollowupPreparationAndRevocation(t *testing.T) {
	s, home, user, r := executionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	note := domain.UserNote{ID: "note_tools", NoteID: "11111111-1111-4111-8111-111111111111", OwnerUserID: user.ID, Title: "Work", Content: "- existing", BodyMarkdown: "- existing", BodyFormat: "markdown", PageType: protocol.NotePageTypeText, Revision: "rev1", Checksum: "checksum", CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	must(t, s.store.UpsertUserNote(ctx, note))
	// Scripted model: search -> inspect exact returned ID -> prepare a write.
	search := executionItems(t, invokeExecution(t, r, "notes.search", map[string]any{"query": "Work", "limit": 10}))
	if len(search.Items) != 1 || search.Items[0].ID != note.ID {
		t.Fatal("search did not return exact owned note")
	}
	read := executionItems(t, invokeExecution(t, r, "notes.get", map[string]any{"note_id": search.Items[0].ID}))
	prepared := invokeExecution(t, r, "notes.append", map[string]any{"note_id": read.Items[0].ID, "revision": read.Items[0].Revision, "text": "new text"})
	executionItems(t, prepared)
	if prepared.Outcome != "not_started" || prepared.Verification != "pending" {
		t.Fatal("write must remain a proposal")
	}
	unchanged, err := s.store.GetUserNoteByID(ctx, note.ID)
	must(t, err)
	if unchanged.Content != note.Content {
		t.Fatal("preparation mutated the note")
	}
	stale := invokeExecution(t, r, "notes.append", map[string]any{"note_id": note.ID, "revision": "stale", "text": "new text"})
	if stale.Error == nil || stale.Error.Code != "revision_conflict" {
		t.Fatal("stale version accepted")
	}
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ProfileNotesEnabled = false
	settings.HomeNotesEnabled = false
	must(t, s.store.UpsertAssistantSettings(ctx, settings))
	denied := invokeExecution(t, r, "notes.get", map[string]any{"note_id": note.ID})
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatal("cached registry retained revoked source access")
	}
}

func TestExecutionToolsRejectForeignIDsAndTraversePaths(t *testing.T) {
	s, home, user, r := executionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	other := domain.User{ID: "usr_other_tools", Email: "other-tools@example.invalid", PasswordHash: "disabled", CreatedAt: now, UpdatedAt: now}
	must(t, s.store.CreateUser(ctx, other))
	must(t, s.store.UpsertUserNote(ctx, domain.UserNote{ID: "note_other", NoteID: "22222222-2222-4222-8222-222222222222", OwnerUserID: other.ID, Title: "Private", Content: "private canary", BodyFormat: "markdown", PageType: protocol.NotePageTypeText, Revision: "r1", Checksum: "s", CreatedAt: now, UpdatedAt: now, UpdatedBy: other.ID}))
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"notes.get", map[string]any{"note_id": "note_other"}},
		{"machines.status", map[string]any{"agent_id": "foreign_agent"}},
		{"evidence.read", map[string]any{"source": "conversation", "id": "foreign_session"}},
	} {
		result := invokeExecution(t, r, call.name, call.args)
		if result.Error == nil || result.Error.Code != "not_found" {
			t.Fatalf("foreign ID accepted by %s", call.name)
		}
	}
	for _, p := range []string{"../secret", "safe/../../secret", `safe\..\secret`, "safe/./secret", "\x00secret"} {
		result := invokeExecution(t, r, "files.list", map[string]any{"agent_id": "any", "source_id": "any", "path": p, "limit": 10})
		if result.Error == nil || result.Error.Code != "invalid_arguments" {
			t.Fatalf("traversal accepted: %q", p)
		}
	}
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ConversationsEnabled = false
	must(t, s.store.UpsertAssistantSettings(ctx, settings))
	denied := invokeExecution(t, r, "evidence.search", map[string]any{"source": "conversation", "query": "", "limit": 10})
	if denied.Error == nil || denied.Error.Code != "permission_denied" {
		t.Fatal("disabled conversation memory was searched")
	}
}

func TestExecutionCalendarSelectionUsesExactSnapshotAndDoesNotMutate(t *testing.T) {
	s, home, user, r := executionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	entry := domain.AssistantCalendarEntry{ID: "entry_1", HomeID: home.ID, UserID: user.ID, DeviceID: "device_1", ExternalEventID: "external_event_1", CalendarID: "personal", Title: "Dentist", StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour), SearchText: "Dentist", MetadataJSON: "{}", UpdatedAt: now}
	must(t, s.store.UpsertAssistantCalendarEntries(ctx, []domain.AssistantCalendarEntry{entry}))
	found := executionItems(t, invokeExecution(t, r, "calendar.search", map[string]any{"query": "Dentist", "limit": 10}))
	if len(found.Items) != 1 || found.Items[0].EventID != entry.ExternalEventID || found.Items[0].DeviceID != entry.DeviceID {
		t.Fatal("calendar target identity missing")
	}
	result := invokeExecution(t, r, "calendar.update_event", map[string]any{"entry_id": entry.ID, "revision": found.Items[0].Revision, "starts_at": now.Add(24 * time.Hour).Format(time.RFC3339), "ends_at": now.Add(25 * time.Hour).Format(time.RFC3339), "timezone": "UTC"})
	executionItems(t, result)
	if result.Outcome != "not_started" {
		t.Fatal("calendar proposal claimed execution")
	}
	createArgs := map[string]any{"device_id": entry.DeviceID, "calendar_id": entry.CalendarID, "title": "New meeting", "starts_at": now.Add(48 * time.Hour).Format(time.RFC3339), "ends_at": now.Add(49 * time.Hour).Format(time.RFC3339), "timezone": "UTC"}
	creation := invokeExecution(t, r, "calendar.create_event", createArgs)
	created := executionItems(t, creation)
	if creation.Outcome != "not_started" || created.Items[0].Type != "calendar" || created.Items[0].DeviceID != entry.DeviceID {
		t.Fatal("creation proposal lost exact calendar target")
	}
	createArgs["device_id"] = "foreign-device"
	denied := invokeExecution(t, r, "calendar.create_event", createArgs)
	if denied.Error == nil || denied.Error.Code != "not_found" {
		t.Fatal("foreign calendar target accepted")
	}
	rows, err := s.store.ListAssistantCalendarEntries(ctx, home.ID, user.ID)
	must(t, err)
	if len(rows) != 1 || !rows[0].StartsAt.Equal(entry.StartsAt) {
		t.Fatal("calendar snapshot mutated while preparing")
	}
}

func TestExecutionDefinitionsMatchPublishedContract(t *testing.T) {
	_, _, _, registry := executionFixture(t)
	raw, err := os.ReadFile("../../schemas/assistant/execution-v2.schema.json")
	must(t, err)
	var schema jsonschema.Schema
	must(t, json.Unmarshal(raw, &schema))
	resolved, err := schema.Resolve(nil)
	must(t, err)
	for _, def := range registry.Definitions() {
		raw, err := json.Marshal(def)
		must(t, err)
		var value any
		must(t, json.Unmarshal(raw, &value))
		if err := resolved.Validate(value); err != nil {
			t.Errorf("%s: %v", def.Name, err)
		}
	}
}
