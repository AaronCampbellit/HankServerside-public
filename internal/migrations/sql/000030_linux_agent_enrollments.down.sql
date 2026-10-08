DROP INDEX IF EXISTS agent_tokens_one_pending_replacement_idx;
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS rotation_requested_at;
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS confirmed_at;
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS activated_at;
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS replaces_token_id;
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS generation;
DROP TABLE IF EXISTS agent_enrollments;
