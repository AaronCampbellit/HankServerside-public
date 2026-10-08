package cloud

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/dropfile/HankServerside/internal/assistant/staging"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) assistantStageRoot() (*os.Root, error) {
	// Reuse the existing configured attachment volume. os.Root contains every
	// byte operation; model arguments never supply a filesystem path.
	root, err := os.OpenRoot(s.noteAttachmentRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.Mkdir(".assistant-staging", noteAttachmentDirMode.Perm()); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := root.Lstat(".assistant-staging")
	if err != nil || !info.IsDir() {
		return nil, errors.New("invalid assistant staging directory")
	}
	staged, err := root.OpenRoot(".assistant-staging")
	if err != nil {
		return nil, err
	}
	// The private attachment group includes the encrypted-backup worker.
	// Match note attachments, including existing directories and restrictive umasks.
	if info.Mode()&(os.ModePerm|os.ModeSetgid) != noteAttachmentDirMode {
		if err = staged.Chmod(".", noteAttachmentDirMode); err != nil {
			staged.Close()
			return nil, err
		}
	}
	return staged, nil
}

// One Home has one cloud writer. Serialize staging admission so concurrent
// uploads cannot exceed the persistent staging budget, including orphan files.
var assistantStageAdmission = make(chan struct{}, 1)

const assistantStageBudget int64 = 4 << 30

func checkAssistantStageBudget(root *os.Root, key string, size int64) error {
	if existing, err := root.Stat(key); err == nil && existing.Mode().IsRegular() && existing.Size() == size {
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	used := int64(0)
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() {
				return errors.New("unexpected staging entry")
			}
			used += info.Size()
			if used > assistantStageBudget-size {
				return errors.New("assistant staging capacity reached")
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

func (s *Server) stageAssistantAttachment(ctx context.Context, home, user, session string, metadata domain.AssistantStage, source io.Reader) (domain.AssistantStage, error) {
	if _, err := s.executionToolContext(ctx, home, user, "home_member"); err != nil {
		return domain.AssistantStage{}, err
	}
	owned, err := s.store.GetAssistantSession(ctx, session)
	if err != nil || owned.HomeID != home || owned.UserID != user {
		return domain.AssistantStage{}, store.ErrNotFound
	}
	metadata.ID = newID("astage")
	metadata.HomeID = home
	metadata.UserID = user
	metadata.SessionID = session
	metadata.StorageKey = staging.Key(home, user, session, metadata.ClientAttachmentID, metadata.ChecksumSHA256)
	root, err := s.assistantStageRoot()
	if err != nil {
		return domain.AssistantStage{}, err
	}
	defer root.Close()
	select {
	case assistantStageAdmission <- struct{}{}:
		defer func() { <-assistantStageAdmission }()
	case <-ctx.Done():
		return domain.AssistantStage{}, ctx.Err()
	}
	if err = checkAssistantStageBudget(root, metadata.StorageKey, metadata.SizeBytes); err != nil {
		return domain.AssistantStage{}, err
	}
	if err = staging.PutRoot(root, metadata.StorageKey, metadata.ChecksumSHA256, metadata.SizeBytes, source); err != nil {
		return domain.AssistantStage{}, err
	}
	if _, err = s.executionToolContext(ctx, home, user, "home_member"); err != nil {
		return domain.AssistantStage{}, err
	}
	return s.store.BindAssistantStage(ctx, metadata)
}

func (s *Server) openAssistantAttachment(ctx context.Context, home, user, session, id string) (*os.File, error) {
	if _, err := s.executionToolContext(ctx, home, user, "home_member"); err != nil {
		return nil, err
	}
	stage, err := s.store.GetAssistantStage(ctx, home, user, session, id)
	if err != nil {
		return nil, err
	}
	root, err := s.assistantStageRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return staging.OpenRoot(root, stage.StorageKey, stage.ChecksumSHA256, stage.SizeBytes)
}
