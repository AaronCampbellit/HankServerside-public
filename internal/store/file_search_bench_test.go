package store

import (
	"context"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func BenchmarkFileSearchCatalogLargeShare(b *testing.B) {
	ctx := context.Background()
	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(b))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_file_search_bench", Email: "file-search-bench@example.test", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_file_search_bench", UserID: user.ID, Name: "Search benchmark", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		b.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		b.Fatal(err)
	}
	if err := db.UpsertAgent(ctx, domain.Agent{ID: "agent_file_search_bench", HomeID: home.ID, Name: "Agent", Status: domain.AgentStatusOnline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}); err != nil {
		b.Fatal(err)
	}
	if err := db.SyncFileSearchSources(ctx, home.ID, "agent_file_search_bench", "share", []protocol.FileSourceInfo{{ID: "share", Name: "Large share", Enabled: true, Readable: true}}); err != nil {
		b.Fatal(err)
	}
	_, err = db.DB().ExecContext(ctx, `INSERT INTO file_search_items
		(home_id, agent_id, source_id, path, parent_path, name, is_directory)
		SELECT $1, $2, 'share', 'Archive/Folder/item-' || i || '.txt', 'Archive/Folder', 'item-' || i || '.txt', false
		FROM generate_series(1, 260000) AS i`, home.ID, "agent_file_search_bench")
	if err != nil {
		b.Fatal(err)
	}
	_, err = db.DB().ExecContext(ctx, `INSERT INTO file_search_items
		(home_id, agent_id, source_id, path, parent_path, name, is_directory)
		VALUES ($1, $2, 'share', 'Archive/Folder/needle-receipt.pdf', 'Archive/Folder', 'needle-receipt.pdf', false)`, home.ID, "agent_file_search_bench")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches, err := db.SearchFileCatalog(ctx, home.ID, []string{"agent_file_search_bench"}, "share", "needle receipt", false, 25)
		if err != nil || len(matches) != 1 || matches[0].Item.Name != "needle-receipt.pdf" {
			b.Fatalf("large search = %+v, %v", matches, err)
		}
	}
}
