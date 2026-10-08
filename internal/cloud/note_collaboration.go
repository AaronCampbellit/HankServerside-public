package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type noteCollaborationHub struct {
	store *store.Store

	mu     sync.Mutex
	byNote map[string]map[string]*noteCollabSession
}

type noteCollabSession struct {
	app             *appConnection
	scope           string
	homeID          string
	noteInternalID  string
	noteKey         string
	collabSessionID string
	joinedAt        time.Time
}

func newNoteCollaborationHub(db *store.Store) *noteCollaborationHub {
	return &noteCollaborationHub{
		store:  db,
		byNote: make(map[string]map[string]*noteCollabSession),
	}
}

func (h *noteCollaborationHub) join(ctx context.Context, scope string, homeID string, noteKey string, collabSessionID string, app *appConnection) (protocol.NoteCollaborationSnapshot, error) {
	scope = normalizeCollabScope(scope)
	note, err := h.resolveNote(ctx, scope, homeID, app.userID, noteKey)
	if err != nil {
		return protocol.NoteCollaborationSnapshot{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NoteCollaborationSnapshot{}, store.ErrNotFound
	}

	session := &noteCollabSession{
		app:             app,
		scope:           scope,
		homeID:          homeID,
		noteInternalID:  note.ID,
		noteKey:         note.NoteID,
		collabSessionID: collabSessionID,
		joinedAt:        nowUTC(),
	}

	key := h.sessionKey(app.sessionID, collabSessionID)
	h.mu.Lock()
	if h.byNote[note.ID] == nil {
		h.byNote[note.ID] = make(map[string]*noteCollabSession)
	}
	h.byNote[note.ID][key] = session
	presence := h.presenceLocked(note.ID)
	peers := h.notePeersLocked(note.ID)
	h.mu.Unlock()

	snapshot, err := h.snapshot(note, presence)
	if err != nil {
		return protocol.NoteCollaborationSnapshot{}, err
	}
	h.broadcastPresence(ctx, peers, scope, note.NoteID, presence)
	return snapshot, nil
}

func (h *noteCollaborationHub) leave(ctx context.Context, scope string, homeID string, noteKey string, collabSessionID string, app *appConnection) error {
	scope = normalizeCollabScope(scope)
	note, err := h.resolveNote(ctx, scope, homeID, app.userID, noteKey)
	if err != nil {
		return err
	}
	key := h.sessionKey(app.sessionID, collabSessionID)
	h.mu.Lock()
	if sessions := h.byNote[note.ID]; sessions != nil {
		delete(sessions, key)
		if len(sessions) == 0 {
			delete(h.byNote, note.ID)
		}
	}
	presence := h.presenceLocked(note.ID)
	peers := h.notePeersLocked(note.ID)
	h.mu.Unlock()
	h.broadcastPresence(ctx, peers, scope, note.NoteID, presence)
	return nil
}

func (h *noteCollaborationHub) sync(ctx context.Context, scope string, homeID string, noteKey string, userID string, afterVersion int64, maxOperations int) (any, error) {
	scope = normalizeCollabScope(scope)
	note, err := h.resolveNote(ctx, scope, homeID, userID, noteKey)
	if err != nil {
		return nil, err
	}
	if note.DeletedAt != nil {
		return nil, store.ErrNotFound
	}

	oldestVersion, err := h.store.GetOldestNoteOperationVersion(ctx, note.ID)
	if err != nil {
		return nil, err
	}
	if oldestVersion > 0 && afterVersion < oldestVersion-1 {
		return h.snapshot(note, h.presence(note.ID))
	}

	operations, err := h.store.ListNoteOperationsSince(ctx, note.ID, afterVersion, maxOperations)
	if err != nil {
		return nil, err
	}
	if len(operations) == 0 {
		return h.snapshot(note, h.presence(note.ID))
	}
	applied, err := decodeAppliedOperations(operations)
	if err != nil {
		return nil, err
	}
	return protocol.NoteCollaborationOpsEvent{
		NoteID:         note.NoteID,
		AppliedVersion: note.CollabVersion,
		Revision:       note.Revision,
		Ops:            applied,
	}, nil
}

func (h *noteCollaborationHub) submitOps(ctx context.Context, homeID string, req protocol.NoteCollaborationSubmitOpsRequest, app *appConnection) (protocol.NoteCollaborationAck, error) {
	scope := normalizeCollabScope(req.Scope)
	note, err := h.resolveNote(ctx, scope, homeID, app.userID, req.NoteID)
	if err != nil {
		return protocol.NoteCollaborationAck{}, err
	}
	if note.DeletedAt != nil {
		return protocol.NoteCollaborationAck{}, store.ErrNotFound
	}

	state, err := decodeCollabState(note)
	if err != nil {
		return protocol.NoteCollaborationAck{}, err
	}

	var (
		appliedOps []protocol.NoteCollaborationAppliedOp
		opRecords  []domain.NoteOperation
	)
	now := nowUTC()
	for _, operation := range req.Ops {
		if strings.TrimSpace(operation.OpID) == "" {
			return protocol.NoteCollaborationAck{}, errors.New("op_id is required")
		}
		exists, err := h.store.HasNoteOperation(ctx, note.ID, operation.OpID)
		if err != nil {
			return protocol.NoteCollaborationAck{}, err
		}
		if exists {
			continue
		}
		state, err = applyCollaborationOperation(state, app.userID, operation)
		if err != nil {
			return protocol.NoteCollaborationAck{}, err
		}
		applied := protocol.NoteCollaborationAppliedOp{
			NoteCollaborationOperation: operation,
			ActorUserID:                app.userID,
			SessionID:                  req.SessionID,
			BaseVersion:                req.BaseVersion,
			AppliedVersion:             state.CollabVersion,
			CreatedAt:                  now,
		}
		appliedOps = append(appliedOps, applied)
		encoded, err := json.Marshal(operation)
		if err != nil {
			return protocol.NoteCollaborationAck{}, err
		}
		opRecords = append(opRecords, domain.NoteOperation{
			NoteID:         note.ID,
			OpID:           operation.OpID,
			ActorUserID:    app.userID,
			SessionID:      req.SessionID,
			BaseVersion:    req.BaseVersion,
			AppliedVersion: state.CollabVersion,
			OpJSON:         string(encoded),
			CreatedAt:      now,
		})
	}

	if len(appliedOps) == 0 {
		return protocol.NoteCollaborationAck{
			NoteID:         note.NoteID,
			SessionID:      req.SessionID,
			AppliedVersion: note.CollabVersion,
			AcceptedOps:    0,
			Revision:       note.Revision,
		}, nil
	}

	if err := h.validateNotebookParent(ctx, scope, homeID, app.userID, note, normalizePageType(state.PageType.Value), strings.TrimSpace(state.ParentID.Value)); err != nil {
		return protocol.NoteCollaborationAck{}, err
	}

	updated, _, err := materializeNoteFromState(note, state, app.userID, now)
	if err != nil {
		return protocol.NoteCollaborationAck{}, err
	}
	if err := h.store.SaveUserNoteWithOperations(ctx, updated, opRecords); err != nil {
		return protocol.NoteCollaborationAck{}, err
	}

	peers := h.notePeers(updated.ID)
	h.broadcastOps(ctx, peers, scope, updated.NoteID, updated.Revision, updated.CollabVersion, appliedOps)

	return protocol.NoteCollaborationAck{
		NoteID:         updated.NoteID,
		SessionID:      req.SessionID,
		AppliedVersion: updated.CollabVersion,
		AcceptedOps:    len(appliedOps),
		Revision:       updated.Revision,
	}, nil
}

func (h *noteCollaborationHub) validateNotebookParent(ctx context.Context, scope string, homeID string, actorUserID string, note domain.UserNote, pageType string, parentID string) error {
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
	if scope == "home" {
		parent, err = h.store.GetHomeNoteVisibleToUser(ctx, homeID, actorUserID, parentID)
	} else {
		parent, err = h.store.GetProfileNote(ctx, note.OwnerUserID, parentID)
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

func (h *noteCollaborationHub) revokeUser(homeID string, userID string, reason string) {
	h.mu.Lock()
	var revoked []*noteCollabSession
	for noteID, sessions := range h.byNote {
		for key, session := range sessions {
			if session.homeID == homeID && session.app.userID == userID {
				revoked = append(revoked, session)
				delete(sessions, key)
			}
		}
		if len(sessions) == 0 {
			delete(h.byNote, noteID)
		}
	}
	h.mu.Unlock()

	for _, session := range revoked {
		_ = writeAppEvent(context.Background(), session.app.peer, "notes.collab.revoked", noteCollabTopic(session.noteKey), protocol.NoteCollaborationRevokedEvent{
			NoteID: session.noteKey,
			Reason: reason,
		})
	}
}

func (h *noteCollaborationHub) revokeNoteUser(noteInternalID string, userID string, reason string) {
	h.mu.Lock()
	var revoked []*noteCollabSession
	if sessions := h.byNote[noteInternalID]; sessions != nil {
		for key, session := range sessions {
			if session.app.userID == userID {
				revoked = append(revoked, session)
				delete(sessions, key)
			}
		}
		if len(sessions) == 0 {
			delete(h.byNote, noteInternalID)
		}
	}
	h.mu.Unlock()

	for _, session := range revoked {
		_ = writeAppEvent(context.Background(), session.app.peer, "notes.collab.revoked", noteCollabTopic(session.noteKey), protocol.NoteCollaborationRevokedEvent{
			NoteID: session.noteKey,
			Reason: reason,
		})
	}
}

func (h *noteCollaborationHub) removeApp(appSessionID string) {
	h.mu.Lock()
	type affectedNote struct {
		noteKey string
		scope   string
	}
	affected := map[string]affectedNote{}
	for noteID, sessions := range h.byNote {
		for key, session := range sessions {
			if session.app.sessionID == appSessionID {
				affected[noteID] = affectedNote{noteKey: session.noteKey, scope: session.scope}
				delete(sessions, key)
			}
		}
		if len(sessions) == 0 {
			delete(h.byNote, noteID)
		}
	}
	type presenceBroadcast struct {
		noteID   string
		noteKey  string
		scope    string
		peers    []*appConnection
		presence []protocol.NoteCollaborationPresenceUser
	}
	var broadcasts []presenceBroadcast
	for noteID, affected := range affected {
		broadcasts = append(broadcasts, presenceBroadcast{
			noteID:   noteID,
			noteKey:  affected.noteKey,
			scope:    affected.scope,
			peers:    h.notePeersLocked(noteID),
			presence: h.presenceLocked(noteID),
		})
	}
	h.mu.Unlock()

	for _, broadcast := range broadcasts {
		h.broadcastPresence(context.Background(), broadcast.peers, broadcast.scope, broadcast.noteKey, broadcast.presence)
	}
}

func (h *noteCollaborationHub) presence(noteID string) []protocol.NoteCollaborationPresenceUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.presenceLocked(noteID)
}

func (h *noteCollaborationHub) notePeers(noteID string) []*appConnection {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notePeersLocked(noteID)
}

func (h *noteCollaborationHub) presenceLocked(noteID string) []protocol.NoteCollaborationPresenceUser {
	sessions := h.byNote[noteID]
	presence := make([]protocol.NoteCollaborationPresenceUser, 0, len(sessions))
	for _, session := range sessions {
		presence = append(presence, protocol.NoteCollaborationPresenceUser{
			UserID:    session.app.userID,
			SessionID: session.collabSessionID,
			JoinedAt:  session.joinedAt,
		})
	}
	sort.Slice(presence, func(i, j int) bool {
		if presence[i].UserID == presence[j].UserID {
			return presence[i].SessionID < presence[j].SessionID
		}
		return presence[i].UserID < presence[j].UserID
	})
	return presence
}

func (h *noteCollaborationHub) notePeersLocked(noteID string) []*appConnection {
	sessions := h.byNote[noteID]
	peers := make([]*appConnection, 0, len(sessions))
	seen := map[string]struct{}{}
	for _, session := range sessions {
		if _, ok := seen[session.app.sessionID]; ok {
			continue
		}
		seen[session.app.sessionID] = struct{}{}
		peers = append(peers, session.app)
	}
	return peers
}

func (h *noteCollaborationHub) sessionKey(appSessionID string, collabSessionID string) string {
	return appSessionID + ":" + collabSessionID
}

func (h *noteCollaborationHub) snapshot(note domain.UserNote, presence []protocol.NoteCollaborationPresenceUser) (protocol.NoteCollaborationSnapshot, error) {
	fetch, err := noteFetch(note)
	if err != nil {
		return protocol.NoteCollaborationSnapshot{}, err
	}
	return protocol.NoteCollaborationSnapshot{
		NoteID:         note.NoteID,
		AppliedVersion: note.CollabVersion,
		Revision:       note.Revision,
		Note:           fetch,
		Presence:       presence,
	}, nil
}

func (h *noteCollaborationHub) broadcastPresence(ctx context.Context, peers []*appConnection, scope string, noteID string, presence []protocol.NoteCollaborationPresenceUser) {
	for _, peer := range peers {
		_ = writeAppEvent(ctx, peer.peer, "notes.collab.presence", scopedNoteCollabTopic(scope, noteID), protocol.NoteCollaborationPresenceEvent{
			NoteID:   noteID,
			Presence: presence,
		})
		if normalizeCollabScope(scope) == "home" {
			_ = writeAppEvent(ctx, peer.peer, "notes.collab.presence", noteCollabTopic(noteID), protocol.NoteCollaborationPresenceEvent{
				NoteID:   noteID,
				Presence: presence,
			})
		}
	}
}

func (h *noteCollaborationHub) broadcastOps(ctx context.Context, peers []*appConnection, scope string, noteID string, revision string, version int64, ops []protocol.NoteCollaborationAppliedOp) {
	for _, peer := range peers {
		payload := protocol.NoteCollaborationOpsEvent{
			NoteID:         noteID,
			AppliedVersion: version,
			Revision:       revision,
			Ops:            ops,
		}
		_ = writeAppEvent(ctx, peer.peer, "notes.collab.ops", scopedNoteCollabTopic(scope, noteID), payload)
		if normalizeCollabScope(scope) == "home" {
			_ = writeAppEvent(ctx, peer.peer, "notes.collab.ops", noteCollabTopic(noteID), payload)
		}
	}
}

func (h *noteCollaborationHub) resolveNote(ctx context.Context, scope string, homeID string, userID string, noteKey string) (domain.UserNote, error) {
	if normalizeCollabScope(scope) == "profile" {
		return h.store.GetProfileNote(ctx, userID, noteKey)
	}
	return h.store.GetHomeNoteVisibleToUser(ctx, homeID, userID, noteKey)
}

func normalizeCollabScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "profile" {
		return "profile"
	}
	return "home"
}

func writeAppEvent(ctx context.Context, peer *wsPeer, event string, topic string, payload any) error {
	body, err := protocol.EncodeBody(payload)
	if err != nil {
		return err
	}
	return peer.Write(ctx, protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeAppEvent,
		Timestamp: nowUTC(),
		Payload:   mustEventBody(event, topic, body),
	})
}

func mustEventBody(event string, topic string, body json.RawMessage) json.RawMessage {
	encoded, _ := json.Marshal(protocol.AppEvent{Event: event, Topic: topic, Body: body})
	return encoded
}

func decodeAppliedOperations(operations []domain.NoteOperation) ([]protocol.NoteCollaborationAppliedOp, error) {
	applied := make([]protocol.NoteCollaborationAppliedOp, 0, len(operations))
	for _, operation := range operations {
		var decoded protocol.NoteCollaborationOperation
		if err := json.Unmarshal([]byte(operation.OpJSON), &decoded); err != nil {
			return nil, err
		}
		applied = append(applied, protocol.NoteCollaborationAppliedOp{
			NoteCollaborationOperation: decoded,
			ActorUserID:                operation.ActorUserID,
			SessionID:                  operation.SessionID,
			BaseVersion:                operation.BaseVersion,
			AppliedVersion:             operation.AppliedVersion,
			CreatedAt:                  operation.CreatedAt,
		})
	}
	return applied, nil
}

func applyCollaborationOperation(state collabState, userID string, operation protocol.NoteCollaborationOperation) (collabState, error) {
	nextVersion := state.CollabVersion + 1
	switch operation.Type {
	case "set_field":
		switch operation.Field {
		case "title":
			var value string
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.Title = collabScalar{Value: value, Version: nextVersion, UserID: userID}
		case "page_type":
			var value string
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.PageType = collabScalar{Value: normalizePageType(value), Version: nextVersion, UserID: userID}
		case "parent_id":
			var value string
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.ParentID = collabScalar{Value: strings.TrimSpace(value), Version: nextVersion, UserID: userID}
		case "sort_order":
			var value int
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.SortOrder = value
		case "pinned":
			var value bool
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.Pinned = value
		case "content":
			var value string
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				return state, err
			}
			state.Content = value
		default:
			return state, errors.New("unsupported field")
		}
	case "text_insert":
		state.Content = insertAt(state.Content, operation.Index, operation.Text)
	case "text_delete":
		state.Content = deleteAt(state.Content, operation.Index, operation.DeleteCount)
	case "text_replace":
		state.Content = operation.Text
	case "board_replace":
		var board protocol.KanbanBoard
		if err := json.Unmarshal(operation.Value, &board); err != nil {
			return state, err
		}
		state.Board = &board
	case "kanban_column_upsert":
		if state.Board == nil {
			state.Board = &protocol.KanbanBoard{}
		}
		var column protocol.KanbanColumn
		if err := json.Unmarshal(operation.Value, &column); err != nil {
			return state, err
		}
		upsertColumn(state.Board, column)
	case "kanban_column_delete":
		if state.Board != nil {
			deleteColumn(state.Board, operation.ID)
		}
	case "kanban_column_move":
		if state.Board != nil {
			moveColumn(state.Board, operation.ID, operation.ToIndex)
		}
	case "kanban_card_upsert":
		if state.Board == nil {
			state.Board = &protocol.KanbanBoard{}
		}
		var card protocol.KanbanCard
		if err := json.Unmarshal(operation.Value, &card); err != nil {
			return state, err
		}
		upsertCard(state.Board, operation.ParentID, card)
	case "kanban_card_delete":
		if state.Board != nil {
			deleteCard(state.Board, operation.ParentID, operation.ID)
		}
	case "kanban_card_move":
		if state.Board != nil {
			moveCard(state.Board, operation.ID, operation.ParentID, operation.ToIndex)
		}
	default:
		return state, errors.New("unsupported collaboration op")
	}
	state.CollabVersion = nextVersion
	return state, nil
}

func insertAt(value string, index int, insert string) string {
	runes := []rune(value)
	if index < 0 {
		index = 0
	}
	if index > len(runes) {
		index = len(runes)
	}
	out := make([]rune, 0, len(runes)+len([]rune(insert)))
	out = append(out, runes[:index]...)
	out = append(out, []rune(insert)...)
	out = append(out, runes[index:]...)
	return string(out)
}

func deleteAt(value string, index int, count int) string {
	runes := []rune(value)
	if index < 0 {
		index = 0
	}
	if index > len(runes) {
		index = len(runes)
	}
	if count < 0 {
		count = 0
	}
	end := index + count
	if end > len(runes) {
		end = len(runes)
	}
	out := make([]rune, 0, len(runes))
	out = append(out, runes[:index]...)
	out = append(out, runes[end:]...)
	return string(out)
}

func upsertColumn(board *protocol.KanbanBoard, column protocol.KanbanColumn) {
	for index := range board.Columns {
		if board.Columns[index].ID == column.ID {
			board.Columns[index] = column
			return
		}
	}
	board.Columns = append(board.Columns, column)
}

func deleteColumn(board *protocol.KanbanBoard, columnID string) {
	for index := range board.Columns {
		if board.Columns[index].ID == columnID {
			board.Columns = append(board.Columns[:index], board.Columns[index+1:]...)
			return
		}
	}
}

func moveColumn(board *protocol.KanbanBoard, columnID string, toIndex int) {
	for index := range board.Columns {
		if board.Columns[index].ID == columnID {
			column := board.Columns[index]
			board.Columns = append(board.Columns[:index], board.Columns[index+1:]...)
			if toIndex < 0 {
				toIndex = 0
			}
			if toIndex > len(board.Columns) {
				toIndex = len(board.Columns)
			}
			board.Columns = append(board.Columns[:toIndex], append([]protocol.KanbanColumn{column}, board.Columns[toIndex:]...)...)
			return
		}
	}
}

func upsertCard(board *protocol.KanbanBoard, columnID string, card protocol.KanbanCard) {
	for columnIndex := range board.Columns {
		if board.Columns[columnIndex].ID != columnID {
			continue
		}
		for cardIndex := range board.Columns[columnIndex].Cards {
			if board.Columns[columnIndex].Cards[cardIndex].ID == card.ID {
				board.Columns[columnIndex].Cards[cardIndex] = card
				return
			}
		}
		board.Columns[columnIndex].Cards = append(board.Columns[columnIndex].Cards, card)
		return
	}
}

func deleteCard(board *protocol.KanbanBoard, columnID string, cardID string) {
	for columnIndex := range board.Columns {
		if columnID != "" && board.Columns[columnIndex].ID != columnID {
			continue
		}
		for cardIndex := range board.Columns[columnIndex].Cards {
			if board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				board.Columns[columnIndex].Cards = append(board.Columns[columnIndex].Cards[:cardIndex], board.Columns[columnIndex].Cards[cardIndex+1:]...)
				return
			}
		}
	}
}

func moveCard(board *protocol.KanbanBoard, cardID string, targetColumnID string, toIndex int) {
	var card *protocol.KanbanCard
	for columnIndex := range board.Columns {
		for cardIndex := range board.Columns[columnIndex].Cards {
			if board.Columns[columnIndex].Cards[cardIndex].ID == cardID {
				value := board.Columns[columnIndex].Cards[cardIndex]
				card = &value
				board.Columns[columnIndex].Cards = append(board.Columns[columnIndex].Cards[:cardIndex], board.Columns[columnIndex].Cards[cardIndex+1:]...)
				break
			}
		}
	}
	if card == nil {
		return
	}
	for columnIndex := range board.Columns {
		if board.Columns[columnIndex].ID != targetColumnID {
			continue
		}
		if toIndex < 0 {
			toIndex = 0
		}
		if toIndex > len(board.Columns[columnIndex].Cards) {
			toIndex = len(board.Columns[columnIndex].Cards)
		}
		cards := board.Columns[columnIndex].Cards
		board.Columns[columnIndex].Cards = append(cards[:toIndex], append([]protocol.KanbanCard{*card}, cards[toIndex:]...)...)
		return
	}
}

func (s *Server) evictCollaborator(homeID string, userID string, reason string) {
	if s.collaboration == nil {
		return
	}
	s.collaboration.revokeUser(homeID, userID, reason)
}
