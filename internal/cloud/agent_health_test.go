package cloud

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestManagementCommandsRequireAdmin(t *testing.T) {
	t.Parallel()

	commands := []string{
		"host.lock",
		"host.status",
		"shell.exec",
		"wol.send",
		protocol.CommandSystemRestart,
	}
	for _, command := range commands {
		if !isManagementCommand(command) {
			t.Errorf("isManagementCommand(%q) = false, want true", command)
		}
	}
}

func TestNoteSyncSchedulingRequiresRegisteredPrimary(t *testing.T) {
	t.Parallel()

	capabilities := []string{"notes.sync"}
	if shouldScheduleNoteSync(false, capabilities) {
		t.Fatal("pre-registration or worker heartbeat scheduled note sync")
	}
	if !shouldScheduleNoteSync(true, capabilities) {
		t.Fatal("registered primary with notes.sync did not schedule note sync")
	}
	if shouldScheduleNoteSync(true, []string{"files.list"}) {
		t.Fatal("primary without notes.sync scheduled note sync")
	}
}

func TestAgentHealthTransitionsCreateDurableNotifications(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_agent_health_notifications", Email: "agent-health-notifications@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_agent_health_notifications", UserID: user.ID, Name: "Agent Health", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_health_notifications", HomeID: home.ID, Name: "Health Agent", Status: domain.AgentStatusOnline, AgentType: AgentTypeWorker, CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	connectionID := server.router.RegisterAgent(home.ID, agent, nil, nil, AgentTypeWorker, nil)
	server.checkAgentHealth(ctx)
	server.router.UnregisterAgent(home.ID, agent.ID, connectionID)
	server.checkAgentHealth(ctx)
	server.checkAgentHealth(ctx)
	connectionID = server.router.RegisterAgent(home.ID, agent, nil, nil, AgentTypeWorker, nil)
	server.checkAgentHealth(ctx)

	lowMetrics, _ := json.Marshal(protocol.HostMetrics{DiskTotalBytes: 1000, DiskUsedBytes: 950})
	server.router.UpdateAgentMetrics(home.ID, agent.ID, lowMetrics)
	server.checkAgentHealth(ctx)
	server.checkAgentHealth(ctx)
	highMetrics, _ := json.Marshal(protocol.HostMetrics{DiskTotalBytes: 1000, DiskUsedBytes: 500})
	server.router.UpdateAgentMetrics(home.ID, agent.ID, highMetrics)
	server.checkAgentHealth(ctx)
	server.router.UnregisterAgent(home.ID, agent.ID, connectionID)

	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]int{
		notificationKindAgentOffline:   1,
		notificationKindAgentRecovered: 1,
		notificationKindDiskLow:        1,
		notificationKindDiskRecovered:  1,
	}
	for _, item := range page.Items {
		wantKinds[item.EventKind]--
	}
	for kind, remaining := range wantKinds {
		if remaining != 0 {
			t.Fatalf("event kind %s remaining=%d, notifications=%#v", kind, remaining, page.Items)
		}
	}
}

func TestAgentOfflineTransitionSurvivesServerRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_agent_health_restart", Email: "agent-health-restart@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_agent_health_restart", UserID: user.ID, Name: "Agent Health Restart", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_health_restart", HomeID: home.ID, Name: "Restart Agent", Status: domain.AgentStatusOnline, AgentType: AgentTypeWorker, CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))

	first := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	first.router.RegisterAgent(home.ID, agent, nil, nil, AgentTypeWorker, nil)
	first.checkAgentHealth(ctx)

	restarted := NewServer("127.0.0.1:0", db, time.Hour, time.Second, nil)
	restarted.checkAgentHealth(ctx)
	page, err := db.ListUserNotifications(ctx, user.ID, domain.NotificationListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].EventKind != notificationKindAgentOffline {
		t.Fatalf("notifications after restart = %#v, %v", page.Items, err)
	}
}
