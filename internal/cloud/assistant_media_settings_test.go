package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestAssistantMediaSettingsEndpointAppliesThroughAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	advertiseGramatonAppCapabilities(t, ctx, testServer, sessionToken, agentConn, agentID, homeID)

	requestCh := make(chan protocol.MediaSettingsApplyRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOneMediaAgentCommand(ctx, agentConn, agentID, homeID, protocol.CommandMediaSettingsApply, func(body json.RawMessage) (any, error) {
			var request protocol.MediaSettingsApplyRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			requestCh <- request
			settings := request.Settings
			settings.HasPassword = request.Password != ""
			return protocol.MediaSettingsApplyResponse{Settings: settings}, nil
		})
	}()

	var response assistantMediaSettingsResponse
	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/home/assistant/media-settings", map[string]any{
		"settings": map[string]any{
			"enabled":                true,
			"base_url":               "https://gramaton.io",
			"username":               "media@example.com",
			"source_id":              "archive",
			"destination_path":       "Media",
			"movie_destination_path": "Movies",
			"tv_destination_path":    "Shows",
		},
		"password": "test-password",
	}, &response)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	request := <-requestCh
	if !request.Persist || request.Password != "test-password" || request.Settings.Username != "media@example.com" || request.Settings.SourceID != "archive" || request.Settings.MovieDestinationPath != "Movies" || request.Settings.TVDestinationPath != "Shows" {
		t.Fatalf("settings apply request = %#v", request)
	}
	if !response.Online || !response.Settings.Enabled || !response.Settings.HasPassword {
		t.Fatalf("settings response = %#v", response)
	}
}

func TestAssistantMediaSettingsEndpointReturnsDestinationOptions(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	advertiseGramatonAppCapabilities(t, ctx, testServer, sessionToken, agentConn, agentID, homeID)

	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOneMediaAgentCommand(ctx, agentConn, agentID, homeID, protocol.CommandMediaSettingsStatus, func(body json.RawMessage) (any, error) {
			var request protocol.MediaSettingsStatusRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			return protocol.MediaSettingsStatusResponse{
				Settings: protocol.MediaSettings{
					BaseURL:              "https://gramaton.io",
					DestinationPath:      "Media",
					MovieDestinationPath: "Movies",
					TVDestinationPath:    "Shows",
					PreferredQuality:     "1080p",
					RequireConfirmation:  true,
				},
				DestinationOptions: []protocol.MediaDestinationOption{
					{Value: "", Label: "SMB share root"},
					{Value: "Movies", Label: "SMB share/Movies"},
					{Value: "Shows", Label: "SMB share/Shows"},
				},
			}, nil
		})
	}()

	var response assistantMediaSettingsResponse
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/assistant/media-settings", nil, &response)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !response.Online || response.Settings.MovieDestinationPath != "Movies" || response.Settings.TVDestinationPath != "Shows" {
		t.Fatalf("settings response = %#v", response)
	}
	values := map[string]string{}
	for _, option := range response.DestinationOptions {
		values[option.Value] = option.Label
	}
	if values["Movies"] != "SMB share/Movies" || values["Shows"] != "SMB share/Shows" {
		t.Fatalf("destination options = %#v", response.DestinationOptions)
	}
}

func TestAssistantMediaJobCancelEndpointRoutesToAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	advertiseGramatonAppCapabilities(t, ctx, testServer, sessionToken, agentConn, agentID, homeID)

	requestCh := make(chan protocol.MediaDownloadCancelRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOneMediaAgentCommand(ctx, agentConn, agentID, homeID, protocol.CommandMediaDownloadCancel, func(body json.RawMessage) (any, error) {
			var request protocol.MediaDownloadCancelRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			requestCh <- request
			return protocol.MediaDownloadCancelResponse{Job: protocol.MediaDownloadJobStatus{
				JobID:  request.JobID,
				Status: protocol.MediaJobStatusCancelled,
				Title:  "Fixture Movie",
			}}, nil
		})
	}()

	var response assistantMediaJobCancelResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/media-jobs/job_fixture/cancel", nil, &response)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	request := <-requestCh
	if request.JobID != "job_fixture" {
		t.Fatalf("cancel request = %#v", request)
	}
	if !response.Online || response.Job.Status != protocol.MediaJobStatusCancelled {
		t.Fatalf("cancel response = %#v", response)
	}
}

func TestAssistantMediaJobStatusEndpointRoutesToAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	advertiseGramatonAppCapabilities(t, ctx, testServer, sessionToken, agentConn, agentID, homeID)

	requestCh := make(chan protocol.MediaDownloadStatusRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOneMediaAgentCommand(ctx, agentConn, agentID, homeID, protocol.CommandMediaDownloadStatus, func(body json.RawMessage) (any, error) {
			var request protocol.MediaDownloadStatusRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			requestCh <- request
			return protocol.MediaDownloadStatusResponse{Job: protocol.MediaDownloadJobStatus{
				JobID:        request.JobID,
				Status:       protocol.MediaJobStatusRunning,
				Title:        "Fixture Movie",
				TotalCount:   1,
				CurrentFile:  "Fixture Movie.mp4",
				BytesWritten: 42,
				BytesTotal:   100,
			}}, nil
		})
	}()

	var response assistantMediaJobStatusResponse
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/assistant/media-jobs/job_fixture", nil, &response)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	request := <-requestCh
	if request.JobID != "job_fixture" {
		t.Fatalf("status request = %#v", request)
	}
	if !response.Online || response.Job.Status != protocol.MediaJobStatusRunning || response.Job.BytesWritten != 42 {
		t.Fatalf("status response = %#v", response)
	}
}

func TestAssistantMediaImageEndpointProxiesThroughAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")
	advertiseGramatonAppCapabilities(t, ctx, testServer, sessionToken, agentConn, agentID, homeID)

	imageBytes := []byte{0x89, 'P', 'N', 'G'}
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOneMediaAgentCommand(ctx, agentConn, agentID, homeID, protocol.CommandMediaImageFetch, func(body json.RawMessage) (any, error) {
			var request protocol.MediaImageFetchRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			if request.URL != "https://image.example/poster.png" {
				return nil, fmt.Errorf("media image URL = %q", request.URL)
			}
			return protocol.MediaImageFetchResponse{
				URL:           request.URL,
				ContentType:   "image/png",
				ContentBase64: base64.StdEncoding.EncodeToString(imageBytes),
			}, nil
		})
	}()

	request, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/home/assistant/media-image?url="+url.QueryEscape("https://image.example/poster.png"), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/png" || string(data) != string(imageBytes) {
		t.Fatalf("image response status=%d content-type=%q data=%v", response.StatusCode, response.Header.Get("Content-Type"), data)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func serveOneMediaAgentCommand(ctx context.Context, agentConn *websocket.Conn, agentID string, homeID string, wantCommand string, handler func(json.RawMessage) (any, error)) error {
	wantCommandID, ok := gramatonAppCommandID(wantCommand)
	if !ok {
		return fmt.Errorf("unsupported Gramaton test command %q", wantCommand)
	}
	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return err
		}
		if envelope.Type != protocol.TypeCloudCommand {
			continue
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			return err
		}
		if command.Command != protocol.CommandAppsInvoke {
			return fmt.Errorf("command = %q, want %q", command.Command, protocol.CommandAppsInvoke)
		}
		invoke, err := decodeProtocolBody[protocol.AppsInvokeRequest](command.Body)
		if err != nil {
			return err
		}
		if invoke.AppID != "gramaton" || invoke.CommandID != wantCommandID {
			return fmt.Errorf("apps.invoke = %s/%s, want gramaton/%s", invoke.AppID, invoke.CommandID, wantCommandID)
		}
		responsePayload, err := handler(invoke.Input)
		if err != nil {
			return err
		}
		output, err := protocol.EncodeBody(responsePayload)
		if err != nil {
			return err
		}
		response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.AppsInvokeResponse{Output: output})
		if err != nil {
			return err
		}
		return wsjson.Write(ctx, agentConn, response)
	}
}

func advertiseGramatonAppCapabilities(t *testing.T, ctx context.Context, testServer *httptest.Server, sessionToken string, agentConn *websocket.Conn, agentID string, homeID string) {
	t.Helper()

	heartbeat, err := protocol.NewEnvelope(protocol.TypeAgentHeartbeat, "", agentID, homeID, protocol.AgentHeartbeat{
		AgentID: agentID,
		SentAt:  time.Now().UTC(),
		Capabilities: []string{
			protocol.CapabilityAppsSandboxV1,
			"apps.gramaton.settings_status",
			"apps.gramaton.settings_apply",
			"apps.gramaton.search",
			"apps.gramaton.plan_download",
			"apps.gramaton.download_start",
			"apps.gramaton.download_status",
			"apps.gramaton.download_jobs",
			"apps.gramaton.download_cancel",
			"apps.gramaton.image_fetch",
		},
	})
	if err != nil {
		t.Fatalf("NewEnvelope heartbeat: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, heartbeat); err != nil {
		t.Fatalf("heartbeat write: %v", err)
	}
	waitForAgentCapability(t, testServer, sessionToken, "apps.gramaton.search")
}
