//go:build linux

package apps

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const defaultAppRuntimeDir = "/var/lib/hank/app-runtime/v1"

func sandboxCommand(ctx context.Context, spec InvokeSpec) (*exec.Cmd, func(), error) {
	noop := func() {}
	helper, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, noop, ErrSandboxUnavailable
	}
	runtimeRoot := spec.RuntimeDir
	if runtimeRoot == "" {
		runtimeRoot = defaultAppRuntimeDir
	}
	runtimeRoot, err = filepath.Abs(runtimeRoot)
	if err != nil {
		return nil, noop, ErrSandboxUnavailable
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil || resolved != runtimeRoot {
		return nil, noop, ErrSandboxUnavailable
	}
	// A dedicated, operator-built runtime contains no host configuration or credentials.
	marker, err := os.ReadFile(filepath.Join(runtimeRoot, "hank-runtime-version"))
	if err != nil || string(marker) != "1\n" {
		return nil, noop, ErrSandboxUnavailable
	}
	executable, err := sandboxExecutable(spec)
	if err != nil {
		return nil, noop, err
	}
	staging, err := os.MkdirTemp("", "hank-app-sandbox-")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	hash, err := snapshotPackage(ctx, spec.WorkDir, staging)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	if spec.PackageHash != "" && hash != spec.PackageHash {
		cleanup()
		return nil, noop, ErrPermissionRefused
	}
	filter, err := appSeccompFile()
	if err != nil {
		cleanup()
		return nil, noop, ErrSandboxUnavailable
	}
	old := cleanup
	cleanup = func() { filter.Close(); old() }
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	parent := os.NewFile(uintptr(pair[0]), "app-broker-parent")
	child := os.NewFile(uintptr(pair[1]), "app-broker-child")
	brokerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); serveAppBroker(brokerCtx, parent, spec.Broker) }()
	oldCleanup := cleanup
	cleanup = func() { cancel(); _ = parent.Close(); _ = child.Close(); <-done; oldCleanup() }
	args := []string{"--unshare-all", "--unshare-user", "--uid", "65534", "--gid", "65534", "--seccomp", "4", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv", "--ro-bind", runtimeRoot, "/", "--ro-bind", staging, "/app", "--size", "67108864", "--tmpfs", "/tmp", "--size", "67108864", "--tmpfs", "/home", "--size", "8388608", "--tmpfs", "/run", "--dev", "/dev", "--chdir", "/app", "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "HOME", "/home", "--setenv", "TMPDIR", "/tmp", "--setenv", "LANG", "C.UTF-8", "--setenv", "HANK_APP_BROKER_FD", "3", "--", executable}
	args = append(args, spec.Args...)
	cmd := exec.CommandContext(ctx, helper, args...)
	group, release, err := newAppCgroup(spec.CgroupRoot)
	if err != nil {
		cleanup()
		return nil, noop, ErrSandboxUnavailable
	}
	previousCleanup := cleanup
	cleanup = func() { release(); previousCleanup() }
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd())}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{child, filter}
	// Do not expose host /proc, even read-only. The private PID namespace and
	// bubblewrap init own descendant lifetime, including setsid/double-fork children.
	if ctx.Err() != nil {
		cleanup()
		return nil, noop, errors.Join(ErrSandboxUnavailable, ctx.Err())
	}
	return cmd, cleanup, nil
}
