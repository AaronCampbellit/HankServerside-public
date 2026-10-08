package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (s *Store) UpsertAssistantAttachments(ctx context.Context, attachments []domain.AssistantAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, attachment := range attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_attachments (
				id, session_id, user_id, client_attachment_id, filename, content_type, kind,
				size_bytes, checksum_sha256, status, created_at, updated_at, committed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id, client_attachment_id) DO UPDATE SET
				filename = excluded.filename,
				content_type = excluded.content_type,
				kind = excluded.kind,
				size_bytes = excluded.size_bytes,
				checksum_sha256 = excluded.checksum_sha256,
				status = excluded.status,
				updated_at = excluded.updated_at,
				committed_at = excluded.committed_at`,
			attachment.ID,
			attachment.SessionID,
			attachment.UserID,
			attachment.ClientAttachmentID,
			attachment.Filename,
			attachment.ContentType,
			attachment.Kind,
			attachment.SizeBytes,
			attachment.ChecksumSHA256,
			attachment.Status,
			attachment.CreatedAt,
			attachment.UpdatedAt,
			attachment.CommittedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListAssistantAttachments(ctx context.Context, sessionID string) ([]domain.AssistantAttachment, error) {
	rows, err := s.query(ctx, `SELECT id, session_id, user_id, client_attachment_id, filename, content_type, kind,
			size_bytes, checksum_sha256, status, created_at, updated_at, committed_at
		FROM assistant_attachments
		WHERE session_id = ?
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attachments []domain.AssistantAttachment
	for rows.Next() {
		attachment, err := scanAssistantAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) ListStagedAssistantAttachments(ctx context.Context, sessionID string) ([]domain.AssistantAttachment, error) {
	rows, err := s.query(ctx, `SELECT id, session_id, user_id, client_attachment_id, filename, content_type, kind,
			size_bytes, checksum_sha256, status, created_at, updated_at, committed_at
		FROM assistant_attachments
		WHERE session_id = ? AND status = 'staged'
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attachments []domain.AssistantAttachment
	for rows.Next() {
		attachment, err := scanAssistantAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) MarkAssistantAttachmentsCommitted(ctx context.Context, sessionID string, clientAttachmentIDs []string, committedAt time.Time) error {
	if len(clientAttachmentIDs) == 0 {
		return nil
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, id := range clientAttachmentIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE assistant_attachments
			SET status = 'committed', updated_at = ?, committed_at = ?
			WHERE session_id = ? AND client_attachment_id = ?`,
			committedAt,
			committedAt,
			sessionID,
			id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkAssistantAttachmentsExpired(ctx context.Context, sessionID string, clientAttachmentIDs []string, expiredAt time.Time) error {
	if len(clientAttachmentIDs) == 0 {
		return nil
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, id := range clientAttachmentIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE assistant_attachments
			SET status = 'expired', updated_at = ?
			WHERE session_id = ? AND client_attachment_id = ? AND status = 'staged'`,
			expiredAt,
			sessionID,
			id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CreateNoteAttachment(ctx context.Context, attachment domain.NoteAttachment) error {
	_, err := s.exec(ctx, `INSERT INTO note_attachments (
			id, note_id, home_id, owner_user_id, filename, content_type, size_bytes,
			checksum_sha256, storage_key, preview_storage_key, preview_content_type,
			preview_size_bytes, preview_checksum_sha256, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attachment.ID,
		attachment.NoteID,
		nullableText(attachment.HomeID),
		attachment.OwnerUserID,
		attachment.Filename,
		attachment.ContentType,
		attachment.SizeBytes,
		attachment.ChecksumSHA256,
		attachment.StorageKey,
		nullableText(attachment.PreviewStorageKey),
		nullableText(attachment.PreviewContentType),
		nullableInt64(attachment.PreviewSizeBytes),
		nullableText(attachment.PreviewChecksumSHA256),
		attachment.DeletedAt,
		attachment.CreatedAt,
		attachment.UpdatedAt,
	)
	return err
}

func (s *Store) CreateNoteAttachmentAndSaveNote(ctx context.Context, attachment domain.NoteAttachment, note domain.UserNote) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO note_attachments (
			id, note_id, home_id, owner_user_id, filename, content_type, size_bytes,
			checksum_sha256, storage_key, preview_storage_key, preview_content_type,
			preview_size_bytes, preview_checksum_sha256, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attachment.ID,
		attachment.NoteID,
		nullableText(attachment.HomeID),
		attachment.OwnerUserID,
		attachment.Filename,
		attachment.ContentType,
		attachment.SizeBytes,
		attachment.ChecksumSHA256,
		attachment.StorageKey,
		nullableText(attachment.PreviewStorageKey),
		nullableText(attachment.PreviewContentType),
		nullableInt64(attachment.PreviewSizeBytes),
		nullableText(attachment.PreviewChecksumSHA256),
		attachment.DeletedAt,
		attachment.CreatedAt,
		attachment.UpdatedAt,
	); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO user_notes (
				id, note_id, owner_user_id, home_id, parent_id, sort_order, title, body_markdown, body_format, page_type, board_json,
				revision, checksum, crdt_state_json, collab_version, deleted_at, created_at, updated_at, updated_by
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				note_id = excluded.note_id,
				owner_user_id = excluded.owner_user_id,
				home_id = excluded.home_id,
				parent_id = excluded.parent_id,
				sort_order = excluded.sort_order,
				title = excluded.title,
				body_markdown = excluded.body_markdown,
				body_format = excluded.body_format,
				page_type = excluded.page_type,
				board_json = excluded.board_json,
				revision = excluded.revision,
				checksum = excluded.checksum,
				crdt_state_json = excluded.crdt_state_json,
				collab_version = excluded.collab_version,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at,
				updated_by = excluded.updated_by`,
		note.ID,
		note.NoteID,
		note.OwnerUserID,
		nullableText(note.HomeID),
		nullableText(note.ParentID),
		note.SortOrder,
		note.Title,
		noteBodyMarkdown(note),
		noteBodyFormat(note),
		note.PageType,
		note.BoardJSON,
		note.Revision,
		note.Checksum,
		note.CRDTStateJSON,
		note.CollabVersion,
		note.DeletedAt,
		note.CreatedAt,
		note.UpdatedAt,
		note.UpdatedBy,
	); err != nil {
		return err
	}

	if err := txNotifyNoteChanged(ctx, tx, note, "notes.changed"); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ListNoteAttachments(ctx context.Context, noteID string) ([]domain.NoteAttachment, error) {
	rows, err := s.query(ctx, `SELECT id, note_id, home_id, owner_user_id, filename, content_type,
			size_bytes, checksum_sha256, storage_key, preview_storage_key, preview_content_type,
			preview_size_bytes, preview_checksum_sha256, deleted_at, created_at, updated_at
		FROM note_attachments
		WHERE note_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attachments []domain.NoteAttachment
	for rows.Next() {
		attachment, err := scanNoteAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) ListLiveNoteAttachmentInventory(ctx context.Context) ([]domain.NoteAttachmentInventoryRecord, error) {
	rows, err := s.query(ctx, `SELECT
			na.id, na.note_id, na.home_id, na.owner_user_id, na.filename, na.content_type,
			na.size_bytes, na.checksum_sha256, na.storage_key, na.preview_storage_key, na.preview_content_type,
			na.preview_size_bytes, na.preview_checksum_sha256, na.deleted_at, na.created_at, na.updated_at,
			un.note_id, un.title,
			CASE WHEN un.home_id IS NULL THEN 'profile' ELSE 'home' END,
			un.page_type, u.email, un.body_markdown, un.board_json
		FROM note_attachments na
		JOIN user_notes un ON un.id = na.note_id
		JOIN users u ON u.id = na.owner_user_id
		WHERE na.deleted_at IS NULL AND un.deleted_at IS NULL
		ORDER BY na.created_at DESC, na.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []domain.NoteAttachmentInventoryRecord{}
	for rows.Next() {
		item, err := scanNoteAttachmentInventory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetLiveNoteAttachmentInventoryByID(ctx context.Context, attachmentID string) (domain.NoteAttachmentInventoryRecord, error) {
	row := s.queryRow(ctx, `SELECT
			na.id, na.note_id, na.home_id, na.owner_user_id, na.filename, na.content_type,
			na.size_bytes, na.checksum_sha256, na.storage_key, na.preview_storage_key, na.preview_content_type,
			na.preview_size_bytes, na.preview_checksum_sha256, na.deleted_at, na.created_at, na.updated_at,
			un.note_id, un.title,
			CASE WHEN un.home_id IS NULL THEN 'profile' ELSE 'home' END,
			un.page_type, u.email, un.body_markdown, un.board_json
		FROM note_attachments na
		JOIN user_notes un ON un.id = na.note_id
		JOIN users u ON u.id = na.owner_user_id
		WHERE na.id = ? AND na.deleted_at IS NULL AND un.deleted_at IS NULL`, attachmentID)
	return scanNoteAttachmentInventory(row)
}

func (s *Store) ListLiveNoteAttachmentStorageKeys(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.query(ctx, `SELECT storage_key FROM note_attachments WHERE deleted_at IS NULL
		UNION ALL SELECT preview_storage_key FROM note_attachments WHERE deleted_at IS NULL AND preview_storage_key IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := map[string]struct{}{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = struct{}{}
	}
	return keys, rows.Err()
}

func (s *Store) ListMCPNoteAttachmentStagingKeys(ctx context.Context) (map[string]struct{}, []string, error) {
	rows, err := s.query(ctx, `SELECT staging_key, status FROM mcp_note_attachment_uploads`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	live := map[string]struct{}{}
	var removable []string
	for rows.Next() {
		var key, status string
		if err := rows.Scan(&key, &status); err != nil {
			return nil, nil, err
		}
		if status == "open" {
			live[key] = struct{}{}
		} else {
			removable = append(removable, key)
		}
	}
	return live, removable, rows.Err()
}

func (s *Store) MCPNoteAttachmentUploadStats(ctx context.Context) (map[string]int64, int64, error) {
	rows, err := s.query(ctx, `SELECT status, COUNT(*), COALESCE(SUM(CASE WHEN status = 'open' THEN received_bytes ELSE 0 END), 0)
		FROM mcp_note_attachment_uploads GROUP BY status`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	counts := map[string]int64{"open": 0, "completed": 0, "aborted": 0, "failed": 0, "expired": 0}
	var stagedBytes int64
	for rows.Next() {
		var status string
		var count, bytes int64
		if err := rows.Scan(&status, &count, &bytes); err != nil {
			return nil, 0, err
		}
		counts[status] = count
		stagedBytes += bytes
	}
	return counts, stagedBytes, rows.Err()
}

func (s *Store) GetNoteAttachment(ctx context.Context, noteID string, attachmentID string) (domain.NoteAttachment, error) {
	row := s.queryRow(ctx, `SELECT id, note_id, home_id, owner_user_id, filename, content_type,
			size_bytes, checksum_sha256, storage_key, preview_storage_key, preview_content_type,
			preview_size_bytes, preview_checksum_sha256, deleted_at, created_at, updated_at
		FROM note_attachments
		WHERE note_id = ? AND id = ? AND deleted_at IS NULL`, noteID, attachmentID)
	return scanNoteAttachment(row)
}

func (s *Store) UpdateNoteAttachmentPreview(ctx context.Context, noteID, attachmentID, storageKey, contentType string, sizeBytes int64, checksum string, updatedAt time.Time) error {
	result, err := s.exec(ctx, `UPDATE note_attachments
		SET preview_storage_key = ?, preview_content_type = ?, preview_size_bytes = ?,
			preview_checksum_sha256 = ?, updated_at = ?
		WHERE note_id = ? AND id = ? AND deleted_at IS NULL AND preview_storage_key IS NULL`,
		storageKey, contentType, sizeBytes, checksum, updatedAt, noteID, attachmentID)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return countErr
	} else if count == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) DeleteNoteAttachment(ctx context.Context, noteID string, attachmentID string, deletedAt time.Time) error {
	result, err := s.exec(ctx, `UPDATE note_attachments
		SET deleted_at = ?, updated_at = ?
		WHERE note_id = ? AND id = ? AND deleted_at IS NULL`,
		deletedAt,
		deletedAt,
		noteID,
		attachmentID,
	)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNoteAttachmentAndSaveNote(ctx context.Context, noteID string, attachmentID string, deletedAt time.Time, note domain.UserNote) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE note_attachments
		SET deleted_at = ?, updated_at = ?
		WHERE note_id = ? AND id = ? AND deleted_at IS NULL`,
		deletedAt,
		deletedAt,
		noteID,
		attachmentID,
	)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO user_notes (
				id, note_id, owner_user_id, home_id, parent_id, sort_order, title, body_markdown, body_format, page_type, board_json,
				revision, checksum, crdt_state_json, collab_version, deleted_at, created_at, updated_at, updated_by
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				note_id = excluded.note_id,
				owner_user_id = excluded.owner_user_id,
				home_id = excluded.home_id,
				parent_id = excluded.parent_id,
				sort_order = excluded.sort_order,
				title = excluded.title,
				body_markdown = excluded.body_markdown,
				body_format = excluded.body_format,
				page_type = excluded.page_type,
				board_json = excluded.board_json,
				revision = excluded.revision,
				checksum = excluded.checksum,
				crdt_state_json = excluded.crdt_state_json,
				collab_version = excluded.collab_version,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at,
				updated_by = excluded.updated_by`,
		note.ID,
		note.NoteID,
		note.OwnerUserID,
		nullableText(note.HomeID),
		nullableText(note.ParentID),
		note.SortOrder,
		note.Title,
		noteBodyMarkdown(note),
		noteBodyFormat(note),
		note.PageType,
		note.BoardJSON,
		note.Revision,
		note.Checksum,
		note.CRDTStateJSON,
		note.CollabVersion,
		note.DeletedAt,
		note.CreatedAt,
		note.UpdatedAt,
		note.UpdatedBy,
	); err != nil {
		return err
	}

	if err := txNotifyNoteChanged(ctx, tx, note, "notes.changed"); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) CreateMCPNoteAttachmentUpload(ctx context.Context, upload domain.MCPNoteAttachmentUpload) error {
	_, err := s.exec(ctx, `INSERT INTO mcp_note_attachment_uploads (
		id, owner_user_id, note_record_id, target_note_id, target_kind, board_id, card_id,
		replacement_attachment_id, filename, content_type, declared_size_bytes,
		declared_checksum_sha256, received_bytes, staging_key, expected_revision, status,
		created_at, updated_at, expires_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		upload.ID, upload.OwnerUserID, upload.NoteRecordID, upload.TargetNoteID, upload.TargetKind,
		upload.BoardID, upload.CardID, nullableText(upload.ReplacementAttachmentID), upload.Filename,
		upload.ContentType, upload.DeclaredSizeBytes, upload.DeclaredChecksumSHA256, upload.ReceivedBytes,
		upload.StagingKey, upload.ExpectedRevision, upload.Status, upload.CreatedAt, upload.UpdatedAt,
		upload.ExpiresAt, upload.CompletedAt,
	)
	return err
}

func (s *Store) GetMCPNoteAttachmentUpload(ctx context.Context, uploadID, ownerUserID string) (domain.MCPNoteAttachmentUpload, error) {
	row := s.queryRow(ctx, `SELECT id, owner_user_id, note_record_id, target_note_id, target_kind,
		board_id, card_id, replacement_attachment_id, filename, content_type, declared_size_bytes,
		declared_checksum_sha256, received_bytes, staging_key, expected_revision, status,
		created_at, updated_at, expires_at, completed_at
		FROM mcp_note_attachment_uploads WHERE id = ? AND owner_user_id = ?`, uploadID, ownerUserID)
	return scanMCPNoteAttachmentUpload(row)
}

func (s *Store) AdvanceMCPNoteAttachmentUpload(ctx context.Context, uploadID, ownerUserID string, expectedOffset, nextOffset int64, updatedAt time.Time) error {
	result, err := s.exec(ctx, `UPDATE mcp_note_attachment_uploads
		SET received_bytes = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND status = 'open' AND received_bytes = ?`,
		nextOffset, updatedAt, uploadID, ownerUserID, expectedOffset)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if _, err := s.GetMCPNoteAttachmentUpload(ctx, uploadID, ownerUserID); err != nil {
		return err
	}
	return ErrConflict
}

func (s *Store) UpdateMCPNoteAttachmentUploadStatus(ctx context.Context, uploadID, ownerUserID, status string, updatedAt time.Time, completedAt *time.Time) error {
	result, err := s.exec(ctx, `UPDATE mcp_note_attachment_uploads
		SET status = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND owner_user_id = ?`, status, updatedAt, completedAt, uploadID, ownerUserID)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return countErr
	} else if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListExpiredMCPNoteAttachmentUploads(ctx context.Context, now time.Time) ([]domain.MCPNoteAttachmentUpload, error) {
	rows, err := s.query(ctx, `SELECT id, owner_user_id, note_record_id, target_note_id, target_kind,
		board_id, card_id, replacement_attachment_id, filename, content_type, declared_size_bytes,
		declared_checksum_sha256, received_bytes, staging_key, expected_revision, status,
		created_at, updated_at, expires_at, completed_at
		FROM mcp_note_attachment_uploads WHERE status = 'open' AND expires_at <= ?
		ORDER BY expires_at ASC, id ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uploads []domain.MCPNoteAttachmentUpload
	for rows.Next() {
		upload, scanErr := scanMCPNoteAttachmentUpload(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		uploads = append(uploads, upload)
	}
	return uploads, rows.Err()
}

func (s *Store) FinalizeMCPNoteAttachmentUpload(ctx context.Context, upload domain.MCPNoteAttachmentUpload, attachment domain.NoteAttachment, note domain.UserNote, operation domain.NoteOperation, expectedRevision string, replace bool, completedAt time.Time) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM mcp_note_attachment_uploads
		WHERE id = ? AND owner_user_id = ? FOR UPDATE`, upload.ID, upload.OwnerUserID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != "open" {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE user_notes SET
		mcp_excluded = ?, pinned = ?, sort_order = ?, title = ?, body_markdown = ?, body_format = ?,
		page_type = ?, board_json = ?, revision = ?, checksum = ?, crdt_state_json = ?,
		collab_version = ?, updated_at = ?, updated_by = ?
		WHERE id = ? AND owner_user_id = ? AND revision = ? AND deleted_at IS NULL`,
		note.MCPExcluded, note.Pinned, note.SortOrder, note.Title, noteBodyMarkdown(note), noteBodyFormat(note),
		note.PageType, note.BoardJSON, note.Revision, note.Checksum, note.CRDTStateJSON,
		note.CollabVersion, note.UpdatedAt, note.UpdatedBy, note.ID, note.OwnerUserID, expectedRevision)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return countErr
	} else if count == 0 {
		return ErrConflict
	}

	if replace {
		result, err = tx.ExecContext(ctx, `UPDATE note_attachments SET
			filename = ?, content_type = ?, size_bytes = ?, checksum_sha256 = ?, storage_key = ?,
			preview_storage_key = ?, preview_content_type = ?, preview_size_bytes = ?,
			preview_checksum_sha256 = ?, updated_at = ?
			WHERE id = ? AND note_id = ? AND owner_user_id = ? AND deleted_at IS NULL`,
			attachment.Filename, attachment.ContentType, attachment.SizeBytes, attachment.ChecksumSHA256,
			attachment.StorageKey, nullableText(attachment.PreviewStorageKey), nullableText(attachment.PreviewContentType),
			nullableInt64(attachment.PreviewSizeBytes), nullableText(attachment.PreviewChecksumSHA256), attachment.UpdatedAt,
			attachment.ID, attachment.NoteID, attachment.OwnerUserID)
	} else {
		result, err = tx.ExecContext(ctx, `INSERT INTO note_attachments (
			id, note_id, home_id, owner_user_id, filename, content_type, size_bytes, checksum_sha256,
			storage_key, preview_storage_key, preview_content_type, preview_size_bytes,
			preview_checksum_sha256, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
			attachment.ID, attachment.NoteID, nullableText(attachment.HomeID), attachment.OwnerUserID,
			attachment.Filename, attachment.ContentType, attachment.SizeBytes, attachment.ChecksumSHA256,
			attachment.StorageKey, nullableText(attachment.PreviewStorageKey), nullableText(attachment.PreviewContentType),
			nullableInt64(attachment.PreviewSizeBytes), nullableText(attachment.PreviewChecksumSHA256),
			attachment.CreatedAt, attachment.UpdatedAt)
	}
	if err != nil {
		return err
	}
	if replace {
		if count, countErr := result.RowsAffected(); countErr != nil {
			return countErr
		} else if count == 0 {
			return ErrNotFound
		}
	}

	sessionID, err := nullableExistingSessionID(ctx, tx, operation.SessionID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO note_operations (
		note_id, op_id, actor_user_id, session_id, base_version, applied_version, op_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		operation.NoteID, operation.OpID, operation.ActorUserID, sessionID, operation.BaseVersion,
		operation.AppliedVersion, operation.OpJSON, operation.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_note_attachment_uploads
		SET status = 'completed', updated_at = ?, completed_at = ?
		WHERE id = ? AND owner_user_id = ? AND status = 'open'`,
		completedAt, completedAt, upload.ID, upload.OwnerUserID); err != nil {
		return err
	}
	if err := txNotifyNoteChanged(ctx, tx, note, "notes.changed"); err != nil {
		return err
	}
	return tx.Commit()
}

func scanAssistantAttachment(scanner interface{ Scan(dest ...any) error }) (domain.AssistantAttachment, error) {
	var attachment domain.AssistantAttachment
	var committedAt sql.NullTime
	if err := scanner.Scan(
		&attachment.ID,
		&attachment.SessionID,
		&attachment.UserID,
		&attachment.ClientAttachmentID,
		&attachment.Filename,
		&attachment.ContentType,
		&attachment.Kind,
		&attachment.SizeBytes,
		&attachment.ChecksumSHA256,
		&attachment.Status,
		&attachment.CreatedAt,
		&attachment.UpdatedAt,
		&committedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantAttachment{}, ErrNotFound
		}
		return domain.AssistantAttachment{}, err
	}
	if committedAt.Valid {
		attachment.CommittedAt = &committedAt.Time
	}
	return attachment, nil
}

func scanNoteAttachment(scanner interface{ Scan(dest ...any) error }) (domain.NoteAttachment, error) {
	var attachment domain.NoteAttachment
	var homeID sql.NullString
	var previewStorageKey, previewContentType, previewChecksum sql.NullString
	var previewSize sql.NullInt64
	var deletedAt sql.NullTime
	if err := scanner.Scan(
		&attachment.ID,
		&attachment.NoteID,
		&homeID,
		&attachment.OwnerUserID,
		&attachment.Filename,
		&attachment.ContentType,
		&attachment.SizeBytes,
		&attachment.ChecksumSHA256,
		&attachment.StorageKey,
		&previewStorageKey,
		&previewContentType,
		&previewSize,
		&previewChecksum,
		&deletedAt,
		&attachment.CreatedAt,
		&attachment.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.NoteAttachment{}, ErrNotFound
		}
		return domain.NoteAttachment{}, err
	}
	if homeID.Valid {
		attachment.HomeID = homeID.String
	}
	if previewStorageKey.Valid {
		attachment.PreviewStorageKey = previewStorageKey.String
		attachment.PreviewContentType = previewContentType.String
		attachment.PreviewSizeBytes = previewSize.Int64
		attachment.PreviewChecksumSHA256 = previewChecksum.String
	}
	if deletedAt.Valid {
		attachment.DeletedAt = &deletedAt.Time
	}
	return attachment, nil
}

func scanNoteAttachmentInventory(scanner interface{ Scan(dest ...any) error }) (domain.NoteAttachmentInventoryRecord, error) {
	var item domain.NoteAttachmentInventoryRecord
	var homeID sql.NullString
	var previewStorageKey, previewContentType, previewChecksum sql.NullString
	var previewSize sql.NullInt64
	var deletedAt sql.NullTime
	err := scanner.Scan(
		&item.Attachment.ID,
		&item.Attachment.NoteID,
		&homeID,
		&item.Attachment.OwnerUserID,
		&item.Attachment.Filename,
		&item.Attachment.ContentType,
		&item.Attachment.SizeBytes,
		&item.Attachment.ChecksumSHA256,
		&item.Attachment.StorageKey,
		&previewStorageKey,
		&previewContentType,
		&previewSize,
		&previewChecksum,
		&deletedAt,
		&item.Attachment.CreatedAt,
		&item.Attachment.UpdatedAt,
		&item.NoteID,
		&item.NoteTitle,
		&item.NoteScope,
		&item.NotePageType,
		&item.OwnerEmail,
		&item.BodyMarkdown,
		&item.BoardJSON,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.NoteAttachmentInventoryRecord{}, ErrNotFound
		}
		return domain.NoteAttachmentInventoryRecord{}, err
	}
	if homeID.Valid {
		item.Attachment.HomeID = homeID.String
	}
	if previewStorageKey.Valid {
		item.Attachment.PreviewStorageKey = previewStorageKey.String
		item.Attachment.PreviewContentType = previewContentType.String
		item.Attachment.PreviewSizeBytes = previewSize.Int64
		item.Attachment.PreviewChecksumSHA256 = previewChecksum.String
	}
	if deletedAt.Valid {
		item.Attachment.DeletedAt = &deletedAt.Time
	}
	return item, nil
}

func scanMCPNoteAttachmentUpload(scanner interface{ Scan(dest ...any) error }) (domain.MCPNoteAttachmentUpload, error) {
	var upload domain.MCPNoteAttachmentUpload
	var replacement sql.NullString
	var declaredSize sql.NullInt64
	var completedAt sql.NullTime
	err := scanner.Scan(
		&upload.ID, &upload.OwnerUserID, &upload.NoteRecordID, &upload.TargetNoteID,
		&upload.TargetKind, &upload.BoardID, &upload.CardID, &replacement, &upload.Filename,
		&upload.ContentType, &declaredSize, &upload.DeclaredChecksumSHA256, &upload.ReceivedBytes,
		&upload.StagingKey, &upload.ExpectedRevision, &upload.Status, &upload.CreatedAt,
		&upload.UpdatedAt, &upload.ExpiresAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.MCPNoteAttachmentUpload{}, ErrNotFound
		}
		return domain.MCPNoteAttachmentUpload{}, err
	}
	if replacement.Valid {
		upload.ReplacementAttachmentID = replacement.String
	}
	if declaredSize.Valid {
		upload.DeclaredSizeBytes = &declaredSize.Int64
	}
	if completedAt.Valid {
		upload.CompletedAt = &completedAt.Time
	}
	return upload, nil
}
