package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestFileJobOwnerAssignmentRequiresExactAdminReview(t *testing.T) {
	db, server, home, agent, token, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := store.FileOperationJob{ID: "historical-move", HomeID: home.ID, UserID: home.UserID, Operation: "move", Status: "rollback_required", FromPath: "/original", ToPath: "/copy", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateFileOperationJob(ctx, job))
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	path := "/v1/home/file-jobs/" + job.ID + "/owner"
	request := func(body fileJobOwnerRequest, headers map[string]string, want int) []byte {
		t.Helper()
		response, data := credentialRequest(t, httpServer.URL, http.MethodPost, path, body, headers)
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d: %s", response.StatusCode, want, data)
		}
		return data
	}
	body := fileJobOwnerRequest{AgentID: agent.ID, ExpectedUpdatedAt: job.UpdatedAt, RequestActionToken: true}
	headers := map[string]string{"Authorization": "Bearer " + token}
	request(body, nil, http.StatusUnauthorized)
	request(body, map[string]string{"Cookie": sessionCookieName + "=" + token}, http.StatusForbidden)
	// A member with Files access still cannot issue an assignment token.
	member := domain.User{ID: "owner-member", Email: "owner-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, member))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "owner-member-session", UserID: member.ID, TokenHash: hashToken("owner-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	request(body, map[string]string{"Authorization": "Bearer owner-member-token"}, http.StatusForbidden)
	// The selected machine may be offline: attribution performs no agent command.
	var reviewed struct {
		Token string `json:"admin_action_token"`
	}
	must(t, json.Unmarshal(request(body, headers, http.StatusCreated), &reviewed))
	changedSelection := body
	changedSelection.RequestActionToken = false
	changedSelection.AdminActionToken = reviewed.Token
	changedSelection.Confirmation = "CONFIRM OWNER"
	another := agent
	another.ID = "other-owning-agent"
	must(t, db.UpsertAgent(ctx, another))
	changedSelection.AgentID = another.ID
	request(changedSelection, headers, http.StatusForbidden)
	must(t, json.Unmarshal(request(body, headers, http.StatusCreated), &reviewed))
	body.RequestActionToken = false
	body.AdminActionToken = reviewed.Token
	request(body, headers, http.StatusBadRequest)
	body.Confirmation = "CONFIRM OWNER"
	request(body, headers, http.StatusOK)
	saved, err := db.GetFileOperationJob(ctx, job.ID)
	must(t, err)
	if saved.AgentID != agent.ID || saved.Status != job.Status || saved.FromPath != job.FromPath || saved.ToPath != job.ToPath {
		t.Fatalf("assignment changed recovery state: %+v", saved)
	}
	request(body, headers, http.StatusConflict)
	body.ExpectedUpdatedAt = saved.UpdatedAt
	body.RequestActionToken = true
	request(body, headers, http.StatusConflict)
}

func TestFileJobOwnerAssignmentCASRejectsUnsafeHistory(t *testing.T) {
	db, _, home, agent, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, status := range []string{"queued", "running", "completed", "rolled_back", "failed", "cancelled", "rollback_required"} {
		job := store.FileOperationJob{ID: "owner-" + status, HomeID: home.ID, UserID: home.UserID, Operation: "move", Status: status, CreatedAt: now, UpdatedAt: now}
		must(t, db.CreateFileOperationJob(ctx, job))
		for _, scope := range []struct {
			homeID, agentID string
			updated         time.Time
		}{{"wrong-home", agent.ID, now}, {home.ID, "missing-agent", now}, {home.ID, agent.ID, now.Add(time.Second)}} {
			changed, err := db.AssignUnknownFileJobOwner(ctx, scope.homeID, job.ID, scope.agentID, status, scope.updated)
			must(t, err)
			if changed {
				t.Fatal("unsafe assignment accepted")
			}
		}
		changed, err := db.AssignUnknownFileJobOwner(ctx, home.ID, job.ID, agent.ID, status, now)
		must(t, err)
		if changed != fileJobOwnerAssignable(status) {
			t.Fatalf("status %s assignment=%v", status, changed)
		}
		if changed {
			again, err := db.AssignUnknownFileJobOwner(ctx, home.ID, job.ID, agent.ID, status, now)
			must(t, err)
			if again {
				t.Fatal("existing owner reassigned")
			}
		}
	}
}

func TestFileJobOwnerQueuePaginatesBeyondRecentHistory(t *testing.T) {
	db, server, home, _, token, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 53; i++ {
		must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{ID: fmt.Sprintf("unknown-%03d", i), HomeID: home.ID, UserID: home.UserID, Operation: "move", Status: "failed", CreatedAt: now, UpdatedAt: now}))
	}
	for i := 0; i < 21; i++ {
		must(t, db.CreateFileOperationJob(ctx, store.FileOperationJob{ID: fmt.Sprintf("recent-%03d", i), HomeID: home.ID, UserID: home.UserID, Operation: "upload", Status: "completed", CreatedAt: now, UpdatedAt: now.Add(time.Hour)}))
	}
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	var page struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
		Next string `json:"next_cursor"`
	}
	response, data := credentialRequest(t, httpServer.URL, http.MethodGet, "/v1/home/file-jobs?owner=unknown", nil, map[string]string{"Authorization": "Bearer " + token})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("queue status=%d", response.StatusCode)
	}
	must(t, json.Unmarshal(data, &page))
	if len(page.Jobs) != 50 || page.Next != "unknown-049" {
		t.Fatalf("first page: %+v", page)
	}
	next, after, err := db.ListUnknownFileJobOwners(ctx, home.ID, page.Next)
	must(t, err)
	if len(next) != 3 || after != "" || next[0].ID != "unknown-050" {
		t.Fatal("older owner reviews unreachable")
	}
	foreign, _, err := db.ListUnknownFileJobOwners(ctx, "wrong-home", "")
	must(t, err)
	if len(foreign) != 0 {
		t.Fatal("queue crossed Home boundary")
	}
}
