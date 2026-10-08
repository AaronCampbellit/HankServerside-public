package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestProductionDesktopRouteInventoryIncludesMilestoneTwoDataPlane(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{`mux.HandleFunc("/v1/agents/"`, `mux.HandleFunc("/v1/desktop-sessions/"`, `mux.HandleFunc("/ws/desktop/browser/"`, `mux.HandleFunc("/ws/desktop/agent/"`} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("production route inventory missing %s", route)
		}
	}
}

func TestManagedFileMoveCreatesAndCompletesJob(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

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
			if err != nil || command.Command != "files.move" {
				return
			}
			request, err := decodeBody[protocol.FilesMoveRequest](command.Body)
			if err != nil || request.JobID == "" {
				return
			}
			response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.FileOperationJobResponse{
				OK:     true,
				JobID:  request.JobID,
				Status: "running",
			})
			_ = wsjson.Write(ctx, agentConn, response)
			body, _ := protocol.EncodeBody(protocol.FileOperationJobEvent{
				JobID:      request.JobID,
				Status:     "completed",
				FilesTotal: 1,
				FilesDone:  1,
			})
			event, _ := protocol.NewEnvelope(protocol.TypeAgentEvent, "", agentID, homeID, protocol.AgentEvent{Event: "files.move_completed", Topic: "files.jobs", Body: body})
			_ = wsjson.Write(ctx, agentConn, event)
		}
	}()

	appConn, _, err := appWebSocketDial(ctx, testServer, sessionToken)
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	body, _ := protocol.EncodeBody(protocol.FilesMoveRequest{SourceID: "primary", DestinationSourceID: "secondary", From: "/in.txt", To: "/out.txt"})
	command, _ := protocol.NewEnvelope(protocol.TypeAppCommand, "req_move_job", "", "", protocol.RoutedCommand{Command: "files.move", Body: body})
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("write move command: %v", err)
	}
	var response protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &response); err != nil {
		t.Fatalf("read move response: %v", err)
	}
	if response.Type != protocol.TypeAppResponse {
		t.Fatalf("response type = %q, want app.response: %#v", response.Type, response.Error)
	}
	payload, err := protocol.DecodePayload[protocol.FileOperationJobResponse](response)
	if err != nil {
		t.Fatalf("decode move response: %v", err)
	}
	if payload.JobID == "" {
		t.Fatal("move response missing job_id")
	}

	var job map[string]any
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/file-jobs/"+payload.JobID, nil, &job)
	deadline := time.Now().Add(2 * time.Second)
	for job["status"] != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/file-jobs/"+payload.JobID, nil, &job)
	}
	if job["status"] != "completed" {
		t.Fatalf("job status = %#v, want completed", job["status"])
	}
}

func TestManagedFileMoveFailureRetryAndCancelLifecycle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	firstJobID := make(chan string, 1)
	retrySeen := make(chan protocol.FilesMoveRequest, 1)
	cancelJobID := make(chan string, 1)
	go func() {
		failedOnce := false
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
			if command.Command == "files.move_cancel" {
				response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.EmptyResponse{OK: true})
				_ = wsjson.Write(ctx, agentConn, response)
				continue
			}
			if command.Command != "files.move" {
				continue
			}
			request, err := decodeBody[protocol.FilesMoveRequest](command.Body)
			if err != nil {
				return
			}
			switch request.From {
			case "/checksum-source":
				if !failedOnce {
					failedOnce = true
					firstJobID <- request.JobID
					response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.FileOperationJobResponse{OK: true, JobID: request.JobID, Status: "running"})
					_ = wsjson.Write(ctx, agentConn, response)
					body, _ := protocol.EncodeBody(protocol.FileOperationJobEvent{JobID: request.JobID, Status: "failed", ErrorMessage: "copy verification failed: checksum mismatch"})
					event, _ := protocol.NewEnvelope(protocol.TypeAgentEvent, "", agentID, homeID, protocol.AgentEvent{Event: "files.move_failed", Topic: "files.jobs", Body: body})
					_ = wsjson.Write(ctx, agentConn, event)
					continue
				}
				retrySeen <- request
				response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.FileOperationJobResponse{
					OK:     true,
					JobID:  request.JobID,
					Status: "running",
				})
				_ = wsjson.Write(ctx, agentConn, response)
				body, _ := protocol.EncodeBody(protocol.FileOperationJobEvent{JobID: request.JobID, Status: "completed", FilesTotal: 3, FilesDone: 3})
				event, _ := protocol.NewEnvelope(protocol.TypeAgentEvent, "", agentID, homeID, protocol.AgentEvent{Event: "files.move_completed", Topic: "files.jobs", Body: body})
				_ = wsjson.Write(ctx, agentConn, event)
			case "/cancel-source":
				cancelJobID <- request.JobID
				response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.FileOperationJobResponse{
					OK:     true,
					JobID:  request.JobID,
					Status: "running",
				})
				_ = wsjson.Write(ctx, agentConn, response)
			}
		}
	}()

	appConn, _, err := appWebSocketDial(ctx, testServer, sessionToken)
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	body, _ := protocol.EncodeBody(protocol.FilesMoveRequest{SourceID: "primary", DestinationSourceID: "secondary", From: "/checksum-source", To: "/checksum-dest", IsDirectory: true})
	command, _ := protocol.NewEnvelope(protocol.TypeAppCommand, "req_move_checksum_failure", "", "", protocol.RoutedCommand{Command: "files.move", Body: body})
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("write failing move command: %v", err)
	}
	var started protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &started); err != nil {
		t.Fatalf("read failing move start response: %v", err)
	}
	if started.Type != protocol.TypeAppResponse {
		t.Fatalf("failing move start response = %#v, want app response", started)
	}
	var failedJobID string
	select {
	case failedJobID = <-firstJobID:
	case <-ctx.Done():
		t.Fatal("timed out waiting for failed job id")
	}
	var job store.FileOperationJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err = db.GetFileOperationJob(ctx, failedJobID)
		if err != nil {
			t.Fatalf("GetFileOperationJob failed: %v", err)
		}
		if job.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != "failed" || !strings.Contains(job.ErrorMessage, "checksum mismatch") {
		t.Fatalf("failed job = %#v, want failed checksum mismatch", job)
	}

	var retried map[string]any
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/file-jobs/"+failedJobID+"/retry", nil, &retried)
	deadline = time.Now().Add(2 * time.Second)
	for retried["status"] != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/file-jobs/"+failedJobID, nil, &retried)
	}
	if retried["status"] != "completed" {
		t.Fatalf("retry job status = %#v, want completed", retried["status"])
	}
	select {
	case request := <-retrySeen:
		if !request.IsDirectory || request.JobID != failedJobID {
			t.Fatalf("retry request = %#v, want directory retry with same job id", request)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for retry command")
	}

	cancelBody, _ := protocol.EncodeBody(protocol.FilesMoveRequest{SourceID: "primary", DestinationSourceID: "secondary", From: "/cancel-source", To: "/cancel-dest"})
	cancelCommand, _ := protocol.NewEnvelope(protocol.TypeAppCommand, "req_move_cancelled", "", "", protocol.RoutedCommand{Command: "files.move", Body: cancelBody})
	if err := wsjson.Write(ctx, appConn, cancelCommand); err != nil {
		t.Fatalf("write cancellable move command: %v", err)
	}
	var cancelledJobID string
	select {
	case cancelledJobID = <-cancelJobID:
	case <-ctx.Done():
		t.Fatal("timed out waiting for cancellable job id")
	}
	var cancelled map[string]any
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/file-jobs/"+cancelledJobID+"/cancel", nil, &cancelled)
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancel job status = %#v, want cancelled", cancelled["status"])
	}
	job, err = db.GetFileOperationJob(ctx, cancelledJobID)
	if err != nil {
		t.Fatalf("GetFileOperationJob cancelled: %v", err)
	}
	if job.Status != "cancelled" {
		t.Fatalf("cancelled job status = %q, want cancelled", job.Status)
	}
}

func TestInterruptedFileOperationJobsBecomeRollbackRequired(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_filejob_interrupt", Email: "filejob-interrupt@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_filejob_interrupt", UserID: user.ID, Name: "File Job Interrupt", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	for _, status := range []string{"queued", "running"} {
		must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{
			ID:                  "filejob_interrupt_" + status,
			HomeID:              home.ID,
			UserID:              user.ID,
			Operation:           protocol.FileOperationMove,
			SourceID:            "primary",
			DestinationSourceID: "secondary",
			FromPath:            "/" + status + "-from",
			ToPath:              "/" + status + "-to",
			Status:              status,
			CreatedAt:           now,
			UpdatedAt:           now,
		}))
		must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{
			ID:        "filejob_interrupt_upload_" + status,
			HomeID:    home.ID,
			UserID:    user.ID,
			Operation: protocol.FileTransferOperationUpload,
			SourceID:  "primary",
			FromPath:  "/" + status + "-upload",
			Status:    status,
			CreatedAt: now,
			UpdatedAt: now,
		}))
	}
	must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{
		ID:        "filejob_interrupt_completed",
		HomeID:    home.ID,
		UserID:    user.ID,
		Operation: protocol.FileOperationMove,
		SourceID:  "primary",
		FromPath:  "/done",
		ToPath:    "/done2",
		Status:    "completed",
		CreatedAt: now,
		UpdatedAt: now,
	}))
	if err := db.MarkInterruptedFileOperationJobs(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("MarkInterruptedFileOperationJobs: %v", err)
	}
	for _, id := range []string{"filejob_interrupt_queued", "filejob_interrupt_running"} {
		job, err := db.GetFileOperationJob(ctx, id)
		if err != nil {
			t.Fatalf("GetFileOperationJob %s: %v", id, err)
		}
		if job.Status != "rollback_required" {
			t.Fatalf("%s status = %q, want rollback_required", id, job.Status)
		}
	}
	for _, id := range []string{"filejob_interrupt_upload_queued", "filejob_interrupt_upload_running"} {
		job, err := db.GetFileOperationJob(ctx, id)
		must(t, err)
		if job.Status != "failed" || job.CompletedAt == nil {
			t.Fatalf("%s = %+v, want completed failed transfer", id, job)
		}
	}
	job, err := db.GetFileOperationJob(ctx, "filejob_interrupt_completed")
	if err != nil {
		t.Fatalf("GetFileOperationJob completed: %v", err)
	}
	if job.Status != "completed" {
		t.Fatalf("completed job status = %q, want completed", job.Status)
	}
}

func TestRollbackRequiredFileOperationJobCanBeRolledBack(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	job := store.FileOperationJob{
		ID:                  "filejob_rollback_required",
		AgentID:             agentID,
		HomeID:              homeID,
		UserID:              "usr_1",
		Operation:           protocol.FileOperationMove,
		SourceID:            "primary",
		DestinationSourceID: "secondary",
		FromPath:            "/source",
		ToPath:              "/dest",
		IsDirectory:         true,
		Status:              "rollback_required",
		ErrorMessage:        "source delete failed after verified copy",
		CreatedAt:           now,
		UpdatedAt:           now,
		CompletedAt:         &now,
	}
	must(t, db.CreateFileOperationJob(ctx, job))

	rollbackSeen := make(chan protocol.FilesMoveRollbackRequest, 1)
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
			if err != nil || command.Command != "files.move_rollback" {
				continue
			}
			request, err := decodeBody[protocol.FilesMoveRollbackRequest](command.Body)
			if err != nil {
				return
			}
			rollbackSeen <- request
			response, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.FileOperationJobResponse{
				OK:     true,
				JobID:  request.JobID,
				Status: "rolled_back",
			})
			_ = wsjson.Write(ctx, agentConn, response)
		}
	}()

	var rolledBack map[string]any
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/file-jobs/"+job.ID+"/rollback", nil, &rolledBack)
	if rolledBack["status"] != "rolled_back" {
		t.Fatalf("rollback response status = %#v, want rolled_back", rolledBack["status"])
	}
	select {
	case request := <-rollbackSeen:
		if request.JobID != job.ID || request.DestinationSourceID != "secondary" || request.To != "/dest" || !request.IsDirectory {
			t.Fatalf("rollback request = %#v", request)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for rollback command")
	}
}

func TestFilePolicyDeniesBlockedPrefixesDeleteAndMaxUpload(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, testServer, homeID, _, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{
		HomeID:           homeID,
		ServiceType:      domain.ServiceTypeSMB,
		PublicConfigJSON: `{"policy":{"read":true,"write":true,"delete":false,"allowed_prefixes":["/Allowed"],"blocked_prefixes":["/Allowed/Private"],"max_upload_bytes":4}}`,
		Status:           domain.SyncStatusHealthy,
		UpdatedAt:        now,
		UpdatedBy:        "usr_1",
	}))

	appConn, _, err := appWebSocketDial(ctx, testServer, sessionToken)
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	sendDeniedFileCommand(t, ctx, appConn, "files.list", protocol.FilesListRequest{SourceID: "primary", Path: "/Allowed/Private/secret.txt"})
	sendDeniedFileCommand(t, ctx, appConn, "files.delete", protocol.FilesDeleteRequest{SourceID: "primary", Path: "/Allowed/ok.txt"})

	response := requestJSONStatus(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/uploads", map[string]any{
		"source_id": "primary",
		"path":      "/Allowed/too-large.txt",
		"size":      5,
	}, http.StatusRequestEntityTooLarge)
	var uploadLimit struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		MaxUploadBytes       int64 `json:"max_upload_bytes"`
		AttemptedUploadBytes int64 `json:"attempted_upload_bytes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&uploadLimit); err != nil {
		t.Fatalf("decode upload limit response: %v", err)
	}
	response.Body.Close()
	if uploadLimit.Error.Code != "upload_too_large" || uploadLimit.MaxUploadBytes != 4 || uploadLimit.AttemptedUploadBytes != 5 {
		t.Fatalf("upload limit response = %#v", uploadLimit)
	}

	events, err := db.ListAuditEvents(ctx, homeID, "file_transfer.setup_failed", auditSeverityWarning, "file_policy", 10, "occurred_at", "desc")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 || events[0].TargetID != protocol.FileTransferOperationUpload {
		t.Fatalf("upload setup audit events = %#v", events)
	}
	if strings.Contains(events[0].MetadataJSON, "/Allowed/too-large.txt") || !strings.Contains(events[0].MetadataJSON, "path_hash") {
		t.Fatalf("upload setup audit metadata = %s", events[0].MetadataJSON)
	}
}

func TestFilePolicyDefaultsAllowDelete(t *testing.T) {
	t.Parallel()

	if err := (fileAccessPolicy{}).allow("delete", "/Allowed/ok.txt"); err != nil {
		t.Fatalf("default policy delete denied: %v", err)
	}
}

func TestClearFileJobHistoryPreservesActiveAndRollbackRequiredJobs(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, testServer, homeID, _, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	now := time.Now().UTC()
	for _, job := range []store.FileOperationJob{
		{ID: "filejob_api_clear_done", HomeID: homeID, UserID: "usr_1", Operation: "upload", FromPath: "/done", Status: "completed", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_api_clear_active", HomeID: homeID, UserID: "usr_1", Operation: "upload", FromPath: "/active", Status: "running", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_api_clear_rollback", HomeID: homeID, UserID: "usr_1", Operation: "move", FromPath: "/source", ToPath: "/destination", Status: "rollback_required", CreatedAt: now, UpdatedAt: now},
	} {
		must(t, db.CreateFileOperationJob(ctx, job))
	}

	response := requestJSONStatus(t, testServer, sessionToken, http.MethodDelete, "/v1/home/file-jobs", nil, http.StatusOK)
	var payload struct {
		Cleared int64 `json:"cleared"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode clear response: %v", err)
	}
	response.Body.Close()
	if payload.Cleared != 1 {
		t.Fatalf("cleared = %d, want 1", payload.Cleared)
	}
	if _, err := db.GetFileOperationJob(ctx, "filejob_api_clear_done"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completed job error = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"filejob_api_clear_active", "filejob_api_clear_rollback"} {
		if _, err := db.GetFileOperationJob(ctx, id); err != nil {
			t.Fatalf("preserved job %s: %v", id, err)
		}
	}
}

func TestAuditEventsCanBeFilteredAndRedacted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, testServer, homeID, _, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	must(t, db.UpsertUserNote(ctx, domain.UserNote{
		ID: "note_audit_label", NoteID: "audit-label", OwnerUserID: "usr_1", HomeID: homeID,
		Title: "Hank Build", PageType: "text", Revision: "rev_1", Checksum: "checksum_1",
		CreatedAt: now, UpdatedAt: now, UpdatedBy: "usr_1",
	}))
	must(t, db.CreateAuditEvent(ctx, store.AuditEvent{
		ID:           "audit_1",
		OccurredAt:   now,
		ActorUserID:  stringPtr("usr_1"),
		HomeID:       stringPtr(homeID),
		EventType:    "service_profile.changed",
		Severity:     auditSeverityInfo,
		TargetType:   "note",
		TargetID:     "note_audit_label",
		MetadataJSON: `{"token":"raw-token","password":"raw-password","safe":"ok"}`,
	}))

	var payload struct {
		Events []struct {
			EventType   string         `json:"event_type"`
			ActorLabel  string         `json:"actor_label"`
			TargetLabel string         `json:"target_label"`
			HelperText  string         `json:"helper_text"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"events"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/audit-events?event_type=service_profile.changed", nil, &payload)
	if len(payload.Events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(payload.Events))
	}
	if payload.Events[0].Metadata["token"] != "[redacted]" || payload.Events[0].Metadata["password"] != "[redacted]" {
		t.Fatalf("metadata not redacted: %#v", payload.Events[0].Metadata)
	}
	if payload.Events[0].Metadata["safe"] != "ok" {
		t.Fatalf("safe metadata changed: %#v", payload.Events[0].Metadata)
	}
	if payload.Events[0].ActorLabel != "user@example.com" || payload.Events[0].TargetLabel != "Hank Build" {
		t.Fatalf("audit presentation labels = actor %q target %q", payload.Events[0].ActorLabel, payload.Events[0].TargetLabel)
	}
	if payload.Events[0].HelperText == "" || !strings.Contains(payload.Events[0].HelperText, "Connection settings changed") {
		t.Fatalf("helper text = %q, want connection settings diagnosis", payload.Events[0].HelperText)
	}
}

func TestHomeAssistantAuditKeepsControlsAndSuppressesPassiveStateNoise(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, testServer, homeID, _, _, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	pending := &pendingRequest{
		requestID: "req_ha_audit", homeID: homeID,
		homeAssistant: &pendingHomeAssistantAudit{
			actorUserID: "usr_1", entityID: "light.kitchen", operation: "light.turn_on", initiator: "human",
		},
	}
	server.auditHomeAssistantControl(ctx, pending, "succeeded", "")
	must(t, db.CreateAuditEvent(ctx, store.AuditEvent{
		ID: "audit_passive_ha_state", OccurredAt: time.Now().UTC(), HomeID: stringPtr(homeID),
		EventType: "dashboard_entities.changed", Severity: auditSeverityInfo,
		TargetType: "dashboard_entity", TargetID: "sensor.temperature", MetadataJSON: "{}",
	}))

	events, err := db.ListAuditEvents(ctx, homeID, "", "", "", 20, "occurred_at", "desc")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	for _, event := range events {
		if event.EventType == "dashboard_entities.changed" {
			t.Fatalf("default audit log included passive Home Assistant state: %#v", event)
		}
	}
	controls, err := db.ListAuditEvents(ctx, homeID, "homeassistant.control.succeeded", "", "homeassistant_entity", 20, "occurred_at", "desc")
	if err != nil || len(controls) != 1 || controls[0].ActorUserID == nil || *controls[0].ActorUserID != "usr_1" || controls[0].TargetID != "light.kitchen" {
		t.Fatalf("Home Assistant control audit = %#v, %v", controls, err)
	}
	passive, err := db.ListAuditEvents(ctx, homeID, "dashboard_entities.changed", "", "", 20, "occurred_at", "desc")
	if err != nil || len(passive) != 1 {
		t.Fatalf("exact passive notification audit lookup = %#v, %v", passive, err)
	}
}

func TestHomeAssistantAuditForCommandExtractsSafeControlFacts(t *testing.T) {
	serviceBody, err := json.Marshal(map[string]any{"entity_id": "light.kitchen", "brightness": 80})
	if err != nil {
		t.Fatal(err)
	}
	commandBody, err := json.Marshal(protocol.HomeAssistantCallServiceRequest{Domain: "light", Service: "turn_on", Body: serviceBody})
	if err != nil {
		t.Fatal(err)
	}
	audit := homeAssistantAuditForCommand(protocol.RoutedCommand{Command: "homeassistant.call_service", Body: commandBody}, "usr_1", "human")
	if audit == nil || audit.actorUserID != "usr_1" || audit.entityID != "light.kitchen" || audit.operation != "light.turn_on" || audit.initiator != "human" {
		t.Fatalf("Home Assistant command audit = %#v", audit)
	}
}

func TestAuditEventsCanBeSorted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, testServer, homeID, _, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	for _, event := range []store.AuditEvent{
		{ID: "audit_sort_z", OccurredAt: now, ActorUserID: stringPtr("usr_1"), HomeID: stringPtr(homeID), EventType: "z.download.failed", Severity: auditSeverityWarning, TargetType: "file_transfer", TargetID: "z", MetadataJSON: "{}"},
		{ID: "audit_sort_a", OccurredAt: now.Add(-time.Minute), ActorUserID: stringPtr("usr_1"), HomeID: stringPtr(homeID), EventType: "a.app_package.preview_failed", Severity: auditSeverityWarning, TargetType: "app_package", TargetID: "a", MetadataJSON: "{}"},
	} {
		must(t, db.CreateAuditEvent(ctx, event))
	}

	var payload struct {
		Events []struct {
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/audit-events?sort=event_type&order=asc", nil, &payload)
	if len(payload.Events) < 2 {
		t.Fatalf("audit event count = %d, want at least 2", len(payload.Events))
	}
	if payload.Events[0].EventType != "a.app_package.preview_failed" || payload.Events[1].EventType != "z.download.failed" {
		t.Fatalf("sorted audit events = %#v", payload.Events)
	}
}

func TestAdminWorkflowAuditTelemetryAndFileJobUI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, testServer, homeID, _, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	member := domain.User{ID: "usr_admin_workflow_member", Email: "admin-workflow-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, member))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: homeID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_admin_workflow_member", UserID: member.ID, TokenHash: hashToken("admin-workflow-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateAuditEvent(ctx, store.AuditEvent{
		ID:           "audit_admin_workflow",
		OccurredAt:   now,
		ActorUserID:  stringPtr("usr_1"),
		HomeID:       stringPtr(homeID),
		EventType:    "file_operation.denied",
		Severity:     auditSeverityWarning,
		TargetType:   "file_policy",
		TargetID:     "files.list",
		MetadataJSON: `{"reason":"blocked","secret_token":"must-hide"}`,
	}))

	memberResponse := requestJSONStatus(t, testServer, "admin-workflow-member-token", http.MethodGet, "/v1/home/audit-events", nil, http.StatusForbidden)
	memberResponse.Body.Close()
	memberResponse = requestJSONStatus(t, testServer, "admin-workflow-member-token", http.MethodGet, "/v1/home/query-telemetry", nil, http.StatusForbidden)
	memberResponse.Body.Close()

	var auditPayload struct {
		Events []struct {
			EventType string         `json:"event_type"`
			Severity  string         `json:"severity"`
			Metadata  map[string]any `json:"metadata"`
		} `json:"events"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/audit-events?event_type=file_operation.denied&severity=warning&target_type=file_policy", nil, &auditPayload)
	if len(auditPayload.Events) != 1 {
		t.Fatalf("filtered audit events = %d, want 1", len(auditPayload.Events))
	}
	if auditPayload.Events[0].EventType != "file_operation.denied" || auditPayload.Events[0].Severity != auditSeverityWarning {
		t.Fatalf("filtered audit event = %#v", auditPayload.Events[0])
	}
	if auditPayload.Events[0].Metadata["secret_token"] != "[redacted]" {
		t.Fatalf("audit metadata not redacted: %#v", auditPayload.Events[0].Metadata)
	}

	queryResponse := requestJSONStatusAny(t, testServer, sessionToken, http.MethodGet, "/v1/home/query-telemetry?limit=20", nil, http.StatusOK, http.StatusServiceUnavailable)
	queryResponse.Body.Close()

	reactShell, err := fs.ReadFile(uiAssets, "ui/react/index.html")
	if err != nil {
		t.Fatalf("read React shell: %v", err)
	}
	assertReactShell(t, "/dashboard/settings/backups", string(reactShell))
	kanbanApp, err := fs.ReadFile(uiAssets, "ui/mcp/kanban-v1.html")
	if err != nil || len(kanbanApp) == 0 {
		t.Fatalf("read embedded Kanban MCP app: bytes=%d err=%v", len(kanbanApp), err)
	}
}

func TestAuthorizationMatrixDeniesMembersRevocationsFeaturesAndNoteIsolation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_authz_owner", Email: "authz-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_authz_member", Email: "authz-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	outsider := domain.User{ID: "usr_authz_outsider", Email: "authz-outsider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_authz_matrix", UserID: owner.ID, Name: "Authorization Matrix Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_authz_matrix", HomeID: home.ID, Name: "Authorization Matrix Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	const ownerToken = "authz-owner-token"
	const memberToken = "authz-member-token"
	const outsiderToken = "authz-outsider-token"
	const agentToken = "authz-agent-token"

	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateUser(ctx, outsider))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_authz_matrix", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentToken), CreatedAt: now}))
	for _, session := range []domain.AppSession{
		{ID: "sess_authz_owner", UserID: owner.ID, TokenHash: hashToken(ownerToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now},
		{ID: "sess_authz_member", UserID: member.ID, TokenHash: hashToken(memberToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now},
		{ID: "sess_authz_outsider", UserID: outsider.ID, TokenHash: hashToken(outsiderToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now},
	} {
		must(t, db.CreateSession(ctx, session))
	}

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()
	agentConn := connectAgentForTest(t, ctx, testServer, agent.ID, agentToken, home.Name)
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	response := requestJSONStatus(t, testServer, outsiderToken, http.MethodGet, "/v1/home", nil, http.StatusNotFound)
	response.Body.Close()

	requestJSON(t, testServer, ownerToken, http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "authz-shared.md",
		"title":   "Authz Shared",
		"content": "share me",
	}, nil)
	response = requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/me/notes/authz-shared.md", nil, http.StatusNotFound)
	response.Body.Close()
	response = requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/home/notes/authz-shared.md", nil, http.StatusNotFound)
	response.Body.Close()

	requestJSON(t, testServer, ownerToken, http.MethodPost, "/v1/home/notes/authz-shared.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)
	var fetched protocol.NotesFetchResponse
	requestJSON(t, testServer, memberToken, http.MethodGet, "/v1/home/notes/authz-shared.md", nil, &fetched)
	if fetched.Content != "share me" {
		t.Fatalf("shared note content = %q, want share me", fetched.Content)
	}
	requestJSON(t, testServer, ownerToken, http.MethodDelete, "/v1/home/notes/authz-shared.md/shares/"+member.ID, nil, nil)
	response = requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/home/notes/authz-shared.md", nil, http.StatusNotFound)
	response.Body.Close()

	requestJSON(t, testServer, ownerToken, http.MethodDelete, "/v1/home/members/"+member.ID, nil, nil)
	response = requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/home", nil, http.StatusNotFound)
	response.Body.Close()
	revokedConn, _, err := appWebSocketDial(ctx, testServer, memberToken)
	if err != nil {
		t.Fatalf("revoked app websocket dial: %v", err)
	}
	sendAppCommandExpectError(t, ctx, revokedConn, "req_revoked_home", "homeassistant.fetch_states", nil, "home_not_found")
	revokedConn.Close(websocket.StatusNormalClosure, "done")

	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.UpsertHomePermissions(ctx, domain.HomePermissions{
		HomeID:               home.ID,
		HomeAssistantEnabled: false,
		FilesEnabled:         false,
		NotesEnabled:         false,
		UpdatedAt:            now,
		UpdatedBy:            owner.ID,
	}))

	response = requestJSONStatus(t, testServer, memberToken, http.MethodGet, "/v1/home/notes", nil, http.StatusForbidden)
	response.Body.Close()
	response = requestJSONStatus(t, testServer, memberToken, http.MethodPost, "/v1/home/files/uploads", map[string]any{
		"source_id": "primary",
		"path":      "/blocked.txt",
		"size":      1,
	}, http.StatusForbidden)
	response.Body.Close()

	memberConn, _, err := appWebSocketDial(ctx, testServer, memberToken)
	if err != nil {
		t.Fatalf("member app websocket dial: %v", err)
	}
	defer memberConn.Close(websocket.StatusNormalClosure, "done")
	sendAppCommandExpectError(t, ctx, memberConn, "req_ha_feature_disabled", "homeassistant.fetch_states", nil, "permission_denied")
	sendAppCommandExpectError(t, ctx, memberConn, "req_files_feature_disabled", "files.list", protocol.FilesListRequest{SourceID: "primary", Path: "/"}, "permission_denied")
	sendAppCommandExpectError(t, ctx, memberConn, "req_notes_feature_disabled", "notes.list", nil, "permission_denied")
	sendAppCommandExpectError(t, ctx, memberConn, "req_restart_admin_only", protocol.CommandSystemRestart, protocol.SystemRestartRequest{Reason: "member attempt"}, "permission_denied")

	var ownerHomeNotes struct {
		Notes []protocol.NoteSummary `json:"notes"`
	}
	requestJSON(t, testServer, ownerToken, http.MethodGet, "/v1/home/notes", nil, &ownerHomeNotes)
}

func TestRealtimeNoteCollabTopicRequiresVisibleNote(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	owner := domain.User{ID: "usr_owner", Email: "owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_realtime", UserID: owner.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_owner", UserID: owner.ID, TokenHash: hashToken("owner-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/me/notes", map[string]any{
		"note_id": "private.md",
		"title":   "Private",
		"content": "private",
	}, nil)

	memberConn, _, err := appWebSocketDial(ctx, testServer, "member-token")
	if err != nil {
		t.Fatalf("member app websocket dial: %v", err)
	}
	defer memberConn.Close(websocket.StatusNormalClosure, "done")

	subscribe := func(requestID string) protocol.Envelope {
		body, _ := protocol.EncodeBody(protocol.AppSubscribeRequest{Topics: []string{"notes.collab:home:private.md"}})
		envelope, _ := protocol.NewEnvelope(protocol.TypeAppCommand, requestID, "", "", protocol.RoutedCommand{Command: "app.subscribe", Body: body})
		if err := wsjson.Write(ctx, memberConn, envelope); err != nil {
			t.Fatalf("write subscribe: %v", err)
		}
		var response protocol.Envelope
		if err := wsjson.Read(ctx, memberConn, &response); err != nil {
			t.Fatalf("read subscribe response: %v", err)
		}
		return response
	}

	denied := subscribe("req_sub_denied")
	if denied.Type != protocol.TypeAppError {
		t.Fatalf("unshared note subscribe type = %q, want app.error", denied.Type)
	}

	requestJSON(t, testServer, "owner-token", http.MethodPost, "/v1/home/notes/private.md/shares", map[string]any{
		"user_id": member.ID,
	}, nil)

	allowed := subscribe("req_sub_allowed")
	if allowed.Type != protocol.TypeAppResponse {
		t.Fatalf("shared note subscribe type = %q, want app.response: %#v", allowed.Type, allowed.Error)
	}
}

func TestRestartRecoveryPersistsTicketsTransfersAndLoginBackoff(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_restart", Email: "restart@example.com", PasswordHash: string(mustPasswordHash(t, "correct-password")), CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_restart", UserID: user.ID, Name: "Restart Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_restart", HomeID: home.ID, Name: "Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	sessionToken := "restart-session-token"
	agentToken := "restart-agent-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_restart", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentToken), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_restart", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server1 := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer1 := httptest.NewServer(server1.http.Handler)
	agentConn1 := connectAgentForTest(t, ctx, testServer1, agent.ID, agentToken, home.Name)

	var ticket struct {
		WebSocketPath string `json:"websocket_path"`
	}
	requestJSON(t, testServer1, sessionToken, http.MethodPost, "/v1/ws/app-ticket", nil, &ticket)

	var setup struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
	}
	requestJSON(t, testServer1, sessionToken, http.MethodPost, "/v1/home/files/uploads", map[string]string{"path": "/restart.txt", "source_id": "primary"}, &setup)

	backoffActive := false
	for i := 0; i < 10; i++ {
		response := requestJSONStatusAny(t, testServer1, "", http.MethodPost, "/v1/auth/login", map[string]string{"email": user.Email, "password": "bad"}, http.StatusUnauthorized, http.StatusTooManyRequests)
		if response.StatusCode == http.StatusTooManyRequests {
			backoffActive = true
			response.Body.Close()
			break
		}
		response.Body.Close()
	}
	if !backoffActive {
		t.Fatal("login backoff did not become active before restart")
	}

	agentConn1.Close(websocket.StatusNormalClosure, "restart")
	testServer1.Close()

	server2 := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer2 := httptest.NewServer(server2.http.Handler)
	defer testServer2.Close()

	appConn, _, err := websocket.Dial(ctx, wsURL(testServer2.URL, ticket.WebSocketPath), nil)
	if err != nil {
		t.Fatalf("ticket did not survive restart: %v", err)
	}
	appConn.Close(websocket.StatusNormalClosure, "done")

	backoffResponse := requestJSONStatus(t, testServer2, "", http.MethodPost, "/v1/auth/login", map[string]string{"email": user.Email, "password": "bad"}, http.StatusTooManyRequests)
	backoffResponse.Body.Close()

	agentConn := connectAgentForTest(t, ctx, testServer2, agent.ID, agentToken, home.Name)
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			switch envelope.Type {
			case protocol.TypeFileTransferOpen:
				open, _ := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
				ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agent.ID, home.ID, protocol.FileTransferReady{Operation: open.Operation, Path: open.Path, Offset: open.Offset})
				_ = wsjson.Write(ctx, agentConn, ready)
			case protocol.TypeFileTransferData:
			case protocol.TypeFileTransferComplete:
				complete, _ := protocol.DecodePayload[protocol.FileTransferComplete](envelope)
				reply, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agent.ID, home.ID, complete)
				_ = wsjson.Write(ctx, agentConn, reply)
			}
		}
	}()

	uploadRequest, _ := http.NewRequest(http.MethodPut, testServer2.URL+setup.URL, strings.NewReader("ok"))
	uploadRequest.Header.Set("Authorization", "Bearer "+setup.TransferToken)
	uploadResponse, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		t.Fatalf("upload after restart: %v", err)
	}
	defer uploadResponse.Body.Close()
	if uploadResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uploadResponse.Body)
		t.Fatalf("upload status = %d body=%s", uploadResponse.StatusCode, string(body))
	}
}

func setupServerAndAgentWithDB(t *testing.T, ctx context.Context) (*store.Store, *httptest.Server, string, string, string, *websocket.Conn) {
	t.Helper()
	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	agentRawToken := "agent-token"
	sessionRawToken := "session-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentRawToken), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	agentConn := connectAgentForTest(t, ctx, testServer, agent.ID, agentRawToken, home.Name)
	return db, testServer, home.ID, agent.ID, sessionRawToken, agentConn
}

func connectAgentForTest(t *testing.T, ctx context.Context, testServer *httptest.Server, agentID string, token string, homeName string) *websocket.Conn {
	t.Helper()
	agentConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer " + token},
			"X-Hank-Agent-ID": []string{agentID},
		},
	})
	if err != nil {
		t.Fatalf("agent websocket dial: %v", err)
	}
	agentConn.SetReadLimit(maxWSMessageBytes)
	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agentID, "", protocol.AgentRegister{AgentID: agentID, HomeName: homeName, Capabilities: []string{protocol.CapabilityAppsSandboxV1}})
	if err != nil {
		t.Fatalf("NewEnvelope register: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, register); err != nil {
		t.Fatalf("agent register write: %v", err)
	}
	var registered protocol.Envelope
	if err := wsjson.Read(ctx, agentConn, &registered); err != nil {
		t.Fatalf("agent read registered: %v", err)
	}
	return agentConn
}

func sendDeniedFileCommand(t *testing.T, ctx context.Context, appConn *websocket.Conn, commandName string, body any) {
	t.Helper()
	sendAppCommandExpectError(t, ctx, appConn, "req_"+strings.ReplaceAll(commandName, ".", "_"), commandName, body, "permission_denied")
}

func sendAppCommandExpectError(t *testing.T, ctx context.Context, appConn *websocket.Conn, requestID string, commandName string, body any, wantCode string) {
	t.Helper()
	var encoded json.RawMessage
	if body != nil {
		var err error
		encoded, err = protocol.EncodeBody(body)
		if err != nil {
			t.Fatalf("encode %s: %v", commandName, err)
		}
	}
	envelope, _ := protocol.NewEnvelope(protocol.TypeAppCommand, requestID, "", "", protocol.RoutedCommand{Command: commandName, Body: encoded})
	if err := wsjson.Write(ctx, appConn, envelope); err != nil {
		t.Fatalf("write %s: %v", commandName, err)
	}
	var response protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &response); err != nil {
		t.Fatalf("read %s: %v", commandName, err)
	}
	if response.RequestID != requestID {
		t.Fatalf("%s request_id = %q, want %q", commandName, response.RequestID, requestID)
	}
	if response.Type != protocol.TypeAppError || response.Error == nil || response.Error.Code != wantCode {
		t.Fatalf("%s response = %#v, want %s", commandName, response, wantCode)
	}
}

func requestJSONStatusAny(t *testing.T, server *httptest.Server, sessionToken string, method string, path string, body any, wantStatuses ...int) *http.Response {
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
	if sessionToken != "" {
		request.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range wantStatuses {
		if response.StatusCode == want {
			return response
		}
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	t.Fatalf("request %s %s status = %d, want one of %v body=%s", method, path, response.StatusCode, wantStatuses, string(data))
	return response
}

func stringPtr(value string) *string {
	return &value
}

func TestHTTPServerTimeoutsSlowlorisAndAllowsLargeTransfer(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	server.http.ReadHeaderTimeout = 100 * time.Millisecond
	server.http.ReadTimeout = 200 * time.Millisecond

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() { _ = server.http.Serve(listener) }()
	defer server.http.Close()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("slowloris dial: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: "); err != nil {
		t.Fatalf("slowloris write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("slowloris connection stayed readable; expected timeout close")
	}

	response, err := http.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("healthz after slowloris: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", response.StatusCode)
	}
}

func TestRouteDeadlineMiddlewareScopesWriteDeadline(t *testing.T) {
	t.Parallel()

	const chunkCount = 5
	mux := http.NewServeMux()
	slowWriter := func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flush")
			return
		}
		w.WriteHeader(http.StatusOK)
		for i := 0; i < chunkCount; i++ {
			if _, err := w.Write(make([]byte, 1024)); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}
	mux.HandleFunc("/v1/file-transfers/", slowWriter)
	mux.HandleFunc("/plain", slowWriter)

	httpServer := &http.Server{
		Handler:      routeDeadlineMiddleware(mux),
		WriteTimeout: 300 * time.Millisecond,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() { _ = httpServer.Serve(listener) }()
	defer httpServer.Close()

	readAll := func(path string) (int, error) {
		response, err := http.Get("http://" + listener.Addr().String() + path)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return len(data), err
	}

	transferBytes, err := readAll("/v1/file-transfers/test")
	if err != nil || transferBytes != chunkCount*1024 {
		t.Fatalf("transfer route bytes = %d err = %v, want %d bytes without error", transferBytes, err, chunkCount*1024)
	}

	plainBytes, err := readAll("/plain")
	if err == nil && plainBytes == chunkCount*1024 {
		t.Fatal("plain route slow write completed fully; expected server write deadline to cut it off")
	}
}

func TestHTTPServerAllowsLargeValidTransfer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	received := make(chan int64, 1)
	go func() {
		var total int64
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			switch envelope.Type {
			case protocol.TypeFileTransferOpen:
				open, _ := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
				ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{Operation: open.Operation, SourceID: open.SourceID, Path: open.Path, Offset: open.Offset})
				_ = wsjson.Write(ctx, agentConn, ready)
			case protocol.TypeFileTransferData:
				chunk, err := protocol.DecodePayload[protocol.FileTransferChunk](envelope)
				if err == nil {
					data, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
					if decodeErr == nil {
						total += int64(len(data))
					}
				}
			case protocol.TypeFileTransferComplete:
				complete, _ := protocol.DecodePayload[protocol.FileTransferComplete](envelope)
				reply, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, complete)
				_ = wsjson.Write(ctx, agentConn, reply)
				received <- total
				return
			}
		}
	}()

	payload := bytes.Repeat([]byte("large-transfer\n"), 160000)
	var setup struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/uploads", map[string]any{"path": "/large-transfer.txt", "source_id": "primary", "size": len(payload)}, &setup)
	uploadRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, testServer.URL+setup.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("large upload request: %v", err)
	}
	uploadRequest.Header.Set("Authorization", "Bearer "+setup.TransferToken)
	uploadResponse, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		t.Fatalf("large upload: %v", err)
	}
	defer uploadResponse.Body.Close()
	if uploadResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uploadResponse.Body)
		t.Fatalf("large upload status = %d body=%s", uploadResponse.StatusCode, string(body))
	}
	select {
	case got := <-received:
		if got != int64(len(payload)) {
			t.Fatalf("agent received %d bytes, want %d", got, len(payload))
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for large transfer completion")
	}
}

func TestHTTPServerShutdownCancelsInFlightRequest(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	cancelled := make(chan struct{})
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	server.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(cancelled)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.http.Serve(listener) }()

	responseCh := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		responseCh <- err
	}()
	select {
	case <-cancelled:
		t.Fatal("request cancelled before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("in-flight request context was not cancelled by shutdown")
	}
	<-responseCh
}
