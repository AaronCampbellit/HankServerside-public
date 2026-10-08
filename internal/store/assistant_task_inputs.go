package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) SubmitAssistantTaskInput(ctx context.Context, homeID, userID, taskID, key, text string, expectedRevision ...int64) (domain.AssistantTask, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.AssistantTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAssistantTask(tx.QueryRowContext(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE id=? AND home_id=? AND user_id=? FOR UPDATE`, taskID, homeID, userID))
	if err != nil {
		return task, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT text FROM assistant_task_inputs WHERE task_id=? AND input_key=?`, taskID, key).Scan(&existing)
	if err == nil {
		if existing != text {
			return task, ErrConflict
		}
		return task, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return task, err
	}
	if len(expectedRevision) > 0 && task.Revision != expectedRevision[0] {
		return task, ErrConflict
	}
	if task.State == "completed" || task.State == "failed" || task.State == "cancelled" {
		return task, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_inputs(task_id,input_key,text) VALUES(?,?,?)`, taskID, key, text); err != nil {
		return task, err
	}
	if err := appendAssistantExecutionMessage(ctx, tx, task.SessionID, assistantInputMessageID(task.ID, key), "user", text); err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals SET state='cancelled' WHERE task_id=? AND state IN ('pending','approved')`, taskID); err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state='cancelled' WHERE task_id=? AND state IN ('pending','running','waiting_approval') AND NOT EXISTS(SELECT 1 FROM assistant_operation_receipts r WHERE r.task_id=assistant_task_steps.task_id AND r.call_id=assistant_task_steps.call_id)`, taskID); err != nil {
		return task, err
	}
	if err := cancelAssistantLocalNotesTx(ctx, tx, taskID); err != nil {
		return task, err
	}
	task, err = scanAssistantTask(tx.QueryRowContext(ctx, `UPDATE assistant_tasks SET state=CASE WHEN EXISTS(SELECT 1 FROM assistant_operation_receipts r WHERE r.task_id=assistant_tasks.id AND r.outcome IN ('accepted','unknown')) THEN 'reconciling' ELSE 'queued' END,lease_owner='',lease_until=NULL,fence=fence+1,revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp() WHERE id=? RETURNING `+assistantTaskColumns, taskID))
	if err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type) VALUES(?,?,'followup')`, taskID, task.EventSequence); err != nil {
		return task, err
	}
	return task, tx.Commit()
}

func (s *Store) PendingAssistantTaskInputs(ctx context.Context, homeID, userID, taskID string) ([]domain.AssistantTaskInput, error) {
	rows, err := s.query(ctx, `SELECT i.id,i.task_id,i.input_key,i.text FROM assistant_task_inputs i JOIN assistant_tasks t ON t.id=i.task_id WHERE t.home_id=? AND t.user_id=? AND i.task_id=? AND NOT i.applied ORDER BY i.id LIMIT 20`, homeID, userID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []domain.AssistantTaskInput{}
	for rows.Next() {
		var input domain.AssistantTaskInput
		if err := rows.Scan(&input.ID, &input.TaskID, &input.Key, &input.Text); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (s *Store) ApplyAssistantTaskInputs(ctx context.Context, task domain.AssistantTask, inputs []domain.AssistantTaskInput) (domain.AssistantTask, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	event := "running"
	if task.State == "waiting_input" {
		event = "waiting_input"
	}
	saved, err := saveAssistantTaskTx(ctx, tx, task, event, "")
	if err != nil {
		return task, err
	}
	for _, input := range inputs {
		if input.TaskID != task.ID {
			return task, ErrConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE assistant_task_inputs SET applied=TRUE WHERE task_id=? AND id=? AND input_key=? AND text=? AND NOT applied`, task.ID, input.ID, input.Key, input.Text)
		if err := assistantLeaseResult(result, err); err != nil {
			return task, err
		}
	}
	return saved, tx.Commit()
}

// Expiration is a state transition, not deletion. It leaves the proposal
// available for inspection and requires a fresh action/approval before retry.
func (s *Store) ExpireAssistantTaskApprovals(ctx context.Context) (int, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM assistant_tasks t WHERE t.state='waiting_approval' AND EXISTS(SELECT 1 FROM assistant_task_approvals a WHERE a.task_id=t.id AND a.state='pending' AND a.expires_at<=clock_timestamp()) ORDER BY t.id LIMIT 100 FOR UPDATE OF t SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals SET state='expired' WHERE task_id=? AND state='pending' AND expires_at<=clock_timestamp()`, id); err != nil {
			return 0, err
		}
		var sequence int64
		if err := tx.QueryRowContext(ctx, `UPDATE assistant_tasks SET state='waiting_input',revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp() WHERE id=? RETURNING event_sequence`, id).Scan(&sequence); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type) VALUES(?,?,'waiting_input')`, id, sequence); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}
