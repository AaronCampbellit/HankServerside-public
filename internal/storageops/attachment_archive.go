package storageops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"filippo.io/age"
)

var attachmentBackupLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// writeEncryptedAttachments never writes a plaintext archive to disk. Only a
// complete, synced age stream may replace a previously completed backup.
func writeEncryptedAttachments(ctx context.Context, source, archive, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("attachment encryption requires the backup repository key")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return errors.New("cannot open attachment source")
	}
	defer root.Close()
	if err = os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		return errors.New("cannot prepare attachment archive directory")
	}
	destination, err := os.OpenRoot(filepath.Dir(archive))
	if err != nil {
		return errors.New("cannot open attachment archive directory")
	}
	defer destination.Close()
	name := ".attachment-" + rand.Text() + ".tmp"
	output, err := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot create encrypted attachment archive")
	}
	defer func() { output.Close(); destination.Remove(name) }()
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return errors.New("cannot configure attachment encryption")
	}
	recipient.SetWorkFactor(18)
	encrypted, err := age.Encrypt(output, recipient)
	if err != nil {
		return errors.New("cannot initialize attachment encryption")
	}
	compressed := gzip.NewWriter(encrypted)
	archiveWriter := tar.NewWriter(compressed)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == RecoveryDirectoryName {
			return fs.SkipDir
		}
		if name == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("attachment source contains a link or special file")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() {
			return archiveWriter.WriteHeader(header)
		}
		input, err := root.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		current, err := input.Stat()
		if err != nil {
			return err
		}
		if !current.Mode().IsRegular() || !os.SameFile(info, current) {
			return errors.New("attachment source changed during backup")
		}
		if err = archiveWriter.WriteHeader(header); err != nil {
			return err
		}
		_, err = io.CopyN(archiveWriter, archiveContextReader{ctx, input}, header.Size)
		return err
	})
	if err != nil {
		return errors.New("attachment archive creation failed")
	}
	if archiveWriter.Close() != nil {
		return errors.New("attachment archive finalization failed")
	}
	if compressed.Close() != nil {
		return errors.New("attachment compression finalization failed")
	}
	if encrypted.Close() != nil {
		return errors.New("attachment encryption finalization failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if output.Sync() != nil {
		return errors.New("encrypted attachment archive sync failed")
	}
	if output.Close() != nil {
		return errors.New("encrypted attachment archive close failed")
	}
	if err = destination.Rename(name, filepath.Base(archive)); err != nil {
		return errors.New("encrypted attachment archive publication failed")
	}
	return nil
}

type archiveContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

// extractAttachmentArchive consumes the complete authenticated stream before
// its caller can publish this private staging directory. Archive paths are
// contained by os.Root; links and special files are never materialized.
func extractAttachmentArchive(ctx context.Context, archive, staging, password string, encrypted bool) error {
	archiveRoot, err := os.OpenRoot(filepath.Dir(archive))
	if err != nil {
		return errors.New("cannot open attachment archive directory")
	}
	defer archiveRoot.Close()
	info, err := archiveRoot.Lstat(filepath.Base(archive))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("attachment archive must be a regular file")
	}
	input, err := archiveRoot.Open(filepath.Base(archive))
	if err != nil {
		return errors.New("cannot read attachment archive")
	}
	defer input.Close()
	var payload io.Reader = archiveContextReader{ctx, input}
	if encrypted {
		identity, err := age.NewScryptIdentity(password)
		if err != nil {
			return errors.New("attachment decryption requires the backup repository key")
		}
		identity.SetMaxWorkFactor(18)
		payload, err = age.Decrypt(payload, identity)
		if err != nil {
			return errors.New("attachment archive authentication failed")
		}
	}
	compressed, err := gzip.NewReader(payload)
	if err != nil {
		return errors.New("attachment archive decoding failed")
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	root, err := os.OpenRoot(staging)
	if err != nil {
		return errors.New("cannot open attachment restore staging")
	}
	defer root.Close()
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("attachment archive decoding failed")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if name == "." || name == "" {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid attachment archive path")
		}
		if !fs.ValidPath(name) || strings.Contains(name, `\`) {
			return errors.New("invalid attachment archive path")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err = root.MkdirAll(name, 0700); err != nil {
				return errors.New("cannot create attachment restore directory")
			}
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			if err = root.MkdirAll(filepath.Dir(name), 0700); err != nil {
				return errors.New("cannot create attachment restore directory")
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return errors.New("cannot create restored attachment")
			}
			_, copyErr := io.Copy(file, archiveContextReader{ctx, reader})
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil {
				return errors.New("attachment extraction failed")
			}
		case tar.TypeLink:
			// GNU tar's legacy writer deduplicated hard-linked source files.
			// Copy only a previously extracted regular file inside staging; do
			// not create filesystem links or trust archive-provided link paths.
			target := strings.TrimPrefix(header.Linkname, "./")
			if !fs.ValidPath(target) || strings.Contains(target, `\`) {
				return errors.New("invalid attachment hard link target")
			}
			input, err := root.Open(target)
			if err != nil {
				return errors.New("attachment hard link target is unavailable")
			}
			info, statErr := input.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				input.Close()
				return errors.New("invalid attachment hard link target")
			}
			if err = root.MkdirAll(filepath.Dir(name), 0700); err != nil {
				input.Close()
				return errors.New("cannot create attachment restore directory")
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				input.Close()
				return errors.New("cannot create restored attachment")
			}
			_, copyErr := io.Copy(file, archiveContextReader{ctx, input})
			input.Close()
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil {
				return errors.New("attachment hard link extraction failed")
			}
		default:
			return errors.New("attachment archive contains a link or special file")
		}
	}
	// tar EOF can precede the gzip checksum and age authentication terminator.
	if _, err = io.Copy(io.Discard, compressed); err != nil {
		return errors.New("attachment archive integrity validation failed")
	}
	if _, err = io.Copy(io.Discard, payload); err != nil {
		return errors.New("attachment archive authentication failed")
	}
	return ctx.Err()
}

func publishRestoredAttachments(staging, destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(staging) {
			continue
		}
		if err = os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = os.Rename(filepath.Join(staging, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
