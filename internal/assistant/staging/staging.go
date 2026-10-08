// Package staging stores immutable, verified attachment bytes below Hank's
// configured attachment root. PostgreSQL owns authorization and expiry; a file
// becomes addressable only after its binding is committed there.
package staging

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
)

const MaxBytes int64 = 100 << 20

var keyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrInvalid = errors.New("invalid staged attachment")

// Key binds bytes to the authenticated owner, conversation and attachment.
// Changing any identity or content creates a different immutable object.
func Key(home, user, session, attachment, digest string) string {
	hash := sha256.New()
	for _, part := range []string{home, user, session, attachment, digest} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// Put rejects short, oversized and changed bytes. Interrupted temporary files
// are never readable via Open. Retrying the same upload is safe after restart.
func Put(rootPath, key, digest string, size int64, source io.Reader) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	return PutRoot(root, key, digest, size, source)
}

func PutRoot(root *os.Root, key, digest string, size int64, source io.Reader) error {
	if !keyPattern.MatchString(key) || !keyPattern.MatchString(digest) || size < 1 || size > MaxBytes {
		return ErrInvalid
	}
	tmp := ".upload-" + rand.Text()
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	// Share only with the attachment volume's private backup group. Explicit
	// chmod preserves this contract even with a restrictive process umask.
	if err = file.Chmod(0660); err != nil {
		file.Close()
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, size+1))
	if copyErr != nil {
		file.Close()
		return copyErr
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		file.Close()
		return ErrInvalid
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// All writers for this key must have the same identity and verified digest.
	// An atomic replacement therefore has exactly the same observable contents.
	if err = root.Rename(tmp, key); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Open verifies disk contents again before returning bytes for an execution.
// A missing/corrupted object requires re-upload; it is never a successful write.
func Open(rootPath, key, digest string, size int64) (*os.File, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return OpenRoot(root, key, digest, size)
}

func OpenRoot(root *os.Root, key, digest string, size int64) (*os.File, error) {
	if !keyPattern.MatchString(key) || !keyPattern.MatchString(digest) || size < 1 || size > MaxBytes {
		return nil, ErrInvalid
	}
	file, err := root.Open(key)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		file.Close()
		return nil, ErrInvalid
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		file.Close()
		return nil, ErrInvalid
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
