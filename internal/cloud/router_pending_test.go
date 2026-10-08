package cloud

import (
	"context"
	"testing"
	"time"
)

func TestUnregisterAppKeepsPendingShellLifecycleUntilResponse(t *testing.T) {
	router := NewRouter()
	app := router.RegisterApp("session", "user", nil)
	_, err := router.AddPending(context.Background(), "request", "home", "shell.session.open", "term_pending", "", agentReplyBinding{homeID: "home", agentID: "agent", peer: &wsPeer{}}, app, time.Minute, func(context.Context, *pendingRequest) {})
	if err != nil {
		t.Fatal(err)
	}

	router.UnregisterApp(app.connectionID)
	pending, ok := router.ResolvePending("request")
	if !ok || pending.shellSessionID != "term_pending" {
		t.Fatal("disconnect discarded the pending shell lifecycle")
	}
}
