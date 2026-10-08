package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/dropfile/HankServerside/internal/domain"
)

const assistantReceiptColumns = `operation_id,home_id,user_id,COALESCE(task_id,''),call_id,tool,action_digest,outcome,result`

func scanAssistantReceipt(row rowScanner) (domain.AssistantOperationReceipt, error) {
	var receipt domain.AssistantOperationReceipt
	var rawResult []byte
	err := row.Scan(&receipt.OperationID, &receipt.HomeID, &receipt.UserID, &receipt.TaskID, &receipt.CallID, &receipt.Tool, &receipt.ActionDigest, &receipt.Outcome, &rawResult)
	receipt.Result = rawResult
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return receipt, err
}
func (s *Store) GetAssistantOperation(ctx context.Context, homeID, userID, operationID string) (domain.AssistantOperationReceipt, error) {
	return scanAssistantReceipt(s.queryRow(ctx, `SELECT `+assistantReceiptColumns+` FROM assistant_operation_receipts WHERE operation_id=? AND home_id=? AND user_id=?`, operationID, homeID, userID))
}

// Begin records dispatch intent and consumes the exact approval atomically.
// fresh=false means a dispatch might already have happened: callers reconcile
// the receipt/destination instead of sending the action again.
func (s *Store) BeginAssistantOperation(ctx context.Context, task domain.AssistantTask, receipt domain.AssistantOperationReceipt, approvalID string) (domain.AssistantTask, domain.AssistantOperationReceipt, bool, error) {
	if task.State != "running" || receipt.OperationID == "" || receipt.TaskID != task.ID || receipt.HomeID != task.HomeID || receipt.UserID != task.UserID {
		return task, receipt, false, ErrConflict
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, receipt, false, err
	}
	defer tx.Rollback()
	saved, err := saveAssistantTaskTx(ctx, tx, task, "checking", receipt.CallID)
	if err != nil {
		return task, receipt, false, err
	}
	existing, err := scanAssistantReceipt(tx.QueryRowContext(ctx, `SELECT `+assistantReceiptColumns+` FROM assistant_operation_receipts WHERE operation_id=? FOR UPDATE`, receipt.OperationID))
	if err == nil {
		if existing.TaskID != task.ID || existing.HomeID != task.HomeID || existing.UserID != task.UserID || existing.CallID != receipt.CallID || existing.Tool != receipt.Tool || existing.ActionDigest != receipt.ActionDigest {
			return task, receipt, false, ErrConflict
		}
		return saved, existing, false, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return task, receipt, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals a SET state='consumed' WHERE a.id=? AND a.task_id=? AND a.call_id=? AND a.action_digest=? AND a.state='approved' AND a.decided_by=? AND a.expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM assistant_task_steps s WHERE s.task_id=a.task_id AND s.call_id=a.call_id AND s.action_digest=a.action_digest AND s.tool=? AND s.state='waiting_approval')`, approvalID, task.ID, receipt.CallID, receipt.ActionDigest, task.UserID, receipt.Tool)
	if err := assistantLeaseResult(result, err); err != nil {
		return task, receipt, false, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_operation_receipts(operation_id,home_id,user_id,task_id,call_id,tool,action_digest,outcome) VALUES(?,?,?,?,?,?,?,'accepted')`, receipt.OperationID, task.HomeID, task.UserID, task.ID, receipt.CallID, receipt.Tool, receipt.ActionDigest)
	if err != nil {
		return task, receipt, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state='running',updated_at=clock_timestamp() WHERE task_id=? AND call_id=?`, task.ID, receipt.CallID); err != nil {
		return task, receipt, false, err
	}
	receipt.Outcome = "accepted"
	receipt.Result = nil
	return saved, receipt, true, tx.Commit()
}

func (s *Store) FinishAssistantOperation(ctx context.Context, task domain.AssistantTask, receipt domain.AssistantOperationReceipt) (domain.AssistantTask, error) {
	return s.commitAssistantLocalOperation(ctx, task, receipt, nil)
}

// A local domain mutation and its completion receipt share this transaction.
// Only store-owned adapters may provide apply; it must use this transaction.
func (s *Store) commitAssistantLocalOperation(ctx context.Context, task domain.AssistantTask, receipt domain.AssistantOperationReceipt, apply func(*dbTx) error) (domain.AssistantTask, error) {
	if task.State != "running" || receipt.TaskID != task.ID || receipt.HomeID != task.HomeID || receipt.UserID != task.UserID || (receipt.Outcome != "confirmed" && receipt.Outcome != "failed" && receipt.Outcome != "unknown" && receipt.Outcome != "accepted") {
		return task, ErrConflict
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	saved, err := saveAssistantTaskTx(ctx, tx, task, "checking", receipt.CallID)
	if err != nil {
		return task, err
	}
	existing, err := scanAssistantReceipt(tx.QueryRowContext(ctx, `SELECT `+assistantReceiptColumns+` FROM assistant_operation_receipts WHERE operation_id=? AND task_id=? AND home_id=? AND user_id=? AND call_id=? AND tool=? AND action_digest=? FOR UPDATE`, receipt.OperationID, task.ID, task.HomeID, task.UserID, receipt.CallID, receipt.Tool, receipt.ActionDigest))
	if err != nil {
		return task, err
	}
	if existing.Outcome == "confirmed" || existing.Outcome == "failed" {
		if existing.Outcome != receipt.Outcome || !assistantReceiptResultEqual(existing.Result, receipt.Result) {
			return task, ErrConflict
		}
		if err := tx.Rollback(); err != nil {
			return task, err
		}
		return s.GetAssistantTask(ctx, task.HomeID, task.UserID, task.ID)
	}
	if apply != nil {
		if err := apply(tx); err != nil {
			return task, err
		}
	}
	var result any
	if len(receipt.Result) > 0 {
		result = string(receipt.Result)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE assistant_operation_receipts SET outcome=?,result=?::jsonb,updated_at=clock_timestamp() WHERE operation_id=? AND task_id=? AND home_id=? AND user_id=? AND call_id=? AND tool=? AND action_digest=? AND outcome IN ('accepted','unknown')`, receipt.Outcome, result, receipt.OperationID, task.ID, task.HomeID, task.UserID, receipt.CallID, receipt.Tool, receipt.ActionDigest)
	if err := assistantLeaseResult(updated, err); err != nil {
		return task, err
	}
	state := "completed"
	if receipt.Outcome == "failed" {
		state = "failed"
	}
	if receipt.Outcome == "unknown" {
		state = "unknown"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state=?,result=?::jsonb,updated_at=clock_timestamp() WHERE task_id=? AND call_id=?`, state, result, task.ID, receipt.CallID); err != nil {
		return task, err
	}
	return saved, tx.Commit()
}

func (s *Store) AssistantTaskHasUncertainOperations(ctx context.Context, homeID, userID, taskID string) (bool, error) {
	var found bool
	err := s.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_operation_receipts WHERE home_id=? AND user_id=? AND task_id=? AND outcome IN ('accepted','unknown'))`, homeID, userID, taskID).Scan(&found)
	return found, err
}

func assistantReceiptResultEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var left, right any
	decode := func(raw json.RawMessage, target *any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	if decode(a, &left) != nil || decode(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
