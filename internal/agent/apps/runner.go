package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxOutputBytes int64 = 1 << 20
	defaultMaxStderrBytes int64 = 64 << 10
)

var appInvocationSlots = make(chan struct{}, 8)

type AppStdioRequest struct {
	ProtocolVersion string          `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	AppID           string          `json:"app_id"`
	CommandID       string          `json:"command_id"`
	Config          json.RawMessage `json:"config,omitempty"`
	Secrets         json.RawMessage `json:"secrets,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	Context         json.RawMessage `json:"context,omitempty"`
}

type AppStdioResponse struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Output    json.RawMessage `json:"output,omitempty"`
	Events    []AppStdioEvent `json:"events,omitempty"`
	Error     *AppError       `json:"error,omitempty"`
}

type AppStdioEvent struct {
	Event string          `json:"event"`
	Topic string          `json:"topic,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type InvokeSpec struct {
	CgroupRoot  string
	RuntimeDir  string
	PackageHash string
	Broker      BrokerHandler
	Executable  string
	Args        []string
	WorkDir     string
	Timeout     time.Duration
	Request     AppStdioRequest
}

type Runner struct {
	CgroupRoot     string
	RuntimeDir     string
	commandBuilder func(context.Context, InvokeSpec) (*exec.Cmd, func(), error)
	MaxOutputBytes int64
	MaxStderrBytes int64
}

func (r Runner) Invoke(ctx context.Context, spec InvokeSpec) (AppStdioResponse, error) {
	requestLine, err := json.Marshal(spec.Request)
	if err != nil {
		return AppStdioResponse{}, fmt.Errorf("marshal app request: %w", err)
	}
	requestLine = append(requestLine, '\n')

	var runCtx context.Context
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	select {
	case appInvocationSlots <- struct{}{}:
		defer func() { <-appInvocationSlots }()
	case <-runCtx.Done():
		return AppStdioResponse{}, runCtx.Err()
	}

	spec.RuntimeDir = r.RuntimeDir
	spec.CgroupRoot = r.CgroupRoot
	build := r.commandBuilder
	if build == nil {
		build = sandboxCommand
	}
	cmd, cleanup, err := build(runCtx, spec)
	if err != nil {
		return AppStdioResponse{}, err
	}
	defer cleanup()
	configureInvocationProcess(cmd)
	cmd.Stdin = bytes.NewReader(requestLine)

	stdout := newBoundedBuffer("stdout", byteLimit(r.MaxOutputBytes, defaultMaxOutputBytes), cancel)
	stderr := newBoundedBuffer("stderr", byteLimit(r.MaxStderrBytes, defaultMaxStderrBytes), cancel)
	runErr := runInvocation(runCtx, cmd, stdout, stderr)

	if err := stdout.Err(); err != nil {
		return AppStdioResponse{}, err
	}
	if err := stderr.Err(); err != nil {
		return AppStdioResponse{}, err
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return AppStdioResponse{}, fmt.Errorf("app invocation timed out after %s", spec.Timeout)
	}
	if errors.Is(runCtx.Err(), context.Canceled) && runErr != nil {
		return AppStdioResponse{}, fmt.Errorf("app invocation canceled: %w", runCtx.Err())
	}
	if runErr != nil && strings.HasPrefix(strings.TrimSpace(string(stderr.Bytes())), "bwrap:") {
		return AppStdioResponse{}, ErrSandboxUnavailable
	}
	if runErr != nil {
		if response, err := decodeAppResponse(stdout.Bytes(), spec.Request.RequestID); err == nil {
			return response, nil
		}
		return AppStdioResponse{}, fmt.Errorf("app invocation failed: %w", runErr)
	}

	return decodeAppResponse(stdout.Bytes(), spec.Request.RequestID)
}

type boundedBuffer struct {
	name     string
	maxBytes int64
	buf      bytes.Buffer
	err      error
	cancel   context.CancelFunc
}

func newBoundedBuffer(name string, maxBytes int64, cancel context.CancelFunc) *boundedBuffer {
	return &boundedBuffer{
		name:     name,
		maxBytes: maxBytes,
		cancel:   cancel,
	}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}

	remaining := b.maxBytes - int64(b.buf.Len())
	if remaining > 0 {
		toWrite := int64(len(p))
		if toWrite > remaining {
			toWrite = remaining
		}
		if toWrite > 0 {
			_, _ = b.buf.Write(p[:toWrite])
		}
	}

	if int64(len(p)) > remaining {
		b.err = fmt.Errorf("app %s exceeded %d bytes", b.name, b.maxBytes)
		b.cancel()
		return len(p), b.err
	}

	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *boundedBuffer) Err() error {
	return b.err
}

func byteLimit(configured int64, fallback int64) int64 {
	if configured > 0 {
		return configured
	}
	return fallback
}

func decodeAppResponse(data []byte, requestID string) (AppStdioResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))

	var response AppStdioResponse
	if err := decoder.Decode(&response); err != nil {
		return AppStdioResponse{}, fmt.Errorf("invalid app response: %w", err)
	}

	var trailing json.RawMessage
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return AppStdioResponse{}, fmt.Errorf("invalid app response: trailing JSON token after app response")
	case errors.Is(err, io.EOF):
		return validateAppResponse(response, requestID)
	default:
		return AppStdioResponse{}, fmt.Errorf("invalid app response: %w", err)
	}
}

func validateAppResponse(response AppStdioResponse, requestID string) (AppStdioResponse, error) {
	if requestID != "" && response.RequestID != requestID {
		return AppStdioResponse{}, fmt.Errorf("invalid app response: request_id %q does not match request %q", response.RequestID, requestID)
	}
	if response.OK && response.Error != nil {
		return AppStdioResponse{}, fmt.Errorf("invalid app response: ok response must not include error")
	}
	if !response.OK && response.Error == nil {
		return AppStdioResponse{}, fmt.Errorf("invalid app response: error response must include error")
	}
	if response.Error != nil && (response.Error.Code == "" || response.Error.Message == "") {
		return AppStdioResponse{}, fmt.Errorf("invalid app response: error code and message are required")
	}
	for index, event := range response.Events {
		if event.Event == "" {
			return AppStdioResponse{}, fmt.Errorf("invalid app response: event %d is missing event name", index)
		}
	}
	return response, nil
}

// Own the output pipes so cancellation remains effective after the direct
// process exits. A child may legitimately finish the response within the
// invocation deadline; a cancelled invocation gets only a bounded drain.
func runInvocation(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) error {
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer errR.Close()
	defer errW.Close()
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inR.Close()
	defer inW.Close()
	input := cmd.Stdin
	cmd.Stdin = inR
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return err
	}
	var readers sync.WaitGroup
	readers.Add(3)
	inR.Close()
	go func() { defer readers.Done(); _, _ = io.Copy(inW, input); inW.Close() }()
	go func() { defer readers.Done(); _, _ = io.Copy(stdout, outR) }()
	go func() { defer readers.Done(); _, _ = io.Copy(stderr, errR) }()
	outputsDone := make(chan struct{})
	go func() { readers.Wait(); close(outputsDone) }()
	runErr := cmd.Wait()
	outW.Close()
	errW.Close()
	select {
	case <-outputsDone:
	case <-ctx.Done():
		// CommandContext's watcher may already have exited with the parent.
		_ = cmd.Cancel()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-outputsDone:
		case <-timer.C:
			outR.Close()
			errR.Close()
			inW.Close()
			<-outputsDone
		}
		timer.Stop()
	}
	if ctx.Err() != nil {
		_ = cmd.Cancel()
	}
	return runErr
}
