package operations

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestJournalRestartNeverReplaysUncertainEffect(t *testing.T) {
	directory := t.TempDir()
	identity := protocol.AssistantOperationIdentity{OperationID: "operation-1", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "files.create_folder", ToolVersion: 1}
	journal, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	_, fresh, err := journal.Begin(identity)
	if err != nil || !fresh {
		t.Fatal("first attempt unavailable")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	receipt, fresh, err := journal.Begin(identity)
	if err != nil || fresh || receipt.Outcome != "unknown" {
		t.Fatal("restart replayed uncertain operation")
	}
	changed := identity
	changed.ActionDigest = strings.Repeat("b", 64)
	if _, _, err := journal.Begin(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("operation key accepted changed action")
	}
	receipt.Outcome = "confirmed"
	receipt.Result = json.RawMessage(`{"verified":true}`)
	if err := journal.Complete(receipt); err != nil {
		t.Fatal(err)
	}
	result, err := journal.Lookup(identity)
	if err != nil || result.Outcome != "confirmed" {
		t.Fatal("verified receipt missing")
	}
	receipt.Outcome = "failed"
	if err := journal.Complete(receipt); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal receipt overwritten")
	}
	identity.OperationID = "../escape"
	if _, _, err := journal.Begin(identity); !errors.Is(err, ErrConflict) {
		t.Fatal("operation path escaped")
	}
}

func TestJournalRequiresExclusiveProcessOwnership(t *testing.T) {
	directory := t.TempDir()
	journal, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	competing, err := Open(directory)
	if err == nil {
		competing.Close()
		t.Fatal("competing journal acquired destination state")
	}
}

func TestJournalProcessCrashAfterEffectDoesNotReplay(t *testing.T) {
	identity := protocol.AssistantOperationIdentity{OperationID: "crash-operation", ActionDigest: strings.Repeat("c", 64), HomeID: "home", UserID: "owner", AgentID: "agent", Tool: "files.create_folder", ToolVersion: 1}
	if directory := os.Getenv("HANK_JOURNAL_CRASH_TEST_DIR"); directory != "" {
		journal, err := Open(filepath.Join(directory, "receipts"))
		if err != nil {
			os.Exit(80)
		}
		_, fresh, err := journal.Begin(identity)
		if err != nil || !fresh {
			os.Exit(81)
		}
		effect, err := os.OpenFile(filepath.Join(directory, "effect"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(82)
		}
		if _, err = effect.WriteString("one effect"); err != nil {
			os.Exit(83)
		}
		if err = effect.Sync(); err != nil {
			os.Exit(84)
		}
		// Exit immediately after the effect, without receipt completion or Close.
		// This simulates process loss before an acknowledgement reaches the server.
		os.Exit(79)
	}
	directory := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestJournalProcessCrashAfterEffectDoesNotReplay$")
	child.Env = append(os.Environ(), "HANK_JOURNAL_CRASH_TEST_DIR="+directory)
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 79 {
		t.Fatalf("crash helper failed: %v", err)
	}
	journal, err := Open(filepath.Join(directory, "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	receipt, fresh, err := journal.Begin(identity)
	if err != nil || fresh || receipt.Outcome != "unknown" {
		t.Fatal("crashed effect was authorized for replay")
	}
	content, err := os.ReadFile(filepath.Join(directory, "effect"))
	if err != nil || string(content) != "one effect" {
		t.Fatal("crash lost fixture effect")
	}
}

func TestJournalEpochSurvivesRestartAndRejectsReplacement(t *testing.T) {
	directory := t.TempDir()
	journal, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	epoch := journal.Epoch()
	identity := protocol.AssistantOperationIdentity{JournalEpoch: epoch, OperationID: "epoch-operation", ActionDigest: strings.Repeat("d", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "files.create_folder", ToolVersion: 1}
	if _, fresh, err := journal.Begin(identity); err != nil || !fresh {
		t.Fatal("cannot begin operation")
	}
	journal.Close()
	journal, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Epoch() != epoch {
		t.Fatal("epoch changed on restart")
	}
	journal.Close()
	replacement, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, _, err := replacement.Begin(identity); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement store authorized old operation")
	}
	if _, err := replacement.Lookup(identity); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement store reported old operation absent")
	}
	if err := os.Remove(filepath.Join(directory, ".epoch")); err != nil {
		t.Fatal(err)
	}
	if damaged, err := Open(directory); err == nil {
		damaged.Close()
		t.Fatal("lost epoch accepted with existing receipts")
	}
}
