import { describe, expect, it } from "vitest";
import { NotificationsClient } from "./notifications";

describe("NotificationsClient", () => {
  it("lists durable notifications with encoded pagination filters", async () => {
    const paths: string[] = [];
    const request = async <T,>(path: string) => {
      paths.push(path);
      return { notifications: null, next_cursor: "next", unread_count: 4 } as T;
    };

    const page = await new NotificationsClient({ request }).list({ cursor: "a+b/=", limit: 25, unread: true });

    expect(paths).toEqual(["/v1/me/notifications?cursor=a%2Bb%2F%3D&limit=25&unread=true"]);
    expect(page).toEqual({ notifications: [], next_cursor: "next", unread_count: 4 });
  });

  it("uses the durable mutation endpoints", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return { ok: true, unread_count: 0 } as T;
    };
    const client = new NotificationsClient({ request });

    await client.markRead("ntf/one");
    await client.markAllRead();
    await client.remove("ntf/one");
    await client.clear();

    expect(calls).toEqual([
      { path: "/v1/me/notifications/ntf%2Fone/read", method: "PUT", body: {} },
      { path: "/v1/me/notifications/read-all", method: "PUT", body: {} },
      { path: "/v1/me/notifications/ntf%2Fone", method: "DELETE", body: undefined },
      { path: "/v1/me/notifications", method: "DELETE", body: undefined },
    ]);
  });

  it("reads and updates all push category preferences", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const settings = {
      user_id: "usr_1",
      agent_health: true,
      quick_links: true,
      storage: true,
      notes: true,
      dashboard_entities: true,
      updated_at: "2026-08-19T00:00:00Z",
    };
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return settings as T;
    };
    const client = new NotificationsClient({ request });

    await client.getSettings();
    await client.updateSettings({ notes: false });

    expect(calls).toEqual([
      { path: "/v1/me/notification-settings", method: undefined, body: undefined },
      { path: "/v1/me/notification-settings", method: "PUT", body: { notes: false } },
    ]);
  });

  it("gets push configuration and registers and deletes a subscription", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      if (path.endsWith("/config")) return { enabled: true, public_key: "key" } as T;
      return { subscription: { id: "wps_1" } } as T;
    };
    const client = new NotificationsClient({ request });
    const input = { endpoint: "https://push.example/sub", keys: { p256dh: "p", auth: "a" }, browser_label: "Chrome" };

    await client.getWebPushConfig();
    await client.registerWebPushSubscription(input);
    await client.deleteWebPushSubscription("wps/1");

    expect(calls).toEqual([
      { path: "/v1/me/web-push/config", method: undefined, body: undefined },
      { path: "/v1/me/web-push/subscriptions", method: "POST", body: input },
      { path: "/v1/me/web-push/subscriptions/wps%2F1", method: "DELETE", body: undefined },
    ]);
  });
});
