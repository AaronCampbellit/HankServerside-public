package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type EnvFilePermissionWarning struct {
	Path string
	Mode os.FileMode
}

func RuntimeEnvFileWarnings(paths ...string) []EnvFilePermissionWarning {
	if runtime.GOOS == "windows" {
		return nil
	}
	seen := map[string]struct{}{}
	warnings := make([]EnvFilePermissionWarning, 0)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		info, err := os.Stat(cleaned)
		if err != nil || info.IsDir() {
			continue
		}
		mode := info.Mode().Perm()
		if mode&0o077 != 0 {
			warnings = append(warnings, EnvFilePermissionWarning{Path: cleaned, Mode: mode})
		}
	}
	return warnings
}
