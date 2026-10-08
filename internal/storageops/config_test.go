package storageops

import (
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validate: %v", err)
	}

	cfg.Target.Path = "relative"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected relative target path to fail")
	}

	cfg = DefaultConfig()
	cfg.Schedule.ChecksumIntervalSeconds = 30
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected short checksum interval to fail")
	}

	cfg = DefaultConfig()
	cfg.Schedule.FullBackupCron = "bad"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected bad cron to fail")
	}

	cfg = DefaultConfig()
	cfg.Schedule.RestoreVerificationCron = "bad"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected bad restore verification cron to fail")
	}
}

func TestCronMatches(t *testing.T) {
	at := time.Date(2026, 4, 26, 2, 0, 0, 0, time.UTC)
	if !CronMatches("0 2 * * 0", at) {
		t.Fatal("expected Sunday full backup cron to match")
	}
	if CronMatches("0 2 * * 1-6", at) {
		t.Fatal("did not expect weekday diff cron to match Sunday")
	}
	if !CronMatches("*/15 * * * *", time.Date(2026, 4, 26, 2, 30, 0, 0, time.UTC)) {
		t.Fatal("expected step cron to match")
	}
}
