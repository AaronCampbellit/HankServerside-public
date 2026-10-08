# Hank Dashboard PWA

HankServerside makes the existing React dashboard installable as a Progressive
Web App. It is not a separate product or route family: installation opens
`/dashboard`, and the same server-side authentication and authorization rules
protect dashboard pages and APIs.

## Offline-Capable Notes

After a successful authenticated visit, the installed dashboard keeps a user-scoped Notes workspace in IndexedDB. Cached Notes can be opened after a failed network bootstrap, and the following actions are local-first:

- browse cached notes, notebooks, and Kanban boards
- create and edit text notes, notebook children, and Kanban cards or columns
- move, pin, exclude from MCP, and delete notes or Kanban items
- select or clear the default Kanban board
- open attachment copies that were cached previously

A local transaction must complete before the UI reports `Saved offline`. Changes enter a durable IndexedDB outbox with stable local and future server IDs. Saves to the same note coalesce without replacing the original base revision. A 15-second, per-user lease prevents two tabs from replaying the same mutation; tab notifications, online events, and bounded retry timers wake synchronization. Once the outbox drains, Hank reconciles full server notes and profile settings without replacing dirty local records.

Save conflicts use a preserve-both policy: the current server note remains canonical and the complete local text or Kanban payload becomes a separately titled conflict copy. A conditional delete conflict restores the current server note with `Deletion needs review`. Permanent failures stay visible until the affected note is corrected and saved again; `Sync now` retries only retryable failures. Authentication failures lock cached Notes until the user signs in again without erasing queued changes.

Offline eligibility begins only after the first successful full Notes reconciliation, lasts for 30 days from the most recent successful authentication, and is limited to the same stored user. Explicit logout purges that user's identity, notes, mutations, attachment blobs, and lease. This is an origin-isolated browser store, not encrypted device storage: anyone who can access the same unlocked browser profile and operating-system account may be able to inspect its IndexedDB data.

## Online-Only Boundary

Authentication renewal, note sharing, attachment upload and deletion, Home Assistant, Files, HankAI, Settings outside the cached Notes workflow, and other dashboard services remain online-only. Kanban and note attachment metadata remains visible offline, but a file without a cached Blob is labeled `Available when connected`; upload controls are disabled and attachment mutations are never queued.

Successful online attachment reads may be cached for viewing. Blobs use a 100 MiB per-user soft cap and least-recently-used eviction. Eviction removes only Blob bytes, never note or attachment metadata.

## Service Worker Boundary

The PWA control files are served at:

- `/manifest.webmanifest`
- `/sw.js`
- `/offline.html`

Vite's `injectManifest` build precaches public application-shell assets, the entry document, and the self-contained connection-required page. Same-origin dashboard navigation is network-first and falls back to the precached entry document so the app can restore an eligible offline Notes session. Public navigation falls back to `/offline.html`.

The service worker does not cache authenticated API responses, `/v1` traffic, `/ws` traffic, note payloads, attachment bytes, credentials, or mutations. API and WebSocket failures retain normal network behavior. Private offline data belongs only in the user-partitioned IndexedDB modules described above.

## Durable Notifications and Web Push

The bell is a server-backed inbox, not a list computed from current page state. Entitled users receive durable entries for Hank Agent offline and recovery transitions, low disk and recovery, monitored quick-link down and recovery, server-monitoring alerts and recovery for the configured Home audience, storage events, shared-note changes, and followed Home Assistant entity alerts. Equivalent unread events can coalesce for five minutes and show an occurrence count. Entries remain for 30 days after their latest occurrence unless the user deletes them. New entries retain a reference to their latest audit event; the inbox `View` action opens that exact event with its full, redacted details expanded. This owner-scoped lookup does not grant access to the home-wide admin audit feed.

The dashboard offers browser delivery once after authenticated sign-in, but calls the browser permission prompt only when the user chooses Enable. A non-sensitive, per-user dismissal marker prevents repeated invitations in the same browser. Home administrators can also configure server-alert email and inbox recipients in Notification Settings; these Home-wide delivery choices are separate from personal push preferences. Permanent Notification Settings controls this device's enrollment and six account-wide push categories. Disabling a category stops push delivery while retaining its inbox copy.

Push handling does not widen the offline boundary. The service worker validates compact schema-versioned JSON, rejects cross-origin or non-dashboard routes, displays a tagged system notification, updates the application badge where supported, and asks open dashboard clients to refresh. Push delivery is a timely signal rather than a second durable inbox: deliveries expire after 15 minutes, and read or stale entries are suppressed so a server or Hank Agent restart cannot resurface old alerts. Clicking an alert focuses or opens a same-origin dashboard page and the app marks the durable entry read. Subscription endpoints and browser keys are never stored in local storage or logs.

Web Push requires HTTPS. iOS and iPadOS delivery requires Hank to be installed to the Home Screen and opened as a standalone web app. Explicit logout unsubscribes the browser and revokes the session-owned server enrollment.

## Installation and Updates

Supporting browsers can offer a native install prompt from the dashboard navigation. iOS Safari receives Add to Home Screen guidance because it does not expose the same prompt API. The installed app uses standalone display mode and Hank's existing icons and theme.

Service-worker updates are announced inside the dashboard. A new worker activates only after the user chooses to update, preventing an unexpected reload during active work. The update prompt can be dismissed for the current page session.

Production deployment requires HTTPS (localhost remains the browser development exception). Proxies must serve the root PWA control routes without rewriting them to authenticated dashboard HTML.

The legacy `/pwa`, `/pwa/`, `/pwa/sw.js`, `/pwa/manifest.webmanifest`, and `/assets/site.webmanifest` routes remain unserved.

## Realtime Recovery

Dashboard command sockets share concurrent connection attempts and reconnect
with bounded exponential backoff while subscriptions or listeners remain. Each
connection obtains a fresh ticket and reauthorizes remembered subscriptions.
After reconnect, Home Assistant reloads its state, file jobs and agent health
refresh, and terminal sessions attach from their last output cursor. Pending
commands fail explicitly when the socket drops; state-changing commands are
never replayed automatically. Explicit close cancels retries, and expired or
revoked browser authentication stops background retrying.
