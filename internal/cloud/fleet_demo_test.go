package cloud

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestFleetShellRelay(t *testing.T) {
	for _, scenario := range []string{"success", "agent_error", "member", "disabled", "invalid", "foreign_target"} {
		t.Run(scenario, func(t *testing.T) {
			db := storeForTest(t)
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			now := time.Now().UTC()
			user := domain.User{ID: "admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
			home := domain.Home{ID: "home", UserID: user.ID, Name: "Fleet", CreatedAt: now, UpdatedAt: now}
			must(t, db.CreateUser(ctx, user))
			must(t, db.CreateHome(ctx, home))
			if scenario == "member" {
				user.ID = "member"
				user.Email = "member@example.com"
				must(t, db.CreateUser(ctx, user))
				must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
			}
			must(t, db.CreateSession(ctx, domain.AppSession{ID: "session", UserID: user.ID, TokenHash: hashToken("user-session"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
			agent := domain.Agent{ID: "linux-b", HomeID: home.ID, Name: "Linux B", AgentType: AgentTypeWorker, Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
			must(t, db.UpsertAgent(ctx, agent))
			must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agent-token", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken("device-secret"), CreatedAt: now}))
			server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
			// Keep other destinations present to detect implicit primary/cross-Home fallback.
			server.router.RegisterAgent(home.ID, domain.Agent{ID: "primary", HomeID: home.ID}, nil, []string{"shell.exec"}, AgentTypePrimary, nil)
			server.router.RegisterAgent("foreign-home", domain.Agent{ID: "another-home-agent", HomeID: "foreign-home"}, nil, []string{"shell.exec"}, AgentTypeWorker, nil)
			ts := httptest.NewServer(server.http.Handler)
			defer ts.Close()
			conn, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/agent"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer device-secret"}, "X-Hank-Agent-ID": []string{agent.ID}}})
			must(t, err)
			defer conn.CloseNow()
			caps := []string{"shell.exec"}
			if scenario == "disabled" {
				caps = nil
			}
			register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, AgentType: AgentTypeWorker, Capabilities: caps})
			must(t, err)
			must(t, wsjson.Write(ctx, conn, register))
			var registered protocol.Envelope
			must(t, wsjson.Read(ctx, conn, &registered))
			if registered.Type != protocol.TypeAgentRegistered {
				t.Fatalf("registration=%+v", registered)
			}
			if scenario == "success" {
				denied, resp, dialErr := appWebSocketDial(ctx, ts, "device-secret")
				if denied != nil {
					denied.CloseNow()
				}
				if dialErr == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
					t.Fatal("device credential authorized a user connection")
				}
				var inventory struct {
					Agents []struct {
						ID string `json:"agent_id"`
					} `json:"agents"`
				}
				requestJSON(t, ts, "user-session", http.MethodGet, "/v1/home/agents", nil, &inventory)
				if len(inventory.Agents) != 1 || inventory.Agents[0].ID != agent.ID {
					t.Fatal("inventory is not Home-scoped")
				}
			}
			app, _, err := appWebSocketDial(ctx, ts, "user-session")
			must(t, err)
			defer app.CloseNow()
			request := protocol.ShellExecRequest{Command: "printf private-demo-content", TimeoutSeconds: 2}
			if scenario == "invalid" {
				request.TimeoutSeconds = 301
			}
			body, err := json.Marshal(request)
			must(t, err)
			target := agent.ID
			if scenario == "foreign_target" {
				target = "another-home-agent"
			}
			envelope, err := protocol.NewEnvelope(protocol.TypeAppCommand, "fleet-test", target, "", protocol.RoutedCommand{Command: "shell.exec", Body: body})
			must(t, err)
			must(t, wsjson.Write(ctx, app, envelope))
			if scenario == "success" || scenario == "agent_error" {
				var received protocol.Envelope
				must(t, wsjson.Read(ctx, conn, &received))
				if received.AgentID != agent.ID || received.HomeID != home.ID {
					t.Fatal("incorrect routing")
				}
				var persisted string
				must(t, db.DB().QueryRowContext(ctx, "SELECT request_payload::text FROM relay_requests WHERE request_id = 'fleet-test'").Scan(&persisted))
				if persisted != "{}" {
					t.Fatal("shell command persisted")
				}
				// The server's normal one-second timeout must not expire this two-second command.
				time.Sleep(1100 * time.Millisecond)
				response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, received.RequestID, agent.ID, home.ID, map[string]any{"exit_code": 7, "stdout": "private-output", "stderr": "", "truncated": false})
				must(t, err)
				if scenario == "agent_error" {
					response.Error = &protocol.ErrorPayload{Code: "command_failed", Message: "private failure content"}
				}
				must(t, wsjson.Write(ctx, conn, response))
			}
			var response protocol.Envelope
			must(t, wsjson.Read(ctx, app, &response))
			if scenario == "success" {
				if response.Type != protocol.TypeAppResponse || response.AgentID != agent.ID {
					t.Fatalf("response=%+v", response)
				}
				var persisted string
				must(t, db.DB().QueryRowContext(ctx, "SELECT response_payload::text FROM relay_requests WHERE request_id = 'fleet-test'").Scan(&persisted))
				if persisted != "{}" {
					t.Fatal("shell output persisted")
				}
			} else if scenario == "agent_error" {
				if response.Error == nil || response.Error.Code != "command_failed" {
					t.Fatalf("response=%+v", response)
				}
				var persisted string
				must(t, db.DB().QueryRowContext(ctx, "SELECT error_message FROM relay_requests WHERE request_id = 'fleet-test'").Scan(&persisted))
				if persisted != "Shell request failed" {
					t.Fatal("shell failure content persisted")
				}
			} else {
				want := map[string]string{"member": "permission_denied", "disabled": "shell_disabled", "invalid": "invalid_command_payload", "foreign_target": "agent_offline"}[scenario]
				if response.Error == nil || response.Error.Code != want {
					t.Fatalf("response=%+v want=%s", response, want)
				}
				if server.router.PendingCount() != 0 {
					t.Fatal("rejected execution left pending work")
				}
			}
		})
	}
}
