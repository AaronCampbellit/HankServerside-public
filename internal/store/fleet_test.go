package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestFleetScopeConstraintsAndConcurrentAdmission(t *testing.T) {
	for _, infinite := range []bool{false, true} {
		t.Run(fmt.Sprint("infinite=", infinite), func(t *testing.T) { testFleetScopeExpiry(t, infinite) })
	}
}
func testFleetScopeExpiry(t *testing.T, infinite bool) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	u := domain.User{ID: "fleet-user", Email: "fleet@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fleet-home", "other-home"} {
		if err := db.CreateHome(ctx, domain.Home{ID: id, UserID: u.ID, Name: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []domain.Agent{{ID: "fleet-agent", HomeID: "fleet-home"}, {ID: "other-agent", HomeID: "other-home"}} {
		a.Name = a.ID
		a.Status = domain.AgentStatusOffline
		a.CreatedAt = now
		a.UpdatedAt = now
		if err := db.UpsertAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	expiry := now.Add(time.Hour)
	var expiresAt *time.Time
	if !infinite {
		expiresAt = &expiry
	}
	grant := FleetGrant{ID: "fleet-grant", HomeID: "fleet-home", UserID: u.ID, Agents: []string{"other-agent"}, Operations: protocol.FleetOperations, ExpiresAt: expiresAt}
	if db.CreateFleetGrant(ctx, grant, "hash") == nil {
		t.Fatal("cross-Home grant accepted by database")
	}
	grant.Agents = []string{"fleet-agent"}
	if err := db.CreateFleetGrant(ctx, grant, "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantByHash(ctx, "hash"); err == nil {
		t.Fatal("pending grant authenticated")
	}
	if err := db.SetFleetGrantState(ctx, grant.HomeID, grant.ID, u.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantByHash(ctx, "hash"); err != nil {
		t.Fatal("approved grant unavailable")
	}
	if err := db.DismissFleetGrant(ctx, grant.HomeID, grant.ID, u.ID); err != ErrNotFound {
		t.Fatal("active grant could be dismissed")
	}

	if _, err := db.exec(ctx, `UPDATE fleet_grants SET expires_at=created_at+interval '1 microsecond' WHERE id=?`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantByHash(ctx, "hash"); err == nil {
		t.Fatal("expired grant authenticated")
	}
	if err := db.DismissFleetGrant(ctx, "other-home", grant.ID, u.ID); err != ErrNotFound {
		t.Fatal("foreign Home dismissal accepted")
	}
	if err := db.DismissFleetGrant(ctx, grant.HomeID, grant.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	dismissed, err := db.FleetGrantDismissals(ctx, u.ID)
	if err != nil || !dismissed[grant.ID] {
		t.Fatal("dismissal did not persist")
	}
	other, err := db.FleetGrantDismissals(ctx, "other-user")
	if err != nil || other[grant.ID] {
		t.Fatal("dismissal leaked across viewers")
	}
	retained, err := db.GetFleetGrant(ctx, grant.HomeID, grant.ID)
	if err != nil || retained.State != "approved" {
		t.Fatal("dismissal mutated grant history")
	}

	if _, err := db.exec(ctx, `UPDATE fleet_grants SET expires_at=? WHERE id=?`, expiresAt, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFleetWorkspace(ctx, "workspace", grant.ID, "other-agent"); err == nil {
		t.Fatal("workspace bypassed grant target")
	}
	if err := db.CreateFleetWorkspace(ctx, "workspace", grant.ID, "fleet-agent"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, _ := db.CreateFleetJob(ctx, fmt.Sprintf("job-%d", i), grant.ID, "fleet-agent", "workspace", "hash")
			if ok {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if admitted != protocol.FleetMaxJobs {
		t.Fatalf("admitted=%d", admitted)
	}
	activeBefore, err := db.FleetAgentJobs(ctx, "fleet-agent")
	if err != nil || len(activeBefore) != protocol.FleetMaxJobs {
		t.Fatalf("active before revocation: %d, %v", len(activeBefore), err)
	}
	for _, job := range activeBefore {
		if job.CancelRequested {
			t.Fatal("valid grant cancelled job")
		}
	}
	jobs, err := db.ListFleetJobs(ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetFleetGrantState(ctx, grant.HomeID, grant.ID, u.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	active, err := db.FleetAgentJobs(ctx, "fleet-agent")
	if err != nil || len(active) != len(jobs) {
		t.Fatalf("active=%d err=%v", len(active), err)
	}
	for _, j := range active {
		if !j.CancelRequested {
			t.Fatal("revoked job cancellation intent missing")
		}
	}
	if _, err = db.FleetGrantByHash(ctx, "hash"); err == nil {
		t.Fatal("revoked grant authenticated")
	}
	if err = db.DeleteAgentForHome(ctx, "fleet-home", "fleet-agent"); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.GetFleetGrant(ctx, "fleet-home", grant.ID)
	if err != nil || len(remaining.Agents) != 0 {
		t.Fatal("deleted agent retained grant scope")
	}
	remainingJobs, err := db.ListFleetJobs(ctx, grant.ID)
	if err != nil || len(remainingJobs) != 0 {
		t.Fatal("deleted agent retained fleet metadata")
	}

}
