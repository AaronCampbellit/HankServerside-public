-- Immutable bindings for server-owned execution attachments. Existing v1
-- attachment metadata is unchanged; client-held bytes are not durable stages.
CREATE TABLE assistant_staged_attachments (
 id TEXT PRIMARY KEY,
 home_id TEXT NOT NULL REFERENCES homes(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 session_id TEXT NOT NULL,
 client_attachment_id TEXT NOT NULL CHECK(length(client_attachment_id) BETWEEN 1 AND 128),
 storage_key TEXT NOT NULL UNIQUE CHECK(storage_key ~ '^[a-f0-9]{64}$'),
 filename TEXT NOT NULL CHECK(length(filename) BETWEEN 1 AND 255),
 content_type TEXT NOT NULL CHECK(length(content_type) BETWEEN 1 AND 255),
 size_bytes BIGINT NOT NULL CHECK(size_bytes BETWEEN 1 AND 104857600),
 checksum_sha256 TEXT NOT NULL CHECK(checksum_sha256 ~ '^[a-f0-9]{64}$'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 UNIQUE(session_id,client_attachment_id),
 FOREIGN KEY(session_id,home_id,user_id) REFERENCES assistant_sessions(id,home_id,user_id) ON DELETE CASCADE,
 CHECK(expires_at>created_at)
);
CREATE INDEX assistant_staged_attachments_expiry_idx ON assistant_staged_attachments(expires_at);
CREATE INDEX assistant_staged_attachments_scope_idx ON assistant_staged_attachments(home_id,user_id);
