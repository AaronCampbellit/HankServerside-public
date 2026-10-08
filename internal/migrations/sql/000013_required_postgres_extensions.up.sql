CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE EXTENSION IF NOT EXISTS pg_buffercache;
CREATE EXTENSION IF NOT EXISTS amcheck WITH SCHEMA pg_catalog;

ALTER TABLE assistant_chunks ADD COLUMN IF NOT EXISTS embedding vector(768) NULL;
ALTER TABLE assistant_file_index ADD COLUMN IF NOT EXISTS embedding vector(768) NULL;

CREATE INDEX IF NOT EXISTS idx_user_notes_title_trgm ON user_notes USING GIN (title gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_user_notes_body_trgm ON user_notes USING GIN (body_markdown gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_assistant_documents_search_trgm ON assistant_documents USING GIN (search_text gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_name_trgm ON assistant_file_index USING GIN (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_path_trgm ON assistant_file_index USING GIN (path gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_assistant_chunks_embedding_hnsw ON assistant_chunks USING hnsw (embedding vector_cosine_ops) WHERE embedding IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_assistant_file_index_embedding_hnsw ON assistant_file_index USING hnsw (embedding vector_cosine_ops) WHERE embedding IS NOT NULL;
