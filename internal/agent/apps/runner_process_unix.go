//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package apps

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureInvocationProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
