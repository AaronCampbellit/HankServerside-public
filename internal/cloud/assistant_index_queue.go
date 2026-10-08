package cloud

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	assistantIndexSourceProfileNote   = "profile_note"
	assistantIndexSourceSharedNote    = "shared_note"
	assistantIndexSourceCalendar      = "calendar"
	assistantIndexSourceFiles         = "files"
	assistantIndexSourceProjectDocs   = "project_docs"
	assistantIndexSourceHomeAssistant = "homeassistant"

	assistantIndexWorkerInterval = 2 * time.Second
	assistantIndexJobTimeout     = 2 * time.Minute
)

func (s *Server) enqueueAssistantIndexJob(ctx context.Context, homeID string, userID string, sourceType string, sourceID string) {
	if strings.TrimSpace(homeID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(sourceType) == "" {
		return
	}
	if _, err := s.store.EnqueueAssistantIndexJob(ctx, domain.AssistantIndexJob{
		HomeID:     homeID,
		UserID:     userID,
		SourceType: sourceType,
		SourceID:   sourceID,
	}); err != nil {
		s.logger.Warn("assistant index job enqueue failed", "home_id", homeID, "user_id", userID, "source_type", sourceType, "error_code", "operation_failed")
	}
}

func (s *Server) enqueueAssistantNoteIndexJob(ctx context.Context, homeID string, userID string, noteID string, sourceType string) {
	if strings.TrimSpace(homeID) == "" {
		if home, err := s.store.GetSingletonHomeForUser(ctx, userID); err == nil {
			homeID = home.ID
		} else if home, err := s.store.GetSingletonHome(ctx); err == nil {
			homeID = home.ID
		}
	}
	s.enqueueAssistantIndexJob(ctx, homeID, userID, sourceType, noteID)
}

func (s *Server) startAssistantIndexWorker(ctx context.Context) {
	if err := s.store.RequeueRunningAssistantIndexJobs(context.Background(), time.Now().UTC()); err != nil {
		s.logger.Warn("failed to requeue interrupted assistant index jobs", "error_code", "operation_failed")
	}
	go func() {
		ticker := time.NewTicker(assistantIndexWorkerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if processed := s.processNextAssistantIndexJob(ctx); !processed {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}
	}()
}

func (s *Server) processNextAssistantIndexJob(ctx context.Context) bool {
	job, err := s.store.ClaimAssistantIndexJob(ctx, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		s.logger.Warn("assistant index job claim failed", "error_code", "operation_failed")
		return false
	}

	jobCtx, cancel := context.WithTimeout(ctx, assistantIndexJobTimeout)
	defer cancel()
	if err := s.processAssistantIndexJob(jobCtx, job); err != nil {
		delay := assistantIndexRetryDelay(job.Attempts)
		if job.Attempts >= 5 {
			delay = 30 * time.Minute
		}
		if failErr := s.store.FailAssistantIndexJob(context.Background(), job.ID, err.Error(), time.Now().UTC().Add(delay), time.Now().UTC()); failErr != nil {
			s.logger.Warn("assistant index job failure update failed", "job_id", job.ID, "error_code", "operation_failed")
		}
		s.logger.Warn("assistant index job failed", "job_id", job.ID, "source_type", job.SourceType, "error_code", "operation_failed")
		return true
	}
	if err := s.store.CompleteAssistantIndexJob(context.Background(), job.ID, time.Now().UTC()); err != nil {
		s.logger.Warn("assistant index job completion update failed", "job_id", job.ID, "error_code", "operation_failed")
	}
	return true
}

func (s *Server) processAssistantIndexJob(ctx context.Context, job domain.AssistantIndexJob) error {
	settings, err := s.currentAssistantSettings(ctx, job.HomeID, job.UserID)
	if err != nil {
		return err
	}
	switch job.SourceType {
	case assistantConversationSourceType:
		if !settings.ConversationsEnabled {
			return nil
		}
		session, err := s.store.GetAssistantSession(ctx, job.SourceID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.HomeID != job.HomeID || session.UserID != job.UserID {
			return errors.New("assistant index session scope mismatch")
		}
		return s.indexAssistantConversation(ctx, session, job.UserID, settings)
	case assistantIndexSourceProfileNote:
		if !settings.ProfileNotesEnabled {
			return nil
		}
		note, err := s.store.GetProfileNote(ctx, job.UserID, job.SourceID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if note.DeletedAt != nil {
			return s.store.DeleteAssistantDocumentForSource(ctx, job.HomeID, job.UserID, assistantIndexSourceProfileNote, note.ID)
		}
		return s.indexAssistantNote(ctx, job.HomeID, job.UserID, assistantIndexSourceProfileNote, note, settings)
	case assistantIndexSourceSharedNote:
		if !settings.HomeNotesEnabled {
			return nil
		}
		note, err := s.store.GetHomeNoteVisibleToUser(ctx, job.HomeID, job.UserID, job.SourceID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if note.DeletedAt != nil {
			return s.store.DeleteAssistantDocumentForSource(ctx, job.HomeID, job.UserID, assistantIndexSourceSharedNote, note.ID)
		}
		return s.indexAssistantNote(ctx, job.HomeID, job.UserID, assistantIndexSourceSharedNote, note, settings)
	case assistantIndexSourceCalendar:
		if !settings.CalendarEnabled {
			return nil
		}
		return s.indexAssistantCalendarSnapshot(ctx, job.HomeID, job.UserID, settings)
	case assistantIndexSourceProjectDocs:
		if !settings.ProjectDocsEnabled {
			return nil
		}
		return s.indexAssistantProjectDocs(ctx, job.HomeID, job.UserID, settings)
	case assistantIndexSourceHomeAssistant:
		if !settings.HomeAssistantEnabled {
			return nil
		}
		home, membership, auth, err := s.assistantIndexRuntime(ctx, job.HomeID, job.UserID)
		if err != nil {
			return err
		}
		return s.indexAssistantHomeAssistantStates(ctx, home, membership, auth, settings)
	case assistantIndexSourceFiles:
		if !settings.FilesEnabled {
			return nil
		}
		home, membership, auth, err := s.assistantIndexRuntime(ctx, job.HomeID, job.UserID)
		if err != nil {
			return err
		}
		return s.indexAssistantFiles(ctx, home, membership, auth, settings)
	default:
		return fmt.Errorf("unknown assistant index source type %q", job.SourceType)
	}
}

func (s *Server) assistantIndexRuntime(ctx context.Context, homeID string, userID string) (domain.Home, domain.HomeMembership, authContext, error) {
	home, err := s.store.GetHomeForUser(ctx, homeID, userID)
	if err != nil {
		return domain.Home{}, domain.HomeMembership{}, authContext{}, err
	}
	membership, err := s.store.GetHomeMembership(ctx, homeID, userID)
	if err != nil {
		return domain.Home{}, domain.HomeMembership{}, authContext{}, err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return domain.Home{}, domain.HomeMembership{}, authContext{}, err
	}
	return home, membership, authContext{User: user}, nil
}

func assistantIndexRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	seconds := math.Pow(2, float64(attempts-1))
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}
