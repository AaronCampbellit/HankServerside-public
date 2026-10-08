package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/domain"
)

func TestDisconnectAgentClosesWebSocketWithPolicyViolation(t *testing.T) {
	router := NewRouter()
	ready := make(chan struct{})
	release := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(w, request, nil)
		if err != nil {
			return
		}
		router.RegisterAgent("home_disconnect", domain.Agent{ID: "agent_disconnect"}, newWSPeer(connection), nil, AgentTypeWorker, nil)
		close(ready)
		<-release
	}))
	defer httpServer.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+httpServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	<-ready
	disconnected := make(chan bool, 1)
	go func() {
		disconnected <- router.DisconnectAgent("home_disconnect", "agent_disconnect", "credentials revoked")
	}()
	_, _, err = connection.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("close status=%d, want %d (err=%v)", got, websocket.StatusPolicyViolation, err)
	}
	if !<-disconnected {
		t.Fatal("connected agent was not disconnected")
	}
}
