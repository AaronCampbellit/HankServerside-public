package storageops

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	IntentTypeBackup         = "backup"
	IntentTypeRestoreTest    = "restore_test"
	IntentTypePrimaryRestore = "primary_restore"
)

type Intent struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	HomeID       string    `json:"home_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
	BackupType   string    `json:"backup_type,omitempty"`
	BackupLabel  string    `json:"backup_label,omitempty"`
	Confirmation string    `json:"confirmation,omitempty"`
	Signature    string    `json:"signature"`
}

func IntentDir(stateDir string) string {
	return filepath.Join(dirOrDefault(stateDir, DefaultStateDir), "intents")
}

func CreateIntent(stateDir string, secret string, intent Intent) (Intent, error) {
	if strings.TrimSpace(secret) == "" {
		return Intent{}, errors.New("intent secret is required")
	}
	intent.ID = strings.TrimSpace(intent.ID)
	if intent.ID == "" {
		intent.ID = newEventID()
	}
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = time.Now().UTC()
	}
	intent.Signature = signIntent(secret, intent)
	if err := ensurePrivateDir(IntentDir(stateDir)); err != nil {
		return Intent{}, err
	}
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return Intent{}, err
	}
	path := filepath.Join(IntentDir(stateDir), intent.ID+".json")
	tmp := path + ".tmp"
	if err := writePrivateFile(tmp, data); err != nil {
		return Intent{}, err
	}
	if err := renamePrivateFile(tmp, path); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func ListIntents(stateDir string, secret string) ([]Intent, error) {
	entries, err := os.ReadDir(IntentDir(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var intents []Intent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(IntentDir(stateDir), entry.Name()))
		if err != nil {
			return nil, err
		}
		var intent Intent
		if err := json.Unmarshal(data, &intent); err != nil {
			return nil, err
		}
		if !VerifyIntent(secret, intent) {
			return nil, fmt.Errorf("intent %s has an invalid signature", intent.ID)
		}
		intents = append(intents, intent)
	}
	sort.SliceStable(intents, func(i, j int) bool {
		return intents[i].CreatedAt.Before(intents[j].CreatedAt)
	})
	return intents, nil
}

// CompleteIntent consumes a processed intent file. Archiving into intents-done
// is best-effort: if the archive dir cannot be created or written (for example
// a state volume whose root ownership prevents the worker from creating
// subdirectories), the intent file is deleted instead. A processed intent must
// never survive completion, or the worker re-executes it on every tick.
func CompleteIntent(stateDir string, intentID string) error {
	intentID = strings.TrimSpace(intentID)
	if intentID == "" {
		return nil
	}
	path := filepath.Join(IntentDir(stateDir), intentID+".json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	doneDir := filepath.Join(dirOrDefault(stateDir, DefaultStateDir), "intents-done")
	archiveErr := ensurePrivateDir(doneDir)
	if archiveErr == nil {
		archiveErr = os.Rename(path, filepath.Join(doneDir, intentID+"-"+time.Now().UTC().Format("20060102T150405Z")+".json"))
		if archiveErr == nil {
			return nil
		}
	}
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(archiveErr, removeErr)
	}
	return nil
}

func VerifyIntent(secret string, intent Intent) bool {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(intent.Signature) == "" {
		return false
	}
	want := signIntent(secret, intent)
	return hmac.Equal([]byte(want), []byte(intent.Signature))
}

func signIntent(secret string, intent Intent) string {
	intent.Signature = ""
	data, _ := json.Marshal(struct {
		ID           string    `json:"id"`
		Type         string    `json:"type"`
		HomeID       string    `json:"home_id"`
		RequestedBy  string    `json:"requested_by"`
		CreatedAt    time.Time `json:"created_at"`
		BackupType   string    `json:"backup_type,omitempty"`
		BackupLabel  string    `json:"backup_label,omitempty"`
		Confirmation string    `json:"confirmation,omitempty"`
	}{
		ID:           intent.ID,
		Type:         intent.Type,
		HomeID:       intent.HomeID,
		RequestedBy:  intent.RequestedBy,
		CreatedAt:    intent.CreatedAt.UTC(),
		BackupType:   intent.BackupType,
		BackupLabel:  intent.BackupLabel,
		Confirmation: intent.Confirmation,
	})
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
