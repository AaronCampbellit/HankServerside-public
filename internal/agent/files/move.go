package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/dropfile/HankServerside/internal/protocol"
)

const moveChunkSize = 256 * 1024

type MoveProgress struct {
	BytesTotal int64
	BytesDone  int64
	FilesTotal int64
	FilesDone  int64
}

type MoveFailureStage string

const (
	MoveFailureStageCopy     MoveFailureStage = "copy"
	MoveFailureStageVerified MoveFailureStage = "verified"
)

type MoveError struct {
	Stage MoveFailureStage
	Err   error
}

func (e *MoveError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *MoveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (s *Service) MoveBetweenSourcesWithProgress(ctx context.Context, sourceID string, destinationSourceID string, from string, to string, isDirectory bool, report func(MoveProgress)) (MoveProgress, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return MoveProgress{}, err
	}
	destination, err := s.sourceForID(destinationSourceID)
	if err != nil {
		return MoveProgress{}, err
	}
	if err := source.authorize("delete", from, 0); err != nil {
		return MoveProgress{}, err
	}
	if err := destination.authorize("write", to, 0); err != nil {
		return MoveProgress{}, err
	}

	if source.ID == destination.ID && source.Type == destination.Type {
		if err := validateSameSourceMove(from, to, isDirectory); err != nil {
			return MoveProgress{}, err
		}
		if err := s.RenameSource(ctx, source.ID, from, to); err != nil {
			return MoveProgress{}, err
		}
		progress := MoveProgress{FilesTotal: 1, FilesDone: 1}
		if stat, err := s.StatSource(ctx, source.ID, to); err == nil && !stat.IsDirectory {
			progress.BytesTotal = stat.Size
			progress.BytesDone = stat.Size
		}
		if report != nil {
			report(progress)
		}
		return progress, nil
	}

	plan, err := s.planMove(ctx, source.ID, from, to, isDirectory)
	if err != nil {
		return MoveProgress{}, err
	}
	progress := MoveProgress{BytesTotal: plan.bytesTotal, FilesTotal: int64(len(plan.files))}
	if report != nil {
		report(progress)
	}
	if err := s.copyMovePlan(ctx, source.ID, destination.ID, plan, &progress, report); err != nil {
		return progress, &MoveError{Stage: MoveFailureStageCopy, Err: err}
	}
	if err := s.verifyMovePlan(ctx, source.ID, destination.ID, plan); err != nil {
		return progress, &MoveError{Stage: MoveFailureStageCopy, Err: err}
	}
	select {
	case <-ctx.Done():
		return progress, &MoveError{Stage: MoveFailureStageVerified, Err: ctx.Err()}
	default:
	}
	if err := s.DeleteSource(ctx, source.ID, from, isDirectory); err != nil {
		return progress, &MoveError{Stage: MoveFailureStageVerified, Err: err}
	}
	return progress, nil
}

func validateSameSourceMove(from string, to string, isDirectory bool) error {
	sourcePath := cleanPath(from)
	destinationPath := cleanPath(to)
	if sourcePath == destinationPath {
		return fmt.Errorf("item is already at destination")
	}
	if isDirectory && destinationPath != "" && strings.HasPrefix(destinationPath, sourcePath+"/") {
		return fmt.Errorf("cannot move a directory inside itself")
	}
	return nil
}

type movePlan struct {
	dirs       []movePath
	files      []movePath
	bytesTotal int64
}

type movePath struct {
	from string
	to   string
	size int64
}

func (s *Service) planMove(ctx context.Context, sourceID string, from string, to string, isDirectory bool) (movePlan, error) {
	if !isDirectory {
		item, err := s.StatSource(ctx, sourceID, from)
		if err != nil {
			return movePlan{}, err
		}
		if item.IsDirectory {
			isDirectory = true
		} else {
			return movePlan{files: []movePath{{from: from, to: to, size: item.Size}}, bytesTotal: item.Size}, nil
		}
	}
	plan := movePlan{dirs: []movePath{{from: from, to: to}}}
	if err := s.planMoveDirectory(ctx, sourceID, from, to, &plan); err != nil {
		return movePlan{}, err
	}
	return plan, nil
}

func (s *Service) planMoveDirectory(ctx context.Context, sourceID string, from string, to string, plan *movePlan) error {
	items, err := s.ListSource(ctx, sourceID, from)
	if err != nil {
		return err
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		childTo := filepath.ToSlash(filepath.Join(to, item.Name))
		if item.IsDirectory {
			plan.dirs = append(plan.dirs, movePath{from: item.Path, to: childTo})
			if err := s.planMoveDirectory(ctx, sourceID, item.Path, childTo, plan); err != nil {
				return err
			}
			continue
		}
		plan.files = append(plan.files, movePath{from: item.Path, to: childTo, size: item.Size})
		plan.bytesTotal += item.Size
	}
	return nil
}

func (s *Service) copyMovePlan(ctx context.Context, sourceID string, destinationSourceID string, plan movePlan, progress *MoveProgress, report func(MoveProgress)) error {
	for _, dir := range plan.dirs {
		if err := s.CreateDirectorySource(ctx, destinationSourceID, dir.to); err != nil {
			return err
		}
	}
	for _, file := range plan.files {
		if err := s.copyMoveFile(ctx, sourceID, destinationSourceID, file, progress, report); err != nil {
			return err
		}
		progress.FilesDone++
		if report != nil {
			report(*progress)
		}
	}
	return nil
}

func (s *Service) copyMoveFile(ctx context.Context, sourceID string, destinationSourceID string, file movePath, progress *MoveProgress, report func(MoveProgress)) error {
	reader, _, err := s.OpenReaderSource(ctx, sourceID, file.from, 0)
	if err != nil {
		return err
	}
	defer reader.Close()

	writer, _, err := s.OpenWriterSource(ctx, destinationSourceID, file.to, 0)
	if err != nil {
		return err
	}
	closeWriter := true
	defer func() {
		if closeWriter {
			_ = writer.Close()
		}
	}()

	buffer := make([]byte, moveChunkSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			written, writeErr := writer.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
			progress.BytesDone += int64(written)
			if report != nil {
				report(*progress)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	closeWriter = false
	return nil
}

func (s *Service) verifyMovePlan(ctx context.Context, sourceID string, destinationSourceID string, plan movePlan) error {
	for _, file := range plan.files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.verifyCopiedFile(ctx, sourceID, destinationSourceID, file.from, file.to); err != nil {
			return err
		}
	}
	return nil
}

func MoveErrorStage(err error) MoveFailureStage {
	var moveErr *MoveError
	if errors.As(err, &moveErr) {
		return moveErr.Stage
	}
	return MoveFailureStageCopy
}

func MoveStatusForError(err error) string {
	if errors.Is(err, context.Canceled) {
		if MoveErrorStage(err) == MoveFailureStageVerified {
			return "rollback_required"
		}
		return "cancelled"
	}
	if MoveErrorStage(err) == MoveFailureStageVerified {
		return "rollback_required"
	}
	return "failed"
}

func MoveErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "move cancelled"
	}
	return fmt.Sprintf("%v", err)
}

func MoveJobEventFromProgress(request protocol.FilesMoveRequest, status string, progress MoveProgress, errorMessage string) protocol.FileOperationJobEvent {
	return protocol.FileOperationJobEvent{
		JobID:               request.JobID,
		Status:              status,
		SourceID:            request.SourceID,
		DestinationSourceID: request.DestinationSourceID,
		From:                request.From,
		To:                  request.To,
		IsDirectory:         request.IsDirectory,
		BytesTotal:          progress.BytesTotal,
		BytesDone:           progress.BytesDone,
		FilesTotal:          progress.FilesTotal,
		FilesDone:           progress.FilesDone,
		ErrorMessage:        errorMessage,
	}
}
