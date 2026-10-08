package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestExecutionAttachmentStageSurvivesRestartAndScopesOwner(t *testing.T) {
	s, task := executionTaskFixture(t)
	s.ConfigureNoteAttachmentStorage(t.TempDir())
	// Upgrade the old owner-only staging directory without exposing bytes to
	// other users; the backup worker relies on the private attachment group.
	must(t, os.Mkdir(filepath.Join(s.noteAttachmentRoot, ".assistant-staging"), 0700))
	ctx := context.Background()
	content := "synthetic stage bytes"
	sum := sha256.Sum256([]byte(content))
	metadata := domain.AssistantStage{ClientAttachmentID: "client1", Filename: "example.txt", ContentType: "text/plain", SizeBytes: int64(len(content)), ChecksumSHA256: hex.EncodeToString(sum[:])}
	if _, err := s.stageAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, metadata, strings.NewReader("partial")); err == nil {
		t.Fatal("partial accepted")
	}
	stage, err := s.stageAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, metadata, strings.NewReader(content))
	must(t, err)
	replay, err := s.stageAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, metadata, strings.NewReader(content))
	must(t, err)
	if stage.ID != replay.ID {
		t.Fatal("retry created duplicate binding")
	}
	metadata.Filename = "changed.txt"
	if _, err = s.stageAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, metadata, strings.NewReader(content)); err == nil {
		t.Fatal("changed metadata replay accepted")
	}
	restarted := NewServer("127.0.0.1:0", s.store, time.Hour, time.Second, s.logger)
	defer restarted.Shutdown(ctx)
	restarted.ConfigureNoteAttachmentStorage(s.noteAttachmentRoot)
	file, err := restarted.openAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, stage.ID)
	must(t, err)
	data, err := io.ReadAll(file)
	file.Close()
	must(t, err)
	if string(data) != content {
		t.Fatal("restart lost staged bytes")
	}
	if _, err = s.openAssistantAttachment(ctx, task.HomeID, "foreign", task.SessionID, stage.ID); err == nil {
		t.Fatal("foreign owner read stage")
	}
	stagePath := filepath.Join(s.noteAttachmentRoot, ".assistant-staging", stage.StorageKey)
	dirInfo, err := os.Stat(filepath.Dir(stagePath))
	must(t, err)
	fileInfo, err := os.Stat(stagePath)
	must(t, err)
	if dirInfo.Mode()&(os.ModePerm|os.ModeSetgid) != noteAttachmentDirMode || fileInfo.Mode().Perm() != noteAttachmentFileMode {
		t.Fatal("staged attachments are not accessible to the private backup group")
	}
	old := time.Now().Add(-48 * time.Hour)
	must(t, os.Chtimes(stagePath, old, old))
	must(t, s.pruneNoteAttachmentFiles(ctx, time.Now(), time.Hour))
	if _, err = os.Stat(stagePath); err != nil {
		t.Fatal("legacy note cleanup removed task staging")
	}
	if _, err = s.resolveNoteAttachmentPath(filepath.Join(".assistant-staging", stage.StorageKey), false); err == nil {
		t.Fatal("legacy note path exposed task staging")
	}
	must(t, s.store.DeleteAssistantSession(ctx, task.SessionID))
	if _, err = s.openAssistantAttachment(ctx, task.HomeID, task.UserID, task.SessionID, stage.ID); err == nil {
		t.Fatal("deleted session retained stage access")
	}
}
func TestExecutionAttachmentStageRejectsEscapingDirectory(t *testing.T) {
	s, task := executionTaskFixture(t)
	root := t.TempDir()
	s.ConfigureNoteAttachmentStorage(root)
	must(t, os.Symlink(t.TempDir(), filepath.Join(root, ".assistant-staging")))
	sum := sha256.Sum256([]byte("x"))
	_, err := s.stageAssistantAttachment(context.Background(), task.HomeID, task.UserID, task.SessionID, domain.AssistantStage{ClientAttachmentID: "x", Filename: "x.txt", ContentType: "text/plain", SizeBytes: 1, ChecksumSHA256: hex.EncodeToString(sum[:])}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("escaping staging directory accepted")
	}
}

func TestAssistantStagingCapacityIncludesOrphans(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	must(t, err)
	defer root.Close()
	file, err := root.Create("orphan")
	must(t, err)
	must(t, file.Truncate(assistantStageBudget))
	must(t, file.Close())
	if checkAssistantStageBudget(root, "new", 1) == nil {
		t.Fatal("orphan bytes bypassed capacity limit")
	}
}
