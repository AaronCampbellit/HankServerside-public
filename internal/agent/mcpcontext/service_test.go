package mcpcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestLiveContextListSearchReadAndSafety(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("Projects/App/main.go", "package main\n// distinctive widget\n")
	mustWrite("Projects/App/README.md", "# App")
	mustWrite("Projects/App/.env", "TOKEN=secret")
	mustWrite("Projects/App/vendor/hidden.go", "package hidden // distinctive")
	service := New(agentfiles.New(root))
	ctx := context.Background()
	list, err := service.List(ctx, protocol.MCPContextListRequest{SourceID: agentfiles.LocalSourceID, RootPath: "Projects/App"})
	if err != nil || len(list.Entries) != 2 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	read, err := service.Read(ctx, protocol.MCPContextReadRequest{SourceID: agentfiles.LocalSourceID, RootPath: "Projects/App", Path: "main.go"})
	if err != nil || !strings.Contains(read.Content, "distinctive widget") {
		t.Fatalf("read = %#v, %v", read, err)
	}
	search, err := service.Search(ctx, protocol.MCPContextSearchRequest{SourceID: agentfiles.LocalSourceID, RootPath: "Projects/App", Query: "distinctive"})
	if err != nil || len(search.Results) != 1 || search.Results[0].Path != "main.go" {
		t.Fatalf("search = %#v, %v", search, err)
	}
	for _, bad := range []string{"../outside.go", "/etc/passwd", ".env", "vendor/hidden.go"} {
		if _, err := service.Read(ctx, protocol.MCPContextReadRequest{SourceID: agentfiles.LocalSourceID, RootPath: "Projects/App", Path: bad}); err == nil {
			t.Fatalf("read %q unexpectedly allowed", bad)
		}
	}
}
