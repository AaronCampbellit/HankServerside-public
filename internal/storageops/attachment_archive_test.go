package storageops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedAttachmentArchiveRoundTripAndTampering(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	archive := filepath.Join(t.TempDir(), "backup.tar.gz.age")
	data := make([]byte, 150000)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	mustArchive(t, os.Mkdir(filepath.Join(source, "nested"), 0700))
	mustArchive(t, os.WriteFile(filepath.Join(source, "nested", "file"), data, 0600))
	key := "fixture-backup-repository-key"
	mustArchive(t, writeEncryptedAttachments(ctx, source, archive, key))
	encrypted, err := os.ReadFile(archive)
	mustArchive(t, err)
	if bytes.Contains(encrypted, data[:50]) || bytes.Contains(encrypted, []byte(key)) {
		t.Fatal("plaintext in encrypted archive")
	}
	if _, err = gzip.NewReader(bytes.NewReader(encrypted)); err == nil {
		t.Fatal("archive readable as plain gzip")
	}
	info, err := os.Stat(archive)
	mustArchive(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive is not private")
	}
	destination := t.TempDir()
	mustArchive(t, extractAttachmentArchive(ctx, archive, destination, key, true))
	got, err := os.ReadFile(filepath.Join(destination, "nested", "file"))
	mustArchive(t, err)
	if !bytes.Equal(got, data) {
		t.Fatal("round trip changed content")
	}
	altered := append([]byte(nil), encrypted...)
	altered[len(altered)/2] ^= 1
	swapped := append([]byte(nil), encrypted...)
	copy(swapped[1000:2000], encrypted[2000:3000])
	copy(swapped[2000:3000], encrypted[1000:2000])
	for name, value := range map[string][]byte{"bit_flip": altered, "truncated": encrypted[:len(encrypted)-1], "chunk_boundary_truncation": encrypted[:65536], "reordered": swapped, "trailing_bytes": append(append([]byte(nil), encrypted...), 1)} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "bad.age")
			mustArchive(t, os.WriteFile(file, value, 0600))
			if extractAttachmentArchive(ctx, file, t.TempDir(), key, true) == nil {
				t.Fatal("corrupt archive accepted")
			}
		})
	}
	if extractAttachmentArchive(ctx, archive, t.TempDir(), "wrong-key", true) == nil {
		t.Fatal("wrong key accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if writeEncryptedAttachments(cancelled, source, archive, key) == nil {
		t.Fatal("cancelled backup succeeded")
	}
	after, err := os.ReadFile(archive)
	mustArchive(t, err)
	if !bytes.Equal(after, encrypted) {
		t.Fatal("failed backup replaced completed archive")
	}
	mustArchive(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(source, "link")))
	if writeEncryptedAttachments(ctx, source, archive, key) == nil {
		t.Fatal("source symlink accepted")
	}
	after, err = os.ReadFile(archive)
	mustArchive(t, err)
	if !bytes.Equal(after, encrypted) {
		t.Fatal("invalid source replaced archive")
	}
	entries, err := os.ReadDir(filepath.Dir(archive))
	mustArchive(t, err)
	if len(entries) != 1 {
		t.Fatal("temporary archive left behind")
	}
}

func writeLegacyArchive(t *testing.T, file, name string, kind byte, data []byte) {
	t.Helper()
	out, err := os.Create(file)
	mustArchive(t, err)
	zip := gzip.NewWriter(out)
	tarball := tar.NewWriter(zip)
	mustArchive(t, tarball.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0700}))
	if strings.HasPrefix(name, "nested/") {
		mustArchive(t, tarball.WriteHeader(&tar.Header{Name: "./nested/", Typeflag: tar.TypeDir, Mode: 0700}))
	}
	header := &tar.Header{Name: name, Typeflag: kind, Mode: 0600, Linkname: "../outside"}
	if kind == tar.TypeReg {
		header.Size = int64(len(data))
	}
	mustArchive(t, tarball.WriteHeader(header))
	if kind == tar.TypeReg {
		_, err = tarball.Write(data)
		mustArchive(t, err)
	}
	mustArchive(t, tarball.Close())
	mustArchive(t, zip.Close())
	mustArchive(t, out.Close())
}

func TestLegacyAttachmentArchivePaths(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "legacy.tar.gz")
	writeLegacyArchive(t, file, "nested/file", tar.TypeReg, []byte("legacy"))
	destination := t.TempDir()
	mustArchive(t, extractAttachmentArchive(ctx, file, destination, "", false))
	got, err := os.ReadFile(filepath.Join(destination, "nested", "file"))
	mustArchive(t, err)
	if string(got) != "legacy" {
		t.Fatal("legacy recovery failed")
	}
	for _, test := range []struct {
		name string
		kind byte
	}{{"../escape", tar.TypeReg}, {"/absolute", tar.TypeReg}, {"link", tar.TypeSymlink}, {"hardlink", tar.TypeLink}, {"device", tar.TypeChar}} {
		writeLegacyArchive(t, file, test.name, test.kind, []byte("private"))
		if extractAttachmentArchive(ctx, file, t.TempDir(), "", false) == nil {
			t.Fatalf("unsafe entry accepted: %s", test.name)
		}
	}
}

func TestAttachmentRestorePrefersEncryptedAndValidatesHash(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	destination := t.TempDir()
	payload := []byte("expected")
	mustArchive(t, os.WriteFile(filepath.Join(source, "file"), payload, 0600))
	sum := sha256.Sum256(payload)
	runner := &scriptedRunner{responses: []scriptedResponse{{contains: "SELECT count(*) FROM note_attachments", output: "1"}, {contains: "SELECT storage_key", output: fmt.Sprintf("file\t%d\t%x", len(payload), sum)}}}
	worker := NewWorker(WorkerOptions{StateDir: t.TempDir(), LogDir: t.TempDir(), NoteAttachmentDir: source, AttachmentRestoreDir: destination, RepoCipherPass: "fixture-key", Runner: runner})
	cfg := DefaultConfig()
	cfg.Target.Path = t.TempDir()
	_, err := worker.service.SaveConfig(cfg)
	mustArchive(t, err)
	label := "20260905-020000F"
	_, err = worker.runAttachmentBackup(ctx, cfg, label)
	mustArchive(t, err)
	encrypted := worker.attachmentBackupPath(cfg, label)
	legacy := strings.TrimSuffix(encrypted, ".age")
	writeLegacyArchive(t, legacy, "file", tar.TypeReg, payload)
	mustArchive(t, os.WriteFile(filepath.Join(destination, "previous"), []byte("preserve"), 0600))
	worker.repoCipherPass = "wrong"
	if _, err = worker.validateAttachmentBackupRestore(ctx, label); err == nil {
		t.Fatal("wrong encrypted key fell back to legacy")
	}
	if _, err = os.Stat(filepath.Join(destination, "previous")); err != nil {
		t.Fatal("failed authentication cleared prior recovery")
	}
	worker.repoCipherPass = "fixture-key"
	_, err = worker.validateAttachmentBackupRestore(ctx, label)
	mustArchive(t, err)
	mustArchive(t, os.WriteFile(filepath.Join(destination, "file"), []byte("tampered"), 0600))
	if _, err = worker.validateRestoredAttachmentFiles(ctx); err == nil {
		t.Fatal("same-size content corruption accepted")
	}
	mustArchive(t, os.Remove(encrypted))
	details, err := worker.validateAttachmentBackupRestore(ctx, label)
	mustArchive(t, err)
	if details["encrypted"] != false {
		t.Fatal("legacy restore not identified")
	}
	if _, err = worker.validateAttachmentBackupRestore(ctx, "../escape"); err == nil {
		t.Fatal("backup label traversal accepted")
	}
}

func mustArchive(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyGNUTarHardLinksAndSparseFiles(t *testing.T) {
	source := t.TempDir()
	file, err := os.Create(filepath.Join(source, "first"))
	mustArchive(t, err)
	_, err = file.WriteAt([]byte("tail"), 4<<20)
	mustArchive(t, err)
	mustArchive(t, file.Close())
	mustArchive(t, os.Link(filepath.Join(source, "first"), filepath.Join(source, "second")))
	archive := filepath.Join(t.TempDir(), "legacy.tar.gz")
	command := exec.Command("tar", "--sparse", "-C", source, "-czf", archive, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("legacy writer: %v %s", err, output)
	}
	destination := t.TempDir()
	mustArchive(t, extractAttachmentArchive(context.Background(), archive, destination, "", false))
	for _, name := range []string{"first", "second"} {
		data, err := os.ReadFile(filepath.Join(destination, name))
		mustArchive(t, err)
		if len(data) != (4<<20)+4 || string(data[len(data)-4:]) != "tail" {
			t.Fatal("legacy sparse/hard-link data mismatch")
		}
	}
}
