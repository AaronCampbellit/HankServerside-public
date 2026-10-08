package store

import (
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestNotificationSourceTransitionsAreDurableRetryableAndEdgeTriggered(t *testing.T) {
	t.Parallel()
	ctx, db, _, home, now := notificationStoreFixture(t, "source-transitions")
	defer db.Close()
	base := domain.NotificationSourceObservation{
		SourceKey: "agent-connectivity:" + home.ID + ":agent_1", HomeID: home.ID, ResourceID: "agent_1",
		State: "online", EventKind: "agent.recovered", Outcome: "recovered", Severity: "info", OccurredAt: now,
	}
	transitioned, err := db.ObserveNotificationSource(ctx, base)
	if err != nil || transitioned {
		t.Fatalf("initial observation transitioned=%v err=%v", transitioned, err)
	}
	base.State, base.EventKind, base.Outcome, base.Severity, base.OccurredAt = "offline", "agent.offline", "offline", "warning", now.Add(time.Minute)
	transitioned, err = db.ObserveNotificationSource(ctx, base)
	if err != nil || !transitioned {
		t.Fatalf("offline observation transitioned=%v err=%v", transitioned, err)
	}
	if transitioned, err = db.ObserveNotificationSource(ctx, base); err != nil || transitioned {
		t.Fatalf("repeated offline observation transitioned=%v err=%v", transitioned, err)
	}
	pending, err := db.ListPendingNotificationSourceEvents(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].EventKind != "agent.offline" {
		t.Fatalf("pending = %#v err=%v", pending, err)
	}
	if err := db.MarkNotificationSourceEventEmitted(ctx, pending[0].ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pending, err = db.ListPendingNotificationSourceEvents(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after mark = %#v err=%v", pending, err)
	}
}
