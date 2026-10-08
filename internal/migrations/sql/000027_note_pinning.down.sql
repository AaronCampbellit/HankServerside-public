DROP INDEX IF EXISTS idx_user_notes_owner_parent_pinned_updated;

ALTER TABLE user_notes
	DROP CONSTRAINT IF EXISTS user_notes_pinned_child_check;

ALTER TABLE user_notes
	DROP COLUMN IF EXISTS pinned;
