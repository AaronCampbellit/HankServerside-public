//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package apps

import "os/exec"

// CommandContext terminates the direct process. The runner bounds pipe
// cleanup; descendant containment requires the platform sandbox policy.
func configureInvocationProcess(cmd *exec.Cmd) {}
