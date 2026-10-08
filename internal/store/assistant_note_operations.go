package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

// CommitAssistantNoteOperation atomically saves the collaborative note, emits
// its existing realtime notification, verifies the stored bytes and completes
// the operation receipt. An accepted local receipt is safe to recover because
// no note effect can commit independently of its confirmed receipt.
func (s *Store) CommitAssistantNoteOperation(ctx context.Context, task domain.AssistantTask, receipt domain.AssistantOperationReceipt, note domain.UserNote, expectedRevision string, operation domain.NoteOperation) (domain.AssistantTask, error) {
	if receipt.Outcome != "confirmed" || (receipt.Tool != "notes.create" && receipt.Tool != "notes.append") || note.UpdatedBy != task.UserID || operation.ActorUserID != task.UserID || operation.NoteID != note.ID || (note.HomeID != "" && note.HomeID != task.HomeID) {
		return task, ErrConflict
	}
	return s.commitAssistantLocalOperation(ctx, task, receipt, func(tx *dbTx) error {
		current, err := scanUserNote(tx.QueryRowContext(ctx, `SELECT `+userNoteColumns+` FROM user_notes WHERE id=? FOR UPDATE`, note.ID))
		if receipt.Tool == "notes.create" {
			if !errors.Is(err, ErrNotFound) {
				if err != nil {
					return err
				}
				return ErrConflict
			}
			if expectedRevision != "" || note.OwnerUserID != task.UserID || note.HomeID != "" {
				return ErrConflict
			}
		} else {
			if err != nil {
				return err
			}
			if expectedRevision == "" || current.Revision != expectedRevision || current.DeletedAt != nil || current.OwnerUserID != note.OwnerUserID || current.HomeID != note.HomeID || current.NoteID != note.NoteID {
				return ErrConflict
			}
			allowed := current.OwnerUserID == task.UserID
			if !allowed && current.HomeID == task.HomeID {
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM note_shares WHERE note_id=? AND home_id=? AND target_user_id=?)`, note.ID, task.HomeID, task.UserID).Scan(&allowed)
				if err != nil {
					return err
				}
			}
			if !allowed {
				return ErrNotFound
			}
			// Metadata outside the append itself remains authoritative in the row.
			note.MCPExcluded = current.MCPExcluded
		}
		if err := saveUserNoteWithOperationsTx(ctx, tx, note, []domain.NoteOperation{operation}); err != nil {
			return err
		}
		saved, err := scanUserNote(tx.QueryRowContext(ctx, `SELECT `+userNoteColumns+` FROM user_notes WHERE id=?`, note.ID))
		if err != nil {
			return err
		}
		if saved.Revision != note.Revision || saved.Checksum != note.Checksum || saved.BodyMarkdown != note.BodyMarkdown {
			return ErrConflict
		}
		return nil
	})
}

// The task row is already locked by the caller. A local effect can only commit
// with its confirmed receipt under that same lock; unconfirmed local receipts
// therefore prove that cancellation/follow-up prevented the effect.
func cancelAssistantLocalNotesTx(ctx context.Context, tx *dbTx, taskID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+assistantReceiptColumns+` FROM assistant_operation_receipts WHERE task_id=? AND tool IN ('notes.create','notes.append') AND outcome IN ('accepted','unknown') FOR UPDATE`, taskID)
	if err != nil {
		return err
	}
	receipts := []domain.AssistantOperationReceipt{}
	for rows.Next() {
		receipt, e := scanAssistantReceipt(rows)
		if e != nil {
			rows.Close()
			return e
		}
		receipts = append(receipts, receipt)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		result := assistant.Failure(receipt.CallID, "cancelled")
		result.OperationID = &receipt.OperationID
		raw, e := json.Marshal(result)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE assistant_operation_receipts SET outcome='failed',result=?::jsonb,updated_at=clock_timestamp() WHERE operation_id=?`, string(raw), receipt.OperationID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state='failed',result=?::jsonb,updated_at=clock_timestamp() WHERE task_id=? AND call_id=?`, string(raw), taskID, receipt.CallID); e != nil {
			return e
		}
	}
	return nil
}
