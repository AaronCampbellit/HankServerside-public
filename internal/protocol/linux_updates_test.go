package protocol

import (
	"testing"
	"time"
)

func TestSystemUpdateContractsValidate(t *testing.T) {
	value := SystemUpdateAssignment{RolloutID: "lroll_12345678", AssignmentID: "luasg_12345678", Version: "0.3.0", ManifestURL: "https://hank.example/install/linux-release/release.json", NotBefore: time.Now().UTC(), HealthDeadlineSeconds: 300}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SystemUpdateAssignment){"id": func(v *SystemUpdateAssignment) { v.AssignmentID = "bad" }, "version": func(v *SystemUpdateAssignment) { v.Version = "0.3" }, "http": func(v *SystemUpdateAssignment) { v.ManifestURL = "http://hank.example/release.json" }, "deadline": func(v *SystemUpdateAssignment) { v.HealthDeadlineSeconds = 30 }} {
		t.Run(name, func(t *testing.T) {
			invalid := value
			mutate(&invalid)
			if invalid.Validate() == nil {
				t.Fatal("invalid assignment accepted")
			}
		})
	}
	event := SystemUpdateState{RolloutID: value.RolloutID, AssignmentID: value.AssignmentID, AgentID: "agent_1", State: UpdateStateDownloading, OccurredAt: time.Now().UTC()}
	if err := event.Validate("agent_1"); err != nil {
		t.Fatal(err)
	}
	event.State = "mystery"
	if event.Validate("agent_1") == nil {
		t.Fatal("unknown update state accepted")
	}
	event.State = UpdateStateHealthy
	if event.Validate("agent_2") == nil {
		t.Fatal("event identity mismatch accepted")
	}
}
