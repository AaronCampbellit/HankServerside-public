package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

var ErrAssistantLeaseLost = errors.New("assistant task lease lost")

const assistantTaskColumns = `id,home_id,user_id,COALESCE(session_id,''),submission_key,request_text,state,revision,checkpoint,max_turns,max_calls,max_tokens,turns,calls,tokens,active_ms,max_active_ms,cancel_requested,lease_owner,lease_until,fence,event_sequence,retry_at,created_at,updated_at`

func scanAssistantTask(row rowScanner) (domain.AssistantTask, error) {
	var task domain.AssistantTask
	err := row.Scan(&task.ID, &task.HomeID, &task.UserID, &task.SessionID, &task.SubmissionKey, &task.RequestText, &task.State, &task.Revision, &task.Checkpoint, &task.MaxTurns, &task.MaxCalls, &task.MaxTokens, &task.Turns, &task.Calls, &task.Tokens, &task.ActiveMS, &task.MaxActiveMS, &task.CancelRequested, &task.LeaseOwner, &task.LeaseUntil, &task.Fence, &task.EventSequence, &task.RetryAt, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return task, err
}

func (s *Store) CreateAssistantTask(ctx context.Context, task domain.AssistantTask) (domain.AssistantTask, bool, error) {
	if task.ID == "" || task.SessionID == "" {
		return domain.AssistantTask{}, false, ErrConflict
	}
	if task.MaxTurns == 0 {
		task.MaxTurns = 12
	}
	if task.MaxCalls == 0 {
		task.MaxCalls = 24
	}
	if task.MaxTokens == 0 {
		task.MaxTokens = 32000
	}
	if task.MaxActiveMS == 0 {
		task.MaxActiveMS = 300000
	}
	if len(task.Checkpoint) == 0 {
		task.Checkpoint = json.RawMessage(`{}`)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.AssistantTask{}, false, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT execution_version FROM assistant_sessions WHERE id=? AND home_id=? AND user_id=? FOR UPDATE`, task.SessionID, task.HomeID, task.UserID).Scan(&version); err != nil {
		return domain.AssistantTask{}, false, ErrNotFound
	}
	existing, existingErr := scanAssistantTask(tx.QueryRowContext(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE session_id=? AND user_id=? AND home_id=? AND submission_key=?`, task.SessionID, task.UserID, task.HomeID, task.SubmissionKey))
	if existingErr == nil {
		if existing.RequestText != task.RequestText {
			return domain.AssistantTask{}, false, ErrConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return domain.AssistantTask{}, false, existingErr
	}
	var conflict bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_tasks WHERE session_id=? AND submission_key<>? AND state NOT IN ('completed','failed','cancelled')) OR EXISTS(SELECT 1 FROM assistant_runs WHERE session_id=? AND state IN ('waiting_confirmation','waiting_client_tool'))`, task.SessionID, task.SubmissionKey, task.SessionID).Scan(&conflict); err != nil {
		return domain.AssistantTask{}, false, err
	}
	if conflict {
		return domain.AssistantTask{}, false, ErrConflict
	}
	if version == 1 {
		var lastRole string
		lastErr := tx.QueryRowContext(ctx, `SELECT role FROM assistant_messages WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, task.SessionID).Scan(&lastRole)
		if lastErr != nil && !errors.Is(lastErr, sql.ErrNoRows) {
			return domain.AssistantTask{}, false, lastErr
		}
		if lastRole == "user" {
			return domain.AssistantTask{}, false, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE assistant_sessions SET execution_version=2 WHERE id=?`, task.SessionID); err != nil {
			return domain.AssistantTask{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO assistant_tasks(id,home_id,user_id,session_id,submission_key,request_text,checkpoint,max_turns,max_calls,max_tokens,max_active_ms,event_sequence)
 VALUES(?,?,?,?,?,?,?::jsonb,?,?,?,?,1) ON CONFLICT(user_id,session_id,submission_key) DO NOTHING`, task.ID, task.HomeID, task.UserID, task.SessionID, task.SubmissionKey, task.RequestText, string(task.Checkpoint), task.MaxTurns, task.MaxCalls, task.MaxTokens, task.MaxActiveMS)
	if err != nil {
		return domain.AssistantTask{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.AssistantTask{}, false, err
	}
	saved, err := scanAssistantTask(tx.QueryRowContext(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE home_id=? AND user_id=? AND session_id=? AND submission_key=?`, task.HomeID, task.UserID, task.SessionID, task.SubmissionKey))
	if err != nil {
		return saved, false, err
	}
	if saved.RequestText != task.RequestText {
		return domain.AssistantTask{}, false, ErrConflict
	}
	if count == 1 {
		if err := appendAssistantExecutionMessage(ctx, tx, saved.SessionID, saved.ID+"_user", "user", saved.RequestText); err != nil {
			return saved, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type) VALUES(?,1,'queued')`, saved.ID); err != nil {
			return saved, false, err
		}
	}
	return saved, count == 1, tx.Commit()
}

func (s *Store) GetAssistantTask(ctx context.Context, homeID, userID, id string) (domain.AssistantTask, error) {
	return scanAssistantTask(s.queryRow(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE id=? AND home_id=? AND user_id=?`, id, homeID, userID))
}

func (s *Store) ListAssistantTasks(ctx context.Context, homeID, userID, sessionID string) ([]domain.AssistantTask, error) {
	rows, err := s.query(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE home_id=? AND user_id=? AND session_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, homeID, userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []domain.AssistantTask{}
	for rows.Next() {
		task, err := scanAssistantTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) ClaimAssistantTask(ctx context.Context, worker string, lease time.Duration) (domain.AssistantTask, error) {
	if worker == "" || lease < time.Second || lease > 10*time.Minute {
		return domain.AssistantTask{}, ErrConflict
	}
	return scanAssistantTask(s.queryRow(ctx, `WITH claimed AS (UPDATE assistant_tasks SET state='running',lease_owner=?,lease_until=clock_timestamp()+(? * interval '1 millisecond'),fence=fence+1,revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp()
 WHERE id=(SELECT id FROM assistant_tasks WHERE NOT cancel_requested AND session_id IS NOT NULL AND
 ((state IN ('queued','waiting_retry','reconciling') AND (retry_at IS NULL OR retry_at<=clock_timestamp())) OR (state='running' AND lease_until<=clock_timestamp()))
 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+assistantTaskColumns+`), emitted AS (INSERT INTO assistant_task_events(task_id,sequence,event_type) SELECT id,event_sequence,'running' FROM claimed RETURNING task_id) SELECT claimed.* FROM claimed JOIN emitted ON emitted.task_id=claimed.id`, worker, lease.Milliseconds()))
}

func (s *Store) RenewAssistantTaskLease(ctx context.Context, task domain.AssistantTask, lease time.Duration) error {
	if lease < time.Second || lease > 10*time.Minute {
		return ErrConflict
	}
	result, err := s.exec(ctx, `UPDATE assistant_tasks SET lease_until=clock_timestamp()+(? * interval '1 millisecond') WHERE id=? AND lease_owner=? AND fence=? AND state='running' AND NOT cancel_requested AND lease_until>clock_timestamp()`, lease.Milliseconds(), task.ID, task.LeaseOwner, task.Fence)
	return assistantLeaseResult(result, err)
}

func assistantLeaseResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrAssistantLeaseLost
	}
	return nil
}

// Task mutation and its event commit together. A stale worker cannot advance
// state, even after another worker has acquired the same task.
func (s *Store) SaveAssistantTask(ctx context.Context, task domain.AssistantTask, eventType, callID string) (domain.AssistantTask, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	saved, err := saveAssistantTaskTx(ctx, tx, task, eventType, callID)
	if err != nil {
		return task, err
	}
	return saved, tx.Commit()
}

func saveAssistantTaskTx(ctx context.Context, tx *dbTx, task domain.AssistantTask, eventType, callID string) (domain.AssistantTask, error) {
	keepLease := task.State == "running"
	saved, err := scanAssistantTask(tx.QueryRowContext(ctx, `UPDATE assistant_tasks SET state=?,checkpoint=?::jsonb,turns=?,calls=?,tokens=?,active_ms=?,retry_at=?,revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp(),
 lease_owner=CASE WHEN ? THEN lease_owner ELSE '' END,lease_until=CASE WHEN ? THEN lease_until ELSE NULL END
 WHERE id=? AND revision=? AND lease_owner=? AND fence=? AND state='running' AND NOT cancel_requested AND lease_until>clock_timestamp()
 RETURNING `+assistantTaskColumns, task.State, string(task.Checkpoint), task.Turns, task.Calls, task.Tokens, task.ActiveMS, task.RetryAt, keepLease, keepLease, task.ID, task.Revision, task.LeaseOwner, task.Fence))
	if errors.Is(err, ErrNotFound) {
		err = ErrAssistantLeaseLost
	}
	if err != nil {
		return task, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type,call_id) VALUES(?,?,?,NULLIF(?,''))`, saved.ID, saved.EventSequence, eventType, callID)
	if err != nil {
		return saved, err
	}
	if saved.State == "completed" || saved.State == "waiting_input" {
		var checkpoint struct {
			FinalText string          `json:"final_text"`
			Sources   json.RawMessage `json:"sources"`
		}
		if json.Unmarshal(saved.Checkpoint, &checkpoint) == nil && checkpoint.FinalText != "" {
			err = appendAssistantExecutionMessage(ctx, tx, saved.SessionID, fmt.Sprintf("%s_answer_%d", saved.ID, saved.Revision), "assistant", checkpoint.FinalText, checkpoint.Sources)
		}
	}
	return saved, err
}

func (s *Store) ListAssistantTaskEvents(ctx context.Context, homeID, userID, taskID string, after int64) ([]domain.AssistantTaskEvent, error) {
	if after < 0 {
		return nil, ErrConflict
	}
	if _, err := s.GetAssistantTask(ctx, homeID, userID, taskID); err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `SELECT e.task_id,e.sequence,e.event_type,COALESCE(e.call_id,''),e.created_at FROM assistant_task_events e JOIN assistant_tasks t ON t.id=e.task_id WHERE t.home_id=? AND t.user_id=? AND e.task_id=? AND e.sequence>? ORDER BY e.sequence LIMIT 200`, homeID, userID, taskID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []domain.AssistantTaskEvent{}
	for rows.Next() {
		var event domain.AssistantTaskEvent
		if err := rows.Scan(&event.TaskID, &event.Sequence, &event.Type, &event.CallID, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

const assistantStepColumns = `task_id,call_id,sequence,tool,tool_version,arguments,action_digest,state,attempts,result`

func scanAssistantStep(row rowScanner) (domain.AssistantTaskStep, error) {
	var step domain.AssistantTaskStep
	var rawResult []byte
	err := row.Scan(&step.TaskID, &step.CallID, &step.Sequence, &step.Tool, &step.ToolVersion, &step.Arguments, &step.ActionDigest, &step.State, &step.Attempts, &rawResult)
	step.Result = rawResult
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return step, err
}

func (s *Store) GetAssistantTaskStep(ctx context.Context, homeID, userID, taskID, callID string) (domain.AssistantTaskStep, error) {
	if _, err := s.GetAssistantTask(ctx, homeID, userID, taskID); err != nil {
		return domain.AssistantTaskStep{}, err
	}
	return scanAssistantStep(s.queryRow(ctx, `SELECT `+assistantStepColumns+` FROM assistant_task_steps WHERE task_id=? AND call_id=?`, taskID, callID))
}

// Plan the call and checkpoint before contacting a tool. Reusing a call ID with
// changed arguments is a conflict; a recovered worker reads its existing step.
func (s *Store) PlanAssistantTaskStep(ctx context.Context, task domain.AssistantTask, step domain.AssistantTaskStep) (domain.AssistantTask, error) {
	if task.State != "running" || step.TaskID != task.ID || step.State != "pending" {
		return task, ErrConflict
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	saved, err := saveAssistantTaskTx(ctx, tx, task, "preparing", step.CallID)
	if err != nil {
		return task, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_task_steps(task_id,call_id,sequence,tool,tool_version,arguments,action_digest,state) VALUES(?,?,?,?,?,?::jsonb,?,'pending')`, step.TaskID, step.CallID, step.Sequence, step.Tool, step.ToolVersion, string(step.Arguments), step.ActionDigest)
	if err != nil {
		return task, err
	}
	return saved, tx.Commit()
}

func (s *Store) StartAssistantTaskStep(ctx context.Context, task domain.AssistantTask, callID string) (domain.AssistantTask, domain.AssistantTaskStep, error) {
	if task.State != "running" {
		return task, domain.AssistantTaskStep{}, ErrConflict
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, domain.AssistantTaskStep{}, err
	}
	defer tx.Rollback()
	saved, err := saveAssistantTaskTx(ctx, tx, task, "reading", callID)
	if err != nil {
		return task, domain.AssistantTaskStep{}, err
	}
	step, err := scanAssistantStep(tx.QueryRowContext(ctx, `UPDATE assistant_task_steps SET state='running',attempts=attempts+1,updated_at=clock_timestamp() WHERE task_id=? AND call_id=? AND state IN ('pending','running') AND attempts<3 RETURNING `+assistantStepColumns, task.ID, callID))
	if err != nil {
		return task, step, err
	}
	return saved, step, tx.Commit()
}

func (s *Store) CompleteAssistantTaskStep(ctx context.Context, task domain.AssistantTask, step domain.AssistantTaskStep, approval *domain.AssistantTaskApproval) (domain.AssistantTask, error) {
	if step.TaskID != task.ID || (step.State != "completed" && step.State != "failed" && step.State != "waiting_approval" && step.State != "unknown") {
		return task, ErrConflict
	}
	if (approval != nil) != (step.State == "waiting_approval") {
		return task, ErrConflict
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	event := "checking"
	if approval != nil {
		task.State = "waiting_approval"
		event = "waiting_approval"
	}
	saved, err := saveAssistantTaskTx(ctx, tx, task, event, step.CallID)
	if err != nil {
		return task, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state=?,result=?::jsonb,action_digest=?,updated_at=clock_timestamp() WHERE task_id=? AND call_id=? AND state='running'`, step.State, string(step.Result), step.ActionDigest, task.ID, step.CallID)
	if err := assistantLeaseResult(result, err); err != nil {
		return task, err
	}
	if approval != nil {
		if approval.TaskID != task.ID || approval.CallID != step.CallID || approval.ActionDigest != step.ActionDigest || !approval.ExpiresAt.After(time.Now()) {
			return task, ErrConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO assistant_task_approvals(id,task_id,call_id,action_digest,expires_at) VALUES(?,?,?,?,?)`, approval.ID, task.ID, step.CallID, step.ActionDigest, approval.ExpiresAt)
		if err != nil {
			return task, err
		}
	}
	return saved, tx.Commit()
}

const assistantApprovalColumns = `id,task_id,call_id,action_digest,state,expires_at,COALESCE(decided_by,''),decided_at`

func scanAssistantApproval(row rowScanner) (domain.AssistantTaskApproval, error) {
	var approval domain.AssistantTaskApproval
	err := row.Scan(&approval.ID, &approval.TaskID, &approval.CallID, &approval.ActionDigest, &approval.State, &approval.ExpiresAt, &approval.DecidedBy, &approval.DecidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return approval, err
}
func (s *Store) GetAssistantTaskApproval(ctx context.Context, homeID, userID, taskID, callID string) (domain.AssistantTaskApproval, error) {
	if _, err := s.GetAssistantTask(ctx, homeID, userID, taskID); err != nil {
		return domain.AssistantTaskApproval{}, err
	}
	return scanAssistantApproval(s.queryRow(ctx, `SELECT `+assistantApprovalColumns+` FROM assistant_task_approvals WHERE task_id=? AND call_id=?`, taskID, callID))
}

// Decisions are owner-scoped, exact-digest-bound and idempotent. An approval
// never authorizes a proposal edited since it was displayed.
func (s *Store) DecideAssistantTaskApproval(ctx context.Context, homeID, userID, taskID, approvalID, digest string, approve bool, expectedRevision ...int64) (domain.AssistantTask, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.AssistantTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAssistantTask(tx.QueryRowContext(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE id=? AND home_id=? AND user_id=? FOR UPDATE`, taskID, homeID, userID))
	if err != nil {
		return task, err
	}
	approval, err := scanAssistantApproval(tx.QueryRowContext(ctx, `SELECT `+assistantApprovalColumns+` FROM assistant_task_approvals WHERE id=? AND task_id=? FOR UPDATE`, approvalID, taskID))
	if err != nil {
		return task, err
	}
	if approval.ActionDigest != digest {
		return task, ErrConflict
	}
	decision, state, event := "rejected", "queued", "queued"
	if approve {
		decision, state, event = "approved", "queued", "queued"
	}
	if approval.State == decision || (approve && approval.State == "consumed") {
		return task, tx.Commit()
	}
	if len(expectedRevision) > 0 && task.Revision != expectedRevision[0] {
		return task, ErrConflict
	}
	if approval.State != "pending" || task.State != "waiting_approval" || task.CancelRequested {
		return task, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals SET state=?,decided_by=?,decided_at=clock_timestamp() WHERE id=? AND state='pending' AND expires_at>clock_timestamp()`, decision, userID, approvalID)
	if err := assistantLeaseResult(result, err); err != nil {
		return task, ErrConflict
	}
	task, err = scanAssistantTask(tx.QueryRowContext(ctx, `UPDATE assistant_tasks SET state=?,revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp() WHERE id=? RETURNING `+assistantTaskColumns, state, taskID))
	if err != nil {
		return task, err
	}
	if !approve {
		if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state='cancelled' WHERE task_id=? AND call_id=?`, taskID, approval.CallID); err != nil {
			return task, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type,call_id) VALUES(?,?,?,?)`, taskID, task.EventSequence, event, approval.CallID); err != nil {
		return task, err
	}
	return task, tx.Commit()
}

func (s *Store) CancelAssistantTask(ctx context.Context, homeID, userID, taskID string) (domain.AssistantTask, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.AssistantTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAssistantTask(tx.QueryRowContext(ctx, `SELECT `+assistantTaskColumns+` FROM assistant_tasks WHERE id=? AND home_id=? AND user_id=? FOR UPDATE`, taskID, homeID, userID))
	if err != nil {
		return task, err
	}
	if task.State == "completed" || task.State == "failed" || task.State == "cancelled" {
		return task, tx.Commit()
	}
	task, err = scanAssistantTask(tx.QueryRowContext(ctx, `UPDATE assistant_tasks SET state='cancelled',cancel_requested=TRUE,lease_owner='',lease_until=NULL,fence=fence+1,revision=revision+1,event_sequence=event_sequence+1,updated_at=clock_timestamp() WHERE id=? RETURNING `+assistantTaskColumns, taskID))
	if err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals SET state='cancelled' WHERE task_id=? AND state IN ('pending','approved')`, taskID); err != nil {
		return task, err
	}
	if err := cancelAssistantLocalNotesTx(ctx, tx, taskID); err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_operation_receipts SET outcome='unknown',updated_at=clock_timestamp() WHERE task_id=? AND outcome='accepted'`, taskID); err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET state=CASE WHEN EXISTS(SELECT 1 FROM assistant_operation_receipts r WHERE r.task_id=assistant_task_steps.task_id AND r.call_id=assistant_task_steps.call_id AND r.outcome='unknown') THEN 'unknown' ELSE 'cancelled' END,updated_at=clock_timestamp() WHERE task_id=? AND state IN ('pending','running','waiting_approval')`, taskID); err != nil {
		return task, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_task_events(task_id,sequence,event_type) VALUES(?,?,'cancelled')`, taskID, task.EventSequence); err != nil {
		return task, err
	}
	return task, tx.Commit()
}

// ListAssistantTaskStepSummaries deliberately omits arguments and result data.
// Detailed evidence/proposals require source authorization at their own API.
func (s *Store) ListAssistantTaskStepSummaries(ctx context.Context, home, user, taskID string) ([]domain.AssistantTaskStep, error) {
	rows, err := s.query(ctx, `SELECT s.call_id,s.tool,s.state,s.attempts FROM assistant_task_steps s JOIN assistant_tasks t ON t.id=s.task_id WHERE t.home_id=? AND t.user_id=? AND t.id=? ORDER BY s.sequence LIMIT 1000`, home, user, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := []domain.AssistantTaskStep{}
	for rows.Next() {
		var step domain.AssistantTaskStep
		if err := rows.Scan(&step.CallID, &step.Tool, &step.State, &step.Attempts); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}
