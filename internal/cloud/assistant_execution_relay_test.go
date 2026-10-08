package cloud

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAppRelayRejectsInternalAssistantOperations(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	user := domain.User{ID: "operation-owner", Email: "operation@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, domain.Home{ID: "operation-home", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "operation-session", UserID: user.ID, TokenHash: hashToken("operation-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	conn, _, err := appWebSocketDial(ctx, httpServer, "operation-token")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	for _, command := range []string{"assistant.operation_execute", "assistant.operation_status", "assistant.operation_stage"} {
		envelope, err := protocol.NewEnvelope(protocol.TypeAppCommand, command, "forged-agent", "operation-home", protocol.RoutedCommand{Command: command, Body: json.RawMessage(`{"identity":{"user_id":"forged-user"}}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := wsjson.Write(ctx, conn, envelope); err != nil {
			t.Fatal(err)
		}
		var response protocol.Envelope
		if err := wsjson.Read(ctx, conn, &response); err != nil {
			t.Fatal(err)
		}
		if response.Type != protocol.TypeAppError {
			t.Fatalf("internal operation relayed: %s", response.Type)
		}
		if response.Error == nil || response.Error.Code != "permission_denied" {
			t.Fatalf("operation did not fail at internal boundary: %#v", response.Error)
		}
	}
}
