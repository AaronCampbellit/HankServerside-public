package cloud

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDocsContainmentAndBoundedReads(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside private canary"), 0600))
	must(t, os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "escape.md")))
	must(t, os.Mkdir(filepath.Join(root, "docs"), 0700))
	must(t, os.WriteFile(filepath.Join(root, "docs", "safe.md"), []byte("# Safe\n"+strings.Repeat("a", maxAssistantProjectDocBytes*2)), 0600))
	docs, err := loadAssistantProjectDocs(root)
	must(t, err)
	if len(docs) != 1 || docs[0].Path != "docs/safe.md" || len(docs[0].Content) > maxAssistantProjectDocBytes {
		t.Fatalf("unexpected bounded documents: count=%d", len(docs))
	}
}
