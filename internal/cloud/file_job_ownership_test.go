package cloud

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestFileJobOwnershipRejectsForeignEventsAndTerminalRegression(t *testing.T) {
	db, server, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	job := store.FileOperationJob{ID: "owned-move", HomeID: home.ID, AgentID: agent.ID, UserID: home.UserID, Operation: "move", Status: "running", SourceID: "primary", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateFileOperationJob(ctx, job))
	event := func(homeID, agentID, event, status string) {
		body, _ := json.Marshal(protocol.FileOperationJobEvent{JobID: job.ID, Status: status, BytesDone: 12, BytesTotal: 12, FilesDone: 1, FilesTotal: 1})
		server.handleFileMoveJobEvent(ctx, homeID, agentID, event, body)
	}
	assertStatus := func(want string) {
		t.Helper()
		got, err := db.GetFileOperationJob(ctx, job.ID)
		must(t, err)
		if got.Status != want {
			t.Fatalf("status = %s, want %s", got.Status, want)
		}
	}
	for _, sender := range []struct{ home, agent string }{{home.ID, "other-agent"}, {home.ID, ""}, {"other-home", agent.ID}} {
		event(sender.home, sender.agent, "files.move_completed", "completed")
		assertStatus("running")
	}
	event(home.ID, agent.ID, "files.move_progress", "completed")
	assertStatus("running")
	event(home.ID, agent.ID, "files.move_completed", "completed")
	assertStatus("completed")
	event(home.ID, agent.ID, "files.move_progress", "running")
	assertStatus("completed")
	event(home.ID, agent.ID, "files.move_failed", "failed")
	assertStatus("completed")
	must(t, db.UpdateFileOperationJob(ctx, job.ID, "cancelled", 0, 0, "", &now))
	event(home.ID, agent.ID, "files.move_failed", "rollback_required")
	assertStatus("rollback_required")
	event(home.ID, agent.ID, "files.move_progress", "running")
	assertStatus("rollback_required")
	// The same agent cannot pass a transfer or an ambiguous historical job off as a move.
	for _, operation := range []string{"download", "upload", "move"} {
		other := job
		other.ID = "unowned-" + operation
		other.Operation = operation
		if operation == "move" {
			other.AgentID = ""
		}
		must(t, db.CreateFileOperationJob(ctx, other))
		body, _ := json.Marshal(protocol.FileOperationJobEvent{JobID: other.ID, Status: "completed"})
		server.handleFileMoveJobEvent(ctx, home.ID, agent.ID, "files.move_completed", body)
		got, err := db.GetFileOperationJob(ctx, other.ID)
		must(t, err)
		if got.Status != "running" {
			t.Fatalf("changed unrelated job: %+v", got)
		}
	}
}

func TestFileJobOwnershipPinsCompletionToPendingJob(t *testing.T) {
	db, server, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"pending-move", "foreign-move"} {
		must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{ID: id, HomeID: home.ID, AgentID: agent.ID, UserID: home.UserID, Operation: "move", Status: "running", CreatedAt: now, UpdatedAt: now}))
	}
	pending := &pendingRequest{fileJobID: "pending-move", homeID: home.ID, requestID: "request"}
	for _, payload := range []string{`{"job_id":"foreign-move","status":"completed"}`, `{"job_id":1}`, `null`, `{"status":"invented"}`, `{"bytes_done":-1}`} {
		err := server.completePendingFileJob(ctx, pending, protocol.Envelope{AgentID: agent.ID, HomeID: home.ID, Payload: json.RawMessage(payload)})
		if err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	for _, id := range []string{"pending-move", "foreign-move"} {
		got, err := db.GetFileOperationJob(ctx, id)
		must(t, err)
		if got.Status != "running" {
			t.Fatalf("invalid reply changed %s", id)
		}
	}
	must(t, server.completePendingFileJob(ctx, pending, protocol.Envelope{AgentID: agent.ID, HomeID: home.ID, Payload: json.RawMessage(`{"job_id":"pending-move","status":"completed","files_done":1}`)}))
	got, err := db.GetFileOperationJob(ctx, "pending-move")
	must(t, err)
	if got.Status != "completed" {
		t.Fatal("legitimate completion rejected")
	}
}

func TestFileJobOwnershipRecoveryRejectsUnknownAndOfflineOwners(t *testing.T) {
	db, server, home, agent, token, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// An online primary cannot act as fallback for a missing/offline worker owner.
	primary := agent
	primary.ID = "new-primary"
	primary.AgentType = AgentTypePrimary
	must(t, db.UpsertAgent(ctx, primary))
	server.router.RegisterAgent(home.ID, primary, &wsPeer{}, nil, AgentTypePrimary, nil)
	for _, owner := range []string{"", agent.ID} {
		for _, action := range []string{"retry", "rollback", "cancel"} {
			status := "failed"
			if action == "rollback" {
				status = "rollback_required"
			}
			if action == "cancel" {
				status = "running"
			}
			job := store.FileOperationJob{ID: newID("job"), HomeID: home.ID, AgentID: owner, UserID: home.UserID, Operation: "move", Status: status, CreatedAt: now, UpdatedAt: now}
			must(t, db.CreateFileOperationJob(ctx, job))
			response, _ := credentialAdminRequest(t, server, token, http.MethodPost, "/v1/home/file-jobs/"+job.ID+"/"+action)
			response.Body.Close()
			if response.StatusCode < 400 {
				t.Fatalf("%s accepted owner %q", action, owner)
			}
			got, err := db.GetFileOperationJob(ctx, job.ID)
			must(t, err)
			if got.Status != status {
				t.Fatalf("%s changed job before owner check", action)
			}
		}
	}
	server.router.RegisterAgent(home.ID, agent, &wsPeer{}, nil, AgentTypeWorker, nil)
	must(t, db.UpsertHomeServiceProfile(ctx, domain.HomeServiceProfile{HomeID: home.ID, ServiceType: domain.ServiceTypeSMB, PublicConfigJSON: `{"policy":{"delete":false}}`, Status: domain.SyncStatusHealthy, UpdatedAt: now, UpdatedBy: home.UserID}))
	for _, action := range []string{"files.move", "files.move_rollback"} {
		job := store.FileOperationJob{HomeID: home.ID, AgentID: agent.ID, Operation: "move"}
		if err := server.authorizeFileJobAction(ctx, job, action, map[string]string{"from": "/source", "to": "/dest"}); err == nil {
			t.Fatalf("%s bypassed policy", action)
		}
	}
}

func TestFileJobOwnershipDatabaseRejectsCrossHomeOwner(t *testing.T) {
	db, _, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	other := home
	other.ID = "foreign-home"
	must(t, db.CreateHome(ctx, other))
	job := store.FileOperationJob{ID: "cross-home", HomeID: other.ID, AgentID: agent.ID, UserID: home.UserID, Operation: "move", Status: "running", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateFileOperationJob(ctx, job); err == nil {
		t.Fatal("database accepted cross-Home ownership")
	}
}

func TestFileJobOwnershipWebSocketRecoveryAndCancelAcknowledgement(t *testing.T) {
	db, server, home, agent, sessionToken, agentToken := credentialHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	owner, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/agent"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + agentToken}, "X-Hank-Agent-ID": {agent.ID}}})
	must(t, err)
	defer owner.CloseNow()
	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, AgentType: AgentTypeWorker})
	must(t, err)
	must(t, wsjson.Write(ctx, owner, register))
	readUntilAgentRegistered(t, ctx, owner)
	now := time.Now().UTC()
	job := store.FileOperationJob{ID: "websocket-recovery", HomeID: home.ID, AgentID: agent.ID, UserID: home.UserID, Operation: "move", Status: "rollback_required", DestinationSourceID: "actual-source", ToPath: "/actual-destination", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateFileOperationJob(ctx, job))
	app, _, err := appWebSocketDial(ctx, ts, sessionToken)
	must(t, err)
	defer app.CloseNow()
	body, _ := protocol.EncodeBody(protocol.FilesMoveRollbackRequest{JobID: job.ID, DestinationSourceID: "forged-source", To: "/forged-destination"})
	command, _ := protocol.NewEnvelope(protocol.TypeAppCommand, "raw-rollback", "other-machine", home.ID, protocol.RoutedCommand{Command: "files.move_rollback", Body: body})
	must(t, wsjson.Write(ctx, app, command))
	var routed protocol.Envelope
	must(t, wsjson.Read(ctx, owner, &routed))
	request, err := protocol.DecodePayload[protocol.RoutedCommand](routed)
	must(t, err)
	rollback, err := decodeBody[protocol.FilesMoveRollbackRequest](request.Body)
	must(t, err)
	if routed.AgentID != agent.ID || rollback.To != job.ToPath || rollback.DestinationSourceID != job.DestinationSourceID {
		t.Fatalf("recovery redirected: %+v %+v", routed, rollback)
	}
	reply, _ := protocol.NewEnvelope(protocol.TypeCloudResponse, routed.RequestID, agent.ID, home.ID, protocol.FileOperationJobResponse{OK: true, JobID: job.ID, Status: "rolled_back"})
	must(t, wsjson.Write(ctx, owner, reply))
	var response protocol.Envelope
	must(t, wsjson.Read(ctx, app, &response))
	if response.Error != nil {
		t.Fatalf("legitimate worker rollback rejected: %+v", response.Error)
	}
	got, err := db.GetFileOperationJob(ctx, job.ID)
	must(t, err)
	if got.Status != "rolled_back" {
		t.Fatal("rollback not persisted")
	}
	for _, payload := range []string{`{"ok":false}`, `null`, `{}`, `{"ok":true}`} {
		must(t, db.UpdateFileOperationJob(ctx, job.ID, "running", 0, 0, "", nil))
		done := make(chan error, 1)
		go func() {
			_, err := server.cancelFileOperationJob(ctx, home, authContext{User: domain.User{ID: home.UserID}}, job.ID)
			done <- err
		}()
		must(t, wsjson.Read(ctx, owner, &routed))
		must(t, wsjson.Write(ctx, owner, protocol.Envelope{Type: protocol.TypeCloudResponse, RequestID: routed.RequestID, Payload: json.RawMessage(payload)}))
		err = <-done
		if (err == nil) != (payload == `{"ok":true}`) {
			t.Fatalf("cancel acknowledgement %s: %v", payload, err)
		}
		got, readErr := db.GetFileOperationJob(ctx, job.ID)
		must(t, readErr)
		if payload != `{"ok":true}` && got.Status != "running" {
			t.Fatal("negative acknowledgement changed job")
		}
	}
	must(t, db.UpdateFileOperationJob(ctx, job.ID, "completed", 8, 1, "", &now))
	server.failFileJob(ctx, job.ID, "rollback_required", "late timeout")
	got, err = db.GetFileOperationJob(ctx, job.ID)
	must(t, err)
	if got.Status != "completed" {
		t.Fatal("late timeout regressed successful move")
	}
}
