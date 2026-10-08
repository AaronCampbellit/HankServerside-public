package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
	"testing"
	"time"
)

func TestTransferLifecycleDuplicateFramesAndUploadDataDoNotBlock(t *testing.T) {
	for _, operation := range []string{protocol.FileTransferOperationDownload, protocol.FileTransferOperationUpload} {
		registry := newTransferRegistry()
		target := replyTestTarget()
		session, _ := registry.Create(target.homeID, target.agentID, "job", operation, "source", "/file", time.Minute)
		attempt, err := registry.BeginAttempt(session, 0, target)
		must(t, err)
		server := &Server{transfers: registry}
		envelope := protocol.Envelope{RequestID: attempt.ID, Payload: json.RawMessage(`{}`)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 40 {
				server.handleTransferReady(target, envelope)
				server.handleTransferComplete(target, envelope)
				server.handleTransferError(target, envelope)
				if operation == protocol.FileTransferOperationUpload {
					server.handleTransferData(target, envelope)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("duplicate/invalid transfer frames blocked reader")
		}
		registry.EndAttempt(attempt.ID)
	}
}

func TestTransferLifecycleOverflowNeverBlocksDataDelivery(t *testing.T) {
	registry := newTransferRegistry()
	target := replyTestTarget()
	session, _ := registry.Create(target.homeID, target.agentID, "job", protocol.FileTransferOperationDownload, "source", "/file", time.Minute)
	attempt, err := registry.BeginAttempt(session, 0, target)
	must(t, err)
	attempt.negotiated = true
	attempt.expectedEnd = 100
	for i := range cap(attempt.DataCh) {
		attempt.deliverData(transferDataFrame{Offset: int64(i), Data: []byte("a")})
	}
	done := make(chan struct{})
	go func() { attempt.deliverData(transferDataFrame{Offset: 16, Data: []byte("last")}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ended transfer kept shared reader blocked")
	}
	select {
	case <-attempt.done:
	default:
		t.Fatal("overflow did not terminate attempt")
	}
	registry.EndAttempt(attempt.ID)
	// The connection is usable for a new transfer after abandoning the old one.
	next, err := registry.BeginAttempt(session, 0, target)
	must(t, err)
	defer registry.EndAttempt(next.ID)
	next.deliverReady(transferReadyResult{Ready: protocol.FileTransferReady{Size: 3}})
	ready, failure, err := (&Server{}).waitTransferReady(context.Background(), next)
	if err != nil || failure != nil || ready.Size != 3 {
		t.Fatalf("replacement attempt = %+v, %v", ready, err)
	}
}

func TestTransferLifecycleNegotiationAndByteWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready protocol.FileTransferReady
		want  string
	}{
		{"old agent", protocol.FileTransferReady{Operation: "download", Size: 4}, "agent_update_required"},
		{"wrong offset", protocol.FileTransferReady{Operation: "download", Size: 4, Offset: 1, FlowControl: protocol.FileTransferFlowControlV1, WindowBytes: protocol.FileTransferWindowBytes}, "invalid_transfer_ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := newTransferRegistry()
			target := replyTestTarget()
			session, _ := registry.Create(target.homeID, target.agentID, "job", "download", "", "file", time.Minute)
			attempt, err := registry.BeginAttempt(session, 0, target)
			must(t, err)
			defer registry.EndAttempt(attempt.ID)
			server := &Server{transfers: registry}
			e, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, attempt.ID, target.agentID, target.homeID, tc.ready)
			server.handleTransferReady(target, e)
			_, failure, err := server.waitTransferReady(context.Background(), attempt)
			if err != nil || failure == nil || failure.Code != tc.want {
				t.Fatalf("failure=%+v, err=%v", failure, err)
			}
		})
	}
	registry := newTransferRegistry()
	target := replyTestTarget()
	session, _ := registry.Create(target.homeID, target.agentID, "job", "download", "", "file", time.Minute)
	attempt, err := registry.BeginAttempt(session, 0, target)
	must(t, err)
	defer registry.EndAttempt(attempt.ID)
	attempt.negotiated = true
	attempt.expectedEnd = protocol.FileTransferWindowBytes * 2
	for i := range 8 {
		attempt.deliverData(transferDataFrame{Offset: int64(i * protocol.FileTransferMaxChunkBytes), Data: make([]byte, protocol.FileTransferMaxChunkBytes)})
	}
	attempt.deliverData(transferDataFrame{Offset: protocol.FileTransferWindowBytes, Data: []byte("x")})
	select {
	case <-attempt.done:
		if attempt.failureResult().Code != "invalid_transfer_chunk" {
			t.Fatal("wrong error")
		}
	default:
		t.Fatal("agent exceeded granted byte window")
	}
}

func TestTransferCancellationRevokesDurableLeaseAndCachedAttempt(t *testing.T) {
	for _, initialStatus := range []string{"running", "completed"} {
		t.Run(initialStatus, func(t *testing.T) {
			db, server, home, agent, _, _ := credentialHTTPFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			job := store.FileOperationJob{ID: "cancel-transfer", HomeID: home.ID, AgentID: agent.ID, UserID: home.UserID, Operation: "download", Status: initialStatus, CreatedAt: now, UpdatedAt: now}
			must(t, db.CreateFileOperationJob(ctx, job))
			session, token := server.transfers.Create(home.ID, agent.ID, job.ID, "download", "local", "file", time.Minute)
			must(t, db.CreateFileTransfer(ctx, store.FileTransferRecord{ID: session.ID, TokenHash: hashToken(token), JobID: job.ID, HomeID: home.ID, UserID: home.UserID, AgentID: agent.ID, Operation: "download", Path: "file", Status: "active", CreatedAt: now, ExpiresAt: session.ExpiresAt}))
			// Wrong Home cannot revoke a lease.
			changed, err := db.CancelFileTransferJob(ctx, "other-home", job.ID)
			must(t, err)
			if changed {
				t.Fatal("cross-Home cancellation succeeded")
			}
			attempt, err := server.transfers.BeginAttempt(session, 0, replyTestTarget())
			must(t, err)
			// Hold the peer writer to model an unavailable connection without a live socket.
			attempt.target.peer.writeOnce.Do(func() { attempt.target.peer.writeGate = make(chan struct{}, 1) })
			attempt.target.peer.writeGate <- struct{}{}
			cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			_, err = server.cancelFileOperationJob(cancelCtx, home, authContext{User: domain.User{ID: home.UserID}}, job.ID)
			// The final read may observe the cancelled request context, but durable revocation must already have committed.
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			select {
			case <-attempt.done:
			default:
				t.Fatal("active transport not interrupted")
			}
			server.transfers.EndAttempt(attempt.ID)
			if _, err := server.transfers.BeginAttempt(session, 0, replyTestTarget()); err != ErrTransferNotFound {
				t.Fatalf("cached session reopened: %v", err)
			}
			if _, err := server.authorizeTransfer(ctx, session.ID, token, "download"); err != ErrTransferNotFound {
				t.Fatalf("cancelled bearer lease accepted: %v", err)
			}
			session.Complete(protocol.FileTransferComplete{Offset: 100, Size: 100})
			server.persistTransferStatus(ctx, session, "completed")
			record, err := db.GetFileTransfer(ctx, session.ID)
			must(t, err)
			if record.Status != "expired" || record.ExpiresAt.After(time.Now()) {
				t.Fatal("late completion revived lease")
			}
			stored, err := db.GetFileOperationJob(ctx, job.ID)
			must(t, err)
			wantStatus := "cancelled"
			if initialStatus == "completed" {
				wantStatus = "completed"
			}
			if stored.Status != wantStatus {
				t.Fatal("late completion revived job")
			}
			// Simulate restart: durable authorization must still reject the original token.
			server.transfers = newTransferRegistry()
			if _, err := server.authorizeTransfer(ctx, session.ID, token, "download"); err != ErrTransferNotFound {
				t.Fatalf("restart revived lease: %v", err)
			}
		})
	}
}
