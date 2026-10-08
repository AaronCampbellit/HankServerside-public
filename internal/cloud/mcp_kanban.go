package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	kanbanDefaultListLimit = 50
	kanbanMaximumListLimit = 100
	kanbanMaximumTags      = 20
	kanbanMaximumTagRunes  = 64
)

var kanbanRoles = map[string]bool{
	"planning": true, "active": true, "rework": true,
	"human": true, "review": true, "complete": true,
}

type mcpKanbanStore interface {
	ListProfileNotes(context.Context, string, bool) ([]domain.UserNote, error)
	GetUserProfileSettings(context.Context, string) (domain.UserProfileSettings, error)
	ListNoteAttachments(context.Context, string) ([]domain.NoteAttachment, error)
}

type mcpKanbanNotes interface {
	FetchProfile(context.Context, string, string) (protocol.NotesFetchResponse, error)
	SaveProfile(context.Context, string, string, protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error)
}

type mcpKanbanService struct {
	store mcpKanbanStore
	notes mcpKanbanNotes
	now   func() time.Time
}

func newMCPKanbanService(store mcpKanbanStore, notes mcpKanbanNotes, now func() time.Time) *mcpKanbanService {
	if now == nil {
		now = time.Now
	}
	return &mcpKanbanService{store: store, notes: notes, now: now}
}

type mcpKanbanColumnSummary struct {
	ID        string `json:"column_id"`
	Title     string `json:"title"`
	Role      string `json:"role,omitempty"`
	CardCount int    `json:"card_count"`
}

type mcpKanbanBoardSummary struct {
	BoardID         string                   `json:"board_id"`
	Title           string                   `json:"title"`
	Default         bool                     `json:"default"`
	IntakeColumnID  string                   `json:"intake_column_id,omitempty"`
	Revision        string                   `json:"revision"`
	Columns         []mcpKanbanColumnSummary `json:"columns"`
	TotalCardCount  int                      `json:"total_card_count"`
	ActiveCardCount int                      `json:"active_card_count"`
}

type mcpKanbanPermissions struct {
	CanRead   bool `json:"can_read"`
	CanWrite  bool `json:"can_write"`
	CanDelete bool `json:"can_delete"`
}

type mcpKanbanColumnView struct {
	ID    string                `json:"column_id"`
	Title string                `json:"title"`
	Role  string                `json:"role,omitempty"`
	Cards []mcpKanbanCardResult `json:"cards"`
}

type mcpKanbanBoardView struct {
	BoardID        string                `json:"board_id"`
	Title          string                `json:"title"`
	Revision       string                `json:"revision"`
	IntakeColumnID string                `json:"intake_column_id,omitempty"`
	Columns        []mcpKanbanColumnView `json:"columns"`
}

type mcpKanbanBoardSnapshot struct {
	StateVersion  string                  `json:"state_version"`
	Boards        []mcpKanbanBoardSummary `json:"boards"`
	SelectedBoard *mcpKanbanBoardView     `json:"selected_board"`
	Permissions   mcpKanbanPermissions    `json:"permissions"`
}

type mcpKanbanCardResult struct {
	BoardID         string                    `json:"board_id"`
	BoardTitle      string                    `json:"board_title"`
	BoardRevision   string                    `json:"board_revision"`
	ColumnID        string                    `json:"column_id"`
	ColumnTitle     string                    `json:"column_title"`
	ColumnRole      string                    `json:"column_role,omitempty"`
	CardID          string                    `json:"card_id"`
	Title           string                    `json:"title"`
	DetailsMarkdown string                    `json:"details_markdown"`
	DueDate         string                    `json:"due_date,omitempty"`
	Tags            []string                  `json:"tags"`
	Priority        string                    `json:"priority,omitempty"`
	Color           string                    `json:"color,omitempty"`
	CreatedAt       time.Time                 `json:"created_at,omitempty"`
	UpdatedAt       time.Time                 `json:"updated_at,omitempty"`
	Columns         []mcpKanbanColumnSummary  `json:"columns,omitempty"`
	Attachments     []protocol.NoteAttachment `json:"attachments,omitempty"`
}

type mcpKanbanListCardsArgs struct {
	BoardID         string   `json:"board_id"`
	ColumnID        string   `json:"column_id"`
	Role            string   `json:"role"`
	Query           string   `json:"query"`
	Tags            []string `json:"tags"`
	Priority        string   `json:"priority"`
	DueFrom         string   `json:"due_from"`
	DueThrough      string   `json:"due_through"`
	IncludeComplete bool     `json:"include_complete"`
	Limit           int      `json:"limit"`
}

type mcpKanbanNoDefaultError struct {
	Boards []mcpKanbanBoardSummary `json:"boards"`
}

type mcpKanbanCreateArgs struct {
	BoardID         string   `json:"board_id"`
	ColumnID        string   `json:"column_id"`
	Title           string   `json:"title"`
	DetailsMarkdown string   `json:"details_markdown"`
	DueDate         string   `json:"due_date"`
	Tags            []string `json:"tags"`
	Priority        string   `json:"priority"`
}

type mcpKanbanUpdateArgs struct {
	BoardID         string    `json:"board_id"`
	CardID          string    `json:"card_id"`
	Title           *string   `json:"title"`
	DetailsMarkdown *string   `json:"details_markdown"`
	DueDate         *string   `json:"due_date"`
	Tags            *[]string `json:"tags"`
	Priority        *string   `json:"priority"`
}

type mcpKanbanWorklogArgs struct {
	BoardID       string `json:"board_id"`
	CardID        string `json:"card_id"`
	EntryMarkdown string `json:"entry_markdown"`
	Kind          string `json:"kind"`
}

type mcpKanbanMoveArgs struct {
	BoardID        string `json:"board_id"`
	CardID         string `json:"card_id"`
	TargetColumnID string `json:"target_column_id"`
	TargetIndex    *int   `json:"target_index"`
}

type mcpKanbanDeleteArgs struct {
	BoardID               string `json:"board_id"`
	CardID                string `json:"card_id"`
	ExpectedBoardRevision string `json:"expected_board_revision"`
	Confirmed             bool   `json:"confirmed"`
}

type mcpKanbanDeleteResult struct {
	BoardID       string `json:"board_id"`
	CardID        string `json:"card_id"`
	BoardRevision string `json:"board_revision"`
	StateVersion  string `json:"state_version"`
	Deleted       bool   `json:"deleted"`
}

type mcpKanbanConflictError struct {
	Message string               `json:"message"`
	Latest  *mcpKanbanCardResult `json:"latest,omitempty"`
}

func (e *mcpKanbanConflictError) Error() string { return e.Message }

func (e *mcpKanbanNoDefaultError) Error() string {
	return "no usable default Kanban board is configured; select a board_id from the available boards"
}

type mcpKanbanBoardRecord struct {
	Note  domain.UserNote
	Fetch protocol.NotesFetchResponse
}

func (s *mcpKanbanService) ListBoards(ctx context.Context, userID string) ([]mcpKanbanBoardSummary, error) {
	records, defaultID, err := s.visibleBoards(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]mcpKanbanBoardSummary, 0, len(records))
	for _, record := range records {
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return nil, fmt.Errorf("board %s: %w", record.Note.NoteID, err)
		}
		result = append(result, summarizeKanbanBoard(record, record.Note.NoteID == defaultID))
	}
	return result, nil
}

func (s *mcpKanbanService) OpenBoard(ctx context.Context, userID, boardID string, permissions mcpKanbanPermissions) (mcpKanbanBoardSnapshot, error) {
	records, defaultID, err := s.visibleBoards(ctx, userID)
	if err != nil {
		return mcpKanbanBoardSnapshot{}, err
	}
	snapshot := mcpKanbanBoardSnapshot{
		Boards:      make([]mcpKanbanBoardSummary, 0, len(records)),
		Permissions: permissions,
	}
	for _, record := range records {
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return mcpKanbanBoardSnapshot{}, fmt.Errorf("board %s: %w", record.Note.NoteID, err)
		}
		snapshot.Boards = append(snapshot.Boards, summarizeKanbanBoard(record, record.Note.NoteID == defaultID))
	}
	requested := strings.TrimSpace(boardID)
	if requested == "" {
		requested = defaultID
		if requested == "" && len(records) > 0 {
			requested = records[0].Note.NoteID
		}
	}
	if requested == "" {
		return snapshot, nil
	}
	var selected *mcpKanbanBoardRecord
	for index := range records {
		if records[index].Note.NoteID == requested {
			selected = &records[index]
			break
		}
	}
	if selected == nil {
		return mcpKanbanBoardSnapshot{}, store.ErrNotFound
	}
	view := mcpKanbanBoardView{
		BoardID: selected.Note.NoteID, Title: selected.Note.Title, Revision: selected.Fetch.Revision,
		IntakeColumnID: selected.Fetch.Board.IntakeColumnID, Columns: []mcpKanbanColumnView{},
	}
	for _, column := range orderedKanbanColumns(selected.Fetch.Board.Columns) {
		columnView := mcpKanbanColumnView{ID: column.ID, Title: column.Title, Role: column.Role, Cards: []mcpKanbanCardResult{}}
		for _, card := range orderedKanbanCards(column.Cards) {
			result := kanbanCardResult(*selected, column, card, false)
			attachments, attachmentErr := s.referencedCardAttachments(ctx, *selected, card)
			if attachmentErr != nil {
				return mcpKanbanBoardSnapshot{}, attachmentErr
			}
			result.Attachments = attachments
			columnView.Cards = append(columnView.Cards, result)
		}
		view.Columns = append(view.Columns, columnView)
	}
	snapshot.StateVersion = selected.Fetch.Revision
	snapshot.SelectedBoard = &view
	return snapshot, nil
}

func (s *mcpKanbanService) ListCards(ctx context.Context, userID string, args mcpKanbanListCardsArgs) ([]mcpKanbanCardResult, error) {
	if args.ColumnID != "" && args.Role != "" {
		return nil, errors.New("column_id and role cannot be combined")
	}
	if args.Role != "" && !kanbanRoles[args.Role] {
		return nil, fmt.Errorf("unsupported role %q", args.Role)
	}
	if args.Limit < 0 || args.Limit > kanbanMaximumListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", kanbanMaximumListLimit)
	}
	if args.Limit == 0 {
		args.Limit = kanbanDefaultListLimit
	}
	if err := validateKanbanDate("due_from", args.DueFrom); err != nil {
		return nil, err
	}
	if err := validateKanbanDate("due_through", args.DueThrough); err != nil {
		return nil, err
	}
	if err := validateKanbanPriority(args.Priority); err != nil {
		return nil, err
	}
	requestedTags, err := normalizeKanbanTags(args.Tags)
	if err != nil {
		return nil, err
	}
	record, err := s.resolveBoard(ctx, userID, args.BoardID)
	if err != nil {
		return nil, err
	}
	if err := validateKanbanBoard(record.Fetch.Board); err != nil {
		return nil, err
	}

	query := strings.ToLower(strings.TrimSpace(args.Query))
	includeComplete := args.IncludeComplete || args.Role == "complete"
	result := make([]mcpKanbanCardResult, 0)
	for _, column := range orderedKanbanColumns(record.Fetch.Board.Columns) {
		if args.ColumnID != "" && column.ID != args.ColumnID {
			continue
		}
		if args.Role != "" && column.Role != args.Role {
			continue
		}
		if !includeComplete && column.Role == "complete" {
			continue
		}
		for _, card := range orderedKanbanCards(column.Cards) {
			if args.Priority != "" && card.Priority != args.Priority {
				continue
			}
			title, details := splitKanbanCardText(card.Text)
			if query != "" && !strings.Contains(strings.ToLower(title+"\n"+details+"\n"+card.Priority+"\n"+strings.Join(card.Tags, " ")), query) {
				continue
			}
			if !kanbanTagsContainAll(card.Tags, requestedTags) {
				continue
			}
			if args.DueFrom != "" && (card.DueDate == "" || card.DueDate < args.DueFrom) {
				continue
			}
			if args.DueThrough != "" && (card.DueDate == "" || card.DueDate > args.DueThrough) {
				continue
			}
			result = append(result, kanbanCardResult(record, column, card, false))
			if len(result) == args.Limit {
				return result, nil
			}
		}
	}
	return result, nil
}

func (s *mcpKanbanService) GetCard(ctx context.Context, userID string, boardID string, cardID string) (mcpKanbanCardResult, error) {
	if strings.TrimSpace(cardID) == "" {
		return mcpKanbanCardResult{}, errors.New("card_id is required")
	}
	record, err := s.resolveBoard(ctx, userID, boardID)
	if err != nil {
		return mcpKanbanCardResult{}, err
	}
	if err := validateKanbanBoard(record.Fetch.Board); err != nil {
		return mcpKanbanCardResult{}, err
	}
	column, card, ok := findKanbanCard(record.Fetch.Board, cardID)
	if !ok {
		return mcpKanbanCardResult{}, store.ErrNotFound
	}
	result := kanbanCardResult(record, column, card, true)
	attachments, err := s.referencedCardAttachments(ctx, record, card)
	if err != nil {
		return mcpKanbanCardResult{}, err
	}
	result.Attachments = attachments
	return result, nil
}

func (s *mcpKanbanService) BoardRevision(ctx context.Context, userID, boardID string) (string, error) {
	record, err := s.resolveBoard(ctx, userID, boardID)
	if err != nil {
		return "", err
	}
	return record.Fetch.Revision, nil
}

func (s *mcpKanbanService) CreateCard(ctx context.Context, userID string, args mcpKanbanCreateArgs) (mcpKanbanCardResult, error) {
	title := strings.TrimSpace(args.Title)
	if title == "" {
		return mcpKanbanCardResult{}, errors.New("title is required")
	}
	if err := validateKanbanDate("due_date", args.DueDate); err != nil {
		return mcpKanbanCardResult{}, err
	}
	if err := validateKanbanPriority(args.Priority); err != nil {
		return mcpKanbanCardResult{}, err
	}
	tags, err := normalizeKanbanTags(args.Tags)
	if err != nil {
		return mcpKanbanCardResult{}, err
	}
	cardID := newID("kanban_card")
	var originalColumn *protocol.KanbanColumn
	for attempt := 0; attempt < 3; attempt++ {
		record, resolveErr := s.resolveBoard(ctx, userID, args.BoardID)
		if resolveErr != nil {
			return mcpKanbanCardResult{}, resolveErr
		}
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return mcpKanbanCardResult{}, err
		}
		columnIndex, selectErr := createDestinationIndex(record.Fetch.Board, args.ColumnID)
		if selectErr != nil {
			return mcpKanbanCardResult{}, selectErr
		}
		currentColumn := record.Fetch.Board.Columns[columnIndex]
		if originalColumn == nil {
			copy := currentColumn
			originalColumn = &copy
		} else if !reflect.DeepEqual(*originalColumn, currentColumn) {
			return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban destination column changed during create"}
		}
		board := cloneKanbanBoard(record.Fetch.Board)
		now := s.now().UTC()
		card := protocol.KanbanCard{
			ID: cardID, Text: joinKanbanCardText(title, args.DetailsMarkdown), SortOrder: len(board.Columns[columnIndex].Cards),
			DueDate: args.DueDate, Tags: tags, Priority: args.Priority, CreatedAt: now, UpdatedAt: now,
		}
		board.Columns[columnIndex].Cards = append(board.Columns[columnIndex].Cards, card)
		response, saveErr := s.saveBoard(ctx, userID, record, board)
		if saveErr == nil {
			record.Fetch.Board = board
			record.Fetch.Revision = response.Revision
			return kanbanCardResult(record, board.Columns[columnIndex], card, true), nil
		}
		if !isNoteConflict(saveErr) {
			return mcpKanbanCardResult{}, saveErr
		}
	}
	return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban board remained in conflict after three attempts"}
}

func (s *mcpKanbanService) UpdateCard(ctx context.Context, userID string, args mcpKanbanUpdateArgs) (mcpKanbanCardResult, error) {
	if strings.TrimSpace(args.CardID) == "" {
		return mcpKanbanCardResult{}, errors.New("card_id is required")
	}
	if args.Title == nil && args.DetailsMarkdown == nil && args.DueDate == nil && args.Tags == nil && args.Priority == nil {
		return mcpKanbanCardResult{}, errors.New("at least one card field is required")
	}
	if args.Title != nil && strings.TrimSpace(*args.Title) == "" {
		return mcpKanbanCardResult{}, errors.New("title cannot be empty")
	}
	if args.DueDate != nil {
		if err := validateKanbanDate("due_date", *args.DueDate); err != nil {
			return mcpKanbanCardResult{}, err
		}
	}
	if args.Priority != nil {
		if err := validateKanbanPriority(*args.Priority); err != nil {
			return mcpKanbanCardResult{}, err
		}
	}
	var normalizedTags []string
	if args.Tags != nil {
		var err error
		normalizedTags, err = normalizeKanbanTags(*args.Tags)
		if err != nil {
			return mcpKanbanCardResult{}, err
		}
	}
	return s.mutateExistingCard(ctx, userID, args.BoardID, args.CardID, func(card *protocol.KanbanCard) error {
		title, details := splitKanbanCardText(card.Text)
		if args.Title != nil {
			title = strings.TrimSpace(*args.Title)
		}
		if args.DetailsMarkdown != nil {
			details = *args.DetailsMarkdown
		}
		card.Text = joinKanbanCardText(title, details)
		if args.DueDate != nil {
			card.DueDate = *args.DueDate
		}
		if args.Tags != nil {
			card.Tags = append([]string(nil), normalizedTags...)
		}
		if args.Priority != nil {
			card.Priority = *args.Priority
		}
		card.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *mcpKanbanService) AppendWorklog(ctx context.Context, userID string, args mcpKanbanWorklogArgs) (mcpKanbanCardResult, error) {
	if strings.TrimSpace(args.CardID) == "" {
		return mcpKanbanCardResult{}, errors.New("card_id is required")
	}
	entry := strings.TrimSpace(args.EntryMarkdown)
	if entry == "" {
		return mcpKanbanCardResult{}, errors.New("entry_markdown is required")
	}
	labels := map[string]string{"progress": "Progress", "verification": "Verification", "blocker": "Blocker", "outcome": "Outcome"}
	label, ok := labels[args.Kind]
	if !ok {
		return mcpKanbanCardResult{}, errors.New("kind must be progress, verification, blocker, or outcome")
	}
	return s.mutateExistingCard(ctx, userID, args.BoardID, args.CardID, func(card *protocol.KanbanCard) error {
		title, details := splitKanbanCardText(card.Text)
		if !hasKanbanWorklog(details) {
			if strings.TrimSpace(details) != "" {
				details = strings.TrimSpace(details) + "\n\n"
			}
			details += "## Work log"
		}
		details = strings.TrimSpace(details) + "\n\n### " + s.now().UTC().Format(time.RFC3339) + " — " + label + "\n\n" + entry
		card.Text = joinKanbanCardText(title, details)
		card.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *mcpKanbanService) MoveCard(ctx context.Context, userID string, args mcpKanbanMoveArgs) (mcpKanbanCardResult, error) {
	if strings.TrimSpace(args.CardID) == "" {
		return mcpKanbanCardResult{}, errors.New("card_id is required")
	}
	if strings.TrimSpace(args.TargetColumnID) == "" {
		return mcpKanbanCardResult{}, errors.New("target_column_id is required")
	}
	var originalCard *protocol.KanbanCard
	var originalSource, originalTarget *protocol.KanbanColumn
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.resolveBoard(ctx, userID, args.BoardID)
		if err != nil {
			return mcpKanbanCardResult{}, err
		}
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return mcpKanbanCardResult{}, err
		}
		sourceIndex, cardIndex, targetIndex, findErr := findKanbanMove(record.Fetch.Board, args.CardID, args.TargetColumnID)
		if findErr != nil {
			return mcpKanbanCardResult{}, findErr
		}
		currentCard := record.Fetch.Board.Columns[sourceIndex].Cards[cardIndex]
		currentSource := record.Fetch.Board.Columns[sourceIndex]
		currentTarget := record.Fetch.Board.Columns[targetIndex]
		if originalCard == nil {
			cardCopy, sourceCopy, targetCopy := currentCard, currentSource, currentTarget
			originalCard, originalSource, originalTarget = &cardCopy, &sourceCopy, &targetCopy
		} else if !reflect.DeepEqual(*originalCard, currentCard) || !reflect.DeepEqual(*originalSource, currentSource) || !reflect.DeepEqual(*originalTarget, currentTarget) {
			latest := kanbanCardResult(record, currentSource, currentCard, true)
			return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban card or required column changed during move", Latest: &latest}
		}

		board := cloneKanbanBoard(record.Fetch.Board)
		card := board.Columns[sourceIndex].Cards[cardIndex]
		board.Columns[sourceIndex].Cards = append(board.Columns[sourceIndex].Cards[:cardIndex], board.Columns[sourceIndex].Cards[cardIndex+1:]...)
		resultingCount := len(board.Columns[targetIndex].Cards)
		if sourceIndex == targetIndex {
			resultingCount = len(board.Columns[targetIndex].Cards)
		}
		insertAt := resultingCount
		if args.TargetIndex != nil {
			insertAt = *args.TargetIndex
		}
		if insertAt < 0 || insertAt > resultingCount {
			return mcpKanbanCardResult{}, fmt.Errorf("target_index must be between 0 and %d", resultingCount)
		}
		card.UpdatedAt = s.now().UTC()
		targetCards := board.Columns[targetIndex].Cards
		targetCards = append(targetCards, protocol.KanbanCard{})
		copy(targetCards[insertAt+1:], targetCards[insertAt:])
		targetCards[insertAt] = card
		board.Columns[targetIndex].Cards = targetCards
		normalizeKanbanCardSortOrders(&board.Columns[sourceIndex])
		if sourceIndex != targetIndex {
			normalizeKanbanCardSortOrders(&board.Columns[targetIndex])
		}
		response, saveErr := s.saveBoard(ctx, userID, record, board)
		if saveErr == nil {
			record.Fetch.Board = board
			record.Fetch.Revision = response.Revision
			return kanbanCardResult(record, board.Columns[targetIndex], card, true), nil
		}
		if !isNoteConflict(saveErr) {
			return mcpKanbanCardResult{}, saveErr
		}
	}
	return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban board remained in conflict after three attempts"}
}

func (s *mcpKanbanService) DeleteCard(ctx context.Context, userID string, args mcpKanbanDeleteArgs) (mcpKanbanDeleteResult, error) {
	if !args.Confirmed {
		return mcpKanbanDeleteResult{}, errors.New("confirmed must be true after in-widget confirmation")
	}
	if strings.TrimSpace(args.CardID) == "" {
		return mcpKanbanDeleteResult{}, errors.New("card_id is required")
	}
	if strings.TrimSpace(args.ExpectedBoardRevision) == "" {
		return mcpKanbanDeleteResult{}, errors.New("expected_board_revision is required")
	}
	var original *protocol.KanbanCard
	var originalColumnID string
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.resolveBoard(ctx, userID, args.BoardID)
		if err != nil {
			return mcpKanbanDeleteResult{}, err
		}
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return mcpKanbanDeleteResult{}, err
		}
		columnIndex, cardIndex, ok := findKanbanCardIndexes(record.Fetch.Board, args.CardID)
		if !ok {
			return mcpKanbanDeleteResult{}, store.ErrNotFound
		}
		current := record.Fetch.Board.Columns[columnIndex].Cards[cardIndex]
		if attempt == 0 && record.Fetch.Revision != args.ExpectedBoardRevision {
			latest := kanbanCardResult(record, record.Fetch.Board.Columns[columnIndex], current, true)
			return mcpKanbanDeleteResult{}, &mcpKanbanConflictError{Message: "Kanban board revision changed before delete", Latest: &latest}
		}
		if original == nil {
			copy := current
			original = &copy
			originalColumnID = record.Fetch.Board.Columns[columnIndex].ID
		} else if originalColumnID != record.Fetch.Board.Columns[columnIndex].ID || !reflect.DeepEqual(*original, current) {
			latest := kanbanCardResult(record, record.Fetch.Board.Columns[columnIndex], current, true)
			return mcpKanbanDeleteResult{}, &mcpKanbanConflictError{Message: "Kanban card changed during delete", Latest: &latest}
		}
		board := cloneKanbanBoard(record.Fetch.Board)
		column := &board.Columns[columnIndex]
		column.Cards = append(column.Cards[:cardIndex], column.Cards[cardIndex+1:]...)
		normalizeKanbanCardSortOrders(column)
		response, saveErr := s.saveBoard(ctx, userID, record, board)
		if saveErr == nil {
			return mcpKanbanDeleteResult{
				BoardID: record.Note.NoteID, CardID: args.CardID, BoardRevision: response.Revision,
				StateVersion: response.Revision, Deleted: true,
			}, nil
		}
		if !isNoteConflict(saveErr) {
			return mcpKanbanDeleteResult{}, saveErr
		}
	}
	return mcpKanbanDeleteResult{}, &mcpKanbanConflictError{Message: "Kanban board remained in conflict after three delete attempts"}
}

func (s *mcpKanbanService) mutateExistingCard(ctx context.Context, userID, boardID, cardID string, mutate func(*protocol.KanbanCard) error) (mcpKanbanCardResult, error) {
	var original *protocol.KanbanCard
	var originalColumnID string
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.resolveBoard(ctx, userID, boardID)
		if err != nil {
			return mcpKanbanCardResult{}, err
		}
		if err := validateKanbanBoard(record.Fetch.Board); err != nil {
			return mcpKanbanCardResult{}, err
		}
		columnIndex, cardIndex, ok := findKanbanCardIndexes(record.Fetch.Board, cardID)
		if !ok {
			return mcpKanbanCardResult{}, store.ErrNotFound
		}
		current := record.Fetch.Board.Columns[columnIndex].Cards[cardIndex]
		if original == nil {
			copy := current
			original = &copy
			originalColumnID = record.Fetch.Board.Columns[columnIndex].ID
		} else if originalColumnID != record.Fetch.Board.Columns[columnIndex].ID || !reflect.DeepEqual(*original, current) {
			latest := kanbanCardResult(record, record.Fetch.Board.Columns[columnIndex], current, true)
			return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban card changed during update", Latest: &latest}
		}
		board := cloneKanbanBoard(record.Fetch.Board)
		if err := mutate(&board.Columns[columnIndex].Cards[cardIndex]); err != nil {
			return mcpKanbanCardResult{}, err
		}
		response, saveErr := s.saveBoard(ctx, userID, record, board)
		if saveErr == nil {
			record.Fetch.Board = board
			record.Fetch.Revision = response.Revision
			return kanbanCardResult(record, board.Columns[columnIndex], board.Columns[columnIndex].Cards[cardIndex], true), nil
		}
		if !isNoteConflict(saveErr) {
			return mcpKanbanCardResult{}, saveErr
		}
	}
	return mcpKanbanCardResult{}, &mcpKanbanConflictError{Message: "Kanban board remained in conflict after three attempts"}
}

func (s *mcpKanbanService) saveBoard(ctx context.Context, userID string, record mcpKanbanBoardRecord, board *protocol.KanbanBoard) (protocol.NotesSaveResponse, error) {
	parentID := record.Fetch.ParentID
	sortOrder := record.Fetch.SortOrder
	excluded := record.Fetch.MCPExcluded
	return s.notes.SaveProfile(ctx, userID, record.Note.NoteID, protocol.NotesSaveRequest{
		NoteID: record.Note.NoteID, Title: record.Fetch.Title,
		Content: kanbanMarkdown(record.Fetch.Title, *board), BodyMarkdown: kanbanMarkdown(record.Fetch.Title, *board), BodyFormat: "markdown",
		ExpectedRevision: record.Fetch.Revision, PageType: protocol.NotePageTypeKanban,
		ParentID: &parentID, MCPExcluded: &excluded, SortOrder: &sortOrder, Board: board,
	})
}

func isNoteConflict(err error) bool {
	var conflict *noteConflictError
	return errors.As(err, &conflict)
}

func joinKanbanCardText(title, details string) string {
	title = strings.TrimSpace(title)
	details = strings.TrimSpace(details)
	if details == "" {
		return title
	}
	return title + "\n" + details
}

func hasKanbanWorklog(details string) bool {
	trimmed := strings.TrimSpace(details)
	return strings.HasPrefix(trimmed, "## Work log") || strings.Contains(trimmed, "\n## Work log")
}

func cloneKanbanBoard(board *protocol.KanbanBoard) *protocol.KanbanBoard {
	if board == nil {
		return nil
	}
	data, _ := json.Marshal(board)
	var result protocol.KanbanBoard
	_ = json.Unmarshal(data, &result)
	return &result
}

func createDestinationIndex(board *protocol.KanbanBoard, columnID string) (int, error) {
	if board == nil || len(board.Columns) == 0 {
		return -1, errors.New("selected Kanban board has no columns")
	}
	selected := strings.TrimSpace(columnID)
	if selected == "" {
		selected = board.IntakeColumnID
	}
	if selected != "" {
		for index := range board.Columns {
			if board.Columns[index].ID == selected {
				return index, nil
			}
		}
		if columnID != "" {
			return -1, store.ErrNotFound
		}
	}
	ordered := orderedKanbanColumns(board.Columns)
	for index := range board.Columns {
		if board.Columns[index].ID == ordered[0].ID {
			return index, nil
		}
	}
	return -1, errors.New("selected Kanban board has no columns")
}

func findKanbanCardIndexes(board *protocol.KanbanBoard, cardID string) (int, int, bool) {
	if board == nil {
		return -1, -1, false
	}
	for columnIndex := range board.Columns {
		for cardIndex := range board.Columns[columnIndex].Cards {
			if board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				return columnIndex, cardIndex, true
			}
		}
	}
	return -1, -1, false
}

func findKanbanMove(board *protocol.KanbanBoard, cardID, targetColumnID string) (int, int, int, error) {
	sourceIndex, cardIndex, ok := findKanbanCardIndexes(board, cardID)
	if !ok {
		return -1, -1, -1, store.ErrNotFound
	}
	for targetIndex := range board.Columns {
		if board.Columns[targetIndex].ID == targetColumnID {
			return sourceIndex, cardIndex, targetIndex, nil
		}
	}
	return -1, -1, -1, store.ErrNotFound
}

func normalizeKanbanCardSortOrders(column *protocol.KanbanColumn) {
	for index := range column.Cards {
		column.Cards[index].SortOrder = index
	}
}

func (s *mcpKanbanService) visibleBoards(ctx context.Context, userID string) ([]mcpKanbanBoardRecord, string, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return nil, "", err
	}
	defaultID := ""
	settings, err := s.store.GetUserProfileSettings(ctx, userID)
	if err == nil {
		var values map[string]any
		if json.Unmarshal(settings.Settings, &values) == nil {
			defaultID, _ = values["kanban_default_board_id"].(string)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}

	records := make([]mcpKanbanBoardRecord, 0)
	for _, note := range notes {
		if normalizePageType(note.PageType) != protocol.NotePageTypeKanban || !mcpNoteVisible(notes, note.NoteID) {
			continue
		}
		fetched, fetchErr := s.notes.FetchProfile(ctx, userID, note.NoteID)
		if fetchErr != nil {
			return nil, "", fetchErr
		}
		records = append(records, mcpKanbanBoardRecord{Note: note, Fetch: fetched})
	}
	usableDefault := ""
	for _, record := range records {
		if record.Note.NoteID == defaultID {
			usableDefault = defaultID
			break
		}
	}
	return records, usableDefault, nil
}

func (s *mcpKanbanService) resolveBoard(ctx context.Context, userID string, boardID string) (mcpKanbanBoardRecord, error) {
	records, defaultID, err := s.visibleBoards(ctx, userID)
	if err != nil {
		return mcpKanbanBoardRecord{}, err
	}
	requested := strings.TrimSpace(boardID)
	if requested == "" {
		requested = defaultID
		if requested == "" {
			boards := make([]mcpKanbanBoardSummary, 0, len(records))
			for _, record := range records {
				boards = append(boards, summarizeKanbanBoard(record, false))
			}
			return mcpKanbanBoardRecord{}, &mcpKanbanNoDefaultError{Boards: boards}
		}
	}
	for _, record := range records {
		if record.Note.NoteID == requested {
			return record, nil
		}
	}
	return mcpKanbanBoardRecord{}, store.ErrNotFound
}

func validateKanbanBoard(board *protocol.KanbanBoard) error {
	if board == nil {
		return errors.New("Kanban board data is missing")
	}
	columnIDs := map[string]bool{}
	cardIDs := map[string]bool{}
	roles := map[string]bool{}
	for _, column := range board.Columns {
		if strings.TrimSpace(column.ID) == "" || columnIDs[column.ID] {
			return errors.New("column IDs must be nonempty and unique")
		}
		columnIDs[column.ID] = true
		if column.Role != "" {
			if !kanbanRoles[column.Role] {
				return fmt.Errorf("unsupported column role %q", column.Role)
			}
			if roles[column.Role] {
				return fmt.Errorf("column role %q must be unique", column.Role)
			}
			roles[column.Role] = true
		}
		for _, card := range column.Cards {
			if strings.TrimSpace(card.ID) == "" || cardIDs[card.ID] {
				return errors.New("card IDs must be nonempty and unique")
			}
			cardIDs[card.ID] = true
			if err := validateKanbanDate("due_date", card.DueDate); err != nil {
				return err
			}
			if _, err := normalizeKanbanTags(card.Tags); err != nil {
				return err
			}
			if err := validateKanbanPriority(card.Priority); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateKanbanDate(field string, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("%s must be a valid YYYY-MM-DD date", field)
	}
	return nil
}

func validateKanbanPriority(value string) error {
	switch strings.TrimSpace(value) {
	case "", "low", "medium", "high":
		return nil
	default:
		return errors.New("priority must be empty, low, medium, or high")
	}
}

func normalizeKanbanTags(tags []string) ([]string, error) {
	result := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > kanbanMaximumTagRunes {
			return nil, fmt.Errorf("tags may contain at most %d Unicode code points", kanbanMaximumTagRunes)
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, tag)
		if len(result) > kanbanMaximumTags {
			return nil, fmt.Errorf("a card may have at most %d tags", kanbanMaximumTags)
		}
	}
	return result, nil
}

func kanbanTagsContainAll(cardTags []string, requested []string) bool {
	available := map[string]bool{}
	for _, tag := range cardTags {
		available[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	for _, tag := range requested {
		if !available[strings.ToLower(tag)] {
			return false
		}
	}
	return true
}

func splitKanbanCardText(text string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
		}
	}
	return "", ""
}

func orderedKanbanColumns(columns []protocol.KanbanColumn) []protocol.KanbanColumn {
	result := append([]protocol.KanbanColumn(nil), columns...)
	sort.SliceStable(result, func(i, j int) bool { return result[i].SortOrder < result[j].SortOrder })
	return result
}

func orderedKanbanCards(cards []protocol.KanbanCard) []protocol.KanbanCard {
	result := append([]protocol.KanbanCard(nil), cards...)
	sort.SliceStable(result, func(i, j int) bool { return result[i].SortOrder < result[j].SortOrder })
	return result
}

func summarizeKanbanBoard(record mcpKanbanBoardRecord, isDefault bool) mcpKanbanBoardSummary {
	result := mcpKanbanBoardSummary{
		BoardID: record.Note.NoteID, Title: record.Note.Title, Default: isDefault,
		Revision: record.Fetch.Revision, Columns: []mcpKanbanColumnSummary{},
	}
	if record.Fetch.Board == nil {
		return result
	}
	result.IntakeColumnID = record.Fetch.Board.IntakeColumnID
	for _, column := range orderedKanbanColumns(record.Fetch.Board.Columns) {
		count := len(column.Cards)
		result.Columns = append(result.Columns, mcpKanbanColumnSummary{ID: column.ID, Title: column.Title, Role: column.Role, CardCount: count})
		result.TotalCardCount += count
		if column.Role != "complete" {
			result.ActiveCardCount += count
		}
	}
	return result
}

func findKanbanCard(board *protocol.KanbanBoard, cardID string) (protocol.KanbanColumn, protocol.KanbanCard, bool) {
	if board == nil {
		return protocol.KanbanColumn{}, protocol.KanbanCard{}, false
	}
	for _, column := range board.Columns {
		for _, card := range column.Cards {
			if card.ID == cardID {
				return column, card, true
			}
		}
	}
	return protocol.KanbanColumn{}, protocol.KanbanCard{}, false
}

func kanbanColumnSummaries(board *protocol.KanbanBoard) []mcpKanbanColumnSummary {
	result := []mcpKanbanColumnSummary{}
	if board == nil {
		return result
	}
	for _, column := range orderedKanbanColumns(board.Columns) {
		result = append(result, mcpKanbanColumnSummary{ID: column.ID, Title: column.Title, Role: column.Role, CardCount: len(column.Cards)})
	}
	return result
}

func kanbanCardResult(record mcpKanbanBoardRecord, column protocol.KanbanColumn, card protocol.KanbanCard, includeColumns bool) mcpKanbanCardResult {
	title, details := splitKanbanCardText(card.Text)
	result := mcpKanbanCardResult{
		BoardID: record.Note.NoteID, BoardTitle: record.Note.Title, BoardRevision: record.Fetch.Revision,
		ColumnID: column.ID, ColumnTitle: column.Title, ColumnRole: column.Role,
		CardID: card.ID, Title: title, DetailsMarkdown: details, DueDate: card.DueDate,
		Tags: append([]string(nil), card.Tags...), Priority: card.Priority, Color: card.Color, CreatedAt: card.CreatedAt, UpdatedAt: card.UpdatedAt,
	}
	if result.Tags == nil {
		result.Tags = []string{}
	}
	if includeColumns {
		result.Columns = kanbanColumnSummaries(record.Fetch.Board)
	}
	return result
}

func (s *mcpKanbanService) referencedCardAttachments(ctx context.Context, record mcpKanbanBoardRecord, card protocol.KanbanCard) ([]protocol.NoteAttachment, error) {
	attachments, err := s.store.ListNoteAttachments(ctx, record.Note.ID)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.NoteAttachment, 0)
	for _, attachment := range attachments {
		pattern, err := noteAttachmentReferencePattern(attachment.ID)
		if err != nil {
			return nil, err
		}
		if pattern.MatchString(card.Text) {
			result = append(result, noteAttachmentToProtocol(attachment, record.Note, "profile"))
		}
	}
	return result, nil
}
