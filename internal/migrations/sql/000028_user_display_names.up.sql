ALTER TABLE users
	ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';

ALTER TABLE users
	ADD CONSTRAINT users_display_name_length_check
	CHECK (char_length(display_name) <= 80);

ALTER TABLE users
	ADD CONSTRAINT users_display_name_trimmed_check
	CHECK (display_name = btrim(display_name));
