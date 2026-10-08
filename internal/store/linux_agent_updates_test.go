package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestLinuxAgentReleaseRolloutEligibilityPinsAndTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_linux_rollout", Email: "linux-rollout@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_rollout", UserID: user.ID, Name: "Linux Rollout", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}

	addAgent := func(id, mode, version, status string, revoked bool) {
		agent := domain.Agent{ID: id, HomeID: home.ID, Name: id, Status: status, AgentType: "worker", Platform: "linux", Architecture: "amd64", AppVersion: version, InstallationMode: mode, Capabilities: []string{"packages.install", "services.restart"}, CreatedAt: now, UpdatedAt: now}
		if err := db.UpsertAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		token := domain.AgentToken{ID: "tok_" + id, HomeID: home.ID, AgentID: id, TokenHash: strings.Repeat(id[len(id)-1:], 64), CreatedAt: now}
		if revoked {
			token.RevokedAt = &now
		}
		if err := db.CreateAgentToken(ctx, token); err != nil {
			t.Fatal(err)
		}
	}
	addAgent("agent_system", "system", "0.2.0", "online", false)
	addAgent("agent_offline", "system", "0.2.0", "offline", false)
	addAgent("agent_user", "user", "0.2.0", "online", false)
	addAgent("agent_current", "system", "0.3.0", "online", false)
	addAgent("agent_revoked", "system", "0.2.0", "online", true)

	release := domain.LinuxAgentRelease{ID: "lrel_030", Version: "0.3.0", ManifestSHA256: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), ManifestURL: "https://hankdemo.campbellservers.com/install/linux-release/release.json", State: "hosted", CreatedAt: now}
	if err := db.RegisterLinuxAgentRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterLinuxAgentRelease(ctx, release); err != nil {
		t.Fatalf("idempotent register: %v", err)
	}
	conflict := release
	conflict.ManifestSHA256 = strings.Repeat("c", 64)
	if err := db.RegisterLinuxAgentRelease(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("release collision = %v", err)
	}

	if err := db.SetLinuxAgentVersionPin(ctx, domain.LinuxAgentVersionPin{HomeID: home.ID, AgentID: "agent_system", Version: "0.3.0", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rollout, err := db.ActivateLinuxAgentRollout(ctx, "0.3.0", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.ActivateLinuxAgentRollout(ctx, "0.3.0", now, 15*time.Minute)
	if err != nil || again.ID != rollout.ID {
		t.Fatalf("idempotent activation = %#v, %v", again, err)
	}
	assignments, err := db.ListLinuxAgentRolloutAssignments(ctx, rollout.ID, home.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, assignment := range assignments {
		states[assignment.AgentID] = assignment.State
	}
	for agentID, want := range map[string]string{"agent_system": "pending", "agent_offline": "waiting_online", "agent_user": "manual_update_required", "agent_current": "healthy"} {
		if states[agentID] != want {
			t.Fatalf("%s state = %q, want %q (all=%v)", agentID, states[agentID], want, states)
		}
	}
	if _, ok := states["agent_revoked"]; ok {
		t.Fatal("revoked agent received an assignment")
	}

	var systemAssignment domain.LinuxAgentUpdateAssignment
	for _, assignment := range assignments {
		if assignment.AgentID == "agent_system" {
			systemAssignment = assignment
		}
	}
	changed, err := db.TransitionLinuxAgentAssignment(ctx, systemAssignment.ID, []string{"pending"}, "downloading", "", now.Add(time.Second))
	if err != nil || !changed {
		t.Fatalf("transition = %v, %v", changed, err)
	}
	changed, err = db.TransitionLinuxAgentAssignment(ctx, systemAssignment.ID, []string{"pending"}, "failed", "download_failed", now.Add(2*time.Second))
	if err != nil || changed {
		t.Fatalf("stale transition = %v, %v", changed, err)
	}
	changed, err = db.TransitionLinuxAgentAssignmentForAgent(ctx, systemAssignment.ID, rollout.ID, home.ID, "agent_offline", []string{"downloading"}, "installing", "", now.Add(3*time.Second))
	if err != nil || changed {
		t.Fatalf("cross-agent transition = %v, %v", changed, err)
	}
	changed, err = db.TransitionLinuxAgentAssignmentForAgent(ctx, systemAssignment.ID, rollout.ID, home.ID, "agent_system", []string{"downloading"}, "reconnecting", "", now.Add(4*time.Second))
	if err != nil || !changed {
		t.Fatalf("authenticated transition = %v, %v", changed, err)
	}
	acknowledged, err := db.AcknowledgeLinuxAgentVersion(ctx, home.ID, "agent_system", systemAssignment.ID, "9.9.9", now.Add(5*time.Second))
	if err != nil || acknowledged {
		t.Fatalf("wrong-version acknowledgement = %v, %v", acknowledged, err)
	}
	acknowledged, err = db.AcknowledgeLinuxAgentVersion(ctx, home.ID, "agent_system", systemAssignment.ID, "0.3.0", now.Add(6*time.Second))
	if err != nil || !acknowledged {
		t.Fatalf("valid acknowledgement = %v, %v", acknowledged, err)
	}

	nextRelease := domain.LinuxAgentRelease{ID: "lrel_031", Version: "0.3.1", ManifestSHA256: strings.Repeat("d", 64), SourceCommit: strings.Repeat("e", 40), ManifestURL: "https://hankdemo.campbellservers.com/install/linux-release/release.json", State: "hosted", CreatedAt: now.Add(7 * time.Second)}
	if err := db.RegisterLinuxAgentRelease(ctx, nextRelease); err != nil {
		t.Fatal(err)
	}
	nextRollout, err := db.ActivateLinuxAgentRollout(ctx, "0.3.1", now.Add(8*time.Second), 0)
	if err != nil {
		t.Fatalf("supersede active rollout: %v", err)
	}
	if nextRollout.ID == rollout.ID || nextRollout.Version != "0.3.1" {
		t.Fatalf("next rollout = %#v", nextRollout)
	}
	var oldState string
	var completedAt time.Time
	if err := db.queryRow(ctx, `SELECT state, completed_at FROM linux_agent_rollouts WHERE id = ?`, rollout.ID).Scan(&oldState, &completedAt); err != nil {
		t.Fatal(err)
	}
	if oldState != "cancelled" || !completedAt.Equal(now.Add(8*time.Second)) {
		t.Fatalf("superseded rollout state = %q completed=%s", oldState, completedAt)
	}
	var oldOfflineState, oldError string
	if err := db.queryRow(ctx, `SELECT state, error_code FROM linux_agent_update_assignments WHERE rollout_id = ? AND agent_id = ?`, rollout.ID, "agent_offline").Scan(&oldOfflineState, &oldError); err != nil {
		t.Fatal(err)
	}
	if oldOfflineState != "cancelled" || oldError != "superseded" {
		t.Fatalf("superseded assignment = %q error=%q", oldOfflineState, oldError)
	}
}

func TestUpdateAgentRuntimeMetadataInfersLegacySystemMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_runtime", Email: "runtime@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_runtime", UserID: user.ID, Name: "Runtime", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAgent(ctx, domain.Agent{ID: "agent_runtime", HomeID: home.ID, Name: "Runtime", Status: "online", AgentType: "worker", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAgentRuntimeMetadata(ctx, home.ID, "agent_runtime", "linux", "amd64", "0.2.0", "", []string{"packages.install", "services.restart"}, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetAgentByID(ctx, "agent_runtime")
	if err != nil || loaded.InstallationMode != "system" {
		t.Fatalf("metadata = %#v, %v", loaded, err)
	}
}
