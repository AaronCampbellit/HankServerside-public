package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDownloadWindowSlowConsumerPreservesBytesAndSharedWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte("hank-flow-window!"), 128*1024)
	const offset int64 = 19
	flow := &downloadWindow{acked: offset, sent: offset, window: protocol.FileTransferWindowBytes, changed: make(chan struct{}, 1)}
	client := NewClient("", "agent", "", "", "", nil, nil, nil, nil, slog.New(discardHandler{}))
	client.downloadFlows["transfer"] = flow
	stalled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		go client.streamDownload(ctx, conn, "transfer", "home", "local", "file", offset, int64(len(payload)), 0, io.NopCloser(bytes.NewReader(payload[offset:])), flow)
		go func() {
			select {
			case <-stalled:
				_ = client.writeJSON(ctx, conn, protocol.Envelope{Type: "heartbeat-control"})
			case <-ctx.Done():
			}
		}()
		for {
			var envelope protocol.Envelope
			if wsjson.Read(ctx, conn, &envelope) != nil {
				return
			}
			client.handleTransferAck(envelope)
		}
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 * 1024)
	var received bytes.Buffer
	for i := 0; i < 8; i++ {
		var e protocol.Envelope
		if err := wsjson.Read(ctx, conn, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type != protocol.TypeFileTransferData {
			t.Fatalf("unexpected frame %s", e.Type)
		}
		chunk, _ := protocol.DecodePayload[protocol.FileTransferChunk](e)
		data, _ := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		received.Write(data)
	}
	// A full unacknowledged window pauses only the stream goroutine.
	close(stalled)
	var control protocol.Envelope
	if err := wsjson.Read(ctx, conn, &control); err != nil {
		t.Fatal(err)
	}
	if control.Type != "heartbeat-control" {
		t.Fatalf("agent exceeded window before acknowledgement: %s", control.Type)
	}
	ack := func() {
		e, _ := protocol.NewEnvelope(protocol.TypeFileTransferAck, "transfer", "agent", "home", protocol.FileTransferAck{Offset: offset + int64(received.Len())})
		if err := wsjson.Write(ctx, conn, e); err != nil {
			t.Fatal(err)
		}
	}
	ack()
	for {
		var e protocol.Envelope
		if err := wsjson.Read(ctx, conn, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == protocol.TypeFileTransferComplete {
			break
		}
		if e.Type != protocol.TypeFileTransferData {
			t.Fatalf("unexpected frame %s", e.Type)
		}
		chunk, _ := protocol.DecodePayload[protocol.FileTransferChunk](e)
		if chunk.Offset != offset+int64(received.Len()) {
			t.Fatal("noncontiguous stream")
		}
		data, _ := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		received.Write(data)
		ack()
	}
	if !bytes.Equal(received.Bytes(), payload[offset:]) {
		t.Fatal("resumed download lost or duplicated bytes")
	}
}

func TestDownloadWindowIgnoresForgedCreditAndCancels(t *testing.T) {
	flow := &downloadWindow{acked: 10, sent: 10, window: 32, changed: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !flow.reserve(ctx, 42) {
		t.Fatal("initial credit refused")
	}
	flow.acknowledge(43)
	flow.acknowledge(-1)
	flow.acknowledge(9)
	if flow.acked != 10 {
		t.Fatal("invalid acknowledgement granted credit")
	}
	done := make(chan bool, 1)
	go func() { done <- flow.reserve(ctx, 43) }()
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled reserve succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("credit waiter leaked")
	}
}

func TestDisconnectedDownloadReleasesCreditWaiter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "large-file")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(protocol.FileTransferWindowBytes)*2), 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient("", "agent", "", "", "", nil, agentfiles.New(root), nil, nil, slog.New(discardHandler{}))
	readerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = client.readLoop(ctx, conn)
		close(readerDone)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 * 1024)
	open, _ := protocol.NewEnvelope(protocol.TypeFileTransferOpen, "disconnect-transfer", "agent", "home", protocol.FileTransferOpen{Operation: "download", SourceID: "local", Path: "/large-file", FlowControl: protocol.FileTransferFlowControlV1, WindowBytes: protocol.FileTransferWindowBytes})
	if err := wsjson.Write(ctx, conn, open); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		var e protocol.Envelope
		if err := wsjson.Read(ctx, conn, &e); err != nil {
			t.Fatal(err)
		}
		if i == 0 && e.Type != protocol.TypeFileTransferReady {
			t.Fatalf("ready=%+v", e)
		}
	}
	_ = conn.CloseNow()
	select {
	case <-readerDone:
	case <-ctx.Done():
		t.Fatal("connection reader did not exit")
	}
	deadline := time.Now().Add(time.Second)
	for {
		client.downloadsMu.Lock()
		downloads := len(client.downloads)
		client.downloadsMu.Unlock()
		client.flowMu.Lock()
		flows := len(client.downloadFlows)
		client.flowMu.Unlock()
		openHandle := false
		if runtime.GOOS == "linux" {
			entries, _ := os.ReadDir("/proc/self/fd")
			for _, entry := range entries {
				target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
				if target == path {
					openHandle = true
				}
			}
		}
		if downloads == 0 && flows == 0 && !openHandle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected download leaked: tasks=%d windows=%d file_open=%v", downloads, flows, openHandle)
		}
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("test lifetime ended before connection-specific cleanup")
	}
}
