package repo_checks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgres18MajorVersionPolicy(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	checker := filepath.Join(root, "scripts", "check-postgres-major.sh")
	tests := []struct {
		name        string
		versionNum  string
		wantSuccess bool
		wantOutput  string
	}{
		{name: "PostgreSQL 18 patch release", versionNum: "180004", wantSuccess: true, wantOutput: "PostgreSQL 18.4"},
		{name: "PostgreSQL 17 is too old", versionNum: "170009", wantOutput: "requires PostgreSQL major version 18"},
		{name: "PostgreSQL 19 needs review", versionNum: "190000", wantOutput: "requires PostgreSQL major version 18"},
		{name: "empty version", versionNum: "", wantOutput: "invalid PostgreSQL server_version_num"},
		{name: "malformed version", versionNum: "18.4", wantOutput: "invalid PostgreSQL server_version_num"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("bash", checker, tt.versionNum)
			output, err := cmd.CombinedOutput()
			if tt.wantSuccess && err != nil {
				t.Fatalf("check PostgreSQL version: %v\n%s", err, output)
			}
			if !tt.wantSuccess && err == nil {
				t.Fatalf("check PostgreSQL version unexpectedly succeeded:\n%s", output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("output = %q, want substring %q", output, tt.wantOutput)
			}
		})
	}
}

func TestPostgres18DeploymentUsesIsolatedRecoveryVolumes(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	postgresDockerfile := mustReadDeploymentFile(t, root, "Dockerfile.postgres")
	if got := strings.Count(postgresDockerfile, "FROM postgres:18-alpine"); got != 2 {
		t.Fatalf("Dockerfile.postgres PostgreSQL 18 stages = %d, want 2", got)
	}

	dbOpsDockerfile := mustReadDeploymentFile(t, root, "Dockerfile.dbops")
	if !strings.Contains(dbOpsDockerfile, "FROM postgres:18-alpine") {
		t.Fatal("Dockerfile.dbops must use PostgreSQL 18 client and pgBackRest tooling")
	}

	compose := mustReadDeploymentFile(t, root, "docker-compose.yml")
	for _, marker := range []string{
		"image: hank-postgres:pg18",
		"image: hank-db-ops:pg18",
		"PGDATA: /var/lib/postgresql/data",
		"- hank_pg18_postgres_data:/var/lib/postgresql\n",
		"hank_pg18_postgres_restore_data:/var/lib/postgresql/restore",
		"hank_pg18_pgbackrest_repo:/var/lib/pgbackrest",
	} {
		if !strings.Contains(compose, marker) {
			t.Fatalf("docker-compose.yml missing PostgreSQL 18 deployment contract %q", marker)
		}
	}
	if got := strings.Count(compose, "PGBACKREST_REPO1_CIPHER_PASS: ${HANK_DB_OPS_REPO_CIPHER_PASS}"); got != 3 {
		t.Fatalf("pgBackRest cipher propagation count = %d, want 3 (primary, db-ops, restore)", got)
	}
}

func mustReadDeploymentFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
