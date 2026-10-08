package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestFileJobDismissRequiresExactAdminReview(t *testing.T) {
	db, server, home, _, token, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := store.FileOperationJob{ID: "dismiss-move", HomeID: home.ID, UserID: home.UserID, Operation: "move", Status: "rollback_required", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateFileOperationJob(ctx, job))
	other := job
	other.ID = "other-move"
	must(t, db.CreateFileOperationJob(ctx, other))
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	path := "/v1/home/file-jobs/" + job.ID + "/dismiss"
	headers := map[string]string{"Authorization": "Bearer " + token}
	request := func(url string, body fileJobDismissRequest, hdr map[string]string, want int) []byte {
		t.Helper()
		response, data := credentialRequest(t, httpServer.URL, http.MethodPost, url, body, hdr)
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d: %s", response.StatusCode, want, data)
		}
		return data
	}
	body := fileJobDismissRequest{ExpectedUpdatedAt: now, RequestActionToken: true}
	request(path, body, nil, http.StatusUnauthorized)
	request(path, body, map[string]string{"Cookie": sessionCookieName + "=" + token}, http.StatusForbidden)
	member := domain.User{ID: "dismiss-member", Email: "dismiss@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, member))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "dismiss-session", UserID: member.ID, TokenHash: hashToken("dismiss-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	request(path, body, map[string]string{"Authorization": "Bearer dismiss-member-token"}, http.StatusForbidden)
	stale := body
	stale.ExpectedUpdatedAt = now.Add(-time.Second)
	request(path, stale, headers, http.StatusConflict)
	var review struct {
		Token string `json:"admin_action_token"`
	}
	must(t, json.Unmarshal(request(path, body, headers, http.StatusCreated), &review))
	body.RequestActionToken = false
	body.AdminActionToken = review.Token
	body.Confirmation = "REMOVE HISTORY"
	request("/v1/home/file-jobs/other-move/dismiss", body, headers, http.StatusForbidden)
	body.RequestActionToken = true
	must(t, json.Unmarshal(request(path, body, headers, http.StatusCreated), &review))
	body.RequestActionToken = false
	body.AdminActionToken = review.Token
	body.Confirmation = ""
	request(path, body, headers, http.StatusBadRequest)
	body.Confirmation = "REMOVE HISTORY"
	request(path, body, headers, http.StatusOK)
	if _, err := db.GetFileOperationJob(ctx, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("job remains: %v", err)
	}
	request(path, body, headers, http.StatusNotFound)
	// A recreated row cannot reuse the consumed token.
	must(t, db.CreateFileOperationJob(ctx, job))
	request(path, body, headers, http.StatusForbidden)
	if _, err := db.GetFileOperationJob(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	must(t, err)
	response := httptest.NewRecorder()
	server.handleFileJobDismiss(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload)), domain.Home{ID: "wrong-home"}, authContext{User: domain.User{ID: home.UserID}}, domain.HomeMembership{Role: domain.HomeRoleAdmin}, job.ID)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-home dismissal status=%d", response.Code)
	}

}

func TestReviewedFileJobRemovalPreservesActiveAndChangedJobs(t *testing.T) {
	db, _, home, _, _, _ := credentialHTTPFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, status := range []string{"queued", "running", "completed", "failed", "cancelled", "rolled_back", "rollback_required"} {
		job := store.FileOperationJob{ID: "dismiss-" + status, HomeID: home.ID, UserID: home.UserID, Operation: "move", Status: status, CreatedAt: now, UpdatedAt: now}
		must(t, db.CreateFileOperationJob(ctx, job))
		for _, scope := range []struct {
			home    string
			updated time.Time
		}{{"wrong-home", now}, {home.ID, now.Add(time.Second)}} {
			changed, err := db.DeleteReviewedFileOperationJob(ctx, scope.home, job.ID, scope.updated)
			must(t, err)
			if changed {
				t.Fatal("unsafe removal accepted")
			}
		}
		changed, err := db.DeleteReviewedFileOperationJob(ctx, home.ID, job.ID, now)
		must(t, err)
		if changed != (status == "rollback_required") {
			t.Fatalf("status %s removal=%v", status, changed)
		}
	}
	job := store.FileOperationJob{ID: "dismiss-upload", HomeID: home.ID, UserID: home.UserID, Operation: "upload", Status: "rollback_required", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateFileOperationJob(ctx, job))
	changed, err := db.DeleteReviewedFileOperationJob(ctx, home.ID, job.ID, now)
	must(t, err)
	if changed {
		t.Fatal("wrong operation removed")
	}
}
