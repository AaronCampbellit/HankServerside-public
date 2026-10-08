CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS homes (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	name TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_memberships (
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY(home_id, user_id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id)
);
INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at)
	SELECT h.id, h.user_id, 'admin', h.created_at, h.updated_at
	FROM homes h
	WHERE NOT EXISTS (
		SELECT 1
		FROM home_memberships hm
		WHERE hm.home_id = h.id AND hm.user_id = h.user_id
	);
CREATE TABLE IF NOT EXISTS home_invitations (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	email TEXT NOT NULL,
	role TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	accepted_at TIMESTAMP NULL,
	expires_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id)
);
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	name TEXT NOT NULL,
	status TEXT NOT NULL,
	last_seen_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id)
);
CREATE TABLE IF NOT EXISTS agent_tokens (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	revoked_at TIMESTAMP NULL,
	expires_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(agent_id) REFERENCES agents(id)
);
CREATE TABLE IF NOT EXISTS app_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	expires_at TIMESTAMP NOT NULL,
	revoked_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS notification_settings (
	user_id TEXT PRIMARY KEY,
	storage_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	notes_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	dashboard_entities_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS apns_devices (
	user_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	device_id TEXT NOT NULL,
	token TEXT NOT NULL,
	environment TEXT NOT NULL,
	bundle_id TEXT NOT NULL,
	enabled_categories JSONB NOT NULL DEFAULT '[]'::jsonb,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	last_registered_at TIMESTAMP NOT NULL,
	PRIMARY KEY(user_id, device_id),
	FOREIGN KEY(user_id) REFERENCES users(id),
	FOREIGN KEY(session_id) REFERENCES app_sessions(id)
);
CREATE TABLE IF NOT EXISTS user_profile_backups (
	user_id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL,
	snapshot_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_notes (
	home_id TEXT NOT NULL,
	note_id TEXT NOT NULL,
	title TEXT NOT NULL,
	content TEXT NOT NULL,
	page_type TEXT NOT NULL,
	board_json TEXT NOT NULL DEFAULT '',
	revision TEXT NOT NULL,
	checksum TEXT NOT NULL,
	deleted_at TIMESTAMP NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	PRIMARY KEY(home_id, note_id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS user_notes (
	id TEXT PRIMARY KEY,
	note_id TEXT NOT NULL,
	owner_user_id TEXT NOT NULL,
	home_id TEXT NULL,
	parent_id TEXT NULL,
	sort_order INTEGER NOT NULL DEFAULT 0,
	title TEXT NOT NULL,
	content TEXT NOT NULL,
	body_markdown TEXT NOT NULL DEFAULT '',
	body_format TEXT NOT NULL DEFAULT 'markdown',
	page_type TEXT NOT NULL,
	board_json TEXT NOT NULL DEFAULT '',
	revision TEXT NOT NULL,
	checksum TEXT NOT NULL,
	crdt_state_json TEXT NOT NULL DEFAULT '',
	collab_version INTEGER NOT NULL DEFAULT 0,
	deleted_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	FOREIGN KEY(owner_user_id) REFERENCES users(id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS note_shares (
	note_id TEXT NOT NULL,
	home_id TEXT NOT NULL,
	target_user_id TEXT NOT NULL,
	shared_by TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY(note_id, target_user_id),
	FOREIGN KEY(note_id) REFERENCES user_notes(id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(target_user_id) REFERENCES users(id),
	FOREIGN KEY(shared_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS note_operations (
	note_id TEXT NOT NULL,
	op_id TEXT NOT NULL,
	actor_user_id TEXT NOT NULL,
	session_id TEXT NULL,
	base_version INTEGER NOT NULL,
	applied_version INTEGER NOT NULL,
	op_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY(note_id, op_id),
	FOREIGN KEY(note_id) REFERENCES user_notes(id),
	FOREIGN KEY(actor_user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS note_attachments (
	id TEXT PRIMARY KEY,
	note_id TEXT NOT NULL,
	home_id TEXT NULL,
	owner_user_id TEXT NOT NULL,
	filename TEXT NOT NULL,
	content_type TEXT NOT NULL,
	size_bytes BIGINT NOT NULL DEFAULT 0,
	checksum_sha256 TEXT NOT NULL DEFAULT '',
	storage_key TEXT NOT NULL,
	deleted_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(note_id) REFERENCES user_notes(id) ON DELETE CASCADE,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(owner_user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS user_profile_settings (
	user_id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL DEFAULT 0,
	settings_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS user_profile_secret_vaults (
	user_id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL DEFAULT 0,
	key_id TEXT NOT NULL DEFAULT '',
	vault_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_note_sync_state (
	home_id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	last_manifest_at TIMESTAMP NULL,
	last_pull_at TIMESTAMP NULL,
	last_push_at TIMESTAMP NULL,
	status TEXT NOT NULL,
	last_error TEXT NOT NULL DEFAULT '',
	pending_pull_count INTEGER NOT NULL DEFAULT 0,
	pending_push_count INTEGER NOT NULL DEFAULT 0,
	last_successful_sync_at TIMESTAMP NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id)
);
CREATE TABLE IF NOT EXISTS home_service_profiles (
	home_id TEXT NOT NULL,
	service_type TEXT NOT NULL,
	public_config_json TEXT NOT NULL DEFAULT '',
	secret_version INTEGER NOT NULL DEFAULT 0,
	applied_version INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	last_backup_at TIMESTAMP NULL,
	last_error TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(home_id, service_type),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_quick_links (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	title TEXT NOT NULL,
	url TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	sort_order INTEGER NOT NULL DEFAULT 0,
	health_check_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	status TEXT NOT NULL DEFAULT 'unchecked' CHECK (status IN ('unchecked', 'up', 'down', 'disabled')),
	status_code INTEGER NOT NULL DEFAULT 0,
	last_checked_at TIMESTAMP NULL,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_permissions (
	home_id TEXT PRIMARY KEY,
	homeassistant_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	files_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	notes_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS home_member_permissions (
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	homeassistant_enabled BOOLEAN NULL,
	files_enabled BOOLEAN NULL,
	notes_enabled BOOLEAN NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	PRIMARY KEY(home_id, user_id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_sessions (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	last_message_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_messages (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	role TEXT NOT NULL,
	status TEXT NOT NULL,
	content_json TEXT NOT NULL,
	model_name TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(session_id) REFERENCES assistant_sessions(id)
);
CREATE TABLE IF NOT EXISTS assistant_runs (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	state TEXT NOT NULL,
	requires_client_tools BOOLEAN NOT NULL DEFAULT FALSE,
	requires_confirmation BOOLEAN NOT NULL DEFAULT FALSE,
	pending_action_json TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	completed_at TIMESTAMP NULL,
	FOREIGN KEY(session_id) REFERENCES assistant_sessions(id),
	FOREIGN KEY(message_id) REFERENCES assistant_messages(id)
);
CREATE TABLE IF NOT EXISTS assistant_attachments (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	client_attachment_id TEXT NOT NULL,
	filename TEXT NOT NULL,
	content_type TEXT NOT NULL,
	kind TEXT NOT NULL,
	size_bytes BIGINT NOT NULL DEFAULT 0,
	checksum_sha256 TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	committed_at TIMESTAMP NULL,
	UNIQUE(session_id, client_attachment_id),
	FOREIGN KEY(session_id) REFERENCES assistant_sessions(id) ON DELETE CASCADE,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_tool_calls (
	id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	tool_scope TEXT NOT NULL,
	arguments_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	result_json JSONB NULL,
	status TEXT NOT NULL,
	started_at TIMESTAMP NOT NULL,
	completed_at TIMESTAMP NULL,
	FOREIGN KEY(run_id) REFERENCES assistant_runs(id)
);
CREATE TABLE IF NOT EXISTS assistant_settings (
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	profile_notes_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	home_notes_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	files_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	calendar_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	homeassistant_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	project_docs_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	conversations_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	system_prompt TEXT NOT NULL DEFAULT '',
	max_context_items INTEGER NOT NULL DEFAULT 8,
	chat_model TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	PRIMARY KEY(home_id, user_id),
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_documents (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	user_id TEXT NULL,
	source_type TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_key TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL,
	path TEXT NOT NULL DEFAULT '',
	canonical_uri TEXT NOT NULL,
	metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	search_text TEXT NOT NULL,
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_version TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_chunks (
	id TEXT PRIMARY KEY,
	document_id TEXT NOT NULL,
	chunk_index INTEGER NOT NULL,
	content TEXT NOT NULL,
	token_count INTEGER NOT NULL,
	embedding_json TEXT NOT NULL DEFAULT '',
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_version TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(document_id) REFERENCES assistant_documents(id) ON DELETE CASCADE,
	UNIQUE(document_id, chunk_index)
);
CREATE TABLE IF NOT EXISTS assistant_file_index (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	service_profile_id TEXT NOT NULL DEFAULT '',
	path TEXT NOT NULL,
	name TEXT NOT NULL,
	is_directory BOOLEAN NOT NULL DEFAULT FALSE,
	size_bytes BIGINT NOT NULL DEFAULT 0,
	modified_at TIMESTAMP NULL,
	search_text TEXT NOT NULL,
	metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	embedding_json TEXT NOT NULL DEFAULT '',
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_version TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	UNIQUE(home_id, path)
);
CREATE TABLE IF NOT EXISTS openai_accounts (
	user_id TEXT PRIMARY KEY,
	provider_user_id TEXT NOT NULL DEFAULT '',
	auth_provider TEXT NOT NULL DEFAULT 'chatgpt_codex',
	chatgpt_plan_type TEXT NOT NULL DEFAULT '',
	access_token TEXT NOT NULL DEFAULT '',
	refresh_token TEXT NOT NULL DEFAULT '',
	token_type TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS openai_oauth_states (
	state_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	code_verifier TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS assistant_calendar_entries (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	device_id TEXT NOT NULL,
	external_event_id TEXT NOT NULL,
	calendar_id TEXT NOT NULL,
	title TEXT NOT NULL,
	location TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	starts_at TIMESTAMP NOT NULL,
	ends_at TIMESTAMP NOT NULL,
	is_all_day BOOLEAN NOT NULL DEFAULT FALSE,
	search_text TEXT NOT NULL DEFAULT '',
	metadata_json TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_homes_user_id ON homes(user_id);
CREATE INDEX IF NOT EXISTS idx_home_memberships_user_id ON home_memberships(user_id);
CREATE INDEX IF NOT EXISTS idx_home_invitations_email ON home_invitations(email);
CREATE INDEX IF NOT EXISTS idx_agents_home_id ON agents(home_id);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_agent_id ON agent_tokens(agent_id);
CREATE INDEX IF NOT EXISTS idx_app_sessions_user_id ON app_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_apns_devices_session ON apns_devices(session_id);
CREATE INDEX IF NOT EXISTS idx_apns_devices_token ON apns_devices(token);
CREATE INDEX IF NOT EXISTS idx_home_notes_updated_at ON home_notes(home_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_user_notes_owner_updated_at ON user_notes(owner_user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_user_notes_home_updated_at ON user_notes(home_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_note_shares_home_user ON note_shares(home_id, target_user_id);
CREATE INDEX IF NOT EXISTS idx_note_operations_note_version ON note_operations(note_id, applied_version);
CREATE INDEX IF NOT EXISTS idx_note_attachments_note ON note_attachments(note_id, created_at ASC);
ALTER TABLE user_notes ADD COLUMN IF NOT EXISTS parent_id TEXT NULL;
ALTER TABLE user_notes ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_notes ADD COLUMN IF NOT EXISTS body_markdown TEXT NOT NULL DEFAULT '';
ALTER TABLE user_notes ADD COLUMN IF NOT EXISTS body_format TEXT NOT NULL DEFAULT 'markdown';
UPDATE user_notes SET body_markdown = content WHERE body_markdown = '';
UPDATE user_notes SET body_format = 'markdown' WHERE body_format = '';
CREATE INDEX IF NOT EXISTS idx_user_notes_owner_parent_order ON user_notes(owner_user_id, parent_id, sort_order);
CREATE INDEX IF NOT EXISTS idx_user_notes_owner_root_order ON user_notes(owner_user_id, sort_order) WHERE parent_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_notes_home_parent_order ON user_notes(home_id, parent_id, sort_order) WHERE home_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_notes_owner_note_id ON user_notes(owner_user_id, note_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_notes_home_note_id ON user_notes(home_id, note_id) WHERE home_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_home_quick_links_home_order ON home_quick_links(home_id, sort_order, created_at);
CREATE INDEX IF NOT EXISTS idx_assistant_sessions_user_updated ON assistant_sessions(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_assistant_messages_session_created ON assistant_messages(session_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_assistant_attachments_session ON assistant_attachments(session_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_assistant_tool_calls_run ON assistant_tool_calls(run_id, started_at ASC);
CREATE INDEX IF NOT EXISTS idx_assistant_documents_home_type ON assistant_documents(home_id, source_type, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_assistant_chunks_document ON assistant_chunks(document_id, chunk_index ASC);
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_home_updated ON assistant_file_index(home_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_assistant_calendar_external ON assistant_calendar_entries(user_id, device_id, external_event_id);
ALTER TABLE openai_accounts ADD COLUMN IF NOT EXISTS auth_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE openai_accounts ADD COLUMN IF NOT EXISTS chatgpt_plan_type TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS project_docs_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS conversations_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS chat_model TEXT NOT NULL DEFAULT '';
ALTER TABLE note_shares ADD COLUMN IF NOT EXISTS permission TEXT NOT NULL DEFAULT 'write';
ALTER TABLE note_operations ADD COLUMN IF NOT EXISTS operation_type TEXT NOT NULL DEFAULT 'collab';
ALTER TABLE note_operations ALTER COLUMN session_id DROP NOT NULL;
ALTER TABLE note_attachments ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE home_note_sync_state ALTER COLUMN agent_id DROP NOT NULL;
ALTER TABLE home_note_sync_state DROP CONSTRAINT IF EXISTS home_note_sync_state_agent_id_fkey;
DO $$ BEGIN
	ALTER TABLE note_operations ADD CONSTRAINT note_operations_session_id_fkey FOREIGN KEY (session_id) REFERENCES app_sessions(id) ON DELETE SET NULL NOT VALID;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE home_note_sync_state ADD CONSTRAINT home_note_sync_state_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE SET NULL NOT VALID;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
ALTER TABLE assistant_file_index DROP CONSTRAINT IF EXISTS assistant_file_index_home_id_path_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_assistant_file_index_home_source_path ON assistant_file_index(home_id, service_profile_id, path);
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_home_source_updated ON assistant_file_index(home_id, service_profile_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_assistant_documents_search_fts ON assistant_documents USING GIN (to_tsvector('simple', search_text));
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_search_fts ON assistant_file_index USING GIN (to_tsvector('simple', search_text || ' ' || name || ' ' || path));
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE EXTENSION IF NOT EXISTS amcheck WITH SCHEMA pg_catalog;
CREATE TABLE IF NOT EXISTS cloud_runtime (
	deployment_id TEXT PRIMARY KEY CHECK (deployment_id = 'singleton'),
	runtime_id TEXT NOT NULL,
	version TEXT NOT NULL,
	started_at TIMESTAMPTZ NOT NULL,
	heartbeat_at TIMESTAMPTZ NOT NULL,
	shutdown_at TIMESTAMPTZ NULL
);
CREATE TABLE IF NOT EXISTS agent_connections (
	connection_id TEXT PRIMARY KEY,
	runtime_id TEXT NOT NULL,
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	capabilities_json TEXT NOT NULL DEFAULT '[]',
	connected_at TIMESTAMPTZ NOT NULL,
	heartbeat_at TIMESTAMPTZ NOT NULL,
	disconnected_at TIMESTAMPTZ NULL
);
CREATE TABLE IF NOT EXISTS app_connections (
	connection_id TEXT PRIMARY KEY,
	runtime_id TEXT NOT NULL,
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	connected_at TIMESTAMPTZ NOT NULL,
	heartbeat_at TIMESTAMPTZ NOT NULL,
	disconnected_at TIMESTAMPTZ NULL
);
CREATE TABLE IF NOT EXISTS relay_requests (
	request_id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	app_connection_id TEXT NOT NULL,
	agent_connection_id TEXT NULL,
	command TEXT NOT NULL,
	request_payload JSONB NOT NULL,
	response_payload JSONB NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'completed', 'failed', 'timed_out', 'cancelled')),
	error_code TEXT NOT NULL DEFAULT '',
	error_message TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	sent_at TIMESTAMPTZ NULL,
	completed_at TIMESTAMPTZ NULL,
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS app_ws_tickets (
	token_hash TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	consumed_at TIMESTAMPTZ NULL
);
CREATE TABLE IF NOT EXISTS file_transfers (
	id TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL UNIQUE,
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	agent_id TEXT NOT NULL DEFAULT '',
	operation TEXT NOT NULL CHECK (operation IN ('download', 'upload')),
	source_id TEXT NOT NULL DEFAULT '',
	path TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'completed', 'failed', 'expired')),
	bytes_total BIGINT NULL,
	bytes_done BIGINT NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	completed_at TIMESTAMPTZ NULL
);
ALTER TABLE file_transfers ADD COLUMN IF NOT EXISTS file_job_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS rate_limit_events (
	bucket TEXT NOT NULL,
	key_hash TEXT NOT NULL,
	occurred_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_limit_events_bucket_key_time ON rate_limit_events(bucket, key_hash, occurred_at DESC);
CREATE TABLE IF NOT EXISTS login_backoff (
	email_hash TEXT PRIMARY KEY,
	failures INTEGER NOT NULL,
	blocked_until TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS file_operation_jobs (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	user_id TEXT NULL REFERENCES users(id) ON DELETE SET NULL,
	operation TEXT NOT NULL CHECK (operation IN ('move', 'copy', 'delete', 'upload', 'download')),
	source_id TEXT NOT NULL,
	destination_source_id TEXT NOT NULL DEFAULT '',
	from_path TEXT NOT NULL,
	to_path TEXT NOT NULL DEFAULT '',
	is_directory BOOLEAN NOT NULL DEFAULT false,
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled', 'rollback_required', 'rolled_back')),
	bytes_total BIGINT NOT NULL DEFAULT 0,
	bytes_done BIGINT NOT NULL DEFAULT 0,
	files_total BIGINT NOT NULL DEFAULT 0,
	files_done BIGINT NOT NULL DEFAULT 0,
	error_message TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	completed_at TIMESTAMPTZ NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
	id TEXT PRIMARY KEY,
	occurred_at TIMESTAMPTZ NOT NULL,
	actor_user_id TEXT NULL REFERENCES users(id) ON DELETE SET NULL,
	actor_agent_id TEXT NULL REFERENCES agents(id) ON DELETE SET NULL,
	home_id TEXT NULL REFERENCES homes(id) ON DELETE SET NULL,
	event_type TEXT NOT NULL,
	severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
	request_id TEXT NOT NULL DEFAULT '',
	ip_hash TEXT NOT NULL DEFAULT '',
	target_type TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	metadata_json JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_audit_events_home_time ON audit_events(home_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_type_time ON audit_events(event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_relay_requests_home_status_expires ON relay_requests(home_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_agent_connections_runtime ON agent_connections(runtime_id, disconnected_at);
CREATE INDEX IF NOT EXISTS idx_app_connections_runtime ON app_connections(runtime_id, disconnected_at);
CREATE INDEX IF NOT EXISTS idx_file_transfers_status_expires ON file_transfers(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_file_transfers_job_id ON file_transfers(file_job_id) WHERE file_job_id <> '';
CREATE INDEX IF NOT EXISTS idx_file_operation_jobs_home_status ON file_operation_jobs(home_id, status, updated_at DESC);
DO $$ BEGIN
	ALTER TABLE home_memberships ADD CONSTRAINT home_memberships_role_check CHECK (role IN ('admin', 'member'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE home_invitations ADD CONSTRAINT home_invitations_role_check CHECK (role IN ('admin', 'member'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE agents ADD CONSTRAINT agents_status_check CHECK (status IN ('online', 'offline'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE home_service_profiles ADD CONSTRAINT home_service_profiles_service_type_check CHECK (service_type IN ('homeassistant', 'smb'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
ALTER TABLE user_notes DROP CONSTRAINT IF EXISTS user_notes_page_type_check;
UPDATE user_notes SET page_type = 'kanban' WHERE page_type = 'board';
ALTER TABLE user_notes ADD CONSTRAINT user_notes_page_type_check CHECK (page_type IN ('text', 'kanban'));
DO $$ BEGIN
	ALTER TABLE note_shares ADD CONSTRAINT note_shares_permission_check CHECK (permission IN ('read', 'write'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE note_operations ADD CONSTRAINT note_operations_operation_type_check CHECK (operation_type IN ('save', 'rename', 'delete', 'collab'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE note_attachments ADD CONSTRAINT note_attachments_status_check CHECK (status IN ('pending', 'ready', 'deleted'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE assistant_messages ADD CONSTRAINT assistant_messages_role_check CHECK (role IN ('user', 'assistant'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE assistant_runs ADD CONSTRAINT assistant_runs_state_check CHECK (state IN ('completed', 'waiting_client_tool', 'waiting_confirmation'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
	ALTER TABLE openai_accounts ADD CONSTRAINT openai_accounts_auth_provider_check CHECK (auth_provider IN ('', 'openai', 'chatgpt', 'chatgpt_codex'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
UPDATE home_memberships SET role = 'admin' WHERE role = 'owner';
