package cloud

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

// Exercise the shipped evaluation CLI against the real HTTP API, worker and
// disposable PostgreSQL fixture. No existing Home or physical agent is exposed.
func TestExecutionProviderLiveOutcomeHarness(t *testing.T) {
	base, model := os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_OLLAMA"), os.Getenv("HANK_ASSISTANT_EXECUTION_LIVE_MODEL")
	if base == "" {
		t.Skip("opt-in synthetic live outcome evaluation")
	}
	if model == "" {
		t.Fatal("explicit model required")
	}
	s, _, user, _ := executionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	token := newID("eval-session")
	must(t, s.store.CreateSession(ctx, domain.AppSession{ID: newID("eval-auth"), UserID: user.ID, TokenHash: hashToken(token), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))
	s.ConfigureAssistantAI(AssistantAIConfig{ExecutionEnabled: true, Provider: "ollama", OllamaBaseURL: base, OllamaChatModel: model})
	server := httptest.NewServer(s.http.Handler)
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runAssistantExecutionWorker(ctx, "live-outcome-eval", s.newAssistantExecutionModel)
	}()
	defer func() { cancel(); <-done }()
	reportDir := t.TempDir()
	command := exec.CommandContext(ctx, "go", "run", "./tools/hankaieval")
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "HANK_LIVE_BASE_URL="+server.URL, "HANK_LIVE_SESSION_TOKEN="+token,
		"HANK_HANKAI_EVAL_GROUPS=execution", "HANK_HANKAI_EXECUTION_REPEATS=3", "HANK_HANKAI_EXPECT_PROVIDER=ollama",
		"HANK_HANKAI_EXPECT_MODEL="+model, "HANK_HANKAI_EXPECT_OLLAMA_URL="+base,
		"HANK_HANKAI_EVAL_RUN_ID=synthetic-outcomes", "HANK_HANKAI_EVAL_REPORT_DIR="+reportDir)
	output, err := command.CombinedOutput()
	t.Logf("%s", output)
	report, readErr := os.ReadFile(filepath.Join(reportDir, "synthetic-outcomes.json"))
	if readErr == nil {
		t.Logf("Synthetic outcome report: %s", report)
	}
	if err != nil {
		t.Fatalf("outcome harness failed: %v", err)
	}
}
