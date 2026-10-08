package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleCloudNotesCommand(ctx context.Context, appPeer *wsPeer, envelope protocol.Envelope, auth authContext, command protocol.RoutedCommand) error {
	var payload any
	var err error

	switch command.Command {
	case "notes.list":
		payload, err = s.notes.ListHome(ctx, envelope.HomeID, auth.User.ID)
		if err == nil {
			payload = protocol.NotesListResponse{Notes: payload.([]protocol.NoteSummary)}
		}

	case "notes.fetch":
		var request protocol.NotesFetchRequest
		request, err = decodeBody[protocol.NotesFetchRequest](command.Body)
		if err == nil {
			payload, err = s.notes.FetchHome(ctx, envelope.HomeID, auth.User.ID, request.NoteID)
		}

	case "notes.save":
		var request protocol.NotesSaveRequest
		request, err = decodeBody[protocol.NotesSaveRequest](command.Body)
		if err == nil {
			payload, err = s.notes.SaveHome(ctx, envelope.HomeID, auth.User.ID, request.NoteID, request)
			if err == nil {
				if response, ok := payload.(protocol.NotesSaveResponse); ok {
					s.enqueueAssistantNoteIndexJob(ctx, envelope.HomeID, auth.User.ID, response.NoteID, assistantIndexSourceSharedNote)
				}
			}
		}

	case "notes.rename":
		var request protocol.NotesRenameRequest
		request, err = decodeBody[protocol.NotesRenameRequest](command.Body)
		if err == nil {
			err = s.notes.RenameHome(ctx, envelope.HomeID, auth.User.ID, request.NoteID, request.Title)
			if err == nil {
				s.enqueueAssistantNoteIndexJob(ctx, envelope.HomeID, auth.User.ID, request.NoteID, assistantIndexSourceSharedNote)
				payload = map[string]any{"ok": true, "note_id": request.NoteID}
			}
		}

	case "notes.delete":
		var request protocol.NotesDeleteRequest
		request, err = decodeBody[protocol.NotesDeleteRequest](command.Body)
		if err == nil {
			err = s.notes.DeleteHome(ctx, envelope.HomeID, auth.User.ID, request.NoteID)
			if err == nil {
				s.enqueueAssistantNoteIndexJob(ctx, envelope.HomeID, auth.User.ID, request.NoteID, assistantIndexSourceSharedNote)
			}
			payload = protocol.EmptyResponse{OK: err == nil}
		}

	case "notes.sync":
		var notes []protocol.NoteSummary
		notes, err = s.notes.Sync(ctx, envelope.HomeID)
		if err == nil {
			payload = protocol.NotesSyncResponse{Notes: notes}
		}

	case "notes.collab.join":
		var request protocol.NoteCollaborationJoinRequest
		request, err = decodeBody[protocol.NoteCollaborationJoinRequest](command.Body)
		if err == nil {
			payload, err = s.collaboration.join(ctx, request.Scope, envelope.HomeID, request.NoteID, request.SessionID, appPeerConnection(appPeer, auth))
		}

	case "notes.collab.leave":
		var request protocol.NoteCollaborationLeaveRequest
		request, err = decodeBody[protocol.NoteCollaborationLeaveRequest](command.Body)
		if err == nil {
			err = s.collaboration.leave(ctx, request.Scope, envelope.HomeID, request.NoteID, request.SessionID, appPeerConnection(appPeer, auth))
			payload = protocol.EmptyResponse{OK: err == nil}
		}

	case "notes.collab.sync":
		var request protocol.NoteCollaborationSyncRequest
		request, err = decodeBody[protocol.NoteCollaborationSyncRequest](command.Body)
		if err == nil {
			payload, err = s.collaboration.sync(ctx, request.Scope, envelope.HomeID, request.NoteID, auth.User.ID, request.AfterVersion, request.MaxOperations)
		}

	case "notes.collab.submit_ops":
		var request protocol.NoteCollaborationSubmitOpsRequest
		request, err = decodeBody[protocol.NoteCollaborationSubmitOpsRequest](command.Body)
		if err == nil {
			payload, err = s.collaboration.submitOps(ctx, envelope.HomeID, request, appPeerConnection(appPeer, auth))
			if err == nil {
				if normalizeCollabScope(request.Scope) == "home" {
					if note, noteErr := s.store.GetHomeNoteVisibleToUser(ctx, envelope.HomeID, auth.User.ID, request.NoteID); noteErr == nil {
						if shareCount, shareErr := s.store.CountNoteShares(ctx, note.ID); shareErr == nil && shareCount > 0 {
							s.markHomeNotesDirty(ctx, envelope.HomeID, "")
						}
					}
				}
			}
		}

	case "notes.search":
		var request protocol.NotesSearchRequest
		request, err = decodeBody[protocol.NotesSearchRequest](command.Body)
		if err == nil {
			var results []protocol.NoteSearchResult
			parentID := strings.TrimSpace(request.NotebookID)
			if parentID == "" {
				parentID = strings.TrimSpace(request.ParentID)
			}
			results, err = s.notes.SearchHome(ctx, envelope.HomeID, auth.User.ID, request.Query, request.Limit, parentID)
			if err == nil {
				payload = protocol.NotesSearchResponse{Results: results}
			}
		}

	case "notes.tags":
		var tags []protocol.NoteTagCount
		tags, err = s.notes.TagsHome(ctx, envelope.HomeID, auth.User.ID)
		if err == nil {
			payload = protocol.NotesTagsResponse{Tags: tags}
		}

	case "notes.tag_rollup":
		var request protocol.NotesTagRollupRequest
		request, err = decodeBody[protocol.NotesTagRollupRequest](command.Body)
		if err == nil {
			var items []protocol.TaggedLineRollupItem
			items, err = s.notes.TagRollupHome(ctx, envelope.HomeID, auth.User.ID, request.Tag)
			if err == nil {
				payload = protocol.NotesTagRollupResponse{Items: items}
			}
		}

	default:
		return s.writeAppCommandError(ctx, appPeer, envelope, "unsupported_command", "unsupported note command", nil)
	}

	if err != nil {
		conflict := &noteConflictError{}
		if errors.As(err, &conflict) {
			return s.writeAppCommandError(ctx, appPeer, envelope, "note_conflict", err.Error(), map[string]any{"current": conflict.Current})
		}
		if errors.Is(err, store.ErrNotFound) {
			return s.writeAppCommandError(ctx, appPeer, envelope, "not_found", err.Error(), nil)
		}
		return s.writeAppCommandError(ctx, appPeer, envelope, "cloud_notes_error", err.Error(), nil)
	}

	switch command.Command {
	case "notes.save", "notes.rename", "notes.delete":
		s.markHomeNotesDirty(ctx, envelope.HomeID, "")
	}

	responseBody, err := protocol.EncodeBody(payload)
	if err != nil {
		return s.writeAppCommandError(ctx, appPeer, envelope, "encoding_failed", err.Error(), nil)
	}
	return appPeer.Write(ctx, protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeAppResponse,
		RequestID: envelope.RequestID,
		HomeID:    envelope.HomeID,
		Timestamp: envelope.Timestamp,
		Payload:   responseBody,
	})
}

func appPeerConnection(appPeer *wsPeer, auth authContext) *appConnection {
	return &appConnection{
		sessionID: auth.Session.ID,
		userID:    auth.User.ID,
		peer:      appPeer,
	}
}

func (s *Server) writeAppCommandError(ctx context.Context, appPeer *wsPeer, envelope protocol.Envelope, code string, message string, details map[string]any) error {
	return appPeer.Write(ctx, protocol.NewErrorEnvelope(protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, code, message, details))
}

func decodeBody[T any](body json.RawMessage) (T, error) {
	var out T
	if len(body) == 0 {
		return out, nil
	}
	err := json.Unmarshal(body, &out)
	return out, err
}

func isProfileScopedNotesCommand(command protocol.RoutedCommand) (bool, error) {
	switch command.Command {
	case "notes.collab.join", "notes.collab.leave", "notes.collab.sync", "notes.collab.submit_ops":
		var body struct {
			Scope string `json:"scope"`
		}
		if len(command.Body) > 0 {
			if err := json.Unmarshal(command.Body, &body); err != nil {
				return false, err
			}
		}
		return normalizeCollabScope(body.Scope) == "profile", nil
	default:
		return false, nil
	}
}
