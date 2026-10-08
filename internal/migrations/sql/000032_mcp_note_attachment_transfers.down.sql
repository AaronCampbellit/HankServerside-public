DROP TABLE IF EXISTS mcp_note_attachment_uploads;
DROP INDEX IF EXISTS note_attachments_preview_storage_key_idx;
ALTER TABLE note_attachments DROP CONSTRAINT IF EXISTS note_attachments_preview_metadata_check;
ALTER TABLE note_attachments DROP COLUMN IF EXISTS preview_checksum_sha256;
ALTER TABLE note_attachments DROP COLUMN IF EXISTS preview_size_bytes;
ALTER TABLE note_attachments DROP COLUMN IF EXISTS preview_content_type;
ALTER TABLE note_attachments DROP COLUMN IF EXISTS preview_storage_key;
