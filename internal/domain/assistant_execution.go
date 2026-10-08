package domain

import (
	"encoding/json"
	"time"
)

type AssistantTask struct {
	ID              string          `json:"id"`
	HomeID          string          `json:"home_id"`
	UserID          string          `json:"user_id"`
	SessionID       string          `json:"session_id"`
	SubmissionKey   string          `json:"submission_key"`
	RequestText     string          `json:"request_text"`
	State           string          `json:"state"`
	Revision        int64           `json:"revision"`
	Checkpoint      json.RawMessage `json:"checkpoint"`
	MaxTurns        int             `json:"max_turns"`
	MaxCalls        int             `json:"max_calls"`
	MaxTokens       int64           `json:"max_tokens"`
	Turns           int             `json:"turns"`
	Calls           int             `json:"calls"`
	Tokens          int64           `json:"tokens"`
	ActiveMS        int64           `json:"active_ms"`
	MaxActiveMS     int64           `json:"max_active_ms"`
	CancelRequested bool            `json:"cancel_requested"`
	LeaseOwner      string          `json:"-"`
	LeaseUntil      *time.Time      `json:"-"`
	Fence           int64           `json:"-"`
	EventSequence   int64           `json:"event_sequence"`
	RetryAt         *time.Time      `json:"retry_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type AssistantTaskStep struct {
	TaskID       string          `json:"task_id"`
	CallID       string          `json:"call_id"`
	Sequence     int64           `json:"sequence"`
	Tool         string          `json:"tool"`
	ToolVersion  int             `json:"tool_version"`
	Arguments    json.RawMessage `json:"arguments"`
	ActionDigest string          `json:"action_digest"`
	State        string          `json:"state"`
	Attempts     int             `json:"attempts"`
	Result       json.RawMessage `json:"result,omitempty"`
}

type AssistantTaskApproval struct {
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	CallID       string     `json:"call_id"`
	ActionDigest string     `json:"action_digest"`
	State        string     `json:"state"`
	ExpiresAt    time.Time  `json:"expires_at"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
}

type AssistantTaskEvent struct {
	TaskID    string    `json:"task_id"`
	Sequence  int64     `json:"sequence"`
	Type      string    `json:"type"`
	CallID    string    `json:"call_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type AssistantOperationReceipt struct {
	OperationID  string          `json:"operation_id"`
	HomeID       string          `json:"home_id"`
	UserID       string          `json:"user_id"`
	TaskID       string          `json:"task_id"`
	CallID       string          `json:"call_id"`
	Tool         string          `json:"tool"`
	ActionDigest string          `json:"action_digest"`
	Outcome      string          `json:"outcome"`
	Result       json.RawMessage `json:"result,omitempty"`
}

type AssistantTaskInput struct {
	ID     int64  `json:"id"`
	TaskID string `json:"task_id"`
	Key    string `json:"key"`
	Text   string `json:"text"`
}

// AssistantStage is an owner-bound immutable server attachment handle.
type AssistantStage struct {
	ID                 string    `json:"id"`
	HomeID             string    `json:"-"`
	UserID             string    `json:"-"`
	SessionID          string    `json:"session_id"`
	ClientAttachmentID string    `json:"client_attachment_id"`
	StorageKey         string    `json:"-"`
	Filename           string    `json:"filename"`
	ContentType        string    `json:"content_type"`
	SizeBytes          int64     `json:"size_bytes"`
	ChecksumSHA256     string    `json:"checksum_sha256"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}
