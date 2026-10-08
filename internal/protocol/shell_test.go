package protocol

import (
	"encoding/json"
	"testing"
)

func TestShellSessionPayloadsUseStableWireNames(t *testing.T) {
	body, err := json.Marshal(ShellSessionOpenRequest{SessionID: "term_0001", Columns: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"session_id":"term_0001","columns":120,"rows":40}`; got != want {
		t.Fatalf("open payload = %s, want %s", got, want)
	}
	if CommandShellSessionOpen != "shell.session.open" || ShellSessionTopic("term_0001") != "shell.session:term_0001" {
		t.Fatal("shell session command/topic constants changed")
	}
}

func TestValidateShellSessionRequests(t *testing.T) {
	if err := (ShellSessionOpenRequest{SessionID: "term_0001", Columns: 80, Rows: 24}).Validate(); err != nil {
		t.Fatalf("valid open rejected: %v", err)
	}
	for _, request := range []ShellSessionOpenRequest{
		{SessionID: "", Columns: 80, Rows: 24},
		{SessionID: "bad space", Columns: 80, Rows: 24},
		{SessionID: "term_0001", Columns: 0, Rows: 24},
		{SessionID: "term_0001", Columns: 80, Rows: 1001},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid open accepted: %#v", request)
		}
	}
	if err := (ShellSessionInputRequest{SessionID: "term_0001", Data: string(make([]byte, MaxShellInputBytes+1))}).Validate(); err == nil {
		t.Fatal("oversized input accepted")
	}
}
