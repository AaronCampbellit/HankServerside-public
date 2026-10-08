package cloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type adminNoteAttachmentItem struct {
	ID             string    `json:"id"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	NoteID         string    `json:"note_id"`
	NoteTitle      string    `json:"note_title"`
	NoteScope      string    `json:"note_scope"`
	OwnerEmail     string    `json:"owner_email"`
	ReferenceCount int       `json:"reference_count"`
	Contexts       []string  `json:"contexts"`
	DownloadURL    string    `json:"download_url"`
}

type adminNoteAttachmentInventory struct {
	TotalFiles  int                       `json:"total_files"`
	TotalBytes  int64                     `json:"total_bytes"`
	Attachments []adminNoteAttachmentItem `json:"attachments"`
}

func (s *Server) handleHomeNoteAttachmentsAdmin(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "note-attachments" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		records, err := s.store.ListLiveNoteAttachmentInventory(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		payload := adminNoteAttachmentInventory{Attachments: make([]adminNoteAttachmentItem, 0, len(records))}
		for _, record := range records {
			payload.Attachments = append(payload.Attachments, adminNoteAttachmentItemFromRecord(record))
			payload.TotalBytes += record.Attachment.SizeBytes
		}
		payload.TotalFiles = len(payload.Attachments)
		writeJSON(w, http.StatusOK, payload)
		return true

	case len(parts) == 3 && parts[2] == "content" && r.Method == http.MethodGet:
		record, err := s.store.GetLiveNoteAttachmentInventoryByID(r.Context(), parts[1])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.serveNoteAttachment(w, r, record.Attachment)
		return true

	case len(parts) == 2 && r.Method == http.MethodDelete:
		record, err := s.store.GetLiveNoteAttachmentInventoryByID(r.Context(), parts[1])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		note, err := s.store.GetUserNoteByID(r.Context(), record.Attachment.NoteID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		updatedNote, cleanupComplete, err := s.deleteStoredNoteAttachment(r.Context(), note, record.NoteScope, auth.User.ID, record.Attachment)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.audit(r.Context(), "note_attachment.deleted", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "note_attachment", record.Attachment.ID, map[string]any{
			"note_id":          record.NoteID,
			"cleanup_complete": cleanupComplete,
		})
		status := http.StatusOK
		if !cleanupComplete {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, protocol.NoteAttachmentDeleteResponse{OK: true, NoteRevision: updatedNote.Revision, CleanupComplete: cleanupComplete})
		return true

	case len(parts) == 1 || len(parts) == 2 || (len(parts) == 3 && parts[2] == "content"):
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	default:
		http.NotFound(w, r)
		return true
	}
}

func adminNoteAttachmentItemFromRecord(record domain.NoteAttachmentInventoryRecord) adminNoteAttachmentItem {
	referenceCount, contexts := noteAttachmentReferenceContexts(record)
	return adminNoteAttachmentItem{
		ID:             record.Attachment.ID,
		Filename:       record.Attachment.Filename,
		ContentType:    record.Attachment.ContentType,
		SizeBytes:      record.Attachment.SizeBytes,
		CreatedAt:      record.Attachment.CreatedAt,
		NoteID:         record.NoteID,
		NoteTitle:      record.NoteTitle,
		NoteScope:      record.NoteScope,
		OwnerEmail:     record.OwnerEmail,
		ReferenceCount: referenceCount,
		Contexts:       contexts,
		DownloadURL:    "/v1/home/note-attachments/" + url.PathEscape(record.Attachment.ID) + "/content",
	}
}

func noteAttachmentReferenceContexts(record domain.NoteAttachmentInventoryRecord) (int, []string) {
	pattern, err := noteAttachmentReferencePattern(record.Attachment.ID)
	if err != nil {
		return 0, []string{}
	}
	if record.NotePageType == protocol.NotePageTypeKanban && strings.TrimSpace(record.BoardJSON) != "" {
		var board protocol.KanbanBoard
		if json.Unmarshal([]byte(record.BoardJSON), &board) == nil {
			count := 0
			contexts := []string{}
			for _, column := range board.Columns {
				for _, card := range column.Cards {
					matches := pattern.FindAllStringIndex(card.Text, -1)
					if len(matches) == 0 {
						continue
					}
					count += len(matches)
					contexts = append(contexts, firstNonBlank(strings.TrimSpace(strings.SplitN(card.Text, "\n", 2)[0]), "Untitled task"))
				}
			}
			return count, contexts
		}
	}
	count := len(pattern.FindAllStringIndex(record.BodyMarkdown, -1))
	contexts := []string{}
	if count > 0 {
		contexts = append(contexts, firstNonBlank(record.NoteTitle, record.NoteID))
	}
	return count, contexts
}
