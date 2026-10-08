ALTER TABLE note_attachments ADD COLUMN IF NOT EXISTS preview_storage_key TEXT;
ALTER TABLE note_attachments ADD COLUMN IF NOT EXISTS preview_content_type TEXT;
ALTER TABLE note_attachments ADD COLUMN IF NOT EXISTS preview_size_bytes BIGINT;
ALTER TABLE note_attachments ADD COLUMN IF NOT EXISTS preview_checksum_sha256 TEXT;
ALTER TABLE note_attachments DROP CONSTRAINT IF EXISTS note_attachments_preview_metadata_check;
ALTER TABLE note_attachments ADD CONSTRAINT note_attachments_preview_metadata_check CHECK (
    (preview_storage_key IS NULL AND preview_content_type IS NULL AND preview_size_bytes IS NULL AND preview_checksum_sha256 IS NULL)
    OR
    (preview_storage_key IS NOT NULL AND preview_content_type IS NOT NULL AND preview_size_bytes > 0 AND preview_checksum_sha256 IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS mcp_note_attachment_uploads (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    note_record_id TEXT NOT NULL REFERENCES user_notes(id) ON DELETE CASCADE,
    target_note_id TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('note', 'kanban_card')),
    board_id TEXT NOT NULL DEFAULT '',
    card_id TEXT NOT NULL DEFAULT '',
    replacement_attachment_id TEXT REFERENCES note_attachments(id) ON DELETE SET NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    declared_size_bytes BIGINT CHECK (declared_size_bytes IS NULL OR (declared_size_bytes > 0 AND declared_size_bytes <= 104857600)),
    declared_checksum_sha256 TEXT NOT NULL DEFAULT '',
    received_bytes BIGINT NOT NULL DEFAULT 0 CHECK (received_bytes >= 0 AND received_bytes <= 104857600),
    staging_key TEXT NOT NULL UNIQUE,
    expected_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('open', 'completed', 'aborted', 'failed', 'expired')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK (declared_size_bytes IS NULL OR received_bytes <= declared_size_bytes),
    CHECK (
        (target_kind = 'note' AND board_id = '' AND card_id = '')
        OR
        (target_kind = 'kanban_card' AND board_id <> '' AND card_id <> '')
    ),
    CHECK (expires_at > created_at),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS mcp_note_attachment_uploads_owner_status_expiry_idx
    ON mcp_note_attachment_uploads(owner_user_id, status, expires_at);
CREATE INDEX IF NOT EXISTS mcp_note_attachment_uploads_expiry_idx
    ON mcp_note_attachment_uploads(expires_at) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS note_attachments_preview_storage_key_idx
    ON note_attachments(preview_storage_key) WHERE preview_storage_key IS NOT NULL;
