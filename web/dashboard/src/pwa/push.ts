export type WebPushCapability = "unsupported" | "default" | "denied" | "granted";

type NotificationPermissionSource = Pick<typeof Notification, "permission">;

export function webPushCapability(
  registration: Pick<ServiceWorkerRegistration, "pushManager"> | undefined,
  notificationApi: NotificationPermissionSource | undefined = typeof Notification === "undefined" ? undefined : Notification,
): WebPushCapability {
  if (!registration?.pushManager || !notificationApi) return "unsupported";
  return notificationApi.permission;
}

export function vapidPublicKeyBytes(publicKey: string): Uint8Array<ArrayBuffer> {
  const normalized = publicKey.trim().replace(/-/g, "+").replace(/_/g, "/");
  if (!normalized || normalized.length % 4 === 1) throw new Error("Invalid VAPID public key");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  let decoded: string;
  try {
    decoded = atob(padded);
  } catch {
    throw new Error("Invalid VAPID public key");
  }
  const bytes = new Uint8Array(new ArrayBuffer(decoded.length));
  for (let index = 0; index < decoded.length; index += 1) bytes[index] = decoded.charCodeAt(index);
  return bytes;
}

export async function subscribeToWebPush(
  registration: ServiceWorkerRegistration,
  publicKey: string,
  notificationApi: NotificationPermissionSource | undefined = typeof Notification === "undefined" ? undefined : Notification,
): Promise<PushSubscription> {
  if (webPushCapability(registration, notificationApi) === "unsupported") {
    throw new Error("Web Push is not supported by this browser");
  }
  const existing = await registration.pushManager.getSubscription();
  if (existing && subscriptionMatchesVAPIDKey(existing, publicKey)) return existing;
  if (notificationApi?.permission !== "granted") {
    throw new Error("Notification permission must be granted before subscribing");
  }
  if (existing) await existing.unsubscribe();
  return registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: vapidPublicKeyBytes(publicKey),
  });
}

function subscriptionMatchesVAPIDKey(subscription: PushSubscription, publicKey: string): boolean {
  const current = subscription.options?.applicationServerKey;
  if (!current) return true;
  const currentBytes = new Uint8Array(current);
  const expected = vapidPublicKeyBytes(publicKey);
  return currentBytes.length === expected.length && currentBytes.every((value, index) => value === expected[index]);
}

export async function unsubscribeFromWebPush(registration: ServiceWorkerRegistration): Promise<boolean> {
  const subscription = await registration.pushManager.getSubscription();
  return subscription ? subscription.unsubscribe() : false;
}

const notificationCategories = new Set(["monitoring", "agent_health", "quick_links", "storage", "notes", "dashboard_entities"]);
export const notificationClickHandoffParam = "hank_notification_id";

export type WebPushPayload = {
  schemaVersion: 1;
  notificationId: string;
  category: string;
  severity: string;
  title: string;
  body: string;
  route: string;
  tag: string;
  unreadCount: number;
  occurredAt: string;
};

const webPushMaxAgeMs = 15 * 60_000;
const webPushMaxFutureSkewMs = 5 * 60_000;

function dashboardRoute(value: unknown, origin: string): string | null {
  if (typeof value !== "string" || !value.startsWith("/")) return null;
  try {
    const url = new URL(value, origin);
    if (url.origin !== origin || (url.pathname !== "/dashboard" && !url.pathname.startsWith("/dashboard/"))) return null;
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return null;
  }
}

export function parseWebPushPayload(value: unknown, origin: string): WebPushPayload | null {
  if (!value || typeof value !== "object") return null;
  const item = value as Record<string, unknown>;
  const route = dashboardRoute(item.route, origin);
  if (
    item.schema_version !== 1 ||
    typeof item.notification_id !== "string" || !item.notification_id || item.notification_id.length > 160 ||
    typeof item.category !== "string" || !notificationCategories.has(item.category) ||
    typeof item.severity !== "string" || !item.severity ||
    typeof item.title !== "string" || !item.title || item.title.length > 240 ||
    typeof item.body !== "string" || item.body.length > 1000 ||
    typeof item.tag !== "string" || item.tag.length > 240 ||
    typeof item.unread_count !== "number" || !Number.isInteger(item.unread_count) || item.unread_count < 0 ||
    typeof item.occurred_at !== "string" || !item.occurred_at || !Number.isFinite(Date.parse(item.occurred_at)) ||
    !route
  ) return null;
  return {
    schemaVersion: 1,
    notificationId: item.notification_id,
    category: item.category,
    severity: item.severity,
    title: item.title,
    body: item.body,
    route,
    tag: item.tag,
    unreadCount: item.unread_count,
    occurredAt: item.occurred_at,
  };
}

type MessageClient = {
  url: string;
  postMessage(message: unknown): void;
  focus?: () => Promise<unknown>;
  navigate?: (url: string) => Promise<unknown>;
};

export type NotificationWorkerScope = {
  location: { origin: string };
  registration: {
    showNotification(title: string, options?: NotificationOptions): Promise<void>;
  };
  navigator?: { setAppBadge?: (count?: number) => Promise<void> };
  clients: {
    matchAll(options?: unknown): Promise<MessageClient[]>;
    openWindow?: (url: string) => Promise<unknown>;
  };
};

export async function handlePushPayload(scope: NotificationWorkerScope, rawPayload: unknown, now = Date.now()): Promise<boolean> {
  const payload = parseWebPushPayload(rawPayload, scope.location.origin);
  if (!payload) return false;
  const occurredAt = Date.parse(payload.occurredAt);
  if (occurredAt < now - webPushMaxAgeMs || occurredAt > now + webPushMaxFutureSkewMs) return false;
  const clients = await scope.clients.matchAll({ type: "window", includeUncontrolled: true });
  for (const client of clients) {
    if (new URL(client.url).origin === scope.location.origin) {
      client.postMessage({ type: "HANK_NOTIFICATIONS_REFRESH", unreadCount: payload.unreadCount });
    }
  }
  if (scope.navigator?.setAppBadge) {
    await scope.navigator.setAppBadge(payload.unreadCount).catch(() => undefined);
  }
  await scope.registration.showNotification(payload.title, {
    body: payload.body,
    tag: payload.tag || payload.notificationId,
    icon: "/assets/hank-icon-192.png",
    badge: "/assets/hank-icon-192.png",
    data: { notificationId: payload.notificationId, route: payload.route },
  });
  return true;
}

export async function handleNotificationClick(
  scope: Pick<NotificationWorkerScope, "location" | "clients">,
  notification: { data?: unknown; close(): void },
): Promise<void> {
  notification.close();
  const data = notification.data && typeof notification.data === "object" ? notification.data as Record<string, unknown> : {};
  const notificationId = typeof data.notificationId === "string" ? data.notificationId : "";
  const route = dashboardRoute(data.route, scope.location.origin) || "/dashboard";
  const clients = await scope.clients.matchAll({ type: "window", includeUncontrolled: true });
  const client = clients.find((candidate) => {
    try { return new URL(candidate.url).origin === scope.location.origin; } catch { return false; }
  });
  if (client) {
    if (client.navigate) await client.navigate(route);
    if (client.focus) await client.focus();
    client.postMessage({ type: "HANK_NOTIFICATION_OPEN", notificationId, route });
    return;
  }
  const target = new URL(route, scope.location.origin);
  if (notificationId) target.searchParams.set(notificationClickHandoffParam, notificationId);
  await scope.clients.openWindow?.(`${target.pathname}${target.search}${target.hash}`);
}
