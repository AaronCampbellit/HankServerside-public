package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestLifecyclePruneSummaryIncludesDesktopRetention(t *testing.T) {
	summary := LifecyclePruneSummary{DesktopJoinCredentialsDeleted: 2, DesktopSessionEventsDeleted: 1, DesktopSessionsDeleted: 1, MCPAttachmentUploadsExpired: 2}
	if summary.Empty() {
		t.Fatal("desktop retention counts were ignored")
	}
}

func TestLifecyclePruneIncludesMCPAttachmentUploads(t *testing.T) {
	source, err := os.ReadFile("production_state.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, statement := range []string{"mcp_attachment_uploads_expired", "mcp_attachment_uploads_deleted", "mcp_note_attachment_uploads"} {
		if !strings.Contains(text, statement) {
			t.Fatalf("lifecycle prune is missing %q", statement)
		}
	}
}

func TestLifecyclePruneExecutesAsOneTransaction(t *testing.T) {
	source, err := os.ReadFile("production_state.go")
	if err != nil {
		t.Fatal(err)
	}
	function := string(source)
	start := strings.Index(function, "func (s *Store) PruneLifecycleWithSummary")
	end := strings.Index(function[start:], "\nfunc scanAppWebSocketTicket")
	if start < 0 || end < 0 {
		t.Fatal("PruneLifecycleWithSummary source not found")
	}
	function = function[start : start+end]
	if !strings.Contains(function, "s.beginTx") || !strings.Contains(function, "tx.ExecContext") || !strings.Contains(function, "tx.Commit") || strings.Contains(function, "s.exec(ctx, statement.sql") {
		t.Fatal("lifecycle retention is not one transaction")
	}
}

func TestUpdateFileOperationJobMonotonicDoesNotRegressTerminalState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_file_job_monotonic", Email: "file-job-monotonic@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	home := domain.Home{ID: "home_file_job_monotonic", UserID: user.ID, Name: "File Job Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	job := FileOperationJob{
		ID:                  "filejob_monotonic",
		HomeID:              home.ID,
		UserID:              user.ID,
		Operation:           "move",
		SourceID:            "primary",
		DestinationSourceID: "secondary",
		FromPath:            "/source",
		ToPath:              "/destination",
		Status:              "running",
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := db.CreateFileOperationJob(ctx, job); err != nil {
		t.Fatalf("CreateFileOperationJob: %v", err)
	}
	completedAt := now.Add(time.Second)
	if err := db.UpdateFileOperationJob(ctx, job.ID, "completed", 10, 1, "", &completedAt); err != nil {
		t.Fatalf("complete file job: %v", err)
	}

	updated, err := db.UpdateFileOperationJobMonotonic(ctx, job.ID, "running", 0, 0, "", nil)
	if err != nil {
		t.Fatalf("UpdateFileOperationJobMonotonic: %v", err)
	}
	if updated {
		t.Fatal("non-terminal update changed a completed file job")
	}
	failedAt := now.Add(2 * time.Second)
	updated, err = db.UpdateFileOperationJobMonotonic(ctx, job.ID, "failed", 0, 0, "late failure", &failedAt)
	if err != nil {
		t.Fatalf("UpdateFileOperationJobMonotonic terminal conflict: %v", err)
	}
	if updated {
		t.Fatal("later terminal update changed the first terminal file-job state")
	}

	got, err := db.GetFileOperationJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetFileOperationJob: %v", err)
	}
	if got.Status != "completed" || got.BytesDone != 10 || got.FilesDone != 1 || got.CompletedAt == nil {
		t.Fatalf("file job regressed after late running response: %#v", got)
	}
}

func TestDeleteTerminalFileOperationJobsKeepsActiveAndOtherHomeJobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_file_job_clear", Email: "file-job-clear@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, home := range []domain.Home{
		{ID: "home_file_job_clear", UserID: user.ID, Name: "Clear Home", CreatedAt: now, UpdatedAt: now},
		{ID: "home_file_job_keep", UserID: user.ID, Name: "Keep Home", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.CreateHome(ctx, home); err != nil {
			t.Fatalf("CreateHome %s: %v", home.ID, err)
		}
	}
	for _, job := range []FileOperationJob{
		{ID: "filejob_clear_done", HomeID: "home_file_job_clear", UserID: user.ID, Operation: "upload", FromPath: "/done", Status: "completed", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_clear_active", HomeID: "home_file_job_clear", UserID: user.ID, Operation: "upload", FromPath: "/active", Status: "running", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_clear_rollback", HomeID: "home_file_job_clear", UserID: user.ID, Operation: "move", FromPath: "/source", ToPath: "/destination", Status: "rollback_required", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_clear_old_transfer", HomeID: "home_file_job_clear", UserID: user.ID, Operation: "download", FromPath: "/old", Status: "rollback_required", CreatedAt: now, UpdatedAt: now},
		{ID: "filejob_other_done", HomeID: "home_file_job_keep", UserID: user.ID, Operation: "upload", FromPath: "/other", Status: "completed", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.CreateFileOperationJob(ctx, job); err != nil {
			t.Fatalf("CreateFileOperationJob %s: %v", job.ID, err)
		}
	}

	cleared, err := db.DeleteTerminalFileOperationJobs(ctx, "home_file_job_clear")
	if err != nil {
		t.Fatalf("DeleteTerminalFileOperationJobs: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("cleared = %d, want 2", cleared)
	}
	for _, id := range []string{"filejob_clear_done", "filejob_clear_old_transfer"} {
		if _, err := db.GetFileOperationJob(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cleared job %s error = %v, want ErrNotFound", id, err)
		}
	}
	for _, id := range []string{"filejob_clear_active", "filejob_clear_rollback", "filejob_other_done"} {
		if _, err := db.GetFileOperationJob(ctx, id); err != nil {
			t.Fatalf("preserved job %s: %v", id, err)
		}
	}
}
