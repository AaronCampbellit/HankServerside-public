package files

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"

	"github.com/dropfile/HankServerside/internal/protocol"
)

const smbDialTimeout = 15 * time.Second

func (s *Service) listSMB(ctx context.Context, sourceID string, filePath string) ([]protocol.FileItem, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	entries, err := share.ReadDir(resolveSharePath(filePath))
	if err != nil {
		return nil, err
	}

	items := make([]protocol.FileItem, 0, len(entries))
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		relative := path.Join(cleanPath(filePath), entry.Name())
		items = append(items, fileItem(relative, entry))
	}

	sortItems(items)
	return items, nil
}

func (s *Service) statSMB(ctx context.Context, sourceID string, filePath string) (protocol.FileItem, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return protocol.FileItem{}, err
	}
	defer cleanup()

	info, err := share.Stat(resolveSharePath(filePath))
	if err != nil {
		return protocol.FileItem{}, err
	}
	return fileItem(cleanPath(filePath), info), nil
}

func (s *Service) createDirectorySMB(ctx context.Context, sourceID string, filePath string) error {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return err
	}
	defer cleanup()

	return share.MkdirAll(resolveSharePath(filePath), 0o755)
}

func (s *Service) renameSMB(ctx context.Context, sourceID string, from string, to string) error {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return err
	}
	defer cleanup()

	destination := resolveSharePath(to)
	if dir := path.Dir(destination); dir != "." {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	return share.Rename(resolveSharePath(from), destination)
}

func (s *Service) deleteSMB(ctx context.Context, sourceID string, filePath string, isDirectory bool) error {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return err
	}
	defer cleanup()

	if isDirectory {
		return share.RemoveAll(resolveSharePath(filePath))
	}
	return share.Remove(resolveSharePath(filePath))
}

func (s *Service) downloadSMB(ctx context.Context, sourceID string, filePath string) (string, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return "", err
	}
	defer cleanup()

	data, err := share.ReadFile(resolveSharePath(filePath))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) uploadSMB(ctx context.Context, sourceID string, filePath string, contentBase64 string) error {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return err
	}
	defer cleanup()

	data, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return err
	}

	if dir := path.Dir(resolveSharePath(filePath)); dir != "." {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	return share.WriteFile(resolveSharePath(filePath), data, 0o644)
}

func (s *Service) openReaderSMB(ctx context.Context, sourceID string, filePath string, offset int64) (ReadHandle, fs.FileInfo, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return nil, nil, err
	}

	file, err := share.Open(resolveSharePath(filePath))
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = cleanup()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		_ = cleanup()
		return nil, nil, fmt.Errorf("path is a directory")
	}
	if offset < 0 || offset > info.Size() {
		_ = file.Close()
		_ = cleanup()
		return nil, nil, fmt.Errorf("invalid read offset")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		_ = cleanup()
		return nil, nil, err
	}

	return &smbReadHandle{file: file, cleanup: cleanup}, info, nil
}

func (s *Service) openWriterSMB(ctx context.Context, sourceID string, filePath string, offset int64) (WriteHandle, int64, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return nil, 0, err
	}

	resolved := resolveSharePath(filePath)
	if dir := path.Dir(resolved); dir != "." {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			_ = cleanup()
			return nil, 0, err
		}
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := share.OpenFile(resolved, flags, 0o644)
	if err != nil {
		_ = cleanup()
		return nil, 0, err
	}
	if offset == 0 {
		return &smbWriteHandle{file: file, cleanup: cleanup}, 0, nil
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = cleanup()
		return nil, 0, err
	}

	size := info.Size()
	if offset < 0 || offset > size {
		_ = file.Close()
		_ = cleanup()
		return nil, 0, fmt.Errorf("invalid write offset")
	}
	if size > offset {
		if err := file.Truncate(offset); err != nil {
			_ = file.Close()
			_ = cleanup()
			return nil, 0, err
		}
		size = offset
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		_ = cleanup()
		return nil, 0, err
	}

	return &smbWriteHandle{file: file, cleanup: cleanup}, size, nil
}

func (s *Service) openRandomWriterSMB(ctx context.Context, sourceID string, filePath string) (RandomWriteHandle, error) {
	share, cleanup, err := s.dialSMBShare(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	resolved := resolveSharePath(filePath)
	if dir := path.Dir(resolved); dir != "." {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			_ = cleanup()
			return nil, err
		}
	}

	file, err := share.OpenFile(resolved, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	return &smbRandomWriteHandle{file: file, cleanup: cleanup}, nil
}

func (s *Service) dialSMBShare(ctx context.Context, sourceID string) (*smb2.Share, func() error, error) {
	cfg, err := s.smbConfigForSource(sourceID)
	if err != nil {
		return nil, nil, err
	}
	if !cfg.Enabled() {
		return nil, nil, ErrDisabled
	}

	s.mu.RLock()
	if conn := s.smbConnections[cfg.ID]; conn != nil && sameSMBConfig(conn.cfg, cfg) {
		share := conn.share
		s.mu.RUnlock()
		return share.WithContext(ctx), func() error { return nil }, nil
	}
	s.mu.RUnlock()

	return s.dialAndCacheSMBShare(ctx, cfg)
}

func (s *Service) dialAndCacheSMBShare(ctx context.Context, cfg SMBConfig) (*smb2.Share, func() error, error) {
	address := smbAddress(cfg.Host)
	conn, err := (&net.Dialer{Timeout: smbDialTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}

	session, err := (&smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     cfg.Username,
			Password: cfg.Password,
			Domain:   cfg.Domain,
		},
	}).DialConn(ctx, conn, address)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	share, err := session.Mount(cfg.Share)
	if err != nil {
		_ = session.Logoff()
		_ = conn.Close()
		return nil, nil, err
	}

	cached := &smbConnection{
		cfg:     cfg,
		conn:    conn,
		session: session,
		share:   share,
	}

	s.mu.Lock()
	if current, ok := s.smbConfigForSourceLocked(cfg.ID); !ok || !sameSMBConfig(current, cfg) {
		s.mu.Unlock()
		_ = cached.close()
		return nil, nil, ErrDisabled
	}
	if existing := s.smbConnections[cfg.ID]; existing != nil {
		_ = existing.close()
	}
	s.smbConnections[cfg.ID] = cached
	s.mu.Unlock()

	return share.WithContext(ctx), func() error { return nil }, nil
}

func (s *Service) smbConfigForSource(sourceID string) (SMBConfig, error) {
	sourceID = cleanSourceID(sourceID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.smbConfigForSourceLocked(sourceID)
	if !ok {
		return SMBConfig{}, fmt.Errorf("file source %q is not configured", sourceID)
	}
	return cfg, nil
}

func (s *Service) smbConfigForSourceLocked(sourceID string) (SMBConfig, bool) {
	if sourceID == "" {
		for _, cfg := range s.smbShares {
			if cfg.Enabled() {
				return cfg, true
			}
		}
		return SMBConfig{}, false
	}
	for _, cfg := range s.smbShares {
		if cfg.ID == sourceID {
			return cfg, true
		}
	}
	return SMBConfig{}, false
}

type smbConnection struct {
	cfg     SMBConfig
	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share
}

func (c *smbConnection) close() error {
	if c == nil {
		return nil
	}
	return errors.Join(c.share.Umount(), c.session.Logoff(), c.conn.Close())
}

func sameSMBConfig(left SMBConfig, right SMBConfig) bool {
	return left.ID == right.ID &&
		left.Host == right.Host &&
		left.Share == right.Share &&
		left.Username == right.Username &&
		left.Password == right.Password &&
		left.Domain == right.Domain
}

func smbAddress(host string) string {
	host = NormalizeSMBHost(host)
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "445")
}

type smbReadHandle struct {
	file    *smb2.File
	cleanup func() error
}

func (h *smbReadHandle) Read(p []byte) (int, error) {
	return h.file.Read(p)
}

func (h *smbReadHandle) Close() error {
	return closeSMBHandle(h.file, h.cleanup)
}

type smbWriteHandle struct {
	file    *smb2.File
	cleanup func() error
}

func (h *smbWriteHandle) Write(p []byte) (int, error) {
	return h.file.Write(p)
}

func (h *smbWriteHandle) Close() error {
	return closeSMBHandle(h.file, h.cleanup)
}

type smbRandomWriteHandle struct {
	file    *smb2.File
	cleanup func() error
}

func (h *smbRandomWriteHandle) WriteAt(p []byte, off int64) (int, error) {
	return h.file.WriteAt(p, off)
}

func (h *smbRandomWriteHandle) Truncate(size int64) error {
	return h.file.Truncate(size)
}

func (h *smbRandomWriteHandle) Close() error {
	return closeSMBHandle(h.file, h.cleanup)
}

func closeSMBHandle(file *smb2.File, cleanup func() error) error {
	return finishSMBCleanup(file.Close(), cleanup())
}

func finishSMBCleanup(fileErr error, cleanupErr error) error {
	if fileErr != nil {
		return fileErr
	}
	if isBenignSMBCleanupError(cleanupErr) {
		return nil
	}
	return cleanupErr
}

func isBenignSMBCleanupError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
