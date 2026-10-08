package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type FleetGrant struct {
	ID            string     `json:"id"`
	MCPAccount    bool       `json:"mcp_account"`
	RequesterName string     `json:"requester_name,omitempty"`
	MCPTokenID    string     `json:"mcp_token_id,omitempty"`
	HomeID        string     `json:"home_id"`
	UserID        string     `json:"user_id"`
	Agents        []string   `json:"agents"`
	Operations    []string   `json:"operations"`
	State         string     `json:"state"`
	ApprovedBy    string     `json:"approved_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

func (s *Store) CreateFleetGrant(ctx context.Context, g FleetGrant, hash string) error {
	if protocol.ValidateFleetOperations(g.Operations) != nil || (!g.MCPAccount && (len(g.Agents) < 1 || len(g.Agents) > 32)) || (g.MCPAccount && (g.MCPTokenID != "" || len(g.Agents) != 0 || len(g.Operations) != len(protocol.FleetOperations))) {
		return errors.New("invalid fleet scope")
	}
	ops, _ := json.Marshal(g.Operations)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_grants(id,home_id,user_id,token_hash,operations,expires_at,mcp_token_id,mcp_account) VALUES($1,$2,$3,$4,$5::jsonb,$6,NULLIF($7,''),$8)`, g.ID, g.HomeID, g.UserID, hash, string(ops), g.ExpiresAt, g.MCPTokenID, g.MCPAccount)
	if err != nil {
		return err
	}
	for _, agent := range g.Agents {
		if _, err = tx.ExecContext(ctx, `INSERT INTO fleet_grant_targets(grant_id,agent_id,home_id) VALUES($1,$2,$3)`, g.ID, agent, g.HomeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const fleetGrantCols = `COALESCE(mcp_token_id,''),mcp_account,id,home_id,user_id,CASE WHEN mcp_account THEN COALESCE((SELECT jsonb_agg(a.id ORDER BY a.id) FROM agents a WHERE a.home_id=fleet_grants.home_id),'[]'::jsonb) ELSE COALESCE((SELECT jsonb_agg(agent_id ORDER BY agent_id) FROM fleet_grant_targets t WHERE t.grant_id=fleet_grants.id),'[]'::jsonb) END,operations,state,COALESCE(approved_by,''),created_at,expires_at,(SELECT COALESCE(NULLIF(u.display_name,''),u.email) FROM users u WHERE u.id=fleet_grants.user_id)`

func scanFleetGrant(row interface{ Scan(...any) error }) (FleetGrant, error) {
	var g FleetGrant
	var agents, ops []byte
	err := row.Scan(&g.MCPTokenID, &g.MCPAccount, &g.ID, &g.HomeID, &g.UserID, &agents, &ops, &g.State, &g.ApprovedBy, &g.CreatedAt, &g.ExpiresAt, &g.RequesterName)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(agents, &g.Agents)
	}
	if err == nil {
		err = json.Unmarshal(ops, &g.Operations)
	}
	return g, err
}
func (s *Store) FleetGrantByHash(ctx context.Context, hash string) (FleetGrant, error) {
	return scanFleetGrant(s.queryRow(ctx, `SELECT `+fleetGrantCols+` FROM fleet_grants WHERE token_hash=? AND mcp_token_id IS NULL AND NOT mcp_account AND state='approved' AND (expires_at IS NULL OR expires_at>now()) AND EXISTS(SELECT 1 FROM home_memberships m WHERE m.home_id=fleet_grants.home_id AND m.user_id=fleet_grants.user_id)`, hash))
}
func (s *Store) GetFleetGrant(ctx context.Context, home, id string) (FleetGrant, error) {
	return scanFleetGrant(s.queryRow(ctx, `SELECT `+fleetGrantCols+` FROM fleet_grants WHERE home_id=? AND id=?`, home, id))
}
func (s *Store) ListFleetGrants(ctx context.Context, home string) ([]FleetGrant, error) {
	rows, err := s.query(ctx, `SELECT `+fleetGrantCols+` FROM fleet_grants WHERE home_id=? ORDER BY created_at DESC LIMIT 200`, home)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []FleetGrant{}
	for rows.Next() {
		g, err := scanFleetGrant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}
func (s *Store) SetFleetGrantState(ctx context.Context, home, id, actor, state string) error {
	result, err := s.exec(ctx, `UPDATE fleet_grants SET state=?,approved_by=CASE WHEN ?='approved' THEN ? ELSE approved_by END WHERE id=? AND home_id=? AND ((?='approved' AND state='pending' AND (expires_at IS NULL OR expires_at>now())) OR (?='revoked' AND state IN ('pending','approved')))`, state, state, actor, id, home, state, state)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrNotFound
	}
	return err
}
func (s *Store) CreateFleetWorkspace(ctx context.Context, id, grant, agent string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Materialize only the target being used, keeping the existing Home and
	// workspace foreign keys for account access, including newly enrolled devices.
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_grant_targets(grant_id,agent_id,home_id)
 SELECT g.id,a.id,g.home_id FROM fleet_grants g JOIN agents a ON a.home_id=g.home_id
 WHERE g.id=$1 AND a.id=$2 AND g.mcp_account AND g.state='approved'
 AND (g.expires_at IS NULL OR g.expires_at>now())
 AND EXISTS(SELECT 1 FROM home_memberships m WHERE m.home_id=g.home_id AND m.user_id=g.user_id)
 ON CONFLICT(grant_id,agent_id) DO NOTHING`, grant, agent)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_workspaces(id,grant_id,agent_id) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, id, grant, agent)
	if err != nil {
		return err
	}
	var owns bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_workspaces WHERE id=$1 AND grant_id=$2 AND agent_id=$3)`, id, grant, agent).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return ErrNotFound
	}
	return tx.Commit()
}
func (s *Store) OwnsFleetWorkspace(ctx context.Context, id, grant, agent string) bool {
	var found bool
	return s.queryRow(ctx, `SELECT true FROM fleet_workspaces WHERE id=? AND grant_id=? AND agent_id=?`, id, grant, agent).Scan(&found) == nil && found
}

type FleetJobRecord struct {
	CancelRequested bool `json:"cancel_requested"`
	protocol.FleetJob
	AgentID     string    `json:"agent_id"`
	RequestHash string    `json:"-"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Store) CreateFleetJob(ctx context.Context, id, grant, agent, workspace, hash string) (bool, error) {
	// Serialize per-agent admission using a transaction-scoped advisory lock.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, agent); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_jobs WHERE id=$1)`, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, tx.Commit()
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM fleet_jobs WHERE agent_id=$1 AND state IN ('dispatching','running')`, agent).Scan(&count); err != nil {
		return false, err
	}
	if count >= protocol.FleetMaxJobs {
		return false, errors.New("fleet concurrency limit reached; reconcile existing jobs")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_jobs(id,grant_id,agent_id,workspace_id,request_hash) VALUES($1,$2,$3,$4,$5)`, id, grant, agent, workspace, hash)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (s *Store) GetFleetJob(ctx context.Context, id, grant string) (FleetJobRecord, error) {
	var j FleetJobRecord
	err := s.queryRow(ctx, `SELECT id,grant_id,agent_id,workspace_id,request_hash,state,exit_code,output_cursor,truncated,updated_at,cancel_requested FROM fleet_jobs WHERE id=? AND grant_id=?`, id, grant).Scan(&j.ID, &j.GrantID, &j.AgentID, &j.WorkspaceID, &j.RequestHash, &j.State, &j.ExitCode, &j.Cursor, &j.Truncated, &j.UpdatedAt, &j.CancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return j, err
}
func (s *Store) UpdateFleetJob(ctx context.Context, grant string, j protocol.FleetJob) error {
	_, err := s.exec(ctx, `UPDATE fleet_jobs SET state=?,exit_code=?,output_cursor=GREATEST(output_cursor,?),truncated=truncated OR ?,updated_at=now() WHERE id=? AND grant_id=? AND (state IN ('dispatching','running','unknown') OR state=?)`, j.State, j.ExitCode, j.Cursor, j.Truncated, j.ID, grant, j.State)
	return err
}
func (s *Store) ListFleetJobs(ctx context.Context, grant string) ([]FleetJobRecord, error) {
	rows, err := s.query(ctx, `SELECT id FROM fleet_jobs WHERE grant_id=? ORDER BY created_at DESC LIMIT 200`, grant)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []FleetJobRecord{}
	for _, id := range ids {
		j, err := s.GetFleetJob(ctx, id, grant)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, nil
}

func (s *Store) CancelFleetJob(ctx context.Context, id, grant string) error {
	_, err := s.exec(ctx, `UPDATE fleet_jobs SET cancel_requested=true,updated_at=now() WHERE id=? AND grant_id=?`, id, grant)
	return err
}
func (s *Store) FleetAgentJobs(ctx context.Context, agent string) ([]FleetJobRecord, error) {
	rows, err := s.query(ctx, `SELECT j.id,j.grant_id, (g.state <> 'approved' OR (g.expires_at IS NOT NULL AND g.expires_at<=now()) OR (g.mcp_token_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM mcp_oauth_tokens t WHERE t.id=g.mcp_token_id AND t.revoked_at IS NULL AND (t.refresh_expires_at IS NULL OR t.refresh_expires_at>now() OR t.access_expires_at>now()))) OR NOT EXISTS(SELECT 1 FROM home_memberships m WHERE m.home_id=g.home_id AND m.user_id=g.user_id)) FROM fleet_jobs j JOIN fleet_grants g ON g.id=j.grant_id WHERE j.agent_id=? AND j.state IN ('dispatching','running','unknown') ORDER BY CASE WHEN j.state IN ('dispatching','running') THEN 0 ELSE 1 END,j.updated_at LIMIT 128`, agent)
	if err != nil {
		return nil, err
	}
	type item struct {
		id, grant string
		cancel    bool
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.grant, &i.cancel); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []FleetJobRecord{}
	for _, i := range items {
		if i.cancel {
			if err = s.CancelFleetJob(ctx, i.id, i.grant); err != nil {
				return nil, err
			}
		}
		j, err := s.GetFleetJob(ctx, i.id, i.grant)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, nil
}

// FleetGrantForMCP rechecks the calling token, account, membership and expiry.
// Account grants work across apps; pre-migration grants retain their exact binding.
func (s *Store) FleetGrantForMCP(ctx context.Context, id, token, user string) (FleetGrant, error) {
	return scanFleetGrant(s.queryRow(ctx, `SELECT `+fleetGrantCols+` FROM fleet_grants WHERE id=? AND user_id=? AND (mcp_account OR mcp_token_id=?) AND state='approved' AND (expires_at IS NULL OR expires_at>now()) AND EXISTS(SELECT 1 FROM home_memberships m WHERE m.home_id=fleet_grants.home_id AND m.user_id=fleet_grants.user_id) AND EXISTS(SELECT 1 FROM mcp_oauth_tokens t WHERE t.id=? AND t.user_id=fleet_grants.user_id AND t.revoked_at IS NULL AND t.access_expires_at>now())`, id, user, token, token))
}

// DismissFleetGrant changes only this viewer's history, never authorization.
func (s *Store) DismissFleetGrant(ctx context.Context, home, id, user string) error {
	result, err := s.exec(ctx, `INSERT INTO fleet_grant_dismissals(grant_id,user_id)
 SELECT id, ? FROM fleet_grants WHERE home_id=? AND id=? AND (state='revoked' OR expires_at<=now())
 ON CONFLICT(grant_id,user_id) DO UPDATE SET dismissed_at=now()`, user, home, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrNotFound
	}
	return err
}
func (s *Store) FleetGrantDismissals(ctx context.Context, user string) (map[string]bool, error) {
	rows, err := s.query(ctx, `SELECT grant_id FROM fleet_grant_dismissals WHERE user_id=?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}
