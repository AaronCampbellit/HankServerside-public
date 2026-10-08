DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
		EXECUTE 'CREATE EXTENSION IF NOT EXISTS vector';
		ALTER TABLE assistant_chunks ADD COLUMN IF NOT EXISTS embedding vector(768) NULL;
		ALTER TABLE assistant_file_index ADD COLUMN IF NOT EXISTS embedding vector(768) NULL;
	END IF;
END $$;
