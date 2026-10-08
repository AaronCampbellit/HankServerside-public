-- Additive execution-v2 storage. Existing v1 runs remain unchanged.
ALTER TABLE assistant_sessions ADD CONSTRAINT assistant_sessions_scope_key UNIQUE (id, home_id, user_id);
CREATE TABLE assistant_tasks (
 id TEXT PRIMARY KEY,
 home_id TEXT NOT NULL REFERENCES homes(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 session_id TEXT,
 submission_key TEXT NOT NULL CHECK (length(submission_key) BETWEEN 1 AND 256),
 schema_version INTEGER NOT NULL DEFAULT 2 CHECK (schema_version = 2),
 request_text TEXT NOT NULL CHECK (octet_length(request_text) <= 131072),
 state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','waiting_approval','waiting_input','waiting_client','waiting_retry','reconciling','completed','failed','cancelled')),
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
 checkpoint JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 2097152),
 max_turns INTEGER NOT NULL DEFAULT 12 CHECK (max_turns BETWEEN 1 AND 100),
 max_calls INTEGER NOT NULL DEFAULT 24 CHECK (max_calls BETWEEN 1 AND 1000),
 max_tokens BIGINT NOT NULL DEFAULT 32000 CHECK (max_tokens BETWEEN 1 AND 10000000),
 turns INTEGER NOT NULL DEFAULT 0 CHECK (turns >= 0 AND turns <= max_turns),
 calls INTEGER NOT NULL DEFAULT 0 CHECK (calls >= 0 AND calls <= max_calls),
 tokens BIGINT NOT NULL DEFAULT 0 CHECK (tokens >= 0),
 active_ms BIGINT NOT NULL DEFAULT 0 CHECK (active_ms >= 0),
 max_active_ms BIGINT NOT NULL DEFAULT 300000 CHECK (max_active_ms BETWEEN 1 AND 3600000),
 cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
 lease_owner TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 fence BIGINT NOT NULL DEFAULT 0 CHECK (fence >= 0),
 event_sequence BIGINT NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
 retry_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (user_id, session_id, submission_key),
 FOREIGN KEY (session_id, home_id, user_id) REFERENCES assistant_sessions(id, home_id, user_id) ON DELETE SET NULL (session_id),
 CHECK ((lease_owner = '' AND lease_until IS NULL) OR (lease_owner <> '' AND lease_until IS NOT NULL))
);
CREATE INDEX assistant_tasks_session ON assistant_tasks(session_id, created_at, id);
CREATE INDEX assistant_tasks_scope ON assistant_tasks(home_id, user_id, created_at DESC);
CREATE INDEX assistant_tasks_claim ON assistant_tasks(state, retry_at, lease_until, created_at) WHERE state IN ('queued','running','waiting_retry','reconciling');
CREATE TABLE assistant_task_steps (
 task_id TEXT NOT NULL REFERENCES assistant_tasks(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL CHECK (length(call_id) BETWEEN 1 AND 256),
 sequence BIGINT NOT NULL CHECK (sequence > 0),
 tool TEXT NOT NULL CHECK (length(tool) BETWEEN 1 AND 100),
 tool_version INTEGER NOT NULL CHECK (tool_version > 0),
 arguments JSONB NOT NULL CHECK (jsonb_typeof(arguments) = 'object' AND octet_length(arguments::text) <= 65536),
 action_digest TEXT NOT NULL CHECK (length(action_digest) = 64),
 state TEXT NOT NULL CHECK (state IN ('pending','running','waiting_approval','completed','failed','unknown','cancelled')),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
 result JSONB CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 524288)),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (task_id, call_id),
 UNIQUE (task_id, sequence)
);
CREATE TABLE assistant_task_approvals (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL,
 call_id TEXT NOT NULL,
 action_digest TEXT NOT NULL CHECK (length(action_digest) = 64),
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','rejected','expired','consumed','cancelled')),
 expires_at TIMESTAMPTZ NOT NULL,
 decided_by TEXT REFERENCES users(id),
 decided_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (task_id, call_id),
 FOREIGN KEY (task_id, call_id) REFERENCES assistant_task_steps(task_id, call_id) ON DELETE CASCADE,
 CHECK ((state IN ('approved','rejected','consumed') AND decided_by IS NOT NULL AND decided_at IS NOT NULL) OR state IN ('pending','expired','cancelled'))
);
CREATE INDEX assistant_task_approvals_actor ON assistant_task_approvals(decided_by);
CREATE INDEX assistant_task_approvals_expiry ON assistant_task_approvals(expires_at) WHERE state = 'pending';
CREATE TABLE assistant_operation_receipts (
 operation_id TEXT PRIMARY KEY,
 home_id TEXT NOT NULL REFERENCES homes(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 task_id TEXT REFERENCES assistant_tasks(id) ON DELETE SET NULL,
 call_id TEXT NOT NULL,
 tool TEXT NOT NULL,
 action_digest TEXT NOT NULL CHECK (length(action_digest) = 64),
 outcome TEXT NOT NULL CHECK (outcome IN ('not_started','accepted','confirmed','failed','unknown')),
 result JSONB CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 524288)),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (task_id, call_id)
);
CREATE INDEX assistant_operation_receipts_scope ON assistant_operation_receipts(home_id, user_id, created_at);
CREATE INDEX assistant_operation_receipts_user ON assistant_operation_receipts(user_id);
CREATE TABLE assistant_task_events (
 task_id TEXT NOT NULL REFERENCES assistant_tasks(id) ON DELETE CASCADE,
 sequence BIGINT NOT NULL CHECK (sequence > 0),
 event_type TEXT NOT NULL CHECK (event_type IN ('queued','running','searching','reading','preparing','waiting_approval','waiting_input','waiting_client','retrying','checking','completed','failed','cancelled','followup')),
 call_id TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (task_id, sequence)
);
CREATE TABLE assistant_task_inputs (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES assistant_tasks(id) ON DELETE CASCADE,
 input_key TEXT NOT NULL CHECK (length(input_key) BETWEEN 1 AND 256),
 text TEXT NOT NULL CHECK (octet_length(text) BETWEEN 1 AND 16384),
 applied BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(task_id,input_key)
);
CREATE INDEX assistant_task_inputs_pending ON assistant_task_inputs(task_id,id) WHERE NOT applied;
