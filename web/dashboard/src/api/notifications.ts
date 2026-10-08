import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";
import type { AuditEvent } from "./logs";

export type NotificationCategory = "monitoring" | "agent_health" | "quick_links" | "storage" | "notes" | "dashboard_entities";

export type NotificationItem = {
  id: string;
  user_id: string;
  home_id: string;
  audit_event_id?: string;
  category: NotificationCategory;
  event_kind: string;
  severity: "info" | "warning" | "danger" | "success" | string;
  title: string;
  body: string;
  target_path: string;
  collapse_key: string;
  outcome: string;
  occurrence_count: number;
  first_occurred_at: string;
  last_occurred_at: string;
  read_at?: string | null;
  created_at: string;
};

export type NotificationPage = {
  notifications: NotificationItem[];
  next_cursor?: string;
  unread_count: number;
};

export type NotificationListOptions = {
  cursor?: string;
  limit?: number;
  unread?: boolean;
  signal?: AbortSignal;
};

export type NotificationSettings = {
  user_id: string;
  monitoring: boolean;
  agent_health: boolean;
  quick_links: boolean;
  storage: boolean;
  notes: boolean;
  dashboard_entities: boolean;
  updated_at: string;
};

export type NotificationSettingsUpdate = Partial<Pick<NotificationSettings,
  "monitoring" | "agent_health" | "quick_links" | "storage" | "notes" | "dashboard_entities"
>>;

export type WebPushConfig = { enabled: false; public_key?: never } | { enabled: true; public_key: string };

export type WebPushSubscriptionInput = {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  browser_label: string;
};

export type RegisteredWebPushSubscription = {
  id: string;
  user_id?: string;
  session_id?: string;
  browser_label?: string;
  created_at?: string;
  refreshed_at?: string;
};

export type NotificationMutationResult = {
  ok?: true;
  updated?: number;
  deleted?: number;
  unread_count: number;
};

export class NotificationsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async list(options: NotificationListOptions = {}): Promise<NotificationPage> {
    const query = new URLSearchParams();
    if (options.cursor) query.set("cursor", options.cursor);
    if (options.limit !== undefined) query.set("limit", String(options.limit));
    if (options.unread !== undefined) query.set("unread", String(options.unread));
    const suffix = query.size ? `?${query.toString()}` : "";
    const payload = await this.api.request<Partial<NotificationPage>>(`/v1/me/notifications${suffix}`, {
      signal: options.signal,
      timeoutMs: 8000,
    });
    return {
      notifications: arrayFrom<NotificationItem>(payload.notifications),
      next_cursor: payload.next_cursor || undefined,
      unread_count: Number.isFinite(payload.unread_count) ? Number(payload.unread_count) : 0,
    };
  }

  markRead(id: string) {
    return this.api.request<NotificationMutationResult>(`/v1/me/notifications/${encodeURIComponent(id)}/read`, { method: "PUT", body: {} });
  }

  markAllRead() {
    return this.api.request<NotificationMutationResult>("/v1/me/notifications/read-all", { method: "PUT", body: {} });
  }

  remove(id: string) {
    return this.api.request<NotificationMutationResult>(`/v1/me/notifications/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

  clear() {
    return this.api.request<NotificationMutationResult>("/v1/me/notifications", { method: "DELETE" });
  }

  async getEvent(id: string): Promise<AuditEvent> {
    const payload = await this.api.request<{ event: AuditEvent }>(`/v1/me/notifications/${encodeURIComponent(id)}/event`, { timeoutMs: 8000 });
    return payload.event;
  }

  getSettings() {
    return this.api.request<NotificationSettings>("/v1/me/notification-settings");
  }

  updateSettings(settings: NotificationSettingsUpdate) {
    return this.api.request<NotificationSettings>("/v1/me/notification-settings", { method: "PUT", body: settings });
  }

  getWebPushConfig() {
    return this.api.request<WebPushConfig>("/v1/me/web-push/config");
  }

  registerWebPushSubscription(subscription: WebPushSubscriptionInput) {
    return this.api.request<{ subscription: RegisteredWebPushSubscription }>("/v1/me/web-push/subscriptions", { method: "POST", body: subscription });
  }

  deleteWebPushSubscription(id: string) {
    return this.api.request<{ ok: true }>(`/v1/me/web-push/subscriptions/${encodeURIComponent(id)}`, { method: "DELETE" });
  }
}

export const notificationsClient = new NotificationsClient();
