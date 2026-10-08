package apps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "app.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestRunnerInvokeReturnsOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nread line\nprintf '%s\n' '{\"request_id\":\"req_1\",\"ok\":true,\"output\":{\"text\":\"ok\"}}'\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
	response, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request: AppStdioRequest{
			ProtocolVersion: "hank.app.stdio.v1",
			RequestID:       "req_1",
			AppID:           "hermes",
			CommandID:       "chat",
			Input:           json.RawMessage(`{"prompt":"hello"}`),
		},
	})
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if !response.OK || string(response.Output) != `{"text":"ok"}` {
		t.Fatalf("response = %#v", response)
	}
}

func TestRunnerInvokeDecodesStructuredErrorFromFailedProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nread line\nprintf '%s\n' '{\"request_id\":\"req_1\",\"ok\":false,\"error\":{\"code\":\"media_error\",\"message\":\"media source is not configured\"}}'\nexit 1\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
	response, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request: AppStdioRequest{
			ProtocolVersion: "hank.app.stdio.v1",
			RequestID:       "req_1",
			AppID:           "gramaton",
			CommandID:       "search",
			Input:           json.RawMessage(`{"query":"the arrow"}`),
		},
	})
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if response.OK || response.Error == nil || response.Error.Message != "media source is not configured" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRunnerInvokeCarriesContextAndDecodesEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nread line\ncase \"$line\" in *'\"trace_id\":\"trace_1\"'*) ;; *) printf '%s\n' '{\"request_id\":\"req_1\",\"ok\":false,\"error\":{\"code\":\"missing_context\",\"message\":\"missing context\"}}'; exit 0 ;; esac\nprintf '%s\n' '{\"request_id\":\"req_1\",\"ok\":true,\"output\":{\"text\":\"ok\"},\"events\":[{\"event\":\"media.download_progress\",\"topic\":\"media.downloads\",\"body\":{\"job_id\":\"job_1\",\"status\":\"running\"}}]}'\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
	response, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request: AppStdioRequest{
			ProtocolVersion: "hank.app.stdio.v1",
			RequestID:       "req_1",
			AppID:           "gramaton",
			CommandID:       "download_status",
			Context:         json.RawMessage(`{"trace_id":"trace_1"}`),
			Input:           json.RawMessage(`{"job_id":"job_1"}`),
		},
	})
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if !response.OK || len(response.Events) != 1 {
		t.Fatalf("response = %#v", response)
	}
	event := response.Events[0]
	if event.Event != "media.download_progress" || event.Topic != "media.downloads" || string(event.Body) != `{"job_id":"job_1","status":"running"}` {
		t.Fatalf("event = %#v", event)
	}
}

func TestRunnerInvokeRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nprintf '%s\n' 'not json'\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
	_, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request:    AppStdioRequest{RequestID: "req_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid app response") {
		t.Fatalf("Invoke error = %v", err)
	}
}

func TestRunnerInvokeRejectsResponseValidationErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "request id mismatch",
			response: `{"request_id":"other","ok":true,"output":{"text":"ok"}}`,
		},
		{
			name:     "missing request id",
			response: `{"ok":true,"output":{"text":"ok"}}`,
		},
		{
			name:     "ok response with error object",
			response: `{"request_id":"req_1","ok":true,"output":{"text":"ok"},"error":{"code":"bad","message":"bad"}}`,
		},
		{
			name:     "error response without error object",
			response: `{"request_id":"req_1","ok":false}`,
		},
		{
			name:     "error response without code",
			response: `{"request_id":"req_1","ok":false,"error":{"message":"bad"}}`,
		},
		{
			name:     "error response without message",
			response: `{"request_id":"req_1","ok":false,"error":{"code":"bad"}}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			exe := writeExecutable(t, dir, "#!/bin/sh\nprintf '%s\n' '"+tt.response+"'\n")
			runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
			_, err := runner.Invoke(context.Background(), InvokeSpec{
				Executable: exe,
				WorkDir:    dir,
				Timeout:    5 * time.Second,
				Request:    AppStdioRequest{RequestID: "req_1"},
			})
			if err == nil || !strings.Contains(err.Error(), "invalid app response") {
				t.Fatalf("Invoke error = %v", err)
			}
		})
	}
}

func TestRunnerInvokeRejectsStdoutLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nprintf '%s\n' '"+strings.Repeat("x", 64)+"'\n")
	runner := testRunner(Runner{MaxOutputBytes: 16, MaxStderrBytes: 1024})
	_, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request:    AppStdioRequest{RequestID: "req_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "stdout exceeded") {
		t.Fatalf("Invoke error = %v", err)
	}
}

func TestRunnerInvokeRejectsStderrLimitWithoutLeakingStderr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stderrText := "secret-token-" + strings.Repeat("x", 64)
	exe := writeExecutable(t, dir, "#!/bin/sh\nprintf '%s\n' '"+stderrText+"' >&2\nprintf '%s\n' '{\"request_id\":\"req_1\",\"ok\":true,\"output\":{\"text\":\"ok\"}}'\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 16})
	_, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    5 * time.Second,
		Request:    AppStdioRequest{RequestID: "req_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "stderr exceeded") {
		t.Fatalf("Invoke error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("Invoke error leaked stderr: %v", err)
	}
}

func TestRunnerInvokeTimesOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\nsleep 2\n")
	runner := testRunner(Runner{MaxOutputBytes: 4096, MaxStderrBytes: 1024})
	started := time.Now()
	_, err := runner.Invoke(context.Background(), InvokeSpec{
		Executable: exe,
		WorkDir:    dir,
		Timeout:    20 * time.Millisecond,
		Request:    AppStdioRequest{RequestID: "req_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Invoke error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("descendant pipes defeated invocation timeout")
	}
}

func TestRunnerCancellationStopsOrdinaryDescendants(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix process group control")
	}
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\n(sleep 0.3; touch escaped) &\nwait\n")
	_, err := (testRunner(Runner{})).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: 30 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout")
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation")
	}
}

func TestRunnerCancellationAfterParentExitStopsDescendants(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix process group control")
	}
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\n(sleep 0.3; touch escaped) &\nprintf '%s\\n' '{\"ok\":true}'\nexit 0\n")
	_, err := (testRunner(Runner{})).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: 30 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout")
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(err) {
		t.Fatal("descendant survived parent exit and timeout")
	}
}

func TestRunnerAllowsDescendantResponseWithinDeadline(t *testing.T) {
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "#!/bin/sh\n(sleep 0.4; printf '%s\\n' '{\"ok\":true}') &\nexit 0\n")
	response, err := (testRunner(Runner{})).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: 5 * time.Second})
	if err != nil || !response.OK {
		t.Fatalf("valid descendant response lost: %+v %v", response, err)
	}
}

// These tests exercise protocol/output/process-group behavior independently of
// the OS sandbox. Actual containment is exercised by sandbox_linux_test.go.
func testRunner(r Runner) Runner {
	r.commandBuilder = func(ctx context.Context, spec InvokeSpec) (*exec.Cmd, func(), error) {
		cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
		cmd.Dir = spec.WorkDir
		return cmd, func() {}, nil
	}
	return r
}
