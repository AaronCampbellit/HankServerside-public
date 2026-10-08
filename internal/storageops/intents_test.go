package storageops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSignedIntentRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	secret := "test-secret"

	intent, err := CreateIntent(stateDir, secret, Intent{
		Type:        IntentTypePrimaryRestore,
		HomeID:      "home_1",
		RequestedBy: "usr_1",
		BackupLabel: "20260430-010101F",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyIntent(secret, intent) {
		t.Fatal("expected intent to verify")
	}
	intent.BackupLabel = "tampered"
	if VerifyIntent(secret, intent) {
		t.Fatal("expected tampered intent to fail verification")
	}

	intents, err := ListIntents(stateDir, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].Type != IntentTypePrimaryRestore {
		t.Fatalf("intents = %+v", intents)
	}
}

func TestCompleteIntentArchivesProcessedIntent(t *testing.T) {
	stateDir := t.TempDir()
	intent, err := CreateIntent(stateDir, "test-secret", Intent{
		Type:        IntentTypeBackup,
		HomeID:      "home_1",
		RequestedBy: "usr_1",
		BackupType:  "diff",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := CompleteIntent(stateDir, intent.ID); err != nil {
		t.Fatalf("complete intent: %v", err)
	}
	intents, err := ListIntents(stateDir, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("intents after completion = %+v", intents)
	}
	archived, err := filepath.Glob(filepath.Join(stateDir, "intents-done", intent.ID+"-*.json"))
	if err != nil || len(archived) != 1 {
		t.Fatalf("archived intents = %v err = %v", archived, err)
	}
}

func TestCompleteIntentRemovesIntentWhenArchiveDirUnavailable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("directory write permissions are not enforced for root")
	}
	stateDir := t.TempDir()
	intent, err := CreateIntent(stateDir, "test-secret", Intent{
		Type:        IntentTypeBackup,
		HomeID:      "home_1",
		RequestedBy: "usr_1",
		BackupType:  "diff",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a state root the worker cannot create subdirectories in
	// (write bit removed) while the intents dir itself stays writable.
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	if err := CompleteIntent(stateDir, intent.ID); err != nil {
		t.Fatalf("complete intent with unavailable archive dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(IntentDir(stateDir), intent.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("intent file should be removed; stat err = %v", err)
	}
}
