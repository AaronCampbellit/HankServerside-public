ALTER TABLE user_notes DROP CONSTRAINT IF EXISTS user_notes_page_type_check;
ALTER TABLE user_notes ADD CONSTRAINT user_notes_page_type_check CHECK (page_type IN ('text', 'kanban', 'notebook'));
