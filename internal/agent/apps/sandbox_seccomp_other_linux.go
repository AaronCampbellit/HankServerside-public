//go:build linux && !amd64 && !arm64

package apps

import "os"

func appSeccompFile() (*os.File, error) { return nil, ErrSandboxUnavailable }
