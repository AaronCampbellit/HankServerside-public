package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestMCPContextSourceLifecycleAndOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC()
	owner := domain.User{ID: "usr_ctx_owner", Email: "ctx-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	other := domain.User{ID: "usr_ctx_other", Email: "ctx-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	home := domain.Home{ID: "home_ctx", UserID: owner.ID, Name: "Context Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	source := domain.MCPContextSource{ID: "mcpcs_1", OwnerUserID: owner.ID, HomeID: home.ID, Name: "MiniHank", FileSourceID: "projects", RootPath: "Projects/MiniHank", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateMCPContextSource(ctx, source); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.GetMCPContextSourceForUser(ctx, source.ID, owner.ID)
	if err != nil || got.Name != source.Name || got.FileSourceID != source.FileSourceID || !got.Enabled {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if _, err := db.GetMCPContextSourceForUser(ctx, source.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get = %v", err)
	}
	got.Name, got.RootPath, got.Enabled = "MiniHank App", "Projects/MiniHank/App", false
	if err := db.UpdateMCPContextSourceForUser(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	items, err := db.ListMCPContextSourcesByUser(ctx, owner.ID, false)
	if err != nil || len(items) != 1 || items[0].Enabled || items[0].RootPath != got.RootPath {
		t.Fatalf("list = %#v, %v", items, err)
	}
	if err := db.DeleteMCPContextSourceForUser(ctx, source.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete = %v", err)
	}
	if err := db.DeleteMCPContextSourceForUser(ctx, source.ID, owner.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetMCPContextSourceForUser(ctx, source.ID, owner.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}
