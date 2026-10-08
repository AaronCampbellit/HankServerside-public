package files

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceRevisionChangesWithRootAndKeyWithoutExposingThem(t *testing.T) {
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	first := New(firstRoot).SearchSourcesWithRevision("agent-token")[0].Revision
	stable := New(firstRoot).SearchSourcesWithRevision("agent-token")[0].Revision
	second := New(secondRoot).SearchSourcesWithRevision("agent-token")[0].Revision
	changedKey := New(firstRoot).SearchSourcesWithRevision("rotated-token")[0].Revision
	if first == "" || first != stable || first == second || first == changedKey || strings.Contains(first, firstRoot) {
		t.Fatalf("source revision did not hide or distinguish roots and keys")
	}
}

func TestListPageSourceBoundsFramesAndKeepsSnapshot(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1201; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := New(root)
	ctx := context.Background()
	cursor := ""
	seen := make(map[string]bool)
	pages := 0
	for {
		page, err := service.ListPageSource(ctx, LocalSourceID, "", cursor)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) >= 2<<20 {
			t.Fatalf("page bytes %d, marshal error %v", len(encoded), err)
		}
		pages++
		for _, item := range page.Items {
			if seen[item.Path] {
				t.Fatalf("duplicate item %q", item.Path)
			}
			seen[item.Path] = true
		}
		if page.NextCursor == "" {
			break
		}
		if pages == 1 {
			if _, err := service.ListPageSource(ctx, LocalSourceID, "other", page.NextCursor); err == nil {
				t.Fatal("cursor accepted for another path")
			}
		}
		cursor = page.NextCursor
	}
	if pages < 3 || len(seen) != 1201 {
		t.Fatalf("pages=%d items=%d", pages, len(seen))
	}
	if _, err := service.ListPageSource(ctx, LocalSourceID, "", cursor); err == nil {
		t.Fatal("consumed cursor accepted")
	}
}
