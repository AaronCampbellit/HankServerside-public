# Search

Hank search in the dashboard top bar returns navigation pages, the current
user's profile notes and Kanban cards, machines, quick links, permitted apps
and members, file names and paths from every connected Hank Agent, and live
Home Assistant entities. The file-server search bar searches only the selected
agent and source. A result for a file carries its agent, source, and path so
opening it cannot silently switch to another share with the same path.

## File catalog

Each connected Hank Agent sends a credential-free list of enabled file sources
to HankServerside. A background worker reads one directory at a time through
the existing outbound agent connection and stores permitted file metadata in
PostgreSQL. Large directory listings are transferred in bounded pages from a
short-lived agent snapshot, so a single large folder cannot exceed the
WebSocket frame limit. The queue survives cloud and agent restarts. Root folders are
scanned first, then unseen descendants, then previously scanned directories
due for reconciliation. There is no share-wide item or directory limit. Git
metadata directories are excluded from traversal. The worker stores file
names, paths, type, size, and modification time; it does not read file contents.
The agent sends a token-keyed, opaque revision for each source. Changing its
root, share target, or credentials clears that source's derived catalog and
starts a fresh scan without sending the underlying configuration to the cloud.

One complete directory listing is published in a transaction. A failed
listing retains older results and is retried after five minutes. Successfully
scanned directories are due again after 30 minutes; successful Hank file
commands and uploads schedule their affected parent folders immediately.
Changes made outside Hank appear after periodic reconciliation. Search remains
available while a source is offline, but results from that source are not
returned until its agent reconnects and confirms its current source policy.

The `file_search_sources`, `file_search_directories`, and `file_search_items`
tables are created by migration 44; migration 45 adds the source revision.
Removing a source removes only its derived
catalog rows. The down migration discards this derived catalog and requires a
fresh scan after rollback. It does not delete original files or assistant
index data.

## Queries and status

`GET /v1/home/file-search` requires authentication, Home membership, and the
Files feature. It accepts `q`, `agent_id`, and `source_id`; omitting agent and
source chooses the primary agent and its default source. Results include
`status`: `indexing` while unscanned directories remain, `partial` when a
directory failed, `offline` when the selected agent is unavailable, and
`ready` after the first complete scan. The dashboard keeps showing available
results when indexing is incomplete and refreshes them while the search is
open. Queries of one character are not matched against the large file catalog.

`GET /v1/home/search` uses the same file catalog across connected agents.
The `file_index_status` field reports whether file results are complete. The
Home's file policy and each agent's current source policy are applied before
metadata leaves the server. File opens and transfers still enforce the usual
agent/source/path authorization, including root containment and symlink checks.
Search results are a catalog view, so a file changed externally after its last
scan may be missing or stale until reconciliation.
