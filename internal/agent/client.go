package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	agentapps "github.com/dropfile/HankServerside/internal/agent/apps"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	agentmcpcontext "github.com/dropfile/HankServerside/internal/agent/mcpcontext"
	agentnotes "github.com/dropfile/HankServerside/internal/agent/notes"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const maxMessageSize = 2 << 20

type Client struct {
	assistantServices *assistantServiceManager
	operationJournal  *operations.Journal
	registeredHomeID  string
	cloudURL          string
	agentID           string
	token             string
	homeName          string
	configPath        string
	logger            *slog.Logger
	dispatcher        commandDispatcher
	writeOnce         sync.Once
	writeGate         chan struct{}
	uploadsMu         sync.Mutex
	uploads           map[string]*uploadTransfer
	flowMu            sync.Mutex
	downloadFlows     map[string]*downloadWindow
	downloadsMu       sync.Mutex
	downloads         map[string]*downloadTask
	movesMu           sync.Mutex
	moves             map[string]context.CancelFunc
	moveSlots         chan struct{}
	restartFn         func()
	shellEnabled      atomic.Bool
}

func NewClient(cloudURL string, agentID string, token string, homeName string, configPath string, ha *agentha.Client, files *agentfiles.Service, notes *agentnotes.Service, apps *agentapps.Manager, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	if ha == nil {
		ha = agentha.New("", "", 0)
	}
	if files == nil {
		files = agentfiles.New("")
	}
	if notes == nil {
		notes = agentnotes.New("")
	}
	if apps == nil {
		apps = agentapps.NewManager("", "", agentapps.Runner{})
	}
	apps.SetPackageDownloadAuth(agentID, token)
	apps.SetFilesService(files)

	terminals := newTerminalManager(false, nil)
	client := &Client{
		cloudURL:   cloudURL,
		agentID:    agentID,
		token:      token,
		homeName:   homeName,
		configPath: configPath,
		logger:     logger,
		dispatcher: commandDispatcher{
			ha:                    ha,
			files:                 files,
			fileSourceRevisionKey: token,
			mcpctx:                agentmcpcontext.New(files),
			notes:                 notes,
			apps:                  apps,
			config:                newConfigManager(configPath, ha, files),
			terminals:             terminals,
		},
		uploads:       make(map[string]*uploadTransfer),
		downloads:     make(map[string]*downloadTask),
		downloadFlows: make(map[string]*downloadWindow),
		moves:         make(map[string]context.CancelFunc),
		moveSlots:     make(chan struct{}, 1),
		restartFn:     defaultRestartFn,
	}
	client.dispatcher.host = &hostService{shellEnabled: func() bool { return client.shellEnabled.Load() }}
	return client
}

func (c *Client) SetShellEnabled(enabled bool) {
	c.shellEnabled.Store(enabled)
	c.dispatcher.terminals.setEnabled(enabled)
}

func defaultRestartFn() {
	os.Exit(0)
}

func (c *Client) Run(ctx context.Context) error {
	backoff := 2 * time.Second

	for {
		err := c.runOnce(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}

		c.logger.Warn("agent connection ended; retrying", "agent_id", c.agentID, "error", err, "retry_in", backoff.String())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	wsURL, err := c.connectionURL()
	if err != nil {
		return err
	}

	c.logger.Info("connecting Hank agent", "agent_id", c.agentID, "cloud_url", wsURL)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer " + c.token},
			"X-Hank-Agent-ID": []string{c.agentID},
		},
	})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "agent shutting down")
	conn.SetReadLimit(maxMessageSize)

	if err := c.sendRegister(ctx, conn); err != nil {
		return err
	}

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	monitorCtx, stopMonitors := context.WithCancel(ctx)
	defer stopMonitors()

	readErr := make(chan error, 1)
	go func() {
		readErr <- c.readLoop(ctx, conn)
	}()
	go c.emitHomeAssistantChanges(monitorCtx, conn)
	go c.emitFileDirectoryChanges(monitorCtx, conn, "/")
	if c.dispatcher.apps != nil {
		c.dispatcher.apps.SetEventSink(func(ctx context.Context, event string, topic string, payload any) error {
			return c.sendAgentEvent(ctx, conn, event, topic, payload)
		})
		defer c.dispatcher.apps.SetEventSink(nil)
	}
	c.dispatcher.terminals.setEventSink(func(ctx context.Context, event string, topic string, payload any) error {
		return c.sendAgentEvent(ctx, conn, event, topic, payload)
	})
	defer c.dispatcher.terminals.setEventSink(nil)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-heartbeatTicker.C:
			if err := c.sendHeartbeat(ctx, conn); err != nil {
				return err
			}
		}
	}
}

func (c *Client) connectionURL() (string, error) {
	parsed, err := url.Parse(c.cloudURL)
	if err != nil {
		return "", fmt.Errorf("invalid cloud websocket url: %w", err)
	}
	return parsed.String(), nil
}

func (c *Client) sendRegister(ctx context.Context, conn *websocket.Conn) error {
	envelope, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", c.agentID, "", protocol.AgentRegister{
		AgentID:      c.agentID,
		HomeName:     c.homeName,
		AgentType:    "primary",
		Capabilities: c.capabilities(),
		Metadata: map[string]string{
			"build": "dev",
		},
	})
	if err != nil {
		return err
	}

	if err := c.writeJSON(ctx, conn, envelope); err != nil {
		return err
	}

	c.logger.Info("sent agent registration", "agent_id", c.agentID, "home_name", c.homeName)
	return nil
}

func (c *Client) sendHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	metrics, _ := json.Marshal(collectHostMetrics())
	envelope, err := protocol.NewEnvelope(protocol.TypeAgentHeartbeat, "", c.agentID, "", protocol.AgentHeartbeat{
		AgentID:      c.agentID,
		SentAt:       time.Now().UTC(),
		Capabilities: c.capabilities(),
		Metrics:      metrics,
	})
	if err != nil {
		return err
	}

	if err := c.writeJSON(ctx, conn, envelope); err != nil {
		return err
	}

	c.logger.Debug("sent agent heartbeat", "agent_id", c.agentID)
	return nil
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	transferCtx, cancelTransfers := context.WithCancel(ctx)
	defer cancelTransfers()
	defer c.closeConnectionUploads(conn)
	fileListSlots := make(chan struct{}, 4)
	fileSearchSlots := make(chan struct{}, 2)
	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			return err
		}

		switch envelope.Type {
		case protocol.TypeAgentRegistered:
			payload, err := protocol.DecodePayload[protocol.AgentRegistered](envelope)
			if err != nil {
				return err
			}
			c.registeredHomeID = payload.HomeID
			c.logger.Info("agent registration accepted", "agent_id", c.agentID, "home_id", payload.HomeID, "accepted_at", payload.AcceptedAt)

		case protocol.TypeAgentError:
			if envelope.Error != nil {
				c.logger.Warn("cloud returned protocol error", "agent_id", c.agentID, "code", envelope.Error.Code, "message", envelope.Error.Message)
			} else {
				c.logger.Warn("cloud returned unknown protocol error", "agent_id", c.agentID)
			}

		case protocol.TypeCloudCommand:
			if readCommand := fileReadCommand(envelope); readCommand != "" {
				fileReadSlots := fileListSlots
				if readCommand == "files.search" {
					fileReadSlots = fileSearchSlots
				}
				select {
				case fileReadSlots <- struct{}{}:
					go func() {
						defer func() { <-fileReadSlots }()
						timeout := 110 * time.Second
						if readCommand == "files.search" {
							timeout = 170 * time.Second
						}
						commandCtx, cancel := context.WithTimeout(transferCtx, timeout)
						defer cancel()
						if err := c.handleCommand(commandCtx, conn, envelope); err != nil && commandCtx.Err() == context.DeadlineExceeded && transferCtx.Err() == nil {
							_ = c.writeError(transferCtx, conn, envelope.RequestID, envelope.HomeID, "file_read_timeout", "file read exceeded its time limit", nil)
						} else if err != nil && transferCtx.Err() == nil {
							c.logger.Warn("file read command failed", "request_id", envelope.RequestID, "error", err)
						}
					}()
				default:
					if err := c.writeError(transferCtx, conn, envelope.RequestID, envelope.HomeID, "too_many_file_reads", "too many file reads are in progress", nil); err != nil {
						return err
					}
				}
				continue
			}
			if err := c.handleCommand(ctx, conn, envelope); err != nil {
				return err
			}

		case protocol.TypeFileTransferOpen:
			if err := c.handleTransferOpen(transferCtx, conn, envelope); err != nil {
				return err
			}

		case protocol.TypeFileTransferData:
			if err := c.handleTransferData(ctx, conn, envelope); err != nil {
				return err
			}

		case protocol.TypeFileTransferComplete:
			if err := c.handleTransferComplete(ctx, conn, envelope); err != nil {
				return err
			}

		case protocol.TypeFileTransferAck:
			c.handleTransferAck(envelope)

		case protocol.TypeFileTransferCancel:
			c.handleTransferCancel(envelope)

		default:
			c.logger.Info("received unsupported cloud message", "agent_id", c.agentID, "type", envelope.Type)
		}
	}
}

func fileReadCommand(envelope protocol.Envelope) string {
	command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
	if err == nil && (command.Command == "files.list" || command.Command == "files.list_page" || command.Command == "files.search") {
		return command.Command
	}
	return ""
}

func (c *Client) handleCommand(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope) error {
	command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_command_payload", err.Error(), nil)
	}

	c.logger.Info("handling cloud command", "agent_id", c.agentID, "home_id", envelope.HomeID, "command", command.Command, "request_id", envelope.RequestID)

	switch command.Command {
	case protocol.CommandAssistantServiceInspect:
		return c.handleAssistantServiceInspect(ctx, conn, envelope, command)
	case protocol.CommandAssistantOperationStage:
		return c.handleAssistantStage(ctx, conn, envelope, command)
	case protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus:
		return c.handleAssistantOperation(ctx, conn, envelope, command)
	case protocol.CommandSystemRestart:
		return c.handleRestartCommand(ctx, conn, envelope, command)
	case "files.move":
		return c.startMoveJob(ctx, conn, envelope, command)
	case "files.move_cancel":
		return c.cancelMoveJob(ctx, conn, envelope, command)
	}

	body, commandErr := c.dispatcher.dispatch(ctx, command)
	if commandErr != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, commandErr.Code, commandErr.Message, commandErr.Details)
	}

	responseBody, err := protocol.EncodeBody(body)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "encoding_failed", err.Error(), nil)
	}

	response := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeCloudResponse,
		RequestID: envelope.RequestID,
		AgentID:   c.agentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   responseBody,
	}
	return c.writeJSON(ctx, conn, response)
}

func (c *Client) handleRestartCommand(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	request, err := decodeBody[protocol.SystemRestartRequest](command.Body)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_restart_request", err.Error(), nil)
	}
	restartAt := time.Now().UTC().Add(500 * time.Millisecond)
	responseBody, err := protocol.EncodeBody(protocol.SystemRestartResponse{
		OK:        true,
		Message:   "agent restart scheduled",
		RestartAt: restartAt,
	})
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "encoding_failed", err.Error(), nil)
	}
	response := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeCloudResponse,
		RequestID: envelope.RequestID,
		AgentID:   c.agentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   responseBody,
	}
	if err := c.writeJSON(ctx, conn, response); err != nil {
		return err
	}
	c.logger.Warn("agent restart requested", "agent_id", c.agentID, "home_id", envelope.HomeID, "request_id", envelope.RequestID, "reason", request.Reason)
	go func() {
		time.Sleep(time.Until(restartAt))
		c.restartFn()
	}()
	return nil
}

func (c *Client) writeError(ctx context.Context, conn *websocket.Conn, requestID string, homeID string, code string, message string, details map[string]any) error {
	envelope := protocol.NewErrorEnvelope(protocol.TypeCloudResponse, requestID, c.agentID, homeID, code, message, details)
	return c.writeJSON(ctx, conn, envelope)
}

func (c *Client) capabilities() []string {
	capabilities := []string{protocol.CommandSystemPing, protocol.CommandSystemRestart, "files.sources", "files.list_page"}
	if c.operationJournal != nil {
		if c.assistantServices.available() {
			capabilities = append(capabilities, protocol.CommandAssistantServiceInspect)
		}
		capabilities = append(capabilities, protocol.CapabilityAssistantOperationsV2, protocol.CommandAssistantOperationStatus, protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStage, protocol.CapabilityAssistantJournalEpochPrefix+c.operationJournal.Epoch())
	}
	capabilities = append(capabilities, "host.read", "host.lock", "wol.send")
	if c.shellEnabled.Load() {
		capabilities = append(capabilities, "shell.exec", protocol.CommandShellSessionOpen, protocol.CommandShellSessionInput, protocol.CommandShellSessionResize, protocol.CommandShellSessionAttach, protocol.CommandShellSessionClose)
	}
	if c.dispatcher.ha.Enabled() {
		capabilities = append(capabilities,
			"homeassistant.health",
			"homeassistant.fetch_states",
			"homeassistant.fetch_state",
			"homeassistant.call_service",
		)
	}
	if c.dispatcher.files.Enabled() {
		capabilities = append(capabilities,
			protocol.CommandMCPContextList,
			protocol.CommandMCPContextSearch,
			protocol.CommandMCPContextRead,
			protocol.CommandMCPContextTest,
			"files.list",
			"files.stat",
			"files.search",
			"files.download",
			"file.transfer.flow_control.v1",
			"files.upload",
			"files.create_directory",
			"files.rename",
			"files.move",
			"files.move_cancel",
			"files.move_rollback",
			"files.delete",
		)
	}
	if c.dispatcher.notes.Enabled() {
		capabilities = append(capabilities,
			"notes.list",
			"notes.fetch",
			"notes.save",
			"notes.rename",
			"notes.delete",
			"notes.sync",
			"notes.search",
			"notes.tags",
			"notes.tag_rollup",
		)
	}
	if c.dispatcher.apps != nil {
		capabilities = append(capabilities,
			protocol.CapabilityAppsSandboxV1,
			protocol.CommandAppsList,
			protocol.CommandAppsPackagePreview,
			protocol.CommandAppsPackageActivate,
			protocol.CommandAppsUninstall,
			protocol.CommandAppsConfigStatus,
			protocol.CommandAppsConfigApply,
			protocol.CommandAppsInvoke,
		)
		capabilities = append(capabilities, c.dispatcher.apps.Capabilities()...)
	}
	capabilities = append(capabilities, "config.status", "config.apply", "config.smb_test")
	return capabilities
}

func (c *Client) writeJSON(ctx context.Context, conn *websocket.Conn, payload any) error {
	c.writeOnce.Do(func() { c.writeGate = make(chan struct{}, 1) })
	select {
	case c.writeGate <- struct{}{}:
		defer func() { <-c.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return wsjson.Write(ctx, conn, payload)
}

func (c *Client) emitHomeAssistantChanges(ctx context.Context, conn *websocket.Conn) {
	if !c.dispatcher.ha.Enabled() {
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	seen := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			states, err := c.dispatcher.ha.FetchStates(ctx)
			if err != nil {
				c.logger.Debug("home assistant realtime poll failed", "agent_id", c.agentID, "error", err)
				continue
			}
			for _, state := range states {
				hash := stableJSONHash(state)
				if previous, ok := seen[state.EntityID]; ok && previous == hash {
					continue
				}
				seen[state.EntityID] = hash
				if err := c.sendAgentEvent(ctx, conn, "homeassistant.state_changed", "homeassistant.states", map[string]any{"state": state}); err != nil {
					c.logger.Debug("home assistant realtime event failed", "agent_id", c.agentID, "error", err)
					return
				}
			}
		}
	}
}

func (c *Client) emitFileDirectoryChanges(ctx context.Context, conn *websocket.Conn, path string) {
	if !c.dispatcher.files.Enabled() {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var previous string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			items, err := c.dispatcher.files.List(ctx, path)
			if err != nil {
				c.logger.Debug("file realtime poll failed", "agent_id", c.agentID, "path", path, "error", err)
				continue
			}
			hash := stableJSONHash(items)
			if previous == hash {
				continue
			}
			previous = hash
			if err := c.sendAgentEvent(ctx, conn, "files.directory_changed", "files.directory:"+path, map[string]any{"path": path, "items": items}); err != nil {
				c.logger.Debug("file realtime event failed", "agent_id", c.agentID, "path", path, "error", err)
				return
			}
		}
	}
}

func (c *Client) sendAgentEvent(ctx context.Context, conn *websocket.Conn, event string, topic string, payload any) error {
	body, err := protocol.EncodeBody(payload)
	if err != nil {
		return err
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeAgentEvent, "", c.agentID, "", protocol.AgentEvent{
		Event: event,
		Topic: topic,
		Body:  body,
	})
	if err != nil {
		return err
	}
	return c.writeJSON(ctx, conn, envelope)
}

func stableJSONHash(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
