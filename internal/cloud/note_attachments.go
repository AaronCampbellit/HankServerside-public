package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
)

const maxNoteAttachmentBytes = 100 << 20

const (
	noteAttachmentDirMode  os.FileMode = 0o770 | os.ModeSetgid
	noteAttachmentFileMode os.FileMode = 0o660
)

var unsafeAttachmentFilenameRunes = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func splitNoteAttachmentRoute(parts []string) (noteID string, attachmentID string, ok bool) {
	for index := 1; index < len(parts); index++ {
		if parts[index] != "attachments" {
			continue
		}
		noteID = strings.TrimSpace(strings.Join(parts[1:index], "/"))
		if noteID == "" {
			return "", "", false
		}
		if index+1 < len(parts) {
			attachmentID = strings.TrimSpace(parts[index+1])
		}
		return noteID, attachmentID, true
	}
	return "", "", false
}

func (s *Server) handleProfileNoteAttachmentsHTTP(w http.ResponseWriter, r *http.Request, auth authContext, noteID string, attachmentID string) {
	note, err := s.store.GetProfileNote(r.Context(), auth.User.ID, noteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.handleNoteAttachmentsHTTP(w, r, note, "profile", auth.User.ID, attachmentID)
}

func (s *Server) handleHomeNoteAttachmentsHTTP(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, noteID string, attachmentID string) {
	note, err := s.store.GetHomeNoteVisibleToUser(r.Context(), home.ID, auth.User.ID, noteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.handleNoteAttachmentsHTTP(w, r, note, "home", auth.User.ID, attachmentID)
}

func (s *Server) handleNoteAttachmentsHTTP(w http.ResponseWriter, r *http.Request, note domain.UserNote, scope string, userID string, attachmentID string) {
	if note.DeletedAt != nil {
		http.NotFound(w, r)
		return
	}

	if attachmentID == "" {
		switch r.Method {
		case http.MethodGet:
			attachments, err := s.store.ListNoteAttachments(r.Context(), note.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, protocol.NoteAttachmentsResponse{
				Attachments: noteAttachmentsToProtocol(attachments, note, scope),
			})
		case http.MethodPost:
			attachment, updatedNote, err := s.storeUploadedNoteAttachment(r, note, scope, userID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.emitNoteAttachmentChanged(r.Context(), updatedNote, scope, userID)
			writeJSON(w, http.StatusCreated, noteAttachmentToProtocol(attachment, updatedNote, scope))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	attachment, err := s.store.GetNoteAttachment(r.Context(), note.ID, attachmentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.serveNoteAttachment(w, r, attachment)
	case http.MethodDelete:
		updatedNote, cleanupComplete, err := s.deleteStoredNoteAttachment(r.Context(), note, scope, userID, attachment)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		if !cleanupComplete {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, protocol.NoteAttachmentDeleteResponse{
			OK:              true,
			NoteRevision:    updatedNote.Revision,
			CleanupComplete: cleanupComplete,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) storeUploadedNoteAttachment(r *http.Request, note domain.UserNote, scope string, userID string) (domain.NoteAttachment, domain.UserNote, error) {
	filename := safeAttachmentFilename(firstNonBlank(r.URL.Query().Get("filename"), r.Header.Get("X-Hank-Filename"), "Attachment"))
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil && parsed != "" {
		contentType = parsed
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	attachmentID := newID("natt")
	storageKey := filepath.Join(note.ID, attachmentID+"-"+filename)
	targetPath, err := s.noteAttachmentPathForWrite(storageKey)
	if err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), noteAttachmentDirMode); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := ensureNoteAttachmentMode(s.noteAttachmentRoot, noteAttachmentDirMode); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := ensureNoteAttachmentMode(filepath.Dir(targetPath), noteAttachmentDirMode); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	tempPath := targetPath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	defer file.Close()
	defer os.Remove(tempPath)

	limited := io.LimitReader(r.Body, maxNoteAttachmentBytes+1)
	hasher := sha256.New()
	written, err := io.Copy(file, io.TeeReader(limited, hasher))
	if err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if written > maxNoteAttachmentBytes {
		return domain.NoteAttachment{}, domain.UserNote{}, errors.New("attachment is too large")
	}
	if written <= 0 {
		return domain.NoteAttachment{}, domain.UserNote{}, errors.New("attachment body is empty")
	}
	if err := file.Close(); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := os.Chmod(targetPath, noteAttachmentFileMode); err != nil {
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}

	now := time.Now().UTC()
	attachment := domain.NoteAttachment{
		ID:             attachmentID,
		NoteID:         note.ID,
		HomeID:         note.HomeID,
		OwnerUserID:    note.OwnerUserID,
		Filename:       filename,
		ContentType:    contentType,
		SizeBytes:      written,
		ChecksumSHA256: hex.EncodeToString(hasher.Sum(nil)),
		StorageKey:     storageKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	updatedNote, err := noteWithAttachmentReference(note, scope, userID, attachment)
	if err != nil {
		_ = os.Remove(targetPath)
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	if err := s.store.CreateNoteAttachmentAndSaveNote(r.Context(), attachment, updatedNote); err != nil {
		_ = os.Remove(targetPath)
		return domain.NoteAttachment{}, domain.UserNote{}, err
	}
	s.enqueueAssistantNoteIndexJob(r.Context(), updatedNote.HomeID, userID, updatedNote.NoteID, assistantNoteSourceType(updatedNote))
	return attachment, updatedNote, nil
}

func noteWithAttachmentReference(note domain.UserNote, scope string, userID string, attachment domain.NoteAttachment) (domain.UserNote, error) {
	body := strings.TrimSpace(noteBodyText(note))
	if body != "" {
		body += "\n\n"
	}
	body += noteAttachmentMarkdownReference(note, scope, attachment)

	revision, checksum, err := revisionAndChecksum(body, note.PageType, note.BoardJSON)
	if err != nil {
		return domain.UserNote{}, err
	}
	note.Content = body
	note.BodyMarkdown = body
	note.BodyFormat = "markdown"
	note.Revision = revision
	note.Checksum = checksum
	note.UpdatedAt = time.Now().UTC()
	note.UpdatedBy = userID
	return note, nil
}

func removeNoteAttachmentReferences(note domain.UserNote, userID string, attachment domain.NoteAttachment) (domain.UserNote, int, error) {
	body := noteBodyText(note)
	pattern, err := noteAttachmentReferencePattern(attachment.ID)
	if err != nil {
		return domain.UserNote{}, 0, err
	}
	removed := len(pattern.FindAllStringIndex(body, -1))
	body = strings.TrimSpace(pattern.ReplaceAllString(body, ""))
	boardJSON := note.BoardJSON
	if note.PageType == protocol.NotePageTypeKanban && strings.TrimSpace(boardJSON) != "" {
		var board protocol.KanbanBoard
		if err := json.Unmarshal([]byte(boardJSON), &board); err != nil {
			return domain.UserNote{}, 0, fmt.Errorf("decode kanban attachment references: %w", err)
		}
		for columnIndex := range board.Columns {
			for cardIndex := range board.Columns[columnIndex].Cards {
				card := &board.Columns[columnIndex].Cards[cardIndex]
				removed += len(pattern.FindAllStringIndex(card.Text, -1))
				card.Text = strings.TrimSpace(pattern.ReplaceAllString(card.Text, ""))
			}
		}
		encoded, err := json.Marshal(board)
		if err != nil {
			return domain.UserNote{}, 0, fmt.Errorf("encode kanban attachment references: %w", err)
		}
		boardJSON = string(encoded)
	}
	revision, checksum, err := revisionAndChecksum(body, note.PageType, boardJSON)
	if err != nil {
		return domain.UserNote{}, 0, err
	}
	note.Content = body
	note.BodyMarkdown = body
	note.BodyFormat = "markdown"
	note.BoardJSON = boardJSON
	note.Revision = revision
	note.Checksum = checksum
	note.UpdatedAt = time.Now().UTC()
	note.UpdatedBy = userID
	return note, removed, nil
}

func (s *Server) deleteStoredNoteAttachment(ctx context.Context, note domain.UserNote, scope string, userID string, attachment domain.NoteAttachment) (domain.UserNote, bool, error) {
	updatedNote, _, err := removeNoteAttachmentReferences(note, userID, attachment)
	if err != nil {
		return domain.UserNote{}, false, err
	}
	if err := s.store.DeleteNoteAttachmentAndSaveNote(ctx, note.ID, attachment.ID, time.Now().UTC(), updatedNote); err != nil {
		return domain.UserNote{}, false, err
	}

	cleanupComplete := true
	attachmentPath, pathErr := s.noteAttachmentPath(attachment.StorageKey)
	if pathErr != nil {
		cleanupComplete = false
		s.logger.Error("note attachment cleanup path failed", "attachment_id", attachment.ID, "note_id", note.NoteID, "error", pathErr)
	} else if err := os.Remove(attachmentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupComplete = false
		s.logger.Error("note attachment file cleanup failed", "attachment_id", attachment.ID, "note_id", note.NoteID, "error", err)
	}

	s.enqueueAssistantNoteIndexJob(ctx, updatedNote.HomeID, updatedNote.OwnerUserID, updatedNote.NoteID, assistantNoteSourceType(updatedNote))
	s.emitNoteAttachmentChanged(ctx, updatedNote, scope, updatedNote.OwnerUserID)
	return updatedNote, cleanupComplete, nil
}

func (s *Server) removeNoteAttachmentFiles(attachment domain.NoteAttachment) bool {
	complete := true
	for _, storageKey := range []string{attachment.StorageKey, attachment.PreviewStorageKey} {
		if strings.TrimSpace(storageKey) == "" {
			continue
		}
		path, err := s.noteAttachmentPath(storageKey)
		if err != nil {
			complete = false
			s.logger.Error("note attachment permanent cleanup path failed", "attachment_id", attachment.ID, "error", err)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			complete = false
			s.logger.Error("note attachment permanent cleanup failed", "attachment_id", attachment.ID, "error", err)
		}
	}
	return complete
}

func noteAttachmentReferencePattern(attachmentID string) (*regexp.Regexp, error) {
	escapedID := regexp.QuoteMeta(url.PathEscape(strings.TrimSpace(attachmentID)))
	return regexp.Compile(`(?m)\n{0,2}!?\[[^\]]+\]\(hank-note-attachment://` + escapedID + `[^)]*\)`)
}

func (s *Server) serveNoteAttachment(w http.ResponseWriter, r *http.Request, attachment domain.NoteAttachment) {
	attachmentPath, err := s.noteAttachmentPath(attachment.StorageKey)
	if err != nil {
		http.Error(w, "invalid attachment path", http.StatusBadRequest)
		return
	}
	file, err := os.Open(attachmentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	requestedDisposition := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("disposition")))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if attachment.ContentType == "text/html" && requestedDisposition == "preview" {
		select {
		case mcpAttachmentProcessingSlots <- struct{}{}:
			defer func() { <-mcpAttachmentProcessingSlots }()
		case <-r.Context().Done():
			return
		}
		sanitized, sanitizeErr := sanitizeNoteAttachmentHTML(io.LimitReader(file, maxNoteAttachmentBytes+1))
		if sanitizeErr != nil {
			http.Error(w, "invalid HTML attachment", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": attachment.Filename}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(sanitized)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sanitized)
		return
	}
	w.Header().Set("Content-Type", attachment.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", attachment.SizeBytes))
	disposition := "attachment"
	if requestedDisposition != "download" && isInlineNoteImageContentType(attachment.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Filename}))
	http.ServeContent(w, r, attachment.Filename, attachment.UpdatedAt, file)
}

func (s *Server) noteAttachmentPath(storageKey string) (string, error) {
	return s.resolveNoteAttachmentPath(storageKey, false)
}

func (s *Server) noteAttachmentPathForWrite(storageKey string) (string, error) {
	return s.resolveNoteAttachmentPath(storageKey, true)
}

func (s *Server) pruneNoteAttachmentFiles(ctx context.Context, now time.Time, retention time.Duration) error {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(s.noteAttachmentRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	liveKeys, err := s.store.ListLiveNoteAttachmentStorageKeys(ctx)
	if err != nil {
		return err
	}
	liveStagingKeys, removableStagingKeys, err := s.store.ListMCPNoteAttachmentStagingKeys(ctx)
	if err != nil {
		return err
	}
	for key := range liveStagingKeys {
		liveKeys[key] = struct{}{}
	}
	stagingRemoved := 0
	for _, key := range removableStagingKeys {
		path, pathErr := s.noteAttachmentPathForWrite(key)
		if pathErr != nil {
			return pathErr
		}
		if removeErr := os.Remove(path); removeErr == nil {
			stagingRemoved++
		} else if !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	cutoff := now.Add(-retention)
	orphansRemoved := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && (path == filepath.Join(root, storageops.RecoveryDirectoryName) || path == filepath.Join(root, ".assistant-staging")) {
			// Execution stages have their own owner/expiry lifecycle. Legacy
			// note orphan cleanup must not remove pending task inputs.
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		storageKey := filepath.ToSlash(relative)
		if _, ok := liveKeys[storageKey]; ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		orphansRemoved++
		return nil
	})
	if err != nil {
		return err
	}
	if stagingRemoved > 0 {
		if s.metrics != nil {
			s.metrics.AddMCPAttachmentCleanup("staging_files", int64(stagingRemoved))
		}
		s.logger.Info("pruned terminal MCP attachment staging files", "count", stagingRemoved)
	}
	if orphansRemoved > 0 {
		if s.metrics != nil {
			s.metrics.AddMCPAttachmentCleanup("orphan_files", int64(orphansRemoved))
		}
		s.logger.Info("pruned orphaned note attachment files", "count", orphansRemoved, "root", root)
	}
	s.refreshMCPAttachmentMetrics(ctx)
	return nil
}

func (s *Server) repairNoteAttachmentBackupPermissions() error {
	root, err := filepath.EvalSymlinks(filepath.Clean(s.noteAttachmentRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && path == filepath.Join(root, storageops.RecoveryDirectoryName) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return ensureNoteAttachmentMode(path, noteAttachmentDirMode)
		}
		return ensureNoteAttachmentMode(path, noteAttachmentFileMode)
	})
}

func (s *Server) resolveNoteAttachmentPath(storageKey string, forWrite bool) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(storageKey))
	if cleaned == ".assistant-staging" || strings.HasPrefix(cleaned, ".assistant-staging"+string(filepath.Separator)) {
		return "", errors.New("attachment key uses private assistant staging")
	}
	if cleaned == storageops.RecoveryDirectoryName || strings.HasPrefix(cleaned, storageops.RecoveryDirectoryName+string(filepath.Separator)) {
		return "", errors.New("attachment key uses private recovery storage")
	}
	if cleaned == "." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", fmt.Errorf("attachment path escapes root")
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(s.noteAttachmentRoot))
	if err != nil {
		if forWrite && errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(s.noteAttachmentRoot, noteAttachmentDirMode); err != nil {
				return "", err
			}
			root, err = filepath.EvalSymlinks(filepath.Clean(s.noteAttachmentRoot))
		}
		if err != nil {
			return "", err
		}
	}
	joined := filepath.Join(root, cleaned)
	resolved, err := filepath.EvalSymlinks(joined)
	if err == nil {
		if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return "", fmt.Errorf("attachment path escapes root")
		}
		return resolved, nil
	}
	if !forWrite || !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(joined)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		realParent, err = nearestExistingAttachmentParent(root, parent)
		if err != nil {
			return "", err
		}
	}
	if realParent != root && !strings.HasPrefix(realParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("attachment path escapes root")
	}
	if realParent != parent {
		relative, err := filepath.Rel(realParent, joined)
		if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			return "", fmt.Errorf("attachment path escapes root")
		}
		return filepath.Join(realParent, relative), nil
	}
	return filepath.Join(realParent, filepath.Base(joined)), nil
}

func nearestExistingAttachmentParent(root string, path string) (string, error) {
	current := filepath.Clean(path)
	for {
		if current == root || strings.HasPrefix(current, root+string(filepath.Separator)) {
			resolved, err := filepath.EvalSymlinks(current)
			if err == nil {
				return resolved, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else {
			return "", fmt.Errorf("attachment path escapes root")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("attachment path escapes root")
		}
		current = parent
	}
}

func (s *Server) emitNoteAttachmentChanged(ctx context.Context, note domain.UserNote, scope string, userID string) {
	if scope == "home" {
		s.markHomeNotesDirty(ctx, note.HomeID, "")
		s.emitHomeNotesChanged(ctx, "notes.attachment_changed", map[string]any{"home_id": note.HomeID, "note_id": note.NoteID})
		return
	}
	s.emitProfileNotesChanged(ctx, map[string]any{"user_id": userID, "note_id": note.NoteID})
}

func (s *Server) addNoteAttachmentsToResponse(ctx context.Context, response protocol.NotesFetchResponse, note domain.UserNote, scope string) protocol.NotesFetchResponse {
	attachments, err := s.store.ListNoteAttachments(ctx, note.ID)
	if err != nil {
		return response
	}
	response.Attachments = noteAttachmentsToProtocol(attachments, note, scope)
	return response
}

func noteAttachmentMarkdownReference(note domain.UserNote, scope string, attachment domain.NoteAttachment) string {
	values := url.Values{}
	values.Set("note_id", note.NoteID)
	values.Set("scope", scope)
	values.Set("filename", attachment.Filename)
	prefix := ""
	if isInlineNoteImageContentType(attachment.ContentType) {
		prefix = "!"
	}
	return fmt.Sprintf("%s[%s](hank-note-attachment://%s?%s)", prefix, attachment.Filename, url.PathEscape(attachment.ID), values.Encode())
}

func isInlineNoteImageContentType(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/heic", "image/heif":
		return true
	default:
		return false
	}
}

func noteAttachmentsToProtocol(attachments []domain.NoteAttachment, note domain.UserNote, scope string) []protocol.NoteAttachment {
	results := make([]protocol.NoteAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		results = append(results, noteAttachmentToProtocol(attachment, note, scope))
	}
	return results
}

func noteAttachmentToProtocol(attachment domain.NoteAttachment, note domain.UserNote, scope string) protocol.NoteAttachment {
	downloadPath := noteAttachmentDownloadPath(note, scope, attachment.ID)
	previewPath := downloadPath
	if attachment.ContentType == "text/html" {
		previewPath += "?" + url.Values{"disposition": []string{"preview"}}.Encode()
	}
	return protocol.NoteAttachment{
		ID:             attachment.ID,
		NoteID:         note.NoteID,
		NoteRevision:   note.Revision,
		Filename:       attachment.Filename,
		ContentType:    attachment.ContentType,
		SizeBytes:      attachment.SizeBytes,
		ChecksumSHA256: attachment.ChecksumSHA256,
		DownloadURL:    downloadPath,
		PreviewURL:     previewPath,
		MarkdownRef:    noteAttachmentMarkdownReference(note, scope, attachment),
		CreatedAt:      attachment.CreatedAt,
		UpdatedAt:      attachment.UpdatedAt,
	}
}

func noteAttachmentDownloadPath(note domain.UserNote, scope string, attachmentID string) string {
	escapedNoteID := pathEscapeSegments(note.NoteID)
	if scope == "home" {
		return fmt.Sprintf("/v1/home/notes/%s/attachments/%s", escapedNoteID, url.PathEscape(attachmentID))
	}
	return fmt.Sprintf("/v1/me/notes/%s/attachments/%s", escapedNoteID, url.PathEscape(attachmentID))
}

func pathEscapeSegments(value string) string {
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func safeAttachmentFilename(value string) string {
	value = strings.TrimSpace(value)
	value = filepath.Base(value)
	value = unsafeAttachmentFilenameRunes.ReplaceAllString(value, "-")
	value = strings.Trim(value, ". ")
	if value == "" {
		return "Attachment"
	}
	if len(value) > 180 {
		ext := filepath.Ext(value)
		base := strings.TrimSuffix(value, ext)
		if len(ext) > 24 {
			ext = ""
		}
		if len(base) > 180-len(ext) {
			base = base[:180-len(ext)]
		}
		value = strings.TrimSpace(base) + ext
	}
	return value
}

// Restored files may be owned by the database operations user. Correct shared
// modes need no chmod by the cloud user, which can still read/write via its group.
func ensureNoteAttachmentMode(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("attachment permissions refuse symlinks")
	}
	if info.Mode()&(os.ModePerm|os.ModeSetgid) == mode {
		return nil
	}
	return os.Chmod(path, mode)
}
