package storageops

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type ExecRunner struct {
	Env []string
	Dir string
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	cmd.Env = append(os.Environ(), r.Env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return strings.TrimSpace(output.String()), err
}

type WorkerOptions struct {
	StateDir             string
	LogDir               string
	IntentSecret         string
	RepoCipherPass       string
	DatabaseURL          string
	Stanza               string
	PGDataPath           string
	RestoreDataPath      string
	RestoreDatabaseURL   string
	NoteAttachmentDir    string
	AttachmentRestoreDir string
	ComposeFile          string
	Runner               Runner
}

type Worker struct {
	service                *Service
	runner                 Runner
	repoCipherPass         string
	databaseURL            string
	stanza                 string
	pgDataPath             string
	restoreDataPath        string
	restoreDatabaseURL     string
	noteAttachmentDir      string
	attachmentRestoreDir   string
	composeFile            string
	prepareRestoreDatabase func(context.Context) error
	lastCronRuns           map[string]string
}

type liveAttachmentFile struct {
	ID                string `json:"id"`
	StorageKey        string `json:"storage_key"`
	SizeBytes         int64  `json:"size_bytes"`
	PreviewStorageKey string `json:"preview_storage_key"`
	PreviewSizeBytes  int64  `json:"preview_size_bytes"`
}

func NewWorker(options WorkerOptions) *Worker {
	repoCipherPass := strings.TrimSpace(options.RepoCipherPass)
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{Env: pgBackRestEnv(repoCipherPass)}
	}
	stanza := strings.TrimSpace(options.Stanza)
	if stanza == "" {
		stanza = "hank"
	}
	pgDataPath := strings.TrimSpace(options.PGDataPath)
	if pgDataPath == "" {
		pgDataPath = "/var/lib/postgresql/data"
	}
	restoreDataPath := strings.TrimSpace(options.RestoreDataPath)
	if restoreDataPath == "" {
		restoreDataPath = "/var/lib/postgresql/restore"
	}
	restoreDatabaseURL := strings.TrimSpace(options.RestoreDatabaseURL)
	if restoreDatabaseURL == "" {
		restoreDatabaseURL = "postgres://hank:hank@postgres-restore:5432/hank?sslmode=disable"
	}
	noteAttachmentDir := strings.TrimSpace(options.NoteAttachmentDir)
	if noteAttachmentDir == "" {
		noteAttachmentDir = "/var/lib/hank/note-attachments"
	}
	attachmentRestoreDir := strings.TrimSpace(options.AttachmentRestoreDir)
	if attachmentRestoreDir == "" {
		attachmentRestoreDir = "/var/lib/hank/note-attachments-restore"
	}
	composeFile := strings.TrimSpace(options.ComposeFile)
	if composeFile == "" {
		composeFile = "/workspace/docker-compose.yml"
	}
	return &Worker{
		service:              NewService(options.StateDir, options.LogDir, options.IntentSecret),
		runner:               runner,
		repoCipherPass:       repoCipherPass,
		databaseURL:          strings.TrimSpace(options.DatabaseURL),
		stanza:               stanza,
		pgDataPath:           pgDataPath,
		restoreDataPath:      restoreDataPath,
		restoreDatabaseURL:   restoreDatabaseURL,
		noteAttachmentDir:    noteAttachmentDir,
		attachmentRestoreDir: attachmentRestoreDir,
		composeFile:          composeFile,
		lastCronRuns:         make(map[string]string),
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	if err := w.RecoverPrimaryRestore(ctx); err != nil {
		return err
	}
	if _, err := w.service.Config(); err != nil {
		if _, saveErr := w.service.SaveConfig(DefaultConfig()); saveErr != nil {
			return saveErr
		}
	}
	if err := w.processIntents(ctx); err != nil {
		return err
	}
	return nil
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.RecoverPrimaryRestore(ctx); err != nil {
		return err
	}
	if _, err := w.service.Config(); err != nil {
		if _, saveErr := w.service.SaveConfig(DefaultConfig()); saveErr != nil {
			return saveErr
		}
	}
	_, _ = AppendEvent(w.service.LogDir, NewEvent("worker", EventStatusStarted, EventSeverityInfo, "Database operations worker started."))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	checksumTimer := time.NewTimer(0)
	defer checksumTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checksumTimer.C:
			cfg, _ := w.service.Config()
			w.RunChecksumStatus(ctx)
			checksumTimer.Reset(time.Duration(cfg.Schedule.ChecksumIntervalSeconds) * time.Second)
		case now := <-ticker.C:
			w.runDueCron(ctx, now.UTC())
			if err := w.processIntents(ctx); err != nil {
				_, _ = AppendEvent(w.service.LogDir, NewEvent("intents", EventStatusFailed, EventSeverityError, err.Error()))
			}
		}
	}
}

func (w *Worker) runDueCron(ctx context.Context, now time.Time) {
	cfg, err := w.service.Config()
	if err != nil {
		_, _ = AppendEvent(w.service.LogDir, NewEvent("config", EventStatusFailed, EventSeverityError, err.Error()))
		return
	}
	w.runCronJob(ctx, "backup-full", cfg.Schedule.FullBackupCron, now, func(context.Context) error {
		return w.RunBackup(ctx, "full")
	})
	w.runCronJob(ctx, "backup-diff", cfg.Schedule.DifferentialBackupCron, now, func(context.Context) error {
		return w.RunBackup(ctx, "diff")
	})
	w.runCronJob(ctx, "amcheck", cfg.Schedule.AMCheckCron, now, w.RunAMCheck)
	if cfg.Schedule.RestoreVerificationEnabled {
		w.runCronJob(ctx, "restore-verification", cfg.Schedule.RestoreVerificationCron, now, w.RunScheduledRestoreVerification)
	}
}

func (w *Worker) runCronJob(ctx context.Context, key string, spec string, now time.Time, run func(context.Context) error) {
	if !CronMatches(spec, now) {
		return
	}
	runKey := now.Format("2006-01-02T15:04")
	if w.lastCronRuns[key] == runKey {
		return
	}
	w.lastCronRuns[key] = runKey
	if err := run(ctx); err != nil {
		_, _ = AppendEvent(w.service.LogDir, NewEvent(key, EventStatusFailed, EventSeverityError, err.Error()))
	}
}

func (w *Worker) processIntents(ctx context.Context) error {
	intents, err := ListIntents(w.service.StateDir, w.service.IntentSecret)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		var runErr error
		switch intent.Type {
		case IntentTypeBackup:
			runErr = w.runBackup(ctx, intent.BackupType, intent.ID)
		case IntentTypeRestoreTest:
			runErr = w.runRestoreTest(ctx, intent.BackupLabel, intent.ID)
		case IntentTypePrimaryRestore:
			runErr = w.runPrimaryRestore(ctx, intent.BackupLabel, intent.ID)
		default:
			runErr = fmt.Errorf("unsupported intent type %q", intent.Type)
		}
		if runErr != nil {
			event := NewEvent(intent.Type, EventStatusFailed, EventSeverityError, runErr.Error())
			event.Details = map[string]any{"intent_id": intent.ID}
			_, _ = AppendEvent(w.service.LogDir, event)
			if err := CompleteIntent(w.service.StateDir, intent.ID); err != nil {
				return err
			}
			continue
		}
		if err := CompleteIntent(w.service.StateDir, intent.ID); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) RunChecksumStatus(ctx context.Context) error {
	if w.databaseURL == "" {
		return errors.New("database URL is required for checksum status checks")
	}
	output, err := w.runner.Run(ctx, "psql", w.databaseURL, "-Atc", "select current_setting('data_checksums')")
	event := NewEvent(EventOperationChecksum, EventStatusSuccess, EventSeverityInfo, "PostgreSQL checksum status checked.")
	enabled := strings.TrimSpace(output) == "on"
	event.Details = map[string]any{"enabled": enabled, "data_checksums": strings.TrimSpace(output)}
	if err != nil {
		event.Status = EventStatusFailed
		event.Severity = EventSeverityError
		event.Message = "Checksum status check failed: " + err.Error()
		event.Details["output_redacted"] = strings.TrimSpace(output) != ""
	} else if !enabled {
		event.Severity = EventSeverityWarning
		event.Message = "PostgreSQL data checksums are not enabled for this cluster."
	}
	_, appendErr := AppendEvent(w.service.LogDir, event)
	if err != nil {
		return err
	}
	return appendErr
}

func (w *Worker) RunAMCheck(ctx context.Context) error {
	if w.databaseURL == "" {
		return errors.New("database URL is required for pg_amcheck")
	}
	output, err := w.runner.Run(ctx, "pg_amcheck", "--install-missing=pg_catalog", "--no-dependent-indexes", w.databaseURL)
	event := NewEvent(EventOperationAMCheck, EventStatusSuccess, EventSeverityInfo, "pg_amcheck completed without reported corruption.")
	event.Details = map[string]any{"output_redacted": strings.TrimSpace(output) != ""}
	if err != nil {
		event.Status = EventStatusFailed
		event.Details = commandFailureDetails(output, err)
		if outputIndicatesAMCheckCorruption(output) {
			event.Severity = EventSeverityCritical
			event.Message = "pg_amcheck reported a database integrity problem."
			event.Details["corruption_detected"] = true
		} else {
			event.Severity = EventSeverityError
			event.Message = "pg_amcheck could not complete."
			event.Details["corruption_detected"] = false
		}
	}
	_, appendErr := AppendEvent(w.service.LogDir, event)
	if err != nil {
		return err
	}
	return appendErr
}

func (w *Worker) RunBackup(ctx context.Context, backupType string) error {
	return w.runBackup(ctx, backupType, "")
}

func (w *Worker) runBackup(ctx context.Context, backupType string, taskID string) error {
	unlock, err := acquireRestoreLock(w.service.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := CheckPrimaryRestoreStartup(w.service.StateDir); err != nil {
		return err
	}
	backupType = strings.TrimSpace(strings.ToLower(backupType))
	if backupType == "" {
		backupType = "diff"
	}
	if backupType != "full" && backupType != "diff" {
		return errors.New("backup type must be full or diff")
	}
	task := w.startTask(taskID, EventOperationBackup, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Preparing backup", func(task *TaskStatus) {
		task.BackupType = backupType
	})
	fail := func(message string, output string, err error) error {
		w.finishTask(&task, TaskStatusFailed, message)
		return w.recordBackupFailure(message, output, err)
	}

	start := NewEvent(EventOperationBackup, EventStatusStarted, EventSeverityInfo, "pgBackRest backup started.")
	start.Details = map[string]any{"backup_type": backupType}
	_, _ = AppendEvent(w.service.LogDir, start)

	cfg, _ := w.service.Config()
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Checking encrypted backup settings")
	if err := w.requireRepoCipherPass(); err != nil {
		return fail("Encrypted pgBackRest repository is not configured.", "", err)
	}
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Validating live note attachments")
	attachmentPreflight, err := w.validateLiveAttachmentFiles(ctx)
	if err != nil {
		return fail("Live attachment integrity check failed before backup.", "", err)
	}
	baseArgs := w.pgBackRestArgs(cfg)
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Preparing pgBackRest folders")
	if err := w.preparePgBackRestPaths(ctx, cfg); err != nil {
		return fail("Could not prepare pgBackRest directories.", "", err)
	}
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Creating pgBackRest stanza")
	if output, err := w.runPgBackRest(ctx, append(baseArgs, "stanza-create")...); err != nil && !strings.Contains(output, "already exists") {
		return fail("pgBackRest stanza creation failed.", output, err)
	}
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Checking pgBackRest repository")
	if output, err := w.runPgBackRest(ctx, append(baseArgs, "check")...); err != nil {
		return fail("pgBackRest check failed.", output, err)
	}
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Running "+backupType+" backup")
	backupArgs := append(append([]string{}, baseArgs...), "--type="+backupType, fmt.Sprintf("--repo1-retention-full=%d", cfg.Schedule.RetentionFull), "backup")
	output, err := w.runPgBackRest(ctx, backupArgs...)
	if err != nil {
		return fail("pgBackRest backup failed.", output, err)
	}
	w.updateTask(&task, taskMessage(EventOperationBackup, backupType, TaskStatusRunning), "Refreshing backup list")
	backups := w.loadBackupInfo(ctx)
	_ = SaveBackupSets(w.service.StateDir, backups)
	label := latestBackupLabel(backups)
	attachmentDetails := map[string]any{"enabled": false}
	if label != "" {
		if details, err := w.runAttachmentBackup(ctx, cfg, label); err != nil {
			return fail("Attachment backup failed.", "", err)
		} else {
			attachmentDetails = details
		}
	}
	task.BackupLabel = label
	event := NewEvent(EventOperationBackup, EventStatusSuccess, EventSeverityInfo, "pgBackRest backup completed.")
	event.BackupLabel = label
	event.Details = map[string]any{"backup_type": backupType, "output_redacted": strings.TrimSpace(output) != "", "attachment_preflight": attachmentPreflight, "attachment_backup": attachmentDetails}
	_, appendErr := AppendEvent(w.service.LogDir, event)
	w.finishTask(&task, TaskStatusSuccess, taskMessage(EventOperationBackup, backupType, TaskStatusSuccess))
	return appendErr
}

func (w *Worker) recordBackupFailure(message string, output string, err error) error {
	event := NewEvent(EventOperationBackup, EventStatusFailed, EventSeverityError, message)
	event.Details = commandFailureDetails(output, err)
	if hint := pgBackRestFailureHint(output, err); hint != "" {
		event.Details["hint"] = hint
	}
	_, _ = AppendEvent(w.service.LogDir, event)
	return fmt.Errorf("%s %w", message, err)
}

func (w *Worker) loadBackupInfo(ctx context.Context) []BackupSet {
	if err := w.requireRepoCipherPass(); err != nil {
		return nil
	}
	cfg, _ := w.service.Config()
	args := append(w.pgBackRestArgs(cfg), "info", "--output=json")
	output, err := w.runPgBackRest(ctx, args...)
	if err != nil {
		return nil
	}
	var decoded []struct {
		Backup []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
			Info  struct {
				Size int64 `json:"size"`
			} `json:"info"`
			Timestamp struct {
				Start int64 `json:"start"`
				Stop  int64 `json:"stop"`
			} `json:"timestamp"`
		} `json:"backup"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil || len(decoded) == 0 {
		return nil
	}
	backups := make([]BackupSet, 0, len(decoded[0].Backup))
	for _, item := range decoded[0].Backup {
		start := time.Unix(item.Timestamp.Start, 0).UTC()
		stop := time.Unix(item.Timestamp.Stop, 0).UTC()
		backups = append(backups, BackupSet{
			Label:     item.Label,
			Type:      item.Type,
			StartedAt: &start,
			StoppedAt: &stop,
			Status:    EventStatusSuccess,
			SizeBytes: item.Info.Size,
		})
	}
	return backups
}

func (w *Worker) RunRestoreTest(ctx context.Context, backupLabel string) error {
	return w.runRestoreTest(ctx, backupLabel, "")
}

func (w *Worker) runRestoreTest(ctx context.Context, backupLabel string, taskID string) error {
	unlock, err := acquireRestoreLock(w.service.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := CheckPrimaryRestoreStartup(w.service.StateDir); err != nil {
		return err
	}
	task := w.startTask(taskID, EventOperationRestoreTest, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Preparing restore verification", func(task *TaskStatus) {
		task.BackupLabel = strings.TrimSpace(backupLabel)
	})
	fail := func(message string, err error) error {
		w.finishTask(&task, TaskStatusFailed, message)
		return w.recordRestoreFailure(EventOperationRestoreTest, message, backupLabel, err)
	}

	start := NewEvent(EventOperationRestoreTest, EventStatusStarted, EventSeverityInfo, "Restore verification started.")
	start.BackupLabel = backupLabel
	_, _ = AppendEvent(w.service.LogDir, start)

	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Checking encrypted backup settings")
	if err := w.requireRepoCipherPass(); err != nil {
		return fail("Encrypted pgBackRest repository is not configured.", err)
	}
	if err := validatePSQLDatabaseURL("HANK_DB_OPS_RESTORE_DATABASE_URL", w.restoreDatabaseURL); err != nil {
		return fail("Restore verification database URL is invalid.", err)
	}
	if err := validatePSQLDatabaseURL("HANK_CLOUD_DATABASE_URL", w.databaseURL); err != nil {
		return fail("Main database URL is invalid.", err)
	}
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Resetting restore test database")
	_ = w.composeWithProfile(context.Background(), "restore", "rm", "-sf", "postgres-restore")
	if err := clearDirectoryContents(w.restoreDataPath); err != nil {
		return fail("Could not clear restore-test data directory.", err)
	}
	_, _ = w.runner.Run(ctx, "chown", "-R", "postgres:postgres", w.restoreDataPath)
	cfg, _ := w.service.Config()
	args := append(w.pgBackRestArgs(cfg), "--pg1-path="+w.restoreDataPath, "--type=immediate", "--target-action=promote")
	if strings.TrimSpace(backupLabel) != "" {
		args = append(args, "--set="+strings.TrimSpace(backupLabel))
	}
	args = append(args, "restore")
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Restoring backup into test database")
	output, err := w.runPgBackRest(ctx, args...)
	if err != nil {
		return fail("Restore verification failed.", fmt.Errorf("%w: %s", err, output))
	}
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Starting restore test database")
	if err := w.composeWithProfile(ctx, "restore", "up", "-d", "postgres-restore"); err != nil {
		return fail("Restore verification database did not start.", err)
	}
	defer func() {
		_ = w.composeWithProfile(context.Background(), "restore", "stop", "postgres-restore")
	}()
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Waiting for restore test database")
	if err := w.waitForRestoreDatabase(ctx); err != nil {
		return fail("Restore verification database was not ready.", err)
	}
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Validating restored database")
	if err := w.validateRestoredDatabase(ctx); err != nil {
		return fail("Restore verification database validation failed.", err)
	}
	w.updateTask(&task, taskMessage(EventOperationRestoreTest, "", TaskStatusRunning), "Validating restored attachments")
	attachmentDetails, err := w.validateAttachmentBackupRestore(ctx, backupLabel)
	if err != nil {
		return fail("Restore verification attachment validation failed.", err)
	}
	event := NewEvent(EventOperationRestoreTest, EventStatusSuccess, EventSeverityInfo, "Restore verification completed.")
	event.BackupLabel = backupLabel
	event.Details = map[string]any{"output_redacted": strings.TrimSpace(output) != "", "table_check": "hank_core_tables", "sample_check": "matched", "role_check": "matched", "attachment_check": attachmentDetails}
	_, appendErr := AppendEvent(w.service.LogDir, event)
	w.finishTask(&task, TaskStatusSuccess, taskMessage(EventOperationRestoreTest, "", TaskStatusSuccess))
	return appendErr
}

func (w *Worker) RunScheduledRestoreVerification(ctx context.Context) error {
	cfg, err := w.service.Config()
	if err != nil {
		return err
	}
	if !cfg.Schedule.RestoreVerificationEnabled {
		return nil
	}
	backups, _ := LoadBackupSets(w.service.StateDir)
	label := latestBackupLabel(backups)
	if label == "" {
		backups = w.loadBackupInfo(ctx)
		_ = SaveBackupSets(w.service.StateDir, backups)
		label = latestBackupLabel(backups)
	}
	if label == "" {
		event := NewEvent(EventOperationRestoreTest, EventStatusFailed, EventSeverityWarning, "Restore verification skipped because no backup is available.")
		_, appendErr := AppendEvent(w.service.LogDir, event)
		return appendErr
	}
	return w.RunRestoreTest(ctx, label)
}

func (w *Worker) RunPrimaryRestore(ctx context.Context, backupLabel string) error {
	return w.runPrimaryRestore(ctx, backupLabel, "")
}

func (w *Worker) runPrimaryRestore(ctx context.Context, backupLabel string, taskID string) error {
	return w.runPairedPrimaryRestore(ctx, backupLabel, taskID)
}

func (w *Worker) recordRestoreFailure(operation string, message string, backupLabel string, err error) error {
	event := NewEvent(operation, EventStatusFailed, EventSeverityCritical, message)
	event.BackupLabel = backupLabel
	event.Details = map[string]any{"error": redactAndTruncate(err.Error())}
	if hint := restoreFailureHint(err); hint != "" {
		event.Details["hint"] = hint
	}
	_, _ = AppendEvent(w.service.LogDir, event)
	return fmt.Errorf("%s %w", message, err)
}

func (w *Worker) startTask(taskID string, operation string, message string, step string, mutate func(*TaskStatus)) TaskStatus {
	now := time.Now().UTC()
	task := TaskStatus{
		ID:        strings.TrimSpace(taskID),
		Operation: operation,
		Status:    TaskStatusRunning,
		Message:   message,
		Step:      step,
		StartedAt: &now,
		UpdatedAt: now,
	}
	if task.ID == "" {
		task.ID = newEventID()
	}
	if mutate != nil {
		mutate(&task)
	}
	_ = SaveActiveTask(w.service.StateDir, task)
	return task
}

func (w *Worker) updateTask(task *TaskStatus, message string, step string) {
	if task == nil || strings.TrimSpace(task.ID) == "" {
		return
	}
	task.Status = TaskStatusRunning
	task.Message = strings.TrimSpace(message)
	task.Step = strings.TrimSpace(step)
	task.UpdatedAt = time.Now().UTC()
	_ = SaveActiveTask(w.service.StateDir, *task)
}

func (w *Worker) finishTask(task *TaskStatus, status string, message string) {
	if task == nil || strings.TrimSpace(task.ID) == "" {
		return
	}
	task.Status = strings.TrimSpace(status)
	task.Message = strings.TrimSpace(message)
	task.Step = ""
	task.UpdatedAt = time.Now().UTC()
	_ = SaveActiveTask(w.service.StateDir, *task)
}

func (w *Worker) compose(ctx context.Context, args ...string) error {
	composeArgs := w.composeCommandArgs()
	composeArgs = append(composeArgs, args...)
	output, err := w.runner.Run(ctx, "docker", composeArgs...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func (w *Worker) composeWithProfile(ctx context.Context, profile string, args ...string) error {
	composeArgs := w.composeCommandArgs()
	if strings.TrimSpace(profile) != "" {
		composeArgs = append(composeArgs, "--profile", strings.TrimSpace(profile))
	}
	composeArgs = append(composeArgs, args...)
	output, err := w.runner.Run(ctx, "docker", composeArgs...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func (w *Worker) composeCommandArgs() []string {
	args := []string{"compose"}
	for _, name := range filepath.SplitList(w.composeFile) {
		if name = strings.TrimSpace(name); name != "" {
			args = append(args, "-f", name)
		}
	}
	return args
}

func (w *Worker) waitForRestoreDatabase(ctx context.Context) error {
	return w.waitForDatabase(ctx, w.restoreDatabaseURL)
}
func (w *Worker) waitForDatabase(ctx context.Context, databaseURL string) error {
	if err := validatePSQLDatabaseURL("HANK_DB_OPS_RESTORE_DATABASE_URL", databaseURL); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	var lastOutput string
	var lastErr error
	for time.Now().Before(deadline) {
		// A recovering PostgreSQL instance can accept reads before pgBackRest's
		// target promotion completes. Wait for a writable primary before applying
		// versioned migrations or publishing the recovered dataset.
		output, err := w.runner.Run(ctx, "psql", databaseURL, "-Atc", "select 1 where not pg_is_in_recovery() and current_setting('transaction_read_only') = 'off'")
		if err == nil && strings.TrimSpace(output) == "1" {
			return nil
		}
		lastOutput = output
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w: %s", lastErr, lastOutput)
	}
	return fmt.Errorf("restore database did not answer readiness query: %s", lastOutput)
}

func (w *Worker) validateRestoredDatabase(ctx context.Context) error {
	missingTables, err := w.missingExpectedTables(ctx)
	if err != nil {
		return err
	}
	if missingTables != "" {
		return fmt.Errorf("restored database is missing expected tables: %s", missingTables)
	}
	mainRoles, err := w.loginRoles(ctx, w.databaseURL)
	if err != nil {
		return fmt.Errorf("main database role check: %w", err)
	}
	restoreRoles, err := w.loginRoles(ctx, w.restoreDatabaseURL)
	if err != nil {
		return fmt.Errorf("restore database role check: %w", err)
	}
	if mainRoles != restoreRoles {
		return fmt.Errorf("restored database login roles differ from main database")
	}
	mainCounts, err := w.tableCounts(ctx, w.databaseURL)
	if err != nil {
		return fmt.Errorf("main database count check: %w", err)
	}
	restoreCounts, err := w.tableCounts(ctx, w.restoreDatabaseURL)
	if err != nil {
		return fmt.Errorf("restore database count check: %w", err)
	}
	if mainCounts != restoreCounts {
		return fmt.Errorf("restored database table counts differ from main database: main=%s restore=%s", mainCounts, restoreCounts)
	}
	mainSamples, err := w.sampleRecordFingerprints(ctx, w.databaseURL)
	if err != nil {
		return fmt.Errorf("main database sample check: %w", err)
	}
	restoreSamples, err := w.sampleRecordFingerprints(ctx, w.restoreDatabaseURL)
	if err != nil {
		return fmt.Errorf("restore database sample check: %w", err)
	}
	if mainSamples != restoreSamples {
		return fmt.Errorf("restored database sample records differ from main database")
	}
	return nil
}

func (w *Worker) missingExpectedTables(ctx context.Context) (string, error) {
	const query = `WITH expected(name) AS (VALUES ('users'), ('homes'), ('home_memberships'), ('agents'), ('agent_tokens'), ('app_sessions'), ('user_notes'), ('home_permissions')) SELECT coalesce(string_agg(name, ',' ORDER BY name), '') FROM expected WHERE to_regclass('public.' || name) IS NULL`
	output, err := w.runner.Run(ctx, "psql", w.restoreDatabaseURL, "-Atc", query)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	return strings.TrimSpace(output), nil
}

func (w *Worker) loginRoles(ctx context.Context, databaseURL string) (string, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return "", errors.New("database URL is required for role validation")
	}
	const query = `SELECT rolname || '|' || rolsuper || '|' || rolinherit || '|' || rolcreaterole || '|' || rolcreatedb || '|' || rolcanlogin || '|' || rolreplication || '|' || rolbypassrls FROM pg_roles WHERE rolcanlogin AND rolname !~ '^pg_' AND rolname <> 'postgres' ORDER BY rolname`
	output, err := w.runner.Run(ctx, "psql", databaseURL, "-Atc", query)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	return strings.TrimSpace(output), nil
}

func (w *Worker) tableCounts(ctx context.Context, databaseURL string) (string, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return "", errors.New("database URL is required for table count validation")
	}
	const query = `WITH table_counts(name, count_value) AS (
		VALUES
			('users', (SELECT count(*) FROM users)),
			('homes', (SELECT count(*) FROM homes)),
			('agents', (SELECT count(*) FROM agents)),
			('user_notes', (SELECT count(*) FROM user_notes)),
			('note_attachments', (SELECT count(*) FROM note_attachments)),
			('assistant_file_index', (SELECT count(*) FROM assistant_file_index)),
			('file_transfers', (SELECT count(*) FROM file_transfers)),
			('file_operation_jobs', (SELECT count(*) FROM file_operation_jobs))
	)
	SELECT string_agg(name || '=' || count_value::text, ',' ORDER BY name) FROM table_counts`
	output, err := w.runner.Run(ctx, "psql", databaseURL, "-Atc", query)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	return strings.TrimSpace(output), nil
}

func (w *Worker) sampleRecordFingerprints(ctx context.Context, databaseURL string) (string, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return "", errors.New("database URL is required for sample record validation")
	}
	const query = `WITH sample_records(label, value) AS (
		VALUES
			('users', coalesce((SELECT string_agg(concat_ws('|', id, email), ',' ORDER BY id) FROM (SELECT id, email FROM users ORDER BY id LIMIT 10) sample), '')),
			('homes', coalesce((SELECT string_agg(concat_ws('|', id, user_id, name), ',' ORDER BY id) FROM (SELECT id, user_id, name FROM homes ORDER BY id LIMIT 10) sample), '')),
			('agents', coalesce((SELECT string_agg(concat_ws('|', id, home_id, name, status), ',' ORDER BY id) FROM (SELECT id, home_id, name, status FROM agents ORDER BY id LIMIT 10) sample), '')),
			('user_notes', coalesce((SELECT string_agg(concat_ws('|', id, note_id, title, revision::text, checksum), ',' ORDER BY id) FROM (SELECT id, note_id, title, revision, checksum FROM user_notes ORDER BY id LIMIT 10) sample), '')),
			('note_attachments', coalesce((SELECT string_agg(concat_ws('|', id, note_id, filename, size_bytes::text, storage_key), ',' ORDER BY id) FROM (SELECT id, note_id, filename, size_bytes, storage_key FROM note_attachments ORDER BY id LIMIT 10) sample), '')),
			('assistant_file_index', coalesce((SELECT string_agg(concat_ws('|', id, path, name, size_bytes::text), ',' ORDER BY id) FROM (SELECT id, path, name, size_bytes FROM assistant_file_index ORDER BY id LIMIT 10) sample), '')),
			('file_transfers', coalesce((SELECT string_agg(concat_ws('|', id, operation, status, bytes_total::text, bytes_done::text, file_job_id), ',' ORDER BY id) FROM (SELECT id, operation, status, bytes_total, bytes_done, file_job_id FROM file_transfers ORDER BY id LIMIT 10) sample), '')),
			('file_operation_jobs', coalesce((SELECT string_agg(concat_ws('|', id, operation, status, bytes_total::text, bytes_done::text), ',' ORDER BY id) FROM (SELECT id, operation, status, bytes_total, bytes_done FROM file_operation_jobs ORDER BY id LIMIT 10) sample), ''))
	)
	SELECT string_agg(label || '=' || md5(value), ',' ORDER BY label) FROM sample_records`
	output, err := w.runner.Run(ctx, "psql", databaseURL, "-Atc", query)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	return strings.TrimSpace(output), nil
}

func (w *Worker) runAttachmentBackup(ctx context.Context, cfg Config, backupLabel string) (map[string]any, error) {
	if strings.TrimSpace(backupLabel) == "" {
		return map[string]any{"enabled": false, "reason": "missing_backup_label"}, nil
	}
	if _, err := os.Stat(w.noteAttachmentDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"enabled": false, "reason": "attachment_dir_missing"}, nil
		}
		return nil, err
	}
	if !attachmentBackupLabel.MatchString(backupLabel) {
		return nil, errors.New("invalid attachment backup label")
	}
	archive := w.attachmentBackupPath(cfg, backupLabel)
	if err := writeEncryptedAttachments(ctx, w.noteAttachmentDir, archive, w.repoCipherPass); err != nil {
		return nil, err
	}
	info, statErr := os.Stat(archive)
	size := int64(0)
	if statErr == nil {
		size = info.Size()
	}
	return map[string]any{"enabled": true, "archive": archive, "size_bytes": size, "format": "tar.gz.age", "encrypted": true}, nil
}

func (w *Worker) validateLiveAttachmentFiles(ctx context.Context) (map[string]any, error) {
	if strings.TrimSpace(w.databaseURL) == "" {
		return nil, errors.New("database URL is required for attachment validation")
	}
	if err := validatePSQLDatabaseURL("HANK_CLOUD_DATABASE_URL", w.databaseURL); err != nil {
		return nil, err
	}
	const query = `SELECT COALESCE(json_agg(json_build_object(
		'id', id,
		'storage_key', storage_key,
		'size_bytes', size_bytes,
		'preview_storage_key', COALESCE(preview_storage_key, ''),
		'preview_size_bytes', COALESCE(preview_size_bytes, 0)
	) ORDER BY id), '[]'::json)::text
	FROM note_attachments
	WHERE deleted_at IS NULL AND status <> 'deleted'`
	output, err := w.runner.Run(ctx, "psql", w.databaseURL, "-X", "-Atc", query)
	if err != nil {
		return nil, fmt.Errorf("query active attachment inventory: %w", err)
	}
	var records []liveAttachmentFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &records); err != nil {
		return nil, errors.New("active attachment inventory was not valid JSON")
	}
	checked, err := validateLiveAttachmentInventory(w.noteAttachmentDir, records)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rows": len(records), "checked_files": checked, "status": "matched"}, nil
}

func validateLiveAttachmentInventory(root string, records []liveAttachmentFile) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return 0, errors.New("attachment root must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return 0, errors.New("attachment root is missing, inaccessible, or unsafe")
	}

	seen := make(map[string]struct{}, len(records)*2)
	checked := 0
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			return checked, errors.New("active attachment inventory contains an empty identifier")
		}
		files := []struct {
			key  string
			size int64
		}{{key: record.StorageKey, size: record.SizeBytes}}
		if strings.TrimSpace(record.PreviewStorageKey) != "" {
			files = append(files, struct {
				key  string
				size int64
			}{key: record.PreviewStorageKey, size: record.PreviewSizeBytes})
		}
		for _, file := range files {
			if file.size < 0 {
				return checked, fmt.Errorf("attachment %q has an invalid recorded size", record.ID)
			}
			path, key, err := canonicalAttachmentPath(root, file.key)
			if err != nil {
				return checked, fmt.Errorf("attachment %q has an unsafe storage key", record.ID)
			}
			if _, duplicate := seen[key]; duplicate {
				return checked, fmt.Errorf("attachment %q reuses a storage key", record.ID)
			}
			seen[key] = struct{}{}
			if err := validateReadableRegularFile(root, path, file.size); err != nil {
				return checked, fmt.Errorf("attachment %q file is missing, unreadable, mismatched, or unsafe", record.ID)
			}
			checked++
		}
	}
	return checked, nil
}

func canonicalAttachmentPath(root, key string) (string, string, error) {
	if key == "" || strings.TrimSpace(key) != key || strings.Contains(key, `\`) || filepath.IsAbs(key) || filepath.Clean(key) != key || key == ".." || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
		return "", "", errors.New("unsafe attachment storage key")
	}
	joined, err := safeJoin(root, key)
	return joined, key, err
}

func validateReadableRegularFile(root, path string, expectedSize int64) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("attachment path escapes root")
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("attachment path is missing or contains a symlink")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("attachment parent is not a directory")
		}
		if index == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() != expectedSize) {
			return errors.New("attachment is not a regular file or has the wrong size")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("attachment cannot be opened for reading")
	}
	return file.Close()
}

func (w *Worker) validateAttachmentBackupRestore(ctx context.Context, backupLabel string) (map[string]any, error) {
	rowCount, err := w.restoreAttachmentRowCount(ctx)
	if err != nil {
		return nil, err
	}
	if rowCount == 0 {
		return map[string]any{"rows": 0, "checked_files": 0, "status": "skipped_empty"}, nil
	}
	cfg, _ := w.service.Config()
	label := strings.TrimSpace(backupLabel)
	if label == "" {
		backups, _ := LoadBackupSets(w.service.StateDir)
		label = latestBackupLabel(backups)
	}
	if label == "" {
		return nil, errors.New("attachment restore validation requires a backup label")
	}
	if !attachmentBackupLabel.MatchString(label) {
		return nil, errors.New("invalid attachment backup label")
	}
	archive := w.attachmentBackupPath(cfg, label)
	encrypted := true
	if _, err := os.Lstat(archive); errors.Is(err, os.ErrNotExist) {
		archive = strings.TrimSuffix(archive, ".age")
		encrypted = false
	} else if err != nil {
		return nil, errors.New("cannot inspect attachment archive")
	}
	if err := os.MkdirAll(w.attachmentRestoreDir, 0700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(w.attachmentRestoreDir, ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := extractAttachmentArchive(ctx, archive, staging, w.repoCipherPass, encrypted); err != nil {
		return nil, err
	}
	checked, err := w.validateRestoredAttachmentFilesAt(ctx, staging)
	if err != nil {
		return nil, err
	}
	if err := publishRestoredAttachments(staging, w.attachmentRestoreDir); err != nil {
		return nil, err
	}
	return map[string]any{"rows": rowCount, "checked_files": checked, "archive": archive, "status": "matched", "encrypted": encrypted}, nil
}

func (w *Worker) restoreAttachmentRowCount(ctx context.Context) (int, error) {
	const query = `SELECT count(*) FROM note_attachments WHERE deleted_at IS NULL AND status <> 'deleted'`
	output, err := w.runner.Run(ctx, "psql", w.restoreDatabaseURL, "-Atc", query)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(output)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (w *Worker) validateRestoredAttachmentFiles(ctx context.Context) (int, error) {
	return w.validateRestoredAttachmentFilesAt(ctx, w.attachmentRestoreDir)
}

func (w *Worker) validateRestoredAttachmentFilesAt(ctx context.Context, restoreDir string) (int, error) {
	const query = `SELECT storage_key || E'\t' || size_bytes::text || E'\t' || checksum_sha256 FROM note_attachments WHERE deleted_at IS NULL AND status <> 'deleted' UNION ALL SELECT preview_storage_key || E'\t' || preview_size_bytes::text || E'\t' || preview_checksum_sha256 FROM note_attachments WHERE deleted_at IS NULL AND status <> 'deleted' AND preview_storage_key IS NOT NULL`
	output, err := w.runner.Run(ctx, "psql", w.restoreDatabaseURL, "-Atc", query)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", err, redactAndTruncate(output))
	}
	root, err := os.OpenRoot(restoreDir)
	if err != nil {
		return 0, errors.New("cannot open attachment validation root")
	}
	defer root.Close()
	reader := bufio.NewReader(strings.NewReader(output))
	checked := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return checked, err
		}
		line = strings.TrimSpace(line)
		if line != "" {
			parts := strings.Split(line, "\t")
			if len(parts) != 3 {
				return checked, errors.New("invalid attachment validation row")
			}
			key, sizeText, checksum := parts[0], parts[1], parts[2]
			if _, _, err := canonicalAttachmentPath(restoreDir, key); err != nil {
				return checked, err
			}
			if key == RecoveryDirectoryName || strings.HasPrefix(key, RecoveryDirectoryName+"/") {
				return checked, errors.New("attachment metadata uses private recovery storage")
			}
			expectedHash, hashErr := hex.DecodeString(checksum)
			if hashErr != nil || len(expectedHash) != sha256.Size {
				return checked, errors.New("invalid attachment checksum metadata")
			}
			wantSize, parseErr := strconv.ParseInt(sizeText, 10, 64)
			if parseErr != nil || wantSize < 0 {
				return checked, errors.New("invalid attachment size metadata")
			}
			file, openErr := root.Open(key)
			if openErr != nil {
				return checked, errors.New("restored attachment is missing or outside its root")
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() != wantSize {
				file.Close()
				return checked, errors.New("restored attachment size or type mismatch")
			}
			digest := sha256.New()
			_, copyErr := io.Copy(digest, archiveContextReader{ctx, file})
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return checked, errors.New("cannot verify restored attachment content")
			}
			if !bytes.Equal(digest.Sum(nil), expectedHash) {
				return checked, errors.New("restored attachment checksum mismatch")
			}
			checked++
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return checked, nil
}

func (w *Worker) attachmentBackupPath(cfg Config, backupLabel string) string {
	cfg = cfg.Normalized()
	return filepath.Join(cfg.Target.Path, "hank-attachments", strings.TrimSpace(backupLabel)+".tar.gz.age")
}

func (w *Worker) runPgBackRest(ctx context.Context, args ...string) (string, error) {
	if os.Geteuid() != 0 {
		return w.runner.Run(ctx, "pgbackrest", args...)
	}
	return w.runner.Run(ctx, "gosu", append([]string{"postgres", "pgbackrest"}, args...)...)
}

func (w *Worker) requireRepoCipherPass() error {
	if strings.TrimSpace(w.repoCipherPass) == "" {
		return errors.New("HANK_DB_OPS_REPO_CIPHER_PASS is required for encrypted pgBackRest backups")
	}
	return nil
}

func (w *Worker) preparePgBackRestPaths(ctx context.Context, cfg Config) error {
	cfg = cfg.Normalized()
	if os.Geteuid() != 0 {
		return nil
	}
	if cfg.Target.Type == TargetTypePosix && strings.TrimSpace(cfg.Target.Path) != "" {
		if _, err := w.runner.Run(ctx, "chown", "-R", "postgres:postgres", cfg.Target.Path); err != nil {
			return err
		}
	}
	_, _ = w.runner.Run(ctx, "chown", "-R", "postgres:postgres", "/var/log/pgbackrest")
	return nil
}

func (w *Worker) writeSafetyMarker(backupLabel string) error {
	path := filepath.Join(w.service.StateDir, "primary-restore-safety-"+time.Now().UTC().Format("20060102T150405Z")+".json")
	data, err := json.MarshalIndent(map[string]any{
		"backup_label": backupLabel,
		"pgdata_path":  w.pgDataPath,
		"created_at":   time.Now().UTC(),
		"note":         "Primary restore requested. pgBackRest repository is the durable restore source.",
	}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, data)
}

func clearDirectoryContents(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func safeJoin(root string, key string) (string, error) {
	cleanRoot := filepath.Clean(root)
	cleanKey := filepath.Clean(strings.TrimSpace(key))
	if cleanKey == "." || filepath.IsAbs(cleanKey) || cleanKey == ".." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	joined := filepath.Join(cleanRoot, cleanKey)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return joined, nil
}

func latestBackupLabel(backups []BackupSet) string {
	if len(backups) == 0 {
		return ""
	}
	return backups[len(backups)-1].Label
}

func truncateOutput(value string) string {
	const maxOutputBytes = 12000
	if len(value) <= maxOutputBytes {
		return value
	}
	return value[:maxOutputBytes] + "\n... truncated ..."
}

func redactAndTruncate(value string) string {
	return truncateOutput(RedactSensitive(value))
}

func commandFailureDetails(output string, err error) map[string]any {
	details := map[string]any{"output_redacted": strings.TrimSpace(output) != ""}
	if err != nil {
		details["error"] = redactAndTruncate(err.Error())
	}
	if excerpt := commandOutputExcerpt(output); excerpt != "" {
		details["output_excerpt"] = excerpt
	}
	return details
}

func commandOutputExcerpt(output string) string {
	output = strings.TrimSpace(redactAndTruncate(output))
	if output == "" {
		return ""
	}
	const maxOutputExcerptBytes = 1600
	if len(output) <= maxOutputExcerptBytes {
		return output
	}
	return output[:maxOutputExcerptBytes] + "\n... truncated ..."
}

func pgBackRestFailureHint(output string, err error) string {
	combined := strings.ToLower(output)
	if err != nil {
		combined += " " + strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(combined, "not allowed on the command-line"):
		return "Deploy the latest server build so pgBackRest receives the repository passphrase through PGBACKREST_REPO1_CIPHER_PASS instead of command-line arguments."
	case strings.Contains(combined, "cipher") || strings.Contains(combined, "decrypt"):
		return "Check that HANK_DB_OPS_REPO_CIPHER_PASS matches the passphrase used when this backup repository was created."
	case strings.Contains(combined, "unable to find primary cluster"):
		return "Check that the pgBackRest stanza points at the running PostgreSQL data directory and socket path."
	case strings.Contains(combined, "permission denied") || strings.Contains(combined, "could not create") || strings.Contains(combined, "unable to create"):
		return "Check ownership and write access for the pgBackRest repository and log directories."
	case strings.Contains(combined, "repo1-path") || strings.Contains(combined, "repository"):
		return "Check that the configured backup target path is mounted into both postgres and db-ops."
	default:
		return ""
	}
}

func outputIndicatesAMCheckCorruption(output string) bool {
	normalized := strings.ToLower(output)
	corruptionSignals := []string{
		"corrupt",
		"checksum verification failed",
		"invalid page",
		"could not read block",
		"block verification failed",
		"heap table corruption",
		"btree index corruption",
		"toast value",
		"missing chunk",
	}
	for _, signal := range corruptionSignals {
		if strings.Contains(normalized, signal) {
			return true
		}
	}
	return false
}

func (w *Worker) pgBackRestArgs(cfg Config) []string {
	cfg = cfg.Normalized()
	args := []string{"--stanza=" + w.stanza, "--repo1-cipher-type=aes-256-cbc"}
	if cfg.Target.Type == TargetTypePosix && strings.TrimSpace(cfg.Target.Path) != "" {
		args = append(args, "--repo1-path="+cfg.Target.Path)
	}
	return args
}

func pgBackRestEnv(repoCipherPass string) []string {
	repoCipherPass = strings.TrimSpace(repoCipherPass)
	if repoCipherPass == "" {
		return nil
	}
	return []string{"PGBACKREST_REPO1_CIPHER_PASS=" + repoCipherPass}
}

func validatePSQLDatabaseURL(envName string, databaseURL string) error {
	value := strings.TrimSpace(databaseURL)
	if value == "" {
		return fmt.Errorf("%s is required", envName)
	}
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		return fmt.Errorf("%s must not include angle brackets", envName)
	}
	if strings.HasPrefix(value, "<") {
		return fmt.Errorf("%s must not include a leading angle bracket", envName)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s is not a valid Postgres URL: %w", envName, err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return fmt.Errorf("%s must start with postgres:// or postgresql://", envName)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a database host", envName)
	}
	if parsed.RawQuery == "" {
		return nil
	}
	for _, part := range strings.Split(parsed.RawQuery, "&") {
		if part == "" {
			continue
		}
		if strings.Contains(part, "<") || strings.Contains(part, ">") {
			return fmt.Errorf("%s has invalid URI query parameter %q; remove angle brackets and use sslmode=disable", envName, part)
		}
		if !strings.Contains(part, "=") {
			return fmt.Errorf("%s has invalid URI query parameter %q; use sslmode=disable", envName, part)
		}
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return fmt.Errorf("%s has an invalid URI query string: %w", envName, err)
	}
	return nil
}

func restoreFailureHint(err error) string {
	if err == nil {
		return ""
	}
	combined := strings.ToLower(err.Error())
	switch {
	case strings.Contains(combined, "hank_db_ops_restore_database_url"):
		return "Fix HANK_DB_OPS_RESTORE_DATABASE_URL in .env.cloud. It should look like postgres://hank:<password>@postgres-restore:5432/hank?sslmode=disable."
	case strings.Contains(combined, "hank_cloud_database_url"):
		return "Fix HANK_CLOUD_DATABASE_URL in .env.cloud. It should look like postgres://hank:<password>@postgres:5432/hank?sslmode=disable."
	case strings.Contains(combined, "missing key/value separator"):
		return "Check the database URL query string in .env.cloud. Use sslmode=disable, not ssl or a value wrapped in angle brackets."
	default:
		return ""
	}
}

func ValidBackupLabel(label string) bool { return attachmentBackupLabel.MatchString(label) }
