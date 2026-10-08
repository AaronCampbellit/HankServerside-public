# Hank Notes And Kanban

This document describes the HTTP notes API for external applications. The same
routes are used by Hank clients and dashboard surfaces, so external apps do not
need a separate protocol bridge or direct access to local note storage.

Kanban cards store their title/Markdown details together with optional due
date, tags, priority (`low`, `medium`, or `high`), color, and immutable creation
time. The dashboard detail modal edits this metadata and board cards show its
priority, tags, and due date. Dashboard search matches card text and metadata;
the MCP Kanban tools expose and filter the same fields.

## Authentication

External applications should use dedicated Notes API tokens, not a normal Hank
login session. A Hank admin creates one token per external app, chooses the
least-privilege scopes it needs, and can revoke that token without signing out
Hank clients.

Create a token with an admin session:

```http
POST /v1/home/notes-api-tokens
Authorization: Bearer <admin-session-token>
Content-Type: application/json
```

```json
{
  "name": "Obsidian import",
  "scopes": ["notes:read", "notes:append"],
  "allow_home_notes": false
}
```

The response includes the raw `hank_note_...` token exactly once:

```json
{
  "token": "hank_note_...",
  "api_token": {
    "id": "noteapi_...",
    "name": "Obsidian import",
    "scopes": ["notes:read", "notes:append"],
    "allow_home_notes": false,
    "expires_at": "2026-09-06T12:00:00Z"
  }
}
```

Store the raw token in the external app secret store and send it as a bearer
token:

```http
Authorization: Bearer hank_note_...
Content-Type: application/json
```

Notes API tokens are only accepted by `/v1/me/notes...` and
`/v1/home/notes...`. They do not grant access to files, assistant, dashboard,
admin, or general Home routes.

Normal Hank clients can still use `POST /v1/auth/login` and send the returned
`session_token` as a bearer token. Browser cookie writes still require the
normal CSRF cookie/header pair. Bearer token requests do not require CSRF.

## Token Management

Token management routes require a normal admin session. Notes API tokens cannot
manage other tokens.

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/v1/home/notes-api-tokens` | List tokens, scopes, revocation state, and usage metadata. |
| `POST` | `/v1/home/notes-api-tokens` | Create a token. The raw token is returned only in this response. |
| `DELETE` | `/v1/home/notes-api-tokens/{tokenID}` | Revoke a token. Revoked tokens authenticate as `401`. |

Supported scopes:

| Scope | Allows |
| --- | --- |
| `notes:read` | List, fetch, search, tags, and tag rollups. |
| `notes:append` | Append text to existing text notes. |
| `notes:write` | Create or replace notes. This also allows append. |
| `notes:delete` | Delete notes. |

Security behavior:

- Tokens are stored as SHA-256 hashes; raw token values are not persisted.
- Tokens expire after 90 days by default unless `expires_at` is supplied.
- Profile notes are allowed by default. Shared Home notes require
  `allow_home_notes: true` and the user's Home notes permission.
- Share management and attachment routes require a normal Hank session.
- Each token use updates `last_used_at`, `last_used_route`,
  `last_used_ip_hash`, `last_used_user_agent_hash`, and `request_count`.
- Create, revoke, and denied-scope events are written to the Home audit log
  without recording raw token values.

## Note Scopes

Hank has two note scopes:

- Profile notes: personal notes owned by the signed-in user, under
  `/v1/me/notes`.
- Shared Home notes: notes visible through the singleton Home, under
  `/v1/home/notes`.

Shared Home notes require Home membership and the `notes` Home permission.
Profile notes are private to the signed-in user unless shared into the Home.

## Profile Note Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/v1/me/notes` | List profile notes. |
| `POST` | `/v1/me/notes` | Create a profile note. |
| `GET` | `/v1/me/notes/search?q=<query>&limit=<n>` | Search profile notes and notebooks. |
| `GET` | `/v1/me/notes/search?q=<query>&notebook_id=<noteID>` | Search profile notes within one notebook. |
| `GET` | `/v1/me/notes/tags` | List profile note tag counts. |
| `GET` | `/v1/me/notes/tag-rollup?tag=<tag>` | List tagged lines across profile notes. |
| `GET` | `/v1/me/notes/{noteID}` | Fetch one profile note. |
| `PUT` | `/v1/me/notes/{noteID}` | Create or replace one profile note. |
| `POST` | `/v1/me/notes/{noteID}/append` | Append text to one profile note. |
| `DELETE` | `/v1/me/notes/{noteID}` | Delete one profile note. |

## Shared Home Note Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/v1/home/notes` | List visible shared Home notes. |
| `GET` | `/v1/home/notes/search?q=<query>&limit=<n>` | Search visible shared Home notes and notebooks. |
| `GET` | `/v1/home/notes/search?q=<query>&notebook_id=<noteID>` | Search visible shared Home notes within one notebook. |
| `GET` | `/v1/home/notes/tags` | List visible shared Home note tag counts. |
| `GET` | `/v1/home/notes/tag-rollup?tag=<tag>` | List tagged lines across visible shared Home notes. |
| `GET` | `/v1/home/notes/{noteID}` | Fetch one visible shared Home note. |
| `PUT` | `/v1/home/notes/{noteID}` | Create or replace one shared Home note. |
| `POST` | `/v1/home/notes/{noteID}/append` | Append text to one shared Home note. |
| `DELETE` | `/v1/home/notes/{noteID}` | Delete one shared Home note. |
| `GET` | `/v1/home/notes/{noteID}/shares` | List note shares. |
| `POST` | `/v1/home/notes/{noteID}/shares` | Share a note with a Home member. |
| `DELETE` | `/v1/home/notes/{noteID}/shares/{userID}` | Revoke one share. |

## Save Payload

Use this payload with `POST /v1/me/notes` and `PUT` note routes:

```json
{
  "note_id": "project-plan.md",
  "title": "Project Plan",
  "body_markdown": "# Project Plan\n\nInitial notes.",
  "body_format": "markdown",
  "expected_revision": "optional-current-revision",
  "page_type": "text",
  "parent_id": "projects",
  "pinned": true
}
```

Fields:

- `note_id`: optional on profile-note create; required in the route for `PUT`.
- `title`: optional when the body can provide a title fallback.
- `content`: compatibility alias for `body_markdown`.
- `body_markdown`: canonical note body for text notes.
- `body_format`: defaults to `markdown`.
- `expected_revision`: optional optimistic concurrency token.
- `page_type`: `text`, `kanban`, or `notebook`; append is only supported for `text`.
- `board`: kanban board payload when `page_type` is `kanban`.
- `parent_id` and `sort_order`: optional hierarchy and ordering metadata. To place a note in a notebook, set `parent_id` to a note whose `page_type` is `notebook`. Send an empty `parent_id` to move it back out.
- `pinned`: optional synced notebook pin. Only child notes can be pinned; root notes and notebooks are always stored as unpinned. Omission on update preserves the existing value for older clients. List and fetch responses return the current value.
- `mcp_excluded`: optional MCP privacy marker. Omission on update preserves the existing value for older clients. MCP excludes marked notes and every child of a marked notebook; normal Notes APIs are unchanged.

Successful saves return:

```json
{
  "note_id": "project-plan.md",
  "revision": "new-revision",
  "updated_at": "2026-06-08T12:00:00Z",
  "page_type": "text"
}
```

## Append Payload

Use append when an external app needs to add text without replacing the whole
note:

```json
{
  "body_markdown": "- [ ] Follow up with Aaron",
  "expected_revision": "optional-current-revision"
}
```

By default, Hank inserts one newline between existing content and appended
content. To choose a different separator, pass `separator`:

```json
{
  "content": "single-line addition",
  "separator": "\n\n"
}
```

Append returns the same response shape as save. If the note is not a text note,
the route returns `409` with `error: note_append_unsupported`.

## Search And Tags

Search routes accept:

- `q` or `query`: search text.
- `notebook_id` or `parent_id`: optional notebook note ID to search within.
- `limit`: optional result limit. Invalid negative or non-numeric values return
  `400`; values above `200` are capped to `200`. The default search limit is
  `50`.

Normal search includes notebook titles. Scoped notebook search returns notes
whose `parent_id` matches the notebook ID.

Search response:

```json
{
  "results": [
    {
      "note_id": "project-plan.md",
      "title": "Project Plan",
      "page_type": "text",
      "parent_id": "projects",
      "preview": "matching text around the search hit",
      "match_location": 12,
      "line_index": 1
    }
  ]
}
```

Tags are extracted from text notes using `#tag` style tokens.

## Conflict Handling

When `expected_revision` does not match the current note revision, Hank returns
`409`:

```json
{
  "error": "note_conflict",
  "current": {
    "note_id": "project-plan.md",
    "title": "Project Plan",
    "body_markdown": "current body",
    "revision": "current-revision"
  }
}
```

External apps should fetch the current note, merge or reapply their intended
change, then retry with the new revision.

Profile-note deletion supports the same optimistic-concurrency boundary:

```text
DELETE /v1/me/notes/{noteID}
X-Hank-Expected-Revision: <opaque revision>
```

The `X-Hank-Expected-Revision` header is optional. Omitting it preserves the
existing unconditional deletion behavior for older clients. When supplied,
Hank compares the opaque revision before atomically moving the owned note to Trash. A
mismatch returns `409` with `error: note_conflict` and the current note in the
same shape shown above.

The dashboard cancels queued saves and waits for an active save before deleting
a note. After deletion, it opens the note’s notebook, or the notes browser if
the note has no notebook. Failed deletions keep the editor and its edits open.

Profile-note Trash preserves the note title, body, page type, Kanban board,
attachments, and notebook metadata. Trash is owner-scoped and exposed through:

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/v1/me/notes/trash` | List the authenticated user's deleted notes. |
| `POST` | `/v1/me/notes/{noteID}/restore` | Restore a deleted note. If its notebook is unavailable, restore it at the root. |
| `DELETE` | `/v1/me/notes/{noteID}/permanent` | Permanently delete a trashed note and clean up its attachment files. |

Hank does not automatically purge Trash. Permanent deletion remains an
explicit, confirmed user action; this avoids silently removing production
note data.

## Curl Examples

Create a profile note:

```bash
curl -sS "$HANK_URL/v1/me/notes" \
  -H "Authorization: Bearer $HANK_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"note_id":"external.md","title":"External","body_markdown":"hello from another app"}'
```

Append to the note:

```bash
curl -sS "$HANK_URL/v1/me/notes/external.md/append" \
  -H "Authorization: Bearer $HANK_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"body_markdown":"second line"}'
```

Search shared Home notes:

```bash
curl -sS "$HANK_URL/v1/home/notes/search?q=receipt&limit=10" \
  -H "Authorization: Bearer $HANK_TOKEN"
```

List and revoke external-app tokens:

```bash
curl -sS "$HANK_URL/v1/home/notes-api-tokens" \
  -H "Authorization: Bearer $HANK_ADMIN_TOKEN"

curl -sS -X DELETE "$HANK_URL/v1/home/notes-api-tokens/$TOKEN_ID" \
  -H "Authorization: Bearer $HANK_ADMIN_TOKEN"
```
