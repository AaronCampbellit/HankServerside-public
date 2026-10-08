package notes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestSaveConflictReturnsCurrentNote(t *testing.T) {
	t.Parallel()

	service := New(t.TempDir())

	first, err := service.Save(context.Background(), "", "My Note", "first version", "", protocol.NotePageTypeText, nil)
	if err != nil {
		t.Fatalf("Save first: %v", err)
	}

	if _, err := service.Save(context.Background(), first.NoteID, "My Note", "second version", first.Revision, protocol.NotePageTypeText, nil); err != nil {
		t.Fatalf("Save second: %v", err)
	}

	_, err = service.Save(context.Background(), first.NoteID, "My Note", "stale write", first.Revision, protocol.NotePageTypeText, nil)
	if err == nil {
		t.Fatal("Save stale write: expected conflict")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Save stale write error = %v, want ErrConflict", err)
	}

	conflict := &ConflictError{}
	if !errors.As(err, &conflict) {
		t.Fatalf("Save stale write error = %T, want *ConflictError", err)
	}
	if conflict.Current.Content != "second version" {
		t.Fatalf("conflict current content = %q, want %q", conflict.Current.Content, "second version")
	}
}

func TestListAndFetchIncludeKanbanMetadata(t *testing.T) {
	t.Parallel()

	service := New(t.TempDir())
	board := &protocol.KanbanBoard{
		Columns: []protocol.KanbanColumn{
			{
				ID:        "col-1",
				Title:     "Doing",
				SortOrder: 0,
				Cards: []protocol.KanbanCard{
					{
						ID:        "card-1",
						Text:      "First card",
						SortOrder: 0,
						CreatedAt: time.Unix(10, 0).UTC(),
						UpdatedAt: time.Unix(20, 0).UTC(),
					},
				},
				CreatedAt: time.Unix(5, 0).UTC(),
				UpdatedAt: time.Unix(6, 0).UTC(),
			},
		},
		CreatedAt: time.Unix(1, 0).UTC(),
		UpdatedAt: time.Unix(2, 0).UTC(),
	}

	save, err := service.Save(context.Background(), "", "Board Note", "", "", protocol.NotePageTypeKanban, board)
	if err != nil {
		t.Fatalf("Save kanban: %v", err)
	}
	if save.PageType != protocol.NotePageTypeKanban {
		t.Fatalf("Save page type = %q, want %q", save.PageType, protocol.NotePageTypeKanban)
	}

	notes, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("List len = %d, want 1", len(notes))
	}
	if notes[0].PageType != protocol.NotePageTypeKanban {
		t.Fatalf("List page type = %q, want %q", notes[0].PageType, protocol.NotePageTypeKanban)
	}
	if notes[0].Preview == "" {
		t.Fatal("List preview: expected non-empty preview")
	}

	fetch, err := service.Fetch(context.Background(), save.NoteID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetch.PageType != protocol.NotePageTypeKanban {
		t.Fatalf("Fetch page type = %q, want %q", fetch.PageType, protocol.NotePageTypeKanban)
	}
	if fetch.Board == nil || len(fetch.Board.Columns) != 1 {
		t.Fatalf("Fetch board = %#v, want one column", fetch.Board)
	}
	if fetch.Content == "" {
		t.Fatal("Fetch content: expected generated markdown")
	}
}

func TestListAndFetchIncludeNotebookMetadata(t *testing.T) {
	t.Parallel()

	service := New(t.TempDir())

	save, err := service.Save(context.Background(), "", "Projects", "", "", protocol.NotePageTypeNotebook, nil)
	if err != nil {
		t.Fatalf("Save notebook: %v", err)
	}
	if save.PageType != protocol.NotePageTypeNotebook {
		t.Fatalf("Save page type = %q, want %q", save.PageType, protocol.NotePageTypeNotebook)
	}

	notes, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("List len = %d, want 1", len(notes))
	}
	if notes[0].PageType != protocol.NotePageTypeNotebook {
		t.Fatalf("List page type = %q, want %q", notes[0].PageType, protocol.NotePageTypeNotebook)
	}

	fetch, err := service.Fetch(context.Background(), save.NoteID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetch.PageType != protocol.NotePageTypeNotebook || fetch.Title != "Projects" {
		t.Fatalf("Fetch page type/title = %q/%q, want notebook/Projects", fetch.PageType, fetch.Title)
	}
	if fetch.Content != "" {
		t.Fatalf("Fetch content = %q, want empty notebook body", fetch.Content)
	}

	results, err := service.Search(context.Background(), "projects", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].NoteID != save.NoteID || results[0].PageType != protocol.NotePageTypeNotebook {
		t.Fatalf("Search results = %#v, want notebook title hit", results)
	}
}

func TestSearchTagsAndRollupsUseTextNotesOnly(t *testing.T) {
	t.Parallel()

	service := New(t.TempDir())

	_, err := service.Save(context.Background(), "", "Projects", "# todo: launch remote search\ncontext line\nneedle lives here", "", protocol.NotePageTypeText, nil)
	if err != nil {
		t.Fatalf("Save text note: %v", err)
	}
	_, err = service.Save(context.Background(), "", "Boards", "", "", protocol.NotePageTypeKanban, &protocol.KanbanBoard{
		Columns: []protocol.KanbanColumn{{ID: "c1", Title: "Todo", SortOrder: 0}},
	})
	if err != nil {
		t.Fatalf("Save kanban note: %v", err)
	}

	results, err := service.Search(context.Background(), "needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search len = %d, want 1", len(results))
	}
	if results[0].PageType != protocol.NotePageTypeText {
		t.Fatalf("Search page type = %q, want text", results[0].PageType)
	}
	if results[0].Preview == "" {
		t.Fatal("Search preview: expected non-empty preview")
	}

	tags, err := service.Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0].Tag != "todo" || tags[0].Count != 1 {
		t.Fatalf("Tags = %#v, want todo count 1", tags)
	}

	rollup, err := service.TagRollup(context.Background(), "todo")
	if err != nil {
		t.Fatalf("TagRollup: %v", err)
	}
	if len(rollup) != 1 {
		t.Fatalf("TagRollup len = %d, want 1", len(rollup))
	}
	if rollup[0].LineText != "launch remote search" {
		t.Fatalf("TagRollup line text = %q, want %q", rollup[0].LineText, "launch remote search")
	}
}
