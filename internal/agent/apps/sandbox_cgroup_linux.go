//go:build linux

package apps

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const defaultAppCgroupRoot = "/sys/fs/cgroup/hank-apps"

// The operator delegates an empty cgroup v2 subtree with memory, pids and cpu
// controllers enabled. Never fall back to launching outside resource limits.
func newAppCgroup(root string) (*os.File, func(), error) {
	noop := func() {}
	if root == "" {
		root = defaultAppCgroupRoot
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, noop, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, noop, ErrSandboxUnavailable
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, noop, ErrSandboxUnavailable
	}
	dir, err := os.MkdirTemp(root, "invocation-")
	if err != nil {
		return nil, noop, err
	}
	var group *os.File
	cleanup := func() {
		// Kill only this newly created invocation and its descendants. cgroup.kill
		// also reaches processes that created a new session or process group.
		_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0600)
		if group != nil {
			_ = group.Close()
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			err := os.Remove(dir)
			if err == nil || errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for name, value := range map[string]string{
		"memory.max": "536870912", "memory.swap.max": "0",
		"memory.oom.group": "1", "pids.max": "64", "cpu.max": "100000 100000",
	} {
		// O_WRONLY without O_CREATE makes absent/undelegated controllers fatal.
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY, 0)
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		_, err = file.WriteString(value)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			cleanup()
			return nil, noop, errors.Join(err, closeErr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); err != nil {
		cleanup()
		return nil, noop, err
	}
	group, err = os.Open(dir)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return group, cleanup, nil
}
