package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type homeSyncController struct {
	mu       sync.Mutex
	inFlight map[string]bool
}

func newHomeSyncController() *homeSyncController {
	return &homeSyncController{inFlight: make(map[string]bool)}
}

func (c *homeSyncController) Start(homeID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight[homeID] {
		return false
	}
	c.inFlight[homeID] = true
	return true
}

func (c *homeSyncController) Done(homeID string) {
	c.mu.Lock()
	delete(c.inFlight, homeID)
	c.mu.Unlock()
}

func (s *Server) scheduleHomeSync(home domain.Home, agentID string) {
	if !s.syncs.Start(home.ID) {
		return
	}

	go func() {
		defer s.syncs.Done(home.ID)

		timeout := s.requestTimeout * 4
		if timeout < 45*time.Second {
			timeout = 45 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		s.reconcileHomeNotes(ctx, home, agentID)
	}()
}

func (s *Server) reconcileHomeNotes(ctx context.Context, home domain.Home, agentID string) {
	now := time.Now().UTC()
	state := domain.HomeNoteSyncState{
		HomeID:         home.ID,
		AgentID:        agentID,
		LastManifestAt: &now,
		Status:         domain.SyncStatusHealthy,
	}

	response, err := s.sendAgentCommand(ctx, home.ID, "notes.sync", nil)
	if err != nil {
		state.Status = domain.SyncStatusOffline
		state.LastError = err.Error()
		_ = s.store.UpsertHomeNoteSyncState(ctx, state)
		return
	}
	if response.Error != nil {
		state.Status = domain.SyncStatusDegraded
		state.LastError = response.Error.Message
		_ = s.store.UpsertHomeNoteSyncState(ctx, state)
		return
	}

	manifest, err := protocol.DecodePayload[protocol.NotesSyncResponse](response)
	if err != nil {
		state.Status = domain.SyncStatusDegraded
		state.LastError = err.Error()
		_ = s.store.UpsertHomeNoteSyncState(ctx, state)
		return
	}

	cloudNotes, err := s.store.ListSyncedHomeNotes(ctx, home.ID, true)
	if err != nil {
		state.Status = domain.SyncStatusDegraded
		state.LastError = err.Error()
		_ = s.store.UpsertHomeNoteSyncState(ctx, state)
		return
	}

	localByID := make(map[string]protocol.NoteSummary, len(manifest.Notes))
	for _, note := range manifest.Notes {
		localByID[note.ID] = note
	}
	cloudByID := make(map[string]domain.UserNote, len(cloudNotes))
	for _, note := range cloudNotes {
		cloudByID[note.NoteID] = note
	}

	var mismatchCount int
	var failedPulls int
	var failedPushes int
	var lastPullAt *time.Time
	var lastPushAt *time.Time
	var lastErr string

	for _, cloudNote := range cloudNotes {
		local, hasLocal := localByID[cloudNote.NoteID]
		if cloudNote.DeletedAt != nil {
			if hasLocal {
				mismatchCount++
				if err := s.pushDelete(ctx, home.ID, cloudNote.NoteID); err != nil {
					failedPushes++
					if lastErr == "" {
						lastErr = err.Error()
					}
				} else {
					timestamp := time.Now().UTC()
					lastPushAt = &timestamp
				}
			}
			delete(localByID, cloudNote.NoteID)
			continue
		}

		if !hasLocal {
			mismatchCount++
			if err := s.pushSave(ctx, home.ID, cloudNote); err != nil {
				failedPushes++
				if lastErr == "" {
					lastErr = err.Error()
				}
			} else {
				timestamp := time.Now().UTC()
				lastPushAt = &timestamp
			}
			continue
		}

		if local.Revision == cloudNote.Revision && local.UpdatedAt.Equal(cloudNote.UpdatedAt) {
			delete(localByID, cloudNote.NoteID)
			continue
		}

		mismatchCount++
		if local.UpdatedAt.After(cloudNote.UpdatedAt) {
			if err := s.pullLocalNote(ctx, home, cloudNote.NoteID); err != nil {
				failedPulls++
				if lastErr == "" {
					lastErr = err.Error()
				}
			} else {
				timestamp := time.Now().UTC()
				lastPullAt = &timestamp
			}
		} else {
			if err := s.pushSave(ctx, home.ID, cloudNote); err != nil {
				failedPushes++
				if lastErr == "" {
					lastErr = err.Error()
				}
			} else {
				timestamp := time.Now().UTC()
				lastPushAt = &timestamp
			}
		}
		delete(localByID, cloudNote.NoteID)
	}

	for noteID := range localByID {
		mismatchCount++
		if err := s.pullLocalNote(ctx, home, noteID); err != nil {
			failedPulls++
			if lastErr == "" {
				lastErr = err.Error()
			}
		} else {
			timestamp := time.Now().UTC()
			lastPullAt = &timestamp
		}
	}

	state.PendingPullCount = failedPulls
	state.PendingPushCount = failedPushes
	state.LastPullAt = lastPullAt
	state.LastPushAt = lastPushAt
	state.LastError = lastErr
	if failedPulls == 0 && failedPushes == 0 {
		state.Status = domain.SyncStatusHealthy
		success := time.Now().UTC()
		state.LastSuccessfulSyncAt = &success
	} else if mismatchCount > 0 {
		state.Status = domain.SyncStatusOutOfSync
	} else {
		state.Status = domain.SyncStatusDegraded
	}
	_ = s.store.UpsertHomeNoteSyncState(ctx, state)
	s.emitSyncStatus(ctx, home.ID)
	s.emitHomeNotesChanged(ctx, "notes.changed", map[string]any{"home_id": home.ID})
}

func (s *Server) pushDelete(ctx context.Context, homeID string, noteID string) error {
	response, err := s.sendAgentCommand(ctx, homeID, "notes.delete", protocol.NotesDeleteRequest{NoteID: noteID})
	if err != nil {
		return err
	}
	if response.Error != nil && response.Error.Code != "not_found" {
		return errors.New(response.Error.Message)
	}
	return nil
}

func (s *Server) pushSave(ctx context.Context, homeID string, note domain.UserNote) error {
	var board *protocol.KanbanBoard
	if strings.TrimSpace(note.BoardJSON) != "" {
		var decoded protocol.KanbanBoard
		if err := json.Unmarshal([]byte(note.BoardJSON), &decoded); err != nil {
			return err
		}
		board = &decoded
	}
	response, err := s.sendAgentCommand(ctx, homeID, "notes.save", protocol.NotesSaveRequest{
		NoteID:   note.NoteID,
		Title:    note.Title,
		Content:  note.Content,
		PageType: note.PageType,
		Board:    board,
	})
	if err != nil {
		return err
	}
	if response.Error != nil {
		return errors.New(response.Error.Message)
	}
	return nil
}

func (s *Server) pullLocalNote(ctx context.Context, home domain.Home, noteID string) error {
	response, err := s.sendAgentCommand(ctx, home.ID, "notes.fetch", protocol.NotesFetchRequest{NoteID: noteID})
	if err != nil {
		return err
	}
	if response.Error != nil {
		return errors.New(response.Error.Message)
	}
	fetched, err := protocol.DecodePayload[protocol.NotesFetchResponse](response)
	if err != nil {
		return err
	}
	boardJSON, _, err := encodeBoard(fetched.PageType, fetched.Board)
	if err != nil {
		return err
	}
	revision, checksum, err := revisionAndChecksum(fetched.Content, fetched.PageType, boardJSON)
	if err != nil {
		return err
	}
	ownerNote, err := s.notes.SaveHome(ctx, home.ID, home.UserID, fetched.NoteID, protocol.NotesSaveRequest{
		NoteID:   fetched.NoteID,
		Title:    fetched.Title,
		Content:  fetched.Content,
		PageType: fetched.PageType,
		Board:    fetched.Board,
	})
	if err != nil {
		return err
	}
	note, err := s.store.GetOwnedHomeNote(ctx, home.ID, home.UserID, ownerNote.NoteID)
	if err != nil {
		return err
	}
	members, err := s.store.ListHomeMembers(ctx, home.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, member := range members {
		if member.UserID == home.UserID {
			continue
		}
		if err := s.store.AddNoteShare(ctx, domain.NoteShare{
			NoteID:       note.ID,
			HomeID:       home.ID,
			TargetUserID: member.UserID,
			SharedBy:     home.UserID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}); err != nil {
			return err
		}
	}
	return s.store.UpsertUserNote(ctx, domain.UserNote{
		ID:            note.ID,
		NoteID:        note.NoteID,
		OwnerUserID:   note.OwnerUserID,
		HomeID:        home.ID,
		Title:         fetched.Title,
		Content:       fetched.Content,
		PageType:      fetched.PageType,
		BoardJSON:     boardJSON,
		Revision:      revision,
		Checksum:      checksum,
		CRDTStateJSON: note.CRDTStateJSON,
		CollabVersion: note.CollabVersion,
		CreatedAt:     note.CreatedAt,
		UpdatedAt:     fetched.UpdatedAt,
		UpdatedBy:     home.UserID,
	})
}

func (s *Server) markHomeSyncOffline(ctx context.Context, homeID string, agentID string) {
	now := time.Now().UTC()
	state, err := s.store.GetHomeNoteSyncState(ctx, homeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return
	}
	state.HomeID = homeID
	state.AgentID = agentID
	state.Status = domain.SyncStatusOffline
	state.LastError = "agent offline"
	state.LastManifestAt = &now
	_ = s.store.UpsertHomeNoteSyncState(ctx, state)
	s.emitSyncStatus(ctx, homeID)
}

func (s *Server) markHomeNotesDirty(ctx context.Context, homeID string, agentID string) {
	state, err := s.store.GetHomeNoteSyncState(ctx, homeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return
	}

	state.HomeID = homeID
	state.AgentID = agentID
	if state.AgentID == "" {
		if agentConn, ok := s.router.GetAgent(homeID); ok {
			state.AgentID = agentConn.agent.ID
		}
	}
	if _, ok := s.router.GetAgent(homeID); ok {
		state.Status = domain.SyncStatusOutOfSync
	} else {
		state.Status = domain.SyncStatusOffline
		state.LastError = "agent offline"
	}
	state.PendingPushCount++
	state.PendingPullCount = 0
	now := time.Now().UTC()
	state.LastManifestAt = &now
	_ = s.store.UpsertHomeNoteSyncState(ctx, state)
	s.emitSyncStatus(ctx, homeID)
	s.emitHomeNotesChanged(ctx, "notes.changed", map[string]any{"home_id": homeID})
	if agentConn, ok := s.router.GetAgent(homeID); ok {
		if home, err := s.store.GetHomeByID(ctx, homeID); err == nil {
			go s.scheduleHomeSync(home, agentConn.agent.ID)
		}
	}
}
