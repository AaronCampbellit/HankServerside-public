// Package operations persists destination receipts before any machine effect.
// It stores identities/digests, never command arguments or credentials.
package operations

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var ErrConflict = errors.New("operation identity conflict")
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var validDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// One journal instance owns an agent state directory. An exclusive directory lock
// prevents competing processes from advancing destination receipts.
type Journal struct {
	mu    sync.Mutex
	root  *os.Root
	lock  *os.File
	epoch string
}

func Open(directory string) (*Journal, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	if err := lockJournal(lock); err != nil {
		lock.Close()
		root.Close()
		return nil, err
	}
	j := &Journal{root: root, lock: lock}
	if err := j.loadEpoch(); err != nil {
		j.Close()
		return nil, err
	}
	return j, nil
}

// Epoch identifies this durable receipt store. A replacement store must never
// authorize replay of an operation prepared against its predecessor.
func (j *Journal) Epoch() string { return j.epoch }

func (j *Journal) loadEpoch() error {
	file, err := j.root.Open(".epoch")
	if err == nil {
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, 33))
		if err != nil {
			return err
		}
		if len(raw) != 32 {
			return ErrConflict
		}
		if _, err := hex.DecodeString(string(raw)); err != nil {
			return ErrConflict
		}
		j.epoch = string(raw)
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Existing receipts without their epoch indicate lost metadata. Fail closed.
	directory, err := j.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			return ErrConflict
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	j.epoch = hex.EncodeToString(random)
	file, err = j.root.OpenFile(".epoch", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(j.epoch)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return j.syncDirectory()
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = unlockJournal(j.lock)
	_ = j.lock.Close()
	return j.root.Close()
}
func valid(identity protocol.AssistantOperationIdentity) bool {
	return validID.MatchString(identity.OperationID) && validDigest.MatchString(identity.ActionDigest) && identity.HomeID != "" && identity.UserID != "" && identity.Tool != "" && identity.ToolVersion > 0 && (identity.AgentID != "" || identity.DeviceID != "")
}

// Begin durably marks an uncertain operation before the caller dispatches it.
// Only fresh=true authorizes a first attempt. An existing unknown receipt may
// represent a crash before or after the effect and must never trigger replay.
func (j *Journal) Begin(identity protocol.AssistantOperationIdentity) (protocol.AssistantOperationStatusResponse, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !valid(identity) || (identity.JournalEpoch != "" && identity.JournalEpoch != j.epoch) {
		return protocol.AssistantOperationStatusResponse{}, false, ErrConflict
	}
	existing, err := j.lookup(identity)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return existing, false, err
	}
	record := protocol.AssistantOperationStatusResponse{Identity: identity, Outcome: "unknown"}
	raw, err := json.Marshal(record)
	if err != nil {
		return record, false, err
	}
	file, err := j.root.OpenFile(identity.OperationID+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, err = j.lookup(identity)
		return existing, false, err
	}
	if err != nil {
		return record, false, err
	}
	// A partial record remains a fail-closed tombstone if writing/sync fails.
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = j.syncDirectory()
	}
	return record, err == nil, err
}
func (j *Journal) Lookup(identity protocol.AssistantOperationIdentity) (protocol.AssistantOperationStatusResponse, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lookup(identity)
}
func (j *Journal) lookup(identity protocol.AssistantOperationIdentity) (protocol.AssistantOperationStatusResponse, error) {
	var result protocol.AssistantOperationStatusResponse
	if !valid(identity) || (identity.JournalEpoch != "" && identity.JournalEpoch != j.epoch) {
		return result, ErrConflict
	}
	file, err := j.root.Open(identity.OperationID + ".json")
	if err != nil {
		return result, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 262145))
	if err != nil {
		return result, err
	}
	if len(raw) > 262144 {
		return result, ErrConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return result, ErrConflict
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, ErrConflict
	}
	if result.Identity != identity {
		return result, ErrConflict
	}
	switch result.Outcome {
	case "unknown", "accepted", "confirmed", "failed":
	default:
		return result, ErrConflict
	}
	return result, nil
}
func (j *Journal) Complete(record protocol.AssistantOperationStatusResponse) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if record.Outcome != "confirmed" && record.Outcome != "failed" && record.Outcome != "accepted" && record.Outcome != "unknown" {
		return ErrConflict
	}
	existing, err := j.lookup(record.Identity)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 262144 {
		return ErrConflict
	}
	if existing.Outcome == "confirmed" || existing.Outcome == "failed" {
		saved, _ := json.Marshal(existing)
		if !bytes.Equal(saved, raw) {
			return ErrConflict
		}
		return nil
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := "receipt-" + hex.EncodeToString(suffix) + ".tmp"
	file, err := j.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer j.root.Remove(temp)
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := j.root.Rename(temp, record.Identity.OperationID+".json"); err != nil {
		return err
	}
	return j.syncDirectory()
}
func (j *Journal) syncDirectory() error {
	directory, err := j.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
