#!/usr/bin/env bash
set -euo pipefail

env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
run_id="${HANK_SCALE_RUN_ID:-scale-fixture}"
file_rows="${HANK_SCALE_FILE_INDEX_ROWS:-1000000}"
note_rows="${HANK_SCALE_NOTE_ROWS:-100000}"
attachment_bytes="${HANK_SCALE_ATTACHMENT_BYTES:-10737418240}"
report_dir="${HANK_SCALE_REPORT_DIR:-./data/scale-validation}"

compose() {
	if [ -n "${COMPOSE_PROJECT_NAME:-}" ]; then
		docker compose --env-file "$env_file" -p "$COMPOSE_PROJECT_NAME" "$@"
	else
		docker compose --env-file "$env_file" "$@"
	fi
}

psql_live() {
	compose exec -T postgres sh -ceu 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"' sh "$@"
}

mkdir -p "$report_dir"
report="$report_dir/scale-validation-${run_id}.txt"
plan_file_index="$report_dir/file-index-plan-${run_id}.txt"
plan_notes="$report_dir/notes-plan-${run_id}.txt"

if ! compose ps --services --status running 2>/dev/null | grep -qx postgres; then
	echo "postgres service is not running in the compose project" >&2
	exit 1
fi
if ! compose ps --services --status running 2>/dev/null | grep -qx cloud; then
	echo "cloud service is not running in the compose project" >&2
	exit 1
fi

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

psql_live \
	-v run_id="$run_id" \
	-v file_rows="$file_rows" \
	-v note_rows="$note_rows" <<'SQL'
DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM homes LIMIT 1) THEN
		RAISE EXCEPTION 'scale validation requires at least one home';
	END IF;
	IF NOT EXISTS (SELECT 1 FROM users LIMIT 1) THEN
		RAISE EXCEPTION 'scale validation requires at least one user';
	END IF;
END $$;

DELETE FROM note_attachments
WHERE id LIKE 'scale_attachment_%'
	AND id <> 'scale_attachment_' || :'run_id';

DELETE FROM user_notes
WHERE id LIKE 'scale_note_%'
	AND id NOT LIKE 'scale_note_' || :'run_id' || '_%';

DELETE FROM assistant_file_index
WHERE service_profile_id = 'synthetic-scale'
	AND path NOT LIKE '/_hank_scale/' || :'run_id' || '/files/%';

WITH selected AS (
	SELECT h.id AS home_id, h.user_id
	FROM homes h
	ORDER BY h.created_at ASC
	LIMIT 1
)
INSERT INTO assistant_file_index (
	id, home_id, service_profile_id, path, name, is_directory, size_bytes, modified_at,
	search_text, metadata_json, embedding_json, embedding_model, embedding_version, updated_at
)
SELECT
	'scale_file_' || :'run_id' || '_' || gs::text,
	selected.home_id,
	'synthetic-scale',
	'/_hank_scale/' || :'run_id' || '/files/file-' || lpad(gs::text, 7, '0') || '.txt',
	'file-' || lpad(gs::text, 7, '0') || '.txt',
	FALSE,
	128,
	now(),
	'hankscale synthetic file index validation ' || :'run_id' || ' row ' || gs::text,
	'{"synthetic":true,"validator":"scale-validation"}'::jsonb,
	'',
	'',
	'',
	now()
FROM selected, generate_series(1, :file_rows::int) AS gs
ON CONFLICT(home_id, service_profile_id, path) DO NOTHING;

WITH selected AS (
	SELECT h.id AS home_id, h.user_id
	FROM homes h
	ORDER BY h.created_at ASC
	LIMIT 1
)
INSERT INTO user_notes (
	id, note_id, owner_user_id, home_id, parent_id, sort_order, title, content,
	body_markdown, body_format, page_type, board_json, revision, checksum, crdt_state_json,
	collab_version, deleted_at, created_at, updated_at, updated_by
)
SELECT
	'scale_note_' || :'run_id' || '_' || gs::text,
	'_hank_scale/' || :'run_id' || '/note-' || lpad(gs::text, 6, '0') || '.md',
	selected.user_id,
	selected.home_id,
	NULL,
	gs,
	'Synthetic scale note ' || gs::text,
	'# Synthetic scale note ' || gs::text || E'\n\nhankscale note validation ' || :'run_id',
	'# Synthetic scale note ' || gs::text || E'\n\nhankscale note validation ' || :'run_id',
	'markdown',
	'text',
	'',
	'scale-' || :'run_id' || '-' || gs::text,
	'scale-' || gs::text,
	'',
	0,
	NULL,
	now(),
	now(),
	selected.user_id
FROM selected, generate_series(1, :note_rows::int) AS gs
ON CONFLICT(id) DO NOTHING;

ANALYZE assistant_file_index;
ANALYZE user_notes;
SQL

note_id="$(psql_live -v run_id="$run_id" -At <<'SQL'
SELECT id
FROM user_notes
WHERE id = 'scale_note_' || :'run_id' || '_1'
LIMIT 1;
SQL
)"
if [ -z "$note_id" ]; then
	echo "failed to locate synthetic note for attachment validation" >&2
	exit 1
fi
storage_key="${note_id}/scale-attachment-${run_id}.bin"

actual_attachment_bytes="$(compose exec -T \
	-e HANK_SCALE_NOTE_ID="$note_id" \
	-e HANK_SCALE_STORAGE_KEY="$storage_key" \
	-e HANK_SCALE_ATTACHMENT_BYTES="$attachment_bytes" \
	cloud sh -ceu '
		root="${HANK_NOTE_ATTACHMENTS_DIR:-/var/lib/hank/note-attachments}"
		find "$root" -mindepth 1 -maxdepth 1 -type d -name "scale_note_scale-*" ! -name "$HANK_SCALE_NOTE_ID" -exec rm -rf {} +
		target="${root}/${HANK_SCALE_STORAGE_KEY}"
		mkdir -p "$(dirname "$target")"
		truncate -s "$HANK_SCALE_ATTACHMENT_BYTES" "$target"
		stat -c %s "$target"
	')"

psql_live \
	-v run_id="$run_id" \
	-v note_id="$note_id" \
	-v storage_key="$storage_key" \
	-v attachment_bytes="$actual_attachment_bytes" <<'SQL'
WITH selected AS (
	SELECT home_id, owner_user_id
	FROM user_notes
	WHERE id = :'note_id'
	LIMIT 1
)
INSERT INTO note_attachments (
	id, note_id, home_id, owner_user_id, filename, content_type, size_bytes,
	checksum_sha256, storage_key, deleted_at, created_at, updated_at
)
SELECT
	'scale_attachment_' || :'run_id',
	:'note_id',
	selected.home_id,
	selected.owner_user_id,
	'scale-attachment.bin',
	'application/octet-stream',
	:attachment_bytes::bigint,
	'synthetic-sparse-file',
	:'storage_key',
	NULL,
	now(),
	now()
FROM selected
ON CONFLICT(id) DO UPDATE SET
	size_bytes = excluded.size_bytes,
	storage_key = excluded.storage_key,
	updated_at = excluded.updated_at;
SQL

psql_live -At -v run_id="$run_id" <<'SQL' >"$plan_file_index"
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, path
FROM assistant_file_index
WHERE home_id = (SELECT id FROM homes ORDER BY created_at ASC LIMIT 1)
	AND service_profile_id = 'synthetic-scale'
	AND path = '/_hank_scale/' || :'run_id' || '/files/file-0000001.txt';
SQL

psql_live -At -v run_id="$run_id" <<'SQL' >"$plan_notes"
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, title
FROM user_notes
WHERE owner_user_id = (SELECT user_id FROM homes ORDER BY created_at ASC LIMIT 1)
	AND parent_id IS NULL
ORDER BY sort_order ASC
LIMIT 50;
SQL

if ! grep -Eq 'Index Scan|Bitmap Index Scan' "$plan_file_index"; then
	echo "file-index query did not use an index; see $plan_file_index" >&2
	exit 1
fi
if ! grep -Eq 'idx_user_notes_owner_parent_order|Index Scan|Bitmap Index Scan' "$plan_notes"; then
	echo "notes query did not use an index; see $plan_notes" >&2
	exit 1
fi

counts="$(psql_live -At -v run_id="$run_id" <<'SQL'
SELECT 'assistant_file_index_synthetic=' || count(*)
FROM assistant_file_index
WHERE service_profile_id = 'synthetic-scale' AND path LIKE '/_hank_scale/' || :'run_id' || '/files/%';
SELECT 'user_notes_synthetic=' || count(*)
FROM user_notes
WHERE id LIKE 'scale_note_' || :'run_id' || '_%';
SELECT 'note_attachments_synthetic=' || count(*) || ' bytes=' || coalesce(sum(size_bytes), 0)
FROM note_attachments
WHERE id = 'scale_attachment_' || :'run_id';
SQL
)"

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
	echo "started_at=$started_at"
	echo "finished_at=$finished_at"
	echo "run_id=$run_id"
	echo "target_file_index_rows=$file_rows"
	echo "target_note_rows=$note_rows"
	echo "target_attachment_bytes=$attachment_bytes"
	echo "$counts"
	echo "file_index_plan=$plan_file_index"
	echo "notes_plan=$plan_notes"
	echo "status=pass"
} >"$report"

cat "$report"
