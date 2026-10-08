package agent

import (
	"context"
	"encoding/json"
	"testing"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDispatcherConfigSMBTest(t *testing.T) {
	t.Parallel()

	manager := newConfigManager("", agentha.New("", "", 0), agentfiles.New(""))
	manager.testSMB = func(_ context.Context, cfg agentfiles.SMBConfig) error {
		if cfg.ID != "archive" || cfg.Share != "archive" {
			t.Fatalf("config = %#v", cfg)
		}
		return nil
	}
	body, err := json.Marshal(protocol.ConfigSMBTestRequest{ID: "archive", Host: "nas.local", Share: "archive"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	result, commandErr := (&commandDispatcher{config: manager}).dispatch(context.Background(), protocol.RoutedCommand{Command: "config.smb_test", Body: body})
	if commandErr != nil {
		t.Fatalf("dispatch error = %#v", commandErr)
	}
	response, ok := result.(protocol.ConfigSMBTestResponse)
	if !ok || !response.OK {
		t.Fatalf("result = %#v, want successful SMB test", result)
	}
}
