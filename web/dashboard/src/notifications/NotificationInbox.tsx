import { useCallback, useEffect, useState } from "react";
import { notificationsClient, type NotificationItem } from "../api/notifications";
import { useOptionalNotifications, type NotificationContextValue } from "./NotificationProvider";

function errorText(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Notifications could not be loaded.";
}

function categoryLabel(category: string): string {
  return ({ monitoring: "Monitoring", agent_health: "Agent health", quick_links: "Quick links", storage: "Storage", notes: "Shared notes", dashboard_entities: "Home Assistant" } as Record<string, string>)[category] || category;
}

function occurrenceTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const elapsed = Date.now() - date.getTime();
  const minutes = Math.max(0, Math.floor(elapsed / 60_000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function NotificationInbox({
  context: suppliedContext,
  onClose,
  onNavigate,
  onUnreadCount,
}: {
  context?: NotificationContextValue;
  onClose: () => void;
  onNavigate: (href: string) => void;
  onUnreadCount?: (count: number) => void;
}) {
  const providerContext = useOptionalNotifications();
  const context = suppliedContext ?? providerContext;
  const [items, setItems] = useState<NotificationItem[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");
  const [pending, setPending] = useState("");

  const updateUnread = useCallback((count: number) => {
    context?.setUnreadCount(count);
    onUnreadCount?.(count);
  }, [context, onUnreadCount]);

  const load = useCallback(async () => {
    setStatus("loading");
    setError("");
    try {
      const page = await notificationsClient.list({ limit: 50 });
      setItems(page.notifications);
      setNextCursor(page.next_cursor || "");
      updateUnread(page.unread_count);
      setStatus("ready");
    } catch (cause) {
      setError(errorText(cause));
      setStatus("error");
    }
  }, [updateUnread]);

  useEffect(() => { void load(); }, [load, context?.inboxVersion]);

  async function loadMore() {
    if (!nextCursor || pending) return;
    setPending("more");
    try {
      const page = await notificationsClient.list({ cursor: nextCursor, limit: 50 });
      setItems((current) => [...current, ...page.notifications.filter((candidate) => !current.some((item) => item.id === candidate.id))]);
      setNextCursor(page.next_cursor || "");
      updateUnread(page.unread_count);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setPending("");
    }
  }

  async function markRead(item: NotificationItem, destination = "") {
    const previous = items;
    if (!item.read_at) setItems((current) => current.map((candidate) => candidate.id === item.id ? { ...candidate, read_at: new Date().toISOString() } : candidate));
    setPending(`read:${item.id}`);
    try {
      if (!item.read_at) {
        const result = await notificationsClient.markRead(item.id);
        updateUnread(result.unread_count);
      }
      if (destination) {
        onClose();
        onNavigate(destination);
      }
    } catch (cause) {
      setItems(previous);
      setError(errorText(cause));
    } finally {
      setPending("");
    }
  }

  async function markAllRead() {
    const previous = items;
    const readAt = new Date().toISOString();
    setItems((current) => current.map((item) => ({ ...item, read_at: item.read_at || readAt })));
    setPending("read-all");
    try {
      const result = await notificationsClient.markAllRead();
      updateUnread(result.unread_count);
    } catch (cause) {
      setItems(previous);
      setError(errorText(cause));
    } finally {
      setPending("");
    }
  }

  async function remove(item: NotificationItem) {
    const previous = items;
    setItems((current) => current.filter((candidate) => candidate.id !== item.id));
    setPending(`delete:${item.id}`);
    try {
      const result = await notificationsClient.remove(item.id);
      updateUnread(result.unread_count);
    } catch (cause) {
      setItems(previous);
      setError(errorText(cause));
    } finally {
      setPending("");
    }
  }

  async function clearAll() {
    if (!window.confirm("Clear every notification from your inbox? This cannot be undone.")) return;
    const previous = items;
    setItems([]);
    setNextCursor("");
    setPending("clear");
    try {
      const result = await notificationsClient.clear();
      updateUnread(result.unread_count);
    } catch (cause) {
      setItems(previous);
      setError(errorText(cause));
    } finally {
      setPending("");
    }
  }

  return (
    <>
      <div className="notif-popover-header">
        <strong>Notifications</strong>
        {items.length ? <span>{items.length}</span> : null}
      </div>
      {items.length ? (
        <div className="notification-inbox-actions">
          <button className="secondary" disabled={Boolean(pending) || !items.some((item) => !item.read_at)} onClick={() => void markAllRead()} type="button">Mark all read</button>
          <button className="danger-link" disabled={Boolean(pending)} onClick={() => void clearAll()} type="button">Clear all</button>
        </div>
      ) : null}
      {error ? <p className="notification-inbox-error" role="alert">{error}</p> : null}
      {status === "loading" ? (
        <div className="notif-empty"><span className="spinner" aria-hidden="true" />Loading notifications...</div>
      ) : status === "error" ? (
        <div className="notif-empty"><button className="secondary" onClick={() => void load()} type="button">Retry notifications</button></div>
      ) : items.length === 0 ? (
        <div className="notif-empty">No notifications yet.</div>
      ) : (
        <div className="notif-list notification-inbox-list" role="list">
          {items.map((item) => (
            <article className={`notif-item tone-${item.severity || "info"}${item.read_at ? " is-read" : " is-unread"}`} key={item.id} role="listitem">
              <button className="notification-inbox-open" onClick={() => void markRead(item, item.target_path || "/dashboard")} type="button" aria-label={`Open ${item.title}`}>
                <span className="notif-dot" aria-hidden="true" />
                <span className="notification-inbox-copy">
                  <span><strong>{item.title}</strong>{item.occurrence_count > 1 ? <b>×{item.occurrence_count}</b> : null}</span>
                  {item.body ? <small>{item.body}</small> : null}
                  <small>{categoryLabel(item.category)} · <time dateTime={item.last_occurred_at}>{occurrenceTime(item.last_occurred_at)}</time></small>
                </span>
              </button>
              <div className="notification-inbox-row-actions">
                {item.audit_event_id ? <button className="secondary" disabled={Boolean(pending)} onClick={() => void markRead(item, `/dashboard/notifications/event?notification=${encodeURIComponent(item.id)}`)} type="button" aria-label={`View log for ${item.title}`}>View</button> : null}
                {!item.read_at ? <button className="secondary" disabled={Boolean(pending)} onClick={() => void markRead(item)} type="button" aria-label={`Mark ${item.title} read`}>Read</button> : null}
                <button className="danger-link" disabled={Boolean(pending)} onClick={() => void remove(item)} type="button" aria-label={`Delete ${item.title}`}>Delete</button>
              </div>
            </article>
          ))}
          {nextCursor ? <button className="secondary notification-load-more" disabled={pending === "more"} onClick={() => void loadMore()} type="button" aria-label="Load more notifications">{pending === "more" ? "Loading…" : "Load more"}</button> : null}
        </div>
      )}
    </>
  );
}
