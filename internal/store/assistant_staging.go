package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dropfile/HankServerside/internal/domain"
)

// BindAssistantStage runs only after a durable, checksum-verified file exists.
// A lost response can repeat the binding; changed metadata cannot overwrite it.
func (s *Store) BindAssistantStage(ctx context.Context, stage domain.AssistantStage) (domain.AssistantStage, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return stage, err
	}
	defer tx.Rollback()
	var session string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM assistant_sessions WHERE id=? AND home_id=? AND user_id=? FOR UPDATE`, stage.SessionID, stage.HomeID, stage.UserID).Scan(&session); err != nil {
		return domain.AssistantStage{}, ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_staged_attachments(id,home_id,user_id,session_id,client_attachment_id,storage_key,filename,content_type,size_bytes,checksum_sha256) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,client_attachment_id) DO NOTHING`, stage.ID, stage.HomeID, stage.UserID, stage.SessionID, stage.ClientAttachmentID, stage.StorageKey, stage.Filename, stage.ContentType, stage.SizeBytes, stage.ChecksumSHA256)
	if err != nil {
		return stage, err
	}
	saved, err := scanAssistantStage(tx.QueryRowContext(ctx, `SELECT `+assistantStageColumns+` FROM assistant_staged_attachments WHERE session_id=? AND client_attachment_id=? AND expires_at>clock_timestamp()`, stage.SessionID, stage.ClientAttachmentID))
	if err != nil {
		return saved, err
	}
	if saved.StorageKey != stage.StorageKey || saved.Filename != stage.Filename || saved.ContentType != stage.ContentType || saved.SizeBytes != stage.SizeBytes || saved.ChecksumSHA256 != stage.ChecksumSHA256 {
		return domain.AssistantStage{}, ErrConflict
	}
	return saved, tx.Commit()
}

const assistantStageColumns = `id,home_id,user_id,session_id,client_attachment_id,storage_key,filename,content_type,size_bytes,checksum_sha256,created_at,expires_at`

func scanAssistantStage(row rowScanner) (domain.AssistantStage, error) {
	var stage domain.AssistantStage
	err := row.Scan(&stage.ID, &stage.HomeID, &stage.UserID, &stage.SessionID, &stage.ClientAttachmentID, &stage.StorageKey, &stage.Filename, &stage.ContentType, &stage.SizeBytes, &stage.ChecksumSHA256, &stage.CreatedAt, &stage.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return stage, err
}
func (s *Store) GetAssistantStage(ctx context.Context, home, user, session, id string) (domain.AssistantStage, error) {
	return scanAssistantStage(s.queryRow(ctx, `SELECT `+assistantStageColumns+` FROM assistant_staged_attachments WHERE id=? AND home_id=? AND user_id=? AND session_id=? AND expires_at>clock_timestamp()`, id, home, user, session))
}

func (s *Store) ListAssistantStages(ctx context.Context, home, user, session string) ([]domain.AssistantStage, error) {
	rows, err := s.query(ctx, `SELECT `+assistantStageColumns+` FROM assistant_staged_attachments WHERE home_id=? AND user_id=? AND session_id=? AND expires_at>clock_timestamp() ORDER BY created_at,id LIMIT 51`, home, user, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []domain.AssistantStage{}
	for rows.Next() {
		stage, err := scanAssistantStage(rows)
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	return stages, rows.Err()
}
