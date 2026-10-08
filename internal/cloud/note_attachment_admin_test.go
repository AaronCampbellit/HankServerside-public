package cloud

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestAdminNoteAttachmentContextsUseKanbanCardsWithoutDoubleCountingBodyProjection(t *testing.T) {
	record := domain.NoteAttachmentInventoryRecord{
		Attachment:   domain.NoteAttachment{ID: "natt-context"},
		NoteID:       "board.md",
		NoteTitle:    "Board",
		NotePageType: protocol.NotePageTypeKanban,
		BodyMarkdown: "![capture](hank-note-attachment://natt-context)",
		BoardJSON:    `{"columns":[{"id":"inbox","cards":[{"id":"one","text":"Site review\n![capture](hank-note-attachment://natt-context)"},{"id":"two","text":"Other"}]}]}`,
	}

	count, contexts := noteAttachmentReferenceContexts(record)
	if count != 1 || len(contexts) != 1 || contexts[0] != "Site review" {
		t.Fatalf("count = %d contexts = %#v", count, contexts)
	}
}

func TestAdminNoteAttachmentInventoryContentAndDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	admin := domain.User{ID: "usr_attachment_admin", Email: "attachment-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_attachment_member", Email: "attachment-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_admin", UserID: admin.ID, Name: "Attachment Home", CreatedAt: now, UpdatedAt: now}
	note := domain.UserNote{
		ID:            "note_attachment_admin",
		NoteID:        "attachment-admin.md",
		OwnerUserID:   admin.ID,
		Title:         "Attachment board",
		BodyMarkdown:  "![capture](hank-note-attachment://natt_admin)",
		BodyFormat:    "markdown",
		PageType:      protocol.NotePageTypeKanban,
		BoardJSON:     `{"columns":[{"id":"inbox","title":"Inbox","cards":[{"id":"card-one","text":"Site review\n![capture](hank-note-attachment://natt_admin)"}]}]}`,
		Revision:      "rev-admin",
		Checksum:      "sum-admin",
		CRDTStateJSON: "{}",
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     admin.ID,
	}
	attachment := domain.NoteAttachment{ID: "natt_admin", NoteID: note.ID, OwnerUserID: admin.ID, Filename: "capture.png", ContentType: "image/png", SizeBytes: 12, ChecksumSHA256: "sum", StorageKey: filepath.Join(note.ID, "natt_admin-capture.png"), CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_attachment_admin", UserID: admin.ID, TokenHash: hashToken("attachment-admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_attachment_member", UserID: member.ID, TokenHash: hashToken("attachment-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.UpsertUserNote(ctx, note))
	must(t, db.CreateNoteAttachment(ctx, attachment))

	root := t.TempDir()
	attachmentPath := filepath.Join(root, attachment.StorageKey)
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachmentPath, []byte("image-bytes!"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureNoteAttachmentStorage(root)
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, "attachment-member-token", http.MethodGet, "/v1/home/note-attachments", nil, http.StatusForbidden)
	response.Body.Close()

	var inventory struct {
		TotalFiles  int   `json:"total_files"`
		TotalBytes  int64 `json:"total_bytes"`
		Attachments []struct {
			ID             string   `json:"id"`
			NoteTitle      string   `json:"note_title"`
			OwnerEmail     string   `json:"owner_email"`
			ReferenceCount int      `json:"reference_count"`
			Contexts       []string `json:"contexts"`
			DownloadURL    string   `json:"download_url"`
		} `json:"attachments"`
	}
	requestJSON(t, testServer, "attachment-admin-token", http.MethodGet, "/v1/home/note-attachments", nil, &inventory)
	if inventory.TotalFiles != 1 || inventory.TotalBytes != 12 || len(inventory.Attachments) != 1 {
		t.Fatalf("inventory totals = %#v", inventory)
	}
	item := inventory.Attachments[0]
	if item.ID != attachment.ID || item.NoteTitle != note.Title || item.OwnerEmail != admin.Email || item.ReferenceCount != 1 || len(item.Contexts) != 1 || item.Contexts[0] != "Site review" {
		t.Fatalf("inventory item = %#v", item)
	}

	response = requestJSONStatus(t, testServer, "attachment-member-token", http.MethodGet, item.DownloadURL, nil, http.StatusForbidden)
	response.Body.Close()
	contentRequest, err := http.NewRequest(http.MethodGet, testServer.URL+item.DownloadURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	contentRequest.Header.Set("Authorization", "Bearer attachment-admin-token")
	contentResponse, err := testServer.Client().Do(contentRequest)
	if err != nil {
		t.Fatal(err)
	}
	contentResponse.Body.Close()
	if contentResponse.StatusCode != http.StatusOK {
		t.Fatalf("content status = %d", contentResponse.StatusCode)
	}

	var deleted protocol.NoteAttachmentDeleteResponse
	requestJSON(t, testServer, "attachment-admin-token", http.MethodDelete, "/v1/home/note-attachments/"+attachment.ID, nil, &deleted)
	if !deleted.OK || !deleted.CleanupComplete || deleted.NoteRevision == "" {
		t.Fatalf("delete response = %#v", deleted)
	}
	if _, err := os.Stat(attachmentPath); !os.IsNotExist(err) {
		t.Fatalf("attachment file still exists: %v", err)
	}
	updated, err := db.GetProfileNote(ctx, admin.ID, note.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated.BodyMarkdown+updated.BoardJSON, attachment.ID) {
		t.Fatalf("attachment reference remained after admin delete: %#v", updated)
	}
}
