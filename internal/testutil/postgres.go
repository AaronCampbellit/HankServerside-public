package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var testSchemaCounter uint64

func PostgreSQLTestURL(t testing.TB) string {
	t.Helper()

	baseURL := strings.TrimSpace(os.Getenv("HANK_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set HANK_TEST_DATABASE_URL to run PostgreSQL-backed tests")
	}

	adminDB, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	databaseName := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&testSchemaCounter, 1))
	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create database %s: %v", databaseName, err)
	}

	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, `SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName)
		_, _ = adminDB.ExecContext(dropCtx, `DROP DATABASE IF EXISTS "`+databaseName+`"`)
		_ = adminDB.Close()
	})

	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	parsed.Path = path.Join("/", databaseName)
	return parsed.String()
}
