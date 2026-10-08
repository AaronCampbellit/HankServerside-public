package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestTerminalManagerStreamsInputAndReplaysAfterCursor(t *testing.T) {
	events := make(chan protocol.ShellSessionOutput, 16)
	manager := newTerminalManager(true, func(_ context.Context, event, _ string, payload any) error {
		if event == "shell.session.output" {
			events <- payload.(protocol.ShellSessionOutput)
		}
		return nil
	})
	t.Cleanup(manager.closeAll)

	opened, err := manager.open(context.Background(), protocol.ShellSessionOpenRequest{SessionID: "term_test_0001", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if opened.SessionID != "term_test_0001" {
		t.Fatalf("open response = %#v", opened)
	}
	if err := manager.input(protocol.ShellSessionInputRequest{SessionID: opened.SessionID, Data: "printf 'hank-live-marker\\n'\n"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case output := <-events:
			if strings.Contains(output.Data, "hank-live-marker") {
				attached, err := manager.attach(protocol.ShellSessionAttachRequest{SessionID: opened.SessionID, AfterCursor: 0})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(attached.Output, "hank-live-marker") || attached.Cursor == 0 {
					t.Fatalf("attach response = %#v", attached)
				}
				return
			}
		case <-deadline:
			t.Fatal("terminal output did not arrive")
		}
	}
}

func TestTerminalManagerRejectsOpenWhenDisabled(t *testing.T) {
	manager := newTerminalManager(false, nil)
	_, err := manager.open(context.Background(), protocol.ShellSessionOpenRequest{SessionID: "term_test_0002", Columns: 80, Rows: 24})
	if err == nil {
		t.Fatal("disabled terminal opened")
	}
}

func TestTerminalManagerDisableClosesConcurrentSessions(t *testing.T) {
	manager := newTerminalManager(true, nil)
	for _, id := range []string{"term_test_0003", "term_test_0004"} {
		if _, err := manager.open(context.Background(), protocol.ShellSessionOpenRequest{SessionID: id, Columns: 80, Rows: 24}); err != nil {
			t.Fatal(err)
		}
	}
	manager.setEnabled(false)
	for _, id := range []string{"term_test_0003", "term_test_0004"} {
		if _, err := manager.attach(protocol.ShellSessionAttachRequest{SessionID: id}); err == nil {
			t.Fatalf("session %s survived shell disable", id)
		}
	}
}
