package cloud

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

// Explicitly opt in: this test sends only synthetic fixtures to the configured
// existing Ollama server. It never connects to a deployed Hank database/API.
func TestAssistantLiveBaseline(t *testing.T) {
	providerURL := os.Getenv("HANK_ASSISTANT_BASELINE_OLLAMA_URL")
	if providerURL == "" {
		t.Skip("live baseline requires an explicitly configured existing Ollama endpoint")
	}
	model := os.Getenv("HANK_ASSISTANT_BASELINE_MODEL")
	if model == "" {
		t.Fatal("HANK_ASSISTANT_BASELINE_MODEL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_baseline", Email: "baseline@example.invalid", PasswordHash: "disabled", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_baseline", UserID: user.ID, Name: "Synthetic baseline", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	token := newToken()
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_baseline", UserID: user.ID, TokenHash: hashToken(token), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	docRoot := t.TempDir()
	must(t, os.WriteFile(filepath.Join(docRoot, "README.md"), []byte("# Hank\nHank is a single-Home self-hosted platform for notes, files, machines and an assistant. Public clients use HTTPS APIs. Never expose raw SMB publicly.\n"), 0600))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer server.Shutdown(context.Background())
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: "ollama", OllamaBaseURL: providerURL, OllamaChatModel: model, OllamaEmbeddingModel: "nomic-embed-text:latest", EmbeddingDimension: 768, ProjectDocsDir: docRoot})
	must(t, server.indexAssistantProjectDocs(ctx, home.ID, user.ID))
	server.startAssistantIndexWorker(ctx)
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()
	root, err := filepath.Abs("../..")
	must(t, err)
	reportDir := filepath.Join(root, "data", "hankai-evals")
	cmd := exec.CommandContext(ctx, "go", "run", "./tools/hankaieval")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"HANK_LIVE_BASE_URL="+ts.URL, "HANK_LIVE_SESSION_TOKEN="+token,
		"HANK_HANKAI_EXPECT_PROVIDER=ollama", "HANK_HANKAI_EXPECT_MODEL="+model,
		"HANK_HANKAI_EVAL_GROUPS=provider,project_docs,notes,calendar,safety,status,multi_source",
		"HANK_HANKAI_EVAL_TIMEOUT_SECONDS=600", "HANK_HANKAI_EVAL_REPORT_DIR="+reportDir,
	)
	output, err := cmd.CombinedOutput()
	// The harness emits only static case names and safe outcome codes.
	t.Log(string(output))
	if err != nil {
		t.Fatal("synthetic live baseline failed; inspect its safe report")
	}
}
