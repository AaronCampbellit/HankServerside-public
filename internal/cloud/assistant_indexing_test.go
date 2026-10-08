package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAssistantConversationReindexChecksJobScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	owner := domain.User{ID: "usr_reindex_owner", Email: "reindex-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	other := domain.User{ID: "usr_reindex_other", Email: "reindex-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	ownerHome := domain.Home{ID: "home_reindex_owner", UserID: owner.ID, Name: "Owner", CreatedAt: now, UpdatedAt: now}
	otherHome := domain.Home{ID: "home_reindex_other", UserID: other.ID, Name: "Other", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, owner))
	must(t, db.CreateUser(ctx, other))
	must(t, db.CreateHome(ctx, ownerHome))
	must(t, db.CreateHome(ctx, otherHome))
	session := domain.AssistantSession{ID: "asess_reindex_scope", HomeID: ownerHome.ID, UserID: owner.ID, Title: "Private", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateAssistantSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := server.processAssistantIndexJob(ctx, domain.AssistantIndexJob{HomeID: otherHome.ID, UserID: other.ID, SourceType: assistantConversationSourceType, SourceID: session.ID})
	if err == nil {
		t.Fatal("conversation reindex accepted a session from another Home and user")
	}
	if err := server.processAssistantIndexJob(ctx, domain.AssistantIndexJob{HomeID: ownerHome.ID, UserID: owner.ID, SourceType: assistantConversationSourceType, SourceID: session.ID}); err != nil {
		t.Fatalf("owned conversation reindex failed: %v", err)
	}
	results, err := db.SearchAssistantContext(ctx, ownerHome.ID, owner.ID, "Private", nil, "", 5)
	if err != nil || len(results) == 0 || results[0].SourceID != session.ID {
		t.Fatalf("owned conversation not indexed: %#v, %v", results, err)
	}
}

func TestAssistantProjectDocIndexJobUsesUserEmbeddingSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_index_model_override", Email: "index-model-override@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_index_model_override", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "per-user-embed" {
			http.Error(w, "wrong embedding model", http.StatusBadRequest)
			return
		}
		embedding := make([]float64, 768)
		embedding[0] = 1
		writeJSON(w, http.StatusOK, map[string]any{"embedding": embedding})
	}))
	defer provider.Close()

	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# User embedding settings\nA private project-document retrieval test."), 0o600))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{OllamaBaseURL: "http://127.0.0.1:1", OllamaEmbeddingModel: "global-embed", ProjectDocsDir: root})
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.OllamaBaseURL = provider.URL
	settings.EmbeddingModel = "per-user-embed"
	must(t, db.UpsertAssistantSettings(ctx, settings))

	if err := server.processAssistantIndexJob(ctx, domain.AssistantIndexJob{HomeID: home.ID, UserID: user.ID, SourceType: assistantIndexSourceProjectDocs}); err != nil {
		t.Fatalf("project docs index job ignored user embedding settings: %v", err)
	}
	queryEmbedding := make([]float64, 768)
	queryEmbedding[0] = 1
	results, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "no lexical overlap", queryEmbedding, "per-user-embed", 5)
	if err != nil || len(results) == 0 || results[0].SourceType != assistantProjectDocSourceType {
		t.Fatalf("per-user model vector was not indexed: %#v, %v", results, err)
	}
}

func TestAssistantProjectDocRefreshPrunesOnlyStaleHomeIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	firstUser := domain.User{ID: "usr_docs_prune_first", Email: "docs-prune-first@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	secondUser := domain.User{ID: "usr_docs_prune_second", Email: "docs-prune-second@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	firstHome := domain.Home{ID: "home_docs_prune_first", UserID: firstUser.ID, Name: "First", CreatedAt: now, UpdatedAt: now}
	secondHome := domain.Home{ID: "home_docs_prune_second", UserID: secondUser.ID, Name: "Second", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, firstUser))
	must(t, db.CreateUser(ctx, secondUser))
	must(t, db.CreateHome(ctx, firstHome))
	must(t, db.CreateHome(ctx, secondHome))

	root := t.TempDir()
	docsDir := filepath.Join(root, "docs")
	must(t, os.MkdirAll(docsDir, 0o700))
	must(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# Current project guide"), 0o600))
	stalePath := filepath.Join(docsDir, "stale.md")
	must(t, os.WriteFile(stalePath, []byte("# Old guide\nuniquestalemarker"), 0o600))
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{ProjectDocsDir: root})
	must(t, server.indexAssistantProjectDocs(ctx, firstHome.ID, firstUser.ID))
	must(t, server.indexAssistantProjectDocs(ctx, secondHome.ID, secondUser.ID))
	must(t, os.Remove(stalePath))
	must(t, server.indexAssistantProjectDocs(ctx, firstHome.ID, firstUser.ID))

	firstResults, err := db.SearchAssistantContext(ctx, firstHome.ID, firstUser.ID, "uniquestalemarker", nil, "", 5)
	must(t, err)
	if len(firstResults) != 0 {
		t.Fatalf("stale project doc remained in refreshed Home: %#v", firstResults)
	}
	secondResults, err := db.SearchAssistantContext(ctx, secondHome.ID, secondUser.ID, "uniquestalemarker", nil, "", 5)
	must(t, err)
	if len(secondResults) == 0 || secondResults[0].SourceType != assistantProjectDocSourceType {
		t.Fatalf("refresh removed another Home's project docs: %#v", secondResults)
	}
}

func TestAssistantFileIndexSourceIDsFromProfileConfigIncludesLocalAndSMB(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"active_source_id": "hankdemo",
		"file_sources": []map[string]any{
			{"id": "hankdemo", "type": "smb", "smb_enabled": true},
			{"id": "hankdemo2", "type": "smb", "smb_enabled": true},
			{"id": "local", "type": "local", "local_root_enabled": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := assistantFileIndexSourceIDsFromProfileConfig(string(raw))
	want := []string{"hankdemo", "hankdemo2", "local"}
	if !equalStringSlices(got, want) {
		t.Fatalf("source IDs = %#v, want %#v", got, want)
	}
}

func TestAssistantFileIndexSourceIDsFromProfileConfigFallsBackToDefault(t *testing.T) {
	t.Parallel()

	got := assistantFileIndexSourceIDsFromProfileConfig(`{"file_sources":[]}`)
	want := []string{""}
	if !equalStringSlices(got, want) {
		t.Fatalf("source IDs = %#v, want %#v", got, want)
	}
}

func TestAssistantFileIndexSourceIDsFromProfileConfigAcceptsLegacyShares(t *testing.T) {
	t.Parallel()

	got := assistantFileIndexSourceIDsFromProfileConfig(`{"shares":[{"id":"archive","host":"nas.local","share":"Archive"}]}`)
	want := []string{"archive"}
	if !equalStringSlices(got, want) {
		t.Fatalf("source IDs = %#v, want %#v", got, want)
	}
}

func TestAssistantFileIndexListRequestPreservesSourceID(t *testing.T) {
	t.Parallel()

	request := assistantFileIndexListRequest("local", "Documents/Taxes")
	if request.SourceID != "local" || request.Path != "Documents/Taxes" {
		t.Fatalf("request = %#v, want source-aware file list request", request)
	}
}

func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
