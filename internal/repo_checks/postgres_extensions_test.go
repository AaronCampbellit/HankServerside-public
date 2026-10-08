package repo_checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresImageBuildsRequiredExtensions(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Dockerfile.postgres"))
	if err != nil {
		t.Fatalf("read Dockerfile.postgres: %v", err)
	}
	body := string(data)
	marker := "PGVECTOR_VERSION=v0.8.2"
	if !strings.Contains(body, marker) {
		t.Fatalf("Dockerfile.postgres missing pinned extension marker %q", marker)
	}
	for _, marker := range []string{"PG_CRON_VERSION", "PGMQ_VERSION", "PG_PARTMAN_VERSION", "PGAUDIT_VERSION"} {
		if strings.Contains(body, marker) {
			t.Fatalf("Dockerfile.postgres should not build deferred extension marker %q", marker)
		}
	}
}

func TestComposePreloadsRequiredPostgresLibraries(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "shared_preload_libraries=pg_stat_statements") {
		t.Fatal("postgres compose config must preload pg_stat_statements")
	}
	for _, setting := range []string{
		"pg_stat_statements.track=all",
	} {
		if !strings.Contains(body, setting) {
			t.Fatalf("docker-compose.yml missing postgres setting %q", setting)
		}
	}
	for _, setting := range []string{
		"pg_cron",
		"pgaudit.log",
		"cron.database_name",
	} {
		if strings.Contains(body, setting) {
			t.Fatalf("docker-compose.yml should not configure deferred setting %q", setting)
		}
	}
}
