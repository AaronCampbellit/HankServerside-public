package apps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrSandboxUnavailable = errors.New("app sandbox unavailable")

// snapshotPackage exposes only immutable package bytes. Agent-owned state is
// deliberately excluded, including when the package was installed by an older agent.
func snapshotPackage(ctx context.Context, root, destination string) (string, error) {
	packageRoot, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer packageRoot.Close()
	hash := sha256.New()
	var total int64
	var count int
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == appStateFilename {
			return nil
		}
		if relative == "." {
			return nil
		}
		count++
		if count > 10000 {
			return errors.New("app package has too many entries")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("app package contains a link or special file")
		}
		fmt.Fprintf(hash, "%d:%s:%o:%d\n", len(relative), relative, info.Mode().Perm(), info.Size())
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			if destination != "" {
				return os.Mkdir(target, 0700)
			}
			return nil
		}
		total += info.Size()
		if total > maxPackageBytes {
			return errors.New("app package exceeds sandbox size limit")
		}
		input, err := packageRoot.Open(relative)
		if err != nil {
			return err
		}
		defer input.Close()
		var output *os.File
		var writer io.Writer = hash
		if destination != "" {
			output, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()&0755)
			if err != nil {
				return err
			}
			defer output.Close()
			writer = io.MultiWriter(hash, output)
		}
		n, err := io.Copy(writer, io.LimitReader(input, info.Size()+1))
		if err != nil {
			return err
		}
		if n != info.Size() {
			return errors.New("app package changed while copying")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sandboxExecutable(spec InvokeSpec) (string, error) {
	relative, err := filepath.Rel(spec.WorkDir, spec.Executable)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrPermissionRefused
	}
	return "/app/" + filepath.ToSlash(relative), nil
}
