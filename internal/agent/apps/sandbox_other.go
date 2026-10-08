//go:build !linux

package apps

import (
	"context"
	"os/exec"
)

func sandboxCommand(context.Context, InvokeSpec) (*exec.Cmd, func(), error) {
	return nil, func() {}, ErrSandboxUnavailable
}
