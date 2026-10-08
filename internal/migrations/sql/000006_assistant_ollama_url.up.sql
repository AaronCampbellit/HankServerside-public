ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS ollama_base_url TEXT NOT NULL DEFAULT '';
