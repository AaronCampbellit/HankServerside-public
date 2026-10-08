package storageops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventLogParsingAndStatus(t *testing.T) {
	logDir := t.TempDir()
	stateDir := t.TempDir()

	_, err := AppendEvent(logDir, Event{
		Operation: EventOperationChecksum,
		Status:    EventStatusSuccess,
		Severity:  EventSeverityInfo,
		Message:   "checksums on",
		Details:   map[string]any{"enabled": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = AppendEvent(logDir, Event{
		Operation: EventOperationAMCheck,
		Status:    EventStatusFailed,
		Severity:  EventSeverityCritical,
		Message:   "corruption found",
		Details:   map[string]any{"corruption_detected": true},
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := ListEvents(logDir, EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events length = %d, want 2", len(events))
	}

	status, err := LoadStatus(stateDir, logDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Checksum.Enabled {
		t.Fatal("expected checksum status to show enabled")
	}
	if !status.Checksum.CorruptionDetected {
		t.Fatal("expected corruption to be highlighted")
	}
	if status.Checksum.FailureCount != 1 {
		t.Fatalf("checksum failure count = %d, want 1", status.Checksum.FailureCount)
	}
}

func TestEventRedaction(t *testing.T) {
	logDir := t.TempDir()
	_, err := AppendEvent(logDir, Event{
		Operation: EventOperationBackup,
		Status:    EventStatusFailed,
		Severity:  EventSeverityError,
		Message:   "backup failed for postgres://hank:db-secret@postgres/hank with repo1-cipher-pass=cipher-secret",
		Details: map[string]any{
			"output": "Authorization: Bearer session-token password=db-password token=raw-token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := ListEvents(logDir, EventFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	encoded := events[0].Message + " " + events[0].Details["output"].(string)
	for _, leaked := range []string{"db-secret", "cipher-secret", "session-token", "db-password", "raw-token"} {
		if contains := strings.Contains(encoded, leaked); contains {
			t.Fatalf("event leaked %q: %+v", leaked, events[0])
		}
	}
}

func TestStorageOpsFilesUsePrivatePermissions(t *testing.T) {
	stateDir := t.TempDir()
	logDir := t.TempDir()

	if _, err := SaveConfig(stateDir, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendEvent(logDir, NewEvent(EventOperationBackup, EventStatusStarted, EventSeverityInfo, "backup started")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateIntent(stateDir, "secret", Intent{Type: IntentTypeBackup}); err != nil {
		t.Fatal(err)
	}
	if err := SaveBackupSets(stateDir, []BackupSet{{Label: "backup_1"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveActiveTask(stateDir, TaskStatus{ID: "task_1", Operation: EventOperationBackup, Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{stateDir, logDir, IntentDir(stateDir)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != privateDirMode {
			t.Fatalf("%s mode = %v, want %v", path, got, privateDirMode)
		}
		if info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("%s mode missing setgid bit: %v", path, info.Mode())
		}
	}
	for _, path := range []string{
		ConfigPath(stateDir),
		EventLogPath(logDir),
		BackupInfoPath(stateDir),
		ActiveTaskPath(stateDir),
		filepath.Join(IntentDir(stateDir), "sto_"),
	} {
		if strings.HasSuffix(path, "sto_") {
			entries, err := os.ReadDir(IntentDir(stateDir))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("intent entries = %d, want 1", len(entries))
			}
			path = filepath.Join(IntentDir(stateDir), entries[0].Name())
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != privateFileMode {
			t.Fatalf("%s mode = %v, want %v", path, got, privateFileMode)
		}
	}
}

func TestStatusIncludesQueuedStorageTask(t *testing.T) {
	stateDir := t.TempDir()
	logDir := t.TempDir()
	intent, err := CreateIntent(stateDir, "secret", Intent{
		Type:       IntentTypeBackup,
		BackupType: "full",
	})
	if err != nil {
		t.Fatal(err)
	}

	status, err := LoadStatus(stateDir, logDir, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Tasks) != 1 {
		t.Fatalf("tasks length = %d, want 1", len(status.Tasks))
	}
	task := status.Tasks[0]
	if task.ID != intent.ID || task.Status != TaskStatusQueued || task.BackupType != "full" {
		t.Fatalf("task = %+v", task)
	}
}

func TestStatusUsesActiveTaskInsteadOfDuplicatingQueuedIntent(t *testing.T) {
	stateDir := t.TempDir()
	logDir := t.TempDir()
	intent, err := CreateIntent(stateDir, "secret", Intent{
		Type:       IntentTypeBackup,
		BackupType: "diff",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := timeNowForTest()
	if err := SaveActiveTask(stateDir, TaskStatus{
		ID:         intent.ID,
		Operation:  EventOperationBackup,
		Status:     TaskStatusRunning,
		Message:    "Running diff backup.",
		Step:       "Running diff backup",
		BackupType: "diff",
		StartedAt:  &now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}

	status, err := LoadStatus(stateDir, logDir, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Tasks) != 1 {
		t.Fatalf("tasks length = %d, want 1: %+v", len(status.Tasks), status.Tasks)
	}
	if status.Tasks[0].Status != TaskStatusRunning || status.Tasks[0].Step != "Running diff backup" {
		t.Fatalf("task = %+v", status.Tasks[0])
	}
}

func TestClearEventLog(t *testing.T) {
	logDir := t.TempDir()
	if _, err := AppendEvent(logDir, Event{
		Operation: EventOperationBackup,
		Status:    EventStatusStarted,
		Severity:  EventSeverityInfo,
		Message:   "backup started",
	}); err != nil {
		t.Fatal(err)
	}

	if err := ClearEventLog(logDir); err != nil {
		t.Fatal(err)
	}
	events, err := ListEvents(logDir, EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events length = %d, want 0", len(events))
	}
}

func timeNowForTest() time.Time {
	return time.Now().UTC()
}

func TestEventLogPruningKeepsRecentEntries(t *testing.T) {
	logDir := t.TempDir()
	for index := 0; index < 5; index++ {
		if _, err := AppendEvent(logDir, Event{
			Operation: EventOperationBackup,
			Status:    EventStatusStarted,
			Severity:  EventSeverityInfo,
			Message:   fmt.Sprintf("backup %d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneEventLog(logDir, 3, 0); err != nil {
		t.Fatal(err)
	}
	events, err := ListEvents(logDir, EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events length = %d, want 3", len(events))
	}
	for _, oldMessage := range []string{"backup 0", "backup 1"} {
		for _, event := range events {
			if event.Message == oldMessage {
				t.Fatalf("old event was not pruned: %+v", events)
			}
		}
	}
}
