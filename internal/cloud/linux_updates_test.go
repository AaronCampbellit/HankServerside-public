package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestLinuxUpdateDispatchAndAuthenticatedReconnectAcknowledgement(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_linux_ws", Email: "linux-ws@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_ws", UserID: user.ID, Name: "Linux WS", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_linux_ws", HomeID: home.ID, Name: "Linux WS", Status: domain.AgentStatusOnline, AgentType: AgentTypeWorker, Platform: "linux", Architecture: "amd64", AppVersion: "0.2.0", InstallationMode: "system", CreatedAt: now, UpdatedAt: now}
	otherAgent := domain.Agent{ID: "agent_linux_other", HomeID: home.ID, Name: "Other Linux", Status: domain.AgentStatusOnline, AgentType: AgentTypeWorker, Platform: "linux", Architecture: "amd64", AppVersion: "0.2.0", InstallationMode: "system", CreatedAt: now, UpdatedAt: now}
	rawToken := "linux-ws-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.UpsertAgent(ctx, otherAgent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_linux_ws", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(rawToken), CreatedAt: now}))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_linux_other", HomeID: home.ID, AgentID: otherAgent.ID, TokenHash: hashToken("other-token"), CreatedAt: now}))
	must(t, db.RegisterLinuxAgentRelease(ctx, domain.LinuxAgentRelease{ID: "lrel_linux_ws", Version: "0.3.0", ManifestSHA256: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), ManifestURL: "https://hankdemo.campbellservers.com/install/linux-release/release.json", State: "hosted", CreatedAt: now}))
	rollout, err := db.ActivateLinuxAgentRollout(ctx, "0.3.0", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := db.ListLinuxAgentRolloutAssignments(ctx, rollout.ID, home.ID)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("assignments = %#v, %v", assignments, err)
	}
	var assignment, otherAssignment domain.LinuxAgentUpdateAssignment
	for _, candidate := range assignments {
		if candidate.AgentID == agent.ID {
			assignment = candidate
		} else if candidate.AgentID == otherAgent.ID {
			otherAssignment = candidate
		}
	}

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()
	connect := func(version, pending string) *websocket.Conn {
		connection, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + rawToken}, "X-Hank-Agent-ID": []string{agent.ID}}})
		if err != nil {
			t.Fatal(err)
		}
		register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, AgentType: AgentTypeWorker, Capabilities: []string{protocol.CommandSystemUpdateApply}, UpdateAssignmentID: pending, Metadata: map[string]string{"platform": "linux", "architecture": "amd64", "app_version": version, "installation_mode": "system"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := wsjson.Write(ctx, connection, register); err != nil {
			t.Fatal(err)
		}
		return connection
	}

	first := connect("0.2.0", "")
	defer first.Close(websocket.StatusNormalClosure, "done")
	var registered protocol.Envelope
	if err := wsjson.Read(ctx, first, &registered); err != nil || registered.Type != protocol.TypeAgentRegistered {
		t.Fatalf("registration = %#v, %v", registered, err)
	}
	forgedState := protocol.SystemUpdateState{RolloutID: rollout.ID, AssignmentID: otherAssignment.ID, AgentID: otherAgent.ID, State: protocol.UpdateStateDownloading, OccurredAt: time.Now().UTC()}
	forgedBody, err := json.Marshal(forgedState)
	if err != nil {
		t.Fatal(err)
	}
	forgedEvent, err := protocol.NewEnvelope(protocol.TypeAgentEvent, "", otherAgent.ID, home.ID, protocol.AgentEvent{Event: protocol.EventSystemUpdateState, Topic: protocol.TopicAgentUpdates, Body: forgedBody})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, first, forgedEvent); err != nil {
		t.Fatal(err)
	}
	var commandEnvelope protocol.Envelope
	if err := wsjson.Read(ctx, first, &commandEnvelope); err != nil {
		t.Fatal(err)
	}
	command, err := protocol.DecodePayload[protocol.RoutedCommand](commandEnvelope)
	if err != nil || command.Command != protocol.CommandSystemUpdateApply {
		t.Fatalf("command = %#v, %v", command, err)
	}
	var update protocol.SystemUpdateAssignment
	err = json.Unmarshal(command.Body, &update)
	if err != nil || update.AssignmentID != assignment.ID || update.Version != "0.3.0" {
		t.Fatalf("update = %#v, %v", update, err)
	}
	accepted, err := protocol.NewEnvelope(protocol.TypeCloudResponse, commandEnvelope.RequestID, agent.ID, home.ID, protocol.SystemUpdateAccepted{AssignmentID: assignment.ID, Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, first, accepted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := db.ListLinuxAgentRolloutAssignments(ctx, rollout.ID, home.ID)
		states := map[string]string{}
		for _, value := range current {
			states[value.AgentID] = value.State
		}
		if err == nil && states[agent.ID] == protocol.UpdateStateInstalling && states[otherAgent.ID] == "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("assignment did not enter installing: %#v, %v", current, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = first.Close(websocket.StatusNormalClosure, "restarting")

	second := connect("0.3.0", assignment.ID)
	defer second.Close(websocket.StatusNormalClosure, "done")
	if err := wsjson.Read(ctx, second, &registered); err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.DecodePayload[protocol.AgentRegistered](registered)
	if err != nil || !ack.UpdateAcknowledged || ack.UpdateAssignmentID != assignment.ID {
		t.Fatalf("acknowledgement = %#v, %v", ack, err)
	}
}
