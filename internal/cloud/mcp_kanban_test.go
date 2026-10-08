package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func setupMCPKanbanService(t *testing.T) (context.Context, *mcpKanbanService, *cloudNotesService, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	db := storeForTest(t)
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC)
	userID := "usr_mcp_kanban_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_")
	must(t, db.CreateUser(ctx, domain.User{ID: userID, Email: userID + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}))
	notes := newCloudNotesService(db)
	service := newMCPKanbanService(db, notes, func() time.Time { return now })
	return ctx, service, notes, db, userID
}

func saveMCPKanbanBoard(t *testing.T, ctx context.Context, notes *cloudNotesService, userID, noteID, title string, excluded bool, board *protocol.KanbanBoard) protocol.NotesSaveResponse {
	t.Helper()
	response, err := notes.SaveProfile(ctx, userID, noteID, protocol.NotesSaveRequest{
		NoteID: noteID, Title: title, PageType: protocol.NotePageTypeKanban,
		MCPExcluded: &excluded, Board: board,
	})
	if err != nil {
		t.Fatalf("SaveProfile(%s): %v", noteID, err)
	}
	return response
}

func testMCPKanbanBoard() *protocol.KanbanBoard {
	return &protocol.KanbanBoard{
		IntakeColumnID: "planning",
		Columns: []protocol.KanbanColumn{
			{ID: "planning", Title: "Brainstorm", Role: "planning", SortOrder: 0, Cards: []protocol.KanbanCard{
				{ID: "research", Text: "Research offline sync\nCapture requirements", SortOrder: 0, Color: "cyan", DueDate: "2026-07-20", Tags: []string{"Hank", "Research"}},
			}},
			{ID: "active", Title: "Implementation", Role: "active", SortOrder: 1, Cards: []protocol.KanbanCard{
				{ID: "build", Text: "Build offline sync\nImplement queue", SortOrder: 0, DueDate: "2026-07-24", Tags: []string{"Hank"}},
			}},
			{ID: "done", Title: "Complete", Role: "complete", SortOrder: 2, Cards: []protocol.KanbanCard{
				{ID: "shipped", Text: "Ship notes", SortOrder: 0, Tags: []string{"Hank"}},
			}},
		},
	}
}

func TestMCPKanbanListBoardsHonorsVisibilityAndDefault(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	saveMCPKanbanBoard(t, ctx, notes, userID, "secret", "Secret", true, testMCPKanbanBoard())
	if _, err := notes.SaveProfile(ctx, userID, "plain", protocol.NotesSaveRequest{NoteID: "plain", Title: "Plain", Content: "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"dashboard":{"density":"compact"},"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}

	boards, err := service.ListBoards(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 || boards[0].BoardID != "work" || !boards[0].Default {
		t.Fatalf("boards = %#v", boards)
	}
	if boards[0].IntakeColumnID != "planning" || boards[0].TotalCardCount != 3 || boards[0].ActiveCardCount != 2 {
		t.Fatalf("board summary = %#v", boards[0])
	}
	if len(boards[0].Columns) != 3 || boards[0].Columns[1].Role != "active" {
		t.Fatalf("columns = %#v", boards[0].Columns)
	}
}

func TestMCPKanbanListCardsFiltersAndHidesCompleteByDefault(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}

	cards, err := service.ListCards(ctx, userID, mcpKanbanListCardsArgs{Query: "offline", Tags: []string{"hank"}, DueFrom: "2026-07-20", DueThrough: "2026-07-24"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].CardID != "research" || cards[1].CardID != "build" {
		t.Fatalf("filtered cards = %#v", cards)
	}

	completed, err := service.ListCards(ctx, userID, mcpKanbanListCardsArgs{Role: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 1 || completed[0].CardID != "shipped" {
		t.Fatalf("completed cards = %#v", completed)
	}
}

func TestMCPKanbanGetCardReturnsDetailsAndColumns(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}

	card, err := service.GetCard(ctx, userID, "", "research")
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "Research offline sync" || card.DetailsMarkdown != "Capture requirements" || card.ColumnRole != "planning" || len(card.Columns) != 3 {
		t.Fatalf("card = %#v", card)
	}
	if card.Color != "cyan" {
		t.Fatalf("color = %q", card.Color)
	}
}

func TestMCPKanbanGetCardIncludesReferencedAttachmentMetadata(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	board := testMCPKanbanBoard()
	board.Columns[0].Cards[0].Text = "Research offline sync\nSee [the brief](https://example.com/brief) and ![wireframe](hank-note-attachment://natt-wireframe)"
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, board)
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}
	note, err := db.GetProfileNote(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC)
	if err := db.CreateNoteAttachment(ctx, domain.NoteAttachment{
		ID: "natt-wireframe", NoteID: note.ID, OwnerUserID: userID,
		Filename: "wireframe.png", ContentType: "image/png", SizeBytes: 42,
		ChecksumSHA256: "checksum", StorageKey: "work/natt-wireframe.png", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateNoteAttachment(ctx, domain.NoteAttachment{
		ID: "natt-unrelated", NoteID: note.ID, OwnerUserID: userID,
		Filename: "unrelated.png", ContentType: "image/png", SizeBytes: 12,
		ChecksumSHA256: "checksum", StorageKey: "work/natt-unrelated.png", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	card, err := service.GetCard(ctx, userID, "", "research")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.DetailsMarkdown, "https://example.com/brief") {
		t.Fatalf("details did not preserve link: %q", card.DetailsMarkdown)
	}
	if len(card.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want only the referenced attachment", card.Attachments)
	}
	attachment := card.Attachments[0]
	if attachment.ID != "natt-wireframe" || attachment.Filename != "wireframe.png" || attachment.DownloadURL != "/v1/me/notes/work/attachments/natt-wireframe" {
		t.Fatalf("attachment = %#v", attachment)
	}
}

func TestMCPKanbanOpenBoardUsesDefaultThenFirstVisibleBoard(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	second := testMCPKanbanBoard()
	second.Columns[0].Cards[0].ID = "second-card"
	saveMCPKanbanBoard(t, ctx, notes, userID, "second", "Second", false, second)
	// The visible list is ordered by most recently updated, not insertion order.
	saveMCPKanbanBoard(t, ctx, notes, userID, "first", "First", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"second"}`)); err != nil {
		t.Fatal(err)
	}
	permissions := mcpKanbanPermissions{CanRead: true, CanWrite: true, CanDelete: false}

	snapshot, err := service.OpenBoard(ctx, userID, "", permissions)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SelectedBoard == nil || snapshot.SelectedBoard.BoardID != "second" || snapshot.StateVersion != snapshot.SelectedBoard.Revision {
		t.Fatalf("default snapshot = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.Permissions, permissions) || len(snapshot.SelectedBoard.Columns) != 3 || snapshot.SelectedBoard.Columns[0].Cards[0].CardID != "second-card" {
		t.Fatalf("default snapshot = %#v", snapshot)
	}

	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"stale"}`)); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.OpenBoard(ctx, userID, "", permissions)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Boards) == 0 || snapshot.SelectedBoard == nil || snapshot.SelectedBoard.BoardID != snapshot.Boards[0].BoardID {
		t.Fatalf("fallback snapshot = %#v", snapshot)
	}
}

func TestMCPKanbanOpenBoardReturnsEmptySnapshotAndHidesExcludedBoards(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	empty, err := service.OpenBoard(ctx, userID, "", mcpKanbanPermissions{CanRead: true})
	if err != nil || empty.SelectedBoard != nil || len(empty.Boards) != 0 {
		t.Fatalf("empty snapshot = %#v, err=%v", empty, err)
	}
	saveMCPKanbanBoard(t, ctx, notes, userID, "secret", "Secret", true, testMCPKanbanBoard())
	if _, err := service.OpenBoard(ctx, userID, "secret", mcpKanbanPermissions{CanRead: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("excluded board error = %v", err)
	}
}

func TestMCPKanbanReadValidationAndNoDefault(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())

	_, err := service.ListCards(ctx, userID, mcpKanbanListCardsArgs{})
	var noDefault *mcpKanbanNoDefaultError
	if !errors.As(err, &noDefault) || len(noDefault.Boards) != 1 {
		t.Fatalf("no-default error = %#v", err)
	}
	_, err = service.ListCards(ctx, userID, mcpKanbanListCardsArgs{BoardID: "work", Limit: 101})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit error = %v", err)
	}
	_, err = service.ListCards(ctx, userID, mcpKanbanListCardsArgs{BoardID: "work", DueFrom: "2026-02-30"})
	if err == nil || !strings.Contains(err.Error(), "due_from") {
		t.Fatalf("date error = %v", err)
	}
}

func TestMCPKanbanRejectsInvalidBoardMetadata(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	board := testMCPKanbanBoard()
	board.Columns[1].Role = "planning"
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, board)
	_, err := service.ListBoards(ctx, userID)
	if err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("duplicate role error = %v", err)
	}
}

func TestMCPKanbanCreateUsesDefaultIntakeAndNormalizesTags(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, userID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateCard(ctx, userID, mcpKanbanCreateArgs{
		Title: "  Design sync  ", DetailsMarkdown: "Capture requirements", DueDate: "2026-07-25",
		Tags: []string{" Hank ", "hank", "Design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.BoardID != "work" || created.ColumnID != "planning" || created.Title != "Design sync" || strings.Join(created.Tags, ",") != "Hank,Design" {
		t.Fatalf("created = %#v", created)
	}
	fetched, err := notes.FetchProfile(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	_, stored, ok := findKanbanCard(fetched.Board, created.CardID)
	if !ok || stored.Text != "Design sync\nCapture requirements" || stored.DueDate != "2026-07-25" {
		t.Fatalf("stored = %#v, found=%v", stored, ok)
	}
}

func TestMCPKanbanCreateFallsBackWhenIntakeColumnIsStale(t *testing.T) {
	legacy := testMCPKanbanBoard()
	legacy.IntakeColumnID = "deleted-column"
	if err := validateKanbanBoard(legacy); err != nil {
		t.Fatalf("stale intake should remain usable: %v", err)
	}

	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, legacy)

	created, err := service.CreateCard(ctx, userID, mcpKanbanCreateArgs{BoardID: "work", Title: "Fallback task"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ColumnID != "planning" {
		t.Fatalf("created column = %q", created.ColumnID)
	}
}

func TestMCPKanbanUpdatePatchesOnlySuppliedFields(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	title := "Research resilient sync"
	tags := []string{}

	updated, err := service.UpdateCard(ctx, userID, mcpKanbanUpdateArgs{BoardID: "work", CardID: "research", Title: &title, Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != title || updated.DetailsMarkdown != "Capture requirements" || updated.DueDate != "2026-07-20" || updated.Color != "cyan" || len(updated.Tags) != 0 {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.BoardRevision == "" {
		t.Fatal("missing updated revision")
	}
	stored, err := notes.FetchProfile(ctx, userID, "work")
	if err != nil || stored.Board.IntakeColumnID != "planning" {
		t.Fatalf("stored board = %#v, err=%v", stored.Board, err)
	}
}

func TestMCPKanbanAppendWorklogPreservesOriginalDetails(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())

	updated, err := service.AppendWorklog(ctx, userID, mcpKanbanWorklogArgs{
		BoardID: "work", CardID: "research", Kind: "verification", EntryMarkdown: "`go test ./...` passed.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Capture requirements\n\n## Work log\n\n### 2026-07-17T14:30:00Z — Verification\n\n`go test ./...` passed."
	if updated.DetailsMarkdown != want {
		t.Fatalf("details = %q, want %q", updated.DetailsMarkdown, want)
	}
}

func TestMCPKanbanMoveSupportsCrossColumnAndSameColumnReorder(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	board := testMCPKanbanBoard()
	board.Columns[1].Cards = append(board.Columns[1].Cards, protocol.KanbanCard{ID: "second", Text: "Second", SortOrder: 1})
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, board)

	index := 0
	moved, err := service.MoveCard(ctx, userID, mcpKanbanMoveArgs{BoardID: "work", CardID: "research", TargetColumnID: "active", TargetIndex: &index})
	if err != nil {
		t.Fatal(err)
	}
	if moved.ColumnID != "active" {
		t.Fatalf("moved = %#v", moved)
	}
	end := 2
	if _, err := service.MoveCard(ctx, userID, mcpKanbanMoveArgs{BoardID: "work", CardID: "research", TargetColumnID: "active", TargetIndex: &end}); err != nil {
		t.Fatal(err)
	}
	fetched, err := notes.FetchProfile(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	active := fetched.Board.Columns[1].Cards
	if len(active) != 3 || active[2].ID != "research" || active[2].SortOrder != 2 {
		t.Fatalf("active cards = %#v", active)
	}
}

func TestMCPKanbanDeleteCardRequiresConfirmationAndRevision(t *testing.T) {
	ctx, service, notes, db, userID := setupMCPKanbanService(t)
	saved := saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	note, err := db.GetProfileNote(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC)
	if err := db.CreateNoteAttachment(ctx, domain.NoteAttachment{ID: "natt-kept", NoteID: note.ID, OwnerUserID: userID, Filename: "kept.png", ContentType: "image/png", SizeBytes: 4, ChecksumSHA256: "sum", StorageKey: "work/kept.png", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.DeleteCard(ctx, userID, mcpKanbanDeleteArgs{BoardID: "work", CardID: "research", ExpectedBoardRevision: saved.Revision}); err == nil || !strings.Contains(err.Error(), "confirmed") {
		t.Fatalf("unconfirmed delete error = %v", err)
	}
	if _, err := service.DeleteCard(ctx, userID, mcpKanbanDeleteArgs{BoardID: "work", CardID: "research", ExpectedBoardRevision: "stale", Confirmed: true}); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale delete error = %v", err)
	}
	deleted, err := service.DeleteCard(ctx, userID, mcpKanbanDeleteArgs{BoardID: "work", CardID: "research", ExpectedBoardRevision: saved.Revision, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.CardID != "research" || deleted.BoardRevision == saved.Revision || deleted.StateVersion != deleted.BoardRevision {
		t.Fatalf("delete result = %#v", deleted)
	}
	fetched, err := notes.FetchProfile(ctx, userID, "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := findKanbanCard(fetched.Board, "research"); ok {
		t.Fatal("deleted card remains on board")
	}
	attachments, err := db.ListNoteAttachments(ctx, note.ID)
	if err != nil || len(attachments) != 1 || attachments[0].ID != "natt-kept" {
		t.Fatalf("attachments = %#v, err=%v", attachments, err)
	}
}

func TestMCPKanbanDeleteRetriesOnlyForUnchangedTarget(t *testing.T) {
	ctx, _, notes, db, userID := setupMCPKanbanService(t)
	saved := saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	conflicting := &conflictingMCPKanbanNotes{base: notes, userID: userID, mutateFirst: func(board *protocol.KanbanBoard) {
		board.Columns[1].Cards[0].Text = "Build offline sync\nHuman edit"
	}}
	service := newMCPKanbanService(db, conflicting, time.Now)
	if _, err := service.DeleteCard(ctx, userID, mcpKanbanDeleteArgs{BoardID: "work", CardID: "research", ExpectedBoardRevision: saved.Revision, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if conflicting.saveCalls != 2 {
		t.Fatalf("save calls = %d, want 2", conflicting.saveCalls)
	}

	saved = saveMCPKanbanBoard(t, ctx, notes, userID, "work-target", "Work target", false, testMCPKanbanBoard())
	targetConflict := &conflictingMCPKanbanNotes{base: notes, userID: userID, mutateFirst: func(board *protocol.KanbanBoard) {
		board.Columns[0].Cards[0].Text = "Human changed title\nCapture requirements"
	}}
	service = newMCPKanbanService(db, targetConflict, time.Now)
	_, err := service.DeleteCard(ctx, userID, mcpKanbanDeleteArgs{BoardID: "work-target", CardID: "research", ExpectedBoardRevision: saved.Revision, Confirmed: true})
	var conflict *mcpKanbanConflictError
	if !errors.As(err, &conflict) || conflict.Latest == nil || conflict.Latest.Title != "Human changed title" {
		t.Fatalf("target conflict = %#v, err=%v", conflict, err)
	}
	if targetConflict.saveCalls != 1 {
		t.Fatalf("target save calls = %d, want 1", targetConflict.saveCalls)
	}
}

func TestMCPKanbanMutationValidation(t *testing.T) {
	ctx, service, notes, _, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())

	if _, err := service.CreateCard(ctx, userID, mcpKanbanCreateArgs{BoardID: "work", Title: " ", DueDate: "2026-02-30"}); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("create error = %v", err)
	}
	badKind := mcpKanbanWorklogArgs{BoardID: "work", CardID: "research", Kind: "note", EntryMarkdown: "entry"}
	if _, err := service.AppendWorklog(ctx, userID, badKind); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("worklog error = %v", err)
	}
	badIndex := 99
	if _, err := service.MoveCard(ctx, userID, mcpKanbanMoveArgs{BoardID: "work", CardID: "research", TargetColumnID: "active", TargetIndex: &badIndex}); err == nil || !strings.Contains(err.Error(), "target_index") {
		t.Fatalf("move error = %v", err)
	}
}

type conflictingMCPKanbanNotes struct {
	base        *cloudNotesService
	userID      string
	mutateFirst func(*protocol.KanbanBoard)
	saveCalls   int
}

func (n *conflictingMCPKanbanNotes) FetchProfile(ctx context.Context, userID string, noteID string) (protocol.NotesFetchResponse, error) {
	return n.base.FetchProfile(ctx, userID, noteID)
}

func (n *conflictingMCPKanbanNotes) SaveProfile(ctx context.Context, userID string, noteID string, request protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error) {
	n.saveCalls++
	if n.saveCalls == 1 && n.mutateFirst != nil {
		current, err := n.base.FetchProfile(ctx, n.userID, noteID)
		if err != nil {
			return protocol.NotesSaveResponse{}, err
		}
		n.mutateFirst(current.Board)
		parentID, sortOrder, excluded := current.ParentID, current.SortOrder, current.MCPExcluded
		if _, err := n.base.SaveProfile(ctx, n.userID, noteID, protocol.NotesSaveRequest{
			NoteID: noteID, Title: current.Title, PageType: protocol.NotePageTypeKanban,
			ExpectedRevision: current.Revision, ParentID: &parentID, SortOrder: &sortOrder, MCPExcluded: &excluded,
			Board: current.Board,
		}); err != nil {
			return protocol.NotesSaveResponse{}, err
		}
	}
	return n.base.SaveProfile(ctx, userID, noteID, request)
}

func TestMCPKanbanConflictRetriesUnrelatedCardChange(t *testing.T) {
	ctx, _, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	conflicting := &conflictingMCPKanbanNotes{base: notes, userID: userID, mutateFirst: func(board *protocol.KanbanBoard) {
		board.Columns[1].Cards[0].Text = "Build offline sync\nHuman edit"
	}}
	service := newMCPKanbanService(db, conflicting, func() time.Time { return time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC) })
	title := "Research resilient sync"

	updated, err := service.UpdateCard(ctx, userID, mcpKanbanUpdateArgs{BoardID: "work", CardID: "research", Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if conflicting.saveCalls != 2 || updated.Title != title {
		t.Fatalf("saveCalls=%d updated=%#v", conflicting.saveCalls, updated)
	}
	fetched, err := notes.FetchProfile(ctx, userID, "work")
	if err != nil || fetched.Board.Columns[1].Cards[0].Text != "Build offline sync\nHuman edit" {
		t.Fatalf("unrelated edit lost: board=%#v err=%v", fetched.Board, err)
	}
}

func TestMCPKanbanConflictRejectsTargetedCardChange(t *testing.T) {
	ctx, _, notes, db, userID := setupMCPKanbanService(t)
	saveMCPKanbanBoard(t, ctx, notes, userID, "work", "Work", false, testMCPKanbanBoard())
	conflicting := &conflictingMCPKanbanNotes{base: notes, userID: userID, mutateFirst: func(board *protocol.KanbanBoard) {
		board.Columns[0].Cards[0].Text = "Human changed title\nCapture requirements"
	}}
	service := newMCPKanbanService(db, conflicting, time.Now)
	title := "Assistant title"

	_, err := service.UpdateCard(ctx, userID, mcpKanbanUpdateArgs{BoardID: "work", CardID: "research", Title: &title})
	var conflict *mcpKanbanConflictError
	if !errors.As(err, &conflict) || conflict.Latest == nil || conflict.Latest.Title != "Human changed title" {
		t.Fatalf("conflict = %#v, err=%v", conflict, err)
	}
	if conflicting.saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", conflicting.saveCalls)
	}
}
