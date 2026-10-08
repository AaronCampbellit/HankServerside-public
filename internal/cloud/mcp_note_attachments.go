package cloud

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

var mcpAttachmentReferenceFinder = regexp.MustCompile(`hank-note-attachment://([^\s?&#)]+)`)

type mcpAttachmentTargetArgs struct {
	Kind    string `json:"kind"`
	NoteID  string `json:"note_id"`
	BoardID string `json:"board_id"`
	CardID  string `json:"card_id"`
}

type mcpAttachmentTarget struct {
	Note        domain.UserNote
	Board       *protocol.KanbanBoard
	ColumnIndex int
	CardIndex   int
	Markdown    string
}

type mcpAttachmentMetadata struct {
	protocol.NoteAttachment
	ReferenceCount int  `json:"reference_count"`
	Readable       bool `json:"readable"`
	Replaceable    bool `json:"replaceable"`
}

type mcpAttachmentListResult struct {
	Target      mcpAttachmentTargetArgs `json:"target"`
	Revision    string                  `json:"revision"`
	Attachments []mcpAttachmentMetadata `json:"attachments"`
}

type mcpAttachmentReadArgs struct {
	Target         mcpAttachmentTargetArgs `json:"target"`
	AttachmentID   string                  `json:"attachment_id"`
	Representation string                  `json:"representation"`
	Offset         int64                   `json:"offset"`
	Length         int64                   `json:"length"`
}

type mcpAttachmentReadResult struct {
	AttachmentID   string           `json:"attachment_id"`
	Representation string           `json:"representation"`
	Content        []map[string]any `json:"-"`
	Offset         int64            `json:"offset"`
	NextOffset     int64            `json:"next_offset"`
	TotalSize      int64            `json:"total_size"`
	ChecksumSHA256 string           `json:"checksum_sha256"`
	EOF            bool             `json:"eof"`
}

type mcpAttachmentStartArgs struct {
	Target              mcpAttachmentTargetArgs `json:"target"`
	Filename            string                  `json:"filename"`
	ContentType         string                  `json:"content_type"`
	SizeBytes           *int64                  `json:"size_bytes"`
	ChecksumSHA256      string                  `json:"checksum_sha256"`
	ExpectedRevision    string                  `json:"expected_revision"`
	ReplaceAttachmentID string                  `json:"replace_attachment_id"`
}

type mcpAttachmentUploadStatus struct {
	UploadID      string     `json:"upload_id"`
	Status        string     `json:"status"`
	Filename      string     `json:"filename"`
	ContentType   string     `json:"content_type"`
	ReceivedBytes int64      `json:"received_bytes"`
	NextOffset    int64      `json:"next_offset"`
	SizeBytes     *int64     `json:"size_bytes,omitempty"`
	Complete      bool       `json:"complete"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type mcpAttachmentChunkArgs struct {
	UploadID   string `json:"upload_id"`
	Offset     int64  `json:"offset"`
	DataBase64 string `json:"data_base64"`
	Text       string `json:"text"`
}

type mcpAttachmentFinishArgs struct {
	UploadID                 string `json:"upload_id"`
	ExpectedRevision         string `json:"expected_revision"`
	ConfirmSharedReplacement bool   `json:"confirm_shared_replacement"`
}

type mcpAttachmentFinishResult struct {
	Attachment     protocol.NoteAttachment `json:"attachment"`
	TargetRevision string                  `json:"target_revision"`
	ReferenceCount int                     `json:"reference_count"`
	Status         string                  `json:"status"`
}

type mcpAttachmentError struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	NextOffset      int64  `json:"next_offset,omitempty"`
	CurrentRevision string `json:"current_revision,omitempty"`
}

func (e *mcpAttachmentError) Error() string { return e.Message }

type mcpNoteAttachmentService struct {
	store          *store.Store
	attachmentRoot string
	now            func() time.Time
	uploadLocks    sync.Map
	uploadLocksMu  sync.Mutex
}

type mcpUploadLock struct {
	mutex sync.Mutex
	refs  int
}

func newMCPNoteAttachmentService(db *store.Store, attachmentRoot string, now func() time.Time) *mcpNoteAttachmentService {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &mcpNoteAttachmentService{store: db, attachmentRoot: attachmentRoot, now: now}
}

func (s *mcpNoteAttachmentService) Start(ctx context.Context, userID string, args mcpAttachmentStartArgs) (mcpAttachmentUploadStatus, error) {
	target, err := s.resolveTarget(ctx, userID, args.Target)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	filename := safeAttachmentFilename(args.Filename)
	contentType := strings.ToLower(strings.TrimSpace(args.ContentType))
	if !supportedMCPAttachmentContentType(contentType) {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "unsupported_media_type", Message: "unsupported attachment content type"}
	}
	if args.SizeBytes != nil && (*args.SizeBytes <= 0 || *args.SizeBytes > maxNoteAttachmentBytes) {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "file_too_large", Message: "attachment size must be between 1 byte and 100 MiB"}
	}
	checksum := strings.ToLower(strings.TrimSpace(args.ChecksumSHA256))
	if checksum != "" {
		decoded, decodeErr := hex.DecodeString(checksum)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "invalid_checksum", Message: "checksum_sha256 must be 64 lowercase hexadecimal characters"}
		}
	}
	if strings.TrimSpace(args.ExpectedRevision) == "" || args.ExpectedRevision != target.Note.Revision {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "revision_conflict", Message: "target revision changed", CurrentRevision: target.Note.Revision}
	}
	if args.ReplaceAttachmentID != "" {
		if !markdownReferencesAttachment(target.Markdown, args.ReplaceAttachmentID) {
			return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "attachment_not_referenced_by_target", Message: "attachment is not referenced by the exact target"}
		}
		attachment, attachmentErr := s.store.GetNoteAttachment(ctx, target.Note.ID, args.ReplaceAttachmentID)
		if attachmentErr != nil {
			return mcpAttachmentUploadStatus{}, attachmentErr
		}
		if !supportedMCPAttachmentContentType(attachment.ContentType) {
			return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "unsupported_media_type", Message: "attachment cannot be replaced through MCP"}
		}
	}
	now := s.now().UTC()
	uploadID := newID("mcpup")
	stagingKey, err := mcpAttachmentStagingKey(uploadID)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	upload := domain.MCPNoteAttachmentUpload{
		ID: uploadID, OwnerUserID: userID, NoteRecordID: target.Note.ID, TargetNoteID: target.Note.NoteID,
		TargetKind: args.Target.Kind, BoardID: args.Target.BoardID, CardID: args.Target.CardID,
		ReplacementAttachmentID: args.ReplaceAttachmentID, Filename: filename, ContentType: contentType,
		DeclaredSizeBytes: args.SizeBytes, DeclaredChecksumSHA256: checksum, StagingKey: stagingKey,
		ExpectedRevision: args.ExpectedRevision, Status: "open", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := s.store.CreateMCPNoteAttachmentUpload(ctx, upload); err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	return uploadStatus(upload), nil
}

func (s *mcpNoteAttachmentService) Status(ctx context.Context, userID, uploadID string) (mcpAttachmentUploadStatus, error) {
	upload, err := s.store.GetMCPNoteAttachmentUpload(ctx, strings.TrimSpace(uploadID), userID)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if upload.Status == "open" && !s.now().Before(upload.ExpiresAt) {
		_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "expired", s.now().UTC(), nil)
		upload.Status = "expired"
	}
	return uploadStatus(upload), nil
}

func (s *mcpNoteAttachmentService) UploadChunk(ctx context.Context, userID string, args mcpAttachmentChunkArgs) (mcpAttachmentUploadStatus, error) {
	unlock := s.lockUpload(args.UploadID)
	defer unlock()
	upload, err := s.store.GetMCPNoteAttachmentUpload(ctx, strings.TrimSpace(args.UploadID), userID)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if upload.Status != "open" {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "upload_not_open", Message: "upload is not open"}
	}
	if !s.now().Before(upload.ExpiresAt) {
		_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "expired", s.now().UTC(), nil)
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "upload_expired", Message: "upload expired"}
	}
	if args.Offset != upload.ReceivedBytes {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "offset_conflict", Message: "chunk offset does not match the committed upload offset", NextOffset: upload.ReceivedBytes}
	}
	data, err := decodeMCPAttachmentChunk(upload.ContentType, args.DataBase64, args.Text)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if int64(len(data)) > maxNoteAttachmentBytes-upload.ReceivedBytes || (upload.DeclaredSizeBytes != nil && upload.ReceivedBytes+int64(len(data)) > *upload.DeclaredSizeBytes) {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "file_too_large", Message: "attachment exceeds its size limit"}
	}
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	path, err := server.noteAttachmentPathForWrite(upload.StagingKey)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if err := reconcileOpenStagingFile(file, upload.ReceivedBytes); err != nil {
		_ = file.Close()
		return mcpAttachmentUploadStatus{}, err
	}
	if _, err := file.Seek(upload.ReceivedBytes, io.SeekStart); err != nil {
		_ = file.Close()
		return mcpAttachmentUploadStatus{}, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return mcpAttachmentUploadStatus{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return mcpAttachmentUploadStatus{}, err
	}
	if err := file.Close(); err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	next := upload.ReceivedBytes + int64(len(data))
	if err := s.store.AdvanceMCPNoteAttachmentUpload(ctx, upload.ID, userID, upload.ReceivedBytes, next, s.now().UTC()); err != nil {
		latest, latestErr := s.store.GetMCPNoteAttachmentUpload(ctx, upload.ID, userID)
		if latestErr == nil {
			return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "offset_conflict", Message: "upload offset changed", NextOffset: latest.ReceivedBytes}
		}
		return mcpAttachmentUploadStatus{}, err
	}
	upload.ReceivedBytes = next
	upload.UpdatedAt = s.now().UTC()
	return uploadStatus(upload), nil
}

func (s *mcpNoteAttachmentService) Abort(ctx context.Context, userID, uploadID string) (mcpAttachmentUploadStatus, error) {
	unlock := s.lockUpload(uploadID)
	defer unlock()
	upload, err := s.store.GetMCPNoteAttachmentUpload(ctx, strings.TrimSpace(uploadID), userID)
	if err != nil {
		return mcpAttachmentUploadStatus{}, err
	}
	if upload.Status == "completed" {
		return mcpAttachmentUploadStatus{}, &mcpAttachmentError{Code: "upload_not_open", Message: "completed upload cannot be aborted"}
	}
	if upload.Status == "open" {
		if err := s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "aborted", s.now().UTC(), nil); err != nil {
			return mcpAttachmentUploadStatus{}, err
		}
		upload.Status = "aborted"
	}
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	if path, pathErr := server.noteAttachmentPath(upload.StagingKey); pathErr == nil {
		_ = os.Remove(path)
	}
	return uploadStatus(upload), nil
}

func (s *mcpNoteAttachmentService) Finish(ctx context.Context, userID string, args mcpAttachmentFinishArgs) (mcpAttachmentFinishResult, error) {
	unlock := s.lockUpload(args.UploadID)
	defer unlock()
	upload, err := s.store.GetMCPNoteAttachmentUpload(ctx, strings.TrimSpace(args.UploadID), userID)
	if err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	if upload.Status != "open" {
		return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "upload_not_open", Message: "upload is not open"}
	}
	if !s.now().Before(upload.ExpiresAt) {
		_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "expired", s.now().UTC(), nil)
		return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "upload_expired", Message: "upload expired"}
	}
	targetArgs := mcpAttachmentTargetArgs{Kind: upload.TargetKind, NoteID: upload.TargetNoteID, BoardID: upload.BoardID, CardID: upload.CardID}
	target, err := s.resolveTarget(ctx, userID, targetArgs)
	if err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	expectedRevision := upload.ExpectedRevision
	if strings.TrimSpace(args.ExpectedRevision) != "" {
		expectedRevision = strings.TrimSpace(args.ExpectedRevision)
	}
	if expectedRevision != target.Note.Revision {
		return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "revision_conflict", Message: "target revision changed", CurrentRevision: target.Note.Revision}
	}
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	stagingPath, err := server.noteAttachmentPath(upload.StagingKey)
	if err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	validated, err := validateMCPAttachment(stagingPath, upload.ContentType)
	if err != nil {
		_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", s.now().UTC(), nil)
		return mcpAttachmentFinishResult{}, classifyMCPAttachmentValidationError(err)
	}
	if upload.DeclaredSizeBytes != nil && validated.SizeBytes != *upload.DeclaredSizeBytes {
		_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", s.now().UTC(), nil)
		return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "invalid_checksum", Message: "received attachment length does not match the declared size"}
	}
	if upload.DeclaredChecksumSHA256 != "" {
		declared, _ := hex.DecodeString(upload.DeclaredChecksumSHA256)
		actual, _ := hex.DecodeString(validated.ChecksumSHA256)
		if len(declared) != sha256.Size || len(actual) != sha256.Size || subtle.ConstantTimeCompare(declared, actual) != 1 {
			_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", s.now().UTC(), nil)
			return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "invalid_checksum", Message: "attachment checksum does not match the declared SHA-256"}
		}
	}

	now := s.now().UTC()
	attachmentID := upload.ReplacementAttachmentID
	var oldAttachment domain.NoteAttachment
	replace := attachmentID != ""
	referenceCount := 1
	if replace {
		if !markdownReferencesAttachment(target.Markdown, attachmentID) {
			return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "attachment_not_referenced_by_target", Message: "attachment is no longer referenced by the exact target"}
		}
		oldAttachment, err = s.store.GetNoteAttachment(ctx, target.Note.ID, attachmentID)
		if err != nil {
			return mcpAttachmentFinishResult{}, err
		}
		referenceCount = countAttachmentReferences(target.Note, attachmentID)
		if referenceCount > 1 && !args.ConfirmSharedReplacement {
			return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "shared_replacement_confirmation_required", Message: fmt.Sprintf("attachment is referenced in %d locations", referenceCount)}
		}
	} else {
		attachmentID = newID("natt")
	}
	version := fmt.Sprintf("%d", now.UnixNano())
	storageKey := filepath.Join(target.Note.ID, attachmentID+"-"+version+"-"+safeAttachmentFilename(upload.Filename))
	storagePath, err := server.noteAttachmentPathForWrite(storageKey)
	if err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(storagePath), noteAttachmentDirMode); err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	if err := os.Rename(stagingPath, storagePath); err != nil {
		return mcpAttachmentFinishResult{}, err
	}
	var previewPath string
	restoreStaging := func(cause error) error {
		_ = os.Remove(previewPath)
		if restoreErr := os.Rename(storagePath, stagingPath); restoreErr != nil {
			_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", s.now().UTC(), nil)
			return fmt.Errorf("%v; restore staged attachment: %w", cause, restoreErr)
		}
		if chmodErr := os.Chmod(stagingPath, 0o600); chmodErr != nil {
			_ = s.store.UpdateMCPNoteAttachmentUploadStatus(ctx, upload.ID, userID, "failed", s.now().UTC(), nil)
			return fmt.Errorf("%v; secure restored staging file: %w", cause, chmodErr)
		}
		return cause
	}
	if err := os.Chmod(storagePath, noteAttachmentFileMode); err != nil {
		return mcpAttachmentFinishResult{}, restoreStaging(err)
	}
	attachment := domain.NoteAttachment{
		ID: attachmentID, NoteID: target.Note.ID, HomeID: target.Note.HomeID, OwnerUserID: userID,
		Filename: safeAttachmentFilename(upload.Filename), ContentType: upload.ContentType,
		SizeBytes: validated.SizeBytes, ChecksumSHA256: validated.ChecksumSHA256, StorageKey: storageKey,
		CreatedAt: now, UpdatedAt: now,
	}
	if replace {
		attachment.CreatedAt = oldAttachment.CreatedAt
	}
	if upload.ContentType != "text/html" {
		preview, previewErr := generateMCPImagePreview(storagePath, upload.ContentType)
		if previewErr != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(classifyMCPAttachmentValidationError(previewErr))
		}
		attachment.PreviewStorageKey = filepath.Join(target.Note.ID, attachmentID+"-"+version+"-preview.png")
		previewPath, err = server.noteAttachmentPathForWrite(attachment.PreviewStorageKey)
		if err != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(err)
		}
		if err := os.WriteFile(previewPath, preview.Data, noteAttachmentFileMode); err != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(err)
		}
		attachment.PreviewContentType = preview.ContentType
		attachment.PreviewSizeBytes = preview.SizeBytes
		attachment.PreviewChecksumSHA256 = preview.ChecksumSHA256
	}

	updatedNote := target.Note
	var operation domain.NoteOperation
	if !replace {
		updatedNote, operation, err = materializeMCPAttachmentTarget(target, attachment, userID, now)
		if err != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(err)
		}
	} else {
		operation = replacementMCPAttachmentOperation(target.Note, attachment, userID, now)
		state, stateErr := decodeCollabState(target.Note)
		if stateErr != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(stateErr)
		}
		state.CollabVersion++
		updatedNote, _, err = materializeNoteFromState(target.Note, state, userID, now)
		if err != nil {
			return mcpAttachmentFinishResult{}, restoreStaging(err)
		}
		operation.AppliedVersion = updatedNote.CollabVersion
		operation.OpID = "mcp-attachment-replace:" + updatedNote.Revision
	}
	if err := s.store.FinalizeMCPNoteAttachmentUpload(ctx, upload, attachment, updatedNote, operation, expectedRevision, replace, now); err != nil {
		// Finalization is the commit point. Restore the fully staged original when
		// the transaction loses a revision race so callers can retry Finish with a
		// fresh expected revision instead of uploading the bytes again.
		if restoredErr := restoreStaging(err); !errors.Is(restoredErr, err) {
			return mcpAttachmentFinishResult{}, restoredErr
		}
		if errors.Is(err, store.ErrConflict) {
			latest, latestErr := s.store.GetProfileNote(ctx, userID, target.Note.NoteID)
			current := ""
			if latestErr == nil {
				current = latest.Revision
			}
			return mcpAttachmentFinishResult{}, &mcpAttachmentError{Code: "revision_conflict", Message: "target revision changed during finalization", CurrentRevision: current}
		}
		return mcpAttachmentFinishResult{}, err
	}
	if replace {
		removeMCPAttachmentObject(server, oldAttachment.StorageKey)
		removeMCPAttachmentObject(server, oldAttachment.PreviewStorageKey)
	}
	attachmentProtocol := noteAttachmentToProtocol(attachment, updatedNote, "profile")
	return mcpAttachmentFinishResult{Attachment: attachmentProtocol, TargetRevision: updatedNote.Revision, ReferenceCount: referenceCount, Status: "completed"}, nil
}

func (s *mcpNoteAttachmentService) lockUpload(uploadID string) func() {
	key := strings.TrimSpace(uploadID)
	s.uploadLocksMu.Lock()
	value, ok := s.uploadLocks.Load(key)
	if !ok {
		value = &mcpUploadLock{}
		s.uploadLocks.Store(key, value)
	}
	lock := value.(*mcpUploadLock)
	lock.refs++
	s.uploadLocksMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		s.uploadLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			s.uploadLocks.Delete(key)
		}
		s.uploadLocksMu.Unlock()
	}
}

func replacementMCPAttachmentOperation(note domain.UserNote, attachment domain.NoteAttachment, userID string, now time.Time) domain.NoteOperation {
	payload, _ := json.Marshal(map[string]any{"type": "replace_attachment", "attachment_id": attachment.ID})
	return domain.NoteOperation{NoteID: note.ID, ActorUserID: userID, SessionID: "mcp", BaseVersion: note.CollabVersion, AppliedVersion: note.CollabVersion + 1, OpJSON: string(payload), CreatedAt: now}
}

func removeMCPAttachmentObject(server *Server, storageKey string) {
	if strings.TrimSpace(storageKey) == "" {
		return
	}
	if path, err := server.noteAttachmentPath(storageKey); err == nil {
		_ = os.Remove(path)
	}
}

func classifyMCPAttachmentValidationError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "UTF-8"):
		return &mcpAttachmentError{Code: "invalid_utf8", Message: message}
	case strings.Contains(message, "dimensions"):
		return &mcpAttachmentError{Code: "unsafe_image_dimensions", Message: message}
	case strings.Contains(message, "too large") || strings.Contains(message, "size limit"):
		return &mcpAttachmentError{Code: "file_too_large", Message: message}
	case strings.Contains(message, "image") || strings.Contains(message, "content does not match") || strings.Contains(message, "svg"):
		return &mcpAttachmentError{Code: "invalid_image", Message: message}
	default:
		return &mcpAttachmentError{Code: "storage_unavailable", Message: message}
	}
}

func uploadStatus(upload domain.MCPNoteAttachmentUpload) mcpAttachmentUploadStatus {
	complete := upload.DeclaredSizeBytes != nil && upload.ReceivedBytes == *upload.DeclaredSizeBytes
	return mcpAttachmentUploadStatus{UploadID: upload.ID, Status: upload.Status, Filename: upload.Filename, ContentType: upload.ContentType, ReceivedBytes: upload.ReceivedBytes, NextOffset: upload.ReceivedBytes, SizeBytes: upload.DeclaredSizeBytes, Complete: complete, ExpiresAt: upload.ExpiresAt, CompletedAt: upload.CompletedAt}
}

func materializeMCPAttachmentTarget(target mcpAttachmentTarget, attachment domain.NoteAttachment, userID string, now time.Time) (domain.UserNote, domain.NoteOperation, error) {
	state, err := decodeCollabState(target.Note)
	if err != nil {
		return domain.UserNote{}, domain.NoteOperation{}, err
	}
	reference := noteAttachmentMarkdownReference(target.Note, "profile", attachment)
	if target.Board == nil {
		content := strings.TrimSpace(state.Content)
		if content != "" {
			content += "\n\n"
		}
		state.Content = content + reference
	} else {
		board := cloneKanbanBoard(target.Board)
		if target.ColumnIndex < 0 || target.ColumnIndex >= len(board.Columns) || target.CardIndex < 0 || target.CardIndex >= len(board.Columns[target.ColumnIndex].Cards) {
			return domain.UserNote{}, domain.NoteOperation{}, store.ErrNotFound
		}
		text := strings.TrimSpace(board.Columns[target.ColumnIndex].Cards[target.CardIndex].Text)
		if text != "" {
			text += "\n\n"
		}
		board.Columns[target.ColumnIndex].Cards[target.CardIndex].Text = text + reference
		board.Columns[target.ColumnIndex].Cards[target.CardIndex].UpdatedAt = now
		board.UpdatedAt = now
		state.Board = board
		state.Content = kanbanMarkdown(target.Note.Title, *board)
	}
	baseVersion := state.CollabVersion
	state.CollabVersion++
	updated, operationJSON, err := materializeNoteFromState(target.Note, state, userID, now)
	if err != nil {
		return domain.UserNote{}, domain.NoteOperation{}, err
	}
	return updated, domain.NoteOperation{
		NoteID: updated.ID, OpID: "mcp-attachment:" + updated.Revision, ActorUserID: userID,
		SessionID: "mcp", BaseVersion: baseVersion, AppliedVersion: state.CollabVersion,
		OpJSON: operationJSON, CreatedAt: now,
	}, nil
}

func decodeMCPAttachmentChunk(contentType, encoded, text string) ([]byte, error) {
	hasEncoded := strings.TrimSpace(encoded) != ""
	hasText := text != ""
	if hasEncoded == hasText {
		return nil, &mcpAttachmentError{Code: "invalid_target", Message: "provide exactly one of data_base64 or text"}
	}
	var data []byte
	var err error
	if hasText {
		if contentType != "text/html" {
			return nil, &mcpAttachmentError{Code: "unsupported_media_type", Message: "text chunks are accepted only for text/html"}
		}
		data = []byte(text)
	} else {
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, &mcpAttachmentError{Code: "invalid_target", Message: "data_base64 is not valid base64"}
		}
	}
	if len(data) == 0 {
		return nil, &mcpAttachmentError{Code: "invalid_target", Message: "attachment chunk is empty"}
	}
	if len(data) > maxMCPAttachmentChunkBytes {
		return nil, &mcpAttachmentError{Code: "file_too_large", Message: "attachment chunk exceeds 4 MiB"}
	}
	return data, nil
}

func reconcileOpenStagingFile(file *os.File, committedBytes int64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < committedBytes {
		return errors.New("staging file is shorter than committed offset")
	}
	if info.Size() > committedBytes {
		return file.Truncate(committedBytes)
	}
	return nil
}

func (s *mcpNoteAttachmentService) List(ctx context.Context, userID string, args mcpAttachmentTargetArgs) (mcpAttachmentListResult, error) {
	target, err := s.resolveTarget(ctx, userID, args)
	if err != nil {
		return mcpAttachmentListResult{}, err
	}
	attachments, err := s.store.ListNoteAttachments(ctx, target.Note.ID)
	if err != nil {
		return mcpAttachmentListResult{}, err
	}
	byID := make(map[string]domain.NoteAttachment, len(attachments))
	for _, attachment := range attachments {
		byID[attachment.ID] = attachment
	}
	orderedIDs := attachmentIDsInMarkdown(target.Markdown)
	result := mcpAttachmentListResult{Target: args, Revision: target.Note.Revision, Attachments: make([]mcpAttachmentMetadata, 0, len(orderedIDs))}
	seen := map[string]struct{}{}
	for _, attachmentID := range orderedIDs {
		if _, ok := seen[attachmentID]; ok {
			continue
		}
		attachment, ok := byID[attachmentID]
		if !ok {
			continue
		}
		seen[attachmentID] = struct{}{}
		result.Attachments = append(result.Attachments, mcpAttachmentMetadata{
			NoteAttachment: noteAttachmentToProtocol(attachment, target.Note, "profile"),
			ReferenceCount: countAttachmentReferences(target.Note, attachment.ID),
			Readable:       supportedMCPAttachmentContentType(attachment.ContentType),
			Replaceable:    supportedMCPAttachmentContentType(attachment.ContentType),
		})
	}
	return result, nil
}

func (s *mcpNoteAttachmentService) Read(ctx context.Context, userID string, args mcpAttachmentReadArgs) (mcpAttachmentReadResult, error) {
	target, err := s.resolveTarget(ctx, userID, args.Target)
	if err != nil {
		return mcpAttachmentReadResult{}, err
	}
	if strings.TrimSpace(args.AttachmentID) == "" || !markdownReferencesAttachment(target.Markdown, args.AttachmentID) {
		return mcpAttachmentReadResult{}, store.ErrNotFound
	}
	attachment, err := s.store.GetNoteAttachment(ctx, target.Note.ID, args.AttachmentID)
	if err != nil {
		return mcpAttachmentReadResult{}, err
	}
	representation := strings.TrimSpace(args.Representation)
	if representation == "" || representation == "auto" {
		if attachment.ContentType == "text/html" {
			representation = "html_source"
		} else {
			representation = "image_preview"
		}
	}
	result := mcpAttachmentReadResult{
		AttachmentID: attachment.ID, Representation: representation, Offset: args.Offset,
		TotalSize: attachment.SizeBytes, ChecksumSHA256: attachment.ChecksumSHA256,
		Content: []map[string]any{{"type": "text", "text": fmt.Sprintf("Attachment %s (%s, %d bytes)", attachment.Filename, attachment.ContentType, attachment.SizeBytes)}},
	}
	switch representation {
	case "html_source":
		if attachment.ContentType != "text/html" {
			return mcpAttachmentReadResult{}, errors.New("html_source requires a text/html attachment")
		}
		length := args.Length
		if length <= 0 || length > maxMCPHTMLSourceBytes {
			length = maxMCPHTMLSourceBytes
		}
		data, nextOffset, eof, err := s.readAttachmentRange(attachment.StorageKey, args.Offset, length, true)
		if err != nil {
			return mcpAttachmentReadResult{}, err
		}
		result.Content = append(result.Content, map[string]any{"type": "text", "text": string(data)})
		result.NextOffset, result.EOF = nextOffset, eof
	case "original_chunk":
		length := args.Length
		if length <= 0 || length > maxMCPAttachmentChunkBytes {
			length = maxMCPAttachmentChunkBytes
		}
		data, nextOffset, eof, err := s.readAttachmentRange(attachment.StorageKey, args.Offset, length, false)
		if err != nil {
			return mcpAttachmentReadResult{}, err
		}
		result.Content = append(result.Content, map[string]any{"type": "text", "text": base64.StdEncoding.EncodeToString(data)})
		result.NextOffset, result.EOF = nextOffset, eof
	case "image_preview":
		if attachment.ContentType == "text/html" {
			return mcpAttachmentReadResult{}, errors.New("image preview is unavailable")
		}
		if attachment.PreviewStorageKey == "" {
			attachment, err = s.createHistoricalImagePreview(ctx, attachment)
			if err != nil {
				return mcpAttachmentReadResult{}, err
			}
		}
		data, err := s.readWholeContainedFile(attachment.PreviewStorageKey, maxMCPImagePreviewBytes)
		if err != nil {
			return mcpAttachmentReadResult{}, err
		}
		result.Content = append(result.Content, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": attachment.PreviewContentType})
		result.NextOffset, result.EOF = attachment.SizeBytes, true
	default:
		return mcpAttachmentReadResult{}, errors.New("unsupported attachment representation")
	}
	return result, nil
}

func (s *mcpNoteAttachmentService) createHistoricalImagePreview(ctx context.Context, attachment domain.NoteAttachment) (domain.NoteAttachment, error) {
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	originalPath, err := server.noteAttachmentPath(attachment.StorageKey)
	if err != nil {
		return domain.NoteAttachment{}, err
	}
	if _, err := validateMCPAttachment(originalPath, attachment.ContentType); err != nil {
		return domain.NoteAttachment{}, err
	}
	preview, err := generateMCPImagePreview(originalPath, attachment.ContentType)
	if err != nil {
		return domain.NoteAttachment{}, err
	}
	previewKey := fmt.Sprintf("%s-previews/%s-%d.png", attachment.NoteID, attachment.ID, s.now().UnixNano())
	previewPath, err := server.noteAttachmentPathForWrite(previewKey)
	if err != nil {
		return domain.NoteAttachment{}, err
	}
	if err := os.MkdirAll(filepath.Dir(previewPath), noteAttachmentDirMode); err != nil {
		return domain.NoteAttachment{}, err
	}
	if err := os.WriteFile(previewPath, preview.Data, noteAttachmentFileMode); err != nil {
		return domain.NoteAttachment{}, err
	}
	if err := s.store.UpdateNoteAttachmentPreview(ctx, attachment.NoteID, attachment.ID, previewKey, preview.ContentType, preview.SizeBytes, preview.ChecksumSHA256, s.now()); err != nil {
		_ = os.Remove(previewPath)
		if errors.Is(err, store.ErrConflict) {
			return s.store.GetNoteAttachment(ctx, attachment.NoteID, attachment.ID)
		}
		return domain.NoteAttachment{}, err
	}
	attachment.PreviewStorageKey = previewKey
	attachment.PreviewContentType = preview.ContentType
	attachment.PreviewSizeBytes = preview.SizeBytes
	attachment.PreviewChecksumSHA256 = preview.ChecksumSHA256
	return attachment, nil
}

func (s *mcpNoteAttachmentService) resolveTarget(ctx context.Context, userID string, args mcpAttachmentTargetArgs) (mcpAttachmentTarget, error) {
	notes, err := s.store.ListProfileNotes(ctx, userID, false)
	if err != nil {
		return mcpAttachmentTarget{}, err
	}
	publicNoteID := strings.TrimSpace(args.NoteID)
	if args.Kind == "kanban_card" {
		publicNoteID = strings.TrimSpace(args.BoardID)
	}
	if publicNoteID == "" || !mcpNoteVisible(notes, publicNoteID) {
		return mcpAttachmentTarget{}, store.ErrNotFound
	}
	note, err := s.store.GetProfileNote(ctx, userID, publicNoteID)
	if err != nil {
		return mcpAttachmentTarget{}, err
	}
	switch strings.TrimSpace(args.Kind) {
	case "note":
		if normalizePageType(note.PageType) != protocol.NotePageTypeText {
			return mcpAttachmentTarget{}, errors.New("note target must be a text note")
		}
		return mcpAttachmentTarget{Note: note, ColumnIndex: -1, CardIndex: -1, Markdown: noteBodyText(note)}, nil
	case "kanban_card":
		if normalizePageType(note.PageType) != protocol.NotePageTypeKanban || strings.TrimSpace(args.CardID) == "" {
			return mcpAttachmentTarget{}, store.ErrNotFound
		}
		var board protocol.KanbanBoard
		if err := json.Unmarshal([]byte(note.BoardJSON), &board); err != nil {
			return mcpAttachmentTarget{}, err
		}
		columnIndex, cardIndex, ok := findKanbanCardIndexes(&board, args.CardID)
		if !ok {
			return mcpAttachmentTarget{}, store.ErrNotFound
		}
		return mcpAttachmentTarget{Note: note, Board: &board, ColumnIndex: columnIndex, CardIndex: cardIndex, Markdown: board.Columns[columnIndex].Cards[cardIndex].Text}, nil
	default:
		return mcpAttachmentTarget{}, errors.New("target kind must be note or kanban_card")
	}
}

func attachmentIDsInMarkdown(markdown string) []string {
	matches := mcpAttachmentReferenceFinder.FindAllStringSubmatch(markdown, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		decoded, err := url.PathUnescape(match[1])
		if err == nil && strings.TrimSpace(decoded) != "" {
			result = append(result, decoded)
		}
	}
	return result
}

func markdownReferencesAttachment(markdown, attachmentID string) bool {
	for _, candidate := range attachmentIDsInMarkdown(markdown) {
		if candidate == attachmentID {
			return true
		}
	}
	return false
}

func countAttachmentReferences(note domain.UserNote, attachmentID string) int {
	pattern, err := noteAttachmentReferencePattern(attachmentID)
	if err != nil {
		return 0
	}
	if normalizePageType(note.PageType) != protocol.NotePageTypeKanban || strings.TrimSpace(note.BoardJSON) == "" {
		return len(pattern.FindAllStringIndex(noteBodyText(note), -1))
	}
	var board protocol.KanbanBoard
	if json.Unmarshal([]byte(note.BoardJSON), &board) != nil {
		return len(pattern.FindAllStringIndex(noteBodyText(note), -1))
	}
	count := 0
	for _, column := range board.Columns {
		for _, card := range column.Cards {
			count += len(pattern.FindAllStringIndex(card.Text, -1))
		}
	}
	return count
}

func (s *mcpNoteAttachmentService) readAttachmentRange(storageKey string, offset, length int64, preserveUTF8 bool) ([]byte, int64, bool, error) {
	if offset < 0 || length <= 0 {
		return nil, 0, false, errors.New("invalid attachment range")
	}
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	path, err := server.noteAttachmentPath(storageKey)
	if err != nil {
		return nil, 0, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if offset > info.Size() {
		return nil, 0, false, errors.New("attachment offset exceeds file size")
	}
	want := min(length, info.Size()-offset)
	data := make([]byte, want)
	read, err := io.ReadFull(io.NewSectionReader(file, offset, want), data)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, 0, false, err
	}
	data = data[:read]
	if preserveUTF8 && offset+int64(len(data)) < info.Size() {
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	next := offset + int64(len(data))
	return data, next, next >= info.Size(), nil
}

func (s *mcpNoteAttachmentService) readWholeContainedFile(storageKey string, limit int64) ([]byte, error) {
	server := &Server{noteAttachmentRoot: s.attachmentRoot}
	path, err := server.noteAttachmentPath(storageKey)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("attachment preview exceeds size limit")
	}
	return data, nil
}
