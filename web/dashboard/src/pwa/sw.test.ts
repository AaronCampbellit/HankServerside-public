import { describe, expect, it, vi } from "vitest";
import { handleNotificationClick, handlePushPayload, parseWebPushPayload } from "./push";

const validPayload = {
  schema_version: 1,
  notification_id: "ntf_1",
  category: "agent_health",
  severity: "warning",
  title: "Primary Hank Agent offline",
  body: "The primary Hank Agent stopped responding.",
  route: "/dashboard/settings/notifications?from=push",
  tag: "agent:offline",
  unread_count: 3,
  occurred_at: "2026-08-21T12:00:00Z",
};

describe("service worker push handling", () => {
  it("rejects unknown schemas, malformed payloads, and unsafe routes", () => {
    expect(parseWebPushPayload({ ...validPayload, category: "monitoring" }, "https://hank.example")?.category).toBe("monitoring");
    expect(parseWebPushPayload({ ...validPayload, category: "unknown" }, "https://hank.example")).toBeNull();
    expect(parseWebPushPayload({ ...validPayload, schema_version: 2 }, "https://hank.example")).toBeNull();
    expect(parseWebPushPayload({ ...validPayload, title: "" }, "https://hank.example")).toBeNull();
    expect(parseWebPushPayload({ ...validPayload, route: "https://evil.example/dashboard" }, "https://hank.example")).toBeNull();
    expect(parseWebPushPayload({ ...validPayload, route: "/login" }, "https://hank.example")).toBeNull();
  });

  it("shows a tagged notification, updates the badge, and refreshes foreground clients", async () => {
    const showNotification = vi.fn().mockResolvedValue(undefined);
    const setAppBadge = vi.fn().mockResolvedValue(undefined);
    const postMessage = vi.fn();
    const scope = {
      location: { origin: "https://hank.example" },
      registration: { showNotification },
      navigator: { setAppBadge },
      clients: { matchAll: vi.fn().mockResolvedValue([{ url: "https://hank.example/dashboard", postMessage }]) },
    };

	await expect(handlePushPayload(scope, validPayload, Date.parse("2026-08-21T12:05:00Z"))).resolves.toBe(true);
    expect(showNotification).toHaveBeenCalledWith("Primary Hank Agent offline", expect.objectContaining({
      body: "The primary Hank Agent stopped responding.",
      tag: "agent:offline",
      data: { notificationId: "ntf_1", route: "/dashboard/settings/notifications?from=push" },
    }));
    expect(setAppBadge).toHaveBeenCalledWith(3);
    expect(postMessage).toHaveBeenCalledWith({ type: "HANK_NOTIFICATIONS_REFRESH", unreadCount: 3 });
  });

  it("ignores invalid payloads without showing a notification", async () => {
    const showNotification = vi.fn();
    const scope = {
      location: { origin: "https://hank.example" },
      registration: { showNotification },
      clients: { matchAll: vi.fn().mockResolvedValue([]) },
    };
    await expect(handlePushPayload(scope, { hello: "world" })).resolves.toBe(false);
    expect(showNotification).not.toHaveBeenCalled();
  });

  it("drops stale pushes instead of resurfacing them after a restart", async () => {
	const showNotification = vi.fn();
	const postMessage = vi.fn();
	const scope = {
	  location: { origin: "https://hank.example" },
	  registration: { showNotification },
	  clients: { matchAll: vi.fn().mockResolvedValue([{ url: "https://hank.example/dashboard", postMessage }]) },
	};

	await expect(handlePushPayload(scope, validPayload, Date.parse("2026-08-21T12:16:00Z"))).resolves.toBe(false);
	expect(showNotification).not.toHaveBeenCalled();
	expect(postMessage).not.toHaveBeenCalled();
  });

  it("navigates and focuses a same-origin client and hands off mark-read data", async () => {
    const postMessage = vi.fn();
    const navigate = vi.fn().mockResolvedValue(undefined);
    const focus = vi.fn().mockResolvedValue(undefined);
    const client = { url: "https://hank.example/dashboard", postMessage, navigate, focus };
    const scope = {
      location: { origin: "https://hank.example" },
      clients: { matchAll: vi.fn().mockResolvedValue([client]), openWindow: vi.fn() },
    };
    const notification = { data: { notificationId: "ntf_1", route: "/dashboard/notes/note_1" }, close: vi.fn() };

    await handleNotificationClick(scope, notification);

    expect(notification.close).toHaveBeenCalledOnce();
    expect(navigate).toHaveBeenCalledWith("/dashboard/notes/note_1");
    expect(focus).toHaveBeenCalledOnce();
    expect(postMessage).toHaveBeenCalledWith({ type: "HANK_NOTIFICATION_OPEN", notificationId: "ntf_1", route: "/dashboard/notes/note_1" });
    expect(scope.clients.openWindow).not.toHaveBeenCalled();
  });

  it("opens a new dashboard client when none is available", async () => {
    const openWindow = vi.fn().mockResolvedValue(undefined);
    const scope = {
      location: { origin: "https://hank.example" },
      clients: { matchAll: vi.fn().mockResolvedValue([]), openWindow },
    };
    await handleNotificationClick(scope, { data: { notificationId: "ntf_1", route: "/dashboard?from=push" }, close: vi.fn() });
    expect(openWindow).toHaveBeenCalledWith("/dashboard?from=push&hank_notification_id=ntf_1");
  });
});
