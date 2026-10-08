package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeEnvFileWarningsDetectsGroupWorldReadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not portable on windows")
	}

	dir := t.TempDir()
	openPath := filepath.Join(dir, ".env.open")
	lockedPath := filepath.Join(dir, ".env.locked")
	if err := os.WriteFile(openPath, []byte("SECRET=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(openPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockedPath, []byte("SECRET=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedPath, 0o600); err != nil {
		t.Fatal(err)
	}

	warnings := RuntimeEnvFileWarnings(openPath, lockedPath)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one", warnings)
	}
	if warnings[0].Path != openPath {
		t.Fatalf("warning path = %q, want %q", warnings[0].Path, openPath)
	}
	if warnings[0].Mode != 0o644 {
		t.Fatalf("warning mode = %v, want 0644", warnings[0].Mode)
	}
}
