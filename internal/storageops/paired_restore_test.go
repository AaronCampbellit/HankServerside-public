package storageops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func pairedFixture(t *testing.T, empty bool) *pairedRestore {
	t.Helper()
	base := t.TempDir()
	pg, attachments, restoredPG, restoredAttachments := filepath.Join(base, "pg"), filepath.Join(base, "attachments"), filepath.Join(base, "restored-pg"), filepath.Join(base, "restored-attachments")
	for _, path := range []string{pg, attachments, restoredPG, restoredAttachments} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{filepath.Join(pg, "database"): "original database", filepath.Join(attachments, "shared"): "original attachment", filepath.Join(attachments, "old-only"): "original only", filepath.Join(restoredPG, "database"): "restored database"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !empty {
		for name, value := range map[string]string{"shared": "restored attachment", "new-only": "new only"} {
			if err := os.WriteFile(filepath.Join(restoredAttachments, name), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	p, err := newPairedRestore(filepath.Join(base, "state"), pg, attachments, "task", "20260905-020000F")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.prepare(context.Background(), restoredPG, restoredAttachments); err != nil {
		t.Fatal(err)
	}
	if err := p.save(); err != nil {
		t.Fatal(err)
	}
	return p
}
func assertOriginalPair(t *testing.T, p *pairedRestore) {
	t.Helper()
	for path, want := range map[string]string{filepath.Join(p.journal.PGRoot, "database"): "original database", filepath.Join(p.journal.AttachmentRoot, "shared"): "original attachment", filepath.Join(p.journal.AttachmentRoot, "old-only"): "original only"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("original pair not restored at %s: %q %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.journal.AttachmentRoot, "new-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restored-only bytes leaked into original pair")
	}
}
func reloadPair(t *testing.T, p *pairedRestore) *pairedRestore {
	t.Helper()
	j, err := readPrimaryRestoreJournal(p.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return &pairedRestore{stateDir: p.stateDir, journal: *j}
}

func TestPairedRestoreCrashAtEveryPublicationRenameRecoversBothOriginals(t *testing.T) {
	for _, after := range []bool{false, true} {
		for failure := 1; failure <= 6; failure++ {
			t.Run(fmt.Sprintf("rename-%d-after-%v", failure, after), func(t *testing.T) {
				p := pairedFixture(t, false)
				count := 0
				p.rename = func(from, to string) error {
					count++
					if count == failure && !after {
						return errors.New("injected crash")
					}
					err := durableRename(from, to)
					if err == nil && count == failure && after {
						return errors.New("injected crash after rename")
					}
					return err
				}
				if err := p.publish(); err == nil {
					t.Fatal("fault was not exercised")
				}
				if err := CheckPrimaryRestoreStartup(p.stateDir); err == nil {
					t.Fatal("cloud startup accepted an interrupted pair")
				}
				recovered := reloadPair(t, p)
				if err := recovered.rollback(); err != nil {
					t.Fatal(err)
				}
				assertOriginalPair(t, recovered)
				if err := CheckPrimaryRestoreStartup(p.stateDir); err != nil {
					t.Fatal(err)
				}
				if err := reloadPair(t, recovered).rollback(); err != nil {
					t.Fatal(err)
				}
				assertOriginalPair(t, recovered)
			})
		}
	}
}

func TestPairedRestoreRollbackSurvivesAnotherCrash(t *testing.T) {
	for _, after := range []bool{false, true} {
		for failure := 1; failure <= 6; failure++ {
			t.Run(fmt.Sprintf("rollback-%d-after-%v", failure, after), func(t *testing.T) {
				p := pairedFixture(t, false)
				if err := p.publish(); err != nil {
					t.Fatal(err)
				}
				count := 0
				p.rename = func(from, to string) error {
					count++
					if count == failure && !after {
						return errors.New("rollback interrupted")
					}
					err := durableRename(from, to)
					if err == nil && count == failure && after {
						return errors.New("rollback interrupted after rename")
					}
					return err
				}
				if err := p.rollback(); err == nil {
					t.Fatal("rollback fault not exercised")
				}
				if err := CheckPrimaryRestoreStartup(p.stateDir); err == nil {
					t.Fatal("partial rollback allowed startup")
				}
				recovered := reloadPair(t, p)
				if err := recovered.rollback(); err != nil {
					t.Fatal(err)
				}
				assertOriginalPair(t, recovered)
			})
		}
	}
}

func TestPairedRestoreCommitsMatchingOrEmptyAttachments(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty-%v", empty), func(t *testing.T) {
			p := pairedFixture(t, empty)
			if err := p.publish(); err != nil {
				t.Fatal(err)
			}
			db, err := os.ReadFile(filepath.Join(p.journal.PGRoot, "database"))
			if err != nil || string(db) != "restored database" {
				t.Fatal("wrong database published")
			}
			entries, err := rootEntries(p.journal.AttachmentRoot)
			if err != nil {
				t.Fatal(err)
			}
			if empty && len(entries) != 0 {
				t.Fatal("empty restored database retained live attachments")
			}
			if !empty {
				data, err := os.ReadFile(filepath.Join(p.journal.AttachmentRoot, "shared"))
				if err != nil || string(data) != "restored attachment" {
					t.Fatal("database/attachment mismatch")
				}
			}
			if err := CheckPrimaryRestoreStartup(p.stateDir); err == nil {
				t.Fatal("unverified pair allowed startup")
			}
			p.journal.Phase = "committed"
			if err := p.save(); err != nil {
				t.Fatal(err)
			}
			if err := CheckPrimaryRestoreStartup(p.stateDir); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(p.pgOld(), "database")); err != nil || string(data) != "original database" {
				t.Fatal("rollback database was not retained")
			}
		})
	}
}

func TestPairedRestoreRejectsEscapingRecoveryDirectory(t *testing.T) {
	base := t.TempDir()
	pg, attachments := filepath.Join(base, "pg"), filepath.Join(base, "attachments")
	for _, path := range []string{pg, attachments} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(attachments, RecoveryDirectoryName)); err != nil {
		t.Fatal(err)
	}
	if _, err := newPairedRestore(filepath.Join(base, "state"), pg, attachments, "task", "label"); err == nil {
		t.Fatal("symlink recovery directory accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("restore wrote outside configured roots")
	}
}
