package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) CreateAssistantSession(ctx context.Context, session domain.AssistantSession) error {
	_, err := s.exec(ctx, `INSERT INTO assistant_sessions (id, home_id, user_id, title, last_message_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.ID,
		session.HomeID,
		session.UserID,
		session.Title,
		session.LastMessageAt,
		session.CreatedAt,
		session.UpdatedAt,
	)
	return err
}

func (s *Store) ListAssistantSessions(ctx context.Context, homeID string, userID string) ([]domain.AssistantSession, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, user_id, title, last_message_at, created_at, updated_at
		FROM assistant_sessions
		WHERE home_id = ? AND user_id = ?
		ORDER BY updated_at DESC, created_at DESC`, homeID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []domain.AssistantSession
	for rows.Next() {
		session, err := scanAssistantSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) GetAssistantSession(ctx context.Context, sessionID string) (domain.AssistantSession, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, user_id, title, last_message_at, created_at, updated_at
		FROM assistant_sessions WHERE id = ?`, sessionID)
	return scanAssistantSession(row)
}

func (s *Store) TouchAssistantSession(ctx context.Context, sessionID string, title string, updatedAt time.Time) error {
	_, err := s.exec(ctx, `UPDATE assistant_sessions
		SET title = ?, last_message_at = ?, updated_at = ?
		WHERE id = ?`, title, updatedAt, updatedAt, sessionID)
	return err
}

func (s *Store) DeleteAssistantSession(ctx context.Context, sessionID string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Fence execution before removing conversation content. Receipts retain only
	// identity/digest/outcome so a deleted conversation cannot cause replay.
	if _, err := tx.ExecContext(ctx, `WITH targets AS MATERIALIZED (SELECT id,state FROM assistant_tasks WHERE session_id=? ORDER BY id FOR UPDATE), updated AS (
 UPDATE assistant_tasks t SET state=CASE WHEN t.state IN ('completed','failed') THEN t.state ELSE 'cancelled' END,cancel_requested=TRUE,lease_owner='',lease_until=NULL,fence=fence+1,revision=revision+1,event_sequence=event_sequence+CASE WHEN t.state IN ('completed','failed','cancelled') THEN 0 ELSE 1 END,request_text='',checkpoint='{}'::jsonb,updated_at=clock_timestamp() FROM targets x WHERE t.id=x.id RETURNING t.id,t.event_sequence,x.state AS previous_state)
 INSERT INTO assistant_task_events(task_id,sequence,event_type) SELECT id,event_sequence,'cancelled' FROM updated WHERE previous_state NOT IN ('completed','failed','cancelled')`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_approvals SET state='cancelled' WHERE task_id IN (SELECT id FROM assistant_tasks WHERE session_id=?) AND state IN ('pending','approved')`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_operation_receipts SET result=NULL,outcome=CASE WHEN outcome='accepted' THEN 'unknown' ELSE outcome END,updated_at=clock_timestamp() WHERE task_id IN (SELECT id FROM assistant_tasks WHERE session_id=?)`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_task_steps SET arguments='{}'::jsonb,result=NULL,state=CASE WHEN state IN ('completed','failed') THEN state ELSE 'cancelled' END WHERE task_id IN (SELECT id FROM assistant_tasks WHERE session_id=?)`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_task_inputs WHERE task_id IN (SELECT id FROM assistant_tasks WHERE session_id=?)`, sessionID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_documents
		WHERE source_type = 'assistant_conversation' AND source_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_attachments WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_tool_calls
		WHERE run_id IN (SELECT id FROM assistant_runs WHERE session_id = ?)`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_runs WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM assistant_sessions WHERE id = ?`, sessionID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) CreateAssistantMessage(ctx context.Context, message domain.AssistantMessage) error {
	// A v1 request already in flight cannot append after the session adopts v2.
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT execution_version FROM assistant_sessions WHERE id=? FOR UPDATE`, message.SessionID).Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_messages(id,session_id,role,status,content_json,model_name,created_at) VALUES(?,?,?,?,?,?,?)`, message.ID, message.SessionID, message.Role, message.Status, message.ContentJSON, message.ModelName, message.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListAssistantMessages(ctx context.Context, sessionID string) ([]domain.AssistantMessage, error) {
	rows, err := s.query(ctx, `SELECT id, session_id, role, status, content_json, model_name, created_at
		FROM assistant_messages
		WHERE session_id = ?
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.AssistantMessage
	for rows.Next() {
		message, err := scanAssistantMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) CreateAssistantRun(ctx context.Context, run domain.AssistantRun) error {
	_, err := s.exec(ctx, `INSERT INTO assistant_runs (
			id, session_id, message_id, state, requires_client_tools, requires_confirmation,
			pending_action_json, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.SessionID,
		run.MessageID,
		run.State,
		run.RequiresClientTools,
		run.RequiresConfirmation,
		run.PendingActionJSON,
		run.CreatedAt,
		run.CompletedAt,
	)
	return err
}

func (s *Store) GetAssistantRun(ctx context.Context, runID string) (domain.AssistantRun, error) {
	row := s.queryRow(ctx, `SELECT id, session_id, message_id, state, requires_client_tools, requires_confirmation,
		pending_action_json, created_at, completed_at
		FROM assistant_runs WHERE id = ?`, runID)
	return scanAssistantRun(row)
}

func (s *Store) GetPendingAssistantRun(ctx context.Context, sessionID string) (domain.AssistantRun, error) {
	row := s.queryRow(ctx, `SELECT id, session_id, message_id, state, requires_client_tools, requires_confirmation,
		pending_action_json, created_at, completed_at
		FROM assistant_runs
		WHERE session_id = ?
			AND completed_at IS NULL
			AND (requires_client_tools = TRUE OR requires_confirmation = TRUE)
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, sessionID)
	return scanAssistantRun(row)
}

func (s *Store) UpdateAssistantRun(ctx context.Context, run domain.AssistantRun) error {
	_, err := s.exec(ctx, `UPDATE assistant_runs
		SET state = ?, requires_client_tools = ?, requires_confirmation = ?, pending_action_json = ?, completed_at = ?
		WHERE id = ?`,
		run.State,
		run.RequiresClientTools,
		run.RequiresConfirmation,
		run.PendingActionJSON,
		run.CompletedAt,
		run.ID,
	)
	return err
}

func (s *Store) GetAssistantSettings(ctx context.Context, homeID string, userID string) (domain.AssistantSettings, error) {
	row := s.queryRow(ctx, `SELECT home_id, user_id, profile_notes_enabled, home_notes_enabled,
			files_enabled, calendar_enabled, homeassistant_enabled, project_docs_enabled, conversations_enabled, system_prompt, max_context_items,
			ai_provider, ollama_base_url, chat_model, embedding_model, prompt_profile, planner_enabled, planner_model,
			created_at, updated_at, updated_by
		FROM assistant_settings
		WHERE home_id = ? AND user_id = ?`, homeID, userID)
	return scanAssistantSettings(row)
}

func (s *Store) UpsertAssistantSettings(ctx context.Context, settings domain.AssistantSettings) error {
	_, err := s.exec(ctx, `INSERT INTO assistant_settings (
			home_id, user_id, profile_notes_enabled, home_notes_enabled,
			files_enabled, calendar_enabled, homeassistant_enabled, project_docs_enabled, conversations_enabled, system_prompt, max_context_items,
			ai_provider, ollama_base_url, chat_model, embedding_model, prompt_profile, planner_enabled, planner_model,
			created_at, updated_at, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, user_id) DO UPDATE SET
			profile_notes_enabled = excluded.profile_notes_enabled,
			home_notes_enabled = excluded.home_notes_enabled,
			files_enabled = excluded.files_enabled,
			calendar_enabled = excluded.calendar_enabled,
			homeassistant_enabled = excluded.homeassistant_enabled,
			project_docs_enabled = excluded.project_docs_enabled,
			conversations_enabled = excluded.conversations_enabled,
			system_prompt = excluded.system_prompt,
			max_context_items = excluded.max_context_items,
			ai_provider = excluded.ai_provider,
			ollama_base_url = excluded.ollama_base_url,
			chat_model = excluded.chat_model,
			embedding_model = excluded.embedding_model,
			prompt_profile = excluded.prompt_profile,
			planner_enabled = excluded.planner_enabled,
			planner_model = excluded.planner_model,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		settings.HomeID,
		settings.UserID,
		settings.ProfileNotesEnabled,
		settings.HomeNotesEnabled,
		settings.FilesEnabled,
		settings.CalendarEnabled,
		settings.HomeAssistantEnabled,
		settings.ProjectDocsEnabled,
		settings.ConversationsEnabled,
		settings.SystemPrompt,
		settings.MaxContextItems,
		settings.AIProvider,
		settings.OllamaBaseURL,
		settings.ChatModel,
		settings.EmbeddingModel,
		settings.PromptProfile,
		settings.PlannerEnabled,
		settings.PlannerModel,
		settings.CreatedAt,
		settings.UpdatedAt,
		settings.UpdatedBy,
	)
	return err
}

func (s *Store) UpsertAssistantCalendarEntries(ctx context.Context, entries []domain.AssistantCalendarEntry) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_calendar_entries (
				id, home_id, user_id, device_id, external_event_id, calendar_id, title, location,
				notes, starts_at, ends_at, is_all_day, search_text, metadata_json, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, device_id, external_event_id) DO UPDATE SET
				id = excluded.id,
				home_id = excluded.home_id,
				calendar_id = excluded.calendar_id,
				title = excluded.title,
				location = excluded.location,
				notes = excluded.notes,
				starts_at = excluded.starts_at,
				ends_at = excluded.ends_at,
				is_all_day = excluded.is_all_day,
				search_text = excluded.search_text,
				metadata_json = excluded.metadata_json,
				updated_at = excluded.updated_at`,
			entry.ID,
			entry.HomeID,
			entry.UserID,
			entry.DeviceID,
			entry.ExternalEventID,
			entry.CalendarID,
			entry.Title,
			entry.Location,
			entry.Notes,
			entry.StartsAt,
			entry.EndsAt,
			entry.IsAllDay,
			entry.SearchText,
			entry.MetadataJSON,
			entry.UpdatedAt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) ListAssistantCalendarEntries(ctx context.Context, homeID string, userID string) ([]domain.AssistantCalendarEntry, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, user_id, device_id, external_event_id, calendar_id, title, location,
		notes, starts_at, ends_at, is_all_day, search_text, metadata_json, updated_at
		FROM assistant_calendar_entries
		WHERE home_id = ? AND user_id = ?
		ORDER BY starts_at ASC, title ASC`, homeID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []domain.AssistantCalendarEntry
	for rows.Next() {
		entry, err := scanAssistantCalendarEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func scanAssistantSession(scanner interface{ Scan(dest ...any) error }) (domain.AssistantSession, error) {
	var session domain.AssistantSession
	if err := scanner.Scan(
		&session.ID,
		&session.HomeID,
		&session.UserID,
		&session.Title,
		&session.LastMessageAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantSession{}, ErrNotFound
		}
		return domain.AssistantSession{}, err
	}
	return session, nil
}

func scanAssistantSettings(scanner interface{ Scan(dest ...any) error }) (domain.AssistantSettings, error) {
	var settings domain.AssistantSettings
	if err := scanner.Scan(
		&settings.HomeID,
		&settings.UserID,
		&settings.ProfileNotesEnabled,
		&settings.HomeNotesEnabled,
		&settings.FilesEnabled,
		&settings.CalendarEnabled,
		&settings.HomeAssistantEnabled,
		&settings.ProjectDocsEnabled,
		&settings.ConversationsEnabled,
		&settings.SystemPrompt,
		&settings.MaxContextItems,
		&settings.AIProvider,
		&settings.OllamaBaseURL,
		&settings.ChatModel,
		&settings.EmbeddingModel,
		&settings.PromptProfile,
		&settings.PlannerEnabled,
		&settings.PlannerModel,
		&settings.CreatedAt,
		&settings.UpdatedAt,
		&settings.UpdatedBy,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantSettings{}, ErrNotFound
		}
		return domain.AssistantSettings{}, err
	}
	return settings, nil
}

func scanAssistantMessage(scanner interface{ Scan(dest ...any) error }) (domain.AssistantMessage, error) {
	var message domain.AssistantMessage
	if err := scanner.Scan(
		&message.ID,
		&message.SessionID,
		&message.Role,
		&message.Status,
		&message.ContentJSON,
		&message.ModelName,
		&message.CreatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantMessage{}, ErrNotFound
		}
		return domain.AssistantMessage{}, err
	}
	return message, nil
}

func scanAssistantRun(scanner interface{ Scan(dest ...any) error }) (domain.AssistantRun, error) {
	var run domain.AssistantRun
	if err := scanner.Scan(
		&run.ID,
		&run.SessionID,
		&run.MessageID,
		&run.State,
		&run.RequiresClientTools,
		&run.RequiresConfirmation,
		&run.PendingActionJSON,
		&run.CreatedAt,
		&run.CompletedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantRun{}, ErrNotFound
		}
		return domain.AssistantRun{}, err
	}
	return run, nil
}

func scanAssistantCalendarEntry(scanner interface{ Scan(dest ...any) error }) (domain.AssistantCalendarEntry, error) {
	var entry domain.AssistantCalendarEntry
	if err := scanner.Scan(
		&entry.ID,
		&entry.HomeID,
		&entry.UserID,
		&entry.DeviceID,
		&entry.ExternalEventID,
		&entry.CalendarID,
		&entry.Title,
		&entry.Location,
		&entry.Notes,
		&entry.StartsAt,
		&entry.EndsAt,
		&entry.IsAllDay,
		&entry.SearchText,
		&entry.MetadataJSON,
		&entry.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantCalendarEntry{}, ErrNotFound
		}
		return domain.AssistantCalendarEntry{}, err
	}
	return entry, nil
}
