package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/migrations"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestValidateAgentTokenAndRevokeForHome(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}

	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Agent", Status: domain.AgentStatusOffline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	if err := db.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	token := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: "token-hash", CreatedAt: now}
	if err := db.CreateAgentToken(ctx, token); err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}

	record, err := db.ValidateAgentToken(ctx, token.TokenHash)
	if err != nil {
		t.Fatalf("ValidateAgentToken: %v", err)
	}
	if record.Agent.ID != agent.ID || record.Home.ID != home.ID {
		t.Fatalf("unexpected token record: %#v", record)
	}
	if record.Agent.AgentType != "worker" {
		t.Fatalf("validated agent type = %q, want worker", record.Agent.AgentType)
	}

	if err := db.RevokeAgentTokenForHome(ctx, home.ID, token.ID); err != nil {
		t.Fatalf("RevokeAgentTokenForHome: %v", err)
	}

	if _, err := db.ValidateAgentToken(ctx, token.TokenHash); err != ErrNotFound {
		t.Fatalf("ValidateAgentToken after revoke = %v, want ErrNotFound", err)
	}
}

func TestMCPNoteAttachmentUploadLifecycle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_mcp_upload", Email: "mcp-upload@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	note := domain.UserNote{ID: "note_mcp_upload", NoteID: "site.md", OwnerUserID: user.ID, Title: "Site", PageType: "text", Revision: "rev-one", CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	if err := db.UpsertUserNote(ctx, note); err != nil {
		t.Fatalf("UpsertUserNote: %v", err)
	}

	upload := domain.MCPNoteAttachmentUpload{
		ID: "mcpup_one", OwnerUserID: user.ID, NoteRecordID: note.ID, TargetNoteID: note.NoteID,
		TargetKind: "note", Filename: "site.html", ContentType: "text/html",
		ReceivedBytes: 0, StagingKey: ".mcp-staging/mcpup_one.part", ExpectedRevision: note.Revision,
		Status: "open", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := db.CreateMCPNoteAttachmentUpload(ctx, upload); err != nil {
		t.Fatalf("CreateMCPNoteAttachmentUpload: %v", err)
	}
	if err := db.AdvanceMCPNoteAttachmentUpload(ctx, upload.ID, user.ID, 0, 12, now.Add(time.Minute)); err != nil {
		t.Fatalf("AdvanceMCPNoteAttachmentUpload: %v", err)
	}
	got, err := db.GetMCPNoteAttachmentUpload(ctx, upload.ID, user.ID)
	if err != nil {
		t.Fatalf("GetMCPNoteAttachmentUpload: %v", err)
	}
	if got.ReceivedBytes != 12 || got.TargetNoteID != note.NoteID || got.Status != "open" {
		t.Fatalf("upload = %#v", got)
	}
	if _, err := db.GetMCPNoteAttachmentUpload(ctx, upload.ID, "usr_other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign GetMCPNoteAttachmentUpload error = %v, want ErrNotFound", err)
	}
	if err := db.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, user.ID, "failed", now.Add(2*time.Minute), nil); err != nil {
		t.Fatalf("UpdateMCPNoteAttachmentUploadStatus: %v", err)
	}
	got, err = db.GetMCPNoteAttachmentUpload(ctx, upload.ID, user.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("failed upload = %#v, err=%v", got, err)
	}
}

func TestUpsertAgentPreservesAssignedTypeWhenOmitted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_agent_type", Email: "agent-type@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_agent_type", UserID: user.ID, Name: "Agent Type Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}

	agent := domain.Agent{ID: "agent_worker", HomeID: home.ID, Name: "Worker", Status: domain.AgentStatusOffline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	if err := db.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent worker: %v", err)
	}
	agent.AgentType = ""
	agent.Name = "Worker Renamed"
	agent.UpdatedAt = now.Add(time.Minute)
	if err := db.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent without type: %v", err)
	}

	stored, err := db.GetAgentByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgentByID: %v", err)
	}
	if stored.AgentType != "worker" {
		t.Fatalf("agent type after omitted reissue = %q, want worker", stored.AgentType)
	}
}

func TestCreateHomeSeedsOwnerMembership(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	home := domain.Home{ID: "home_owner", UserID: user.ID, Name: "Owner Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}

	membership, err := db.GetHomeMembership(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("GetHomeMembership: %v", err)
	}
	if membership.Role != domain.HomeRoleAdmin {
		t.Fatalf("membership role = %q, want %q", membership.Role, domain.HomeRoleAdmin)
	}

	homes, err := db.ListHomesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListHomesByUser: %v", err)
	}
	if len(homes) != 1 || homes[0].ID != home.ID {
		t.Fatalf("homes = %#v, want %q", homes, home.ID)
	}
}

func TestUserDisplayNameRoundTripsThroughUsersAndHomeMembers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{
		ID:           "usr_display_name",
		Email:        "display@example.com",
		DisplayName:  "Original Name",
		PasswordHash: "hash",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	home := domain.Home{ID: "home_display_name", UserID: user.ID, Name: "Display Name Home", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))

	mustStore(t, db.UpdateUserDisplayName(ctx, user.ID, "Updated Name"))
	stored, err := db.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if stored.DisplayName != "Updated Name" || stored.Email != user.Email {
		t.Fatalf("stored user = %#v, want display name Updated Name and email %q", stored, user.Email)
	}

	members, err := db.ListHomeMembers(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListHomeMembers: %v", err)
	}
	if len(members) != 1 || members[0].DisplayName != "Updated Name" || members[0].Email != user.Email {
		t.Fatalf("members = %#v, want updated display name and unchanged email", members)
	}

	mustStore(t, db.UpdateUserDisplayName(ctx, user.ID, ""))
	stored, err = db.GetUserByEmail(ctx, user.Email)
	if err != nil {
		t.Fatalf("GetUserByEmail after clear: %v", err)
	}
	if stored.DisplayName != "" || stored.Email != user.Email {
		t.Fatalf("stored user after clear = %#v, want empty display name and email %q", stored, user.Email)
	}
}

func TestNotificationSettingsAndAPNSDevices(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_notify", Email: "notify@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_notify", UserID: user.ID, TokenHash: "hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	settings, err := db.SaveNotificationSettings(ctx, domain.NotificationSettings{
		UserID:                   user.ID,
		StorageEnabled:           false,
		NotesEnabled:             true,
		DashboardEntitiesEnabled: false,
	})
	if err != nil {
		t.Fatalf("SaveNotificationSettings: %v", err)
	}
	if settings.StorageEnabled || !settings.NotesEnabled || settings.DashboardEntitiesEnabled {
		t.Fatalf("settings = %#v", settings)
	}

	loaded, err := db.GetNotificationSettings(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	if loaded.UserID != settings.UserID ||
		loaded.StorageEnabled != settings.StorageEnabled ||
		loaded.NotesEnabled != settings.NotesEnabled ||
		loaded.DashboardEntitiesEnabled != settings.DashboardEntitiesEnabled ||
		!loaded.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(settings.UpdatedAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("loaded settings = %#v, want %#v", loaded, settings)
	}

	device, err := db.UpsertAPNSDevice(ctx, domain.APNSDevice{
		UserID:            user.ID,
		SessionID:         session.ID,
		DeviceID:          "device-1",
		Token:             "token-1",
		Environment:       "sandbox",
		BundleID:          "com.dropfile.Hank",
		EnabledCategories: json.RawMessage(`["notes"]`),
	})
	if err != nil {
		t.Fatalf("UpsertAPNSDevice: %v", err)
	}
	if device.DeviceID != "device-1" || string(device.EnabledCategories) != `["notes"]` {
		t.Fatalf("device = %#v", device)
	}

	devices, err := db.ListActiveAPNSDevicesForUsers(ctx, []string{user.ID})
	if err != nil {
		t.Fatalf("ListActiveAPNSDevicesForUsers: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceID != "device-1" {
		t.Fatalf("devices = %#v", devices)
	}

	if err := db.DeleteAPNSDevicesForSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteAPNSDevicesForSession: %v", err)
	}
	devices, err = db.ListActiveAPNSDevicesForUsers(ctx, []string{user.ID})
	if err != nil {
		t.Fatalf("ListActiveAPNSDevicesForUsers after delete: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("devices after delete = %#v", devices)
	}
}

func TestLoginBackoffPersistsAndClears(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	email := "backoff@example.com"
	for i := 0; i < 4; i++ {
		if err := db.RecordLoginFailure(ctx, email); err != nil {
			t.Fatalf("RecordLoginFailure %d: %v", i+1, err)
		}
		if retryAfter, blocked, err := db.LoginBackoffBlocked(ctx, email); err != nil {
			t.Fatalf("LoginBackoffBlocked %d: %v", i+1, err)
		} else if blocked {
			t.Fatalf("LoginBackoffBlocked %d blocked with retry_after=%v, want not blocked", i+1, retryAfter)
		}
	}
	if err := db.RecordLoginFailure(ctx, email); err != nil {
		t.Fatalf("RecordLoginFailure 5: %v", err)
	}
	if retryAfter, blocked, err := db.LoginBackoffBlocked(ctx, email); err != nil {
		t.Fatalf("LoginBackoffBlocked after threshold: %v", err)
	} else if !blocked || retryAfter <= 0 {
		t.Fatalf("LoginBackoffBlocked after threshold = blocked %v retry_after %v, want active backoff", blocked, retryAfter)
	}
	if err := db.RecordLoginSuccess(ctx, email); err != nil {
		t.Fatalf("RecordLoginSuccess: %v", err)
	}
	if retryAfter, blocked, err := db.LoginBackoffBlocked(ctx, email); err != nil {
		t.Fatalf("LoginBackoffBlocked after clear: %v", err)
	} else if blocked {
		t.Fatalf("LoginBackoffBlocked after clear blocked with retry_after=%v", retryAfter)
	}
}

func TestPruneLifecycleRemovesExpiredOperationalRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	old := now.Add(-45 * 24 * time.Hour)
	retention := 30 * 24 * time.Hour

	user := domain.User{ID: "usr_prune", Email: "prune@example.com", PasswordHash: "hash", CreatedAt: old, UpdatedAt: old}
	home := domain.Home{ID: "home_prune", UserID: user.ID, Name: "Prune Home", CreatedAt: old, UpdatedAt: old}
	session := domain.AppSession{ID: "sess_prune", UserID: user.ID, TokenHash: "session-prune", ExpiresAt: now.Add(time.Hour), CreatedAt: old}
	assistantSession := domain.AssistantSession{ID: "asess_prune", HomeID: home.ID, UserID: user.ID, Title: "Prune", LastMessageAt: old, CreatedAt: old, UpdatedAt: old}
	note := domain.UserNote{
		ID:            "note_prune",
		NoteID:        "prune.md",
		OwnerUserID:   user.ID,
		HomeID:        home.ID,
		Title:         "Prune",
		BodyMarkdown:  "body",
		BodyFormat:    "markdown",
		PageType:      "text",
		Revision:      "rev",
		Checksum:      "sum",
		CRDTStateJSON: "{}",
		CreatedAt:     old,
		UpdatedAt:     old,
		UpdatedBy:     user.ID,
	}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	mustStore(t, db.CreateSession(ctx, session))
	mustStore(t, db.CreateAssistantSession(ctx, assistantSession))
	mustStore(t, db.UpsertUserNote(ctx, note))
	mustStore(t, db.CreateFileTransfer(ctx, FileTransferRecord{
		ID:          "xfer_old_done",
		TokenHash:   "xfer-hash",
		HomeID:      home.ID,
		UserID:      user.ID,
		AgentID:     "agent_prune",
		Operation:   "download",
		SourceID:    "local",
		Path:        "/done.txt",
		Status:      "completed",
		CreatedAt:   old,
		ExpiresAt:   old,
		CompletedAt: &old,
	}))
	mustStore(t, db.CreateAuditEvent(ctx, AuditEvent{
		ID:           "audit_old",
		OccurredAt:   old,
		ActorUserID:  &user.ID,
		HomeID:       &home.ID,
		EventType:    "test.old",
		Severity:     "info",
		MetadataJSON: "{}",
	}))
	mustStore(t, db.CreateNoteAttachment(ctx, domain.NoteAttachment{
		ID:             "natt_old",
		NoteID:         note.ID,
		HomeID:         home.ID,
		OwnerUserID:    user.ID,
		Filename:       "old.txt",
		ContentType:    "text/plain",
		SizeBytes:      3,
		ChecksumSHA256: "sum",
		StorageKey:     "note_prune/natt_old.txt",
		DeletedAt:      &old,
		CreatedAt:      old,
		UpdatedAt:      old,
	}))
	mustStore(t, db.UpsertAssistantAttachments(ctx, []domain.AssistantAttachment{{
		ID:                 "att_old",
		SessionID:          assistantSession.ID,
		UserID:             user.ID,
		ClientAttachmentID: "client_old",
		Filename:           "old.pdf",
		ContentType:        "application/pdf",
		Kind:               "file",
		Status:             "expired",
		CreatedAt:          old,
		UpdatedAt:          old,
	}}))
	if _, err := db.exec(ctx, `INSERT INTO login_backoff (email_hash, failures, blocked_until, updated_at) VALUES (?, 6, ?, ?)`, stableHash("old@example.com"), old, old); err != nil {
		t.Fatalf("insert login_backoff: %v", err)
	}

	if err := db.PruneLifecycle(ctx, now, retention); err != nil {
		t.Fatalf("PruneLifecycle: %v", err)
	}
	assertNoRowsStore(t, db, ctx, "file transfer", `SELECT 1 FROM file_transfers WHERE id = ?`, "xfer_old_done")
	assertNoRowsStore(t, db, ctx, "audit event", `SELECT 1 FROM audit_events WHERE id = ?`, "audit_old")
	assertNoRowsStore(t, db, ctx, "login backoff", `SELECT 1 FROM login_backoff WHERE email_hash = ?`, stableHash("old@example.com"))
	assertNoRowsStore(t, db, ctx, "assistant attachment", `SELECT 1 FROM assistant_attachments WHERE id = ?`, "att_old")
	assertNoRowsStore(t, db, ctx, "note attachment row", `SELECT 1 FROM note_attachments WHERE id = ?`, "natt_old")
}

func TestListLiveNoteAttachmentInventory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	deletedAt := now.Add(time.Minute)
	user := domain.User{ID: "usr_attachment_inventory", Email: "inventory@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_inventory", UserID: user.ID, Name: "Inventory Home", CreatedAt: now, UpdatedAt: now}
	profileNote := domain.UserNote{ID: "note_inventory_profile", NoteID: "profile.md", OwnerUserID: user.ID, Title: "Profile files", BodyMarkdown: "profile", BodyFormat: "markdown", PageType: "text", Revision: "rev-profile", Checksum: "sum-profile", CRDTStateJSON: "{}", CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	homeNote := domain.UserNote{ID: "note_inventory_home", NoteID: "home.md", OwnerUserID: user.ID, HomeID: home.ID, Title: "Home files", BodyMarkdown: "home", BodyFormat: "markdown", PageType: "text", Revision: "rev-home", Checksum: "sum-home", CRDTStateJSON: "{}", CreatedAt: now, UpdatedAt: now, UpdatedBy: user.ID}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	mustStore(t, db.UpsertUserNote(ctx, profileNote))
	mustStore(t, db.UpsertUserNote(ctx, homeNote))
	for _, attachment := range []domain.NoteAttachment{
		{ID: "natt_profile", NoteID: profileNote.ID, OwnerUserID: user.ID, Filename: "profile.png", ContentType: "image/png", SizeBytes: 5, ChecksumSHA256: "sum-profile", StorageKey: "profile/natt_profile.png", CreatedAt: now, UpdatedAt: now},
		{ID: "natt_home", NoteID: homeNote.ID, HomeID: home.ID, OwnerUserID: user.ID, Filename: "home.pdf", ContentType: "application/pdf", SizeBytes: 7, ChecksumSHA256: "sum-home", StorageKey: "home/natt_home.pdf", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
		{ID: "natt_deleted", NoteID: homeNote.ID, HomeID: home.ID, OwnerUserID: user.ID, Filename: "deleted.txt", ContentType: "text/plain", SizeBytes: 9, ChecksumSHA256: "sum-deleted", StorageKey: "home/natt_deleted.txt", DeletedAt: &deletedAt, CreatedAt: now.Add(2 * time.Second), UpdatedAt: deletedAt},
	} {
		mustStore(t, db.CreateNoteAttachment(ctx, attachment))
	}

	items, err := db.ListLiveNoteAttachmentInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("inventory length = %d, want 2: %#v", len(items), items)
	}
	if items[0].Attachment.ID != "natt_home" || items[0].NoteTitle != "Home files" || items[0].NoteScope != "home" || items[0].OwnerEmail != user.Email {
		t.Fatalf("home inventory item = %#v", items[0])
	}
	if items[1].Attachment.ID != "natt_profile" || items[1].NoteScope != "profile" {
		t.Fatalf("profile inventory item = %#v", items[1])
	}
}

func TestListAuditEventsSortsBoundedResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_audit_sort", Email: "audit-sort@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_audit_sort", UserID: user.ID, Name: "Audit Sort Home", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))

	for _, event := range []AuditEvent{
		{ID: "audit_sort_b", OccurredAt: now.Add(-time.Minute), ActorUserID: &user.ID, HomeID: &home.ID, EventType: "download.failed", Severity: "warning", TargetType: "file_transfer", TargetID: "b", MetadataJSON: "{}"},
		{ID: "audit_sort_a", OccurredAt: now, ActorUserID: &user.ID, HomeID: &home.ID, EventType: "app_package.preview_failed", Severity: "warning", TargetType: "app_package", TargetID: "a", MetadataJSON: "{}"},
		{ID: "audit_sort_c", OccurredAt: now.Add(-2 * time.Minute), ActorUserID: &user.ID, HomeID: &home.ID, EventType: "upload.failed", Severity: "critical", TargetType: "file_transfer", TargetID: "c", MetadataJSON: "{}"},
	} {
		mustStore(t, db.CreateAuditEvent(ctx, event))
	}

	byType, err := db.ListAuditEvents(ctx, home.ID, "", "", "", 10, "event_type", "asc")
	if err != nil {
		t.Fatalf("ListAuditEvents by event_type: %v", err)
	}
	if got := []string{byType[0].EventType, byType[1].EventType, byType[2].EventType}; strings.Join(got, ",") != "app_package.preview_failed,download.failed,upload.failed" {
		t.Fatalf("event_type sort = %v", got)
	}

	fallback, err := db.ListAuditEvents(ctx, home.ID, "", "", "", 10, "metadata_json", "sideways")
	if err != nil {
		t.Fatalf("ListAuditEvents fallback: %v", err)
	}
	if fallback[0].ID != "audit_sort_a" || fallback[1].ID != "audit_sort_b" || fallback[2].ID != "audit_sort_c" {
		t.Fatalf("fallback sort IDs = %s, %s, %s; want newest first", fallback[0].ID, fallback[1].ID, fallback[2].ID)
	}
}

func mustStore(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoRowsStore(t *testing.T, db *Store, ctx context.Context, label string, query string, args ...any) {
	t.Helper()
	var value int
	err := db.queryRow(ctx, query, args...).Scan(&value)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("%s query error = %v, want sql.ErrNoRows", label, err)
	}
}

func TestSecretEncryptionProtectsStoredTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	if err := db.ConfigureSecretEncryption("test-secret-encryption-key"); err != nil {
		t.Fatalf("ConfigureSecretEncryption: %v", err)
	}

	now := time.Now().UTC()
	user := domain.User{ID: "usr_secret_store", Email: "secret-store@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_secret_store", UserID: user.ID, TokenHash: "session-hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:       user.ID,
		AccessToken:  "openai-access-token",
		RefreshToken: "openai-refresh-token",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("UpsertOpenAIAccount: %v", err)
	}
	if _, err := db.UpsertAPNSDevice(ctx, domain.APNSDevice{
		UserID:            user.ID,
		SessionID:         session.ID,
		DeviceID:          "device-secret",
		Token:             "apns-device-token",
		Environment:       "sandbox",
		BundleID:          "com.dropfile.Hank",
		EnabledCategories: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("UpsertAPNSDevice: %v", err)
	}
	if _, err := db.SaveUserProfileSecretVault(ctx, user.ID, nil, "local", json.RawMessage(`{"password":"vault-secret"}`)); err != nil {
		t.Fatalf("SaveUserProfileSecretVault: %v", err)
	}

	var storedAccess, storedRefresh, storedAPNS, storedVault string
	if err := db.queryRow(ctx, `SELECT access_token, refresh_token FROM openai_accounts WHERE user_id = ?`, user.ID).Scan(&storedAccess, &storedRefresh); err != nil {
		t.Fatalf("raw openai query: %v", err)
	}
	if err := db.queryRow(ctx, `SELECT token FROM apns_devices WHERE user_id = ? AND device_id = ?`, user.ID, "device-secret").Scan(&storedAPNS); err != nil {
		t.Fatalf("raw apns query: %v", err)
	}
	if err := db.queryRow(ctx, `SELECT vault_json::text FROM user_profile_secret_vaults WHERE user_id = ?`, user.ID).Scan(&storedVault); err != nil {
		t.Fatalf("raw vault query: %v", err)
	}
	for label, value := range map[string]string{
		"access":  storedAccess,
		"refresh": storedRefresh,
		"apns":    storedAPNS,
		"vault":   storedVault,
	} {
		if strings.Contains(value, "token") || strings.Contains(value, "vault-secret") {
			t.Fatalf("%s stored plaintext secret: %s", label, value)
		}
	}

	account, err := db.GetOpenAIAccount(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetOpenAIAccount: %v", err)
	}
	if account.AccessToken != "openai-access-token" || account.RefreshToken != "openai-refresh-token" {
		t.Fatalf("decrypted account = %#v", account)
	}
	vault, err := db.GetUserProfileSecretVault(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserProfileSecretVault: %v", err)
	}
	if string(vault.Vault) != `{"password":"vault-secret"}` {
		t.Fatalf("decrypted vault = %s", vault.Vault)
	}
}

func TestReencryptPlaintextSecrets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_secret_reencrypt", Email: "secret-reencrypt@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_secret_reencrypt", UserID: user.ID, TokenHash: "session-hash", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:       user.ID,
		AccessToken:  "plain-openai-access",
		RefreshToken: "plain-openai-refresh",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("UpsertOpenAIAccount: %v", err)
	}
	if _, err := db.UpsertAPNSDevice(ctx, domain.APNSDevice{
		UserID:            user.ID,
		SessionID:         session.ID,
		DeviceID:          "device-reencrypt",
		Token:             "plain-apns-token",
		Environment:       "sandbox",
		BundleID:          "com.dropfile.Hank",
		EnabledCategories: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("UpsertAPNSDevice: %v", err)
	}
	if _, err := db.SaveUserProfileSecretVault(ctx, user.ID, nil, "local", json.RawMessage(`{"password":"plain-vault-secret"}`)); err != nil {
		t.Fatalf("SaveUserProfileSecretVault: %v", err)
	}

	report, err := db.SecretStorageReport(ctx)
	if err != nil {
		t.Fatalf("SecretStorageReport: %v", err)
	}
	if report.PlaintextTotal() != 4 {
		t.Fatalf("plaintext total = %d, want 4: %#v", report.PlaintextTotal(), report)
	}
	if err := db.ConfigureSecretEncryption("test-secret-encryption-key"); err != nil {
		t.Fatalf("ConfigureSecretEncryption: %v", err)
	}
	report, err = db.ReencryptPlaintextSecrets(ctx)
	if err != nil {
		t.Fatalf("ReencryptPlaintextSecrets: %v", err)
	}
	if report.PlaintextTotal() != 0 || report.ReencryptedSecretColumns != 4 {
		t.Fatalf("reencrypt report = %#v, want zero plaintext and four updated columns", report)
	}

	var storedAccess, storedRefresh, storedAPNS, storedVault string
	if err := db.queryRow(ctx, `SELECT access_token, refresh_token FROM openai_accounts WHERE user_id = ?`, user.ID).Scan(&storedAccess, &storedRefresh); err != nil {
		t.Fatalf("raw openai query: %v", err)
	}
	if err := db.queryRow(ctx, `SELECT token FROM apns_devices WHERE user_id = ? AND device_id = ?`, user.ID, "device-reencrypt").Scan(&storedAPNS); err != nil {
		t.Fatalf("raw apns query: %v", err)
	}
	if err := db.queryRow(ctx, `SELECT vault_json::text FROM user_profile_secret_vaults WHERE user_id = ?`, user.ID).Scan(&storedVault); err != nil {
		t.Fatalf("raw vault query: %v", err)
	}
	for label, value := range map[string]string{"access": storedAccess, "refresh": storedRefresh, "apns": storedAPNS, "vault": storedVault} {
		if !strings.Contains(value, encryptedSecretPrefix) {
			t.Fatalf("%s stored value = %q, want encrypted prefix", label, value)
		}
		if strings.Contains(value, "plain-") {
			t.Fatalf("%s stored plaintext secret after reencrypt: %s", label, value)
		}
	}
}

func TestNotificationRecipientQueries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner_notify", Email: "owner-notify@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	admin := domain.User{ID: "usr_admin_notify", Email: "admin-notify@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member_notify", Email: "member-notify@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_notify", UserID: owner.ID, Name: "Notify Home", CreatedAt: now, UpdatedAt: now}
	for _, user := range []domain.User{owner, admin, member} {
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatalf("CreateUser %s: %v", user.ID, err)
		}
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	if err := db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("AddHomeMembership admin: %v", err)
	}
	if err := db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("AddHomeMembership member: %v", err)
	}

	storageRecipients, err := db.ListStorageNotificationUserIDs(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListStorageNotificationUserIDs: %v", err)
	}
	assertSameStrings(t, storageRecipients, []string{owner.ID, admin.ID})

	note := domain.UserNote{
		ID:            "note_notify_internal",
		NoteID:        "note-notify.md",
		OwnerUserID:   owner.ID,
		HomeID:        home.ID,
		Title:         "Notify",
		Content:       "body",
		BodyMarkdown:  "body",
		BodyFormat:    "markdown",
		PageType:      "text",
		Revision:      "rev-1",
		Checksum:      "sum-1",
		CRDTStateJSON: "{}",
		CollabVersion: 1,
		CreatedAt:     now,
		UpdatedAt:     now,
		UpdatedBy:     owner.ID,
	}
	if err := db.SaveUserNoteWithOperations(ctx, note, nil); err != nil {
		t.Fatalf("SaveUserNoteWithOperations: %v", err)
	}
	if err := db.AddNoteShare(ctx, domain.NoteShare{NoteID: note.ID, HomeID: home.ID, TargetUserID: member.ID, SharedBy: owner.ID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("AddNoteShare member: %v", err)
	}
	noteRecipients, err := db.ListNoteNotificationUserIDs(ctx, note.ID, owner.ID)
	if err != nil {
		t.Fatalf("ListNoteNotificationUserIDs: %v", err)
	}
	assertSameStrings(t, noteRecipients, []string{member.ID})

	if _, err := db.SaveUserProfileSettings(ctx, member.ID, nil, json.RawMessage(`{"dashboard_tiles":[{"entity_id":"light.kitchen","is_enabled":true}]}`)); err != nil {
		t.Fatalf("SaveUserProfileSettings member: %v", err)
	}
	if _, err := db.SaveUserProfileSettings(ctx, owner.ID, nil, json.RawMessage(`{"dashboard_tiles":[{"entity_id":"light.kitchen","is_enabled":false}]}`)); err != nil {
		t.Fatalf("SaveUserProfileSettings owner: %v", err)
	}
	dashboardRecipients, err := db.ListDashboardEntityNotificationUserIDs(ctx, home.ID, "light.kitchen")
	if err != nil {
		t.Fatalf("ListDashboardEntityNotificationUserIDs: %v", err)
	}
	assertSameStrings(t, dashboardRecipients, []string{member.ID})
}

func TestBootstrapSingletonHomeCreatesAdminAndPermissions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	home, created, err := db.BootstrapSingletonHome(ctx, user, "Home")
	if err != nil {
		t.Fatalf("BootstrapSingletonHome: %v", err)
	}
	if !created {
		t.Fatal("expected bootstrap to create the singleton home")
	}
	if home.Name != "Home" {
		t.Fatalf("home name = %q, want %q", home.Name, "Home")
	}

	membership, err := db.GetHomeMembership(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("GetHomeMembership: %v", err)
	}
	if membership.Role != domain.HomeRoleAdmin {
		t.Fatalf("membership role = %q, want %q", membership.Role, domain.HomeRoleAdmin)
	}

	permissions, err := db.GetHomePermissions(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetHomePermissions: %v", err)
	}
	if !permissions.HomeAssistantEnabled || !permissions.FilesEnabled || !permissions.NotesEnabled {
		t.Fatalf("expected all default permissions enabled, got %#v", permissions)
	}
}

func TestHomeQuickLinksPersistAndReorder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_quick_links", Email: "quick-links@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_links", UserID: user.ID, Name: "Quick Links Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}

	var tableName string
	if err := db.queryRow(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'home_quick_links'`).Scan(&tableName); err != nil {
		t.Fatalf("home_quick_links table missing: %v", err)
	}

	linkOne := domain.HomeQuickLink{
		ID:                 "ql_one",
		HomeID:             home.ID,
		Title:              "Docker",
		URL:                "http://docker.local:9000",
		Description:        "Containers",
		SortOrder:          10,
		HealthCheckEnabled: true,
		Status:             domain.QuickLinkStatusUnchecked,
		CreatedAt:          now,
		UpdatedAt:          now,
		UpdatedBy:          user.ID,
	}
	linkTwo := domain.HomeQuickLink{
		ID:                 "ql_two",
		HomeID:             home.ID,
		Title:              "GitHub",
		URL:                "https://github.com",
		SortOrder:          20,
		HealthCheckEnabled: false,
		Status:             domain.QuickLinkStatusDisabled,
		CreatedAt:          now,
		UpdatedAt:          now,
		UpdatedBy:          user.ID,
	}
	if err := db.CreateHomeQuickLink(ctx, linkOne); err != nil {
		t.Fatalf("CreateHomeQuickLink one: %v", err)
	}
	if err := db.CreateHomeQuickLink(ctx, linkTwo); err != nil {
		t.Fatalf("CreateHomeQuickLink two: %v", err)
	}

	if count, err := db.CountHomeQuickLinks(ctx, home.ID); err != nil {
		t.Fatalf("CountHomeQuickLinks: %v", err)
	} else if count != 2 {
		t.Fatalf("quick link count = %d, want 2", count)
	}

	links, err := db.ListHomeQuickLinks(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListHomeQuickLinks: %v", err)
	}
	if len(links) != 2 || links[0].ID != linkOne.ID || links[1].ID != linkTwo.ID {
		t.Fatalf("initial order = %#v", links)
	}

	checkedAt := now.Add(time.Minute)
	updated, err := db.UpdateHomeQuickLinkStatus(ctx, home.ID, linkOne.ID, domain.QuickLinkStatusUp, http.StatusNoContent, &checkedAt, "")
	if err != nil {
		t.Fatalf("UpdateHomeQuickLinkStatus: %v", err)
	}
	if updated.Status != domain.QuickLinkStatusUp || updated.StatusCode != http.StatusNoContent || updated.LastCheckedAt == nil {
		t.Fatalf("updated status = %#v", updated)
	}

	linkTwo.Title = "GitHub Main"
	linkTwo.HealthCheckEnabled = true
	linkTwo.Status = domain.QuickLinkStatusUnchecked
	linkTwo.UpdatedAt = now.Add(2 * time.Minute)
	linkTwo.UpdatedBy = user.ID
	updated, err = db.UpdateHomeQuickLink(ctx, linkTwo)
	if err != nil {
		t.Fatalf("UpdateHomeQuickLink: %v", err)
	}
	if updated.Title != "GitHub Main" || !updated.HealthCheckEnabled {
		t.Fatalf("updated link = %#v", updated)
	}

	if err := db.ReorderHomeQuickLinks(ctx, home.ID, []string{linkTwo.ID, linkOne.ID}); err != nil {
		t.Fatalf("ReorderHomeQuickLinks: %v", err)
	}
	links, err = db.ListHomeQuickLinks(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListHomeQuickLinks after reorder: %v", err)
	}
	if links[0].ID != linkTwo.ID || links[1].ID != linkOne.ID {
		t.Fatalf("reordered links = %#v", links)
	}

	if err := db.DeleteHomeQuickLink(ctx, home.ID, linkTwo.ID); err != nil {
		t.Fatalf("DeleteHomeQuickLink: %v", err)
	}
	if count, err := db.CountHomeQuickLinks(ctx, home.ID); err != nil {
		t.Fatalf("CountHomeQuickLinks after delete: %v", err)
	} else if count != 1 {
		t.Fatalf("quick link count after delete = %d, want 1", count)
	}
}

func TestOpenFailsWhenMultipleHomesExist(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	url := testutil.PostgreSQLTestURL(t)
	raw, err := sql.Open(driverName, url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	now := time.Now().UTC()
	statements := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE homes (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE home_memberships (home_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL, PRIMARY KEY(home_id, user_id))`,
		fmt.Sprintf(`INSERT INTO users (id, email, password_hash, created_at, updated_at) VALUES
			('usr_1', 'one@example.com', 'hash', '%s', '%s'),
			('usr_2', 'two@example.com', 'hash', '%s', '%s')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
		fmt.Sprintf(`INSERT INTO homes (id, user_id, name, created_at, updated_at) VALUES
			('home_1', 'usr_1', 'One', '%s', '%s'),
			('home_2', 'usr_2', 'Two', '%s', '%s')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
		fmt.Sprintf(`INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at) VALUES
			('home_1', 'usr_1', 'admin', '%s', '%s'),
			('home_2', 'usr_2', 'admin', '%s', '%s')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed statement failed: %v", err)
		}
	}

	if _, err := OpenMigrating(ctx, url); err == nil {
		t.Fatal("expected Open to fail when multiple homes exist")
	} else if !errors.Is(err, ErrUnsupportedMultiHome) {
		t.Fatalf("Open error = %v, want ErrUnsupportedMultiHome", err)
	}
}

func TestOpenReadOnlyDoesNotCreateSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	url := testutil.PostgreSQLTestURL(t)
	raw, err := sql.Open(driverName, url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	before := countPublicTables(t, ctx, raw)
	opened, err := Open(ctx, url)
	if err == nil {
		_ = opened.Close()
		t.Fatal("expected Open to fail when schema_migrations is missing")
	}
	after := countPublicTables(t, ctx, raw)
	if after != before {
		t.Fatalf("Open created schema objects: before=%d after=%d", before, after)
	}
}

func TestBaselineExistingSchemaThenReadOnlyStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	url := testutil.PostgreSQLTestURL(t)
	raw, err := sql.Open(driverName, url)
	if err != nil {
		t.Fatal(err)
	}
	db := &Store{db: raw, databaseURL: url}
	defer db.Close()
	baseline, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}
	// Build the actual legacy baseline, not an already fully migrated database.
	for _, statement := range baseline[0].Statements {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply baseline: %v", err)
		}
	}
	if err := migrations.BaselineExisting(ctx, db.DB(), 0); err != nil {
		t.Fatalf("BaselineExisting: %v", err)
	}
	statuses, err := migrations.AppliedReadOnly(ctx, db.DB())
	if err != nil {
		t.Fatalf("AppliedReadOnly after baseline: %v", err)
	}
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Version != all[0].Version || statuses[0].Checksum != all[0].Checksum {
		t.Fatalf("migration statuses after baseline = %#v, want first migration only", statuses)
	}
	if err := migrations.CheckReadOnly(ctx, db.DB()); !errors.Is(err, migrations.ErrPendingMigrations) {
		t.Fatalf("CheckReadOnly after baseline = %v, want ErrPendingMigrations", err)
	}
	if err := migrations.ApplyPending(ctx, db.DB()); err != nil {
		t.Fatalf("ApplyPending after baseline: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrating store: %v", err)
	}

	opened, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open after baseline: %v", err)
	}
	defer opened.Close()
	statuses, err = opened.MigrationStatuses(ctx)
	if err != nil {
		t.Fatalf("MigrationStatuses: %v", err)
	}
	if len(statuses) != len(all) {
		t.Fatalf("migration statuses = %#v, want %d applied migrations", statuses, len(all))
	}
}

func TestLegacyCleanupMigrationsArchiveAndCanonicalizeNotes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	url := testutil.PostgreSQLTestURL(t)
	raw, err := sql.Open(driverName, url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	all, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All: %v", err)
	}
	for _, statement := range all[0].Statements {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply baseline statement: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE user_notes DROP CONSTRAINT IF EXISTS user_notes_page_type_check`); err != nil {
		t.Fatalf("drop current page type constraint: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE user_notes ADD CONSTRAINT user_notes_page_type_check CHECK (page_type IN ('text', 'board'))`); err != nil {
		t.Fatalf("add legacy page type constraint: %v", err)
	}

	now := time.Now().UTC()
	if _, err := raw.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)`, "usr_legacy", "legacy@example.com", "hash", now); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO homes (id, user_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)`, "home_legacy", "usr_legacy", "Legacy Home", now); err != nil {
		t.Fatalf("insert legacy home: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO home_notes (home_id, note_id, title, content, page_type, board_json, revision, checksum, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		"home_legacy", "home-note.md", "Home Note", "Legacy home body", "text", "", "rev-home", "sum-home", now, "usr_legacy"); err != nil {
		t.Fatalf("insert legacy home note: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO user_notes (id, note_id, owner_user_id, home_id, title, content, body_markdown, page_type, board_json, revision, checksum, crdt_state_json, collab_version, created_at, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8, $9, $10, $11, $12, $13, $13, $14)`,
		"note_legacy", "legacy.md", "usr_legacy", "home_legacy", "Legacy Board", "Legacy profile body", "board", `{"columns":[]}`, "rev-profile", "sum-profile", "{}", 1, now, "usr_legacy"); err != nil {
		t.Fatalf("insert legacy user note: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO openai_oauth_states (state_hash, user_id, code_verifier, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, "state-hash", "usr_legacy", "verifier", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert legacy oauth state: %v", err)
	}

	if err := migrations.ApplyPending(ctx, raw); err != nil {
		t.Fatalf("ApplyPending: %v", err)
	}
	if tableExists(t, ctx, raw, "home_notes") {
		t.Fatal("home_notes still exists after cleanup migration")
	}
	if tableExists(t, ctx, raw, "openai_oauth_states") {
		t.Fatal("openai_oauth_states still exists after cleanup migration")
	}
	if !tableExists(t, ctx, raw, "legacy_home_notes_archive") {
		t.Fatal("legacy_home_notes_archive missing after cleanup migration")
	}
	if columnExists(t, ctx, raw, "user_notes", "content") {
		t.Fatal("user_notes.content still exists after cleanup migration")
	}

	var archivedBody string
	if err := raw.QueryRowContext(ctx, `SELECT content FROM legacy_home_notes_archive WHERE home_id = $1 AND note_id = $2`, "home_legacy", "home-note.md").Scan(&archivedBody); err != nil {
		t.Fatalf("query archived home note: %v", err)
	}
	if archivedBody != "Legacy home body" {
		t.Fatalf("archived home note body = %q", archivedBody)
	}

	var bodyMarkdown, pageType string
	if err := raw.QueryRowContext(ctx, `SELECT body_markdown, page_type FROM user_notes WHERE id = $1`, "note_legacy").Scan(&bodyMarkdown, &pageType); err != nil {
		t.Fatalf("query cleaned user note: %v", err)
	}
	if bodyMarkdown != "Legacy profile body" || pageType != "kanban" {
		t.Fatalf("cleaned user note body/page_type = %q/%q, want canonical body and kanban", bodyMarkdown, pageType)
	}
	if err := migrations.CheckReadOnly(ctx, raw); err != nil {
		t.Fatalf("CheckReadOnly after cleanup: %v", err)
	}
}

func TestListAndDeletePendingHomeInvitations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	invitation := domain.HomeInvitation{
		ID:        "invite_1",
		HomeID:    home.ID,
		Email:     "member@example.com",
		Role:      domain.HomeRoleMember,
		TokenHash: "hash",
		CreatedAt: now,
	}

	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	if err := db.CreateHomeInvitation(ctx, invitation); err != nil {
		t.Fatalf("CreateHomeInvitation: %v", err)
	}

	invitations, err := db.ListPendingHomeInvitations(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListPendingHomeInvitations: %v", err)
	}
	if len(invitations) != 1 || invitations[0].ID != invitation.ID {
		t.Fatalf("invitations = %#v, want invitation %q", invitations, invitation.ID)
	}

	if err := db.DeletePendingHomeInvitation(ctx, home.ID, invitation.ID); err != nil {
		t.Fatalf("DeletePendingHomeInvitation: %v", err)
	}

	invitations, err = db.ListPendingHomeInvitations(ctx, home.ID)
	if err != nil {
		t.Fatalf("ListPendingHomeInvitations after delete: %v", err)
	}
	if len(invitations) != 0 {
		t.Fatalf("pending invitations = %#v, want empty", invitations)
	}
}

func TestOpenFailsOnMigrationChecksumMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	url := testutil.PostgreSQLTestURL(t)
	db, err := OpenMigrating(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.exec(ctx, `UPDATE schema_migrations SET checksum = ? WHERE version = 1`, "bad-checksum-for-test"); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(ctx, url)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("expected Open to fail on migration checksum mismatch")
	}
	if !errors.Is(err, migrations.ErrChecksumMismatch) {
		t.Fatalf("Open error = %v, want ErrChecksumMismatch", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	db, err := OpenMigrating(context.Background(), testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func countPublicTables(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&count); err != nil {
		t.Fatalf("count public tables: %v", err)
	}
	return count
}

func tableExists(t *testing.T, ctx context.Context, db *sql.DB, tableName string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1
	)`, tableName).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", tableName, err)
	}
	return exists
}

func columnExists(t *testing.T, ctx context.Context, db *sql.DB, tableName, columnName string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
	)`, tableName, columnName).Scan(&exists); err != nil {
		t.Fatalf("check column %s.%s: %v", tableName, columnName, err)
	}
	return exists
}

func assertSameStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
}
