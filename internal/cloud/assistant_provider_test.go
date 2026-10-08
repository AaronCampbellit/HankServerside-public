package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestAssistantProviderUsesConfiguredOllama(t *testing.T) {
	t.Parallel()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode Ollama chat body: %v", err)
			}
			if body["think"] != false {
				t.Fatalf("Ollama chat think = %#v, want false", body["think"])
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"message": map[string]any{"content": "Grounded answer from Ollama."},
			})
		case "/api/embeddings":
			writeJSON(w, http.StatusOK, map[string]any{
				"embedding": []float64{1, 2, 3, 4},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{
		Provider:             "ollama",
		OllamaBaseURL:        provider.URL,
		OllamaChatModel:      "test-chat",
		OllamaEmbeddingModel: "test-embed",
		EmbeddingDimension:   4,
	})

	answer, model, err := server.generateAssistantLLMResponse(context.Background(), "usr_provider", []assistantLLMMessage{
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("generateAssistantLLMResponse: %v", err)
	}
	if answer != "Grounded answer from Ollama." {
		t.Fatalf("answer = %q", answer)
	}
	if model != "ollama:test-chat" {
		t.Fatalf("model = %q", model)
	}

	embedding, modelName, version := server.embedAssistantText(context.Background(), "usr_provider", "hello")
	if len(embedding) != 4 {
		t.Fatalf("embedding len = %d, want 4", len(embedding))
	}
	if modelName != "test-embed" || version != "ollama" {
		t.Fatalf("embedding metadata = %q/%q", modelName, version)
	}
}

func TestOllamaBaseURLCandidatesFallbackFromLocalhostToDockerHost(t *testing.T) {
	t.Parallel()

	candidates := ollamaBaseURLCandidates("http://127.0.0.1:11434/")
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0] != "http://127.0.0.1:11434" || candidates[1] != "http://host.docker.internal:11434" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestFetchOllamaChatModelsIncludesLocalChatAndExcludesEmbeddings(t *testing.T) {
	t.Parallel()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []map[string]any{
				{"name": "qwen3:14b"},
				{"name": "llama3.2:latest"},
				{"name": "nomic-embed-text:latest"},
			},
		})
	}))
	defer provider.Close()

	models, err := fetchOllamaChatModels(context.Background(), provider.URL)
	if err != nil {
		t.Fatalf("fetchOllamaChatModels error = %v", err)
	}
	modelSet := map[string]bool{}
	for _, model := range models {
		modelSet[model] = true
	}
	if !modelSet["qwen3:14b"] || !modelSet["llama3.2:latest"] {
		t.Fatalf("local chat models missing: %#v", models)
	}
	if modelSet["nomic-embed-text:latest"] {
		t.Fatalf("embedding model should not be offered as chat model: %#v", models)
	}
}

func TestAssistantModelsEndpointOmitsUnavailableOllamaDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_ollama_models", Email: "ollama-models@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_ollama_models", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_ollama_models", UserID: user.ID, TokenHash: hashToken("ollama-models-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.AIProvider = "ollama"
	settings.OllamaBaseURL = "http://ollama.test"
	settings.ChatModel = "qwen3:14b"
	must(t, db.UpsertAssistantSettings(ctx, settings))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []map[string]any{{"name": "qwen3:14b"}},
		})
	}))
	defer provider.Close()
	settings.OllamaBaseURL = provider.URL
	must(t, db.UpsertAssistantSettings(ctx, settings))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: "ollama", OllamaBaseURL: provider.URL, OllamaChatModel: "llama3.1"})
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var response assistantModelOptionsResponse
	requestJSON(t, testServer, "ollama-models-token", http.MethodGet, "/v1/home/assistant/models", nil, &response)
	modelSet := map[string]bool{}
	for _, model := range response.Models {
		modelSet[model] = true
	}
	if !modelSet["qwen3:14b"] {
		t.Fatalf("models missing configured Ollama model: %#v", response.Models)
	}
	if modelSet["llama3.1"] {
		t.Fatalf("unavailable Ollama default should not be offered: %#v", response.Models)
	}
}

func TestSanitizeAssistantModelTextRemovesThinkingBlocks(t *testing.T) {
	t.Parallel()

	got := sanitizeAssistantModelText("  <think>private reasoning</think>\nUse the vector DB context to answer.  ")
	if got != "Use the vector DB context to answer." {
		t.Fatalf("sanitizeAssistantModelText = %q", got)
	}
	got = sanitizeAssistantModelText("Answer first. <think>later reasoning</think> Answer second.")
	if got != "Answer first.  Answer second." {
		t.Fatalf("sanitizeAssistantModelText inline = %q", got)
	}
}

func TestAssistantStatusUsesLinkedChatGPTCodexWhenConfigured(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_status", Email: "assistant-status@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_status", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_assistant_status", UserID: user.ID, TokenHash: hashToken("assistant-status-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "plus",
		AccessToken:     "linked-token",
		TokenType:       "Bearer",
		Scope:           "chat",
		CreatedAt:       now,
		UpdatedAt:       now,
	}))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ChatModel = "gpt-codex-large"
	must(t, db.UpsertAssistantSettings(ctx, settings))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTChatModel: "gpt-codex-test", OpenAIEmbeddingModel: "embed-test"})
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var status map[string]any
	requestJSON(t, testServer, "assistant-status-token", http.MethodGet, "/v1/home/assistant/status", nil, &status)
	if status["provider"] != assistantProviderChatGPTCodex {
		t.Fatalf("provider = %#v, want %s", status["provider"], assistantProviderChatGPTCodex)
	}
	if status["chat_configured"] != true {
		encoded, _ := json.Marshal(status)
		t.Fatalf("chat_configured = false, status=%s", encoded)
	}
	if status["chat_model"] != "gpt-codex-large" {
		t.Fatalf("chat_model = %#v", status["chat_model"])
	}
	if status["chat_model_default"] != "gpt-codex-test" || status["chat_model_override"] != "gpt-codex-large" {
		t.Fatalf("chat model defaults = %#v", status)
	}
	if status["embedding_configured"] != true {
		t.Fatalf("embedding_configured = %#v, want true", status["embedding_configured"])
	}
	if status["embedding_model"] != "embed-test" {
		t.Fatalf("embedding_model = %#v", status["embedding_model"])
	}
}

func TestAssistantModelsEndpointUsesLinkedChatGPTCodexModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_models", Email: "assistant-models@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_models", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_assistant_models", UserID: user.ID, TokenHash: hashToken("assistant-models-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "plus",
		AccessToken:     "linked-token",
		TokenType:       "Bearer",
		Scope:           "chatgpt_codex",
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer linked-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "workspace-123" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		if got := r.URL.Query().Get("client_version"); got != chatGPTCodexClientVersion {
			t.Fatalf("client_version = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []map[string]any{
				{"id": "gpt-codex-large"},
				{"slug": "gpt-codex-small"},
				{"id": "text-embedding-3-small"},
			},
		})
	}))
	defer provider.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTBackendBaseURL: provider.URL, ChatGPTChatModel: "gpt-codex-test"})
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var response assistantModelOptionsResponse
	requestJSON(t, testServer, "assistant-models-token", http.MethodGet, "/v1/home/assistant/models", nil, &response)
	if response.Provider != assistantProviderChatGPTCodex || response.Source != assistantProviderChatGPTCodex {
		t.Fatalf("model response provider/source = %#v", response)
	}
	models := map[string]bool{}
	for _, model := range response.Models {
		models[model] = true
	}
	for _, want := range []string{"gpt-codex-test", "gpt-codex-large", "gpt-codex-small"} {
		if !models[want] {
			t.Fatalf("models missing %q: %#v", want, response.Models)
		}
	}
	if models["text-embedding-3-small"] {
		t.Fatalf("embedding model should not be offered as chat model: %#v", response.Models)
	}
}

func TestChatGPTCodexProviderPostsResponsesWithBearerAndAccount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_chatgpt_provider", Email: "chatgpt-provider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "team",
		AccessToken:     "linked-token",
		RefreshToken:    "refresh-token",
		TokenType:       "Bearer",
		Scope:           "chatgpt_codex",
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer linked-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "workspace-123" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		var body struct {
			Model string                `json:"model"`
			Input []assistantLLMMessage `json:"input"`
			Store bool                  `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "gpt-codex-test" {
			t.Fatalf("model = %q", body.Model)
		}
		if len(body.Input) != 1 || body.Input[0].Content != "hello" {
			t.Fatalf("input = %#v", body.Input)
		}
		if body.Store {
			t.Fatal("store = true, want false")
		}
		writeJSON(w, http.StatusOK, map[string]any{"output_text": "Codex-backed answer."})
	}))
	defer provider.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTBackendBaseURL: provider.URL, ChatGPTChatModel: "gpt-codex-test"})

	answer, model, err := server.generateAssistantLLMResponse(ctx, user.ID, []assistantLLMMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("generateAssistantLLMResponse: %v", err)
	}
	if answer != "Codex-backed answer." {
		t.Fatalf("answer = %q", answer)
	}
	if model != "chatgpt_codex:gpt-codex-test" {
		t.Fatalf("model = %q", model)
	}
}

func TestChatGPTCodexProviderUsesModelOverride(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_chatgpt_model_override", Email: "chatgpt-model@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "plus",
		AccessToken:     "linked-token",
		TokenType:       "Bearer",
		Scope:           "chatgpt_codex",
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "gpt-codex-large" {
			t.Fatalf("model = %q", body.Model)
		}
		writeJSON(w, http.StatusOK, map[string]any{"output_text": "Override answer."})
	}))
	defer provider.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTBackendBaseURL: provider.URL, ChatGPTChatModel: "gpt-codex-test"})

	answer, model, err := server.generateAssistantLLMResponse(ctx, user.ID, []assistantLLMMessage{{Role: "user", Content: "hello"}}, "gpt-codex-large")
	if err != nil {
		t.Fatalf("generateAssistantLLMResponse: %v", err)
	}
	if answer != "Override answer." || model != "chatgpt_codex:gpt-codex-large" {
		t.Fatalf("answer/model = %q/%q", answer, model)
	}
}

func TestChatGPTCodexLinkedAccountCanEmbedForVectorSearch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_chatgpt_embed", Email: "chatgpt-embed@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "plus",
		AccessToken:     "linked-token",
		TokenType:       "Bearer",
		Scope:           "chatgpt_codex",
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer linked-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			Model      string `json:"model"`
			Input      string `json:"input"`
			Dimensions int    `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "embed-linked" || body.Input != "hello" || body.Dimensions != 4 {
			t.Fatalf("embedding body = %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.1, 0.2, 0.3, 0.4}},
			},
		})
	}))
	defer provider.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{
		Provider:             assistantProviderChatGPTCodex,
		ChatGPTOAuthEnabled:  true,
		OpenAIBaseURL:        provider.URL,
		OpenAIEmbeddingModel: "embed-linked",
		EmbeddingDimension:   4,
	})

	embedding, model, version := server.embedAssistantText(ctx, user.ID, "hello")
	if len(embedding) != 4 || model != "embed-linked" || version != assistantProviderChatGPTCodex {
		t.Fatalf("embedding metadata = len %d %q/%q", len(embedding), model, version)
	}
}

func TestOpenAIProviderDoesNotUseChatGPTOAuthToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_openai_separate", Email: "openai-separate@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:         user.ID,
		ProviderUserID: "workspace-123",
		AuthProvider:   openAIAccountProviderChatGPTCodex,
		AccessToken:    "chatgpt-token",
		TokenType:      "Bearer",
		CreatedAt:      now,
		UpdatedAt:      now,
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: "openai", OpenAIChatModel: "gpt-test"})

	_, _, err := server.generateAssistantLLMResponse(ctx, user.ID, []assistantLLMMessage{{Role: "user", Content: "hello"}})
	if err == nil || err.Error() != "OpenAI is not configured" {
		t.Fatalf("err = %v, want OpenAI is not configured", err)
	}
}

func TestAssistantProviderUsesOpenAIAPIKeyForChatAndEmbeddings(t *testing.T) {
	t.Parallel()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer api-key" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "OpenAI API answer."}},
				},
			})
		case "/v1/embeddings":
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]any{
					{"embedding": []float64{0.1, 0.2, 0.3, 0.4}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{
		Provider:             "openai",
		OpenAIBaseURL:        provider.URL,
		OpenAIAPIKey:         "api-key",
		OpenAIChatModel:      "gpt-api-test",
		OpenAIEmbeddingModel: "embed-api-test",
		EmbeddingDimension:   4,
	})

	answer, model, err := server.generateAssistantLLMResponse(context.Background(), "usr_openai_api", []assistantLLMMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("generateAssistantLLMResponse: %v", err)
	}
	if answer != "OpenAI API answer." || model != "openai:gpt-api-test" {
		t.Fatalf("answer/model = %q/%q", answer, model)
	}
	embedding, embeddingModel, version := server.embedAssistantText(context.Background(), "usr_openai_api", "hello")
	if len(embedding) != 4 || embeddingModel != "embed-api-test" || version != "openai" {
		t.Fatalf("embedding metadata = len %d %q/%q", len(embedding), embeddingModel, version)
	}
}

func TestAssistantAutoFallsBackLocalWithoutProviders(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: "auto", EmbeddingDimension: 4})

	status := server.assistantStatus(context.Background(), "usr_auto_local")
	if status.Provider != "local" || status.ChatConfigured || status.EmbeddingConfigured {
		t.Fatalf("status = %#v", status)
	}
	embedding, model, version := server.embedAssistantText(context.Background(), "usr_auto_local", "hello")
	if len(embedding) != 4 || model != "local-hash" || version != "v1" {
		t.Fatalf("embedding = len %d %q/%q", len(embedding), model, version)
	}
}

func TestAssistantIndexRetriesConfiguredEmbeddingFailure(t *testing.T) {
	t.Parallel()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer provider.Close()

	db := storeForTest(t)
	defer db.Close()
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{OllamaBaseURL: provider.URL, OllamaEmbeddingModel: "test-embed", EmbeddingDimension: 4})
	if _, err := server.assistantChunksForText(context.Background(), "usr_index_retry", "doc_index_retry", "keep the existing index", time.Now().UTC()); err == nil {
		t.Fatal("configured embedding failure must fail indexing instead of writing local-hash")
	}

	server.ConfigureAssistantAI(AssistantAIConfig{EmbeddingDimension: 4})
	chunks, err := server.assistantChunksForText(context.Background(), "usr_index_retry", "doc_index_retry", "local-only installation", time.Now().UTC())
	if err != nil || len(chunks) != 1 || chunks[0].EmbeddingModel != "local-hash" {
		t.Fatalf("local-only indexing = %#v, %v", chunks, err)
	}
}

func TestChatGPTCodexRefreshesExpiredTokenBeforeChat(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	user := domain.User{ID: "usr_chatgpt_refresh", Email: "chatgpt-refresh@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:         user.ID,
		ProviderUserID: "workspace-123",
		AuthProvider:   openAIAccountProviderChatGPTCodex,
		AccessToken:    "expired-token",
		RefreshToken:   "refresh-token",
		TokenType:      "Bearer",
		Scope:          "chatgpt_codex",
		ExpiresAt:      &expired,
		CreatedAt:      now,
		UpdatedAt:      now,
	}))

	refreshCalls := 0
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		refreshCalls++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh-token" {
			t.Fatalf("refresh body = %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "new-refresh-token",
			"id_token":      fakeChatGPTIDToken(t, "workspace-123", "plus", now.Add(time.Hour)),
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer authServer.Close()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Fatalf("Authorization = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"output_text": "Refreshed answer."})
	}))
	defer provider.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTAuthIssuer: authServer.URL, ChatGPTBackendBaseURL: provider.URL, ChatGPTChatModel: "gpt-codex-test"})

	answer, _, err := server.generateAssistantLLMResponse(ctx, user.ID, []assistantLLMMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("generateAssistantLLMResponse: %v", err)
	}
	if answer != "Refreshed answer." {
		t.Fatalf("answer = %q", answer)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	account, err := db.GetOpenAIAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.AccessToken != "fresh-token" || account.RefreshToken != "new-refresh-token" || account.ChatGPTPlanType != "plus" {
		t.Fatalf("refreshed account = %#v", account)
	}
}

func TestChatGPTCodexRefreshFailureDeletesLink(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	user := domain.User{ID: "usr_chatgpt_refresh_fail", Email: "chatgpt-refresh-fail@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:         user.ID,
		ProviderUserID: "workspace-123",
		AuthProvider:   openAIAccountProviderChatGPTCodex,
		AccessToken:    "expired-token",
		RefreshToken:   "refresh-token",
		TokenType:      "Bearer",
		Scope:          "chatgpt_codex",
		ExpiresAt:      &expired,
		CreatedAt:      now,
		UpdatedAt:      now,
	}))

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "refresh_token_expired"}})
	}))
	defer authServer.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTAuthIssuer: authServer.URL, ChatGPTBackendBaseURL: "https://chatgpt.invalid/backend-api/codex", ChatGPTChatModel: "gpt-codex-test"})

	_, _, err := server.generateAssistantLLMResponse(ctx, user.ID, []assistantLLMMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, errChatGPTRelinkRequired) {
		t.Fatalf("err = %v, want errChatGPTRelinkRequired", err)
	}
	if _, err := db.GetOpenAIAccount(ctx, user.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOpenAIAccount err = %v, want ErrNotFound", err)
	}
}
