//go:build unix

package storageops

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func acquireRestoreLock(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0770); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(stateDir, ".dataset-operation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another backup or restore operation is active")
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }, nil
}

func attachmentGroup(root string) (int, error) {
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		return 0, err
	}
	return int(stat.Gid), nil
}
