package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

type AssistantAIConfig struct {
	ExecutionEnabled      bool
	Provider              string
	OllamaBaseURL         string
	OllamaChatModel       string
	OllamaEmbeddingModel  string
	OpenAIBaseURL         string
	OpenAIAPIKey          string
	OpenAIChatModel       string
	OpenAIEmbeddingModel  string
	ChatGPTOAuthEnabled   bool
	ChatGPTAuthIssuer     string
	ChatGPTBackendBaseURL string
	ChatGPTClientID       string
	ChatGPTChatModel      string
	ProjectDocsDir        string
	EmbeddingDimension    int
}

type assistantProviderStatus struct {
	Provider            string
	ChatConfigured      bool
	EmbeddingConfigured bool
	ChatModel           string
	DefaultChatModel    string
	ChatModelOverride   string
	ChatModelOptions    []string
	EmbeddingModel      string
	VectorStore         string
}

type assistantModelOptionsResponse struct {
	Provider       string   `json:"provider"`
	ChatConfigured bool     `json:"chat_configured"`
	CurrentModel   string   `json:"current_model"`
	DefaultModel   string   `json:"default_model"`
	Override       string   `json:"override,omitempty"`
	Models         []string `json:"models"`
	Source         string   `json:"source"`
	Error          string   `json:"error,omitempty"`
}

type assistantLLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const chatGPTCodexClientVersion = "0.135.0"

func (c *AssistantAIConfig) normalize() {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = "auto"
	}
	c.OllamaBaseURL = strings.TrimRight(strings.TrimSpace(c.OllamaBaseURL), "/")
	c.OllamaChatModel = strings.TrimSpace(c.OllamaChatModel)
	if c.OllamaChatModel == "" {
		c.OllamaChatModel = "llama3.1"
	}
	c.OllamaEmbeddingModel = strings.TrimSpace(c.OllamaEmbeddingModel)
	if c.OllamaEmbeddingModel == "" {
		c.OllamaEmbeddingModel = "nomic-embed-text"
	}
	c.OpenAIBaseURL = strings.TrimRight(strings.TrimSpace(c.OpenAIBaseURL), "/")
	if c.OpenAIBaseURL == "" {
		c.OpenAIBaseURL = "https://api.openai.com"
	}
	c.OpenAIChatModel = strings.TrimSpace(c.OpenAIChatModel)
	if c.OpenAIChatModel == "" {
		c.OpenAIChatModel = "gpt-4o-mini"
	}
	c.OpenAIEmbeddingModel = strings.TrimSpace(c.OpenAIEmbeddingModel)
	if c.OpenAIEmbeddingModel == "" {
		c.OpenAIEmbeddingModel = "text-embedding-3-small"
	}
	c.ChatGPTAuthIssuer = strings.TrimRight(strings.TrimSpace(c.ChatGPTAuthIssuer), "/")
	if c.ChatGPTAuthIssuer == "" {
		c.ChatGPTAuthIssuer = "https://auth.openai.com"
	}
	c.ChatGPTBackendBaseURL = strings.TrimRight(strings.TrimSpace(c.ChatGPTBackendBaseURL), "/")
	if c.ChatGPTBackendBaseURL == "" {
		c.ChatGPTBackendBaseURL = "https://chatgpt.com/backend-api/codex"
	}
	c.ChatGPTClientID = strings.TrimSpace(c.ChatGPTClientID)
	if c.ChatGPTClientID == "" {
		c.ChatGPTClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	}
	c.ChatGPTChatModel = strings.TrimSpace(c.ChatGPTChatModel)
	if c.ChatGPTChatModel == "" {
		c.ChatGPTChatModel = "gpt-5.4-mini"
	}
	c.ProjectDocsDir = strings.TrimSpace(c.ProjectDocsDir)
	if c.ProjectDocsDir == "" {
		c.ProjectDocsDir = "."
	}
	if c.EmbeddingDimension <= 0 {
		c.EmbeddingDimension = 768
	}
}

func (c AssistantAIConfig) defaultChatModelForProvider(provider string) string {
	c.normalize()
	switch provider {
	case "ollama":
		return c.OllamaChatModel
	case "openai":
		return c.OpenAIChatModel
	case assistantProviderChatGPTCodex:
		return c.ChatGPTChatModel
	default:
		return "local fallback"
	}
}

func assistantChatModelOverride(values ...string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func assistantChatModelOptions(cfg AssistantAIConfig) []string {
	cfg.normalize()
	return uniqueStrings([]string{
		cfg.ChatGPTChatModel,
		cfg.OpenAIChatModel,
		cfg.OllamaChatModel,
	})
}

func assistantEmbeddingModelOptions(cfg AssistantAIConfig) []string {
	cfg.normalize()
	return uniqueStrings([]string{
		cfg.OllamaEmbeddingModel,
		cfg.OpenAIEmbeddingModel,
		"nomic-embed-text",
		"qwen3-embedding:0.6b",
	})
}

func assistantAIConfigWithSettings(cfg AssistantAIConfig, settings domain.AssistantSettings) AssistantAIConfig {
	settings = normalizeAssistantSettings(settings)
	if strings.TrimSpace(settings.AIProvider) != "" {
		cfg.Provider = settings.AIProvider
	}
	if strings.TrimSpace(settings.OllamaBaseURL) != "" {
		cfg.OllamaBaseURL = settings.OllamaBaseURL
	}
	if strings.TrimSpace(settings.EmbeddingModel) != "" {
		cfg.OllamaEmbeddingModel = settings.EmbeddingModel
		cfg.OpenAIEmbeddingModel = settings.EmbeddingModel
	}
	cfg.normalize()
	return cfg
}

func (s *Server) assistantStatus(ctx context.Context, userID string, modelOverride ...string) assistantProviderStatus {
	cfg := s.assistantAI
	cfg.normalize()
	provider, token := s.resolveAssistantProvider(ctx, userID)
	override := assistantChatModelOverride(modelOverride...)
	defaultChatModel := cfg.defaultChatModelForProvider(provider)
	chatModel := defaultChatModel
	if defaultChatModel != "local fallback" {
		chatModel = defaultString(override, defaultChatModel)
	}
	status := assistantProviderStatus{
		Provider:          provider,
		VectorStore:       "postgres",
		ChatModel:         chatModel,
		DefaultChatModel:  defaultChatModel,
		ChatModelOverride: override,
		ChatModelOptions:  assistantChatModelOptions(cfg),
		EmbeddingModel:    "local-hash",
		ChatConfigured:    provider == "ollama" && cfg.OllamaBaseURL != "",
	}
	if cfg.OllamaBaseURL != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OllamaEmbeddingModel
	} else if strings.TrimSpace(cfg.OpenAIAPIKey) != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OpenAIEmbeddingModel
	} else if provider == assistantProviderChatGPTCodex && token != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OpenAIEmbeddingModel
	}
	if provider == "openai" {
		status.ChatConfigured = token != ""
	}
	if provider == assistantProviderChatGPTCodex {
		status.ChatConfigured = token != ""
	}
	if !s.store.VectorAvailable() {
		status.VectorStore = "postgres_without_pgvector"
	}
	return status
}

func (s *Server) assistantStatusWithSettings(ctx context.Context, userID string, settings domain.AssistantSettings) assistantProviderStatus {
	cfg := assistantAIConfigWithSettings(s.assistantAI, settings)
	provider, token := s.resolveAssistantProviderWithConfig(ctx, userID, cfg)
	override := strings.TrimSpace(settings.ChatModel)
	defaultChatModel := cfg.defaultChatModelForProvider(provider)
	chatModel := defaultChatModel
	if defaultChatModel != "local fallback" {
		chatModel = defaultString(override, defaultChatModel)
	}
	status := assistantProviderStatus{
		Provider:          provider,
		VectorStore:       "postgres",
		ChatModel:         chatModel,
		DefaultChatModel:  defaultChatModel,
		ChatModelOverride: override,
		ChatModelOptions:  assistantChatModelOptions(cfg),
		EmbeddingModel:    "local-hash",
		ChatConfigured:    provider == "ollama" && cfg.OllamaBaseURL != "",
	}
	if cfg.OllamaBaseURL != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OllamaEmbeddingModel
	} else if strings.TrimSpace(cfg.OpenAIAPIKey) != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OpenAIEmbeddingModel
	} else if provider == assistantProviderChatGPTCodex && token != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		status.EmbeddingConfigured = true
		status.EmbeddingModel = cfg.OpenAIEmbeddingModel
	}
	if provider == "openai" || provider == assistantProviderChatGPTCodex {
		status.ChatConfigured = token != ""
	}
	if !s.store.VectorAvailable() {
		status.VectorStore = "postgres_without_pgvector"
	}
	return status
}

func (s *Server) handleAssistantModels(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings, err := s.currentAssistantSettings(r.Context(), home.ID, auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := s.assistantStatusWithSettings(r.Context(), auth.User.ID, settings)
	modelsCtx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	models, source, err := s.fetchAssistantChatModelsWithConfig(modelsCtx, auth.User.ID, assistantAIConfigWithSettings(s.assistantAI, settings), status.Provider)
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	if len(models) == 0 {
		models = status.ChatModelOptions
		source = "configured"
	}
	defaultModel := status.DefaultChatModel
	if status.Provider == "ollama" && source == "ollama" && !stringSliceContains(models, defaultModel) {
		defaultModel = ""
	}
	models = assistantModelOptionsWithConfigured(models, defaultModel, settings.ChatModel)
	writeJSON(w, http.StatusOK, assistantModelOptionsResponse{
		Provider:       status.Provider,
		ChatConfigured: status.ChatConfigured,
		CurrentModel:   status.ChatModel,
		DefaultModel:   status.DefaultChatModel,
		Override:       settings.ChatModel,
		Models:         models,
		Source:         source,
		Error:          errorMessage,
	})
}

func stringSliceContains(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func (s *Server) fetchAssistantChatModels(ctx context.Context, userID string, provider string) ([]string, string, error) {
	cfg := s.assistantAI
	cfg.normalize()
	return s.fetchAssistantChatModelsWithConfig(ctx, userID, cfg, provider)
}

func (s *Server) fetchAssistantChatModelsWithConfig(ctx context.Context, userID string, cfg AssistantAIConfig, provider string) ([]string, string, error) {
	cfg.normalize()
	switch provider {
	case "openai":
		token := strings.TrimSpace(cfg.OpenAIAPIKey)
		if token == "" {
			return nil, "", errors.New("OpenAI API key is not configured")
		}
		models, err := fetchOpenAIChatModels(ctx, cfg.OpenAIBaseURL, token)
		return models, "openai_api", err
	case assistantProviderChatGPTCodex:
		if !cfg.ChatGPTOAuthEnabled {
			return nil, "", errChatGPTRelinkRequired
		}
		account, err := s.chatGPTCodexAccount(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		models, err := fetchChatGPTCodexModels(ctx, cfg.ChatGPTBackendBaseURL, account.AccessToken, account.ProviderUserID)
		return models, assistantProviderChatGPTCodex, err
	case "ollama":
		if cfg.OllamaBaseURL == "" {
			return nil, "", errors.New("Ollama is not configured")
		}
		models, err := fetchOllamaChatModels(ctx, cfg.OllamaBaseURL)
		return models, "ollama", err
	default:
		return nil, "", nil
	}
}

func assistantModelOptionsWithConfigured(models []string, defaultModel string, override string) []string {
	values := make([]string, 0, len(models)+2)
	values = append(values, override, defaultModel)
	values = append(values, models...)
	return uniqueStrings(values)
}

func (s *Server) resolveAssistantProvider(ctx context.Context, userID string) (string, string) {
	cfg := s.assistantAI
	cfg.normalize()
	return s.resolveAssistantProviderWithConfig(ctx, userID, cfg)
}

func (s *Server) resolveAssistantProviderWithConfig(ctx context.Context, userID string, cfg AssistantAIConfig) (string, string) {
	cfg.normalize()
	openAIToken := strings.TrimSpace(cfg.OpenAIAPIKey)
	chatGPTLinked := s.hasLinkedChatGPTCodex(ctx, userID)

	switch cfg.Provider {
	case "ollama":
		return "ollama", ""
	case "openai":
		return "openai", openAIToken
	case assistantProviderChatGPTCodex:
		if cfg.ChatGPTOAuthEnabled && chatGPTLinked {
			return assistantProviderChatGPTCodex, "linked"
		}
		return assistantProviderChatGPTCodex, ""
	case "disabled":
		return "local", ""
	default:
		if cfg.OllamaBaseURL != "" {
			return "ollama", ""
		}
		if openAIToken != "" {
			return "openai", openAIToken
		}
		if cfg.ChatGPTOAuthEnabled && chatGPTLinked {
			return assistantProviderChatGPTCodex, "linked"
		}
		return "local", ""
	}
}

func (s *Server) hasLinkedChatGPTCodex(ctx context.Context, userID string) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	account, err := s.store.GetOpenAIAccount(ctx, userID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("assistant ChatGPT account lookup failed", "error_code", "operation_failed")
		}
		return false
	}
	return account.AuthProvider == openAIAccountProviderChatGPTCodex && strings.TrimSpace(account.AccessToken) != ""
}

func (s *Server) generateAssistantLLMResponse(ctx context.Context, userID string, messages []assistantLLMMessage, modelOverride ...string) (string, string, error) {
	cfg := s.assistantAI
	cfg.normalize()
	return s.generateAssistantLLMResponseWithConfig(ctx, userID, cfg, messages, assistantChatModelOverride(modelOverride...))
}

func (s *Server) generateAssistantLLMResponseWithSettings(ctx context.Context, userID string, settings domain.AssistantSettings, messages []assistantLLMMessage) (string, string, error) {
	cfg := assistantAIConfigWithSettings(s.assistantAI, settings)
	return s.generateAssistantLLMResponseWithConfig(ctx, userID, cfg, messages, strings.TrimSpace(settings.ChatModel))
}

func (s *Server) generateAssistantLLMResponseWithConfig(ctx context.Context, userID string, cfg AssistantAIConfig, messages []assistantLLMMessage, modelOverride string) (string, string, error) {
	cfg.normalize()
	provider, token := s.resolveAssistantProviderWithConfig(ctx, userID, cfg)
	chatModel := defaultString(strings.TrimSpace(modelOverride), cfg.defaultChatModelForProvider(provider))
	record := func(err error) {
		if s.metrics != nil {
			s.metrics.RecordAssistantProvider(provider, err != nil)
		}
	}
	switch provider {
	case "ollama":
		if cfg.OllamaBaseURL == "" {
			record(errors.New("not configured"))
			return "", "", errors.New("Ollama is not configured")
		}
		text, err := postOllamaChat(ctx, cfg.OllamaBaseURL, chatModel, messages)
		record(err)
		return text, "ollama:" + chatModel, err
	case "openai":
		if token == "" {
			record(errors.New("not configured"))
			return "", "", errors.New("OpenAI is not configured")
		}
		text, err := postOpenAIChat(ctx, cfg.OpenAIBaseURL, token, chatModel, messages)
		record(err)
		return text, "openai:" + chatModel, err
	case assistantProviderChatGPTCodex:
		if !cfg.ChatGPTOAuthEnabled {
			record(errChatGPTRelinkRequired)
			return "", "", errChatGPTRelinkRequired
		}
		account, err := s.chatGPTCodexAccount(ctx, userID)
		if err != nil {
			record(err)
			return "", "", err
		}
		text, err := postChatGPTCodexResponse(ctx, cfg.ChatGPTBackendBaseURL, account.AccessToken, account.ProviderUserID, chatModel, messages)
		var providerErr *providerHTTPError
		if errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
			account, err = s.refreshChatGPTCodexAccount(ctx, account)
			if err != nil {
				record(err)
				return "", "", err
			}
			text, err = postChatGPTCodexResponse(ctx, cfg.ChatGPTBackendBaseURL, account.AccessToken, account.ProviderUserID, chatModel, messages)
			if errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
				_ = s.store.DeleteOpenAIAccount(ctx, userID)
				record(errChatGPTRelinkRequired)
				return "", "", errChatGPTRelinkRequired
			}
		}
		record(err)
		return text, assistantProviderChatGPTCodex + ":" + chatModel, err
	default:
		record(errors.New("not configured"))
		return "", "local", errors.New("assistant provider is not configured")
	}
}

func (s *Server) embedAssistantText(ctx context.Context, userID string, text string, settingsOverride ...domain.AssistantSettings) ([]float64, string, string) {
	cfg := s.assistantAI
	if len(settingsOverride) > 0 {
		cfg = assistantAIConfigWithSettings(cfg, settingsOverride[0])
	}
	cfg.normalize()
	if cfg.OllamaBaseURL != "" {
		if embedding, err := postOllamaEmbedding(ctx, cfg.OllamaBaseURL, cfg.OllamaEmbeddingModel, text); err == nil && len(embedding) > 0 {
			return normalizeEmbedding(embedding, cfg.EmbeddingDimension), cfg.OllamaEmbeddingModel, "ollama"
		} else if err != nil {
			s.logger.Warn("assistant Ollama embedding failed", "error_code", "operation_failed")
		}
	}
	if strings.TrimSpace(cfg.OpenAIAPIKey) != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		if embedding, err := postOpenAIEmbedding(ctx, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.OpenAIEmbeddingModel, cfg.EmbeddingDimension, text); err == nil && len(embedding) > 0 {
			return normalizeEmbedding(embedding, cfg.EmbeddingDimension), cfg.OpenAIEmbeddingModel, "openai"
		} else if err != nil {
			s.logger.Warn("assistant OpenAI embedding failed", "error_code", "operation_failed")
		}
	}
	if cfg.ChatGPTOAuthEnabled && strings.TrimSpace(userID) != "" && strings.TrimSpace(cfg.OpenAIEmbeddingModel) != "" {
		if embedding, err := s.embedAssistantTextWithLinkedChatGPT(ctx, userID, cfg, text); err == nil && len(embedding) > 0 {
			return normalizeEmbedding(embedding, cfg.EmbeddingDimension), cfg.OpenAIEmbeddingModel, assistantProviderChatGPTCodex
		} else if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, errChatGPTRelinkRequired) {
			s.logger.Warn("assistant ChatGPT/Codex embedding failed", "error_code", "operation_failed")
		}
	}
	return localEmbedding(text, cfg.EmbeddingDimension), "local-hash", "v1"
}

func (s *Server) embedAssistantTextWithLinkedChatGPT(ctx context.Context, userID string, cfg AssistantAIConfig, text string) ([]float64, error) {
	account, err := s.chatGPTCodexAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	embedding, err := postOpenAIEmbedding(ctx, cfg.OpenAIBaseURL, account.AccessToken, cfg.OpenAIEmbeddingModel, cfg.EmbeddingDimension, text)
	var providerErr *providerHTTPError
	if errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
		account, err = s.refreshChatGPTCodexAccount(ctx, account)
		if err != nil {
			return nil, err
		}
		embedding, err = postOpenAIEmbedding(ctx, cfg.OpenAIBaseURL, account.AccessToken, cfg.OpenAIEmbeddingModel, cfg.EmbeddingDimension, text)
		if errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
			_ = s.store.DeleteOpenAIAccount(ctx, userID)
			return nil, errChatGPTRelinkRequired
		}
	}
	return embedding, err
}

func postOllamaChat(ctx context.Context, baseURL string, model string, messages []assistantLLMMessage) (string, error) {
	var body struct {
		Model    string                `json:"model"`
		Messages []assistantLLMMessage `json:"messages"`
		Stream   bool                  `json:"stream"`
		Think    bool                  `json:"think"`
	}
	body.Model = model
	body.Messages = messages
	body.Stream = false
	body.Think = false
	var response struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	var lastErr error
	for _, endpointBase := range ollamaBaseURLCandidates(baseURL) {
		response = struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Error string `json:"error"`
		}{}
		if err := postJSON(ctx, endpointBase+"/api/chat", "", body, &response); err != nil {
			lastErr = err
			continue
		}
		if response.Error != "" {
			return "", errors.New(response.Error)
		}
		return sanitizeAssistantModelText(response.Message.Content), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("Ollama is not configured")
}

func postOllamaEmbedding(ctx context.Context, baseURL string, model string, text string) ([]float64, error) {
	body := map[string]any{"model": model, "prompt": text}
	var response struct {
		Embedding []float64 `json:"embedding"`
		Error     string    `json:"error"`
	}
	var lastErr error
	for _, endpointBase := range ollamaBaseURLCandidates(baseURL) {
		response = struct {
			Embedding []float64 `json:"embedding"`
			Error     string    `json:"error"`
		}{}
		if err := postJSON(ctx, endpointBase+"/api/embeddings", "", body, &response); err != nil {
			lastErr = err
			continue
		}
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Embedding, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("Ollama is not configured")
}

func postOpenAIChat(ctx context.Context, baseURL string, token string, model string, messages []assistantLLMMessage) (string, error) {
	body := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.2,
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := postJSON(ctx, baseURL+"/v1/chat/completions", token, body, &response); err != nil {
		return "", err
	}
	if response.Error != nil {
		return "", errors.New(response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return "", errors.New("OpenAI returned no choices")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

func postOpenAIEmbedding(ctx context.Context, baseURL string, token string, model string, dimensions int, text string) ([]float64, error) {
	body := map[string]any{
		"model": model,
		"input": text,
	}
	if dimensions > 0 {
		body["dimensions"] = dimensions
	}
	var response struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := postJSON(ctx, baseURL+"/v1/embeddings", token, body, &response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, errors.New(response.Error.Message)
	}
	if len(response.Data) == 0 {
		return nil, errors.New("OpenAI returned no embeddings")
	}
	return response.Data[0].Embedding, nil
}

func fetchOpenAIChatModels(ctx context.Context, baseURL string, token string) ([]string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/models"
	data, err := getEndpointBody(ctx, endpoint, map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		return nil, err
	}
	return assistantModelIDsFromJSON(data, true), nil
}

func fetchChatGPTCodexModels(ctx context.Context, baseURL string, token string, accountID string) ([]string, error) {
	headers := map[string]string{"Authorization": "Bearer " + token}
	if strings.TrimSpace(accountID) != "" {
		headers["ChatGPT-Account-ID"] = accountID
	}
	endpointURL, err := url.Parse(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return nil, err
	}
	query := endpointURL.Query()
	query.Set("client_version", chatGPTCodexClientVersion)
	endpointURL.RawQuery = query.Encode()
	endpoint := endpointURL.String()
	data, err := getEndpointBody(ctx, endpoint, headers)
	if err != nil {
		return nil, err
	}
	models := assistantModelIDsFromJSON(data, true)
	if len(models) == 0 {
		return nil, errors.New("ChatGPT/Codex did not return chat models")
	}
	return models, nil
}

func fetchOllamaChatModels(ctx context.Context, baseURL string) ([]string, error) {
	var lastErr error
	for _, endpointBase := range ollamaBaseURLCandidates(baseURL) {
		endpoint := strings.TrimRight(endpointBase, "/") + "/api/tags"
		data, err := getEndpointBody(ctx, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		return assistantModelIDsFromJSON(data, true), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("Ollama is not configured")
}

func ollamaBaseURLCandidates(baseURL string) []string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil
	}
	values := []string{trimmed}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return values
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" {
		return values
	}
	port := parsed.Port()
	parsed.Host = "host.docker.internal"
	if port != "" {
		parsed.Host += ":" + port
	}
	values = append(values, strings.TrimRight(parsed.String(), "/"))
	return uniqueStrings(values)
}

func postChatGPTCodexResponse(ctx context.Context, baseURL string, token string, accountID string, model string, messages []assistantLLMMessage) (string, error) {
	body := map[string]any{
		"model": model,
		"input": messages,
		"store": false,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
	}
	if strings.TrimSpace(accountID) != "" {
		headers["ChatGPT-Account-ID"] = accountID
	}
	var response struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Text    string `json:"text"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := postJSONToEndpoint(ctx, strings.TrimRight(baseURL, "/")+"/responses", body, headers, &response); err != nil {
		return "", err
	}
	if response.Error != nil {
		return "", errors.New(response.Error.Message)
	}
	if strings.TrimSpace(response.OutputText) != "" {
		return strings.TrimSpace(response.OutputText), nil
	}
	for _, output := range response.Output {
		if strings.TrimSpace(output.Text) != "" {
			return strings.TrimSpace(output.Text), nil
		}
		for _, content := range output.Content {
			if strings.TrimSpace(content.Text) != "" {
				return strings.TrimSpace(content.Text), nil
			}
		}
	}
	return "", errors.New("ChatGPT/Codex returned no output text")
}

type providerHTTPError struct {
	StatusCode int
	Body       string
}

func (e *providerHTTPError) Error() string {
	return fmt.Sprintf("provider status %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

func postJSON(ctx context.Context, url string, bearerToken string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &providerHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func getEndpointBody(ctx context.Context, endpoint string, headers map[string]string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			request.Header.Set(key, value)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &providerHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	return data, nil
}

func assistantModelIDsFromJSON(data []byte, chatOnly bool) []string {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var ids []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				if text, ok := item.(string); ok {
					if assistantModelIDAllowed(text, chatOnly) {
						ids = append(ids, strings.TrimSpace(text))
					}
					continue
				}
				walk(item)
			}
		case map[string]any:
			for _, key := range []string{"id", "model", "slug", "model_slug", "name"} {
				if text, ok := typed[key].(string); ok && assistantModelIDAllowed(text, chatOnly) {
					ids = append(ids, strings.TrimSpace(text))
					break
				}
			}
			for _, key := range []string{"models", "data", "items", "available_models"} {
				if child, ok := typed[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(raw)
	return uniqueStrings(ids)
}

func assistantModelIDAllowed(value string, chatOnly bool) bool {
	id := strings.TrimSpace(value)
	if id == "" || strings.ContainsAny(id, "\r\n\t") {
		return false
	}
	if !chatOnly {
		return true
	}
	lowered := strings.ToLower(id)
	for _, blocked := range []string{"embed", "embedding", "whisper", "tts", "dall-e", "image", "moderation", "audio", "transcribe", "speech", "realtime"} {
		if strings.Contains(lowered, blocked) {
			return false
		}
	}
	return strings.HasPrefix(lowered, "gpt-") ||
		strings.HasPrefix(lowered, "o") ||
		strings.Contains(lowered, "codex") ||
		strings.Contains(lowered, "chat") ||
		strings.Contains(lowered, "llama") ||
		strings.Contains(lowered, "mistral") ||
		strings.Contains(lowered, "mixtral") ||
		strings.Contains(lowered, "qwen") ||
		strings.Contains(lowered, "gemma") ||
		strings.Contains(lowered, "phi") ||
		strings.Contains(lowered, "deepseek") ||
		strings.Contains(lowered, "command-r") ||
		strings.Contains(lowered, "yi")
}

func sanitizeAssistantModelText(value string) string {
	text := strings.TrimSpace(value)
	for {
		lowered := strings.ToLower(text)
		start := strings.Index(lowered, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(lowered[start:], "</think>")
		if end < 0 {
			text = strings.TrimSpace(text[:start])
			break
		}
		end += start + len("</think>")
		text = strings.TrimSpace(text[:start] + text[end:])
	}
	return text
}

func localEmbedding(text string, dimension int) []float64 {
	if dimension <= 0 {
		dimension = 768
	}
	values := make([]float64, dimension)
	terms := strings.Fields(strings.ToLower(text))
	if len(terms) == 0 {
		terms = []string{strings.ToLower(text)}
	}
	for _, term := range terms {
		sum := sha256.Sum256([]byte(term))
		index := int(binary.BigEndian.Uint64(sum[:8]) % uint64(dimension))
		weight := 1.0 + float64(len(term)%7)/10
		values[index] += weight
	}
	var norm float64
	for _, value := range values {
		norm += value * value
	}
	if norm == 0 {
		return values
	}
	norm = math.Sqrt(norm)
	for i := range values {
		values[i] = values[i] / norm
	}
	return values
}

func normalizeEmbedding(values []float64, dimension int) []float64 {
	if dimension <= 0 {
		return values
	}
	normalized := make([]float64, dimension)
	copy(normalized, values)
	return normalized
}
