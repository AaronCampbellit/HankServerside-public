package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var ErrDisabled = errors.New("home assistant is not configured")

type Client struct {
	mu         sync.RWMutex
	baseURL    string
	token      string
	httpClient *http.Client
}

func New(baseURL string, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL != "" && c.token != ""
}

func (c *Client) Health(ctx context.Context) error {
	if !c.Enabled() {
		return ErrDisabled
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("home assistant health status %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) FetchStates(ctx context.Context) ([]protocol.HomeAssistantState, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/states", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, decodeUpstreamError(resp)
	}

	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	states := make([]protocol.HomeAssistantState, 0, len(raw))
	for _, item := range raw {
		states = append(states, mapState(item))
	}

	return states, nil
}

func (c *Client) FetchState(ctx context.Context, entityID string) (protocol.HomeAssistantState, error) {
	if !c.Enabled() {
		return protocol.HomeAssistantState{}, ErrDisabled
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/states/"+url.PathEscape(entityID), nil)
	if err != nil {
		return protocol.HomeAssistantState{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return protocol.HomeAssistantState{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return protocol.HomeAssistantState{}, decodeUpstreamError(resp)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return protocol.HomeAssistantState{}, err
	}

	return mapState(raw), nil
}

func (c *Client) CallService(ctx context.Context, domain string, service string, body json.RawMessage) (json.RawMessage, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/api/services/"+url.PathEscape(domain)+"/"+url.PathEscape(service), body)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, decodeUpstreamError(resp)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	return json.RawMessage(data), nil
}

func (c *Client) newRequest(ctx context.Context, method string, path string, body []byte) (*http.Request, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	token := c.token
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) ApplyConfig(baseURL string, token string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	c.token = strings.TrimSpace(token)
	c.httpClient.Timeout = timeout
}

func (c *Client) Snapshot() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	timeoutSeconds := 0
	if c.httpClient != nil {
		timeoutSeconds = int(c.httpClient.Timeout / time.Second)
	}

	return map[string]any{
		"base_url":         c.baseURL,
		"timeout_seconds":  timeoutSeconds,
		"token_configured": c.token != "",
	}
}

func decodeUpstreamError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return fmt.Errorf("home assistant status %d: %s", resp.StatusCode, trimmed)
	}
	return fmt.Errorf("home assistant status %d", resp.StatusCode)
}

func mapState(raw map[string]any) protocol.HomeAssistantState {
	state := protocol.HomeAssistantState{
		Attributes: mapAny(raw["attributes"]),
		Context:    mapAny(raw["context"]),
		Raw:        raw,
	}
	if entityID, _ := raw["entity_id"].(string); entityID != "" {
		state.EntityID = entityID
	}
	if value, _ := raw["state"].(string); value != "" {
		state.State = value
	}
	if changed, ok := raw["last_changed"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, changed); err == nil {
			state.LastChanged = &parsed
		}
	}
	if updated, ok := raw["last_updated"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, updated); err == nil {
			state.LastUpdated = &parsed
		}
	}
	return state
}

func mapAny(value any) map[string]any {
	out, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return out
}
