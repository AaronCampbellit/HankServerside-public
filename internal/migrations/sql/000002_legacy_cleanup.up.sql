ALTER TABLE user_notes DROP CONSTRAINT IF EXISTS user_notes_page_type_check;
UPDATE user_notes SET page_type = 'kanban' WHERE page_type = 'board';
ALTER TABLE user_notes ADD CONSTRAINT user_notes_page_type_check CHECK (page_type IN ('text', 'kanban'));
CREATE TABLE IF NOT EXISTS legacy_home_notes_archive (
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
	archived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY(home_id, note_id)
);
DO $$ BEGIN
	IF to_regclass('public.home_notes') IS NOT NULL THEN
		INSERT INTO legacy_home_notes_archive (
			home_id, note_id, title, content, page_type, board_json, revision, checksum,
			deleted_at, updated_at, updated_by
		)
		SELECT home_id, note_id, title, content, page_type, board_json, revision, checksum,
			deleted_at, updated_at, updated_by
		FROM home_notes
		ON CONFLICT(home_id, note_id) DO UPDATE SET
			title = excluded.title,
			content = excluded.content,
			page_type = excluded.page_type,
			board_json = excluded.board_json,
			revision = excluded.revision,
			checksum = excluded.checksum,
			deleted_at = excluded.deleted_at,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by,
			archived_at = now();
	END IF;
END $$;
DROP TABLE IF EXISTS home_notes;
DO $$ BEGIN
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'user_notes' AND column_name = 'content'
	) THEN
		UPDATE user_notes SET body_markdown = content WHERE body_markdown = '' AND content <> '';
	END IF;
END $$;
ALTER TABLE user_notes DROP COLUMN IF EXISTS content;
DROP TABLE IF EXISTS openai_oauth_states;
CREATE TABLE IF NOT EXISTS legacy_openai_accounts_archive (
	user_id TEXT PRIMARY KEY,
	provider_user_id TEXT NOT NULL DEFAULT '',
	auth_provider TEXT NOT NULL DEFAULT '',
	chatgpt_plan_type TEXT NOT NULL DEFAULT '',
	token_type TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO legacy_openai_accounts_archive (
	user_id, provider_user_id, auth_provider, chatgpt_plan_type, token_type, scope,
	expires_at, created_at, updated_at
)
SELECT user_id, provider_user_id, auth_provider, chatgpt_plan_type, token_type, scope,
	expires_at, created_at, updated_at
FROM openai_accounts
WHERE auth_provider <> 'chatgpt_codex'
ON CONFLICT(user_id) DO UPDATE SET
	provider_user_id = excluded.provider_user_id,
	auth_provider = excluded.auth_provider,
	chatgpt_plan_type = excluded.chatgpt_plan_type,
	token_type = excluded.token_type,
	scope = excluded.scope,
	expires_at = excluded.expires_at,
	updated_at = excluded.updated_at,
	archived_at = now();
DELETE FROM openai_accounts WHERE auth_provider <> 'chatgpt_codex';
ALTER TABLE openai_accounts DROP CONSTRAINT IF EXISTS openai_accounts_auth_provider_check;
ALTER TABLE openai_accounts ALTER COLUMN auth_provider SET DEFAULT 'chatgpt_codex';
ALTER TABLE openai_accounts ADD CONSTRAINT openai_accounts_auth_provider_check CHECK (auth_provider IN ('chatgpt_codex'));
