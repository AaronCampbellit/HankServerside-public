package storageops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	TargetTypePosix = "posix"

	DefaultStateDir = "/var/lib/hank/db-ops/state"
	DefaultLogDir   = "/var/log/hank/db-ops"

	DefaultFullBackupCron          = "0 2 * * 0"
	DefaultDifferentialBackupCron  = "0 2 * * 1-6"
	DefaultAMCheckCron             = "30 3 * * 0"
	DefaultRestoreVerificationCron = "0 4 * * 0"
)

type Config struct {
	Target    BackupTarget   `json:"target"`
	Schedule  ScheduleConfig `json:"schedule"`
	Restore   RestoreConfig  `json:"restore"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type BackupTarget struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
}

type ScheduleConfig struct {
	FullBackupCron              string `json:"full_backup_cron"`
	DifferentialBackupCron      string `json:"differential_backup_cron"`
	ChecksumIntervalSeconds     int    `json:"checksum_interval_seconds"`
	AMCheckCron                 string `json:"amcheck_cron"`
	RestoreVerificationCron     string `json:"restore_verification_cron"`
	RetentionFull               int    `json:"retention_full"`
	RestoreVerificationEnabled  bool   `json:"restore_verification_enabled"`
	ExternalTargetsConfigurable bool   `json:"external_targets_configurable"`
}

type RestoreConfig struct {
	ConfirmationPhrase    string `json:"confirmation_phrase"`
	PrimaryRestoreEnabled bool   `json:"primary_restore_enabled"`
}

func DefaultConfig() Config {
	return Config{
		Target: BackupTarget{
			Type: TargetTypePosix,
			Path: "/var/lib/pgbackrest",
		},
		Schedule: ScheduleConfig{
			FullBackupCron:              DefaultFullBackupCron,
			DifferentialBackupCron:      DefaultDifferentialBackupCron,
			ChecksumIntervalSeconds:     int((15 * time.Minute).Seconds()),
			AMCheckCron:                 DefaultAMCheckCron,
			RestoreVerificationCron:     DefaultRestoreVerificationCron,
			RetentionFull:               2,
			RestoreVerificationEnabled:  true,
			ExternalTargetsConfigurable: true,
		},
		Restore: RestoreConfig{
			ConfirmationPhrase:    "RESTORE HANK DATABASE",
			PrimaryRestoreEnabled: true,
		},
		UpdatedAt: time.Now().UTC(),
	}
}

func (c Config) Normalized() Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(c.Target.Type) == "" {
		c.Target.Type = defaults.Target.Type
	}
	c.Target.Type = strings.ToLower(strings.TrimSpace(c.Target.Type))
	c.Target.Path = strings.TrimSpace(c.Target.Path)
	if c.Target.Path == "" && c.Target.Type == TargetTypePosix {
		c.Target.Path = defaults.Target.Path
	}
	if strings.TrimSpace(c.Schedule.FullBackupCron) == "" {
		c.Schedule.FullBackupCron = defaults.Schedule.FullBackupCron
	}
	if strings.TrimSpace(c.Schedule.DifferentialBackupCron) == "" {
		c.Schedule.DifferentialBackupCron = defaults.Schedule.DifferentialBackupCron
	}
	if strings.TrimSpace(c.Schedule.AMCheckCron) == "" {
		c.Schedule.AMCheckCron = defaults.Schedule.AMCheckCron
	}
	if strings.TrimSpace(c.Schedule.RestoreVerificationCron) == "" {
		c.Schedule.RestoreVerificationCron = defaults.Schedule.RestoreVerificationCron
	}
	if c.Schedule.ChecksumIntervalSeconds <= 0 {
		c.Schedule.ChecksumIntervalSeconds = defaults.Schedule.ChecksumIntervalSeconds
	}
	if c.Schedule.RetentionFull <= 0 {
		c.Schedule.RetentionFull = defaults.Schedule.RetentionFull
	}
	if strings.TrimSpace(c.Restore.ConfirmationPhrase) == "" {
		c.Restore.ConfirmationPhrase = defaults.Restore.ConfirmationPhrase
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now().UTC()
	}
	return c
}

func (c Config) Validate() error {
	c = c.Normalized()
	if c.Target.Type != TargetTypePosix {
		return fmt.Errorf("unsupported backup target type %q", c.Target.Type)
	}
	if c.Target.Path == "" || !strings.HasPrefix(c.Target.Path, "/") {
		return errors.New("posix backup target path must be absolute")
	}
	if err := ValidateCronSpec(c.Schedule.FullBackupCron); err != nil {
		return fmt.Errorf("full backup schedule: %w", err)
	}
	if err := ValidateCronSpec(c.Schedule.DifferentialBackupCron); err != nil {
		return fmt.Errorf("differential backup schedule: %w", err)
	}
	if err := ValidateCronSpec(c.Schedule.AMCheckCron); err != nil {
		return fmt.Errorf("amcheck schedule: %w", err)
	}
	if err := ValidateCronSpec(c.Schedule.RestoreVerificationCron); err != nil {
		return fmt.Errorf("restore verification schedule: %w", err)
	}
	if c.Schedule.ChecksumIntervalSeconds < 60 {
		return errors.New("checksum interval must be at least 60 seconds")
	}
	if c.Schedule.RetentionFull < 1 || c.Schedule.RetentionFull > 30 {
		return errors.New("retention_full must be between 1 and 30")
	}
	if strings.TrimSpace(c.Restore.ConfirmationPhrase) == "" {
		return errors.New("restore confirmation phrase is required")
	}
	return nil
}

func ConfigPath(stateDir string) string {
	return filepath.Join(dirOrDefault(stateDir, DefaultStateDir), "storage-config.json")
}

func LoadConfig(stateDir string) (Config, error) {
	path := ConfigPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := DefaultConfig()
			return cfg, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg = cfg.Normalized()
	return cfg, cfg.Validate()
}

func SaveConfig(stateDir string, cfg Config) (Config, error) {
	cfg = cfg.Normalized()
	cfg.UpdatedAt = time.Now().UTC()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := ensurePrivateDir(dirOrDefault(stateDir, DefaultStateDir)); err != nil {
		return Config{}, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, err
	}
	path := ConfigPath(stateDir)
	tmp := path + ".tmp"
	if err := writePrivateFile(tmp, data); err != nil {
		return Config{}, err
	}
	if err := renamePrivateFile(tmp, path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ValidateCronSpec(spec string) error {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return errors.New("cron schedule must have 5 fields")
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if err := validateCronField(field, ranges[index][0], ranges[index][1]); err != nil {
			return fmt.Errorf("field %d: %w", index+1, err)
		}
	}
	return nil
}

func validateCronField(field string, min int, max int) error {
	if field == "" {
		return errors.New("empty field")
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return errors.New("empty list value")
		}
		base := part
		if strings.Contains(part, "/") {
			chunks := strings.Split(part, "/")
			if len(chunks) != 2 || chunks[0] == "" || chunks[1] == "" {
				return fmt.Errorf("invalid step %q", part)
			}
			step, err := strconv.Atoi(chunks[1])
			if err != nil || step <= 0 {
				return fmt.Errorf("invalid step %q", part)
			}
			base = chunks[0]
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			chunks := strings.Split(base, "-")
			if len(chunks) != 2 {
				return fmt.Errorf("invalid range %q", base)
			}
			start, errStart := strconv.Atoi(chunks[0])
			end, errEnd := strconv.Atoi(chunks[1])
			if errStart != nil || errEnd != nil || start > end || start < min || end > max {
				return fmt.Errorf("invalid range %q", base)
			}
			continue
		}
		value, err := strconv.Atoi(base)
		if err != nil || value < min || value > max {
			return fmt.Errorf("invalid value %q", base)
		}
	}
	return nil
}

func dirOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
