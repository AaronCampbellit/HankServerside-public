package cloud

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestAppNotesSyncUsesCloudStoreWithoutChangingBackups(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_admin", UserID: admin.ID, TokenHash: hashToken("session-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

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
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	response, err := server.notes.SaveHome(ctx, home.ID, admin.ID, "shared.md", protocol.NotesSaveRequest{
		NoteID:   "shared.md",
		Title:    "Shared",
		Content:  "cloud-backed note",
		PageType: protocol.NotePageTypeText,
	})
	if err != nil {
		t.Fatalf("SaveHome: %v", err)
	}

	note, err := db.GetOwnedHomeNote(ctx, home.ID, admin.ID, response.NoteID)
	if err != nil {
		t.Fatalf("GetOwnedHomeNote: %v", err)
	}
	shareTime := now.Add(30 * time.Second)
	must(t, db.AddNoteShare(ctx, domain.NoteShare{
		NoteID:       note.ID,
		HomeID:       home.ID,
		TargetUserID: member.ID,
		SharedBy:     admin.ID,
		CreatedAt:    shareTime,
		UpdatedAt:    shareTime,
	}))

	profileBackupAt := now.Add(45 * time.Second)
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{
		HomeID:           home.ID,
		ServiceType:      domain.ServiceTypeSMB,
		PublicConfigJSON: `{"host":"nas.local","share":"docs"}`,
		SecretVersion:    1,
		AppliedVersion:   1,
		Status:           domain.SyncStatusHealthy,
		UpdatedAt:        profileBackupAt,
		UpdatedBy:        admin.ID,
		LastBackupAt:     &profileBackupAt,
	}))

	noteBackupBefore, err := db.GetLatestHomeNoteUpdate(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetLatestHomeNoteUpdate before sync: %v", err)
	}
	if noteBackupBefore == nil {
		t.Fatal("expected note backup timestamp before app sync")
	}

	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	appConn, _, err := appWebSocketDial(ctx, testServer, "session-token")
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	command, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_notes_sync", "", "", protocol.RoutedCommand{
		Command: "notes.sync",
	})
	if err != nil {
		t.Fatalf("NewEnvelope notes.sync: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("app notes.sync write: %v", err)
	}

	syncEnvelope := readUntilRequestID(t, ctx, appConn, "req_notes_sync")
	if syncEnvelope.Type != protocol.TypeAppResponse {
		t.Fatalf("notes.sync response type = %q, want %q", syncEnvelope.Type, protocol.TypeAppResponse)
	}

	syncPayload, err := protocol.DecodePayload[protocol.NotesSyncResponse](syncEnvelope)
	if err != nil {
		t.Fatalf("DecodePayload notes.sync: %v", err)
	}
	if len(syncPayload.Notes) != 1 || syncPayload.Notes[0].ID != "shared.md" {
		t.Fatalf("notes.sync payload = %#v, want shared.md from cloud store", syncPayload.Notes)
	}

	noteBackupAfter, err := db.GetLatestHomeNoteUpdate(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetLatestHomeNoteUpdate after sync: %v", err)
	}
	if noteBackupAfter == nil || !noteBackupAfter.Equal(*noteBackupBefore) {
		t.Fatalf("note backup timestamp changed across app notes.sync: before=%v after=%v", noteBackupBefore, noteBackupAfter)
	}

	profile, err := db.GetHomeServiceProfile(ctx, home.ID, domain.ServiceTypeSMB)
	if err != nil {
		t.Fatalf("GetHomeServiceProfile after sync: %v", err)
	}
	if profile.LastBackupAt == nil || !samePostgresTime(*profile.LastBackupAt, profileBackupAt) {
		t.Fatalf("profile backup timestamp changed across app notes.sync: want %v got %v", profileBackupAt, profile.LastBackupAt)
	}
}

func TestReconcileHomeNotesPullsAgentNoteIntoCloudBackup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	agentToken := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken("agent-token"), CreatedAt: now}

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
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, agentToken))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	agentConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer agent-token"},
			"X-Hank-Agent-ID": []string{"agent_1"},
		},
	})
	if err != nil {
		t.Fatalf("agent websocket dial: %v", err)
	}
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, HomeName: home.Name})
	if err != nil {
		t.Fatalf("NewEnvelope register: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, register); err != nil {
		t.Fatalf("agent register write: %v", err)
	}
	readUntilAgentRegistered(t, ctx, agentConn)

	localUpdatedAt := now.Add(2 * time.Minute).Round(0)
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeCloudCommand {
				continue
			}

			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil {
				return
			}

			switch command.Command {
			case "notes.sync":
				reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agent.ID, home.ID, protocol.NotesSyncResponse{
					Notes: []protocol.NoteSummary{{
						ID:        "shared.md",
						Title:     "Shared",
						UpdatedAt: localUpdatedAt,
						Revision:  "rev-agent",
						Size:      int64(len("agent version")),
						PageType:  protocol.NotePageTypeText,
					}},
				})
				_ = wsjson.Write(ctx, agentConn, reply)
			case "notes.fetch":
				reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agent.ID, home.ID, protocol.NotesFetchResponse{
					NoteID:    "shared.md",
					Title:     "Shared",
					Content:   "agent version",
					Revision:  "rev-agent",
					UpdatedAt: localUpdatedAt,
					PageType:  protocol.NotePageTypeText,
				})
				_ = wsjson.Write(ctx, agentConn, reply)
			default:
				return
			}
		}
	}()

	server.reconcileHomeNotes(ctx, home, agent.ID)

	note, err := db.GetHomeNoteVisibleToUser(ctx, home.ID, admin.ID, "shared.md")
	if err != nil {
		t.Fatalf("GetHomeNoteVisibleToUser: %v", err)
	}
	if note.Content != "agent version" {
		t.Fatalf("cloud note content = %q, want %q", note.Content, "agent version")
	}
	if !samePostgresTime(note.UpdatedAt, localUpdatedAt) {
		t.Fatalf("cloud note updated_at = %v, want %v", note.UpdatedAt, localUpdatedAt)
	}

	latestBackupAt, err := db.GetLatestHomeNoteUpdate(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetLatestHomeNoteUpdate: %v", err)
	}
	if latestBackupAt == nil || !samePostgresTime(*latestBackupAt, localUpdatedAt) {
		t.Fatalf("latest backup timestamp = %v, want %v", latestBackupAt, localUpdatedAt)
	}

	state, err := db.GetHomeNoteSyncState(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetHomeNoteSyncState: %v", err)
	}
	if state.LastPullAt == nil {
		t.Fatal("expected note pull timestamp after reconciliation")
	}
	if state.LastSuccessfulSyncAt == nil {
		t.Fatal("expected successful sync timestamp after reconciliation")
	}
	if state.Status != domain.SyncStatusHealthy {
		t.Fatalf("sync status = %q, want %q", state.Status, domain.SyncStatusHealthy)
	}
}

func samePostgresTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func TestServiceProfileApplySetsBackupTimestamp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	appliedAt := time.Now().UTC().Round(0)
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeCloudCommand {
				continue
			}

			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil || command.Command != "config.apply" {
				return
			}
			var apply protocol.ConfigApplyRequest
			if err := json.Unmarshal(command.Body, &apply); err != nil {
				t.Errorf("config.apply body decode: %v", err)
				return
			}
			var public map[string]string
			if err := json.Unmarshal(apply.PublicConfig, &public); err != nil {
				t.Errorf("public config decode: %v", err)
				return
			}
			if public["username"] != "aaron" {
				t.Errorf("public username = %q, want %q", public["username"], "aaron")
				return
			}
			var secrets map[string]string
			if err := json.Unmarshal(apply.Secrets, &secrets); err != nil {
				t.Errorf("secrets decode: %v", err)
				return
			}
			if _, ok := secrets["username"]; ok {
				t.Errorf("secrets unexpectedly included username")
				return
			}
			if secrets["password"] != "secret" {
				t.Errorf("secret password = %q, want %q", secrets["password"], "secret")
				return
			}

			reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.ConfigApplyResponse{
				Profile: protocol.ServiceProfileSnapshot{
					ServiceType:    domain.ServiceTypeSMB,
					PublicConfig:   mustEncodeBody(t, map[string]any{"host": "nas.local", "share": "docs", "username": "aaron"}),
					SecretVersion:  1,
					AppliedVersion: 1,
					Status:         domain.SyncStatusHealthy,
					UpdatedAt:      appliedAt,
				},
			})
			_ = wsjson.Write(ctx, agentConn, reply)
		}
	}()

	var profile domain.HomeServiceProfile
	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/home/service-profiles/smb", map[string]any{
		"public_config": map[string]any{"host": "nas.local", "share": "docs", "username": "aaron"},
		"secrets":       map[string]any{"password": "secret"},
		"persist":       true,
	}, &profile)

	if profile.LastBackupAt == nil {
		t.Fatal("expected profile backup timestamp after config.apply")
	}
	if profile.SecretVersion != 1 {
		t.Fatalf("secret_version = %d, want 1", profile.SecretVersion)
	}
	if profile.AppliedVersion != 1 {
		t.Fatalf("applied_version = %d, want 1", profile.AppliedVersion)
	}
	if profile.Status != domain.SyncStatusHealthy {
		t.Fatalf("profile status = %q, want %q", profile.Status, domain.SyncStatusHealthy)
	}
}

func TestSMBServiceProfileTestRoutesWithoutPersisting(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	go func() {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil || command.Command != "config.smb_test" {
			t.Errorf("command = %#v, err = %v", command, err)
			return
		}
		var request protocol.ConfigSMBTestRequest
		if err := json.Unmarshal(command.Body, &request); err != nil {
			t.Errorf("decode SMB test: %v", err)
			return
		}
		if request.ID != "archive" || request.Password != "draft-secret" {
			t.Errorf("SMB test request = %#v", request)
			return
		}
		reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.ConfigSMBTestResponse{OK: true})
		_ = wsjson.Write(ctx, agentConn, reply)
	}()

	var response protocol.ConfigSMBTestResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/service-profiles/smb/test", map[string]any{
		"id": "archive", "name": "Archive", "host": "backup.local", "share": "archive", "username": "backup", "password": "draft-secret",
	}, &response)
	if !response.OK {
		t.Fatalf("response = %#v, want ok", response)
	}
}

func TestHermesServiceProfileApplyRoutesToAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	appliedAt := time.Now().UTC().Round(0)
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeCloudCommand {
				continue
			}

			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil || command.Command != "config.apply" {
				return
			}
			var apply protocol.ConfigApplyRequest
			if err := json.Unmarshal(command.Body, &apply); err != nil {
				t.Errorf("config.apply body decode: %v", err)
				return
			}
			if apply.ServiceType != domain.ServiceTypeHermes {
				t.Errorf("service_type = %q, want %q", apply.ServiceType, domain.ServiceTypeHermes)
				return
			}
			var public struct {
				APIBaseURL     string `json:"api_base_url"`
				Model          string `json:"model"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(apply.PublicConfig, &public); err != nil {
				t.Errorf("public config decode: %v", err)
				return
			}
			if public.APIBaseURL != "http://hermes-vm:8642" || public.Model != "hermes-agent" || public.TimeoutSeconds != 120 {
				t.Errorf("public config = %#v", public)
				return
			}
			var secrets map[string]string
			if err := json.Unmarshal(apply.Secrets, &secrets); err != nil {
				t.Errorf("secrets decode: %v", err)
				return
			}
			if secrets["api_key"] != "hermes-secret" {
				t.Errorf("api key secret = %q", secrets["api_key"])
				return
			}

			reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.ConfigApplyResponse{
				Profile: protocol.ServiceProfileSnapshot{
					ServiceType:    domain.ServiceTypeHermes,
					PublicConfig:   mustEncodeBody(t, map[string]any{"api_base_url": "http://hermes-vm:8642", "model": "hermes-agent", "timeout_seconds": 120, "api_key_set": true}),
					SecretVersion:  1,
					AppliedVersion: 1,
					Status:         domain.SyncStatusHealthy,
					UpdatedAt:      appliedAt,
				},
			})
			_ = wsjson.Write(ctx, agentConn, reply)
		}
	}()

	var profile domain.HomeServiceProfile
	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/home/service-profiles/hermes", map[string]any{
		"public_config": map[string]any{"api_base_url": "http://hermes-vm:8642", "model": "hermes-agent", "timeout_seconds": 120},
		"secrets":       map[string]any{"api_key": "hermes-secret"},
		"persist":       true,
	}, &profile)

	if profile.ServiceType != domain.ServiceTypeHermes {
		t.Fatalf("service_type = %q, want %q", profile.ServiceType, domain.ServiceTypeHermes)
	}
	if profile.SecretVersion != 1 || profile.AppliedVersion != 1 {
		t.Fatalf("versions = secret %d applied %d, want 1/1", profile.SecretVersion, profile.AppliedVersion)
	}
	if profile.Status != domain.SyncStatusHealthy {
		t.Fatalf("profile status = %q, want %q", profile.Status, domain.SyncStatusHealthy)
	}
}

func readUntilAgentRegistered(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type == protocol.TypeAgentRegistered {
			return
		}
	}
}
