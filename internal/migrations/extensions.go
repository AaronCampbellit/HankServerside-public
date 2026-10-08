package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

var RequiredExtensions = []string{
	"vector",
	"pg_trgm",
	"pg_stat_statements",
	"pg_buffercache",
	"amcheck",
}

var RequiredPreloadLibraries = []string{
	"pg_stat_statements",
}

type ExtensionStatus struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Schema    string `json:"schema,omitempty"`
}

type ExtensionHealth struct {
	OK               bool              `json:"ok"`
	PreloadLibraries map[string]bool   `json:"preload_libraries"`
	Extensions       []ExtensionStatus `json:"extensions"`
}

func RequiredExtensionHealth(ctx context.Context, db *sql.DB) (ExtensionHealth, error) {
	health := ExtensionHealth{OK: true, PreloadLibraries: map[string]bool{}}
	var rawPreload string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('shared_preload_libraries', true)`).Scan(&rawPreload); err != nil {
		return health, fmt.Errorf("check shared_preload_libraries: %w", err)
	}
	loaded := preloadLibrarySet(rawPreload)
	for _, library := range RequiredPreloadLibraries {
		_, ok := loaded[library]
		health.PreloadLibraries[library] = ok
		if !ok {
			health.OK = false
		}
	}
	for _, extension := range RequiredExtensions {
		status := ExtensionStatus{Name: extension, Required: true}
		if err := db.QueryRowContext(ctx, `SELECT
				EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = $1),
				COALESCE((SELECT extversion FROM pg_extension WHERE extname = $1), ''),
				COALESCE((SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1), '')`,
			extension).Scan(&status.Available, &status.Version, &status.Schema); err != nil {
			return health, fmt.Errorf("check required Postgres extension %s: %w", extension, err)
		}
		status.Installed = status.Version != ""
		if !status.Available || !status.Installed {
			health.OK = false
		}
		health.Extensions = append(health.Extensions, status)
	}
	return health, nil
}

func CheckRequiredExtensions(ctx context.Context, db *sql.DB) error {
	if err := checkRequiredPreloadLibraries(ctx, db); err != nil {
		return err
	}
	for _, extension := range RequiredExtensions {
		var installed bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, extension).Scan(&installed); err != nil {
			return fmt.Errorf("check required Postgres extension %s: %w", extension, err)
		}
		if !installed {
			return fmt.Errorf("required Postgres extension %s is not installed", extension)
		}
	}
	return nil
}

func checkRequiredPreloadLibraries(ctx context.Context, db *sql.DB) error {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('shared_preload_libraries', true)`).Scan(&raw); err != nil {
		return fmt.Errorf("check shared_preload_libraries: %w", err)
	}
	loaded := preloadLibrarySet(raw)
	for _, library := range RequiredPreloadLibraries {
		if _, ok := loaded[library]; !ok {
			return fmt.Errorf("required Postgres preload library %s is not configured in shared_preload_libraries", library)
		}
	}
	return nil
}

func preloadLibrarySet(raw string) map[string]struct{} {
	loaded := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			loaded[item] = struct{}{}
		}
	}
	return loaded
}
