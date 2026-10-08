package protocol

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestShellExecBounds(t *testing.T) {
	for _, request := range []ShellExecRequest{{}, {Command: " "}, {Command: "a\x00b"}, {Command: strings.Repeat("x", MaxShellCommandBytes+1)}, {Command: "true", TimeoutSeconds: -1}, {Command: "true", TimeoutSeconds: 301}, {Command: "true", TimeoutSeconds: math.Inf(1)}, {Command: "true", TimeoutSeconds: math.NaN()}, {Command: "true", TimeoutSeconds: 0.00001}} {
		if request.Validate() == nil {
			t.Fatalf("accepted invalid request: length=%d timeout=%v", len(request.Command), request.TimeoutSeconds)
		}
	}
	request := ShellExecRequest{Command: "true"}
	if request.Validate() != nil || request.Timeout() != time.Minute {
		t.Fatal("default timeout")
	}
	request.TimeoutSeconds = 0.5
	if request.Validate() != nil || request.Timeout() != 500*time.Millisecond {
		t.Fatal("fractional timeout")
	}
}
