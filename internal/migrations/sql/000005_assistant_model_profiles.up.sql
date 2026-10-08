ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS ai_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS embedding_model TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_settings ADD COLUMN IF NOT EXISTS prompt_profile TEXT NOT NULL DEFAULT '';
