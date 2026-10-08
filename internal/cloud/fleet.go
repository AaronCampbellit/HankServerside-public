package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("app_ticket") != "" {
		http.Error(w, "URL credentials are not accepted", 401)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/fleet/"), "/"), "/")
	if parts[0] == "grants" {
		s.handleFleetGrants(w, r, parts)
		return
	}
	raw, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "fleet grant required", 401)
		return
	}
	grant, err := s.store.FleetGrantByHash(r.Context(), hashToken(raw))
	if err != nil {
		http.Error(w, "fleet grant unavailable", 401)
		return
	}
	s.handleFleetRuntime(w, r, grant, parts)
}

// Shared policy and dispatch for scoped bearer and hosted MCP transports.
func (s *Server) handleFleetRuntime(w http.ResponseWriter, r *http.Request, grant store.FleetGrant, parts []string) {

	user, err := s.store.GetUserByID(r.Context(), grant.UserID)
	if err != nil || user.PasswordChangeRequired {
		http.Error(w, "fleet account unavailable", 403)
		return
	}
	if len(parts) == 1 && parts[0] == "access" && r.Method == "GET" {
		writeJSON(w, 200, grant)
		return
	}
	if len(parts) == 1 && parts[0] == "agents" && r.Method == "GET" {
		entries := []AgentSnapshot{}
		for _, a := range s.router.AgentsForHome(grant.HomeID) {
			if slices.Contains(grant.Agents, a.AgentID) {
				entries = append(entries, a)
			}
		}
		writeJSON(w, 200, map[string]any{"agents": entries})
		return
	}
	if len(parts) == 1 && parts[0] == "jobs" && r.Method == "GET" {
		if !slices.Contains(grant.Operations, "job.read") {
			http.Error(w, "operation not granted", 403)
			return
		}
		jobs, err := s.store.ListFleetJobs(r.Context(), grant.ID)
		if err != nil {
			http.Error(w, "job list unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"jobs": jobs})
		return
	}
	var body struct {
		AgentID string `json:"agent_id"`
		protocol.FleetRequest
	}
	var command, op string
	switch {
	case len(parts) == 1 && parts[0] == "workspaces" && r.Method == "POST":
		command = "fleet.workspace.create"
		op = "workspace.write"
	case len(parts) == 3 && parts[0] == "workspaces" && parts[2] == "file" && r.Method == "POST":
		command = "fleet.workspace.write"
		op = "workspace.write"
	case len(parts) == 3 && parts[0] == "workspaces" && parts[2] == "file" && r.Method == "GET":
		command = "fleet.workspace.read"
		op = "workspace.read"
	case len(parts) == 1 && parts[0] == "jobs" && r.Method == "POST":
		command = "fleet.job.start"
		op = "job.run"
	case len(parts) == 2 && parts[0] == "jobs" && r.Method == "GET":
		command = "fleet.job.read"
		op = "job.read"
	case len(parts) == 3 && parts[0] == "jobs" && parts[2] == "cancel" && r.Method == "POST":
		command = "fleet.job.cancel"
		op = "job.cancel"
	default:
		http.NotFound(w, r)
		return
	}
	if !slices.Contains(grant.Operations, op) {
		http.Error(w, "operation not granted", 403)
		return
	}
	if r.Method == "POST" {
		if parseJSON(w, r, &body) != nil {
			http.Error(w, "invalid fleet request", 400)
			return
		}
	} else {
		body.AgentID = r.URL.Query().Get("agent_id")
		body.Path = r.URL.Query().Get("path")
		after := r.URL.Query().Get("after")
		if after != "" {
			body.After, err = strconv.ParseUint(after, 10, 64)
			if err != nil {
				http.Error(w, "invalid cursor", 400)
				return
			}
		}
	}
	body.GrantID = grant.ID // Never trust caller-supplied ownership.
	if parts[0] == "workspaces" && len(parts) > 1 {
		body.WorkspaceID = parts[1]
	}
	if parts[0] == "jobs" && len(parts) > 1 {
		body.JobID = parts[1]
		job, err := s.store.GetFleetJob(r.Context(), body.JobID, grant.ID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body.AgentID = job.AgentID
		body.WorkspaceID = job.WorkspaceID
	}
	if !slices.Contains(grant.Agents, body.AgentID) {
		http.NotFound(w, r)
		return
	}
	agent, err := s.store.GetAgentByID(r.Context(), body.AgentID)
	if err != nil || agent.HomeID != grant.HomeID {
		http.NotFound(w, r)
		return
	}
	if command == "fleet.workspace.create" && body.WorkspaceID == "" {
		body.WorkspaceID = newID("fws")
	}
	if err := body.FleetRequest.Validate(command); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if command != "fleet.workspace.create" && strings.HasPrefix(command, "fleet.workspace.") || command == "fleet.job.start" {
		if !s.store.OwnsFleetWorkspace(r.Context(), body.WorkspaceID, grant.ID, body.AgentID) {
			http.NotFound(w, r)
			return
		}
	}
	if command == "fleet.job.start" {
		fingerprint, _ := json.Marshal(body.FleetRequest)
		created, err := s.store.CreateFleetJob(r.Context(), body.JobID, grant.ID, body.AgentID, body.WorkspaceID, hashToken(string(fingerprint)))
		if err != nil {
			http.Error(w, "job admission failed; reconcile active jobs", 409)
			return
		}
		if !created {
			job, err := s.store.GetFleetJob(r.Context(), body.JobID, grant.ID)
			if err != nil || job.RequestHash != hashToken(string(fingerprint)) {
				http.Error(w, "job ID conflict", 409)
				return
			}
			writeJSON(w, 200, job)
			return // A submitted ID is never dispatched twice.
		}
	}
	if command == "fleet.workspace.create" {
		if s.store.CreateFleetWorkspace(r.Context(), body.WorkspaceID, grant.ID, body.AgentID) != nil {
			http.Error(w, "workspace allocation failed", 500)
			return
		}
	}
	if command == "fleet.job.cancel" {
		if s.store.CancelFleetJob(r.Context(), body.JobID, grant.ID) != nil {
			http.Error(w, "cancellation intent unavailable", 500)
			return
		}
	}
	connection, ok := s.router.ResolveAgent(grant.HomeID, body.AgentID)
	if !ok || !s.router.supportsCurrentAgent(connection, protocol.CapabilityFleetV1) {
		if command == "fleet.job.start" {
			_ = s.store.UpdateFleetJob(r.Context(), grant.ID, protocol.FleetJob{ID: body.JobID, State: "unknown"})
		}
		// Preserve the allocated ID so the caller can inspect uncertain outcomes.
		writeJSON(w, 503, map[string]any{"error": "fleet agent unavailable", "job_id": body.JobID, "workspace_id": body.WorkspaceID})
		return
	}
	response, err := s.sendAgentCommandTo(r.Context(), grant.HomeID, body.AgentID, command, body.FleetRequest)
	if response.Error != nil && (response.Error.Code == "fleet_revision_conflict" || response.Error.Code == "fleet_workspace_busy") {
		writeJSON(w, 409, map[string]any{"error": response.Error.Code})
		return
	}
	if err != nil || response.Error != nil {
		if command == "fleet.job.start" {
			_ = s.store.UpdateFleetJob(r.Context(), grant.ID, protocol.FleetJob{ID: body.JobID, State: "unknown"})
		}
		writeJSON(w, 502, map[string]any{"error": "fleet operation incomplete; inspect before retrying", "job_id": body.JobID, "workspace_id": body.WorkspaceID})
		return
	}
	if strings.HasPrefix(command, "fleet.job.") {
		var job protocol.FleetJob
		if json.Unmarshal(response.Payload, &job) != nil || job.ID != body.JobID || job.GrantID != grant.ID || job.WorkspaceID != body.WorkspaceID || (!protocol.FleetTerminal(job.State) && job.State != "running") {
			http.Error(w, "invalid job response", 502)
			return
		}
		if err = s.store.UpdateFleetJob(r.Context(), grant.ID, job); err != nil {
			http.Error(w, "job metadata update failed; inspect before retrying", 500)
			return
		}
	}
	s.audit(r.Context(), "fleet.operation", auditSeverityInfo, grant.UserID, "", grant.HomeID, requestIDFromContext(r.Context()), "fleet_grant", grant.ID, map[string]any{"operation": op, "agent_id": body.AgentID, "job_id": body.JobID, "workspace_id": body.WorkspaceID})
	writeJSON(w, 200, json.RawMessage(response.Payload))
}

func (s *Server) handleFleetGrants(w http.ResponseWriter, r *http.Request, parts []string) {
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	home, member, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		if r.Method == "GET" {
			grants, err := s.store.ListFleetGrants(r.Context(), home.ID)
			if err != nil {
				http.Error(w, "grant list unavailable", 500)
				return
			}
			dismissed, err := s.store.FleetGrantDismissals(r.Context(), auth.User.ID)
			if err != nil {
				http.Error(w, "grant list unavailable", 500)
				return
			}
			visible := []store.FleetGrant{}
			for _, g := range grants {
				if !dismissed[g.ID] && (member.Role == domain.HomeRoleAdmin || g.UserID == auth.User.ID) {
					visible = append(visible, g)
				}
			}
			writeJSON(w, 200, map[string]any{"grants": visible})
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			Agents     []string `json:"agents"`
			Operations []string `json:"operations"`
			Hours      *int     `json:"hours"`
			Duration   string   `json:"duration"`
			MCPTokenID string   `json:"mcp_token_id"`
			MCPAccount bool     `json:"mcp_account"`
		}
		if parseJSON(w, r, &body) != nil {
			http.Error(w, "invalid grant request", 400)
			return
		}
		expiresAt, err := fleetGrantExpiry(time.Now().UTC(), body.Duration, body.Hours)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.MCPTokenID != "" {
			http.Error(w, "Connection-based requests are no longer supported. Reload this page to request account device access.", 400)
			return
		}
		if body.MCPAccount {
			if len(body.Agents) != 0 || len(body.Operations) != 0 {
				http.Error(w, "account access includes all devices and permissions; omit agents and operations", 400)
				return
			}
			body.Operations = slices.Clone(protocol.FleetOperations)
			body.Agents = []string{}
		} else if protocol.ValidateFleetOperations(body.Operations) != nil || len(body.Agents) == 0 || len(body.Agents) > 32 {
			http.Error(w, "invalid grant request", 400)
			return
		}

		seen := map[string]bool{}
		for _, id := range body.Agents {
			a, err := s.store.GetAgentByID(r.Context(), id)
			if err != nil || a.HomeID != home.ID || seen[id] {
				http.NotFound(w, r)
				return
			}
			seen[id] = true
		}
		token := "hfg_" + newToken()
		grant := store.FleetGrant{MCPAccount: body.MCPAccount, ID: newID("fgrant"), HomeID: home.ID, UserID: auth.User.ID, Agents: body.Agents, Operations: body.Operations, State: "pending", ExpiresAt: expiresAt}
		if s.store.CreateFleetGrant(r.Context(), grant, hashToken(token)) != nil {
			http.Error(w, "grant request failed", 500)
			return
		}
		s.audit(r.Context(), "fleet.grant.requested", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "fleet_grant", grant.ID, nil)
		if body.MCPAccount {
			token = ""
		}
		writeJSON(w, 201, map[string]any{"grant": grant, "token": token})
		return
	}
	if len(parts) == 3 && parts[2] == "dismiss" && r.Method == "POST" {
		grant, err := s.store.GetFleetGrant(r.Context(), home.ID, parts[1])
		if err != nil || (member.Role != domain.HomeRoleAdmin && grant.UserID != auth.User.ID) {
			http.NotFound(w, r)
			return
		}
		if err := s.store.DismissFleetGrant(r.Context(), home.ID, grant.ID, auth.User.ID); err != nil {
			http.Error(w, "only expired or revoked access can be dismissed", 409)
			return
		}
		s.audit(r.Context(), "fleet.grant.dismissed", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "fleet_grant", grant.ID, nil)
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if len(parts) != 3 || r.Method != "POST" || (parts[2] != "approve" && parts[2] != "revoke") {
		http.NotFound(w, r)
		return
	}
	grant, err := s.store.GetFleetGrant(r.Context(), home.ID, parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if member.Role != domain.HomeRoleAdmin && (parts[2] != "revoke" || grant.UserID != auth.User.ID) {
		http.Error(w, "administrator approval required", 403)
		return
	}
	var body struct {
		Confirmation string `json:"confirmation"`
		ActionToken  string `json:"action_token"`
	}
	if parseJSON(w, r, &body) != nil {
		http.Error(w, "invalid approval", 400)
		return
	}
	action := "fleet." + parts[2] + ":" + grant.ID
	phrase := strings.ToUpper(parts[2]) + " " + grant.ID
	if body.ActionToken == "" {
		token, expires := s.adminActionTokens.Issue(auth.User.ID, action, 5*time.Minute)
		writeJSON(w, 200, map[string]any{"grant": grant, "confirmation": phrase, "action_token": token, "expires_at": expires, "authority": "Commands run with the agent’s full account permissions, including root for system agents."})
		return
	}
	if body.Confirmation != phrase || s.adminActionTokens.Consume(body.ActionToken, auth.User.ID, action) != nil {
		http.Error(w, "approval confirmation invalid", 403)
		return
	}
	state := "approved"
	if parts[2] == "revoke" {
		state = "revoked"
	}
	if err = s.store.SetFleetGrantState(r.Context(), home.ID, grant.ID, auth.User.ID, state); err != nil {
		http.Error(w, "grant state conflict", 409)
		return
	}
	s.audit(r.Context(), "fleet.grant."+state, auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "fleet_grant", grant.ID, nil)
	if state == "revoked" {
		jobs, _ := s.store.ListFleetJobs(r.Context(), grant.ID)
		for _, j := range jobs {
			if j.State == "running" || j.State == "dispatching" || j.State == "unknown" {
				// Revocation denies new access immediately and persists cancellation for reconnect.
				_ = s.store.CancelFleetJob(r.Context(), j.ID, grant.ID)
				conn, ok := s.router.ResolveAgent(home.ID, j.AgentID)
				if !ok || !s.router.supportsCurrentAgent(conn, protocol.CapabilityFleetV1) {
					continue
				}
				response, err := s.sendAgentCommandTo(r.Context(), home.ID, j.AgentID, "fleet.job.cancel", protocol.FleetRequest{GrantID: grant.ID, JobID: j.ID})
				if err == nil && response.Error == nil {
					var status protocol.FleetJob
					if json.Unmarshal(response.Payload, &status) == nil && status.ID == j.ID && status.GrantID == grant.ID {
						_ = s.store.UpdateFleetJob(r.Context(), grant.ID, status)
					}
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"state": state})
}

// Reconciliation never resubmits a start. It only inspects durable IDs or cancels.
func (s *Server) reconcileFleetAgent(ctx context.Context, home, agent string) {
	if _, loaded := s.fleetReconcileWorkers.LoadOrStore(agent, true); loaded {
		return
	}
	defer s.fleetReconcileWorkers.Delete(agent)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	connection, ok := s.router.ResolveAgent(home, agent)
	if !ok || !s.router.supportsCurrentAgent(connection, protocol.CapabilityFleetV1) {
		return
	}
	jobs, err := s.store.FleetAgentJobs(ctx, agent)
	if err != nil {
		return
	}
	for _, j := range jobs {
		command := "fleet.job.read"
		if j.CancelRequested {
			command = "fleet.job.cancel"
		}
		response, err := s.sendAgentCommandTo(ctx, home, agent, command, protocol.FleetRequest{GrantID: j.GrantID, JobID: j.ID, After: j.Cursor})
		if err != nil {
			continue
		}
		if response.Error != nil {
			if response.Error.Code == "fleet_job_not_found" {
				_ = s.store.UpdateFleetJob(ctx, j.GrantID, protocol.FleetJob{ID: j.ID, State: "unknown"})
			}
			continue
		}
		var status protocol.FleetJob
		if json.Unmarshal(response.Payload, &status) == nil && status.ID == j.ID && status.GrantID == j.GrantID && status.WorkspaceID == j.WorkspaceID && (protocol.FleetTerminal(status.State) || status.State == "running") {
			status.Output = nil
			if s.store.UpdateFleetJob(ctx, j.GrantID, status) == nil && status.State != j.State {
				s.audit(ctx, "fleet.job.state", auditSeverityInfo, "", "", home, "", "fleet_job", j.ID, map[string]any{"agent_id": agent, "grant_id": j.GrantID, "state": status.State})
			}
		}
	}
}

// fleetGrantExpiry preserves the legacy hours API while offering calendar durations.
// Calendar dates clamp to the final day when the target month is shorter.
func fleetGrantExpiry(now time.Time, duration string, hours *int) (*time.Time, error) {
	invalid := fmt.Errorf("choose duration 1_month, 3_months, 6_months, 1_year, or infinite; legacy hours must be 1–168 and cannot be combined with duration")
	if hours != nil {
		if duration != "" || *hours < 1 || *hours > 168 {
			return nil, invalid
		}
		expiry := now.Add(time.Duration(*hours) * time.Hour)
		return &expiry, nil
	}
	months := 0
	switch duration {
	case "1_month":
		months = 1
	case "3_months":
		months = 3
	case "6_months":
		months = 6
	case "1_year":
		months = 12
	case "infinite":
		return nil, nil
	default:
		return nil, invalid
	}
	first := time.Date(now.Year(), now.Month()+time.Month(months), 1, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), now.Location())
	day := now.Day()
	if last := first.AddDate(0, 1, -1).Day(); day > last {
		day = last
	}
	expiry := first.AddDate(0, 0, day-1)
	return &expiry, nil
}
