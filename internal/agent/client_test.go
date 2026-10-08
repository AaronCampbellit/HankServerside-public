package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }

func TestSlowFileSearchDoesNotBlockOtherFileListings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		close(accepted)
		<-ctx.Done()
		_ = conn.Close()
	}()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ready.txt"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := agentfiles.NewWithConfig(agentfiles.Config{
		Root:   root,
		Shares: []agentfiles.SMBConfig{{ID: "slow", Host: listener.Addr().String(), Share: "hold", Username: "test", Password: "test"}},
	})
	client := NewClient("", "agent", "", "", "", nil, service, nil, nil, slog.New(discardHandler{}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = client.readLoop(ctx, conn)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	command := func(id, name string, body any) {
		t.Helper()
		encoded, err := protocol.EncodeBody(body)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := protocol.NewEnvelope(protocol.TypeCloudCommand, id, "agent", "home", protocol.RoutedCommand{Command: name, Body: encoded})
		if err != nil {
			t.Fatal(err)
		}
		if err := wsjson.Write(ctx, conn, envelope); err != nil {
			t.Fatal(err)
		}
	}
	command("slow-search", "files.search", protocol.FilesSearchRequest{SourceID: "slow", Query: "missing"})
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("search did not reach slow share")
	}
	command("second-search", "files.search", protocol.FilesSearchRequest{SourceID: "slow", Query: "another"})
	command("fast-list", "files.list", protocol.FilesListRequest{SourceID: agentfiles.LocalSourceID, Path: "/"})
	var response protocol.Envelope
	if err := wsjson.Read(ctx, conn, &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "fast-list" || response.Error != nil {
		t.Fatalf("slow search blocked file listing: request=%s error=%+v", response.RequestID, response.Error)
	}
	listed, err := protocol.DecodePayload[protocol.FilesListResponse](response)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Name != "ready.txt" {
		t.Fatalf("file listing = %+v, error = %v", listed, err)
	}
}

func TestClientRegistersAsPrimaryAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := NewClient("ws://example.invalid", "agent_1", "token", "Home", "", nil, agentfiles.New(t.TempDir()), nil, nil, slog.New(discardHandler{}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			t.Errorf("read registration: %v", err)
			return
		}
		payload, err := protocol.DecodePayload[protocol.AgentRegister](envelope)
		if err != nil {
			t.Errorf("decode registration: %v", err)
			return
		}
		if payload.AgentType != "primary" {
			t.Errorf("agent_type = %q, want primary", payload.AgentType)
		}
		for _, capability := range []string{"files.sources", "files.list_page"} {
			if !slices.Contains(payload.Capabilities, capability) {
				t.Errorf("registration missing %s", capability)
			}
		}
	}))
	defer server.Close()

	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	if err := client.sendRegister(ctx, conn); err != nil {
		t.Fatalf("sendRegister: %v", err)
	}
}

func TestPrimaryAgentShellCapabilitiesFollowExistingEnableSwitch(t *testing.T) {
	client := NewClient("ws://example.invalid", "agent_1", "token", "Home", "", nil, nil, nil, nil, slog.New(discardHandler{}))
	for _, capability := range []string{"files.sources", "files.list_page"} {
		if !slices.Contains(client.capabilities(), capability) {
			t.Fatalf("catalog protocol missing %s when no source is configured", capability)
		}
	}
	if !slices.Contains(client.capabilities(), "config.smb_test") {
		t.Fatalf("primary capabilities missing config.smb_test: %v", client.capabilities())
	}
	if slices.Contains(client.capabilities(), protocol.CommandShellSessionOpen) || slices.Contains(client.capabilities(), "shell.exec") {
		t.Fatal("shell capabilities advertised while disabled")
	}
	client.SetShellEnabled(true)
	for _, capability := range []string{"host.read", "host.lock", "wol.send", "shell.exec", protocol.CommandShellSessionOpen} {
		if !slices.Contains(client.capabilities(), capability) {
			t.Fatalf("enabled capabilities missing %q: %v", capability, client.capabilities())
		}
	}
	client.SetShellEnabled(false)
	if slices.Contains(client.capabilities(), protocol.CommandShellSessionOpen) {
		t.Fatal("live shell remained advertised after disable")
	}
}

func TestClientSystemRestartAcknowledgesBeforeRestartHook(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	restarted := make(chan struct{}, 1)
	client := NewClient("ws://example.invalid", "agent_1", "token", "Home", "", nil, nil, nil, nil, slog.New(discardHandler{}))
	client.restartFn = func() {
		restarted <- struct{}{}
	}

	body, err := protocol.EncodeBody(protocol.SystemRestartRequest{Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	commandBody, err := protocol.EncodeBody(protocol.RoutedCommand{Command: protocol.CommandSystemRestart, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	envelope := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeCloudCommand,
		RequestID: "restart_req",
		AgentID:   "agent_1",
		HomeID:    "home_1",
		Timestamp: time.Now().UTC(),
		Payload:   commandBody,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		if err := client.handleCommand(ctx, conn, envelope); err != nil {
			t.Errorf("handleCommand: %v", err)
		}
	}))
	defer server.Close()

	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	var response protocol.Envelope
	if err := wsjson.Read(ctx, conn, &response); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.Type != protocol.TypeCloudResponse || response.RequestID != envelope.RequestID {
		t.Fatalf("response envelope = %#v", response)
	}
	var payload protocol.SystemRestartResponse
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Message == "" || payload.RestartAt.IsZero() {
		t.Fatalf("restart response = %#v", payload)
	}

	select {
	case <-restarted:
	case <-ctx.Done():
		t.Fatal("restart hook was not called")
	}
}

func TestDownloadTransferHonorsRequestedLength(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewClient("ws://example.invalid", "agent_1", "token", "Home", "", nil, agentfiles.New(root), nil, nil, slog.New(discardHandler{}))

	open, err := protocol.NewEnvelope(protocol.TypeFileTransferOpen, "xfer_1", "agent_1", "home_1", protocol.FileTransferOpen{
		Operation: protocol.FileTransferOperationDownload,
		Path:      "demo.txt",
		Offset:    6,
		Length:    5,
	})
	if err != nil {
		t.Fatal(err)
	}

	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		if err := client.handleTransferOpen(ctx, conn, open); err != nil {
			t.Errorf("handleTransferOpen: %v", err)
			return
		}
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)

	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	var ready protocol.Envelope
	if err := wsjson.Read(ctx, conn, &ready); err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if ready.Type != protocol.TypeFileTransferReady {
		t.Fatalf("ready envelope type = %q", ready.Type)
	}

	var dataEnvelope protocol.Envelope
	if err := wsjson.Read(ctx, conn, &dataEnvelope); err != nil {
		t.Fatalf("read data: %v", err)
	}
	if dataEnvelope.Type != protocol.TypeFileTransferData {
		t.Fatalf("data envelope type = %q", dataEnvelope.Type)
	}
	chunk, err := protocol.DecodePayload[protocol.FileTransferChunk](dataEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Offset != 6 || string(data) != "world" {
		t.Fatalf("chunk offset/data = %d/%q, want 6/world", chunk.Offset, string(data))
	}

	var completeEnvelope protocol.Envelope
	if err := wsjson.Read(ctx, conn, &completeEnvelope); err != nil {
		t.Fatalf("read complete: %v", err)
	}
	complete, err := protocol.DecodePayload[protocol.FileTransferComplete](completeEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if completeEnvelope.Type != protocol.TypeFileTransferComplete || complete.Offset != 11 || complete.Size != 11 {
		t.Fatalf("complete = type %q payload %#v", completeEnvelope.Type, complete)
	}
}
