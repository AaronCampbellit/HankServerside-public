package cloud

import (
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestLegacyLinuxUpdateCommandInstallsGuardAndCarriesAuthenticatedAssignment(t *testing.T) {
	assignment := domain.LinuxAgentUpdateAssignment{ID: "luasg_12345678", RolloutID: "lroll_12345678", ToVersion: "0.3.0"}
	command, err := legacyLinuxUpdateCommand(assignment, "https://hankdemo.campbellservers.com/install/linux-release/release.json", "0.2.0", time.Unix(2_000_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"update-assignment.json", "update-guard.state", "systemctl start --no-block hankagent-update-guard.service", "/usr/bin/hankagent --system update apply", "base64 -d"} {
		if !strings.Contains(command, required) {
			t.Fatalf("legacy bridge omits %q", required)
		}
	}
	if strings.Contains(command, "Bearer ") || strings.Contains(command, "agent-token") {
		t.Fatal("legacy bridge contains credential material")
	}
}
