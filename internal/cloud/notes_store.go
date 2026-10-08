package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type noteConflictError struct {
	Current protocol.NotesFetchResponse
}

func (e *noteConflictError) Error() string { return "note conflict" }

var (
	errNoteAppendContentRequired     = errors.New("append content is required")
	errNoteAppendUnsupportedPageType = errors.New("append is only supported for text notes")
	errNotebookCannotHaveParent      = errors.New("notebooks cannot be placed inside another notebook")
	errInvalidNotebookParent         = errors.New("parent_id must refer to a notebook owned by the same user")
)

type collabScalar struct {
	Value   string `json:"value"`
	Version int64  `json:"version"`
	UserID  string `json:"user_id,omitempty"`
}

type collabState struct {
	Title         collabScalar          `json:"title"`
	PageType      collabScalar          `json:"page_type"`
	ParentID      collabScalar          `json:"parent_id"`
	Pinned        bool                  `json:"pinned"`
	SortOrder     int                   `json:"sort_order"`
	Content       string                `json:"content"`
	Board         *protocol.KanbanBoard `json:"board,omitempty"`
	CollabVersion int64                 `json:"collab_version"`
}

type appliedNoteOperations struct {
	Note       domain.UserNote
	AppliedOps []protocol.NoteCollaborationAppliedOp
}

type cloudNotesService struct {
	store *store.Store
}

func newCloudNotesService(db *store.Store) *cloudNotesService {
	return &cloudNotesService{store: db}
}

func (s *cloudNotesService) ListProfile(ctx context.Context, userID string) ([]protocol.NoteSummary, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return noteSummaries(notes), nil
}

func (s *cloudNotesService) ListDeletedProfile(ctx context.Context, userID string) ([]protocol.NoteSummary, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, true)
	if err != nil {
		return nil, err
	}
	return deletedNoteSummaries(notes), nil
}

func (s *cloudNotesService) ListHome(ctx context.Context, homeID string, userID string) ([]protocol.NoteSummary, error) {
	notes, err := s.store.ListVisibleHomeNotes(ctx, homeID, userID, false)
	if err != nil {
		return nil, err
	}
	return noteSummaries(notes), nil
}

func (s *cloudNotesService) SearchProfile(ctx context.Context, userID string, query string, limit int, parentID string) ([]protocol.NoteSearchResult, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return searchNotes(notes, query, limit, parentID), nil
}

func (s *cloudNotesService) TagsProfile(ctx context.Context, userID string) ([]protocol.NoteTagCount, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return noteTags(notes), nil
}

func (s *cloudNotesService) TagRollupProfile(ctx context.Context, userID string, tag string) ([]protocol.TaggedLineRollupItem, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return noteTagRollup(notes, tag), nil
}

func (s *cloudNotesService) FetchProfile(ctx context.Context, userID string, noteID string) (protocol.NotesFetchResponse, error) {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return protocol.NotesFetchResponse{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NotesFetchResponse{}, store.ErrNotFound
	}
	return noteFetch(note)
}

func (s *cloudNotesService) FetchHome(ctx context.Context, homeID string, userID string, noteID string) (protocol.NotesFetchResponse, error) {
	note, err := s.store.GetHomeNoteVisibleToUser(ctx, homeID, userID, noteID)
	if err != nil {
		return protocol.NotesFetchResponse{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NotesFetchResponse{}, store.ErrNotFound
	}
	return noteFetch(note)
}

func (s *cloudNotesService) SaveProfile(ctx context.Context, userID string, noteID string, request protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error) {
	return s.save(ctx, "", userID, userID, noteID, request)
}

func (s *cloudNotesService) SaveHome(ctx context.Context, homeID string, userID string, noteID string, request protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error) {
	return s.save(ctx, homeID, userID, userID, noteID, request)
}

func (s *cloudNotesService) AppendProfile(ctx context.Context, userID string, noteID string, request protocol.NotesAppendRequest) (protocol.NotesSaveResponse, error) {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NotesSaveResponse{}, store.ErrNotFound
	}
	saveRequest, err := appendRequestForNote(note, request)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	return s.SaveProfile(ctx, userID, noteID, saveRequest)
}

func (s *cloudNotesService) AppendHome(ctx context.Context, homeID string, userID string, noteID string, request protocol.NotesAppendRequest) (protocol.NotesSaveResponse, error) {
	note, err := s.store.GetHomeNoteVisibleToUser(ctx, homeID, userID, noteID)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NotesSaveResponse{}, store.ErrNotFound
	}
	saveRequest, err := appendRequestForNote(note, request)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	return s.SaveHome(ctx, homeID, userID, noteID, saveRequest)
}

func (s *cloudNotesService) RenameHome(ctx context.Context, homeID string, userID string, noteID string, title string) error {
	note, err := s.store.GetHomeNoteVisibleToUser(ctx, homeID, userID, noteID)
	if err != nil {
		return err
	}
	return s.saveTitleOnly(ctx, note, userID, title)
}

func (s *cloudNotesService) RenameProfile(ctx context.Context, userID string, noteID string, title string) error {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return err
	}
	return s.saveTitleOnly(ctx, note, userID, title)
}

func (s *cloudNotesService) saveTitleOnly(ctx context.Context, note domain.UserNote, userID string, title string) error {
	state, err := decodeCollabState(note)
	if err != nil {
		return err
	}
	now := nowUTC()
	state.Title = collabScalar{Value: strings.TrimSpace(title), Version: note.CollabVersion + 1, UserID: userID}
	state.CollabVersion++
	updated, operation, err := materializeNoteFromState(note, state, userID, now)
	if err != nil {
		return err
	}
	return s.store.SaveUserNoteWithOperations(ctx, updated, []domain.NoteOperation{{
		NoteID:         updated.ID,
		OpID:           "http-rename:" + updated.Revision,
		ActorUserID:    userID,
		SessionID:      "http",
		BaseVersion:    note.CollabVersion,
		AppliedVersion: state.CollabVersion,
		OpJSON:         operation,
		CreatedAt:      now,
	}})
}

func (s *cloudNotesService) DeleteProfile(ctx context.Context, userID string, noteID string, expectedRevision string) error {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return err
	}
	return s.deleteNote(ctx, note, userID, expectedRevision)
}

func (s *cloudNotesService) DeleteHome(ctx context.Context, homeID string, userID string, noteID string) error {
	note, err := s.store.GetHomeNoteVisibleToUser(ctx, homeID, userID, noteID)
	if err != nil {
		return err
	}
	return s.deleteNote(ctx, note, userID, "")
}

func (s *cloudNotesService) deleteNote(ctx context.Context, note domain.UserNote, userID string, expectedRevision string) error {
	state, err := decodeCollabState(note)
	if err != nil {
		return err
	}
	now := nowUTC()
	state.CollabVersion++
	operationBody, err := json.Marshal(map[string]any{
		"type": "delete_note",
	})
	if err != nil {
		return err
	}
	note.Revision = revisionBytes(operationBody)
	note.CRDTStateJSON = encodeCollabState(state)
	note.CollabVersion = state.CollabVersion
	note.UpdatedAt = now
	note.UpdatedBy = userID
	note.DeletedAt = &now
	err = s.store.SaveUserNoteWithOperationsIfRevision(ctx, note, []domain.NoteOperation{{
		NoteID:         note.ID,
		OpID:           "http-delete:" + note.Revision,
		ActorUserID:    userID,
		SessionID:      "http",
		BaseVersion:    state.CollabVersion - 1,
		AppliedVersion: state.CollabVersion,
		OpJSON:         string(operationBody),
		CreatedAt:      now,
	}}, expectedRevision)
	var conflict *store.UserNoteRevisionConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	current, currentErr := noteFetch(conflict.Current)
	if currentErr != nil {
		return currentErr
	}
	return &noteConflictError{Current: current}
}

func (s *cloudNotesService) RestoreProfile(ctx context.Context, userID string, noteID string) error {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return err
	}
	if note.DeletedAt == nil {
		return store.ErrNotFound
	}
	state, err := decodeCollabState(note)
	if err != nil {
		return err
	}
	now := nowUTC()
	if note.ParentID != "" {
		parent, parentErr := s.store.GetProfileNote(ctx, userID, note.ParentID)
		if parentErr != nil || parent.DeletedAt != nil || parent.PageType != protocol.NotePageTypeNotebook {
			note.ParentID = ""
			note.Pinned = false
			state.ParentID = collabScalar{Value: "", Version: state.CollabVersion + 1, UserID: userID}
			state.Pinned = false
		}
	}
	state.CollabVersion++
	operationBody, err := json.Marshal(map[string]any{"type": "restore_note"})
	if err != nil {
		return err
	}
	expectedRevision := note.Revision
	note.DeletedAt = nil
	note.Revision = revisionBytes(operationBody)
	note.CRDTStateJSON = encodeCollabState(state)
	note.CollabVersion = state.CollabVersion
	note.UpdatedAt = now
	note.UpdatedBy = userID
	return s.store.SaveUserNoteWithOperationsIfRevision(ctx, note, []domain.NoteOperation{{
		NoteID: note.ID, OpID: "http-restore:" + note.Revision, ActorUserID: userID,
		SessionID: "http", BaseVersion: state.CollabVersion - 1, AppliedVersion: state.CollabVersion,
		OpJSON: string(operationBody), CreatedAt: now,
	}}, expectedRevision)
}

func (s *cloudNotesService) PermanentlyDeleteProfile(ctx context.Context, userID string, noteID string) ([]domain.NoteAttachment, error) {
	note, err := s.store.GetProfileNote(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.DeletedAt == nil {
		return nil, store.ErrNotFound
	}
	attachments, err := s.store.ListNoteAttachments(ctx, note.ID)
	if err != nil {
		return nil, err
	}
	if err := s.store.PermanentlyDeleteUserNote(ctx, note.ID, userID, time.Now().UTC()); err != nil {
		return nil, err
	}
	return attachments, nil
}

func (s *cloudNotesService) ListShares(ctx context.Context, homeID string, noteID string, ownerUserID string) ([]protocol.NotesShareSummary, error) {
	note, err := s.findSharableNote(ctx, homeID, ownerUserID, noteID)
	if err != nil {
		return nil, err
	}
	shares, err := s.store.ListNoteShares(ctx, note.ID)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.NotesShareSummary, 0, len(shares))
	for _, share := range shares {
		result = append(result, protocol.NotesShareSummary{
			UserID:    share.TargetUserID,
			Email:     share.Email,
			SharedBy:  share.SharedBy,
			CreatedAt: share.CreatedAt,
			UpdatedAt: share.UpdatedAt,
		})
	}
	return result, nil
}

func (s *cloudNotesService) GrantShare(ctx context.Context, homeID string, noteID string, ownerUserID string, targetUserID string) (domain.UserNote, bool, error) {
	note, err := s.findSharableNote(ctx, homeID, ownerUserID, noteID)
	if err != nil {
		return domain.UserNote{}, false, err
	}
	if targetUserID == ownerUserID {
		return domain.UserNote{}, false, fmt.Errorf("cannot share to the owner")
	}
	if _, err := s.store.GetHomeMembership(ctx, homeID, targetUserID); err != nil {
		return domain.UserNote{}, false, err
	}
	firstShare := false
	now := nowUTC()
	if note.HomeID == "" {
		if _, err := s.store.GetHomeNoteByKey(ctx, homeID, note.NoteID); err == nil {
			return domain.UserNote{}, false, fmt.Errorf("note already exists in home")
		} else if !errors.Is(err, store.ErrNotFound) {
			return domain.UserNote{}, false, err
		}
		note.HomeID = homeID
		note.UpdatedAt = now
		note.UpdatedBy = ownerUserID
		if err := s.store.UpsertUserNote(ctx, note); err != nil {
			return domain.UserNote{}, false, err
		}
		firstShare = true
	}
	if note.HomeID != homeID {
		return domain.UserNote{}, false, fmt.Errorf("note is already bound to a different home")
	}
	share := domain.NoteShare{
		NoteID:       note.ID,
		HomeID:       homeID,
		TargetUserID: targetUserID,
		SharedBy:     ownerUserID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.AddNoteShare(ctx, share); err != nil {
		return domain.UserNote{}, false, err
	}
	return note, firstShare, nil
}

func (s *cloudNotesService) RevokeShare(ctx context.Context, homeID string, noteID string, ownerUserID string, targetUserID string) (domain.UserNote, bool, error) {
	note, err := s.findSharableNote(ctx, homeID, ownerUserID, noteID)
	if err != nil {
		return domain.UserNote{}, false, err
	}
	if err := s.store.RemoveNoteShare(ctx, note.ID, targetUserID); err != nil {
		return domain.UserNote{}, false, err
	}
	shareCount, err := s.store.CountNoteShares(ctx, note.ID)
	if err != nil {
		return domain.UserNote{}, false, err
	}
	return note, shareCount == 0, nil
}

func (s *cloudNotesService) Sync(ctx context.Context, homeID string) ([]protocol.NoteSummary, error) {
	notes, err := s.store.ListSyncedHomeNotes(ctx, homeID, false)
	if err != nil {
		return nil, err
	}
	return noteSummaries(notes), nil
}

func (s *cloudNotesService) SearchHome(ctx context.Context, homeID string, userID string, query string, limit int, parentID string) ([]protocol.NoteSearchResult, error) {
	notes, err := s.store.ListVisibleHomeNotes(ctx, homeID, userID, false)
	if err != nil {
		return nil, err
	}
	return searchNotes(notes, query, limit, parentID), nil
}

func (s *cloudNotesService) TagsHome(ctx context.Context, homeID string, userID string) ([]protocol.NoteTagCount, error) {
	notes, err := s.store.ListVisibleHomeNotes(ctx, homeID, userID, false)
	if err != nil {
		return nil, err
	}
	return noteTags(notes), nil
}

func (s *cloudNotesService) TagRollupHome(ctx context.Context, homeID string, userID string, tag string) ([]protocol.TaggedLineRollupItem, error) {
	notes, err := s.store.ListVisibleHomeNotes(ctx, homeID, userID, false)
	if err != nil {
		return nil, err
	}
	return noteTagRollup(notes, tag), nil
}

func (s *cloudNotesService) findSharableNote(ctx context.Context, homeID string, ownerUserID string, noteID string) (domain.UserNote, error) {
	note, err := s.store.GetOwnedHomeNote(ctx, homeID, ownerUserID, noteID)
	if err == nil {
		return note, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.UserNote{}, err
	}
	note, err = s.store.GetProfileNote(ctx, ownerUserID, noteID)
	if err != nil {
		return domain.UserNote{}, err
	}
	if note.HomeID != "" && note.HomeID != homeID {
		return domain.UserNote{}, fmt.Errorf("note is already bound to a different home")
	}
	return note, nil
}

func (s *cloudNotesService) save(ctx context.Context, homeID string, actorUserID string, ownerUserID string, noteID string, request protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error) {
	var (
		existing domain.UserNote
		err      error
	)

	switch {
	case noteID != "" && homeID != "":
		existing, err = s.store.GetHomeNoteVisibleToUser(ctx, homeID, actorUserID, noteID)
	case noteID != "":
		existing, err = s.store.GetProfileNote(ctx, ownerUserID, noteID)
	default:
		err = store.ErrNotFound
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return protocol.NotesSaveResponse{}, err
	}
	if err == nil && existing.DeletedAt == nil && request.ExpectedRevision != "" && request.ExpectedRevision != existing.Revision {
		current, currentErr := noteFetch(existing)
		if currentErr != nil {
			return protocol.NotesSaveResponse{}, currentErr
		}
		return protocol.NotesSaveResponse{}, &noteConflictError{Current: current}
	}

	if errors.Is(err, store.ErrNotFound) {
		resolvedNoteID, createErr := s.generateNoteID(ctx, ownerUserID, homeID, strings.TrimSpace(noteID), request.Title)
		if createErr != nil {
			return protocol.NotesSaveResponse{}, createErr
		}
		existing = domain.UserNote{
			ID:          newID("note"),
			NoteID:      resolvedNoteID,
			OwnerUserID: ownerUserID,
			HomeID:      homeID,
			CreatedAt:   nowUTC(),
		}
	} else if existing.OwnerUserID != actorUserID && homeID == "" {
		return protocol.NotesSaveResponse{}, store.ErrNotFound
	}

	pageType := normalizePageType(request.PageType)
	if pageType == "" && existing.PageType != "" {
		pageType = existing.PageType
	}
	if pageType == "" {
		pageType = protocol.NotePageTypeText
	}

	parentID := existing.ParentID
	if request.ParentID != nil {
		parentID = strings.TrimSpace(*request.ParentID)
	}
	if err := s.validateNotebookParent(ctx, homeID, actorUserID, existing, pageType, parentID); err != nil {
		return protocol.NotesSaveResponse{}, err
	}

	_, board, err := encodeBoard(pageType, request.Board)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	content := request.Content
	if request.BodyMarkdown != "" {
		content = request.BodyMarkdown
	}
	if pageType == protocol.NotePageTypeNotebook {
		content = ""
	}
	if pageType == protocol.NotePageTypeKanban && content == "" && board != nil {
		content = kanbanMarkdown(request.Title, *board)
	}

	title := strings.TrimSpace(request.Title)
	if title == "" {
		if derived := titleFromContent(content); derived != "" {
			title = derived
		} else if existing.Title != "" {
			title = existing.Title
		} else {
			title = titleFromNoteID(existing.NoteID)
		}
	}

	state := collabState{
		Title:         collabScalar{Value: title, Version: existing.CollabVersion + 1, UserID: actorUserID},
		PageType:      collabScalar{Value: pageType, Version: existing.CollabVersion + 1, UserID: actorUserID},
		ParentID:      collabScalar{Value: parentID, Version: existing.CollabVersion, UserID: actorUserID},
		Pinned:        existing.Pinned,
		SortOrder:     existing.SortOrder,
		Content:       content,
		Board:         board,
		CollabVersion: existing.CollabVersion + 1,
	}
	if request.ParentID != nil {
		state.ParentID = collabScalar{Value: parentID, Version: state.CollabVersion, UserID: actorUserID}
	}
	if request.SortOrder != nil {
		state.SortOrder = *request.SortOrder
	}
	if request.Pinned != nil {
		state.Pinned = *request.Pinned
	}
	if parentID == "" || pageType == protocol.NotePageTypeNotebook {
		state.Pinned = false
	}
	now := nowUTC()
	updated, operationJSON, err := materializeNoteFromState(existing, state, actorUserID, now)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	updated.MCPExcluded = existing.MCPExcluded
	if request.MCPExcluded != nil {
		updated.MCPExcluded = *request.MCPExcluded
	}
	if err := s.store.SaveUserNoteWithOperations(ctx, updated, []domain.NoteOperation{{
		NoteID:         updated.ID,
		OpID:           "http-save:" + updated.Revision,
		ActorUserID:    actorUserID,
		SessionID:      "http",
		BaseVersion:    existing.CollabVersion,
		AppliedVersion: updated.CollabVersion,
		OpJSON:         operationJSON,
		CreatedAt:      now,
	}}); err != nil {
		return protocol.NotesSaveResponse{}, err
	}

	return protocol.NotesSaveResponse{
		NoteID:    updated.NoteID,
		Revision:  updated.Revision,
		UpdatedAt: updated.UpdatedAt,
		PageType:  updated.PageType,
	}, nil
}

func (s *cloudNotesService) validateNotebookParent(ctx context.Context, homeID string, actorUserID string, note domain.UserNote, pageType string, parentID string) error {
	if parentID == "" {
		return nil
	}
	if pageType == protocol.NotePageTypeNotebook {
		return errNotebookCannotHaveParent
	}
	if parentID == note.NoteID {
		return errInvalidNotebookParent
	}

	var (
		parent domain.UserNote
		err    error
	)
	if homeID != "" {
		parent, err = s.store.GetHomeNoteVisibleToUser(ctx, homeID, actorUserID, parentID)
	} else {
		parent, err = s.store.GetProfileNote(ctx, note.OwnerUserID, parentID)
	}
	if err != nil {
		return err
	}
	if parent.DeletedAt != nil {
		return store.ErrNotFound
	}
	if parent.OwnerUserID != note.OwnerUserID || normalizePageType(parent.PageType) != protocol.NotePageTypeNotebook {
		return errInvalidNotebookParent
	}
	return nil
}

func (s *cloudNotesService) generateNoteID(ctx context.Context, ownerUserID string, homeID string, requested string, title string) (string, error) {
	candidate := strings.TrimSpace(requested)
	if candidate == "" {
		candidate = slugify(title)
		if candidate == "" {
			candidate = "note"
		}
		candidate += ".md"
	}
	ext := ""
	if !strings.Contains(candidate, ".") {
		ext = ".md"
	}
	base := strings.TrimSuffix(candidate, ext)
	if base == "" {
		base = "note"
	}
	for attempt := 0; attempt < 1000; attempt++ {
		current := candidate
		if attempt > 0 {
			current = fmt.Sprintf("%s-%d%s", base, attempt, ext)
		}
		if _, err := s.store.GetProfileNote(ctx, ownerUserID, current); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if homeID != "" {
			if _, err := s.store.GetHomeNoteByKey(ctx, homeID, current); err == nil {
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return "", err
			}
		}
		return current, nil
	}
	return "", fmt.Errorf("unable to allocate note id")
}

func noteSummaries(notes []domain.UserNote) []protocol.NoteSummary {
	summaries := make([]protocol.NoteSummary, 0, len(notes))
	for _, note := range notes {
		if note.DeletedAt != nil {
			continue
		}
		summaries = append(summaries, noteSummary(note))
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return strings.ToLower(summaries[i].Title) < strings.ToLower(summaries[j].Title)
		}
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries
}

func deletedNoteSummaries(notes []domain.UserNote) []protocol.NoteSummary {
	summaries := make([]protocol.NoteSummary, 0, len(notes))
	for _, note := range notes {
		if note.DeletedAt == nil {
			continue
		}
		summaries = append(summaries, noteSummary(note))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].DeletedAt.After(*summaries[j].DeletedAt)
	})
	return summaries
}

func noteSummary(note domain.UserNote) protocol.NoteSummary {
	return protocol.NoteSummary{
		ID:          note.NoteID,
		Title:       note.Title,
		UpdatedAt:   note.UpdatedAt,
		Revision:    note.Revision,
		Size:        int64(len(noteBodyText(note))),
		StorageKey:  note.NoteID,
		PageType:    note.PageType,
		ParentID:    note.ParentID,
		MCPExcluded: note.MCPExcluded,
		Pinned:      note.Pinned,
		SortOrder:   note.SortOrder,
		BodyFormat:  noteBodyFormat(note),
		OwnerUserID: note.OwnerUserID,
		Shared:      note.HomeID != "",
		Preview:     previewFromContent(noteBodyText(note)),
		Tags:        extractTags(noteBodyText(note)),
		DeletedAt:   note.DeletedAt,
	}
}

func noteFetch(note domain.UserNote) (protocol.NotesFetchResponse, error) {
	var board *protocol.KanbanBoard
	if strings.TrimSpace(note.BoardJSON) != "" {
		var decoded protocol.KanbanBoard
		if err := json.Unmarshal([]byte(note.BoardJSON), &decoded); err != nil {
			return protocol.NotesFetchResponse{}, err
		}
		board = &decoded
	}
	return protocol.NotesFetchResponse{
		NoteID:       note.NoteID,
		Title:        note.Title,
		Content:      noteBodyText(note),
		BodyMarkdown: noteBodyText(note),
		BodyFormat:   noteBodyFormat(note),
		Revision:     note.Revision,
		UpdatedAt:    note.UpdatedAt,
		PageType:     note.PageType,
		ParentID:     note.ParentID,
		MCPExcluded:  note.MCPExcluded,
		Pinned:       note.Pinned,
		SortOrder:    note.SortOrder,
		OwnerUserID:  note.OwnerUserID,
		Shared:       note.HomeID != "",
		Preview:      previewFromContent(noteBodyText(note)),
		Tags:         extractTags(noteBodyText(note)),
		Board:        board,
	}, nil
}

func noteBodyText(note domain.UserNote) string {
	if note.BodyMarkdown != "" {
		return note.BodyMarkdown
	}
	return note.Content
}

func noteBodyFormat(note domain.UserNote) string {
	if strings.TrimSpace(note.BodyFormat) != "" {
		return note.BodyFormat
	}
	return "markdown"
}

func appendRequestForNote(note domain.UserNote, request protocol.NotesAppendRequest) (protocol.NotesSaveRequest, error) {
	if normalizePageType(note.PageType) != protocol.NotePageTypeText {
		return protocol.NotesSaveRequest{}, errNoteAppendUnsupportedPageType
	}
	content, err := appendNoteContent(noteBodyText(note), request)
	if err != nil {
		return protocol.NotesSaveRequest{}, err
	}
	return protocol.NotesSaveRequest{
		NoteID:           note.NoteID,
		Title:            note.Title,
		Content:          content,
		BodyMarkdown:     content,
		BodyFormat:       noteBodyFormat(note),
		ExpectedRevision: request.ExpectedRevision,
		PageType:         protocol.NotePageTypeText,
		MCPExcluded:      boolPointer(note.MCPExcluded),
		Pinned:           boolPointer(note.Pinned),
	}, nil
}

func boolPointer(value bool) *bool { return &value }

func appendNoteContent(existing string, request protocol.NotesAppendRequest) (string, error) {
	addition := request.BodyMarkdown
	if addition == "" {
		addition = request.Content
	}
	if addition == "" {
		return "", errNoteAppendContentRequired
	}
	separator := "\n"
	if request.Separator != nil {
		separator = *request.Separator
	}
	if existing == "" {
		separator = ""
	}
	return existing + separator + addition, nil
}

func decodeCollabState(note domain.UserNote) (collabState, error) {
	if strings.TrimSpace(note.CRDTStateJSON) != "" {
		var state collabState
		if err := json.Unmarshal([]byte(note.CRDTStateJSON), &state); err == nil {
			if state.PageType.Value == "" {
				state.PageType.Value = normalizePageType(note.PageType)
			}
			if state.ParentID.Value == "" {
				state.ParentID.Value = note.ParentID
			}
			if state.Title.Value == "" {
				state.Title.Value = note.Title
			}
			if state.Content == "" {
				state.Content = noteBodyText(note)
			}
			if state.SortOrder == 0 {
				state.SortOrder = note.SortOrder
			}
			if !state.Pinned {
				state.Pinned = note.Pinned
			}
			if state.Board == nil && strings.TrimSpace(note.BoardJSON) != "" {
				var board protocol.KanbanBoard
				if err := json.Unmarshal([]byte(note.BoardJSON), &board); err == nil {
					state.Board = &board
				}
			}
			if state.CollabVersion < note.CollabVersion {
				state.CollabVersion = note.CollabVersion
			}
			return state, nil
		}
	}
	var board *protocol.KanbanBoard
	if strings.TrimSpace(note.BoardJSON) != "" {
		var decoded protocol.KanbanBoard
		if err := json.Unmarshal([]byte(note.BoardJSON), &decoded); err == nil {
			board = &decoded
		}
	}
	return collabState{
		Title:         collabScalar{Value: note.Title, Version: note.CollabVersion},
		PageType:      collabScalar{Value: normalizePageType(note.PageType), Version: note.CollabVersion},
		ParentID:      collabScalar{Value: note.ParentID, Version: note.CollabVersion},
		Pinned:        note.Pinned,
		SortOrder:     note.SortOrder,
		Content:       noteBodyText(note),
		Board:         board,
		CollabVersion: note.CollabVersion,
	}, nil
}

func encodeCollabState(state collabState) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func materializeNoteFromState(base domain.UserNote, state collabState, updatedBy string, updatedAt time.Time) (domain.UserNote, string, error) {
	pageType := normalizePageType(state.PageType.Value)
	boardJSON, board, err := encodeBoard(pageType, state.Board)
	if err != nil {
		return domain.UserNote{}, "", err
	}
	content := state.Content
	if strings.TrimSpace(state.ParentID.Value) == "" || pageType == protocol.NotePageTypeNotebook {
		state.Pinned = false
	}
	if pageType == protocol.NotePageTypeNotebook {
		content = ""
	}
	if pageType == protocol.NotePageTypeKanban && content == "" && board != nil {
		content = kanbanMarkdown(state.Title.Value, *board)
	}
	_, checksum, err := revisionAndChecksum(content, pageType, boardJSON)
	if err != nil {
		return domain.UserNote{}, "", err
	}
	revisionPayload, err := json.Marshal(map[string]any{
		"content":    content,
		"page_type":  pageType,
		"board":      boardJSON,
		"title":      strings.TrimSpace(state.Title.Value),
		"parent_id":  strings.TrimSpace(state.ParentID.Value),
		"pinned":     state.Pinned,
		"sort_order": state.SortOrder,
	})
	if err != nil {
		return domain.UserNote{}, "", err
	}
	revision := revisionBytes(revisionPayload)
	base.Title = strings.TrimSpace(state.Title.Value)
	if base.Title == "" {
		base.Title = titleFromNoteID(base.NoteID)
	}
	base.ParentID = strings.TrimSpace(state.ParentID.Value)
	base.Pinned = state.Pinned
	base.SortOrder = state.SortOrder
	base.Content = content
	base.BodyMarkdown = content
	base.BodyFormat = "markdown"
	base.PageType = pageType
	base.BoardJSON = boardJSON
	base.Revision = revision
	base.Checksum = checksum
	base.CRDTStateJSON = encodeCollabState(state)
	base.CollabVersion = state.CollabVersion
	base.UpdatedAt = updatedAt
	base.UpdatedBy = updatedBy
	if base.CreatedAt.IsZero() {
		base.CreatedAt = updatedAt
	}
	operationJSON, err := json.Marshal(map[string]any{
		"type":          "replace_snapshot",
		"title":         base.Title,
		"content":       base.Content,
		"body_markdown": base.BodyMarkdown,
		"page_type":     base.PageType,
		"parent_id":     base.ParentID,
		"pinned":        base.Pinned,
		"sort_order":    base.SortOrder,
		"board":         state.Board,
	})
	if err != nil {
		return domain.UserNote{}, "", err
	}
	return base, string(operationJSON), nil
}

func encodeBoard(pageType string, board *protocol.KanbanBoard) (string, *protocol.KanbanBoard, error) {
	if pageType != protocol.NotePageTypeKanban {
		return "", nil, nil
	}
	value := protocol.KanbanBoard{}
	if board != nil {
		value = *board
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), &value, nil
}

func revisionAndChecksum(content string, pageType string, boardJSON string) (string, string, error) {
	revisionPayload := map[string]any{
		"content":   content,
		"page_type": pageType,
		"board":     boardJSON,
	}
	encoded, err := json.Marshal(revisionPayload)
	if err != nil {
		return "", "", err
	}
	return revisionBytes(encoded), revisionBytes([]byte(content)), nil
}

func revisionBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizePageType(pageType string) string {
	pageType = strings.TrimSpace(strings.ToLower(pageType))
	switch pageType {
	case "", protocol.NotePageTypeText:
		return protocol.NotePageTypeText
	case protocol.NotePageTypeKanban, "board":
		return protocol.NotePageTypeKanban
	case protocol.NotePageTypeNotebook:
		return protocol.NotePageTypeNotebook
	default:
		return protocol.NotePageTypeText
	}
}

func titleFromContent(content string) string {
	plain := strings.TrimSpace(content)
	if plain == "" {
		return ""
	}
	plain = strings.TrimSpace(strings.TrimLeft(plain, "#>*-+0123456789.[] xX\t "))
	if plain == "" {
		return ""
	}
	words := strings.Fields(plain)
	if len(words) > 5 {
		words = words[:5]
	}
	title := strings.Join(words, " ")
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	return title
}

func titleFromNoteID(noteID string) string {
	name := noteID
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[:index]
	}
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")
	if name == "" {
		return "Note"
	}
	return strings.Title(name)
}

func previewFromContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 140 {
				return line[:140]
			}
			return line
		}
	}
	return ""
}

func searchNotes(notes []domain.UserNote, query string, limit int, parentID string) []protocol.NoteSearchResult {
	needle := strings.TrimSpace(query)
	if needle == "" {
		return []protocol.NoteSearchResult{}
	}
	if limit <= 0 {
		limit = 50
	}
	loweredNeedle := strings.ToLower(needle)
	parentID = strings.TrimSpace(parentID)
	results := make([]protocol.NoteSearchResult, 0)
	for _, note := range notes {
		if parentID != "" && note.ParentID != parentID {
			continue
		}
		titleMatch := strings.Index(strings.ToLower(note.Title), loweredNeedle)
		body := noteBodyText(note)
		bodyMatch := -1
		if normalizePageType(note.PageType) != protocol.NotePageTypeNotebook {
			bodyMatch = strings.Index(strings.ToLower(body), loweredNeedle)
		}
		if titleMatch < 0 && bodyMatch < 0 {
			continue
		}
		preview := note.Title
		matchLocation := 0
		lineIndex := 0
		if bodyMatch >= 0 {
			preview = snippetAround(body, bodyMatch, len(needle))
			matchLocation = bodyMatch
			lineIndex = strings.Count(body[:bodyMatch], "\n")
		}
		results = append(results, protocol.NoteSearchResult{
			NoteID:        note.NoteID,
			Title:         note.Title,
			PageType:      note.PageType,
			ParentID:      note.ParentID,
			Preview:       preview,
			MatchLocation: matchLocation,
			LineIndex:     lineIndex,
		})
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func noteTags(notes []domain.UserNote) []protocol.NoteTagCount {
	counts := map[string]int{}
	for _, note := range notes {
		if note.PageType != protocol.NotePageTypeText {
			continue
		}
		for _, tag := range extractTags(note.Content) {
			counts[tag]++
		}
	}
	tags := make([]protocol.NoteTagCount, 0, len(counts))
	for tag, count := range counts {
		tags = append(tags, protocol.NoteTagCount{Tag: tag, Count: count})
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Count == tags[j].Count {
			return tags[i].Tag < tags[j].Tag
		}
		return tags[i].Count > tags[j].Count
	})
	return tags
}

func noteTagRollup(notes []domain.UserNote, tag string) []protocol.TaggedLineRollupItem {
	normalizedTag := normalizeTag(tag)
	if normalizedTag == "" {
		return []protocol.TaggedLineRollupItem{}
	}
	items := make([]protocol.TaggedLineRollupItem, 0)
	for _, note := range notes {
		if note.PageType != protocol.NotePageTypeText {
			continue
		}
		for index, line := range extractTaggedLines(note.Content) {
			if !strings.EqualFold(line.tag, normalizedTag) {
				continue
			}
			items = append(items, protocol.TaggedLineRollupItem{
				NoteID:    note.NoteID,
				NoteTitle: note.Title,
				PageType:  note.PageType,
				Tag:       line.tag,
				LineText:  line.text,
				LineIndex: index,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if strings.EqualFold(items[i].NoteTitle, items[j].NoteTitle) {
			return items[i].LineIndex < items[j].LineIndex
		}
		return strings.ToLower(items[i].NoteTitle) < strings.ToLower(items[j].NoteTitle)
	})
	return items
}

func snippetAround(content string, offset int, needleLength int) string {
	if offset < 0 || offset >= len(content) {
		return previewFromContent(content)
	}
	start := offset - 40
	if start < 0 {
		start = 0
	}
	end := offset + needleLength + 40
	if end > len(content) {
		end = len(content)
	}
	return strings.TrimSpace(content[start:end])
}

func extractTags(content string) []string {
	seen := map[string]struct{}{}
	var tags []string
	for _, field := range strings.Fields(content) {
		normalized := normalizeTag(field)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		tags = append(tags, normalized)
	}
	sort.Strings(tags)
	return tags
}

type taggedLine struct {
	tag  string
	text string
}

func extractTaggedLines(content string) []taggedLine {
	lines := strings.Split(content, "\n")
	items := make([]taggedLine, 0)
	for _, line := range lines {
		for _, field := range strings.Fields(line) {
			tag := normalizeTag(field)
			if tag == "" {
				continue
			}
			items = append(items, taggedLine{tag: tag, text: strings.TrimSpace(line)})
		}
	}
	return items
}

func normalizeTag(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "#") || len(value) < 2 {
		return ""
	}
	value = strings.TrimRightFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-'
	})
	if len(value) < 2 {
		return ""
	}
	return strings.ToLower(value)
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			builder.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_':
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func kanbanMarkdown(title string, board protocol.KanbanBoard) string {
	if strings.TrimSpace(title) == "" {
		title = "Board"
	}
	lines := []string{"# " + title, ""}
	for _, column := range board.Columns {
		lines = append(lines, "## "+strings.TrimSpace(column.Title))
		if len(column.Cards) == 0 {
			lines = append(lines, "-")
			continue
		}
		for _, card := range column.Cards {
			lines = append(lines, "- "+strings.TrimSpace(card.Text))
		}
		lines = append(lines, "")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
