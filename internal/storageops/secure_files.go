package storageops

import "os"

const (
	privateDirMode      os.FileMode = 0o770
	privateDirChmodMode os.FileMode = os.ModeSetgid | privateDirMode
	privateFileMode     os.FileMode = 0o660
)

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, privateDirMode); err != nil {
		return err
	}
	return chmodIfNeeded(path, privateDirChmodMode)
}

func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, privateFileMode); err != nil {
		return err
	}
	return chmodIfNeeded(path, privateFileMode)
}

func renamePrivateFile(tmp string, path string) error {
	if err := chmodIfNeeded(tmp, privateFileMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return chmodIfNeeded(path, privateFileMode)
}

func chmodIfNeeded(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == mode.Perm() && info.Mode()&os.ModeSetgid == mode&os.ModeSetgid {
		return nil
	}
	return os.Chmod(path, mode)
}
