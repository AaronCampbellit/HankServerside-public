package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAssistantDirectoryExclusiveAndContained(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	service := New(root)
	ctx := context.Background()
	if err := service.CreateDirectoryExclusiveSource(ctx, "", "new"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "new")); err != nil || !info.IsDir() {
		t.Fatal("directory not created")
	}
	if err := service.CreateDirectoryExclusiveSource(ctx, "", "new"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing target accepted: %v", err)
	}
	if err := service.CreateDirectoryExclusiveSource(ctx, "", "missing/child"); err == nil {
		t.Fatal("created unapproved parent")
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"../outside", "escape/child", "new/../../outside"} {
		if err := service.CreateDirectoryExclusiveSource(ctx, "", target); err == nil {
			t.Fatalf("escaped with %q", target)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside directory mutated")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := service.CreateDirectoryExclusiveSource(cancelled, "", "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled operation attempted")
	}
}
