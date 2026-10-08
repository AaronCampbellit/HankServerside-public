package files

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// CreateDirectoryExclusiveSource creates exactly one new directory. Existing
// targets and missing parents fail instead of turning a replay into success.
// The caller must journal its intent before invoking this operation.
func (s *Service) CreateDirectoryExclusiveSource(ctx context.Context, sourceID, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsAny(target, "\\\x00") {
		return fmt.Errorf("invalid directory path")
	}
	for _, part := range strings.Split(target, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("invalid directory path")
		}
	}
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("write", target, 0); err != nil {
		return err
	}
	if source.Type == fileSourceTypeSMB {
		share, cleanup, err := s.dialSMBShare(ctx, source.ID)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := share.Mkdir(resolveSharePath(target), 0755); err != nil {
			return err
		}
	} else {
		root, err := os.OpenRoot(source.Root)
		if err != nil {
			return err
		}
		defer root.Close()
		if err := root.Mkdir(cleanPath(target), 0755); err != nil {
			return err
		}
	}
	s.invalidateSearchIndex(source.ID)
	return nil
}

// UploadExclusiveSource writes a new exact target from verified staged bytes.
// Any failure after creation leaves an uncertain partial file for inspection;
// callers must not retry or overwrite it without a new explicit user action.
func (s *Service) UploadExclusiveSource(ctx context.Context, sourceID, target string, size int64, reader io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size < 1 || strings.ContainsAny(target, "\\\x00") {
		return fmt.Errorf("invalid upload")
	}
	for _, part := range strings.Split(target, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("invalid upload path")
		}
	}
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("write", target, size); err != nil {
		return err
	}
	type syncedWriter interface {
		io.Writer
		io.Closer
		Sync() error
	}
	var file syncedWriter
	if source.Type == fileSourceTypeSMB {
		share, cleanup, err := s.dialSMBShare(ctx, source.ID)
		if err != nil {
			return err
		}
		defer cleanup()
		file, err = share.OpenFile(resolveSharePath(target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
	} else {
		root, err := os.OpenRoot(source.Root)
		if err != nil {
			return err
		}
		defer root.Close()
		file, err = root.OpenFile(cleanPath(target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
	}
	defer s.invalidateSearchIndex(source.ID)
	written, err := io.Copy(file, io.LimitReader(reader, size+1))
	if err == nil && written != size {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
