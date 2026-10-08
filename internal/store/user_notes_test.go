package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestConditionalProfileNoteDeleteRejectsAStaleRevision(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_conditional_delete", Email: "conditional-delete@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	original := domain.UserNote{
		ID: "note_conditional_delete", NoteID: "work.md", OwnerUserID: user.ID,
		Title: "Work", BodyMarkdown: "version one", BodyFormat: "markdown",
		PageType: "text", Revision: "rev-1", CRDTStateJSON: "{}",
		CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID,
	}
	if err := db.SaveUserNoteWithOperations(ctx, original, nil); err != nil {
		t.Fatalf("save original: %v", err)
	}
	current := original
	current.BodyMarkdown = "version two"
	current.Revision = "rev-2"
	current.UpdatedAt = now.Add(time.Second)
	if err := db.SaveUserNoteWithOperations(ctx, current, nil); err != nil {
		t.Fatalf("save current: %v", err)
	}

	deletedAt := now.Add(2 * time.Second)
	tombstone := original
	tombstone.BodyMarkdown = ""
	tombstone.Revision = "delete-rev"
	tombstone.DeletedAt = &deletedAt
	tombstone.UpdatedAt = deletedAt
	err = db.SaveUserNoteWithOperationsIfRevision(ctx, tombstone, nil, "rev-1")
	var conflict *UserNoteRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale delete error = %v, want UserNoteRevisionConflictError", err)
	}
	if conflict.Current.Revision != "rev-2" || conflict.Current.BodyMarkdown != "version two" {
		t.Fatalf("conflict current = revision %q body %q", conflict.Current.Revision, conflict.Current.BodyMarkdown)
	}
	fetched, err := db.GetProfileNote(ctx, user.ID, original.NoteID)
	if err != nil {
		t.Fatalf("GetProfileNote after conflict: %v", err)
	}
	if fetched.Revision != "rev-2" || fetched.DeletedAt != nil {
		t.Fatalf("stale delete changed note: revision=%q deleted_at=%v", fetched.Revision, fetched.DeletedAt)
	}

	tombstone = current
	tombstone.BodyMarkdown = ""
	tombstone.Revision = "delete-rev"
	tombstone.DeletedAt = &deletedAt
	tombstone.UpdatedAt = deletedAt
	if err := db.SaveUserNoteWithOperationsIfRevision(ctx, tombstone, nil, "rev-2"); err != nil {
		t.Fatalf("matching delete: %v", err)
	}
	deleted, err := db.ListProfileNotes(ctx, user.ID, true)
	if err != nil {
		t.Fatalf("ListProfileNotes: %v", err)
	}
	if len(deleted) != 1 || deleted[0].DeletedAt == nil {
		t.Fatalf("deleted notes = %#v", deleted)
	}
}

func TestUserNotesStoreFullMetadataAndPostgresNotify(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	notifications, err := db.Listen(ctx, NotificationChannelNotes)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_meta", Email: "notes-meta@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	parent := domain.UserNote{
		ID:            "note_meta_parent",
		NoteID:        "parent.md",
		OwnerUserID:   user.ID,
		Title:         "Parent",
		BodyFormat:    "markdown",
		PageType:      "notebook",
		Revision:      "rev-parent",
		CRDTStateJSON: "{}",
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     user.ID,
	}
	if err := db.UpsertUserNote(ctx, parent); err != nil {
		t.Fatalf("UpsertUserNote parent: %v", err)
	}

	note := domain.UserNote{
		ID:            "note_meta",
		NoteID:        "child.md",
		OwnerUserID:   user.ID,
		ParentID:      "parent.md",
		Pinned:        true,
		SortOrder:     7,
		Title:         "Child",
		Content:       "# Child",
		BodyMarkdown:  "# Child",
		BodyFormat:    "markdown",
		PageType:      "text",
		Revision:      "rev-meta",
		Checksum:      "sum-meta",
		CRDTStateJSON: "{}",
		CollabVersion: 1,
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     user.ID,
	}
	if err := db.SaveUserNoteWithOperations(ctx, note, []domain.NoteOperation{{
		NoteID:         note.ID,
		OpID:           "op-meta",
		ActorUserID:    user.ID,
		SessionID:      "test",
		BaseVersion:    0,
		AppliedVersion: 1,
		OpJSON:         `{"type":"text_replace","text":"# Child"}`,
		CreatedAt:      now,
	}}); err != nil {
		t.Fatalf("SaveUserNoteWithOperations: %v", err)
	}

	fetched, err := db.GetProfileNote(ctx, user.ID, "child.md")
	if err != nil {
		t.Fatalf("GetProfileNote: %v", err)
	}
	if fetched.ParentID != "parent.md" || !fetched.Pinned || fetched.SortOrder != 7 || fetched.BodyMarkdown != "# Child" || fetched.BodyFormat != "markdown" {
		t.Fatalf("metadata = parent:%q pinned:%t order:%d markdown:%q format:%q", fetched.ParentID, fetched.Pinned, fetched.SortOrder, fetched.BodyMarkdown, fetched.BodyFormat)
	}

	select {
	case notification := <-notifications:
		var payload struct {
			Event         string `json:"event"`
			NoteID        string `json:"note_id"`
			OwnerUserID   string `json:"owner_user_id"`
			CollabVersion int64  `json:"collab_version"`
		}
		if err := json.Unmarshal(notification.Payload, &payload); err != nil {
			t.Fatalf("notification payload json: %v", err)
		}
		if payload.Event != "notes.changed" || payload.NoteID != "child.md" || payload.OwnerUserID != user.ID || payload.CollabVersion != 1 {
			t.Fatalf("notification payload = %+v", payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for note notification")
	}
}

func TestUserNotesStoreEnforcesNotebookHierarchy(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_notes_hierarchy", Email: "notes-hierarchy@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	other := domain.User{ID: "usr_notes_hierarchy_other", Email: "notes-hierarchy-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}

	newNote := func(id, noteID, ownerID, pageType, parentID string) domain.UserNote {
		return domain.UserNote{
			ID:            id,
			NoteID:        noteID,
			OwnerUserID:   ownerID,
			ParentID:      parentID,
			Title:         noteID,
			BodyFormat:    "markdown",
			PageType:      pageType,
			Revision:      "rev-" + id,
			CRDTStateJSON: "{}",
			CreatedAt:     now,
			UpdatedAt:     now,
			UpdatedBy:     ownerID,
		}
	}
	mustSave := func(note domain.UserNote) {
		t.Helper()
		if err := db.UpsertUserNote(ctx, note); err != nil {
			t.Fatalf("UpsertUserNote %s: %v", note.NoteID, err)
		}
	}

	notebook := newNote("note_hierarchy_notebook", "projects", owner.ID, "notebook", "")
	plain := newNote("note_hierarchy_plain", "plain.md", owner.ID, "text", "")
	otherNotebook := newNote("note_hierarchy_other_notebook", "other-notebook", other.ID, "notebook", "")
	deletedNotebook := newNote("note_hierarchy_deleted_notebook", "deleted-notebook", owner.ID, "notebook", "")
	deletedNotebook.DeletedAt = &now
	mustSave(notebook)
	mustSave(plain)
	mustSave(otherNotebook)
	mustSave(deletedNotebook)

	child := newNote("note_hierarchy_child", "child.md", owner.ID, "text", notebook.NoteID)
	mustSave(child)
	fetched, err := db.GetProfileNote(ctx, owner.ID, child.NoteID)
	if err != nil {
		t.Fatalf("GetProfileNote child: %v", err)
	}
	if fetched.ParentID != notebook.NoteID {
		t.Fatalf("child parent_id = %q, want %q", fetched.ParentID, notebook.NoteID)
	}

	loose := newNote("note_hierarchy_loose", "loose.md", owner.ID, "text", "")
	mustSave(loose)
	fetched, err = db.GetProfileNote(ctx, owner.ID, loose.NoteID)
	if err != nil {
		t.Fatalf("GetProfileNote loose: %v", err)
	}
	if fetched.ParentID != "" {
		t.Fatalf("loose parent_id = %q, want empty", fetched.ParentID)
	}

	for _, test := range []struct {
		name     string
		id       string
		noteID   string
		parentID string
	}{
		{name: "missing parent", id: "note_hierarchy_missing", noteID: "missing-child.md", parentID: "missing"},
		{name: "cross-owner parent", id: "note_hierarchy_cross_owner", noteID: "cross-owner-child.md", parentID: otherNotebook.NoteID},
		{name: "non-notebook parent", id: "note_hierarchy_plain_parent", noteID: "plain-child.md", parentID: plain.NoteID},
		{name: "deleted notebook parent", id: "note_hierarchy_deleted_parent", noteID: "deleted-child.md", parentID: deletedNotebook.NoteID},
		{name: "self parent", id: "note_hierarchy_self", noteID: "self.md", parentID: "self.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			note := newNote(test.id, test.noteID, owner.ID, "text", test.parentID)
			if err := db.UpsertUserNote(ctx, note); err == nil {
				t.Fatalf("UpsertUserNote parent_id %q succeeded, want database integrity error", test.parentID)
			}
		})
	}

	deletedNotebook = notebook
	deletedNotebook.DeletedAt = &now
	if err := db.UpsertUserNote(ctx, deletedNotebook); err == nil {
		t.Fatal("soft-deleting notebook with active child succeeded, want database integrity error")
	}

	convertedNotebook := notebook
	convertedNotebook.PageType = "text"
	if err := db.UpsertUserNote(ctx, convertedNotebook); err == nil {
		t.Fatal("converting notebook with active child succeeded, want database integrity error")
	}

	child.ParentID = ""
	mustSave(child)
	mustSave(convertedNotebook)
}

func TestSaveUserNoteUsesCanonicalBodyMarkdownOnly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var contentColumnCount int
	if err := db.queryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'user_notes' AND column_name = 'content'`).Scan(&contentColumnCount); err != nil {
		t.Fatalf("count content column: %v", err)
	}
	if contentColumnCount != 0 {
		t.Fatalf("user_notes.content column count = %d, want 0", contentColumnCount)
	}

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_body", Email: "notes-body@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	note := domain.UserNote{
		ID:            "note_body",
		NoteID:        "board.md",
		OwnerUserID:   user.ID,
		Title:         "Board",
		Content:       "compatibility alias should not be stored",
		BodyMarkdown:  "# Canonical Board",
		BodyFormat:    "markdown",
		PageType:      "kanban",
		BoardJSON:     `{"columns":[]}`,
		Revision:      "rev-body",
		Checksum:      "sum-body",
		CRDTStateJSON: "{}",
		CollabVersion: 1,
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     user.ID,
	}
	if err := db.SaveUserNoteWithOperations(ctx, note, []domain.NoteOperation{{
		NoteID:         note.ID,
		OpID:           "op-body",
		ActorUserID:    user.ID,
		SessionID:      "test",
		BaseVersion:    0,
		AppliedVersion: 1,
		OpJSON:         `{"type":"replace_snapshot","page_type":"kanban"}`,
		CreatedAt:      now,
	}}); err != nil {
		t.Fatalf("SaveUserNoteWithOperations: %v", err)
	}

	fetched, err := db.GetProfileNote(ctx, user.ID, "board.md")
	if err != nil {
		t.Fatalf("GetProfileNote: %v", err)
	}
	if fetched.PageType != "kanban" || fetched.BodyMarkdown != "# Canonical Board" || fetched.Content != fetched.BodyMarkdown {
		t.Fatalf("fetched note page/body/content = %q/%q/%q", fetched.PageType, fetched.BodyMarkdown, fetched.Content)
	}
}

func TestUserNotesStoreMCPExcludedPersistence(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var excludedColumnCount int
	if err := db.queryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'user_notes' AND column_name = 'mcp_excluded'`).Scan(&excludedColumnCount); err != nil {
		t.Fatalf("count mcp_excluded column: %v", err)
	}
	if excludedColumnCount != 1 {
		t.Fatalf("user_notes.mcp_excluded column count = %d, want 1", excludedColumnCount)
	}

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_mcp_excluded", Email: "notes-mcp-excluded@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	note := domain.UserNote{
		ID:           "note_mcp_excluded",
		NoteID:       "private.md",
		OwnerUserID:  user.ID,
		Title:        "Private note",
		Content:      "hidden from mcp",
		BodyMarkdown: "hidden from mcp",
		BodyFormat:   "markdown",
		PageType:     "text",
		MCPExcluded:  true,
		Revision:     "rev-mcp-excluded",
		Checksum:     "sum-mcp-excluded",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	if err := db.SaveUserNoteWithOperations(ctx, note, []domain.NoteOperation{{
		NoteID:         note.ID,
		OpID:           "op-mcp-excluded",
		ActorUserID:    user.ID,
		SessionID:      "test",
		BaseVersion:    0,
		AppliedVersion: 1,
		OpJSON:         `{"type":"text_replace","text":"hidden from mcp"}`,
		CreatedAt:      now,
	}}); err != nil {
		t.Fatalf("SaveUserNoteWithOperations: %v", err)
	}

	fetched, err := db.GetProfileNote(ctx, user.ID, note.NoteID)
	if err != nil {
		t.Fatalf("GetProfileNote: %v", err)
	}
	if !fetched.MCPExcluded {
		t.Fatalf("GetProfileNote MCPExcluded = %v, want true", fetched.MCPExcluded)
	}

	listed, err := db.ListProfileNotes(ctx, user.ID, false)
	if err != nil {
		t.Fatalf("ListProfileNotes: %v", err)
	}
	if len(listed) != 1 || !listed[0].MCPExcluded {
		t.Fatalf("ListProfileNotes = %#v, want one MCP-excluded note", listed)
	}
}
