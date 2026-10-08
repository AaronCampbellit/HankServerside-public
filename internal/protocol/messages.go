package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const Version = "v1"

const (
	TypeAgentRegister        = "agent.register"
	TypeAgentRegistered      = "agent.registered"
	TypeAgentHeartbeat       = "agent.heartbeat"
	TypeAgentEvent           = "agent.event"
	TypeAgentError           = "agent.error"
	TypeAppCommand           = "app.command"
	TypeAppResponse          = "app.response"
	TypeAppEvent             = "app.event"
	TypeAppError             = "app.error"
	TypeCloudCommand         = "cloud.command"
	TypeCloudResponse        = "cloud.response"
	TypeFileTransferAck      = "file.transfer.ack"
	TypeFileTransferOpen     = "file.transfer.open"
	TypeFileTransferReady    = "file.transfer.ready"
	TypeFileTransferData     = "file.transfer.data"
	TypeFileTransferComplete = "file.transfer.complete"
	TypeFileTransferCancel   = "file.transfer.cancel"
	TypeFileTransferError    = "file.transfer.error"
)

const (
	CommandSystemPing         = "system.ping"
	CommandSystemRestart      = "system.restart"
	CommandSystemUpdateApply  = "system.update.apply"
	CommandShellSessionOpen   = "shell.session.open"
	CommandShellSessionInput  = "shell.session.input"
	CommandShellSessionResize = "shell.session.resize"
	CommandShellSessionAttach = "shell.session.attach"
	CommandShellSessionClose  = "shell.session.close"
)

const MaxShellInputBytes = 64 * 1024

var shellSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func ShellSessionTopic(sessionID string) string { return "shell.session:" + sessionID }

type ShellSessionOpenRequest struct {
	SessionID string `json:"session_id"`
	Columns   uint16 `json:"columns"`
	Rows      uint16 `json:"rows"`
}

func (r ShellSessionOpenRequest) Validate() error {
	if !shellSessionIDPattern.MatchString(r.SessionID) {
		return errors.New("invalid shell session id")
	}
	return validateTerminalSize(r.Columns, r.Rows)
}

type ShellSessionOpenResponse struct {
	SessionID string `json:"session_id"`
	Cursor    uint64 `json:"cursor"`
	Shell     string `json:"shell,omitempty"`
}

type ShellSessionInputRequest struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
}

func (r ShellSessionInputRequest) Validate() error {
	if !shellSessionIDPattern.MatchString(r.SessionID) {
		return errors.New("invalid shell session id")
	}
	if len(r.Data) == 0 || len(r.Data) > MaxShellInputBytes {
		return fmt.Errorf("shell input must be between 1 and %d bytes", MaxShellInputBytes)
	}
	return nil
}

type ShellSessionResizeRequest struct {
	SessionID string `json:"session_id"`
	Columns   uint16 `json:"columns"`
	Rows      uint16 `json:"rows"`
}

func (r ShellSessionResizeRequest) Validate() error {
	if !shellSessionIDPattern.MatchString(r.SessionID) {
		return errors.New("invalid shell session id")
	}
	return validateTerminalSize(r.Columns, r.Rows)
}

type ShellSessionAttachRequest struct {
	SessionID   string `json:"session_id"`
	AfterCursor uint64 `json:"after_cursor,omitempty"`
}

type ShellSessionAttachResponse struct {
	SessionID string `json:"session_id"`
	Cursor    uint64 `json:"cursor"`
	Output    string `json:"output,omitempty"`
	Exited    bool   `json:"exited,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

func (r ShellSessionAttachRequest) Validate() error {
	if !shellSessionIDPattern.MatchString(r.SessionID) {
		return errors.New("invalid shell session id")
	}
	return nil
}

type ShellSessionCloseRequest struct {
	SessionID string `json:"session_id"`
}

func (r ShellSessionCloseRequest) Validate() error {
	if !shellSessionIDPattern.MatchString(r.SessionID) {
		return errors.New("invalid shell session id")
	}
	return nil
}

type ShellSessionOutput struct {
	SessionID string `json:"session_id"`
	Cursor    uint64 `json:"cursor"`
	Data      string `json:"data"`
}

type ShellSessionExited struct {
	SessionID string `json:"session_id"`
	Cursor    uint64 `json:"cursor"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func validateTerminalSize(columns, rows uint16) error {
	if columns < 20 || columns > 500 || rows < 5 || rows > 500 {
		return errors.New("terminal size is outside supported range")
	}
	return nil
}

type Envelope struct {
	Version   string          `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	HomeID    string          `json:"home_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *ErrorPayload   `json:"error,omitempty"`
}

type ErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type AgentRegister struct {
	AgentID            string            `json:"agent_id"`
	HomeName           string            `json:"home_name,omitempty"`
	AgentType          string            `json:"agent_type,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Capabilities       []string          `json:"capabilities,omitempty"`
	UpdateAssignmentID string            `json:"update_assignment_id,omitempty"`
}

type AgentRegistered struct {
	AcceptedAt         time.Time `json:"accepted_at"`
	HomeID             string    `json:"home_id"`
	Message            string    `json:"message"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	UpdateAssignmentID string    `json:"update_assignment_id,omitempty"`
	UpdateAcknowledged bool      `json:"update_acknowledged,omitempty"`
}

const (
	TopicAgentUpdates         = "agents.updates"
	EventSystemUpdateState    = "system.update.state"
	UpdateStateDelayed        = "delayed"
	UpdateStateWaitingOnline  = "waiting_online"
	UpdateStateDownloading    = "downloading"
	UpdateStateInstalling     = "installing"
	UpdateStateReconnecting   = "reconnecting"
	UpdateStateHealthy        = "healthy"
	UpdateStateFailed         = "failed"
	UpdateStateRolledBack     = "rolled_back"
	UpdateStateRecoveryFailed = "recovery_failed"
)

var updateIDPattern = regexp.MustCompile(`^(lroll|luasg)_[A-Za-z0-9_-]{8,128}$`)
var semanticVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type SystemUpdateAssignment struct {
	RolloutID             string    `json:"rollout_id"`
	AssignmentID          string    `json:"assignment_id"`
	Version               string    `json:"version"`
	ManifestURL           string    `json:"manifest_url"`
	NotBefore             time.Time `json:"not_before"`
	HealthDeadlineSeconds int       `json:"health_deadline_seconds"`
}

func (value SystemUpdateAssignment) Validate() error {
	if !updateIDPattern.MatchString(value.RolloutID) || !strings.HasPrefix(value.RolloutID, "lroll_") || !updateIDPattern.MatchString(value.AssignmentID) || !strings.HasPrefix(value.AssignmentID, "luasg_") {
		return errors.New("invalid Linux update identity")
	}
	if !semanticVersionPattern.MatchString(value.Version) {
		return errors.New("invalid Linux update version")
	}
	parsed, err := url.Parse(value.ManifestURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("Linux update manifest must use HTTPS")
	}
	if value.NotBefore.IsZero() || value.HealthDeadlineSeconds < 60 || value.HealthDeadlineSeconds > 900 {
		return errors.New("invalid Linux update timing")
	}
	return nil
}

type SystemUpdateAccepted struct {
	AssignmentID string `json:"assignment_id"`
	Accepted     bool   `json:"accepted"`
}

type SystemUpdateState struct {
	RolloutID    string    `json:"rollout_id"`
	AssignmentID string    `json:"assignment_id"`
	AgentID      string    `json:"agent_id"`
	State        string    `json:"state"`
	ErrorCode    string    `json:"error_code,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (value SystemUpdateState) Validate(agentID string) error {
	if !updateIDPattern.MatchString(value.RolloutID) || !strings.HasPrefix(value.RolloutID, "lroll_") || !updateIDPattern.MatchString(value.AssignmentID) || !strings.HasPrefix(value.AssignmentID, "luasg_") || value.AgentID == "" || value.AgentID != agentID || value.OccurredAt.IsZero() {
		return errors.New("invalid Linux update event identity")
	}
	if !slices.Contains([]string{UpdateStateDelayed, UpdateStateWaitingOnline, UpdateStateDownloading, UpdateStateInstalling, UpdateStateReconnecting, UpdateStateHealthy, UpdateStateFailed, UpdateStateRolledBack, UpdateStateRecoveryFailed}, value.State) {
		return errors.New("invalid Linux update state")
	}
	return nil
}

type AgentHeartbeat struct {
	AgentID      string          `json:"agent_id"`
	SentAt       time.Time       `json:"sent_at"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Metrics      json.RawMessage `json:"metrics,omitempty"`
}

// HostMetrics is the conventional shape workers put in AgentHeartbeat.Metrics.
type HostMetrics struct {
	CPULoad1m        float64 `json:"cpu_load_1m,omitempty"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes int64   `json:"memory_total_bytes,omitempty"`
	DiskUsedBytes    int64   `json:"disk_used_bytes,omitempty"`
	DiskTotalBytes   int64   `json:"disk_total_bytes,omitempty"`
	UptimeSeconds    int64   `json:"uptime_seconds,omitempty"`
}

type RoutedCommand struct {
	Command string          `json:"command"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type AppSubscribeRequest struct {
	Topics []string `json:"topics"`
}

type AppSubscribeResponse struct {
	Topics []string `json:"topics"`
}

type AppEvent struct {
	Event string          `json:"event"`
	Topic string          `json:"topic,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

type AgentEvent struct {
	Event string          `json:"event"`
	Topic string          `json:"topic,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

type RoutedResponse struct {
	OK   bool            `json:"ok"`
	Body json.RawMessage `json:"body,omitempty"`
}

type SystemPingRequest struct {
	Message string `json:"message,omitempty"`
}

type SystemPingResponse struct {
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

type SystemRestartRequest struct {
	Reason string `json:"reason,omitempty"`
}

type SystemRestartResponse struct {
	OK        bool      `json:"ok"`
	Message   string    `json:"message"`
	RestartAt time.Time `json:"restart_at"`
}

func NewEnvelope(messageType string, requestID string, agentID string, homeID string, payload any) (Envelope, error) {
	envelope := Envelope{
		Version:   Version,
		Type:      messageType,
		RequestID: requestID,
		AgentID:   agentID,
		HomeID:    homeID,
		Timestamp: time.Now().UTC(),
	}

	if payload == nil {
		return envelope, nil
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}

	envelope.Payload = encoded
	return envelope, nil
}

func NewErrorEnvelope(messageType string, requestID string, agentID string, homeID string, code string, message string, details map[string]any) Envelope {
	return Envelope{
		Version:   Version,
		Type:      messageType,
		RequestID: requestID,
		AgentID:   agentID,
		HomeID:    homeID,
		Timestamp: time.Now().UTC(),
		Error: &ErrorPayload{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

func DecodePayload[T any](envelope Envelope) (T, error) {
	var payload T
	err := json.Unmarshal(envelope.Payload, &payload)
	return payload, err
}

func EncodeBody(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return encoded, nil
}
