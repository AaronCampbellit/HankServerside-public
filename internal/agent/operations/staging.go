package operations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"

	"github.com/dropfile/HankServerside/internal/assistant/staging"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const MaxStageChunk = 256 << 10

func (j *Journal) stageRoot(identity protocol.AssistantOperationIdentity, digest string, size int64) (*os.Root, string, error) {
	if !valid(identity) || identity.JournalEpoch != j.epoch || !validDigest.MatchString(digest) || size < 1 || size > staging.MaxBytes {
		return nil, "", ErrConflict
	}
	if err := j.root.Mkdir("staging", 0700); err != nil && !os.IsExist(err) {
		return nil, "", err
	}
	root, err := j.root.OpenRoot("staging")
	if err != nil {
		return nil, "", err
	}
	raw, _ := json.Marshal(struct {
		Identity protocol.AssistantOperationIdentity
		Digest   string
		Size     int64
	}{identity, digest, size})
	sum := sha256.Sum256(raw)
	return root, hex.EncodeToString(sum[:]), nil
}

// StageChunk persists retryable transport bytes, never a destination effect.
// Repeated chunks must match already durable bytes exactly; gaps are rejected.
func (j *Journal) StageChunk(identity protocol.AssistantOperationIdentity, digest string, size, offset int64, data []byte) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(data) < 1 || len(data) > MaxStageChunk || offset < 0 || offset > size-int64(len(data)) {
		return 0, ErrConflict
	}
	root, key, err := j.stageRoot(identity, digest, size)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if complete, err := staging.OpenRoot(root, key, digest, size); err == nil {
		complete.Close()
		return size, nil
	}
	file, err := root.OpenFile(key+".partial", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if offset > info.Size() || info.Size() > size {
		return 0, ErrConflict
	}
	if offset < info.Size() {
		existing := make([]byte, len(data))
		if _, err := file.ReadAt(existing, offset); err != nil || !bytes.Equal(existing, data) {
			return 0, ErrConflict
		}
	} else {
		if _, err := file.WriteAt(data, offset); err != nil {
			return 0, err
		}
		if err := file.Sync(); err != nil {
			return 0, err
		}
	}
	info, err = file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() == size {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if err := staging.PutRoot(root, key, digest, size, file); err != nil {
			return 0, err
		}
		// The immutable verified file survives; a leftover partial is harmless.
		_ = root.Remove(key + ".partial")
	}
	return info.Size(), nil
}

func (j *Journal) OpenStage(identity protocol.AssistantOperationIdentity, digest string, size int64) (*os.File, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	root, key, err := j.stageRoot(identity, digest, size)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return staging.OpenRoot(root, key, digest, size)
}
