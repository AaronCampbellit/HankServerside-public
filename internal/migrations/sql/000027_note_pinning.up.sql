ALTER TABLE user_notes
	ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE user_notes
	DROP CONSTRAINT IF EXISTS user_notes_pinned_child_check;

ALTER TABLE user_notes
	ADD CONSTRAINT user_notes_pinned_child_check
	CHECK (NOT pinned OR (parent_id IS NOT NULL AND page_type <> 'notebook'));

CREATE INDEX IF NOT EXISTS idx_user_notes_owner_parent_pinned_updated
	ON user_notes(owner_user_id, parent_id, pinned DESC, updated_at DESC);
