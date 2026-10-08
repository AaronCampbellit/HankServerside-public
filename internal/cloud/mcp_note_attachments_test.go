package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/observability"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestDecodeMCPAttachmentChunkEnforcesOneBoundedInput(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	got, err := decodeMCPAttachmentChunk("image/png", encoded, "")
	if err != nil || !bytes.Equal(got, []byte("image-bytes")) {
		t.Fatalf("decoded = %q, err=%v", got, err)
	}
	if _, err := decodeMCPAttachmentChunk("text/html", encoded, "<p>both</p>"); err == nil {
		t.Fatal("both input forms succeeded")
	}
	if _, err := decodeMCPAttachmentChunk("image/png", "", "text"); err == nil {
		t.Fatal("image text chunk succeeded")
	}
	tooLarge := base64.StdEncoding.EncodeToString(make([]byte, maxMCPAttachmentChunkBytes+1))
	if _, err := decodeMCPAttachmentChunk("image/png", tooLarge, ""); err == nil {
		t.Fatal("oversized chunk succeeded")
	}
}

func TestMCPUploadLocksReleaseTerminalEntries(t *testing.T) {
	service := &mcpNoteAttachmentService{}
	for index := 0; index < 32; index++ {
		unlock := service.lockUpload(fmt.Sprintf("mcpup_%d", index))
		unlock()
	}
	count := 0
	service.uploadLocks.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("released upload locks retained = %d, want 0", count)
	}
}

func TestMaterializeMCPAttachmentTargetAppendsExactReference(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	note := domain.UserNote{ID: "note-one", NoteID: "one", OwnerUserID: "usr", Title: "One", BodyMarkdown: "Intro", Content: "Intro", BodyFormat: "markdown", PageType: protocol.NotePageTypeText, Revision: "old", CollabVersion: 2, CreatedAt: now, UpdatedAt: now, UpdatedBy: "usr"}
	attachment := domain.NoteAttachment{ID: "natt-html", NoteID: note.ID, OwnerUserID: "usr", Filename: "site.html", ContentType: "text/html"}
	target := mcpAttachmentTarget{Note: note, ColumnIndex: -1, CardIndex: -1, Markdown: "Intro"}
	updated, operation, err := materializeMCPAttachmentTarget(target, attachment, "usr", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated.BodyMarkdown, "[site.html](hank-note-attachment://natt-html?") || updated.Revision == note.Revision || updated.CollabVersion != 3 {
		t.Fatalf("updated = %#v", updated)
	}
	if operation.NoteID != note.ID || operation.BaseVersion != 2 || operation.AppliedVersion != 3 {
		t.Fatalf("operation = %#v", operation)
	}
}

func TestMaterializeMCPAttachmentTargetChangesOnlyExactCard(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	board := testMCPKanbanBoard()
	encoded, _ := json.Marshal(board)
	note := domain.UserNote{ID: "note-board", NoteID: "work", OwnerUserID: "usr", Title: "Work", BodyMarkdown: kanbanMarkdown("Work", *board), Content: kanbanMarkdown("Work", *board), BodyFormat: "markdown", PageType: protocol.NotePageTypeKanban, BoardJSON: string(encoded), Revision: "old", CollabVersion: 4, CreatedAt: now, UpdatedAt: now, UpdatedBy: "usr"}
	attachment := domain.NoteAttachment{ID: "natt-image", NoteID: note.ID, OwnerUserID: "usr", Filename: "capture.png", ContentType: "image/png"}
	target := mcpAttachmentTarget{Note: note, Board: board, ColumnIndex: 0, CardIndex: 0, Markdown: board.Columns[0].Cards[0].Text}
	updated, _, err := materializeMCPAttachmentTarget(target, attachment, "usr", now)
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.KanbanBoard
	if err := json.Unmarshal([]byte(updated.BoardJSON), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Columns[0].Cards[0].Text, "![capture.png](hank-note-attachment://natt-image?") || got.Columns[1].Cards[0].Text != board.Columns[1].Cards[0].Text {
		t.Fatalf("board = %#v", got)
	}
}

func TestMCPAttachmentListFiltersExactCardReferences(t *testing.T) {
	ctx, service, notes, db, userID, _ := setupMCPAttachmentService(t)
	board := testMCPKanbanBoard()
	board.Columns[0].Cards[0].Text += "\n![first](hank-note-attachment://natt-first)"
	board.Columns[1].Cards[0].Text += "\n![second](hank-note-attachment://natt-second)"
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, board)
	note, err := db.GetProfileNote(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, attachment := range []domain.NoteAttachment{
		{ID: "natt-first", NoteID: note.ID, OwnerUserID: userID, Filename: "first.png", ContentType: "image/png", SizeBytes: 1, ChecksumSHA256: strings.Repeat("a", 64), StorageKey: "work/first.png"},
		{ID: "natt-second", NoteID: note.ID, OwnerUserID: userID, Filename: "second.png", ContentType: "image/png", SizeBytes: 1, ChecksumSHA256: strings.Repeat("b", 64), StorageKey: "work/second.png"},
	} {
		attachment.CreatedAt = time.Now().UTC()
		attachment.UpdatedAt = attachment.CreatedAt
		if err := db.CreateNoteAttachment(ctx, attachment); err != nil {
			t.Fatal(err)
		}
	}

	got, err := service.List(ctx, userID, mcpAttachmentTargetArgs{Kind: "kanban_card", BoardID: "work", CardID: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision == "" || len(got.Attachments) != 1 || got.Attachments[0].ID != "natt-first" {
		t.Fatalf("list = %#v", got)
	}
}

func TestMCPAttachmentReadReturnsHTMLSourceAndImageContent(t *testing.T) {
	ctx, service, notes, db, userID, root := setupMCPAttachmentService(t)
	body := "<!doctype html><p>Hello, Hank</p>"
	response, err := notes.SaveProfile(ctx, userID, "site", protocol.NotesSaveRequest{NoteID: "site", Title: "Site", Content: "[site.html](hank-note-attachment://natt-html)\n![image](hank-note-attachment://natt-image)"})
	if err != nil {
		t.Fatal(err)
	}
	note, err := db.GetProfileNote(ctx, userID, response.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	preview := pngBytes(t, 3, 2)
	writeAttachmentTestFile(t, root, "site/site.html", []byte(body))
	writeAttachmentTestFile(t, root, "site/image.png", pngBytes(t, 4, 3))
	writeAttachmentTestFile(t, root, "site/image-preview.png", preview)
	now := time.Now().UTC()
	for _, attachment := range []domain.NoteAttachment{
		{ID: "natt-html", NoteID: note.ID, OwnerUserID: userID, Filename: "site.html", ContentType: "text/html", SizeBytes: int64(len(body)), ChecksumSHA256: strings.Repeat("c", 64), StorageKey: "site/site.html", CreatedAt: now, UpdatedAt: now},
		{ID: "natt-image", NoteID: note.ID, OwnerUserID: userID, Filename: "image.png", ContentType: "image/png", SizeBytes: 10, ChecksumSHA256: strings.Repeat("d", 64), StorageKey: "site/image.png", PreviewStorageKey: "site/image-preview.png", PreviewContentType: "image/png", PreviewSizeBytes: int64(len(preview)), PreviewChecksumSHA256: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.CreateNoteAttachment(ctx, attachment); err != nil {
			t.Fatal(err)
		}
	}
	target := mcpAttachmentTargetArgs{Kind: "note", NoteID: "site"}
	htmlResult, err := service.Read(ctx, userID, mcpAttachmentReadArgs{Target: target, AttachmentID: "natt-html", Representation: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(htmlResult.Content) != 2 || htmlResult.Content[1]["type"] != "text" || htmlResult.Content[1]["text"] != body || !htmlResult.EOF {
		t.Fatalf("html read = %#v", htmlResult)
	}
	imageResult, err := service.Read(ctx, userID, mcpAttachmentReadArgs{Target: target, AttachmentID: "natt-image", Representation: "image_preview"})
	if err != nil {
		t.Fatal(err)
	}
	if len(imageResult.Content) != 2 || imageResult.Content[1]["type"] != "image" || imageResult.Content[1]["mimeType"] != "image/png" {
		t.Fatalf("image read = %#v", imageResult)
	}
	decoded, err := base64.StdEncoding.DecodeString(imageResult.Content[1]["data"].(string))
	if err != nil || string(decoded) != string(preview) {
		t.Fatalf("image data mismatch err=%v", err)
	}
}

func TestMCPAttachmentReadLazilyCreatesHistoricalImagePreview(t *testing.T) {
	ctx, service, notes, db, userID, root := setupMCPAttachmentService(t)
	response, err := notes.SaveProfile(ctx, userID, "legacy", protocol.NotesSaveRequest{NoteID: "legacy", Title: "Legacy", Content: "![legacy](hank-note-attachment://natt-legacy)"})
	if err != nil {
		t.Fatal(err)
	}
	note, err := db.GetProfileNote(ctx, userID, response.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	body := pngBytes(t, 5, 4)
	writeAttachmentTestFile(t, root, "legacy/legacy.png", body)
	now := time.Now().UTC()
	if err := db.CreateNoteAttachment(ctx, domain.NoteAttachment{ID: "natt-legacy", NoteID: note.ID, OwnerUserID: userID, Filename: "legacy.png", ContentType: "image/png", SizeBytes: int64(len(body)), ChecksumSHA256: strings.Repeat("f", 64), StorageKey: "legacy/legacy.png", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Read(ctx, userID, mcpAttachmentReadArgs{Target: mcpAttachmentTargetArgs{Kind: "note", NoteID: "legacy"}, AttachmentID: "natt-legacy", Representation: "image_preview"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(result.Content) != 2 || result.Content[1]["type"] != "image" {
		t.Fatalf("read = %#v", result)
	}
	stored, err := db.GetNoteAttachment(ctx, note.ID, "natt-legacy")
	if err != nil || stored.PreviewStorageKey == "" {
		t.Fatalf("stored attachment = %#v, err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(root, stored.PreviewStorageKey)); err != nil {
		t.Fatalf("preview file: %v", err)
	}
}

func TestMCPAttachmentFinishAddsHTMLToTextNote(t *testing.T) {
	ctx, service, notes, _, userID, _ := setupMCPAttachmentService(t)
	saved, err := notes.SaveProfile(ctx, userID, "finish", protocol.NotesSaveRequest{NoteID: "finish", Title: "Finish", Content: "Intro"})
	if err != nil {
		t.Fatal(err)
	}
	body := "<!doctype html><p>Finished</p>"
	started, err := service.Start(ctx, userID, mcpAttachmentStartArgs{Target: mcpAttachmentTargetArgs{Kind: "note", NoteID: "finish"}, Filename: "site.html", ContentType: "text/html", ExpectedRevision: saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadChunk(ctx, userID, mcpAttachmentChunkArgs{UploadID: started.UploadID, Offset: 0, Text: body}); err != nil {
		t.Fatal(err)
	}
	finished, err := service.Finish(ctx, userID, mcpAttachmentFinishArgs{UploadID: started.UploadID})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Attachment.ID == "" || finished.Status != "completed" || finished.TargetRevision == saved.Revision {
		t.Fatalf("finished = %#v", finished)
	}
	fetched, err := notes.FetchProfile(ctx, userID, "finish")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fetched.BodyMarkdown, "[site.html](hank-note-attachment://"+finished.Attachment.ID) {
		t.Fatalf("body = %q", fetched.BodyMarkdown)
	}
}

func TestMCPAttachmentRevisionConflictPreservesRetryableUpload(t *testing.T) {
	ctx, service, notes, db, userID, root := setupMCPAttachmentService(t)
	saved, err := notes.SaveProfile(ctx, userID, "retry", protocol.NotesSaveRequest{NoteID: "retry", Title: "Retry", Content: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, userID, mcpAttachmentStartArgs{Target: mcpAttachmentTargetArgs{Kind: "note", NoteID: "retry"}, Filename: "retry.html", ContentType: "text/html", ExpectedRevision: saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadChunk(ctx, userID, mcpAttachmentChunkArgs{UploadID: started.UploadID, Offset: 0, Text: "<p>Retry</p>"}); err != nil {
		t.Fatal(err)
	}
	changed, err := notes.SaveProfile(ctx, userID, "retry", protocol.NotesSaveRequest{NoteID: "retry", Title: "Retry", Content: "Changed", ExpectedRevision: saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, userID, mcpAttachmentFinishArgs{UploadID: started.UploadID}); mcpAttachmentErrorCode(err) != "revision_conflict" {
		t.Fatalf("Finish conflict error = %v", err)
	}
	upload, err := db.GetMCPNoteAttachmentUpload(ctx, started.UploadID, userID)
	if err != nil || upload.Status != "open" {
		t.Fatalf("upload = %#v, err=%v", upload, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(upload.StagingKey))); err != nil {
		t.Fatalf("retry staging file: %v", err)
	}
	if _, err := service.Finish(ctx, userID, mcpAttachmentFinishArgs{UploadID: started.UploadID, ExpectedRevision: changed.Revision}); err != nil {
		t.Fatalf("retry Finish: %v", err)
	}
}

func TestMCPAttachmentSharedReplacementRequiresConfirmationAndRetainsID(t *testing.T) {
	ctx, service, notes, db, userID, root := setupMCPAttachmentService(t)
	attachmentID := "natt-shared-replacement"
	body := "[one](hank-note-attachment://" + attachmentID + ")\n\n[two](hank-note-attachment://" + attachmentID + ")"
	saved, err := notes.SaveProfile(ctx, userID, "shared", protocol.NotesSaveRequest{NoteID: "shared", Title: "Shared", Content: body})
	if err != nil {
		t.Fatal(err)
	}
	note, err := db.GetProfileNote(ctx, userID, "shared")
	if err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("<p>Old</p>")
	writeAttachmentTestFile(t, root, "shared/old.html", oldBody)
	now := time.Now().UTC()
	if err := db.CreateNoteAttachment(ctx, domain.NoteAttachment{ID: attachmentID, NoteID: note.ID, OwnerUserID: userID, Filename: "shared.html", ContentType: "text/html", SizeBytes: int64(len(oldBody)), ChecksumSHA256: strings.Repeat("a", 64), StorageKey: "shared/old.html", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, userID, mcpAttachmentStartArgs{Target: mcpAttachmentTargetArgs{Kind: "note", NoteID: "shared"}, Filename: "new.html", ContentType: "text/html", ExpectedRevision: saved.Revision, ReplaceAttachmentID: attachmentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadChunk(ctx, userID, mcpAttachmentChunkArgs{UploadID: started.UploadID, Offset: 0, Text: "<p>New</p>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, userID, mcpAttachmentFinishArgs{UploadID: started.UploadID}); mcpAttachmentErrorCode(err) != "shared_replacement_confirmation_required" {
		t.Fatalf("Finish shared error = %v", err)
	}
	finished, err := service.Finish(ctx, userID, mcpAttachmentFinishArgs{UploadID: started.UploadID, ConfirmSharedReplacement: true})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Attachment.ID != attachmentID || finished.ReferenceCount != 2 {
		t.Fatalf("finished = %#v", finished)
	}
}

func TestMCPAttachmentTargetsRespectExclusionAndOwnership(t *testing.T) {
	ctx, service, notes, db, userID, _ := setupMCPAttachmentService(t)
	excluded := true
	if _, err := notes.SaveProfile(ctx, userID, "locked", protocol.NotesSaveRequest{NoteID: "locked", Title: "Locked", Content: "Private", MCPExcluded: &excluded}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, userID, mcpAttachmentTargetArgs{Kind: "note", NoteID: "locked"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("excluded target error = %v", err)
	}
	otherID := userID + "_other"
	now := time.Now().UTC()
	if err := db.CreateUser(ctx, domain.User{ID: otherID, Email: otherID + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.SaveProfile(ctx, otherID, "foreign", protocol.NotesSaveRequest{NoteID: "foreign", Title: "Foreign", Content: "Other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, userID, mcpAttachmentTargetArgs{Kind: "note", NoteID: "foreign"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign target error = %v", err)
	}
}

func mcpAttachmentErrorCode(err error) string {
	var target *mcpAttachmentError
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func TestCountAttachmentReferencesDoesNotDoubleCountMaterializedKanbanBody(t *testing.T) {
	attachmentID := "natt-shared"
	board := protocol.KanbanBoard{Columns: []protocol.KanbanColumn{{
		ID: "col-1",
		Cards: []protocol.KanbanCard{
			{ID: "card-1", Text: "[file](hank-note-attachment://natt-shared)"},
			{ID: "card-2", Text: "![image](hank-note-attachment://natt-shared)"},
		},
	}}}
	boardJSON, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	note := domain.UserNote{
		PageType:     protocol.NotePageTypeKanban,
		BoardJSON:    string(boardJSON),
		BodyMarkdown: "[file](hank-note-attachment://natt-shared)\n![image](hank-note-attachment://natt-shared)",
	}
	if got := countAttachmentReferences(note, attachmentID); got != 2 {
		t.Fatalf("reference count = %d, want 2", got)
	}
}

func TestMCPAttachmentPrunePreservesOpenStagingAndRemovesTerminalStaging(t *testing.T) {
	ctx, service, notes, db, userID, root := setupMCPAttachmentService(t)
	saved, err := notes.SaveProfile(ctx, userID, "prune", protocol.NotesSaveRequest{NoteID: "prune", Title: "Prune", Content: "Body"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, userID, mcpAttachmentStartArgs{Target: mcpAttachmentTargetArgs{Kind: "note", NoteID: "prune"}, Filename: "page.html", ContentType: "text/html", ExpectedRevision: saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadChunk(ctx, userID, mcpAttachmentChunkArgs{UploadID: started.UploadID, Offset: 0, Text: "<p>staged</p>"}); err != nil {
		t.Fatal(err)
	}
	upload, err := db.GetMCPNoteAttachmentUpload(ctx, started.UploadID, userID)
	if err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(root, filepath.FromSlash(upload.StagingKey))
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stagingPath, old, old); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, noteAttachmentRoot: root, metrics: observability.NewMetrics(), logger: slog.Default()}
	if err := server.pruneNoteAttachmentFiles(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagingPath); err != nil {
		t.Fatalf("open staging file was pruned: %v", err)
	}
	if err := db.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err := server.pruneNoteAttachmentFiles(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("terminal staging file still exists: %v", err)
	}
	if err := server.pruneNoteAttachmentFiles(ctx, time.Now(), time.Hour); err != nil {
		t.Fatalf("repeat prune with absent terminal staging file: %v", err)
	}
}

func setupMCPAttachmentService(t *testing.T) (context.Context, *mcpNoteAttachmentService, *cloudNotesService, *store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	db := storeForTest(t)
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC()
	userID := "usr_mcp_attachment_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_")
	must(t, db.CreateUser(ctx, domain.User{ID: userID, Email: userID + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}))
	root := t.TempDir()
	notes := newCloudNotesService(db)
	return ctx, newMCPNoteAttachmentService(db, root, func() time.Time { return now }), notes, db, userID, root
}

func writeAttachmentTestFile(t *testing.T, root, key string, body []byte) {
	t.Helper()
	path := filepath.Join(root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
