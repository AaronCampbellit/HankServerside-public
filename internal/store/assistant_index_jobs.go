package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const (
	AssistantIndexJobStatusQueued    = "queued"
	AssistantIndexJobStatusRunning   = "running"
	AssistantIndexJobStatusCompleted = "completed"
	AssistantIndexJobStatusFailed    = "failed"
)

func (s *Store) EnqueueAssistantIndexJob(ctx context.Context, job domain.AssistantIndexJob) (domain.AssistantIndexJob, error) {
	now := time.Now().UTC()
	job.ID = strings.TrimSpace(job.ID)
	if job.ID == "" {
		job.ID = stableJobID(job.HomeID, job.UserID, job.SourceType, job.SourceID)
	}
	job.HomeID = strings.TrimSpace(job.HomeID)
	job.UserID = strings.TrimSpace(job.UserID)
	job.SourceType = strings.TrimSpace(job.SourceType)
	job.SourceID = strings.TrimSpace(job.SourceID)
	if job.Status == "" {
		job.Status = AssistantIndexJobStatusQueued
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = now
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now

	row := s.queryRow(ctx, `INSERT INTO assistant_index_jobs (
			id, home_id, user_id, source_type, source_id, status, attempts, last_error,
			run_after, started_at, completed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, user_id, source_type, source_id) DO UPDATE SET
			status = 'queued',
			last_error = '',
			run_after = excluded.run_after,
			started_at = NULL,
			completed_at = NULL,
			updated_at = excluded.updated_at
		RETURNING id, home_id, user_id, source_type, source_id, status, attempts, last_error,
			run_after, started_at, completed_at, created_at, updated_at`,
		job.ID,
		job.HomeID,
		job.UserID,
		job.SourceType,
		job.SourceID,
		job.Status,
		job.Attempts,
		job.LastError,
		job.RunAfter,
		job.StartedAt,
		job.CompletedAt,
		job.CreatedAt,
		job.UpdatedAt,
	)
	return scanAssistantIndexJob(row)
}

func (s *Store) ClaimAssistantIndexJob(ctx context.Context, now time.Time) (domain.AssistantIndexJob, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := s.queryRow(ctx, `UPDATE assistant_index_jobs
		SET status = 'running',
			attempts = attempts + 1,
			started_at = ?,
			updated_at = ?
		WHERE id = (
			SELECT id
			FROM assistant_index_jobs
			WHERE status = 'queued' AND run_after <= ?
			ORDER BY updated_at ASC, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, home_id, user_id, source_type, source_id, status, attempts, last_error,
			run_after, started_at, completed_at, created_at, updated_at`, now, now, now)
	return scanAssistantIndexJob(row)
}

func (s *Store) CompleteAssistantIndexJob(ctx context.Context, jobID string, completedAt time.Time) error {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	_, err := s.exec(ctx, `UPDATE assistant_index_jobs
		SET status = 'completed',
			last_error = '',
			completed_at = ?,
			updated_at = ?
		WHERE id = ?`, completedAt, completedAt, jobID)
	return err
}

func (s *Store) RequeueRunningAssistantIndexJobs(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `UPDATE assistant_index_jobs
		SET status = 'queued',
			started_at = NULL,
			run_after = ?,
			updated_at = ?
		WHERE status = 'running'`, now, now)
	return err
}

func (s *Store) FailAssistantIndexJob(ctx context.Context, jobID string, errText string, runAfter time.Time, failedAt time.Time) error {
	if failedAt.IsZero() {
		failedAt = time.Now().UTC()
	}
	if runAfter.IsZero() {
		runAfter = failedAt.Add(time.Minute)
	}
	status := AssistantIndexJobStatusQueued
	if runAfter.Sub(failedAt) >= 30*time.Minute {
		status = AssistantIndexJobStatusFailed
	}
	_, err := s.exec(ctx, `UPDATE assistant_index_jobs
		SET status = ?,
			last_error = ?,
			run_after = ?,
			updated_at = ?
		WHERE id = ?`, status, strings.TrimSpace(errText), runAfter, failedAt, jobID)
	return err
}

func (s *Store) AssistantIndexJobCounts(ctx context.Context, homeID string, userID string) (queued int, running int, failed int, err error) {
	rows, err := s.query(ctx, `SELECT status, COUNT(*)
		FROM assistant_index_jobs
		WHERE home_id = ? AND user_id = ? AND status IN ('queued', 'running', 'failed')
		GROUP BY status`, homeID, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case AssistantIndexJobStatusQueued:
			queued = count
		case AssistantIndexJobStatusRunning:
			running = count
		case AssistantIndexJobStatusFailed:
			failed = count
		}
	}
	return queued, running, failed, rows.Err()
}

func scanAssistantIndexJob(scanner interface{ Scan(dest ...any) error }) (domain.AssistantIndexJob, error) {
	var job domain.AssistantIndexJob
	var startedAt, completedAt sql.NullTime
	if err := scanner.Scan(
		&job.ID,
		&job.HomeID,
		&job.UserID,
		&job.SourceType,
		&job.SourceID,
		&job.Status,
		&job.Attempts,
		&job.LastError,
		&job.RunAfter,
		&startedAt,
		&completedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.AssistantIndexJob{}, ErrNotFound
		}
		return domain.AssistantIndexJob{}, err
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return job, nil
}

func stableJobID(homeID string, userID string, sourceType string, sourceID string) string {
	key := strings.Join([]string{homeID, userID, sourceType, sourceID}, ":")
	hash := stableHash(key)
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "aijob_" + hash
}
