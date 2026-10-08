package loadtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type scenarioReport struct {
	Scenario  string  `json:"scenario"`
	Count     int     `json:"count"`
	Errors    int     `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	P50MS     int64   `json:"p50_ms"`
	P95MS     int64   `json:"p95_ms"`
	P99MS     int64   `json:"p99_ms"`
}

var (
	reportMu      sync.Mutex
	reportResults []scenarioReport
)

func TestMain(m *testing.M) {
	code := m.Run()
	if path := strings.TrimSpace(os.Getenv("HANK_LOADTEST_REPORT")); path != "" {
		report := map[string]any{
			"generated_at": time.Now().UTC(),
			"base_url":     os.Getenv("HANK_LOADTEST_BASE_URL"),
			"scenarios":    reportResults,
		}
		if encoded, err := json.MarshalIndent(report, "", "  "); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, encoded, 0o644)
		}
	}
	os.Exit(code)
}

func TestHealthEndpointLoadSmoke(t *testing.T) {
	baseURL := os.Getenv("HANK_LOADTEST_BASE_URL")
	if baseURL == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL to run load smoke tests")
	}

	client := &http.Client{Timeout: envDuration("HANK_LOADTEST_HEALTH_TIMEOUT_SECONDS", 15*time.Second)}
	const workers = 10
	const requestsPerWorker = 20
	var wg sync.WaitGroup
	errors := make(chan error, workers*requestsPerWorker)
	latencies := make(chan time.Duration, workers*requestsPerWorker)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requestsPerWorker; i++ {
				start := time.Now()
				response, err := client.Get(baseURL + "/healthz")
				if err != nil {
					errors <- err
					continue
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					errors <- &statusError{status: response.StatusCode}
					continue
				}
				latencies <- time.Since(start)
			}
		}()
	}
	wg.Wait()
	close(errors)
	close(latencies)
	if len(errors) > 0 {
		t.Fatalf("load smoke errors: %v", <-errors)
	}
	var samples []time.Duration
	for latency := range latencies {
		samples = append(samples, latency)
	}
	if len(samples) != workers*requestsPerWorker {
		t.Fatalf("latency samples = %d, want %d", len(samples), workers*requestsPerWorker)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	recordLoadResult("health", samples, 0, workers*requestsPerWorker)
}

func TestSessionValidationLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run session validation load tests")
	}

	requestCount := envInt("HANK_LOADTEST_SESSION_REQUESTS", 100)
	workers := envInt("HANK_LOADTEST_SESSION_WORKERS", 10)
	client := &loadHTTPClient{baseURL: baseURL, token: sessionToken, http: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	runConcurrent(requestCount, workers, func(index int) (time.Duration, error) {
		start := time.Now()
		var me map[string]any
		if err := client.doJSON(ctx, http.MethodGet, "/v1/me", nil, http.StatusOK, &me); err != nil {
			return 0, fmt.Errorf("session validation %d: %w", index, err)
		}
		if me["user"] == nil {
			return 0, fmt.Errorf("session validation %d returned no user", index)
		}
		return time.Since(start), nil
	}, func(samples []time.Duration, err error) {
		if err != nil {
			t.Fatalf("session validation load error: %v", err)
		}
		recordLoadResult("session_validation", sortedDurations(samples), 0, requestCount)
	})
}

func TestAppWebSocketRelayLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run app websocket load tests")
	}

	connectionCount := envInt("HANK_LOADTEST_APP_WS_CONNECTIONS", 50)
	pingCount := envInt("HANK_LOADTEST_RELAY_PINGS", 10)
	if pingCount > connectionCount {
		pingCount = connectionCount
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	conns := make([]*websocket.Conn, connectionCount)
	latencies := make(chan time.Duration, pingCount)
	errors := make(chan error, connectionCount+pingCount)
	var wg sync.WaitGroup
	for i := 0; i < connectionCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			conn, err := openAppLoadWebSocket(ctx, baseURL, sessionToken)
			if err != nil {
				errors <- err
				return
			}
			conn.SetReadLimit(32 << 20)
			conns[index] = conn
		}(i)
	}
	wg.Wait()
	if len(errors) > 0 {
		close(errors)
		for _, conn := range conns {
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "load test cleanup")
			}
		}
		t.Fatalf("websocket connect error: %v", <-errors)
	}
	defer func() {
		for _, conn := range conns {
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "load test complete")
			}
		}
	}()

	for i := 0; i < pingCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			start := time.Now()
			requestID := fmt.Sprintf("load_ping_%d_%d", time.Now().UnixNano(), index)
			envelope, err := protocol.NewEnvelope(protocol.TypeAppCommand, requestID, "", "", protocol.RoutedCommand{
				Command: protocol.CommandSystemPing,
				Body:    []byte(`{"message":"load"}`),
			})
			if err != nil {
				errors <- err
				return
			}
			if err := wsjson.Write(ctx, conns[index], envelope); err != nil {
				errors <- err
				return
			}
			for {
				var response protocol.Envelope
				if err := wsjson.Read(ctx, conns[index], &response); err != nil {
					errors <- err
					return
				}
				if response.RequestID != requestID {
					continue
				}
				if response.Error != nil {
					errors <- fmt.Errorf("relay ping returned %s: %s", response.Error.Code, response.Error.Message)
					return
				}
				latencies <- time.Since(start)
				return
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	close(latencies)
	if len(errors) > 0 {
		t.Fatalf("websocket relay error: %v", <-errors)
	}
	samples := make([]time.Duration, 0, pingCount)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	if len(samples) != pingCount {
		t.Fatalf("latency samples = %d, want %d", len(samples), pingCount)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("app websocket connections=%d relay_pings=%d p50=%s p95=%s p99=%s", connectionCount, pingCount, percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99))
	recordLoadResult("app_websocket_relay", samples, 0, pingCount)
}

func TestAppWebSocketReconnectLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run app reconnect load tests")
	}

	reconnectCount := envInt("HANK_LOADTEST_APP_WS_RECONNECTS", 50)
	workers := envInt("HANK_LOADTEST_APP_WS_RECONNECT_WORKERS", 10)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	runConcurrent(reconnectCount, workers, func(index int) (time.Duration, error) {
		start := time.Now()
		conn, err := openAppLoadWebSocket(ctx, baseURL, sessionToken)
		if err != nil {
			return 0, fmt.Errorf("app reconnect %d open: %w", index, err)
		}
		if err := conn.Close(websocket.StatusNormalClosure, "reconnect load"); err != nil {
			return 0, fmt.Errorf("app reconnect %d close: %w", index, err)
		}
		return time.Since(start), nil
	}, func(samples []time.Duration, err error) {
		if err != nil {
			t.Fatalf("app reconnect load error: %v", err)
		}
		recordLoadResult("app_websocket_reconnect", sortedDurations(samples), 0, reconnectCount)
	})
}

func TestConcurrentFileTransfers(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	sourceID := envString("HANK_LOADTEST_FILE_SOURCE", "primary")
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run file transfer load tests")
	}

	transferCount := envInt("HANK_LOADTEST_TRANSFERS", 10)
	runID := fmt.Sprintf("load-%d", time.Now().UTC().UnixNano())
	root := "_hank_load/" + runID
	client := &loadHTTPClient{baseURL: baseURL, token: sessionToken, http: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	appConn, err := openAppLoadWebSocket(ctx, baseURL, sessionToken)
	if err != nil {
		t.Fatalf("open cleanup websocket: %v", err)
	}
	if err := sendLoadCommand(ctx, appConn, "mkdir_"+runID, "files.create_directory", map[string]any{"source_id": sourceID, "path": root}, nil); err != nil {
		_ = appConn.Close(websocket.StatusInternalError, "mkdir failed")
		t.Fatalf("create load directory: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_ = sendLoadCommand(cleanupCtx, appConn, "delete_"+runID, "files.delete", map[string]any{"source_id": sourceID, "path": root, "is_directory": true}, nil)
		_ = appConn.Close(websocket.StatusNormalClosure, "file transfer load cleanup")
	})

	var wg sync.WaitGroup
	errors := make(chan error, transferCount)
	latencies := make(chan time.Duration, transferCount)
	for i := 0; i < transferCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			path := fmt.Sprintf("%s/file-%02d.txt", root, index)
			payload := []byte(fmt.Sprintf("hank load transfer %s %02d\n", runID, index))
			start := time.Now()
			var setup struct {
				TransferID    string `json:"transfer_id"`
				TransferToken string `json:"transfer_token"`
				JobID         string `json:"job_id"`
				URL           string `json:"url"`
			}
			if err := client.doJSON(ctx, http.MethodPost, "/v1/home/files/uploads", map[string]string{"source_id": sourceID, "path": path}, http.StatusCreated, &setup); err != nil {
				errors <- fmt.Errorf("setup transfer %d: %w", index, err)
				return
			}
			if setup.TransferID == "" || setup.TransferToken == "" || setup.JobID == "" || setup.URL == "" {
				errors <- fmt.Errorf("setup transfer %d missing fields: transfer_id=%t token=%t job_id=%t url=%t", index, setup.TransferID != "", setup.TransferToken != "", setup.JobID != "", setup.URL != "")
				return
			}
			status, body, err := client.doRaw(ctx, http.MethodPut, setup.URL, payload, setup.TransferToken)
			if err != nil {
				errors <- fmt.Errorf("upload transfer %d: %w", index, err)
				return
			}
			if status != http.StatusOK {
				errors <- fmt.Errorf("upload transfer %d status=%d body=%s", index, status, string(body))
				return
			}
			var transferStatus struct {
				Status    string `json:"status"`
				BytesDone int64  `json:"bytes_done"`
			}
			if err := client.doJSONWithBearer(ctx, http.MethodGet, setup.URL+"/status", nil, setup.TransferToken, http.StatusOK, &transferStatus); err != nil {
				errors <- fmt.Errorf("status transfer %d: %w", index, err)
				return
			}
			if transferStatus.Status != "completed" || transferStatus.BytesDone != int64(len(payload)) {
				errors <- fmt.Errorf("transfer %d status=%q bytes_done=%d want completed/%d", index, transferStatus.Status, transferStatus.BytesDone, len(payload))
				return
			}
			latencies <- time.Since(start)
		}(i)
	}
	wg.Wait()
	close(errors)
	close(latencies)
	if len(errors) > 0 {
		t.Fatalf("file transfer load error: %v", <-errors)
	}
	samples := make([]time.Duration, 0, transferCount)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	if len(samples) != transferCount {
		t.Fatalf("transfer latency samples = %d, want %d", len(samples), transferCount)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("file_transfers=%d source=%s p50=%s p95=%s p99=%s", transferCount, sourceID, percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99))
	recordLoadResult("file_transfers", samples, 0, transferCount)
}

func TestCrossSourceMoveJobLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	sourceOne := envString("HANK_LOADTEST_FILE_SOURCE", "primary")
	sourceTwo := envString("HANK_LOADTEST_FILE_SOURCE_TWO", "secondary")
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run cross-source move load tests")
	}
	if sourceOne == "" || sourceTwo == "" || sourceOne == sourceTwo {
		t.Skip("set distinct HANK_LOADTEST_FILE_SOURCE and HANK_LOADTEST_FILE_SOURCE_TWO")
	}

	moveCount := envInt("HANK_LOADTEST_MOVE_JOBS", 10)
	runID := fmt.Sprintf("move-load-%d", time.Now().UTC().UnixNano())
	root := "_hank_load/" + runID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	setupConn, err := openAppLoadWebSocket(ctx, baseURL, sessionToken)
	if err != nil {
		t.Fatalf("open setup websocket: %v", err)
	}
	if err := sendLoadCommand(ctx, setupConn, "mkdir_src_"+runID, "files.create_directory", protocol.FilesCreateDirectoryRequest{SourceID: sourceOne, Path: root}, nil); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := sendLoadCommand(ctx, setupConn, "mkdir_dst_"+runID, "files.create_directory", protocol.FilesCreateDirectoryRequest{SourceID: sourceTwo, Path: root}, nil); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = sendLoadCommand(cleanupCtx, setupConn, "delete_src_"+runID, "files.delete", protocol.FilesDeleteRequest{SourceID: sourceOne, Path: root, IsDirectory: true}, nil)
		_ = sendLoadCommand(cleanupCtx, setupConn, "delete_dst_"+runID, "files.delete", protocol.FilesDeleteRequest{SourceID: sourceTwo, Path: root, IsDirectory: true}, nil)
	})

	for i := 0; i < moveCount; i++ {
		path := fmt.Sprintf("%s/move-%02d.txt", root, i)
		payload := fmt.Sprintf("hank move load %s %02d\n", runID, i)
		if err := sendLoadCommand(ctx, setupConn, fmt.Sprintf("seed_move_%d_%s", i, runID), "files.upload", protocol.FilesUploadRequest{
			SourceID:      sourceOne,
			Path:          path,
			ContentBase64: base64.StdEncoding.EncodeToString([]byte(payload)),
		}, nil); err != nil {
			t.Fatalf("seed move file %d: %v", i, err)
		}
	}

	workers := envInt("HANK_LOADTEST_MOVE_WORKERS", minInt(10, moveCount))
	runConcurrent(moveCount, workers, func(index int) (time.Duration, error) {
		conn, err := openAppLoadWebSocket(ctx, baseURL, sessionToken)
		if err != nil {
			return 0, fmt.Errorf("move %d open websocket: %w", index, err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "move load")

		start := time.Now()
		from := fmt.Sprintf("%s/move-%02d.txt", root, index)
		to := fmt.Sprintf("%s/moved-%02d.txt", root, index)
		var moved protocol.FileOperationJobResponse
		if err := sendLoadCommand(ctx, conn, fmt.Sprintf("move_job_%d_%s", index, runID), "files.move", protocol.FilesMoveRequest{
			SourceID:            sourceOne,
			DestinationSourceID: sourceTwo,
			From:                from,
			To:                  to,
		}, &moved); err != nil {
			return 0, fmt.Errorf("move %d command: %w", index, err)
		}
		if moved.JobID == "" || moved.Status != "completed" {
			return 0, fmt.Errorf("move %d job response = %#v", index, moved)
		}
		return time.Since(start), nil
	}, func(samples []time.Duration, err error) {
		if err != nil {
			t.Fatalf("cross-source move job load error: %v", err)
		}
		recordLoadResult("cross_source_move_jobs", sortedDurations(samples), 0, moveCount)
	})
}

func TestNotesListLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run notes load tests")
	}

	requestCount := envInt("HANK_LOADTEST_NOTES_REQUESTS", 10)
	minNotes := envIntAllowZero("HANK_LOADTEST_MIN_NOTES", 0)
	client := &loadHTTPClient{baseURL: baseURL, token: sessionToken, http: &http.Client{Timeout: envDuration("HANK_LOADTEST_NOTES_TIMEOUT_SECONDS", 120*time.Second)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runConcurrent(requestCount, minInt(5, requestCount), func(index int) (time.Duration, error) {
		start := time.Now()
		var payload struct {
			Notes []json.RawMessage `json:"notes"`
		}
		if err := client.doJSON(ctx, http.MethodGet, "/v1/home/notes", nil, http.StatusOK, &payload); err != nil {
			return 0, fmt.Errorf("notes list %d: %w", index, err)
		}
		if len(payload.Notes) < minNotes {
			return 0, fmt.Errorf("notes list %d returned %d notes, want at least %d", index, len(payload.Notes), minNotes)
		}
		return time.Since(start), nil
	}, func(samples []time.Duration, err error) {
		if err != nil {
			t.Fatalf("notes list load error: %v", err)
		}
		recordLoadResult("notes_list", sortedDurations(samples), 0, requestCount)
	})
}

func TestAssistantRequestLoad(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("HANK_LOADTEST_BASE_URL"), "/")
	sessionToken := strings.TrimSpace(os.Getenv("HANK_LOADTEST_SESSION_TOKEN"))
	if baseURL == "" || sessionToken == "" {
		t.Skip("set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN to run assistant load tests")
	}

	requestCount := envInt("HANK_LOADTEST_ASSISTANT_REQUESTS", 10)
	client := &loadHTTPClient{baseURL: baseURL, token: sessionToken, http: &http.Client{Timeout: envDuration("HANK_LOADTEST_ASSISTANT_TIMEOUT_SECONDS", 120*time.Second)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	errors := make(chan error, requestCount)
	latencies := make(chan time.Duration, requestCount)
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			start := time.Now()
			var session struct {
				ID string `json:"id"`
			}
			if err := client.doJSON(ctx, http.MethodPost, "/v1/home/assistant/sessions", nil, http.StatusCreated, &session); err != nil {
				errors <- fmt.Errorf("create assistant session %d: %w", index, err)
				return
			}
			if session.ID == "" {
				errors <- fmt.Errorf("create assistant session %d returned empty id", index)
				return
			}
			var response map[string]any
			message := fmt.Sprintf("Synthetic load validation request %d. Reply with a short status sentence.", index)
			if err := client.doJSON(ctx, http.MethodPost, "/v1/home/assistant/sessions/"+url.PathEscape(session.ID)+"/messages", map[string]any{"content": message}, http.StatusCreated, &response); err != nil {
				errors <- fmt.Errorf("assistant message %d: %w", index, err)
				return
			}
			if len(response) == 0 {
				errors <- fmt.Errorf("assistant message %d returned empty response", index)
				return
			}
			latencies <- time.Since(start)
		}(i)
	}
	wg.Wait()
	close(errors)
	close(latencies)
	if len(errors) > 0 {
		t.Fatalf("assistant load error: %v", <-errors)
	}
	samples := make([]time.Duration, 0, requestCount)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	if len(samples) != requestCount {
		t.Fatalf("assistant latency samples = %d, want %d", len(samples), requestCount)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("assistant_requests=%d p50=%s p95=%s p99=%s", requestCount, percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99))
	recordLoadResult("assistant_requests", samples, 0, requestCount)
}

func recordLoadResult(scenario string, samples []time.Duration, errors int, total int) {
	reportMu.Lock()
	defer reportMu.Unlock()
	errorRate := 0.0
	if total > 0 {
		errorRate = float64(errors) / float64(total)
	}
	reportResults = append(reportResults, scenarioReport{
		Scenario:  scenario,
		Count:     total,
		Errors:    errors,
		ErrorRate: errorRate,
		P50MS:     percentile(samples, 0.50).Milliseconds(),
		P95MS:     percentile(samples, 0.95).Milliseconds(),
		P99MS:     percentile(samples, 0.99).Milliseconds(),
	})
}

func runConcurrent(total int, workers int, fn func(index int) (time.Duration, error), done func([]time.Duration, error)) {
	if workers <= 0 || workers > total {
		workers = total
	}
	jobs := make(chan int, total)
	errors := make(chan error, total)
	latencies := make(chan time.Duration, total)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				latency, err := fn(index)
				if err != nil {
					errors <- err
					continue
				}
				latencies <- latency
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errors)
	close(latencies)
	var firstErr error
	if len(errors) > 0 {
		firstErr = <-errors
	}
	samples := make([]time.Duration, 0, total)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	if firstErr == nil && len(samples) != total {
		firstErr = fmt.Errorf("latency samples = %d, want %d", len(samples), total)
	}
	done(samples, firstErr)
}

func sortedDurations(samples []time.Duration) []time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples
}

func openAppLoadWebSocket(ctx context.Context, baseURL string, sessionToken string) (*websocket.Conn, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/ws/app-ticket", bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return nil, &statusError{status: response.StatusCode}
	}
	var ticket struct {
		WebSocketPath string `json:"websocket_path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		return nil, err
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if parsedBase.Scheme == "https" {
		parsedBase.Scheme = "wss"
	} else {
		parsedBase.Scheme = "ws"
	}
	parsedPath, err := url.Parse(ticket.WebSocketPath)
	if err != nil {
		return nil, err
	}
	parsedBase.Path = parsedPath.Path
	parsedBase.RawQuery = parsedPath.RawQuery
	conn, _, err := websocket.Dial(ctx, parsedBase.String(), nil)
	return conn, err
}

func sendLoadCommand(ctx context.Context, conn *websocket.Conn, requestID string, command string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeAppCommand, requestID, "", "", protocol.RoutedCommand{
		Command: command,
		Body:    payload,
	})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, conn, envelope); err != nil {
		return err
	}
	for {
		var response protocol.Envelope
		if err := wsjson.Read(ctx, conn, &response); err != nil {
			return err
		}
		if response.RequestID != requestID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("%s returned %s: %s", command, response.Error.Code, response.Error.Message)
		}
		if out != nil && len(response.Payload) > 0 {
			return json.Unmarshal(response.Payload, out)
		}
		return nil
	}
}

type loadHTTPClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func (c *loadHTTPClient) doJSON(ctx context.Context, method string, path string, body any, wantStatus int, out any) error {
	return c.doJSONWithBearer(ctx, method, path, body, c.token, wantStatus, out)
}

func (c *loadHTTPClient) doJSONWithBearer(ctx context.Context, method string, path string, body any, bearer string, wantStatus int, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	status, data, err := c.doRaw(ctx, method, path, payload, bearer)
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

func (c *loadHTTPClient) doRaw(ctx context.Context, method string, path string, body []byte, bearer string) (int, []byte, error) {
	target := c.baseURL + path
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil && strings.Contains(path, "/assistant/") {
		request.Header.Set("Content-Type", "application/json")
	}
	if body != nil && strings.Contains(path, "/files/") && method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 512<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, data, nil
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envIntAllowZero(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}

func envString(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	index := int(float64(len(samples)-1) * p)
	if index < 0 {
		index = 0
	}
	if index >= len(samples) {
		index = len(samples) - 1
	}
	return samples[index]
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

type statusError struct {
	status int
}

func (e *statusError) Error() string {
	return http.StatusText(e.status)
}
