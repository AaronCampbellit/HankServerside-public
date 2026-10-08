package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestMergeSMBPublicConfigPreservesUnpatchedSourcesAndPolicies(t *testing.T) {
	t.Parallel()

	existing := `{
		"shares":[{"id":"media","host":"nas.local","share":"media","policy":{"read":true,"write":false}}],
		"folders":[{"id":"docs","root":"/srv/docs","policy":{"read":true,"write":false}}]
	}`
	folderPatch := json.RawMessage(`{"folders":[{"id":"archive","root":"/srv/archive","policy":{"read":true,"write":false}}]}`)
	merged, err := mergeServicePublicConfig(domain.ServiceTypeSMB, existing, folderPatch)
	if err != nil {
		t.Fatalf("merge folder patch: %v", err)
	}
	var folderResult map[string]any
	if err := json.Unmarshal(merged, &folderResult); err != nil {
		t.Fatalf("decode folder result: %v", err)
	}
	shares, _ := folderResult["shares"].([]any)
	if len(shares) != 1 {
		t.Fatalf("shares after folder patch = %#v, want preserved share", folderResult["shares"])
	}
	share := shares[0].(map[string]any)
	if policy, _ := share["policy"].(map[string]any); policy["write"] != false {
		t.Fatalf("share policy after folder patch = %#v, want write=false", share["policy"])
	}

	sharePatch := json.RawMessage(`{"shares":[{"id":"backup","host":"backup.local","share":"backup","policy":{"read":true}}]}`)
	merged, err = mergeServicePublicConfig(domain.ServiceTypeSMB, string(merged), sharePatch)
	if err != nil {
		t.Fatalf("merge share patch: %v", err)
	}
	var shareResult map[string]any
	if err := json.Unmarshal(merged, &shareResult); err != nil {
		t.Fatalf("decode share result: %v", err)
	}
	folders, _ := shareResult["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("folders after share patch = %#v, want preserved folder", shareResult["folders"])
	}
	folder := folders[0].(map[string]any)
	if policy, _ := folder["policy"].(map[string]any); policy["write"] != false {
		t.Fatalf("folder policy after share patch = %#v, want write=false", folder["policy"])
	}
}

func TestNoteSummariesSortByMostRecentFirst(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	summaries := noteSummaries([]domain.UserNote{
		{
			ID:        "stored_old",
			NoteID:    "old",
			Title:     "Alpha",
			Content:   "old",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now.Add(-2 * time.Hour),
		},
		{
			ID:        "stored_new",
			NoteID:    "new",
			Title:     "Zulu",
			Content:   "new",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now,
		},
		{
			ID:        "stored_middle",
			NoteID:    "middle",
			Title:     "Beta",
			Content:   "middle",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now.Add(-time.Hour),
		},
	})

	got := []string{summaries[0].ID, summaries[1].ID, summaries[2].ID}
	want := []string{"new", "middle", "old"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("note order = %v, want %v", got, want)
		}
	}
}

func TestNormalizePageTypeAcceptsLegacyBoardAsKanban(t *testing.T) {
	t.Parallel()

	if got := normalizePageType("board"); got != protocol.NotePageTypeKanban {
		t.Fatalf("normalizePageType(board) = %q, want %q", got, protocol.NotePageTypeKanban)
	}
}

func TestTitleFromContentUsesFirstWords(t *testing.T) {
	t.Parallel()

	got := titleFromContent("Store list needs milk eggs bread and coffee")
	want := "Store list needs milk eggs"
	if got != want {
		t.Fatalf("titleFromContent = %q, want %q", got, want)
	}
}

func TestImageAttachmentReferenceUsesMarkdownImage(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	note := domain.UserNote{NoteID: "store.md"}
	attachment := domain.NoteAttachment{
		ID:          "natt_image",
		Filename:    "receipt.png",
		ContentType: "image/png",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	reference := noteAttachmentMarkdownReference(note, "profile", attachment)
	if !strings.HasPrefix(reference, "![receipt.png](hank-note-attachment://natt_image?") {
		t.Fatalf("reference = %q, want markdown image attachment", reference)
	}
}

func TestNoteAttachmentPathForWriteAllowsMissingNoteDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server := &Server{noteAttachmentRoot: root}
	path, err := server.noteAttachmentPathForWrite(filepath.Join("note_123", "natt_456-receipt.png"))
	if err != nil {
		t.Fatalf("noteAttachmentPathForWrite: %v", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks root: %v", err)
	}
	wantPrefix := filepath.Join(realRoot, "note_123") + string(filepath.Separator)
	if !strings.HasPrefix(path, wantPrefix) {
		t.Fatalf("path = %q, want under %q", path, wantPrefix)
	}
}

func TestProfileNotesRequireExplicitHomeShare(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	ownerSession := domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, ownerSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var invite struct {
		Token string `json:"token"`
	}
	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/members/invitations", map[string]any{
		"email": member.Email,
		"role":  domain.HomeRoleMember,
	}, &invite)
	requestJSON(t, testServer, "member-token", http.MethodPost, "/v1/home/invitations/accept", map[string]any{
		"token": invite.Token,
	}, nil)

	var created protocol.NotesSaveResponse
	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "shared.md",
		"title":   "Shared",
		"content": "family checklist #shared",
	}, &created)

	response := requestJSONStatus(t, testServer, "member-token", http.MethodGet, "/v1/home/notes/shared.md", nil, http.StatusNotFound)
	response.Body.Close()

	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/notes/shared.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, "member-token", http.MethodGet, "/v1/home/notes/shared.md", nil, &fetched)
	if fetched.Content != "family checklist #shared" {
		t.Fatalf("note content = %q, want %q", fetched.Content, "family checklist #shared")
	}
}

func TestRemovingMemberRevokesSharedNoteAccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	ownerSession := domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.CreateSession(ctx, ownerSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "shared.md",
		"title":   "Shared",
		"content": "family checklist",
	}, nil)
	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/notes/shared.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)

	requestJSON(t, testServer, "owner-token", http.MethodDelete, "/v1/home/members/"+member.ID, nil, nil)

	response := requestJSONStatus(t, testServer, "member-token", http.MethodGet, "/v1/home/notes/shared.md", nil, http.StatusNotFound)
	response.Body.Close()
}

func TestOwnerCanListAndRevokePendingInvitations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	ownerSession := domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, ownerSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created struct {
		InvitationID string `json:"invitation_id"`
	}
	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/members/invitations", map[string]any{
		"email": "member@example.com",
		"role":  domain.HomeRoleMember,
	}, &created)

	var auditPayload struct {
		Events []struct {
			EventType string `json:"event_type"`
			TargetID  string `json:"target_id"`
		} `json:"events"`
	}
	requestJSON(t, testServer, "owner-token", http.MethodGet, "/v1/home/audit-events?event_type=invitation.created", nil, &auditPayload)
	if len(auditPayload.Events) != 1 {
		t.Fatalf("invitation audit events = %d, want 1", len(auditPayload.Events))
	}
	if auditPayload.Events[0].TargetID != created.InvitationID {
		t.Fatalf("invitation audit target = %q, want %q", auditPayload.Events[0].TargetID, created.InvitationID)
	}

	var list struct {
		Invitations []domain.HomeInvitation `json:"invitations"`
	}
	requestJSON(t, testServer, "owner-token", http.MethodGet, "/v1/home/members/invitations", nil, &list)
	if len(list.Invitations) != 1 || list.Invitations[0].ID != created.InvitationID {
		t.Fatalf("invitations = %#v, want %q", list.Invitations, created.InvitationID)
	}

	requestJSON(t, testServer, "owner-token", http.MethodDelete, "/v1/home/members/invitations/"+created.InvitationID, nil, nil)
	requestJSON(t, testServer, "owner-token", http.MethodGet, "/v1/home/members/invitations", nil, &list)
	if len(list.Invitations) != 0 {
		t.Fatalf("invitations after delete = %#v, want empty", list.Invitations)
	}
}

func TestNotesCollaborationBroadcastsOpsAndRevocation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	ownerSession := domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.CreateSession(ctx, ownerSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "shared.md",
		"title":   "Shared",
		"content": "hello",
	}, nil)
	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/notes/shared.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)

	ownerConn, _, err := appWebSocketDial(ctx, testServer, "owner-token")
	if err != nil {
		t.Fatalf("owner websocket dial: %v", err)
	}
	defer ownerConn.Close(websocket.StatusNormalClosure, "done")

	memberConn, _, err := appWebSocketDial(ctx, testServer, "member-token")
	if err != nil {
		t.Fatalf("member websocket dial: %v", err)
	}
	defer memberConn.Close(websocket.StatusNormalClosure, "done")

	joinOwner, err := protocol.NewEnvelope(protocol.TypeAppCommand, "join_owner", "", home.ID, protocol.RoutedCommand{
		Command: "notes.collab.join",
		Body:    mustEncodeBody(t, protocol.NoteCollaborationJoinRequest{NoteID: "shared.md", SessionID: "owner-live"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	must(t, wsjson.Write(ctx, ownerConn, joinOwner))
	readUntilRequestID(t, ctx, ownerConn, "join_owner")

	joinMember, err := protocol.NewEnvelope(protocol.TypeAppCommand, "join_member", "", home.ID, protocol.RoutedCommand{
		Command: "notes.collab.join",
		Body:    mustEncodeBody(t, protocol.NoteCollaborationJoinRequest{NoteID: "shared.md", SessionID: "member-live"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	must(t, wsjson.Write(ctx, memberConn, joinMember))
	readUntilRequestID(t, ctx, memberConn, "join_member")
	readUntilEvent(t, ctx, ownerConn, "notes.collab.presence")

	submit, err := protocol.NewEnvelope(protocol.TypeAppCommand, "submit_ops", "", home.ID, protocol.RoutedCommand{
		Command: "notes.collab.submit_ops",
		Body: mustEncodeBody(t, protocol.NoteCollaborationSubmitOpsRequest{
			NoteID:      "shared.md",
			SessionID:   "owner-live",
			BaseVersion: 1,
			Ops: []protocol.NoteCollaborationOperation{{
				OpID:  "op-1",
				Type:  "text_insert",
				Index: 5,
				Text:  " world",
			}},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	must(t, wsjson.Write(ctx, ownerConn, submit))
	readUntilRequestID(t, ctx, ownerConn, "submit_ops")

	event := readUntilEvent(t, ctx, memberConn, "notes.collab.ops")
	payload, err := protocol.DecodePayload[protocol.AppEvent](event)
	if err != nil {
		t.Fatal(err)
	}
	var ops protocol.NoteCollaborationOpsEvent
	if err := json.Unmarshal(payload.Body, &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Ops) != 1 || ops.Ops[0].Text != " world" {
		t.Fatalf("unexpected ops payload: %#v", ops)
	}

	requestJSON(t, testServer, "owner-token", http.MethodDelete, "/v1/home/notes/shared.md/shares/"+member.ID, nil, nil)
	revoked := readUntilEvent(t, ctx, memberConn, "notes.collab.revoked")
	revokedPayload, err := protocol.DecodePayload[protocol.AppEvent](revoked)
	if err != nil {
		t.Fatal(err)
	}
	var revokedBody protocol.NoteCollaborationRevokedEvent
	if err := json.Unmarshal(revokedPayload.Body, &revokedBody); err != nil {
		t.Fatal(err)
	}
	if revokedBody.NoteID != "shared.md" {
		t.Fatalf("revoked note_id = %q, want %q", revokedBody.NoteID, "shared.md")
	}
}

func TestMemberCannotUpdateServiceProfile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	ownerSession := domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.CreateSession(ctx, ownerSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, "member-token", http.MethodPut, "/v1/home/service-profiles/smb", map[string]any{
		"public_config": map[string]any{"host": "nas.local", "share": "docs"},
	}, http.StatusForbidden)
	defer response.Body.Close()
}

func TestSMBServiceProfileTestRejectsMemberAndOfflineAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: owner.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()
	body := map[string]any{"id": "archive", "host": "nas.local", "share": "archive", "password": "secret"}

	memberResponse := requestJSONStatus(t, testServer, "member-token", http.MethodPost, "/v1/home/service-profiles/smb/test", body, http.StatusForbidden)
	memberResponse.Body.Close()
	offlineResponse := requestJSONStatus(t, testServer, "owner-token", http.MethodPost, "/v1/home/service-profiles/smb/test", body, http.StatusConflict)
	defer offlineResponse.Body.Close()
	data, err := io.ReadAll(offlineResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("offline response exposed password: %s", data)
	}
}

func TestHomePermissionsDenyMemberNotesAccessWithoutOverride(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	adminSession := domain.AppSession{ID: "sess_admin", UserID: admin.ID, TokenHash: hashToken("admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.UpsertHomePermissions(ctx, domain.HomePermissions{
		HomeID:               home.ID,
		HomeAssistantEnabled: true,
		FilesEnabled:         true,
		NotesEnabled:         false,
		UpdatedAt:            now,
		UpdatedBy:            admin.ID,
	}))
	must(t, db.CreateSession(ctx, adminSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, "member-token", http.MethodGet, "/v1/home/notes", nil, http.StatusForbidden)
	defer response.Body.Close()
}

func TestHomeMemberPermissionsOverrideRestoresNotesAccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	adminSession := domain.AppSession{ID: "sess_admin", UserID: admin.ID, TokenHash: hashToken("admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	memberSession := domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	override := true

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.UpsertHomePermissions(ctx, domain.HomePermissions{
		HomeID:               home.ID,
		HomeAssistantEnabled: true,
		FilesEnabled:         true,
		NotesEnabled:         false,
		UpdatedAt:            now,
		UpdatedBy:            admin.ID,
	}))
	must(t, db.UpsertHomeMemberPermissions(ctx, domain.HomeMemberPermissions{
		HomeID:       home.ID,
		UserID:       member.ID,
		NotesEnabled: &override,
		UpdatedAt:    now,
		UpdatedBy:    admin.ID,
	}))
	must(t, db.CreateSession(ctx, adminSession))
	must(t, db.CreateSession(ctx, memberSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		Notes []protocol.NoteSummary `json:"notes"`
	}
	requestJSON(t, testServer, "member-token", http.MethodGet, "/v1/home/notes", nil, &payload)
	if len(payload.Notes) != 0 {
		t.Fatalf("notes = %#v, want empty slice", payload.Notes)
	}
}

func TestNotesCommandsUseCloudStoreWhenAgentOffline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Offline Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken("session-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	appConn, _, err := appWebSocketDial(ctx, testServer, "session-token")
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	save, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_notes_save", "", home.ID, protocol.RoutedCommand{
		Command: "notes.save",
		Body: mustEncodeBody(t, protocol.NotesSaveRequest{
			NoteID:  "shared.md",
			Title:   "Shared",
			Content: "offline-safe note",
		}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope save: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, save); err != nil {
		t.Fatalf("app save write: %v", err)
	}

	var saveResponse protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &saveResponse); err != nil {
		t.Fatalf("app save response read: %v", err)
	}
	if saveResponse.Type != protocol.TypeAppResponse {
		t.Fatalf("save response type = %q, want %q", saveResponse.Type, protocol.TypeAppResponse)
	}

	fetch, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_notes_fetch", "", home.ID, protocol.RoutedCommand{
		Command: "notes.fetch",
		Body:    mustEncodeBody(t, protocol.NotesFetchRequest{NoteID: "shared.md"}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope fetch: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, fetch); err != nil {
		t.Fatalf("app fetch write: %v", err)
	}

	var fetchResponse protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &fetchResponse); err != nil {
		t.Fatalf("app fetch response read: %v", err)
	}
	if fetchResponse.Type != protocol.TypeAppResponse {
		t.Fatalf("fetch response type = %q, want %q", fetchResponse.Type, protocol.TypeAppResponse)
	}

	fetched, err := protocol.DecodePayload[protocol.NotesFetchResponse](fetchResponse)
	if err != nil {
		t.Fatalf("DecodePayload fetch: %v", err)
	}
	if fetched.Content != "offline-safe note" {
		t.Fatalf("note content = %q, want %q", fetched.Content, "offline-safe note")
	}
}

func TestProfileNoteHTTPWriteBroadcastsRealtimeEvent(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	// Database creation/migrations have their own setup budget. Measure the
	// realtime behavior only after that fixture exists.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_live", Email: "live@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_live", UserID: user.ID, TokenHash: hashToken("live-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	appConn, _, err := appWebSocketDial(ctx, testServer, "live-token")
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	subscribe, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_subscribe_profile_notes", "", "", protocol.RoutedCommand{
		Command: "app.subscribe",
		Body:    mustEncodeBody(t, protocol.AppSubscribeRequest{Topics: []string{"notes.profile"}}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope subscribe: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, subscribe); err != nil {
		t.Fatalf("subscribe write: %v", err)
	}
	subscribeResponse := readUntilRequestID(t, ctx, appConn, "req_subscribe_profile_notes")
	if subscribeResponse.Type != protocol.TypeAppResponse {
		t.Fatalf("subscribe response type = %q, want %q", subscribeResponse.Type, protocol.TypeAppResponse)
	}

	requestJSON(t, testServer, "live-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":   "live-note",
		"title":     "Live Note",
		"content":   "from app",
		"page_type": "text",
	}, nil)

	eventEnvelope := readUntilEvent(t, ctx, appConn, "notes.changed")
	event, err := protocol.DecodePayload[protocol.AppEvent](eventEnvelope)
	if err != nil {
		t.Fatalf("DecodePayload event: %v", err)
	}
	if event.Topic != "notes.profile" {
		t.Fatalf("event topic = %q, want notes.profile", event.Topic)
	}
	var body struct {
		UserID string `json:"user_id"`
		NoteID string `json:"note_id"`
	}
	if err := json.Unmarshal(event.Body, &body); err != nil {
		t.Fatalf("decode event body: %v", err)
	}
	if body.UserID != user.ID || body.NoteID != "live-note" {
		t.Fatalf("event body = %+v, want user_id=%q note_id=live-note", body, user.ID)
	}
}

func TestProfileScopedCollaborationDoesNotRequireHome(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	// Database creation/migrations are fixture setup, not the behavior deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_desktop", Email: "desktop@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_desktop", UserID: user.ID, TokenHash: hashToken("desktop-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "desktop-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id":     "desktop-note",
		"title":       "Desktop Note",
		"content":     "hello",
		"page_type":   "text",
		"body_format": "markdown",
	}, nil)

	appConn, _, err := appWebSocketDial(ctx, testServer, "desktop-token")
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	join, err := protocol.NewEnvelope(protocol.TypeAppCommand, "join_profile", "", "", protocol.RoutedCommand{
		Command: "notes.collab.join",
		Body: mustEncodeBody(t, protocol.NoteCollaborationJoinRequest{
			NoteID:    "desktop-note",
			SessionID: "desktop-live",
			Scope:     "profile",
		}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope join: %v", err)
	}
	must(t, wsjson.Write(ctx, appConn, join))
	joinResponse := readUntilRequestID(t, ctx, appConn, "join_profile")
	if joinResponse.Type != protocol.TypeAppResponse {
		t.Fatalf("join response type = %q, want %q", joinResponse.Type, protocol.TypeAppResponse)
	}
	snapshot, err := protocol.DecodePayload[protocol.NoteCollaborationSnapshot](joinResponse)
	if err != nil {
		t.Fatalf("DecodePayload join: %v", err)
	}
	if snapshot.Note.Content != "hello" {
		t.Fatalf("snapshot content = %q, want hello", snapshot.Note.Content)
	}

	submit, err := protocol.NewEnvelope(protocol.TypeAppCommand, "submit_profile_ops", "", "", protocol.RoutedCommand{
		Command: "notes.collab.submit_ops",
		Body: mustEncodeBody(t, protocol.NoteCollaborationSubmitOpsRequest{
			NoteID:      "desktop-note",
			SessionID:   "desktop-live",
			Scope:       "profile",
			BaseVersion: snapshot.AppliedVersion,
			Ops: []protocol.NoteCollaborationOperation{{
				OpID:  "desktop-op-1",
				Type:  "text_insert",
				Index: 5,
				Text:  " desktop",
			}},
		}),
	})
	if err != nil {
		t.Fatalf("NewEnvelope submit: %v", err)
	}
	must(t, wsjson.Write(ctx, appConn, submit))
	submitResponse := readUntilRequestID(t, ctx, appConn, "submit_profile_ops")
	if submitResponse.Type != protocol.TypeAppResponse {
		t.Fatalf("submit response type = %q, want %q", submitResponse.Type, protocol.TypeAppResponse)
	}

	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, "desktop-token", http.MethodGet, "/v1/me/notes/desktop-note", nil, &fetched)
	if fetched.Content != "hello desktop" {
		t.Fatalf("fetched content = %q, want hello desktop", fetched.Content)
	}
}

func requestJSONStatus(t *testing.T, server *httptest.Server, sessionToken string, method string, path string, body any, wantStatus int) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("request %s %s status = %d, want %d body=%s", method, path, response.StatusCode, wantStatus, string(data))
	}
	return response
}

func mustEncodeBody(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	body, err := protocol.EncodeBody(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func readUntilRequestID(t *testing.T, ctx context.Context, conn *websocket.Conn, requestID string) protocol.Envelope {
	t.Helper()
	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.RequestID == requestID {
			return envelope
		}
	}
}

func readUntilEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, wantEvent string) protocol.Envelope {
	t.Helper()
	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != protocol.TypeAppEvent {
			continue
		}
		event, err := protocol.DecodePayload[protocol.AppEvent](envelope)
		if err != nil {
			t.Fatal(err)
		}
		if event.Event == wantEvent {
			return envelope
		}
	}
}
