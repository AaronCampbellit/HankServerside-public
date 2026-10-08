package storageops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/store"
)

func (w *Worker) runPairedPrimaryRestore(ctx context.Context, label, taskID string) error {
	unlock, err := acquireRestoreLock(w.service.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := CheckPrimaryRestoreStartup(w.service.StateDir); err != nil {
		return err
	}
	label = strings.TrimSpace(label)
	if label == "" || !attachmentBackupLabel.MatchString(label) {
		return errors.New("paired restore requires an explicit valid backup label")
	}
	if err := w.requireRepoCipherPass(); err != nil {
		return err
	}
	for name, value := range map[string]string{"HANK_CLOUD_DATABASE_URL": w.databaseURL, "HANK_DB_OPS_RESTORE_DATABASE_URL": w.restoreDatabaseURL} {
		if err := validatePSQLDatabaseURL(name, value); err != nil {
			return err
		}
	}
	if err := w.validateRestoreRoots(); err != nil {
		return err
	}
	task := w.startTask(taskID, EventOperationPrimaryRestore, "Restoring database and matching attachments", "Validating isolated backup", func(task *TaskStatus) { task.BackupLabel = label })
	fail := func(err error) error {
		w.finishTask(&task, TaskStatusFailed, "Paired restore failed; inspect recovery status")
		return w.recordRestoreFailure(EventOperationPrimaryRestore, "Paired restore failed.", label, err)
	}
	if err := w.composeWithProfile(ctx, "restore", "rm", "-sf", "postgres-restore"); err != nil {
		return fail(err)
	}
	if err := clearDirectoryContents(w.restoreDataPath); err != nil {
		return fail(err)
	}
	cfg, err := w.service.Config()
	if err != nil {
		return fail(err)
	}
	args := append(w.pgBackRestArgs(cfg), "--pg1-path="+w.restoreDataPath, "--type=immediate", "--target-action=promote", "--set="+label, "restore")
	if _, err := w.runPgBackRest(ctx, args...); err != nil {
		return fail(err)
	}
	if err := w.composeWithProfile(ctx, "restore", "up", "-d", "postgres-restore"); err != nil {
		return fail(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = w.composeWithProfile(cleanup, "restore", "stop", "postgres-restore")
	}()
	if err := w.waitForRestoreDatabase(ctx); err != nil {
		return fail(err)
	}
	const identityQuery = "SELECT pg_postmaster_start_time()::text"
	liveIdentity, liveErr := w.runner.Run(ctx, "psql", w.databaseURL, "-Atc", identityQuery)
	restoredIdentity, restoredErr := w.runner.Run(ctx, "psql", w.restoreDatabaseURL, "-Atc", identityQuery)
	if liveErr != nil || restoredErr != nil || strings.TrimSpace(liveIdentity) == "" || strings.TrimSpace(restoredIdentity) == "" || strings.TrimSpace(liveIdentity) == strings.TrimSpace(restoredIdentity) {
		return fail(errors.New("cannot prove the restore database is a separate PostgreSQL instance"))
	}
	// This is the explicit versioned migration workflow on the isolated copy.
	// It never changes the live database or compares historical data with today's rows.
	if w.prepareRestoreDatabase != nil {
		err = w.prepareRestoreDatabase(ctx)
	} else {
		var restored *store.Store
		restored, err = store.OpenMigrating(ctx, w.restoreDatabaseURL)
		if err == nil {
			err = restored.Close()
		}
	}
	if err != nil {
		return fail(fmt.Errorf("restored schema is incompatible: %w", err))
	}
	if err := clearDirectoryContents(w.attachmentRestoreDir); err != nil {
		return fail(err)
	}
	if _, err := w.validateAttachmentBackupRestore(ctx, label); err != nil {
		return fail(err)
	}
	// Stop the isolated database cleanly before copying its physical data directory.
	if err := w.composeWithProfile(ctx, "restore", "stop", "postgres-restore"); err != nil {
		return fail(err)
	}
	p, err := newPairedRestore(w.service.StateDir, w.pgDataPath, w.noteAttachmentDir, task.ID, label)
	if err != nil {
		return fail(err)
	}
	if err := p.prepare(ctx, w.restoreDataPath, w.attachmentRestoreDir); err != nil {
		return fail(err)
	}
	if err := preparePublishedAttachmentPermissions(p.attachmentNew(), w.noteAttachmentDir); err != nil {
		return fail(err)
	}
	if err := p.save(); err != nil {
		return fail(err)
	}
	w.updateTask(&task, "Restoring database and matching attachments", "Stopping services for matched dataset replacement")
	if err := w.compose(ctx, "stop", "cloud", "postgres"); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	if err := p.publish(); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	if err := w.compose(ctx, "up", "-d", "postgres"); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	if err := w.waitForDatabase(ctx, w.databaseURL); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	// Revalidate the bytes at their live paths against the restored live database.
	check := *w
	check.restoreDatabaseURL = w.databaseURL
	if _, err := check.validateRestoredAttachmentFilesAt(ctx, w.noteAttachmentDir); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	p.journal.Phase = "committed"
	if err := p.save(); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	if err := w.compose(ctx, "up", "-d", "--wait", "--wait-timeout", "120", "cloud"); err != nil {
		return fail(w.rollbackPrimaryPair(p, err))
	}
	p.journal.RuntimeStarted = true
	if err := p.save(); err != nil {
		return fail(err)
	}
	// A completed intent is archived before any subsequent worker run can replay it.
	if err := CompleteIntent(w.service.StateDir, task.ID); err != nil {
		return fail(err)
	}
	event := NewEvent(EventOperationPrimaryRestore, EventStatusSuccess, EventSeverityInfo, "Database and matching attachments restored.")
	event.BackupLabel = label
	event.Details = map[string]any{"recovery_id": p.journal.ID, "original_pair_retained": true}
	_, err = AppendEvent(w.service.LogDir, event)
	w.finishTask(&task, TaskStatusSuccess, "Database and matching attachments restored")
	return err
}

func (w *Worker) validateRestoreRoots() error {
	paths := []string{w.pgDataPath, w.restoreDataPath, w.noteAttachmentDir, w.attachmentRestoreDir}
	for i, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil || absolute == string(filepath.Separator) {
			return errors.New("invalid restore directory")
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || resolved != absolute {
			return errors.New("restore directories must exist without symlinks")
		}
		for j, other := range paths {
			if i != j && containsPath(absolute, other) {
				return errors.New("restore directories must not overlap")
			}
		}
	}
	if w.databaseURL == w.restoreDatabaseURL {
		return errors.New("restore database must be isolated from the live database")
	}
	return nil
}

func preparePublishedAttachmentPermissions(staging, liveRoot string) error {
	group, err := attachmentGroup(liveRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(staging, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0660)
		if entry.IsDir() {
			mode = 0770 | os.ModeSetgid
		} else if !entry.Type().IsRegular() {
			return errors.New("unsafe attachment staging entry")
		}
		if err := os.Chown(path, -1, group); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	})
}

func (w *Worker) rollbackPrimaryPair(p *pairedRestore, cause error) error {
	// Recovery must finish even if the initiating request/worker context was cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p.journal.RuntimeStarted = false
	p.journal.Phase = "rolling_back"
	if err := p.save(); err != nil {
		return fmt.Errorf("%w; cannot persist rollback guard: %v", cause, err)
	}
	if err := w.compose(ctx, "stop", "cloud", "postgres"); err != nil {
		return fmt.Errorf("%w; services could not stop for rollback: %v", cause, err)
	}
	if err := p.rollback(); err != nil {
		return fmt.Errorf("%w; original pair could not be recovered; cloud must remain stopped: %v", cause, err)
	}
	if err := w.compose(ctx, "up", "-d", "postgres"); err != nil {
		return fmt.Errorf("%w; original database could not restart: %v", cause, err)
	}
	if err := w.waitForDatabase(ctx, w.databaseURL); err != nil {
		return fmt.Errorf("%w; original database is not ready: %v", cause, err)
	}
	if err := w.compose(ctx, "up", "-d", "--wait", "--wait-timeout", "120", "cloud"); err != nil {
		return fmt.Errorf("%w; original cloud could not restart: %v", cause, err)
	}
	p.journal.RuntimeStarted = true
	if err := p.save(); err != nil {
		return fmt.Errorf("%w; recovered runtime marker could not be saved: %v", cause, err)
	}
	return cause
}

// RecoverPrimaryRestore runs before scheduling or consuming intents. It archives
// the prior intent after recovery so a crash cannot replay a destructive request.
func (w *Worker) RecoverPrimaryRestore(ctx context.Context) error {
	unlock, err := acquireRestoreLock(w.service.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	j, err := readPrimaryRestoreJournal(w.service.StateDir)
	if err != nil || j == nil {
		return err
	}
	pgRoot, err := filepath.Abs(w.pgDataPath)
	if err != nil {
		return err
	}
	attachmentRoot, err := filepath.Abs(w.noteAttachmentDir)
	if err != nil {
		return err
	}
	if j.PGRoot != pgRoot || j.AttachmentRoot != attachmentRoot {
		return errors.New("restore configuration differs from the pending recovery journal")
	}
	if j.Phase != "committed" && j.Phase != "rolled_back" {
		p := &pairedRestore{stateDir: w.service.StateDir, journal: *j}
		sentinel := errors.New("interrupted primary restore")
		recoveryErr := w.rollbackPrimaryPair(p, sentinel)
		if recoveryErr != sentinel {
			return recoveryErr
		}
		event := NewEvent(EventOperationPrimaryRestore, EventStatusFailed, EventSeverityWarning, "Interrupted restore rolled back to its original database and attachments.")
		event.BackupLabel = j.BackupLabel
		_, _ = AppendEvent(w.service.LogDir, event)
	}
	if (j.Phase == "committed" || j.Phase == "rolled_back") && !j.RuntimeStarted {
		if err := w.compose(ctx, "up", "-d", "postgres"); err != nil {
			return err
		}
		if err := w.waitForDatabase(ctx, w.databaseURL); err != nil {
			return err
		}
		if err := w.compose(ctx, "up", "-d", "--wait", "--wait-timeout", "120", "cloud"); err != nil {
			return err
		}
		p := &pairedRestore{stateDir: w.service.StateDir, journal: *j}
		p.journal.RuntimeStarted = true
		if err := p.save(); err != nil {
			return err
		}
	}
	return CompleteIntent(w.service.StateDir, j.TaskID)
}
