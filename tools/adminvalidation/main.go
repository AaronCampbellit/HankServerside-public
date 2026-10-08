package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type validationClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "admin validation failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	baseRaw := envOrDefault("HANK_LIVE_BASE_URL", "http://127.0.0.1:18080")
	baseURL, err := url.Parse(strings.TrimRight(baseRaw, "/"))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(os.Getenv("HANK_LIVE_SESSION_TOKEN"))
	if token == "" {
		return errors.New("HANK_LIVE_SESSION_TOKEN is required")
	}
	client := validationClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := client.expectHTMLContains(ctx, "/dashboard/settings/backups", []string{`<div id="root"></div>`, `<script type="module" crossorigin src="/assets/index-`}); err != nil {
		return err
	}
	fmt.Println("PASS admin storage route serves React dashboard")

	if err := client.expectHTMLContains(ctx, "/dashboard/file-server", []string{`<div id="root"></div>`, `<script type="module" crossorigin src="/assets/index-`}); err != nil {
		return err
	}
	fmt.Println("PASS file server route serves React dashboard")

	var bootstrap map[string]any
	if err := client.doJSON(ctx, http.MethodGet, "/v1/ui/bootstrap", nil, http.StatusOK, &bootstrap); err != nil {
		return fmt.Errorf("GET /v1/ui/bootstrap: %w", err)
	}
	if err := expectObject(bootstrap, "user"); err != nil {
		return err
	}
	if err := expectObject(bootstrap, "permissions"); err != nil {
		return err
	}
	if err := expectObject(bootstrap, "server"); err != nil {
		return err
	}
	if err := expectArray(bootstrap, "navigation"); err != nil {
		return err
	}
	fmt.Println("PASS React bootstrap API returned required dashboard contract")

	var apps map[string]any
	if err := client.doJSON(ctx, http.MethodGet, "/v1/home/apps", nil, http.StatusOK, &apps); err != nil {
		return fmt.Errorf("GET /v1/home/apps: %w", err)
	}
	if err := expectArray(apps, "apps"); err != nil {
		return err
	}
	fmt.Println("PASS apps API returned array contract")

	var audit struct {
		Events []map[string]any `json:"events"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/v1/home/audit-events?limit=25", nil, http.StatusOK, &audit); err != nil {
		return fmt.Errorf("GET /v1/home/audit-events: %w", err)
	}
	for _, event := range audit.Events {
		if containsUnredactedSecret(event["metadata"]) {
			return fmt.Errorf("audit event metadata contains unredacted secret-like value: %#v", event)
		}
	}
	fmt.Printf("PASS audit API returned %d redacted events\n", len(audit.Events))

	var jobs struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/v1/home/file-jobs?limit=25", nil, http.StatusOK, &jobs); err != nil {
		return fmt.Errorf("GET /v1/home/file-jobs: %w", err)
	}
	fmt.Printf("PASS file job API returned %d jobs\n", len(jobs.Jobs))

	status, body, err := client.doRaw(ctx, http.MethodGet, "/v1/home/query-telemetry?limit=20", nil, client.token)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		return fmt.Errorf("query telemetry status=%d want 200 or 503 body=%s", status, string(body))
	}
	if status == http.StatusOK {
		var telemetry struct {
			Queries []map[string]any `json:"queries"`
		}
		if err := json.Unmarshal(body, &telemetry); err != nil {
			return err
		}
		fmt.Printf("PASS query telemetry API returned %d rows\n", len(telemetry.Queries))
	} else {
		fmt.Printf("PASS query telemetry API reported unavailable: %s\n", strings.TrimSpace(string(body)))
	}
	return nil
}

func expectObject(payload map[string]any, key string) error {
	if value, ok := payload[key].(map[string]any); ok && value != nil {
		return nil
	}
	return fmt.Errorf("%s should be a JSON object, got %#v", key, payload[key])
}

func expectArray(payload map[string]any, key string) error {
	if value, ok := payload[key].([]any); ok && value != nil {
		return nil
	}
	return fmt.Errorf("%s should be a JSON array, got %#v", key, payload[key])
}

func (c validationClient) expectHTMLContains(ctx context.Context, path string, wants []string) error {
	status, body, err := c.doRaw(ctx, http.MethodGet, path, nil, c.token)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s status=%d body=%s", path, status, string(body))
	}
	return containsAll(path, string(body), wants)
}

func containsAll(name string, body string, wants []string) error {
	for _, want := range wants {
		if !strings.Contains(body, want) {
			return fmt.Errorf("%s missing %q", name, want)
		}
	}
	return nil
}

func containsUnredactedSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			lowered := strings.ToLower(key)
			if strings.Contains(lowered, "token") || strings.Contains(lowered, "secret") || strings.Contains(lowered, "password") {
				if text, ok := nested.(string); ok && text != "" && text != "[redacted]" {
					return true
				}
			}
			if containsUnredactedSecret(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsUnredactedSecret(nested) {
				return true
			}
		}
	}
	return false
}

func (c validationClient) doJSON(ctx context.Context, method string, path string, body any, wantStatus int, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	status, data, err := c.doRaw(ctx, method, path, payload, c.token)
	if err != nil {
		return err
	}
	if status != wantStatus {
		return fmt.Errorf("status=%d want=%d body=%s", status, wantStatus, string(data))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	return nil
}

func (c validationClient) doRaw(ctx context.Context, method string, path string, body []byte, bearer string) (int, []byte, error) {
	target, err := c.baseURL.Parse(path)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
