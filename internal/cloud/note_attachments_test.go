package cloud

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestRemoveNoteAttachmentReferencesUpdatesMarkdownAndKanbanBoard(t *testing.T) {
	note := domain.UserNote{
		PageType:     protocol.NotePageTypeKanban,
		BodyMarkdown: "![capture](hank-note-attachment://natt-1)",
		BoardJSON:    `{"columns":[{"id":"inbox","cards":[{"id":"one","text":"One\n![capture](hank-note-attachment://natt-1)"},{"id":"two","text":"Keep me"}]}]}`,
	}

	updated, removed, err := removeNoteAttachmentReferences(note, "user-1", domain.NoteAttachment{ID: "natt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if removed < 2 {
		t.Fatalf("removed references = %d, want at least 2", removed)
	}
	if strings.Contains(updated.BodyMarkdown+updated.BoardJSON, "natt-1") {
		t.Fatalf("attachment reference remained: %#v", updated)
	}
	if !strings.Contains(updated.BoardJSON, "Keep me") {
		t.Fatalf("unrelated card changed: %s", updated.BoardJSON)
	}
}

func TestNoteAttachmentResponseIncludesPostUploadRevision(t *testing.T) {
	response := noteAttachmentToProtocol(
		domain.NoteAttachment{ID: "natt-1", Filename: "capture.png", ContentType: "image/png"},
		domain.UserNote{NoteID: "work", Revision: "revision-after-upload"},
		"profile",
	)
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal attachment response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode attachment response: %v", err)
	}
	if got := decoded["note_revision"]; got != "revision-after-upload" {
		t.Fatalf("note_revision = %#v, want post-upload revision", got)
	}
}
