package cloud

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDashboardSearchURLTargetsExactItem(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		key   string
		value string
		want  string
	}{
		{name: "note", path: "/dashboard/profile-notes", key: "note", value: "note/roof", want: "/dashboard/profile-notes?note=note%2Froof"},
		{name: "app", path: "/dashboard/settings/apps", key: "app", value: "weather station", want: "/dashboard/settings/apps?app=weather+station"},
		{name: "member", path: "/dashboard/settings/people", key: "member", value: "owner@example.com", want: "/dashboard/settings/people?member=owner%40example.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dashboardSearchURL(test.path, test.key, test.value); got != test.want {
				t.Fatalf("dashboardSearchURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFileSearchURLKeepsExactAgentSourceAndPath(t *testing.T) {
	got := fileSearchURL(indexedFileResult{FileItem: protocol.FileItem{SourceID: "share one", Path: "Taxes/2026 & private.pdf"}, AgentID: "agent-two"})
	for _, expected := range []string{"agent_id=agent-two", "source_id=share+one", "path=Taxes%2F2026+%26+private.pdf", "preview=1"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("file search URL %q misses %q", got, expected)
		}
	}
}

func TestFileCatalogPageUsesReadPolicy(t *testing.T) {
	body, err := json.Marshal(protocol.FilesListPageRequest{SourceID: "share", Path: "Allowed/Archive", Cursor: "opaque"})
	if err != nil {
		t.Fatal(err)
	}
	checks, _, err := fileCommandPolicyChecks(protocol.RoutedCommand{Command: "files.list_page", Body: body})
	if err != nil || len(checks) != 1 || checks[0].action != "read" || checks[0].path != "Allowed/Archive" {
		t.Fatalf("page policy checks = %+v, %v", checks, err)
	}
}
