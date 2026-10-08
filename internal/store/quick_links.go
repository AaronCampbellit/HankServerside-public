package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const homeQuickLinkColumns = `id, home_id, title, url, description, sort_order, health_check_enabled, status, status_code, last_checked_at, last_error, created_at, updated_at, updated_by`

func (s *Store) CountHomeQuickLinks(ctx context.Context, homeID string) (int, error) {
	var count int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM home_quick_links WHERE home_id = ?`, homeID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) ListHomeQuickLinks(ctx context.Context, homeID string) ([]domain.HomeQuickLink, error) {
	rows, err := s.query(ctx, `SELECT `+homeQuickLinkColumns+`
		FROM home_quick_links
		WHERE home_id = ?
		ORDER BY sort_order ASC, created_at ASC, title ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []domain.HomeQuickLink
	for rows.Next() {
		link, err := scanHomeQuickLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) GetHomeQuickLink(ctx context.Context, homeID string, linkID string) (domain.HomeQuickLink, error) {
	row := s.queryRow(ctx, `SELECT `+homeQuickLinkColumns+`
		FROM home_quick_links
		WHERE home_id = ? AND id = ?`, homeID, linkID)
	return scanHomeQuickLink(row)
}

func (s *Store) CreateHomeQuickLink(ctx context.Context, link domain.HomeQuickLink) error {
	_, err := s.exec(ctx, `INSERT INTO home_quick_links (
			id, home_id, title, url, description, sort_order, health_check_enabled, status, status_code,
			last_checked_at, last_error, created_at, updated_at, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID,
		link.HomeID,
		link.Title,
		link.URL,
		link.Description,
		link.SortOrder,
		link.HealthCheckEnabled,
		link.Status,
		link.StatusCode,
		link.LastCheckedAt,
		link.LastError,
		link.CreatedAt,
		link.UpdatedAt,
		link.UpdatedBy,
	)
	return err
}

func (s *Store) UpdateHomeQuickLink(ctx context.Context, link domain.HomeQuickLink) (domain.HomeQuickLink, error) {
	result, err := s.exec(ctx, `UPDATE home_quick_links
		SET title = ?, url = ?, description = ?, health_check_enabled = ?, status = ?, status_code = ?,
			last_checked_at = ?, last_error = ?, updated_at = ?, updated_by = ?
		WHERE home_id = ? AND id = ?`,
		link.Title,
		link.URL,
		link.Description,
		link.HealthCheckEnabled,
		link.Status,
		link.StatusCode,
		link.LastCheckedAt,
		link.LastError,
		link.UpdatedAt,
		link.UpdatedBy,
		link.HomeID,
		link.ID,
	)
	if err != nil {
		return domain.HomeQuickLink{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.HomeQuickLink{}, err
	}
	if rows == 0 {
		return domain.HomeQuickLink{}, ErrNotFound
	}
	return s.GetHomeQuickLink(ctx, link.HomeID, link.ID)
}

func (s *Store) UpdateHomeQuickLinkStatus(ctx context.Context, homeID string, linkID string, status string, statusCode int, checkedAt *time.Time, lastError string) (domain.HomeQuickLink, error) {
	var checkedValue any
	if checkedAt != nil {
		checkedValue = *checkedAt
	}
	result, err := s.exec(ctx, `UPDATE home_quick_links
		SET status = ?, status_code = ?, last_checked_at = ?, last_error = ?
		WHERE home_id = ? AND id = ?`,
		status,
		statusCode,
		checkedValue,
		lastError,
		homeID,
		linkID,
	)
	if err != nil {
		return domain.HomeQuickLink{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.HomeQuickLink{}, err
	}
	if rows == 0 {
		return domain.HomeQuickLink{}, ErrNotFound
	}
	return s.GetHomeQuickLink(ctx, homeID, linkID)
}

func (s *Store) DeleteHomeQuickLink(ctx context.Context, homeID string, linkID string) error {
	result, err := s.exec(ctx, `DELETE FROM home_quick_links WHERE home_id = ? AND id = ?`, homeID, linkID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReorderHomeQuickLinks(ctx context.Context, homeID string, linkIDs []string) error {
	seen := make(map[string]struct{}, len(linkIDs))
	for _, linkID := range linkIDs {
		if _, ok := seen[linkID]; ok {
			return fmt.Errorf("%w: duplicate link id %q", ErrConflict, linkID)
		}
		seen[linkID] = struct{}{}
	}

	count, err := s.CountHomeQuickLinks(ctx, homeID)
	if err != nil {
		return err
	}
	if count != len(linkIDs) {
		return ErrNotFound
	}

	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for index, linkID := range linkIDs {
		result, err := tx.ExecContext(ctx, `UPDATE home_quick_links SET sort_order = ? WHERE home_id = ? AND id = ?`, index*10, homeID, linkID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

func scanHomeQuickLink(scanner interface{ Scan(dest ...any) error }) (domain.HomeQuickLink, error) {
	var link domain.HomeQuickLink
	var lastChecked sql.NullTime
	err := scanner.Scan(
		&link.ID,
		&link.HomeID,
		&link.Title,
		&link.URL,
		&link.Description,
		&link.SortOrder,
		&link.HealthCheckEnabled,
		&link.Status,
		&link.StatusCode,
		&lastChecked,
		&link.LastError,
		&link.CreatedAt,
		&link.UpdatedAt,
		&link.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeQuickLink{}, ErrNotFound
	}
	if err != nil {
		return domain.HomeQuickLink{}, err
	}
	if lastChecked.Valid {
		link.LastCheckedAt = &lastChecked.Time
	}
	return link, nil
}
