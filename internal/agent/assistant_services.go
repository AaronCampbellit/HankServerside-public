package agent

import (
	"bytes"
	"context"
	"errors"
	"github.com/dropfile/HankServerside/internal/protocol"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var assistantServiceUnitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}\.service$`)
var errAssistantServiceUnavailable = errors.New("allowlisted service unavailable")

type assistantServiceManager struct {
	grants map[string]map[string]bool
	run    func(context.Context, ...string) (string, error)
}

// Configuration is explicit per unit and operation, and never grants shell.
func newAssistantServiceManager(grants []protocol.AssistantServiceGrant) (*assistantServiceManager, error) {
	if len(grants) > 32 {
		return nil, errAssistantServiceUnavailable
	}
	manager := &assistantServiceManager{grants: map[string]map[string]bool{}, run: runAssistantSystemctl}
	for _, grant := range grants {
		if !assistantServiceUnitPattern.MatchString(grant.Unit) || len(grant.Operations) == 0 || len(grant.Operations) > 3 || manager.grants[grant.Unit] != nil {
			return nil, errAssistantServiceUnavailable
		}
		allowed := map[string]bool{}
		for _, operation := range grant.Operations {
			if operation != "start" && operation != "stop" && operation != "restart" {
				return nil, errAssistantServiceUnavailable
			}
			if allowed[operation] {
				return nil, errAssistantServiceUnavailable
			}
			allowed[operation] = true
		}
		manager.grants[grant.Unit] = allowed
	}
	return manager, nil
}

type boundedServiceOutput struct{ bytes.Buffer }

func (b *boundedServiceOutput) Write(p []byte) (int, error) {
	if len(p) > 16384-b.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

func runAssistantSystemctl(ctx context.Context, args ...string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errAssistantServiceUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, "/usr/bin/systemctl", args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0"}
	var output boundedServiceOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", errAssistantServiceUnavailable
	}
	return output.String(), nil
}

func (m *assistantServiceManager) available() bool {
	if m == nil || len(m.grants) == 0 || runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat("/usr/bin/systemctl")
	return err == nil
}

func (m *assistantServiceManager) inspect(ctx context.Context, unit string) (protocol.AssistantServiceState, error) {
	if m == nil || m.grants[unit] == nil {
		return protocol.AssistantServiceState{}, errAssistantServiceUnavailable
	}
	raw, err := m.run(ctx, "--system", "--no-pager", "show", "--property=Id,LoadState,ActiveState,SubState,InvocationID", "--", unit)
	if err != nil {
		return protocol.AssistantServiceState{}, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[key] = value
		}
	}
	if fields["Id"] != unit || fields["LoadState"] != "loaded" {
		return protocol.AssistantServiceState{}, errAssistantServiceUnavailable
	}
	state := protocol.AssistantServiceState{Unit: unit, ActiveState: fields["ActiveState"], SubState: fields["SubState"], InvocationID: fields["InvocationID"], AllowedOperations: []string{}}
	for _, operation := range []string{"start", "stop", "restart"} {
		if m.grants[unit][operation] {
			state.AllowedOperations = append(state.AllowedOperations, operation)
		}
	}
	return state, nil
}

func (c *Client) SetAssistantServiceOperations(raw string) error {
	var grants []protocol.AssistantServiceGrant
	if strings.TrimSpace(raw) != "" {
		if strictOperationJSON([]byte(raw), &grants) != nil {
			return errAssistantServiceUnavailable
		}
	}
	manager, err := newAssistantServiceManager(grants)
	if err != nil {
		return err
	}
	c.assistantServices = manager
	return nil
}
