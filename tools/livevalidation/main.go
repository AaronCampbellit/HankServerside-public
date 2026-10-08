package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type liveClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "live validation failed: %v\n", err)
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
	firstSource := envOrDefault("HANK_LIVE_SOURCE_ONE", "primary")
	secondSource := envOrDefault("HANK_LIVE_SOURCE_TWO", "secondary")
	runID := envOrDefault("HANK_LIVE_RUN_ID", "live-"+time.Now().UTC().Format("20060102T150405Z"))
	root := "_hank_validation/" + runID
	payload := "hank live validation payload " + runID + "\n"
	requestID := func(suffix string) string {
		return runID + "_" + suffix
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := liveClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: envDuration("HANK_LIVE_HTTP_TIMEOUT_SECONDS", 30*time.Second)},
	}

	var home map[string]any
	if err := client.doJSON(ctx, http.MethodGet, "/v1/home", nil, http.StatusOK, &home); err != nil {
		return fmt.Errorf("GET /v1/home: %w", err)
	}
	fmt.Printf("PASS home loaded id=%v name=%v\n", home["id"], home["name"])

	var agent struct {
		Agent map[string]any `json:"agent"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/v1/home/agent", nil, http.StatusOK, &agent); err != nil {
		return fmt.Errorf("GET /v1/home/agent: %w", err)
	}
	if agent.Agent == nil || agent.Agent["status"] != "online" {
		return fmt.Errorf("agent is not online: %#v", agent.Agent)
	}
	fmt.Printf("PASS agent online id=%v\n", agent.Agent["agent_id"])

	appWS, err := client.openAppWebSocket(ctx)
	if err != nil {
		return err
	}
	defer appWS.Close(websocket.StatusNormalClosure, "validation complete")

	var ping protocol.SystemPingResponse
	if err := sendCommand(ctx, appWS, requestID("ping"), protocol.CommandSystemPing, protocol.SystemPingRequest{Message: "live-validation"}, &ping); err != nil {
		return err
	}
	fmt.Printf("PASS system.ping message=%q\n", ping.Message)

	var health protocol.HomeAssistantHealthResponse
	if err := sendCommand(ctx, appWS, requestID("ha_health"), "homeassistant.health", nil, &health); err != nil {
		return err
	}
	if !health.OK {
		return errors.New("homeassistant.health returned ok=false")
	}
	fmt.Println("PASS homeassistant.health")

	var states protocol.HomeAssistantFetchStatesResponse
	if err := sendCommand(ctx, appWS, requestID("ha_states"), "homeassistant.fetch_states", nil, &states); err != nil {
		return err
	}
	if len(states.States) == 0 {
		return errors.New("homeassistant.fetch_states returned no states")
	}
	fmt.Printf("PASS homeassistant.fetch_states count=%d first=%s\n", len(states.States), states.States[0].EntityID)

	if err := sendCommand(ctx, appWS, requestID("mkdir_one"), "files.create_directory", protocol.FilesCreateDirectoryRequest{SourceID: firstSource, Path: root}, nil); err != nil {
		return err
	}
	if err := testHTTPTransfers(ctx, client, firstSource, root+"/transfer.txt", payload); err != nil {
		return err
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	if err := sendCommand(ctx, appWS, requestID("upload_one"), "files.upload", protocol.FilesUploadRequest{SourceID: firstSource, Path: root + "/ws.txt", ContentBase64: encoded}, nil); err != nil {
		return err
	}
	var download protocol.FilesDownloadResponse
	if err := sendCommand(ctx, appWS, requestID("download_one"), "files.download", protocol.FilesDownloadRequest{SourceID: firstSource, Path: root + "/ws.txt"}, &download); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(download.ContentBase64)
	if err != nil {
		return err
	}
	if string(decoded) != payload {
		return fmt.Errorf("websocket file payload mismatch: got %q", string(decoded))
	}
	fmt.Printf("PASS files upload/download source=%s\n", firstSource)

	if err := sendCommand(ctx, appWS, requestID("rename_one"), "files.rename", protocol.FilesRenameRequest{SourceID: firstSource, From: root + "/ws.txt", To: root + "/renamed.txt"}, nil); err != nil {
		return err
	}
	if err := sendCommand(ctx, appWS, requestID("move_two"), "files.move", protocol.FilesMoveRequest{SourceID: firstSource, DestinationSourceID: secondSource, From: root + "/renamed.txt", To: root + "/moved.txt"}, nil); err != nil {
		return err
	}
	var moved protocol.FilesDownloadResponse
	if err := sendCommand(ctx, appWS, requestID("download_two"), "files.download", protocol.FilesDownloadRequest{SourceID: secondSource, Path: root + "/moved.txt"}, &moved); err != nil {
		return err
	}
	movedDecoded, err := base64.StdEncoding.DecodeString(moved.ContentBase64)
	if err != nil {
		return err
	}
	if string(movedDecoded) != payload {
		return fmt.Errorf("cross-source moved payload mismatch: got %q", string(movedDecoded))
	}
	fmt.Printf("PASS files rename/move/download sources=%s,%s\n", firstSource, secondSource)

	cleanupErrs := []error{
		sendCommand(ctx, appWS, requestID("delete_transfer"), "files.delete", protocol.FilesDeleteRequest{SourceID: firstSource, Path: root + "/transfer.txt"}, nil),
		sendCommand(ctx, appWS, requestID("delete_root_one"), "files.delete", protocol.FilesDeleteRequest{SourceID: firstSource, Path: root, IsDirectory: true}, nil),
		sendCommand(ctx, appWS, requestID("delete_moved"), "files.delete", protocol.FilesDeleteRequest{SourceID: secondSource, Path: root + "/moved.txt"}, nil),
		sendCommand(ctx, appWS, requestID("delete_root_two"), "files.delete", protocol.FilesDeleteRequest{SourceID: secondSource, Path: root, IsDirectory: true}, nil),
	}
	for _, err := range cleanupErrs {
		if err != nil {
			return err
		}
	}
	fmt.Println("PASS files cleanup")
	fmt.Println("PASS live validation complete")
	return nil
}

func (c liveClient) openAppWebSocket(ctx context.Context) (*websocket.Conn, error) {
	var ticket struct {
		WebSocketPath string `json:"websocket_path"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/ws/app-ticket", nil, http.StatusCreated, &ticket); err != nil {
		return nil, fmt.Errorf("POST /v1/ws/app-ticket: %w", err)
	}
	if !strings.HasPrefix(ticket.WebSocketPath, "/ws/app?app_ticket=") {
		return nil, fmt.Errorf("unexpected websocket path %q", ticket.WebSocketPath)
	}
	wsURL := *c.baseURL
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	parsedPath, err := url.Parse(ticket.WebSocketPath)
	if err != nil {
		return nil, err
	}
	wsURL.Path = parsedPath.Path
	wsURL.RawQuery = parsedPath.RawQuery
	conn, _, err := websocket.Dial(ctx, wsURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial app websocket: %w", err)
	}
	conn.SetReadLimit(32 << 20)
	return conn, nil
}

func sendCommand(ctx context.Context, conn *websocket.Conn, requestID string, command string, body any, out any) error {
	rawBody, err := protocol.EncodeBody(body)
	if err != nil {
		return err
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeAppCommand, requestID, "", "", protocol.RoutedCommand{
		Command: command,
		Body:    rawBody,
	})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, conn, envelope); err != nil {
		return fmt.Errorf("%s write: %w", command, err)
	}
	for {
		var response protocol.Envelope
		if err := wsjson.Read(ctx, conn, &response); err != nil {
			return fmt.Errorf("%s read: %w", command, err)
		}
		if response.RequestID != requestID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("%s returned %s: %s", command, response.Error.Code, response.Error.Message)
		}
		if response.Type != protocol.TypeAppResponse {
			return fmt.Errorf("%s response type %q", command, response.Type)
		}
		if out != nil && len(response.Payload) > 0 {
			if err := json.Unmarshal(response.Payload, out); err != nil {
				return fmt.Errorf("%s decode: %w", command, err)
			}
		}
		return nil
	}
}

func testHTTPTransfers(ctx context.Context, client liveClient, sourceID string, path string, payload string) error {
	var setup struct {
		TransferID    string `json:"transfer_id"`
		TransferToken string `json:"transfer_token"`
		URL           string `json:"url"`
	}
	if err := client.doJSON(ctx, http.MethodPost, "/v1/home/files/uploads", map[string]string{"source_id": sourceID, "path": path}, http.StatusCreated, &setup); err != nil {
		return fmt.Errorf("setup upload transfer: %w", err)
	}
	if setup.TransferID == "" || setup.TransferToken == "" || setup.URL == "" {
		return fmt.Errorf("upload setup missing transfer fields: transfer_id_present=%t transfer_token_present=%t url_present=%t", setup.TransferID != "", setup.TransferToken != "", setup.URL != "")
	}
	if strings.Contains(setup.URL, setup.TransferToken) || strings.Contains(setup.URL, "token=") {
		return fmt.Errorf("upload setup returned token-bearing URL %q", setup.URL)
	}
	status, _, err := client.doRaw(ctx, http.MethodPut, setup.URL+"?token="+url.QueryEscape(setup.TransferToken), []byte(payload), "")
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("upload query-token status=%d, want 401", status)
	}
	status, body, err := client.doRaw(ctx, http.MethodPut, setup.URL, []byte(payload), setup.TransferToken)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("upload bearer status=%d body=%s", status, string(body))
	}

	var downloadSetup struct {
		TransferID    string `json:"transfer_id"`
		TransferToken string `json:"transfer_token"`
		URL           string `json:"url"`
	}
	if err := client.doJSON(ctx, http.MethodPost, "/v1/home/files/downloads", map[string]string{"source_id": sourceID, "path": path}, http.StatusCreated, &downloadSetup); err != nil {
		return fmt.Errorf("setup download transfer: %w", err)
	}
	if downloadSetup.TransferID == "" || downloadSetup.TransferToken == "" || downloadSetup.URL == "" {
		return fmt.Errorf("download setup missing transfer fields: transfer_id_present=%t transfer_token_present=%t url_present=%t", downloadSetup.TransferID != "", downloadSetup.TransferToken != "", downloadSetup.URL != "")
	}
	if strings.Contains(downloadSetup.URL, downloadSetup.TransferToken) || strings.Contains(downloadSetup.URL, "token=") {
		return fmt.Errorf("download setup returned token-bearing URL %q", downloadSetup.URL)
	}
	status, _, err = client.doRaw(ctx, http.MethodGet, downloadSetup.URL+"?token="+url.QueryEscape(downloadSetup.TransferToken), nil, "")
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("download query-token status=%d, want 401", status)
	}
	status, body, err = client.doRaw(ctx, http.MethodGet, downloadSetup.URL, nil, downloadSetup.TransferToken)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("download bearer status=%d body=%s", status, string(body))
	}
	if string(body) != payload {
		return fmt.Errorf("download bearer payload mismatch: got %q", string(body))
	}
	fmt.Printf("PASS file transfer bearer-only source=%s\n", sourceID)
	return nil
}

func (c liveClient) doJSON(ctx context.Context, method string, path string, body any, wantStatus int, out any) error {
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

func (c liveClient) doRaw(ctx context.Context, method string, path string, body []byte, bearer string) (int, []byte, error) {
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

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}
