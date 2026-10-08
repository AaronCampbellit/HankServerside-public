-- Additive demo control-plane state. No existing rows are changed.
CREATE TABLE fleet_grants (
 id TEXT PRIMARY KEY,
 home_id TEXT NOT NULL REFERENCES homes(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 token_hash TEXT NOT NULL UNIQUE,
 operations JSONB NOT NULL CHECK (jsonb_typeof(operations) = 'array' AND jsonb_array_length(operations) BETWEEN 1 AND 5 AND operations <@ '["workspace.read","workspace.write","job.run","job.read","job.cancel"]'::jsonb),
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','revoked')),
 approved_by TEXT REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 expires_at TIMESTAMPTZ NOT NULL,
 CHECK (expires_at > created_at),
 CHECK (state <> 'approved' OR approved_by IS NOT NULL),
 UNIQUE(id,home_id)
);
CREATE INDEX fleet_grants_home_user ON fleet_grants(home_id,user_id,created_at);
CREATE UNIQUE INDEX fleet_agent_home_identity ON agents(id,home_id);
CREATE TABLE fleet_grant_targets (
 grant_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 home_id TEXT NOT NULL,
 PRIMARY KEY(grant_id,agent_id),
 FOREIGN KEY(grant_id,home_id) REFERENCES fleet_grants(id,home_id),
 FOREIGN KEY(agent_id,home_id) REFERENCES agents(id,home_id) ON DELETE CASCADE
);
CREATE TABLE fleet_workspaces (
 id TEXT PRIMARY KEY,
 grant_id TEXT NOT NULL REFERENCES fleet_grants(id),
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(id,grant_id,agent_id),
 FOREIGN KEY(grant_id,agent_id) REFERENCES fleet_grant_targets(grant_id,agent_id) ON DELETE CASCADE
);
CREATE TABLE fleet_jobs (
 id TEXT PRIMARY KEY,
 grant_id TEXT NOT NULL REFERENCES fleet_grants(id),
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'dispatching' CHECK (state IN ('dispatching','running','succeeded','failed','cancelled','timed_out','unknown')),
 exit_code INTEGER,
 output_cursor BIGINT NOT NULL DEFAULT 0 CHECK (output_cursor >= 0),
 truncated BOOLEAN NOT NULL DEFAULT false,
 cancel_requested BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(workspace_id,grant_id,agent_id) REFERENCES fleet_workspaces(id,grant_id,agent_id) ON DELETE CASCADE
);
CREATE INDEX fleet_jobs_grant_created ON fleet_jobs(grant_id,created_at DESC);
CREATE INDEX fleet_jobs_active_agent ON fleet_jobs(agent_id) WHERE state IN ('dispatching','running');
