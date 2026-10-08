package protocol

import (
	"errors"
	"math"
	"strings"
	"time"
)

const MaxShellCommandBytes = 64 * 1024
const MaxShellTimeoutSeconds = 300

// ShellExecRequest is the bounded one-shot shell contract. Zero selects 60 seconds.
type ShellExecRequest struct {
	Command        string  `json:"command"`
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
}

func (r ShellExecRequest) Validate() error {
	if strings.TrimSpace(r.Command) == "" || len(r.Command) > MaxShellCommandBytes || strings.ContainsRune(r.Command, 0) {
		return errors.New("command must contain 1 to 65536 bytes and no NUL")
	}
	if math.IsNaN(r.TimeoutSeconds) || math.IsInf(r.TimeoutSeconds, 0) || r.TimeoutSeconds < 0 || r.TimeoutSeconds > MaxShellTimeoutSeconds || (r.TimeoutSeconds > 0 && r.TimeoutSeconds < 0.001) {
		return errors.New("timeout_seconds must be zero or between 0.001 and 300")
	}
	return nil
}

func (r ShellExecRequest) Timeout() time.Duration {
	if r.TimeoutSeconds == 0 {
		return 60 * time.Second
	}
	return time.Duration(r.TimeoutSeconds * float64(time.Second))
}
