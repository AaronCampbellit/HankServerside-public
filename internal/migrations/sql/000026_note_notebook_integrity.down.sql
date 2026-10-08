DROP TRIGGER IF EXISTS user_notes_protect_notebook_parent ON user_notes;
DROP FUNCTION IF EXISTS protect_user_note_notebook_parent();
DROP TRIGGER IF EXISTS user_notes_validate_parent ON user_notes;
DROP FUNCTION IF EXISTS validate_user_note_parent();
ALTER TABLE user_notes DROP CONSTRAINT IF EXISTS user_notes_owner_parent_fkey;
