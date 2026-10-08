package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchStatesAndCallService(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/states":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"entity_id": "light.kitchen",
					"state":     "on",
					"attributes": map[string]any{
						"friendly_name": "Kitchen",
					},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/services/light/turn_on":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-token", 5*time.Second)

	states, err := client.FetchStates(context.Background())
	if err != nil {
		t.Fatalf("FetchStates: %v", err)
	}
	if len(states) != 1 || states[0].EntityID != "light.kitchen" || states[0].State != "on" {
		t.Fatalf("FetchStates returned %#v", states)
	}

	result, err := client.CallService(context.Background(), "light", "turn_on", json.RawMessage(`{"entity_id":"light.kitchen"}`))
	if err != nil {
		t.Fatalf("CallService: %v", err)
	}
	if string(result) != "{\"ok\":true}\n" {
		t.Fatalf("CallService result = %q", string(result))
	}
}

func TestFetchStatePathEscaping(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/states/light.kitchen%2Fmain" {
			t.Fatalf("unexpected escaped path: %s", r.URL.EscapedPath())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entity_id": "light.kitchen/main", "state": "on"})
	}))
	defer server.Close()

	client := New(server.URL, "test-token", 3*time.Second)
	state, err := client.FetchState(context.Background(), "light.kitchen/main")
	if err != nil {
		t.Fatalf("FetchState: %v", err)
	}
	if state.EntityID != "light.kitchen/main" {
		t.Fatalf("entity id = %q", state.EntityID)
	}
}

func TestFetchStatesMalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{"))
	}))
	defer server.Close()

	client := New(server.URL, "test-token", 3*time.Second)
	_, err := client.FetchStates(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFetchStatesTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := New(server.URL, "test-token", 20*time.Millisecond)
	_, err := client.FetchStates(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "Client.Timeout") && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout-related error, got %v", err)
	}
}

func TestHealthDisabled(t *testing.T) {
	t.Parallel()

	client := New("", "", time.Second)
	err := client.Health(context.Background())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}
