package staging

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingInterruptedUploadRetryAndTampering(t *testing.T) {
	root := t.TempDir()
	content := "synthetic attachment"
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	key := Key("home", "owner", "session", "attachment", digest)
	if err := Put(root, key, digest, int64(len(content)), strings.NewReader("partial")); err == nil {
		t.Fatal("partial accepted")
	}
	if _, err := Open(root, key, digest, int64(len(content))); err == nil {
		t.Fatal("partial readable")
	}
	for i := 0; i < 2; i++ {
		if err := Put(root, key, digest, int64(len(content)), strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	file, err := Open(root, key, digest, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	file.Close()
	if string(data) != content {
		t.Fatal("readback mismatch")
	}
	if Key("home", "other", "session", "attachment", digest) == key {
		t.Fatal("owner not bound")
	}
	if err := os.WriteFile(filepath.Join(root, key), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, key, digest, int64(len(content))); err == nil {
		t.Fatal("corruption accepted")
	}
}
func TestStagingRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	sum := sha256.Sum256([]byte("x"))
	digest := hex.EncodeToString(sum[:])
	if err := Put(root, "../escape", digest, 1, strings.NewReader("x")); err == nil {
		t.Fatal("path accepted")
	}
	key := Key("h", "u", "s", "a", digest)
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, key, digest, 1); err == nil {
		t.Fatal("escaping symlink readable")
	}
}
