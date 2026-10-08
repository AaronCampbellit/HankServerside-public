package cloud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestProfileNoteTrashPreservesRestoresAndPermanentlyDeletesContent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_note_trash", Email: "note-trash@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	service := newCloudNotesService(db)

	created, err := service.SaveProfile(ctx, user.ID, "trash-test.md", protocol.NotesSaveRequest{
		NoteID: "trash-test.md", Title: "Restore me", BodyMarkdown: "important body", BodyFormat: "markdown", PageType: protocol.NotePageTypeText,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := service.DeleteProfile(ctx, user.ID, created.NoteID, created.Revision); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	stored, err := db.GetProfileNote(ctx, user.ID, created.NoteID)
	if err != nil || stored.DeletedAt == nil || stored.BodyMarkdown != "important body" || stored.PageType != protocol.NotePageTypeText {
		t.Fatalf("trashed note did not preserve content: %#v, %v", stored, err)
	}
	trash, err := service.ListDeletedProfile(ctx, user.ID)
	if err != nil || len(trash) != 1 || trash[0].Title != "Restore me" || trash[0].DeletedAt == nil {
		t.Fatalf("ListDeletedProfile = %#v, %v", trash, err)
	}
	if err := service.RestoreProfile(ctx, user.ID, created.NoteID); err != nil {
		t.Fatalf("RestoreProfile: %v", err)
	}
	restored, err := service.FetchProfile(ctx, user.ID, created.NoteID)
	if err != nil || restored.BodyMarkdown != "important body" || restored.Title != "Restore me" {
		t.Fatalf("restored note = %#v, %v", restored, err)
	}
	if err := service.DeleteProfile(ctx, user.ID, created.NoteID, restored.Revision); err != nil {
		t.Fatalf("DeleteProfile again: %v", err)
	}
	if _, err := service.PermanentlyDeleteProfile(ctx, user.ID, created.NoteID); err != nil {
		t.Fatalf("PermanentlyDeleteProfile: %v", err)
	}
	if _, err := db.GetProfileNote(ctx, user.ID, created.NoteID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetProfileNote after permanent delete = %v, want ErrNotFound", err)
	}
}
