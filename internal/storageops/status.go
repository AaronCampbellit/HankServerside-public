package storageops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type StatusSnapshot struct {
	Config   Config         `json:"config"`
	Checksum ChecksumStatus `json:"checksum"`
	Backup   BackupStatus   `json:"backup"`
	Restore  RestoreStatus  `json:"restore"`
	Tasks    []TaskStatus   `json:"tasks"`
	Events   []Event        `json:"events"`
	Failures []Event        `json:"failures"`
}

type ChecksumStatus struct {
	Enabled            bool       `json:"enabled"`
	LastCheckAt        *time.Time `json:"last_check_at,omitempty"`
	LastAMCheckAt      *time.Time `json:"last_amcheck_at,omitempty"`
	CorruptionDetected bool       `json:"corruption_detected"`
	FailureCount       int        `json:"failure_count"`
	LastError          string     `json:"last_error,omitempty"`
}

type BackupStatus struct {
	Target           BackupTarget `json:"target"`
	LastSuccessfulAt *time.Time   `json:"last_successful_at,omitempty"`
	LastFailedAt     *time.Time   `json:"last_failed_at,omitempty"`
	LastBackupLabel  string       `json:"last_backup_label,omitempty"`
	FailureCount     int          `json:"failure_count"`
	Backups          []BackupSet  `json:"backups"`
}

type BackupSet struct {
	Label     string     `json:"label"`
	Type      string     `json:"type"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	Status    string     `json:"status,omitempty"`
	SizeBytes int64      `json:"size_bytes,omitempty"`
}

type RestoreStatus struct {
	LastTestAt           *time.Time `json:"last_test_at,omitempty"`
	LastPrimaryRestoreAt *time.Time `json:"last_primary_restore_at,omitempty"`
	LastFailedAt         *time.Time `json:"last_failed_at,omitempty"`
	PendingIntents       []Intent   `json:"pending_intents,omitempty"`
}

const (
	TaskStatusQueued  = "queued"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"
)

type TaskStatus struct {
	ID          string     `json:"id"`
	Operation   string     `json:"operation"`
	Status      string     `json:"status"`
	Message     string     `json:"message"`
	Step        string     `json:"step,omitempty"`
	BackupType  string     `json:"backup_type,omitempty"`
	BackupLabel string     `json:"backup_label,omitempty"`
	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func LoadStatus(stateDir string, logDir string, secret string) (StatusSnapshot, error) {
	cfg, err := LoadConfig(stateDir)
	if err != nil {
		cfg = DefaultConfig()
	}
	events, eventErr := ListEvents(logDir, EventFilter{Limit: 100})
	if eventErr != nil {
		return StatusSnapshot{}, eventErr
	}
	failures, failureErr := ListEvents(logDir, EventFilter{Limit: 50, FailuresOnly: true})
	if failureErr != nil {
		return StatusSnapshot{}, failureErr
	}
	backups, _ := LoadBackupSets(stateDir)
	intents, _ := ListIntents(stateDir, secret)
	for index := range intents {
		intents[index].Confirmation = ""
	}
	tasks := currentTasks(stateDir, intents)

	status := StatusSnapshot{
		Config: cfg,
		Backup: BackupStatus{
			Target:  cfg.Target,
			Backups: backups,
		},
		Tasks:    tasks,
		Events:   events,
		Failures: failures,
		Restore:  RestoreStatus{PendingIntents: intents},
	}
	for index := len(events) - 1; index >= 0; index-- {
		applyEventToStatus(&status, events[index])
	}
	return status, nil
}

func ActiveTaskPath(stateDir string) string {
	return filepath.Join(dirOrDefault(stateDir, DefaultStateDir), "active-task.json")
}

func SaveActiveTask(stateDir string, task TaskStatus) error {
	task = task.normalized()
	if err := ensurePrivateDir(dirOrDefault(stateDir, DefaultStateDir)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	path := ActiveTaskPath(stateDir)
	tmp := path + ".tmp"
	if err := writePrivateFile(tmp, data); err != nil {
		return err
	}
	return renamePrivateFile(tmp, path)
}

func LoadActiveTask(stateDir string) (TaskStatus, bool, error) {
	data, err := os.ReadFile(ActiveTaskPath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TaskStatus{}, false, nil
		}
		return TaskStatus{}, false, err
	}
	var task TaskStatus
	if err := json.Unmarshal(data, &task); err != nil {
		return TaskStatus{}, false, err
	}
	return task.normalized(), true, nil
}

func currentTasks(stateDir string, intents []Intent) []TaskStatus {
	var tasks []TaskStatus
	active, hasActive, _ := LoadActiveTask(stateDir)
	activeVisible := hasActive && active.visible(time.Now().UTC())
	if activeVisible {
		tasks = append(tasks, active)
	}
	for _, intent := range intents {
		if activeVisible && active.ID == intent.ID {
			continue
		}
		tasks = append(tasks, taskFromIntent(intent))
	}
	return tasks
}

func taskFromIntent(intent Intent) TaskStatus {
	queuedAt := intent.CreatedAt
	return TaskStatus{
		ID:          intent.ID,
		Operation:   strings.TrimSpace(intent.Type),
		Status:      TaskStatusQueued,
		Message:     taskMessage(intent.Type, intent.BackupType, "queued"),
		Step:        "Waiting for database operations worker",
		BackupType:  strings.TrimSpace(intent.BackupType),
		BackupLabel: strings.TrimSpace(intent.BackupLabel),
		QueuedAt:    &queuedAt,
		UpdatedAt:   queuedAt,
	}.normalized()
}

func (task TaskStatus) normalized() TaskStatus {
	now := time.Now().UTC()
	task.ID = strings.TrimSpace(task.ID)
	if task.ID == "" {
		task.ID = newEventID()
	}
	task.Operation = strings.TrimSpace(task.Operation)
	task.Status = strings.TrimSpace(task.Status)
	if task.Status == "" {
		task.Status = TaskStatusRunning
	}
	task.Message = strings.TrimSpace(task.Message)
	if task.Message == "" {
		task.Message = taskMessage(task.Operation, task.BackupType, task.Status)
	}
	task.Step = strings.TrimSpace(task.Step)
	task.BackupType = strings.TrimSpace(strings.ToLower(task.BackupType))
	task.BackupLabel = strings.TrimSpace(task.BackupLabel)
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	return task
}

func (task TaskStatus) visible(now time.Time) bool {
	if task.UpdatedAt.IsZero() {
		return false
	}
	switch task.Status {
	case TaskStatusQueued, TaskStatusRunning:
		return now.Sub(task.UpdatedAt) <= 6*time.Hour
	default:
		return now.Sub(task.UpdatedAt) <= 5*time.Minute
	}
}

func taskMessage(operation string, backupType string, status string) string {
	switch operation {
	case EventOperationBackup:
		kind := "backup"
		switch strings.ToLower(strings.TrimSpace(backupType)) {
		case "full":
			kind = "full backup"
		case "diff":
			kind = "diff backup"
		}
		switch status {
		case TaskStatusQueued:
			return "Queued " + kind + "."
		case TaskStatusSuccess:
			return strings.ToUpper(kind[:1]) + kind[1:] + " completed."
		case TaskStatusFailed:
			return strings.ToUpper(kind[:1]) + kind[1:] + " failed."
		default:
			return "Running " + kind + "."
		}
	case EventOperationRestoreTest:
		switch status {
		case TaskStatusQueued:
			return "Queued restore verification."
		case TaskStatusSuccess:
			return "Restore verification completed."
		case TaskStatusFailed:
			return "Restore verification failed."
		default:
			return "Running restore verification."
		}
	case EventOperationPrimaryRestore:
		switch status {
		case TaskStatusQueued:
			return "Queued primary restore."
		case TaskStatusSuccess:
			return "Primary restore completed."
		case TaskStatusFailed:
			return "Primary restore failed."
		default:
			return "Running primary restore."
		}
	default:
		return "Storage task " + status + "."
	}
}

func applyEventToStatus(status *StatusSnapshot, event Event) {
	occurredAt := event.Time
	switch event.Operation {
	case EventOperationChecksum:
		if event.Status == EventStatusSuccess {
			status.Checksum.LastCheckAt = &occurredAt
			status.Checksum.Enabled = boolFromDetails(event.Details, "enabled")
		}
		if IsFailureEvent(event) {
			status.Checksum.FailureCount++
			status.Checksum.LastError = event.Message
			if boolFromDetails(event.Details, "corruption_detected") || strings.Contains(strings.ToLower(event.Message), "corrupt") {
				status.Checksum.CorruptionDetected = true
			}
		}
	case EventOperationAMCheck:
		if event.Status == EventStatusSuccess {
			status.Checksum.LastAMCheckAt = &occurredAt
		}
		if IsFailureEvent(event) {
			status.Checksum.FailureCount++
			status.Checksum.LastError = event.Message
			if boolFromDetails(event.Details, "corruption_detected") || strings.Contains(strings.ToLower(event.Message), "corrupt") {
				status.Checksum.CorruptionDetected = true
			}
		}
	case EventOperationBackup:
		if event.Status == EventStatusSuccess {
			status.Backup.LastSuccessfulAt = &occurredAt
			status.Backup.LastBackupLabel = event.BackupLabel
		}
		if IsFailureEvent(event) {
			status.Backup.LastFailedAt = &occurredAt
			status.Backup.FailureCount++
		}
	case EventOperationRestoreTest:
		if event.Status == EventStatusSuccess {
			status.Restore.LastTestAt = &occurredAt
		}
		if IsFailureEvent(event) {
			status.Restore.LastFailedAt = &occurredAt
		}
	case EventOperationPrimaryRestore:
		if event.Status == EventStatusSuccess {
			status.Restore.LastPrimaryRestoreAt = &occurredAt
		}
		if IsFailureEvent(event) {
			status.Restore.LastFailedAt = &occurredAt
		}
	}
}

func BackupInfoPath(stateDir string) string {
	return filepath.Join(dirOrDefault(stateDir, DefaultStateDir), "backups.json")
}

func SaveBackupSets(stateDir string, backups []BackupSet) error {
	if err := ensurePrivateDir(dirOrDefault(stateDir, DefaultStateDir)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(backups, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(BackupInfoPath(stateDir), data)
}

func LoadBackupSets(stateDir string) ([]BackupSet, error) {
	data, err := os.ReadFile(BackupInfoPath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var backups []BackupSet
	if err := json.Unmarshal(data, &backups); err != nil {
		return nil, err
	}
	return backups, nil
}

func boolFromDetails(details map[string]any, key string) bool {
	if details == nil {
		return false
	}
	value, ok := details[key]
	if !ok {
		return false
	}
	boolValue, _ := value.(bool)
	return boolValue
}
