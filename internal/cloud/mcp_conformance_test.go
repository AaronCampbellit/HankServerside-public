package cloud

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/observability"
)

func TestMCP20260728Conformance(t *testing.T) {
	if os.Getenv("HANK_MCP_CONFORMANCE") != "1" {
		t.Skip("set HANK_MCP_CONFORMANCE=1 to run the pinned official MCP conformance scenarios")
	}

	metrics := observability.NewMetrics()
	server := &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: metrics,
		mcpDocs: newMCPDocsIndex(t.TempDir()),
	}
	server.mcpSubscriptions = newMCPSubscriptionHub(defaultMCPSubscriptionConfig(), metrics)
	auth := mcpAuthContext{
		User: domain.User{ID: "usr_conformance"},
		Token: domain.MCPToken{ID: "token_conformance", Scopes: []string{
			domain.MCPScopeDocsRead,
			domain.NotesAPIScopeRead,
			domain.NotesAPIScopeAppend,
			domain.NotesAPIScopeWrite,
			domain.NotesAPIScopeDelete,
		}},
	}
	harness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !server.mcpOriginAllowed(r) {
			writeJSON(w, http.StatusForbidden, jsonrpcErrorResponse(nil, -32000, "forbidden origin"))
			return
		}
		server.handleMCPAuthenticatedEndpoint(w, r, auth)
	}))
	defer harness.Close()

	for _, scenario := range []string{
		"tools-list",
	} {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			outputDir := filepath.Join(t.TempDir(), "results")
			command := exec.Command(
				"npx", "-y", "@modelcontextprotocol/conformance@0.2.0-alpha.9",
				"server",
				"--url", harness.URL,
				"--scenario", scenario,
				"--spec-version", mcpModernProtocolVersion,
				"--output-dir", outputDir,
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("official conformance scenario %s failed: %v\n%s", scenario, err, output)
			}
		})
	}
}
