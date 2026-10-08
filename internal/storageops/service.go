package storageops

import (
	"errors"
	"strings"
)

var ErrInvalidRequest = errors.New("invalid storage request")

type Service struct {
	StateDir     string
	LogDir       string
	IntentSecret string
}

func NewService(stateDir string, logDir string, intentSecret string) *Service {
	return &Service{
		StateDir:     dirOrDefault(stateDir, DefaultStateDir),
		LogDir:       dirOrDefault(logDir, DefaultLogDir),
		IntentSecret: strings.TrimSpace(intentSecret),
	}
}

func (s *Service) Status() (StatusSnapshot, error) {
	return LoadStatus(s.StateDir, s.LogDir, s.IntentSecret)
}

func (s *Service) Events(filter EventFilter) ([]Event, error) {
	return ListEvents(s.LogDir, filter)
}

func (s *Service) ClearEvents() error {
	return ClearEventLog(s.LogDir)
}

func (s *Service) Config() (Config, error) {
	return LoadConfig(s.StateDir)
}

func (s *Service) SaveConfig(cfg Config) (Config, error) {
	return SaveConfig(s.StateDir, cfg)
}

func (s *Service) RequestBackup(homeID string, requestedBy string, backupType string) (Intent, error) {
	backupType = strings.TrimSpace(strings.ToLower(backupType))
	if backupType == "" {
		backupType = "diff"
	}
	if backupType != "full" && backupType != "diff" {
		return Intent{}, errors.Join(ErrInvalidRequest, errors.New("backup_type must be full or diff"))
	}
	return s.createIntent(Intent{
		Type:        IntentTypeBackup,
		HomeID:      homeID,
		RequestedBy: requestedBy,
		BackupType:  backupType,
	})
}

func (s *Service) RequestRestoreTest(homeID string, requestedBy string, backupLabel string) (Intent, error) {
	return s.createIntent(Intent{
		Type:        IntentTypeRestoreTest,
		HomeID:      homeID,
		RequestedBy: requestedBy,
		BackupLabel: strings.TrimSpace(backupLabel),
	})
}

func (s *Service) RequestPrimaryRestore(homeID string, requestedBy string, backupLabel string, confirmation string) (Intent, error) {
	cfg, err := s.Config()
	if err != nil {
		return Intent{}, err
	}
	if !cfg.Restore.PrimaryRestoreEnabled {
		return Intent{}, errors.New("primary restore is disabled")
	}
	if !ValidBackupLabel(strings.TrimSpace(backupLabel)) {
		return Intent{}, errors.Join(ErrInvalidRequest, errors.New("a valid backup_label is required"))
	}
	if strings.TrimSpace(confirmation) != strings.TrimSpace(cfg.Restore.ConfirmationPhrase) {
		return Intent{}, errors.Join(ErrInvalidRequest, errors.New("restore confirmation phrase did not match"))
	}
	return s.createIntent(Intent{
		Type:        IntentTypePrimaryRestore,
		HomeID:      homeID,
		RequestedBy: requestedBy,
		BackupLabel: strings.TrimSpace(backupLabel),
	})
}

func (s *Service) createIntent(intent Intent) (Intent, error) {
	if strings.TrimSpace(s.IntentSecret) == "" {
		return Intent{}, errors.New("db-ops intent secret is not configured")
	}
	return CreateIntent(s.StateDir, s.IntentSecret, intent)
}
