package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestDatabasePoolBoundsConcurrencyAndCancelsWaiters(t *testing.T) {
	db, err := openDatabasePool(testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held := make([]*sql.Conn, 0, maxDatabaseConnections)
	defer func() {
		for _, conn := range held {
			conn.Close()
		}
	}()
	for range maxDatabaseConnections {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if conn, err := db.Conn(waiting); !errors.Is(err, context.DeadlineExceeded) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("saturated pool result = %v, want deadline exceeded", err)
	}
	stats := db.Stats()
	if stats.OpenConnections != maxDatabaseConnections || stats.MaxOpenConnections != maxDatabaseConnections || stats.WaitCount == 0 {
		t.Fatalf("pool escaped limit or did not queue: %+v", stats)
	}
	held[0].Close()
	held = held[1:]
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("pool did not recover after release: %v", err)
	}
}
