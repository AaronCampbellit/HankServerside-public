package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestAppendNoteContentDefaultsToSingleNewline(t *testing.T) {
	appended, err := appendNoteContent("first line", protocol.NotesAppendRequest{Content: "second line"})
	if err != nil {
		t.Fatalf("appendNoteContent: %v", err)
	}
	if appended != "first line\nsecond line" {
		t.Fatalf("appended content = %q", appended)
	}
}

func TestAppendNoteContentHonorsBodyMarkdownAndExplicitSeparator(t *testing.T) {
	separator := "\n\n"
	appended, err := appendNoteContent("first paragraph", protocol.NotesAppendRequest{
		BodyMarkdown: "second paragraph",
		Separator:    &separator,
	})
	if err != nil {
		t.Fatalf("appendNoteContent: %v", err)
	}
	if appended != "first paragraph\n\nsecond paragraph" {
		t.Fatalf("appended content = %q", appended)
	}
}

func TestAppendNoteContentRequiresContent(t *testing.T) {
	_, err := appendNoteContent("first line", protocol.NotesAppendRequest{})
	if !errors.Is(err, errNoteAppendContentRequired) {
		t.Fatalf("appendNoteContent err = %v, want errNoteAppendContentRequired", err)
	}
}

func TestConditionalProfileNoteDeleteReturnsTheCurrentNote(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_http_conditional_delete", Email: "http-conditional-delete@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "http-conditional-delete-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_http_conditional_delete", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "work.md", "title": "Work", "body_markdown": "version one",
	}, &created)
	var updated protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPut, "/v1/me/notes/work.md", map[string]any{
		"title": "Work", "body_markdown": "version two", "expected_revision": created.Revision,
	}, &updated)

	request, err := http.NewRequest(http.MethodDelete, testServer.URL+"/v1/me/notes/work.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Hank-Expected-Revision", created.Revision)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("stale delete status = %d body=%s, want 409", response.StatusCode, body)
	}
	var conflict struct {
		Error   string                      `json:"error"`
		Current protocol.NotesFetchResponse `json:"current"`
	}
	if err := json.NewDecoder(response.Body).Decode(&conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error != "note_conflict" || conflict.Current.Revision != updated.Revision || conflict.Current.BodyMarkdown != "version two" {
		t.Fatalf("conflict = %#v", conflict)
	}

	request, err = http.NewRequest(http.MethodDelete, testServer.URL+"/v1/me/notes/work.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Hank-Expected-Revision", updated.Revision)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("matching delete status = %d body=%s, want 200", response.StatusCode, body)
	}
}

func TestProfileNotesKanbanWorkflowMetadataRoundTrips(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_kanban_metadata", Email: "kanban-metadata@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "kanban-metadata-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_kanban_metadata", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	board := &protocol.KanbanBoard{
		IntakeColumnID: "ideas",
		Columns: []protocol.KanbanColumn{{
			ID: "ideas", Title: "Brainstorm", Role: "planning", SortOrder: 0,
			Cards: []protocol.KanbanCard{{
				ID: "offline-sync", Text: "Research offline sync\nCapture constraints",
				SortOrder: 0, Color: "cyan", DueDate: "2026-07-24",
				Tags: []string{"Hank", "Research"},
			}},
		}},
	}

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", protocol.NotesSaveRequest{
		NoteID: "work", Title: "Work", PageType: protocol.NotePageTypeKanban, Board: board,
	}, nil)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/work", nil, &fetched)
	if fetched.Board == nil || fetched.Board.IntakeColumnID != "ideas" {
		t.Fatalf("fetched board = %#v", fetched.Board)
	}
	column := fetched.Board.Columns[0]
	card := column.Cards[0]
	if column.Role != "planning" || card.Color != "cyan" || card.DueDate != "2026-07-24" || strings.Join(card.Tags, ",") != "Hank,Research" {
		t.Fatalf("fetched workflow metadata = column:%#v card:%#v", column, card)
	}
}

func TestProfileNotesNotebookPinningRoundTripsAndRejectsInvalidTargets(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_note_pinning", Email: "note-pinning@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "note-pinning-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_note_pinning", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "projects", "title": "Projects", "page_type": "notebook", "pinned": true,
	}, nil)

	parentID := "projects"
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "pinned.md", "title": "Pinned", "page_type": "text", "parent_id": parentID, "pinned": true,
	}, nil)
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "root.md", "title": "Root", "page_type": "text", "pinned": true,
	}, nil)

	var child protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/pinned.md", nil, &child)
	if !child.Pinned {
		t.Fatal("notebook child pinned = false, want true")
	}

	var notebook protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/projects", nil, &notebook)
	if notebook.Pinned {
		t.Fatal("notebook pinned = true, want false")
	}

	var root protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/root.md", nil, &root)
	if root.Pinned {
		t.Fatal("root note pinned = true, want false")
	}

	var list protocol.NotesListResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes", nil, &list)
	foundPinned := false
	for _, note := range list.Notes {
		if note.ID == "pinned.md" {
			foundPinned = note.Pinned
		}
	}
	if !foundPinned {
		t.Fatal("profile notes list omitted pinned child state")
	}
}

func TestExternalProfileNotesAPIReadSearchTagsAndAppend(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_external_profile_notes", Email: "external-profile-notes@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "external-profile-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_external_profile_notes", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var save protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "external.md",
		"title":         "External API",
		"body_markdown": "#todo buy milk\nremember the receipt",
	}, &save)

	var search protocol.NotesSearchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/search?q=milk&limit=5", nil, &search)
	if len(search.Results) != 1 || search.Results[0].NoteID != "external.md" || !strings.Contains(search.Results[0].Preview, "milk") {
		t.Fatalf("search results = %#v, want external.md milk hit", search.Results)
	}

	var tags protocol.NotesTagsResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/tags", nil, &tags)
	if len(tags.Tags) != 1 || tags.Tags[0].Tag != "#todo" || tags.Tags[0].Count != 1 {
		t.Fatalf("tags = %#v, want one todo tag", tags.Tags)
	}

	var rollup protocol.NotesTagRollupResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/tag-rollup?tag=%23todo", nil, &rollup)
	if len(rollup.Items) != 1 || rollup.Items[0].NoteID != "external.md" || !strings.Contains(rollup.Items[0].LineText, "buy milk") {
		t.Fatalf("tag rollup = %#v, want external.md todo line", rollup.Items)
	}

	var appendResponse protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes/external.md/append", map[string]any{
		"body_markdown":     "synced from another app",
		"expected_revision": save.Revision,
	}, &appendResponse)
	if appendResponse.Revision == save.Revision {
		t.Fatalf("append revision = original revision %q, want new revision", appendResponse.Revision)
	}

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/external.md", nil, &fetched)
	if fetched.BodyMarkdown != "#todo buy milk\nremember the receipt\nsynced from another app" {
		t.Fatalf("fetched body = %q", fetched.BodyMarkdown)
	}

	response := requestJSONStatus(t, testServer, token, http.MethodPost, "/v1/me/notes/external.md/append", map[string]any{
		"content":           "stale write",
		"expected_revision": save.Revision,
	}, http.StatusConflict)
	response.Body.Close()
}

func TestProfileNotesNotebookLifecycleAndScopedSearch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_notebooks", Email: "profile-notebooks@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "profile-notebooks-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_profile_notebooks", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var notebookSave protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":   "projects",
		"title":     "Projects Notebook",
		"page_type": "notebook",
	}, &notebookSave)
	if notebookSave.PageType != protocol.NotePageTypeNotebook {
		t.Fatalf("notebook save page_type = %q, want %q", notebookSave.PageType, protocol.NotePageTypeNotebook)
	}

	var notebook protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/projects", nil, &notebook)
	if notebook.PageType != protocol.NotePageTypeNotebook || notebook.Title != "Projects Notebook" {
		t.Fatalf("notebook fetch = page_type:%q title:%q", notebook.PageType, notebook.Title)
	}

	parentID := "projects"
	var childSave protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "milk.md",
		"title":         "Milk Run",
		"body_markdown": "buy milk inside notebook",
		"page_type":     "text",
		"parent_id":     parentID,
	}, &childSave)

	var child protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/milk.md", nil, &child)
	if child.ParentID != "projects" {
		t.Fatalf("child parent_id = %q, want projects", child.ParentID)
	}

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "loose.md",
		"title":         "Loose Milk",
		"body_markdown": "buy milk outside notebook",
		"page_type":     "text",
	}, nil)

	var allSearch protocol.NotesSearchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/search?q=projects", nil, &allSearch)
	if len(allSearch.Results) != 1 || allSearch.Results[0].NoteID != "projects" || allSearch.Results[0].PageType != protocol.NotePageTypeNotebook {
		t.Fatalf("all search results = %#v, want notebook title hit", allSearch.Results)
	}

	var scopedSearch protocol.NotesSearchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/search?q=milk&notebook_id=projects", nil, &scopedSearch)
	if len(scopedSearch.Results) != 1 || scopedSearch.Results[0].NoteID != "milk.md" || scopedSearch.Results[0].ParentID != "projects" {
		t.Fatalf("scoped search results = %#v, want only milk.md under projects", scopedSearch.Results)
	}

	noParent := ""
	var moved protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPut, "/v1/me/notes/milk.md", map[string]any{
		"title":             "Milk Run",
		"body_markdown":     "buy milk inside notebook",
		"expected_revision": childSave.Revision,
		"page_type":         "text",
		"parent_id":         noParent,
	}, &moved)
	if moved.Revision == childSave.Revision {
		t.Fatalf("moved revision = original revision %q, want new revision", moved.Revision)
	}

	var movedChild protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/milk.md", nil, &movedChild)
	if movedChild.ParentID != "" {
		t.Fatalf("moved child parent_id = %q, want empty", movedChild.ParentID)
	}

	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/search?q=milk&notebook_id=projects", nil, &scopedSearch)
	if len(scopedSearch.Results) != 0 {
		t.Fatalf("scoped search after move = %#v, want no results", scopedSearch.Results)
	}
}

func TestProfileNotesRejectTextNoteAsNotebookParent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_notebook_parent", Email: "profile-notebook-parent@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "profile-notebook-parent-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_profile_notebook_parent", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "plain.md",
		"title":         "Plain note",
		"body_markdown": "not a notebook",
		"page_type":     "text",
	}, nil)

	response := requestJSONStatus(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "child.md",
		"title":         "Child",
		"body_markdown": "should not save",
		"page_type":     "text",
		"parent_id":     "plain.md",
	}, http.StatusBadRequest)
	response.Body.Close()
}

func TestExternalHomeNotesAPIUsesSharedVisibilityAndHomePermission(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_external_home_owner", Email: "external-home-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_external_home_member", Email: "external-home-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_external_notes", UserID: owner.ID, Name: "External Notes Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_external_home_owner", UserID: owner.ID, TokenHash: hashToken("external-owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_external_home_member", UserID: member.ID, TokenHash: hashToken("external-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var save protocol.NotesSaveResponse
	requestJSON(t, testServer, "external-owner-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "shared-external.md",
		"title":   "Shared External",
		"content": "#handoff shared milk note",
	}, &save)
	requestJSON(t, testServer, "external-owner-token", http.MethodPost, "/v1/home/notes/shared-external.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)

	var search protocol.NotesSearchResponse
	requestJSON(t, testServer, "external-member-token", http.MethodGet, "/v1/home/notes/search?q=milk", nil, &search)
	if len(search.Results) != 1 || search.Results[0].NoteID != "shared-external.md" {
		t.Fatalf("home search results = %#v, want shared-external.md", search.Results)
	}

	requestJSON(t, testServer, "external-member-token", http.MethodPost, "/v1/home/notes/shared-external.md/append", map[string]any{
		"content":           "member update",
		"expected_revision": save.Revision,
	}, nil)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, "external-owner-token", http.MethodGet, "/v1/home/notes/shared-external.md", nil, &fetched)
	if fetched.Content != "#handoff shared milk note\nmember update" {
		t.Fatalf("shared note content = %q", fetched.Content)
	}

	must(t, db.UpsertHomePermissions(ctx, domain.HomePermissions{
		HomeID:               home.ID,
		HomeAssistantEnabled: true,
		FilesEnabled:         true,
		NotesEnabled:         false,
		UpdatedAt:            now,
		UpdatedBy:            owner.ID,
	}))
	response := requestJSONStatus(t, testServer, "external-member-token", http.MethodGet, "/v1/home/notes/search?q=milk", nil, http.StatusForbidden)
	response.Body.Close()
}

func TestProfileNotesMCPExcludedRoundTripAndMCPVisibility(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_notes_mcp_excluded", Email: "profile-notes-mcp-excluded@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "profile-notes-mcp-excluded-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_profile_notes_mcp_excluded", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var notebookSave protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":      "private-notebook",
		"title":        "Private Notebook",
		"page_type":    "notebook",
		"mcp_excluded": true,
	}, &notebookSave)

	var notebook protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/private-notebook", nil, &notebook)
	if !notebook.MCPExcluded {
		t.Fatalf("notebook MCPExcluded = %v, want true", notebook.MCPExcluded)
	}

	parentID := "private-notebook"
	var childSave protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "child-hidden.md",
		"title":         "Child Hidden",
		"body_markdown": "#secret hidden by parent",
		"page_type":     "text",
		"parent_id":     parentID,
		"mcp_excluded":  false,
	}, &childSave)

	var child protocol.NotesFetchResponse
	child = protocol.NotesFetchResponse{}
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/child-hidden.md", nil, &child)
	if child.ParentID != "private-notebook" || child.MCPExcluded {
		t.Fatalf("child fetch = parent:%q excluded:%v", child.ParentID, child.MCPExcluded)
	}

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "private-note.md",
		"title":         "Private Note",
		"body_markdown": "#secret direct exclusion",
		"mcp_excluded":  true,
	}, nil)

	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "visible-note.md",
		"title":         "Visible Note",
		"body_markdown": "#todo visible to mcp",
	}, nil)

	var listed protocol.NotesListResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes", nil, &listed)
	listedByID := map[string]protocol.NoteSummary{}
	for _, note := range listed.Notes {
		listedByID[note.ID] = note
	}
	if !listedByID["private-notebook"].MCPExcluded || !listedByID["private-note.md"].MCPExcluded || listedByID["child-hidden.md"].MCPExcluded {
		t.Fatalf("list summaries = %#v", listedByID)
	}

	auth := mcpAuthContext{
		User: user,
		Token: domain.MCPToken{Scopes: []string{
			domain.NotesAPIScopeRead,
			domain.NotesAPIScopeWrite,
			domain.NotesAPIScopeAppend,
			domain.NotesAPIScopeDelete,
		}},
	}
	mcpCall := func(name string, arguments map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
		if err != nil {
			t.Fatalf("marshal mcp call: %v", err)
		}
		return server.mcpToolsCall(ctx, auth, raw)
	}

	mustContain := func(text string, needle string) {
		t.Helper()
		if !strings.Contains(text, needle) {
			t.Fatalf("text %q missing %q", text, needle)
		}
	}

	initialList := mcpCall("list_notes", map[string]any{})
	if initialList["isError"] == true {
		t.Fatalf("list_notes failed: %v", initialList)
	}
	initialListText := mcpFirstText(initialList)
	mustContain(initialListText, "visible-note.md")
	if strings.Contains(initialListText, "private-notebook") || strings.Contains(initialListText, "private-note.md") || strings.Contains(initialListText, "child-hidden.md") {
		t.Fatalf("list_notes exposed excluded notes: %q", initialListText)
	}

	hiddenSearch := mcpCall("search_notes", map[string]any{"query": "secret", "limit": 10})
	if hiddenSearch["isError"] == true {
		t.Fatalf("search_notes failed: %v", hiddenSearch)
	}
	if strings.Contains(mcpFirstText(hiddenSearch), "child-hidden.md") || strings.Contains(mcpFirstText(hiddenSearch), "private-note.md") {
		t.Fatalf("search_notes exposed excluded notes: %q", mcpFirstText(hiddenSearch))
	}

	tags := mcpCall("list_note_tags", map[string]any{})
	if tags["isError"] == true {
		t.Fatalf("list_note_tags failed: %v", tags)
	}
	tagText := mcpFirstText(tags)
	mustContain(tagText, "#todo")
	if strings.Contains(tagText, "#secret") {
		t.Fatalf("list_note_tags exposed excluded tags: %q", tagText)
	}

	for _, tc := range []struct {
		name      string
		arguments map[string]any
	}{
		{name: "get_note", arguments: map[string]any{"note_id": "private-note.md"}},
		{name: "get_note", arguments: map[string]any{"note_id": "child-hidden.md"}},
		{name: "update_note", arguments: map[string]any{"note_id": "private-note.md", "title": "Still Private", "content": "updated"}},
		{name: "append_note", arguments: map[string]any{"note_id": "private-note.md", "content": "updated"}},
		{name: "delete_note", arguments: map[string]any{"note_id": "private-note.md"}},
	} {
		res := mcpCall(tc.name, tc.arguments)
		if res["isError"] != true || mcpFirstText(res) != "not found" {
			t.Fatalf("%s(%v) = %v, want MCP not found", tc.name, tc.arguments, res)
		}
	}

	noParent := ""
	var moved protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPut, "/v1/me/notes/child-hidden.md", map[string]any{
		"title":             "Child Hidden",
		"body_markdown":     "#secret hidden by parent",
		"expected_revision": childSave.Revision,
		"page_type":         "text",
		"parent_id":         noParent,
		"mcp_excluded":      false,
	}, &moved)

	// parent_id is omitted at the root; do not retain it from the previous response.
	child = protocol.NotesFetchResponse{}
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/child-hidden.md", nil, &child)
	if child.ParentID != "" || child.MCPExcluded {
		t.Fatalf("moved child fetch = parent:%q excluded:%v", child.ParentID, child.MCPExcluded)
	}

	visibleGet := mcpCall("get_note", map[string]any{"note_id": "child-hidden.md"})
	if visibleGet["isError"] == true {
		t.Fatalf("get_note after move failed: %v", visibleGet)
	}
	mustContain(mcpFirstText(visibleGet), "hidden by parent")

	visibleSearch := mcpCall("search_notes", map[string]any{"query": "hidden", "limit": 10})
	if visibleSearch["isError"] == true {
		t.Fatalf("search_notes after move failed: %v", visibleSearch)
	}
	mustContain(mcpFirstText(visibleSearch), "child-hidden.md")
}

func TestProfileNotesUpdateOmittedMCPExcludedPreservesExistingLock(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_notes_lock_preserve", Email: "profile-notes-lock-preserve@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	token := "profile-notes-lock-preserve-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_profile_notes_lock_preserve", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":       "legacy-lock.md",
		"title":         "Legacy Lock",
		"body_markdown": "#locked content",
		"mcp_excluded":  true,
	}, &created)

	encodedUpdate, err := json.Marshal(protocol.NotesSaveRequest{
		Title:            "Legacy Lock Updated",
		BodyMarkdown:     "#locked content updated",
		ExpectedRevision: created.Revision,
	})
	if err != nil {
		t.Fatalf("marshal typed update request: %v", err)
	}
	t.Logf("encoded typed update request: %s", string(encodedUpdate))
	if strings.Contains(string(encodedUpdate), `"mcp_excluded"`) {
		t.Fatalf("typed update request = %s, want mcp_excluded omitted when unchanged", string(encodedUpdate))
	}

	var updated protocol.NotesSaveResponse
	requestJSON(t, testServer, token, http.MethodPut, "/v1/me/notes/legacy-lock.md", protocol.NotesSaveRequest{
		Title:            "Legacy Lock Updated",
		BodyMarkdown:     "#locked content updated",
		ExpectedRevision: created.Revision,
	}, &updated)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, token, http.MethodGet, "/v1/me/notes/legacy-lock.md", nil, &fetched)
	if fetched.Title != "Legacy Lock Updated" || fetched.BodyMarkdown != "#locked content updated" {
		t.Fatalf("updated fetch = title:%q body:%q", fetched.Title, fetched.BodyMarkdown)
	}
	if !fetched.MCPExcluded {
		t.Fatalf("MCPExcluded after omitted update = %v, want true", fetched.MCPExcluded)
	}
	noteRecord, err := db.GetProfileNote(ctx, user.ID, "legacy-lock.md")
	if err != nil {
		t.Fatalf("GetProfileNote after update: %v", err)
	}
	if !noteRecord.MCPExcluded {
		t.Fatalf("stored MCPExcluded after omitted update = %v, want true", noteRecord.MCPExcluded)
	}
}
