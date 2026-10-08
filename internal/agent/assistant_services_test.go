package agent

import (
	"context"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"strings"
	"testing"
)

func TestAssistantServiceAllowlistAndRestartReadback(t *testing.T) {
	for _, unit := range []string{"../escape.service", "--system.service", "name;touch.service", "name.service\n"} {
		if _, err := newAssistantServiceManager([]protocol.AssistantServiceGrant{{Unit: unit, Operations: []string{"restart"}}}); err == nil {
			t.Fatalf("invalid unit accepted: %q", unit)
		}
	}
	if _, err := newAssistantServiceManager([]protocol.AssistantServiceGrant{{Unit: "example.service", Operations: []string{"reboot"}}}); err == nil {
		t.Fatal("unreviewed operation allowed")
	}
	for _, changed := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "accepted"}[changed], func(t *testing.T) {
			journal, err := operations.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			client := NewClient("", "agent", "", "", "", nil, nil, nil, nil, nil)
			client.registeredHomeID = "home"
			client.SetOperationJournal(journal)
			if err := client.SetAssistantServiceOperations(`[{"unit":"example.service","operations":["restart"]}]`); err != nil {
				t.Fatal(err)
			}
			writes := 0
			client.assistantServices.run = func(_ context.Context, args ...string) (string, error) {
				if args[len(args)-1] != "example.service" || args[len(args)-2] != "--" {
					t.Fatal("unit not exact argv")
				}
				if args[2] == "restart" {
					writes++
					return "", nil
				}
				if args[2] != "show" {
					t.Fatal("unexpected operation")
				}
				invocation := "before"
				if changed && writes > 0 {
					invocation = "after"
				}
				return "Id=example.service\nLoadState=loaded\nActiveState=active\nSubState=running\nInvocationID=" + invocation + "\n", nil
			}
			prior, err := client.assistantServices.inspect(context.Background(), "example.service")
			if err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(protocol.AssistantServiceOperationArguments{Unit: "example.service", Operation: "restart", PriorState: prior})
			identity := protocol.AssistantOperationIdentity{JournalEpoch: journal.Epoch(), OperationID: "service-restart", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "admin", AgentID: "agent", Tool: "machines.service_action", ToolVersion: 1}
			body, _ := json.Marshal(protocol.AssistantOperationRequest{Identity: identity, Arguments: args})
			invoke := func() (protocol.AssistantOperationStatusResponse, error) {
				return client.executeAssistantOperation(context.Background(), protocol.Envelope{HomeID: "home", AgentID: "agent"}, protocol.RoutedCommand{Command: protocol.CommandAssistantOperationExecute, Body: body})
			}
			result, err := invoke()
			if err != nil {
				t.Fatal(err)
			}
			expected := "accepted"
			if changed {
				expected = "confirmed"
			}
			if result.Outcome != expected || writes != 1 {
				t.Fatal("restart not verified against invocation identity")
			}
			if _, err := invoke(); err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatal("restart replayed")
			}
			denied, _ := json.Marshal(protocol.AssistantServiceOperationArguments{Unit: "example.service", Operation: "stop", PriorState: prior})
			body, _ = json.Marshal(protocol.AssistantOperationRequest{Identity: identity, Arguments: denied})
			if _, err := invoke(); err == nil {
				t.Fatal("ungranted stop accepted")
			}
		})
	}
}
