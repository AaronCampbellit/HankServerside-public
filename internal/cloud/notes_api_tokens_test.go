package cloud

import (
	"context"
	"encoding/json"
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

func TestNotesAPITokenLifecycleScopesAndRevocation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_api_owner", Email: "notes-api-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notes_api", UserID: user.ID, Name: "Notes API Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "notes-api-admin-session"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_notes_api_admin", UserID: user.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	root := t.TempDir()
	server.ConfigureNoteAttachmentStorage(root)
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created struct {
		Token    string               `json:"token"`
		APIToken domain.NotesAPIToken `json:"api_token"`
		Scopes   []string             `json:"scopes"`
	}
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/notes-api-tokens", map[string]any{
		"name":             "Obsidian append",
		"scopes":           []string{domain.NotesAPIScopeRead, domain.NotesAPIScopeAppend},
		"allow_home_notes": true,
	}, &created)
	if !strings.HasPrefix(created.Token, "hank_note_") {
		t.Fatalf("token prefix = %q", created.Token)
	}
	if created.APIToken.TokenHash != "" {
		t.Fatalf("api token response exposed token hash")
	}
	if created.APIToken.Name != "Obsidian append" || !created.APIToken.AllowHomeNotes {
		t.Fatalf("api token response = %#v", created.APIToken)
	}

	var save protocol.NotesSaveResponse
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "api-token-note.md",
		"title":   "API Token Note",
		"content": "initial",
	}, &save)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, created.Token, http.MethodGet, "/v1/me/notes/api-token-note.md", nil, &fetched)
	if fetched.Content != "initial" {
		t.Fatalf("fetched content = %q", fetched.Content)
	}

	requestJSON(t, testServer, created.Token, http.MethodPost, "/v1/me/notes/api-token-note.md/append", map[string]any{
		"content":           "from token",
		"expected_revision": save.Revision,
	}, nil)

	uploadReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/me/notes/api-token-note.md/attachments?filename=screenshot.png", strings.NewReader("fake-png-bytes"))
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	uploadReq.Header.Set("Authorization", "Bearer "+created.Token)
	uploadReq.Header.Set("Content-Type", "image/png")
	uploadResp, err := testServer.Client().Do(uploadReq)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(uploadResp.Body)
		t.Fatalf("upload status = %d body = %s", uploadResp.StatusCode, body)
	}
	var uploaded struct {
		protocol.NoteAttachment
		NoteRevision string `json:"note_revision"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode uploaded attachment: %v", err)
	}
	if uploaded.Filename != "screenshot.png" || uploaded.ContentType != "image/png" || uploaded.SizeBytes != int64(len("fake-png-bytes")) {
		t.Fatalf("uploaded attachment = %#v", uploaded)
	}
	if !strings.HasPrefix(uploaded.MarkdownRef, "![screenshot.png](hank-note-attachment://") {
		t.Fatalf("uploaded markdown reference = %q, want inline image reference", uploaded.MarkdownRef)
	}
	if uploaded.NoteRevision == "" || uploaded.NoteRevision == save.Revision {
		t.Fatalf("uploaded note revision = %q, want the post-upload revision", uploaded.NoteRevision)
	}

	requestJSON(t, testServer, created.Token, http.MethodGet, "/v1/me/notes/api-token-note.md", nil, &fetched)
	if !strings.Contains(fetched.BodyMarkdown, "![screenshot.png](hank-note-attachment://") {
		t.Fatalf("fetched body did not include inline image reference: %q", fetched.BodyMarkdown)
	}
	if len(fetched.Attachments) != 1 || fetched.Attachments[0].ID != uploaded.ID {
		t.Fatalf("fetched attachments = %#v, want uploaded attachment", fetched.Attachments)
	}

	assertNoteAttachmentBackupReadable(t, root)

	downloadReq, err := http.NewRequest(http.MethodGet, testServer.URL+uploaded.DownloadURL, nil)
	if err != nil {
		t.Fatalf("new download request: %v", err)
	}
	downloadReq.Header.Set("Authorization", "Bearer "+created.Token)
	downloadResp, err := testServer.Client().Do(downloadReq)
	if err != nil {
		t.Fatalf("download attachment: %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(downloadResp.Body)
		t.Fatalf("download status = %d body = %s", downloadResp.StatusCode, body)
	}
	if got := downloadResp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Fatalf("Content-Disposition = %q, want inline image", got)
	}
	downloaded, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(downloaded) != "fake-png-bytes" {
		t.Fatalf("downloaded attachment = %q", downloaded)
	}
	noteRecord, err := db.GetProfileNote(ctx, user.ID, "api-token-note.md")
	if err != nil {
		t.Fatalf("get note record: %v", err)
	}
	attachmentRecord, err := db.GetNoteAttachment(ctx, noteRecord.ID, uploaded.ID)
	if err != nil {
		t.Fatalf("get attachment record: %v", err)
	}
	attachmentPath := filepath.Join(root, attachmentRecord.StorageKey)
	if _, err := os.Stat(attachmentPath); err != nil {
		t.Fatalf("stat uploaded attachment: %v", err)
	}
	var deleted protocol.NoteAttachmentDeleteResponse
	requestJSON(t, testServer, adminToken, http.MethodDelete, uploaded.DownloadURL, nil, &deleted)
	if !deleted.OK || !deleted.CleanupComplete || deleted.NoteRevision == "" {
		t.Fatalf("delete response = %#v", deleted)
	}
	if _, err := os.Stat(attachmentPath); !os.IsNotExist(err) {
		t.Fatalf("attachment file still exists after delete: %v", err)
	}
	response := requestJSONStatus(t, testServer, adminToken, http.MethodGet, uploaded.DownloadURL, nil, http.StatusNotFound)
	response.Body.Close()
	// Decode into a fresh value: omitted empty fields must not retain the previous response.
	fetched = protocol.NotesFetchResponse{}
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/me/notes/api-token-note.md", nil, &fetched)
	if strings.Contains(fetched.BodyMarkdown, uploaded.ID) || len(fetched.Attachments) != 0 {
		t.Fatalf("deleted attachment remained in note: %#v", fetched)
	}

	response = requestJSONStatus(t, testServer, created.Token, http.MethodDelete, "/v1/me/notes/api-token-note.md", nil, http.StatusForbidden)
	response.Body.Close()

	var list struct {
		Tokens []domain.NotesAPIToken `json:"tokens"`
	}
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/home/notes-api-tokens", nil, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].LastUsedAt == nil || list.Tokens[0].RequestCount < 2 {
		t.Fatalf("usage metadata = %#v", list.Tokens)
	}
	if list.Tokens[0].LastUsedRoute != "DELETE /v1/me/notes/api-token-note.md" {
		t.Fatalf("last route = %q", list.Tokens[0].LastUsedRoute)
	}

	requestJSON(t, testServer, adminToken, http.MethodDelete, "/v1/home/notes-api-tokens/"+created.APIToken.ID, nil, nil)
	response = requestJSONStatus(t, testServer, created.Token, http.MethodGet, "/v1/me/notes/api-token-note.md", nil, http.StatusUnauthorized)
	response.Body.Close()
}

func assertNoteAttachmentBackupReadable(t *testing.T, root string) {
	t.Helper()

	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if entry.IsDir() {
			if mode&0o077 != 0o070 || info.Mode()&os.ModeSetgid == 0 {
				t.Fatalf("attachment directory %s mode = %o, want private shared-group access and setgid", path, mode)
			}
			return nil
		}
		if mode&0o077 != 0o060 {
			t.Fatalf("attachment file %s mode = %o, want private shared-group read/write for backup", path, mode)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk attachment root: %v", err)
	}
}

func TestRepairNoteAttachmentBackupPermissionsFixesLegacyModes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	noteDir := filepath.Join(root, "note_legacy")
	if err := os.MkdirAll(noteDir, 0o700); err != nil {
		t.Fatalf("mkdir note dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(noteDir, "natt_legacy-receipt.png"), []byte("receipt"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	server := &Server{noteAttachmentRoot: root}
	if err := server.repairNoteAttachmentBackupPermissions(); err != nil {
		t.Fatalf("repairNoteAttachmentBackupPermissions: %v", err)
	}

	assertNoteAttachmentBackupReadable(t, root)
}

func TestNotesAPITokenReadScopeCannotWrite(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_api_read", Email: "notes-api-read@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notes_api_read", UserID: user.ID, Name: "Notes API Read Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "notes-api-read-admin-session"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_notes_api_read_admin", UserID: user.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created struct {
		Token    string               `json:"token"`
		APIToken domain.NotesAPIToken `json:"api_token"`
	}
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/notes-api-tokens", map[string]any{
		"name":   "Read only",
		"scopes": []string{domain.NotesAPIScopeRead},
	}, &created)

	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "read-only.md",
		"title":   "Read Only",
		"content": "read me",
	}, nil)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, created.Token, http.MethodGet, "/v1/me/notes/read-only.md", nil, &fetched)
	if fetched.Content != "read me" {
		t.Fatalf("fetched content = %q", fetched.Content)
	}

	response := requestJSONStatus(t, testServer, created.Token, http.MethodPost, "/v1/me/notes/read-only.md/append", map[string]any{"content": "blocked"}, http.StatusForbidden)
	response.Body.Close()
	response = requestJSONStatus(t, testServer, created.Token, http.MethodPost, "/v1/me/notes", map[string]any{"note_id": "blocked.md", "content": "blocked"}, http.StatusForbidden)
	response.Body.Close()
}

func TestNotesAPITokenCreateRejectsUnsafeScopes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notes_api_bad_scope", Email: "notes-api-bad-scope@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notes_api_bad_scope", UserID: user.ID, Name: "Notes API Bad Scope Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "notes-api-bad-scope-admin-session"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_notes_api_bad_scope_admin", UserID: user.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, adminToken, http.MethodPost, "/v1/home/notes-api-tokens", map[string]any{
		"name":   "Bad",
		"scopes": []string{"files:read"},
	}, http.StatusBadRequest)
	defer response.Body.Close()
	errorBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	if !strings.Contains(string(errorBody), "unsupported scope") {
		t.Fatalf("error body = %q", string(errorBody))
	}
}
