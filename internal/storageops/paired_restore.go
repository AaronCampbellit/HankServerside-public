package storageops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// RecoveryDirectoryName is private operator state, never attachment content.
const RecoveryDirectoryName = ".hank-recovery"
const primaryRestoreJournalName = "primary-restore.json"

// The journal precedes each destructive phase. Paths are derived from trusted
// worker configuration, not accepted from journal contents.
type pairedRestoreJournal struct {
	Version         int      `json:"version"`
	ID              string   `json:"id"`
	RuntimeStarted  bool     `json:"runtime_started"`
	TaskID          string   `json:"task_id"`
	BackupLabel     string   `json:"backup_label"`
	PGRoot          string   `json:"pg_root"`
	AttachmentRoot  string   `json:"attachment_root"`
	Phase           string   `json:"phase"`
	AttachmentPhase string   `json:"attachment_phase"`
	OldEntries      []string `json:"old_entries"`
	NewEntries      []string `json:"new_entries"`
}

type pairedRestore struct {
	stateDir string
	journal  pairedRestoreJournal
	// Tests inject filesystem failures at the same boundary as production renames.
	rename func(string, string) error
}

func (p *pairedRestore) pgRecovery() string {
	return filepath.Join(filepath.Dir(p.journal.PGRoot), RecoveryDirectoryName, p.journal.ID)
}
func (p *pairedRestore) attachmentRecovery() string {
	return filepath.Join(p.journal.AttachmentRoot, RecoveryDirectoryName, p.journal.ID)
}
func (p *pairedRestore) pgNew() string         { return filepath.Join(p.pgRecovery(), "new") }
func (p *pairedRestore) pgOld() string         { return filepath.Join(p.pgRecovery(), "old") }
func (p *pairedRestore) attachmentNew() string { return filepath.Join(p.attachmentRecovery(), "new") }
func (p *pairedRestore) attachmentOld() string { return filepath.Join(p.attachmentRecovery(), "old") }

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func durableRename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(from)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(to))
}
func (p *pairedRestore) move(from, to string) error {
	if p.rename != nil {
		return p.rename(from, to)
	}
	return durableRename(from, to)
}
func (p *pairedRestore) save() error {
	if err := os.MkdirAll(p.stateDir, 0770); err != nil {
		return err
	}
	data, err := json.Marshal(p.journal)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.stateDir, ".primary-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0640); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return durableRename(f.Name(), filepath.Join(p.stateDir, primaryRestoreJournalName))
}
func readPrimaryRestoreJournal(stateDir string) (*pairedRestoreJournal, error) {
	path := filepath.Join(stateDir, primaryRestoreJournalName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("invalid primary restore journal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j pairedRestoreJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, errors.New("cannot decode primary restore journal")
	}
	if j.TaskID != "" && (filepath.Base(j.TaskID) != j.TaskID || j.TaskID == "." || j.TaskID == "..") {
		return nil, errors.New("invalid restore task identifier")
	}
	if j.Version != 1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`).MatchString(j.ID) {
		return nil, errors.New("unsupported primary restore journal")
	}
	switch j.Phase {
	case "prepared", "swapping", "rolling_back", "committed", "rolled_back":
	default:
		return nil, errors.New("unknown primary restore phase")
	}
	switch j.AttachmentPhase {
	case "", "moving_old", "moving_new", "published", "restoring_old", "restored":
	default:
		return nil, errors.New("unknown attachment restore phase")
	}
	for _, names := range [][]string{j.OldEntries, j.NewEntries} {
		seen := map[string]bool{}
		for _, name := range names {
			if name == "" || name == "." || name == ".." || name == RecoveryDirectoryName || filepath.Base(name) != name || seen[name] {
				return nil, errors.New("invalid restore entry")
			}
			seen[name] = true
		}
	}
	return &j, nil
}

// CheckPrimaryRestoreStartup must run before opening the durable store or
// repairing attachment permissions. A committed journal describes a verified
// pair; every unfinished phase blocks cloud startup.
func CheckPrimaryRestoreStartup(stateDir string) error {
	if stateDir == "" {
		stateDir = NewService("", "", "").StateDir
	}
	journal, err := readPrimaryRestoreJournal(stateDir)
	if err != nil {
		return err
	}
	if journal != nil && journal.Phase != "committed" && journal.Phase != "rolled_back" {
		return errors.New("primary restore is incomplete; database operations must recover the matched pair before cloud startup")
	}
	return nil
}

func newPairedRestore(stateDir, pgRoot, attachmentRoot, taskID, label string) (*pairedRestore, error) {
	pgRoot, err := filepath.Abs(pgRoot)
	if err != nil {
		return nil, err
	}
	attachmentRoot, err = filepath.Abs(attachmentRoot)
	if err != nil {
		return nil, err
	}
	for _, root := range []string{pgRoot, attachmentRoot} {
		info, err := os.Lstat(root)
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root || !info.IsDir() || root == string(filepath.Separator) {
			return nil, errors.New("restore roots must be real, non-root directories")
		}
	}
	if containsPath(pgRoot, attachmentRoot) || containsPath(attachmentRoot, pgRoot) {
		return nil, errors.New("restore datasets must have separate roots")
	}
	p := &pairedRestore{stateDir: stateDir, journal: pairedRestoreJournal{Version: 1, ID: rand.Text(), TaskID: taskID, BackupLabel: label, PGRoot: pgRoot, AttachmentRoot: attachmentRoot, Phase: "prepared"}}
	for _, base := range []string{filepath.Dir(p.pgRecovery()), filepath.Dir(p.attachmentRecovery())} {
		if err := ensureRecoveryDirectory(base); err != nil {
			return nil, err
		}
	}
	for _, path := range []string{p.pgRecovery(), p.attachmentRecovery(), p.attachmentOld()} {
		if err := ensureRecoveryDirectory(path); err != nil {
			return nil, err
		}
	}
	return p, nil
}
func containsPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !(len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator))
}

// copyRestoreTree accepts regular files and directories only. A PostgreSQL
// tablespace/WAL symlink requires a separately planned restore layout.
func copyRestoreTree(ctx context.Context, source, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	var directories []string
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("restore data contains a link or special file")
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			directories = append(directories, target)
			if relative != "." {
				if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
					return err
				}
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, archiveContextReader{ctx, in})
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDirectory(directories[i]); err != nil {
			return err
		}
	}
	return syncDirectory(filepath.Dir(destination))
}
func rootEntries(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == RecoveryDirectoryName {
			continue
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil, errors.New("restore root contains a link or special file")
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
func (p *pairedRestore) prepare(ctx context.Context, restoredPG, restoredAttachments string) error {
	if err := copyRestoreTree(ctx, restoredPG, p.pgNew()); err != nil {
		return err
	}
	if err := copyRestoreTree(ctx, restoredAttachments, p.attachmentNew()); err != nil {
		return err
	}
	var err error
	p.journal.NewEntries, err = rootEntries(p.attachmentNew())
	if err != nil {
		return err
	}
	// Old entries are captured after cloud stops, immediately before cutover.
	return nil
}
func (p *pairedRestore) publish() error {
	var err error
	p.journal.OldEntries, err = rootEntries(p.journal.AttachmentRoot)
	if err != nil {
		return err
	}
	p.journal.Phase = "swapping"
	if err = p.save(); err != nil {
		return err
	}
	if err = p.move(p.journal.PGRoot, p.pgOld()); err != nil {
		return err
	}
	if err = p.move(p.pgNew(), p.journal.PGRoot); err != nil {
		return err
	}
	p.journal.AttachmentPhase = "moving_old"
	if err = p.save(); err != nil {
		return err
	}
	for _, name := range p.journal.OldEntries {
		if err = p.move(filepath.Join(p.journal.AttachmentRoot, name), filepath.Join(p.attachmentOld(), name)); err != nil {
			return err
		}
	}
	p.journal.AttachmentPhase = "moving_new"
	if err = p.save(); err != nil {
		return err
	}
	for _, name := range p.journal.NewEntries {
		if err = p.move(filepath.Join(p.attachmentNew(), name), filepath.Join(p.journal.AttachmentRoot, name)); err != nil {
			return err
		}
	}
	p.journal.AttachmentPhase = "published"
	return p.save()
}
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
func (p *pairedRestore) rollback() error {
	p.journal.RuntimeStarted = false
	p.journal.Phase = "rolling_back"
	if err := p.save(); err != nil {
		return err
	}
	phase := p.journal.AttachmentPhase
	if phase == "moving_new" || phase == "published" {
		for _, name := range p.journal.NewEntries {
			source := filepath.Join(p.journal.AttachmentRoot, name)
			target := filepath.Join(p.attachmentNew(), name)
			live, err := exists(source)
			if err != nil {
				return err
			}
			staged, err := exists(target)
			if err != nil {
				return err
			}
			if live && staged {
				return fmt.Errorf("ambiguous attachment rollback entry %q", name)
			}
			if live {
				if err := p.move(source, target); err != nil {
					return err
				}
			}
		}
	}
	if phase != "" && phase != "restored" {
		p.journal.AttachmentPhase = "restoring_old"
		if err := p.save(); err != nil {
			return err
		}
		for _, name := range p.journal.OldEntries {
			source := filepath.Join(p.attachmentOld(), name)
			target := filepath.Join(p.journal.AttachmentRoot, name)
			old, err := exists(source)
			if err != nil {
				return err
			}
			live, err := exists(target)
			if err != nil {
				return err
			}
			if old && live {
				return fmt.Errorf("ambiguous original attachment %q", name)
			}
			if old {
				if err := p.move(source, target); err != nil {
					return err
				}
			} else if !live {
				return errors.New("original attachment entry is missing")
			}
		}
		p.journal.AttachmentPhase = "restored"
		if err := p.save(); err != nil {
			return err
		}
	}
	old, err := exists(p.pgOld())
	if err != nil {
		return err
	}
	if old {
		live, err := exists(p.journal.PGRoot)
		if err != nil {
			return err
		}
		if live {
			if err := p.move(p.journal.PGRoot, filepath.Join(p.pgRecovery(), "failed")); err != nil {
				return err
			}
		}
		if err := p.move(p.pgOld(), p.journal.PGRoot); err != nil {
			return err
		}
	}
	live, err := exists(p.journal.PGRoot)
	if err != nil {
		return err
	}
	if !live {
		return errors.New("original database directory is missing")
	}
	p.journal.Phase = "rolled_back"
	return p.save()
}

func ensureRecoveryDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("recovery directory must be a private real directory")
	}
	// Persist the directory entry itself before a journal can refer to it or
	// original data can move underneath it. Syncing only its children is insufficient.
	return syncDirectory(filepath.Dir(path))
}
