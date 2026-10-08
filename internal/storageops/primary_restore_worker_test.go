package storageops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type pairedWorkerRunner struct {
	t         *testing.T
	worker    *Worker
	calls     []string
	failure   string
	rows      string
	checksums string
}

func (r *pairedWorkerRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if name == "pgbackrest" && strings.HasSuffix(call, " restore") {
		if r.failure == "database restore" {
			return "", errors.New("restore failed")
		}
		if err := os.WriteFile(filepath.Join(r.worker.restoreDataPath, "database"), []byte("new database"), 0600); err != nil {
			r.t.Fatal(err)
		}
		return "", nil
	}
	if name == "psql" {
		if strings.Contains(call, "pg_postmaster_start_time") {
			if args[0] == r.worker.databaseURL {
				return "2026-09-05 00:00:00+00", nil
			}
			return "2026-09-05 01:00:00+00", nil
		}
		if strings.Contains(call, "select 1") {
			data, _ := os.ReadFile(filepath.Join(r.worker.pgDataPath, "database"))
			if r.failure == "database readiness" && args[0] == r.worker.databaseURL && string(data) == "new database" {
				return "", errors.New("not ready")
			}
			return "1", nil
		}
		if strings.Contains(call, "count(*) FROM note_attachments") {
			return r.rows, nil
		}
		if strings.Contains(call, "SELECT storage_key") {
			return r.checksums, nil
		}
	}
	if strings.Contains(call, " up -d ") {
		data, _ := os.ReadFile(filepath.Join(r.worker.pgDataPath, "database"))
		if string(data) == "new database" {
			if r.failure == "database start" && strings.HasSuffix(call, " postgres") {
				return "", errors.New("database did not start")
			}
			if r.failure == "cloud start" && strings.HasSuffix(call, " cloud") {
				return "", errors.New("cloud did not start")
			}
		}
	}
	return "", nil
}
func primaryWorkerFixture(t *testing.T, mode string) (*Worker, *pairedWorkerRunner) {
	t.Helper()
	base := t.TempDir()
	paths := map[string]string{}
	for _, name := range []string{"pg", "restore-pg", "attachments", "restore-attachments", "archive-source", "repo"} {
		paths[name] = filepath.Join(base, name)
		if err := os.Mkdir(paths[name], 0770); err != nil {
			t.Fatal(err)
		}
	}
	w := NewWorker(WorkerOptions{StateDir: filepath.Join(base, "state"), LogDir: filepath.Join(base, "logs"), RepoCipherPass: "test-only-backup-key", DatabaseURL: "postgres://test@live/db?sslmode=disable", RestoreDatabaseURL: "postgres://test@restore/db?sslmode=disable", PGDataPath: paths["pg"], RestoreDataPath: paths["restore-pg"], NoteAttachmentDir: paths["attachments"], AttachmentRestoreDir: paths["restore-attachments"]})
	cfg := DefaultConfig()
	cfg.Target.Path = paths["repo"]
	if _, err := w.service.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{filepath.Join(paths["pg"], "database"): "old database", filepath.Join(paths["attachments"], "file"): "old attachment", filepath.Join(paths["attachments"], "old-only"): "old only", filepath.Join(paths["archive-source"], "file"): "restored attachment", filepath.Join(paths["archive-source"], "preview"): "restored preview"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &pairedWorkerRunner{t: t, worker: w, failure: mode, rows: "1"}
	for _, name := range []string{"file", "preview"} {
		data, err := os.ReadFile(filepath.Join(paths["archive-source"], name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		runner.checksums += name + "\t" + fmt.Sprint(len(data)) + "\t" + hex.EncodeToString(hash[:]) + "\n"
	}
	w.runner = runner
	w.prepareRestoreDatabase = func(context.Context) error { return nil }
	if mode == "empty" {
		runner.rows = "0"
		runner.checksums = ""
	} else if mode != "missing archive" {
		archive := w.attachmentBackupPath(cfg, "20260905-020000F")
		if err := writeEncryptedAttachments(context.Background(), paths["archive-source"], archive, w.repoCipherPass); err != nil {
			t.Fatal(err)
		}
		if mode == "wrong key" {
			w.repoCipherPass = "different-test-only-key"
		}
		if mode == "corrupt archive" {
			if err := os.WriteFile(archive, []byte("corrupt archive"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return w, runner
}

func TestPrimaryRestoreValidatesBeforeStoppingLiveAndRollsBackFailures(t *testing.T) {
	for _, mode := range []string{"database restore", "missing archive", "wrong key", "corrupt archive", "database start", "database readiness", "cloud start"} {
		t.Run(mode, func(t *testing.T) {
			w, runner := primaryWorkerFixture(t, mode)
			ctx := context.Background()
			cancel := func() {}
			if mode == "database readiness" {
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
			}
			defer cancel()
			if err := w.RunPrimaryRestore(ctx, "20260905-020000F"); err == nil {
				t.Fatal("restore failure was ignored")
			}
			data, err := os.ReadFile(filepath.Join(w.pgDataPath, "database"))
			if err != nil || string(data) != "old database" {
				t.Fatal("failed restore changed original database")
			}
			data, err = os.ReadFile(filepath.Join(w.noteAttachmentDir, "file"))
			if err != nil || string(data) != "old attachment" {
				t.Fatal("failed restore changed original attachments")
			}
			if mode == "missing archive" || mode == "wrong key" || mode == "corrupt archive" || mode == "database restore" {
				for _, call := range runner.calls {
					if strings.Contains(call, " stop cloud postgres") {
						t.Fatal("preflight failure stopped live services")
					}
				}
			}
			if err := CheckPrimaryRestoreStartup(w.service.StateDir); err != nil {
				t.Fatal("recovered original pair remains blocked", err)
			}
		})
	}
}

func TestPrimaryRestorePublishesPairAndDoesNotReplayCommittedJournal(t *testing.T) {
	for _, mode := range []string{"success", "empty"} {
		t.Run(mode, func(t *testing.T) {
			w, runner := primaryWorkerFixture(t, mode)
			if err := w.RunPrimaryRestore(context.Background(), "20260905-020000F"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(w.pgDataPath, "database"))
			if err != nil || string(data) != "new database" {
				t.Fatal("new database not published")
			}
			if mode == "empty" {
				entries, err := rootEntries(w.noteAttachmentDir)
				if err != nil || len(entries) != 0 {
					t.Fatal("empty backup kept old live files")
				}
			} else {
				for _, name := range []string{"file", "preview"} {
					info, err := os.Stat(filepath.Join(w.noteAttachmentDir, name))
					if err != nil || info.Mode().Perm() != 0660 {
						t.Fatal("restored attachment permissions are not shared and private")
					}
				}
			}
			if _, err := os.Stat(filepath.Join(w.noteAttachmentDir, "old-only")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old-only file retained in live pair")
			}
			before := len(runner.calls)
			if err := w.RecoverPrimaryRestore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != before {
				t.Fatal("committed restore replayed orchestration")
			}
		})
	}
}

func TestPrimaryRestoreRecoveryRestartsValidPairBeforeArchiving(t *testing.T) {
	for _, phase := range []string{"committed", "rolled_back"} {
		t.Run(phase, func(t *testing.T) {
			w, runner := primaryWorkerFixture(t, "missing archive")
			p, err := newPairedRestore(w.service.StateDir, w.pgDataPath, w.noteAttachmentDir, "interrupted-runtime", "20260905-020000F")
			if err != nil {
				t.Fatal(err)
			}
			p.journal.Phase = phase
			if err := p.save(); err != nil {
				t.Fatal(err)
			}
			if err := w.RecoverPrimaryRestore(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls := strings.Join(runner.calls, "\n")
			if !strings.Contains(calls, " up -d postgres") || !strings.Contains(calls, " up -d --wait --wait-timeout 120 cloud") {
				t.Fatalf("runtime not restarted: %s", calls)
			}
			for _, call := range runner.calls {
				if strings.Contains(call, " restore") || strings.Contains(call, " stop ") {
					t.Fatalf("valid dataset was replaced: %s", call)
				}
			}
			journal, err := readPrimaryRestoreJournal(w.service.StateDir)
			if err != nil || !journal.RuntimeStarted {
				t.Fatal("runtime completion not persisted", err)
			}
			before := len(runner.calls)
			if err := w.RecoverPrimaryRestore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != before {
				t.Fatal("completed runtime recovery replayed")
			}
		})
	}
}
