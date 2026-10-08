package store

import (
	"context"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestFileSearchCatalogIndexesAndReconcilesDirectories(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_file_search", Email: "file-search@example.test", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_file_search", UserID: user.ID, Name: "Search", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent_search_1", "agent_search_2"} {
		if err := db.UpsertAgent(ctx, domain.Agent{ID: id, HomeID: home.ID, Name: id, Status: domain.AgentStatusOnline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := db.SyncFileSearchSources(ctx, home.ID, id, "share", []protocol.FileSourceInfo{{ID: "share", Name: id, Enabled: true, Readable: true}}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := db.NextFileSearchDirectory(ctx, home.ID, "agent_search_1")
	if err != nil || root.Path != "" || root.SourceID != "share" {
		t.Fatalf("root = %+v, %v", root, err)
	}
	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_1", root, []protocol.FileItem{
		{SourceID: "share", Path: "Taxes", Name: "Taxes", IsDirectory: true},
		{SourceID: "share", Path: "roof-plan.pdf", Name: "roof-plan.pdf"},
	}); err != nil {
		t.Fatal(err)
	}
	child, err := db.NextFileSearchDirectory(ctx, home.ID, "agent_search_1")
	if err != nil || child.Path != "Taxes" {
		t.Fatalf("child = %+v, %v", child, err)
	}
	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_1", child, []protocol.FileItem{{SourceID: "share", Path: "Taxes/roof-receipt.pdf", Name: "roof-receipt.pdf"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_2", FileSearchDirectory{SourceID: "share"}, []protocol.FileItem{{SourceID: "share", Path: "roof-other.pdf", Name: "roof-other.pdf"}}); err != nil {
		t.Fatal(err)
	}

	matches, err := db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1"}, "share", "roof", false, 20)
	if err != nil || len(matches) != 2 || matches[0].AgentID != "agent_search_1" {
		t.Fatalf("scoped matches = %+v, %v", matches, err)
	}
	all, err := db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1", "agent_search_2"}, "", "roof", false, 20)
	if err != nil || len(all) != 3 {
		t.Fatalf("all matches = %+v, %v", all, err)
	}
	phrase, err := db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1"}, "share", "taxes receipt", false, 20)
	if err != nil || len(phrase) != 1 || phrase[0].Item.Path != "Taxes/roof-receipt.pdf" {
		t.Fatalf("multi-word match = %+v, %v", phrase, err)
	}
	statuses, err := db.FileSearchStatuses(ctx, home.ID, []string{"agent_search_1"}, "share", false)
	if err != nil || len(statuses) != 1 || statuses[0].Pending != 0 || statuses[0].Indexed != 3 {
		t.Fatalf("status = %+v, %v", statuses, err)
	}
	if err := db.DeferFileSearchDirectory(ctx, home.ID, "agent_search_1", root); err != nil {
		t.Fatal(err)
	}
	statuses, err = db.FileSearchStatuses(ctx, home.ID, []string{"agent_search_1"}, "share", false)
	if err != nil || len(statuses) != 1 || !statuses[0].Error || statuses[0].Indexed != 3 {
		t.Fatalf("failed scan must retain catalog: %+v, %v", statuses, err)
	}

	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_1", root, []protocol.FileItem{{SourceID: "share", Path: "roof-plan.pdf", Name: "roof-plan.pdf"}}); err != nil {
		t.Fatal(err)
	}
	statuses, err = db.FileSearchStatuses(ctx, home.ID, []string{"agent_search_1"}, "share", false)
	if err != nil || len(statuses) != 1 || statuses[0].Error {
		t.Fatalf("successful scan did not clear failure: %+v, %v", statuses, err)
	}
	matches, err = db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1"}, "share", "receipt", false, 20)
	if err != nil || len(matches) != 0 {
		t.Fatalf("removed directory descendants = %+v, %v", matches, err)
	}
	if _, err := db.NextFileSearchDirectory(ctx, home.ID, "agent_search_1"); !IsNoFileSearchWork(err) {
		t.Fatalf("unexpected pending directory: %v", err)
	}
	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_1", root, []protocol.FileItem{{SourceID: "share", Path: "../escape", Name: "escape"}}); err == nil {
		t.Fatal("accepted escaping path")
	}
	if err := db.MarkFileSearchDirectoryDue(ctx, home.ID, "agent_search_1", "share", ""); err != nil {
		t.Fatal(err)
	}
	if next, err := db.NextFileSearchDirectory(ctx, home.ID, "agent_search_1"); err != nil || next.Path != "" {
		t.Fatalf("scheduled root = %+v, %v", next, err)
	}
	if err := db.SyncFileSearchSources(ctx, home.ID, "agent_search_1", "share", []protocol.FileSourceInfo{{ID: "share", Enabled: true, Readable: true, AllowedPrefixes: []string{"Allowed"}}}); err != nil {
		t.Fatal(err)
	}
	if next, err := db.NextFileSearchDirectory(ctx, home.ID, "agent_search_1"); err != nil || next.Path != "Allowed" {
		t.Fatalf("allowed root = %+v, %v", next, err)
	}
	matches, err = db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1"}, "share", "roof", false, 20)
	if err != nil || len(matches) != 0 {
		t.Fatalf("old policy results survived = %+v, %v", matches, err)
	}
	if err := db.SaveFileSearchDirectory(ctx, home.ID, "agent_search_1", FileSearchDirectory{SourceID: "share", Path: "Allowed", Depth: 1},
		[]protocol.FileItem{{SourceID: "share", Path: "Allowed/fixture.txt", Name: "fixture.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncFileSearchSources(ctx, home.ID, "agent_search_1", "share", []protocol.FileSourceInfo{{
		ID: "share", Enabled: true, Readable: true, AllowedPrefixes: []string{"Allowed"}, Revision: "new-root",
	}}); err != nil {
		t.Fatal(err)
	}
	matches, err = db.SearchFileCatalog(ctx, home.ID, []string{"agent_search_1"}, "share", "fixture", false, 20)
	if err != nil || len(matches) != 0 {
		t.Fatalf("old source revision results survived = %+v, %v", matches, err)
	}
}
