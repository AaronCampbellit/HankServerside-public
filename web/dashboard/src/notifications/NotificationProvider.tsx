import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  notificationsClient,
  type NotificationCategory,
  type NotificationSettings,
  type RegisteredWebPushSubscription,
  type WebPushConfig,
  type WebPushSubscriptionInput,
} from "../api/notifications";
import { notificationClickHandoffParam, subscribeToWebPush, unsubscribeFromWebPush, webPushCapability } from "../pwa/push";

export type NotificationDeliveryState =
  | "loading" | "disabled-server" | "unsupported" | "install-required" | "denied"
  | "available" | "subscribed" | "error";

export type NotificationContextValue = {
  status: "idle" | "loading" | "ready" | "error";
  deliveryState: NotificationDeliveryState;
  settings: NotificationSettings | null;
  config: WebPushConfig | null;
  unreadCount: number;
  inboxVersion: number;
  error: string;
  enable: () => Promise<void>;
  disable: () => Promise<void>;
  refresh: () => Promise<void>;
  refreshInbox: () => void;
  setUnreadCount: (count: number) => void;
  updateCategory: (category: NotificationCategory, enabled: boolean) => Promise<void>;
  dismissInvitation: () => void;
};

const NotificationContext = createContext<NotificationContextValue | null>(null);

function errorText(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Notification settings could not be updated.";
}

function invitationKey(userID: string): string {
  return `hank-notifications-invitation:${userID}`;
}

function requiresIOSInstall(): boolean {
  const navigatorStandalone = Boolean((navigator as Navigator & { standalone?: boolean }).standalone);
  const standalone = navigatorStandalone || window.matchMedia?.("(display-mode: standalone)").matches === true;
  const appleMobile = /iPad|iPhone|iPod/.test(navigator.userAgent)
    || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  return appleMobile && !standalone;
}

function browserLabel(): string {
  const agent = navigator.userAgent;
  if (/Edg\//.test(agent)) return "Edge";
  if (/Firefox\//.test(agent)) return "Firefox";
  if (/Chrome\//.test(agent) || /CriOS\//.test(agent)) return "Chrome";
  if (/Safari\//.test(agent)) return "Safari";
  return "Browser";
}

function registrationInput(subscription: PushSubscription): WebPushSubscriptionInput {
  const json = subscription.toJSON();
  if (!json.endpoint || !json.keys?.p256dh || !json.keys.auth) throw new Error("The browser returned an incomplete push subscription");
  return { endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth }, browser_label: browserLabel() };
}

export function NotificationProvider({ userID, children }: { userID?: string; children: ReactNode }) {
  const activeUserRef = useRef(userID);
  const refreshGenerationRef = useRef(0);
  activeUserRef.current = userID;
  const [status, setStatus] = useState<NotificationContextValue["status"]>(userID ? "loading" : "idle");
  const [deliveryState, setDeliveryState] = useState<NotificationDeliveryState>(userID ? "loading" : "unsupported");
  const [settings, setSettings] = useState<NotificationSettings | null>(null);
  const [config, setConfig] = useState<WebPushConfig | null>(null);
  const [subscriptionID, setSubscriptionID] = useState("");
  const [unreadCount, setUnreadCountState] = useState(0);
  const [inboxVersion, setInboxVersion] = useState(0);
  const [error, setError] = useState("");
  const [invitationDismissed, setInvitationDismissed] = useState(true);

  const setUnreadCount = useCallback((count: number) => setUnreadCountState(Math.max(0, Math.floor(count))), []);
  const refreshInbox = useCallback(() => setInboxVersion((version) => version + 1), []);

  const registerSubscription = useCallback(async (subscription: PushSubscription): Promise<RegisteredWebPushSubscription> => {
    const response = await notificationsClient.registerWebPushSubscription(registrationInput(subscription));
    return response.subscription;
  }, []);

  const refresh = useCallback(async () => {
    if (!userID) return;
    const generation = ++refreshGenerationRef.current;
    setStatus("loading");
    setDeliveryState("loading");
    setError("");
    try {
      const [nextSettings, nextConfig, page] = await Promise.all([
        notificationsClient.getSettings(),
        notificationsClient.getWebPushConfig(),
        notificationsClient.list({ limit: 1 }),
      ]);
      if (activeUserRef.current !== userID || refreshGenerationRef.current !== generation) return;
      setSettings(nextSettings);
      setConfig(nextConfig);
      setUnreadCount(page.unread_count);
      setInvitationDismissed(localStorage.getItem(invitationKey(userID)) === "dismissed");
      setSubscriptionID("");
      setStatus("ready");
      if (!nextConfig.enabled) {
        setDeliveryState("disabled-server");
      } else if (requiresIOSInstall()) {
        setDeliveryState("install-required");
      } else if (!("serviceWorker" in navigator) || typeof Notification === "undefined") {
        setDeliveryState("unsupported");
      } else if (Notification.permission === "denied") {
        setDeliveryState("denied");
      } else if (Notification.permission !== "granted") {
        setDeliveryState("available");
      } else {
        const registration = await navigator.serviceWorker.ready;
        const capability = webPushCapability(registration);
        if (capability === "denied") {
          setDeliveryState("denied");
        } else if (capability === "unsupported") {
          setDeliveryState("unsupported");
        } else if (capability === "granted") {
          const subscription = await subscribeToWebPush(registration, nextConfig.public_key);
          const registered = await registerSubscription(subscription);
          setSubscriptionID(registered.id);
          setDeliveryState("subscribed");
        } else {
          setDeliveryState("available");
        }
      }
    } catch (cause) {
      if (activeUserRef.current !== userID || refreshGenerationRef.current !== generation) return;
      setError(errorText(cause));
      setDeliveryState("error");
      setStatus("error");
    }
  }, [registerSubscription, setUnreadCount, userID]);

  useEffect(() => {
    if (!userID) {
      refreshGenerationRef.current += 1;
      setStatus("idle");
      setSettings(null);
      setConfig(null);
      setSubscriptionID("");
      setUnreadCount(0);
      setDeliveryState("unsupported");
      return;
    }
    void refresh();
  }, [refresh, setUnreadCount, userID]);

  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    const receiveWorkerMessage = (event: MessageEvent) => {
      if (event.data?.type === "HANK_NOTIFICATIONS_REFRESH") {
        if (Number.isInteger(event.data.unreadCount)) setUnreadCount(event.data.unreadCount);
        refreshInbox();
      } else if (event.data?.type === "HANK_NOTIFICATION_OPEN") {
        if (typeof event.data.notificationId === "string" && event.data.notificationId) {
          void notificationsClient.markRead(event.data.notificationId)
            .then((result) => setUnreadCount(result.unread_count))
            .catch(() => undefined);
        }
        refreshInbox();
      }
    };
    navigator.serviceWorker.addEventListener("message", receiveWorkerMessage);
    return () => navigator.serviceWorker.removeEventListener("message", receiveWorkerMessage);
  }, [refreshInbox, setUnreadCount]);

  useEffect(() => {
    if (!userID) return;
    const url = new URL(window.location.href);
    const notificationID = url.searchParams.get(notificationClickHandoffParam) || "";
    if (!notificationID || notificationID.length > 160) return;
    url.searchParams.delete(notificationClickHandoffParam);
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
    void notificationsClient.markRead(notificationID)
      .then((result) => {
        if (activeUserRef.current !== userID) return;
        setUnreadCount(result.unread_count);
        refreshInbox();
      })
      .catch(() => undefined);
  }, [refreshInbox, setUnreadCount, userID]);

  useEffect(() => {
    if (!userID) return;
    let active = true;
    const refreshSummary = async () => {
      if (document.visibilityState !== "visible") return;
      try {
        const page = await notificationsClient.list({ limit: 1 });
        if (!active) return;
        setUnreadCount(page.unread_count);
        refreshInbox();
      } catch {
        // A transient summary refresh must not replace usable inbox state.
      }
    };
    const onVisibilityChange = () => { if (document.visibilityState === "visible") void refreshSummary(); };
    document.addEventListener("visibilitychange", onVisibilityChange);
    const interval = window.setInterval(() => void refreshSummary(), 60_000);
    return () => {
      active = false;
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.clearInterval(interval);
    };
  }, [refreshInbox, setUnreadCount, userID]);

  async function enable() {
    if (!userID || !config?.enabled || !("serviceWorker" in navigator) || typeof Notification === "undefined") return;
    setError("");
    try {
      let permission = Notification.permission;
      if (permission === "default") permission = await Notification.requestPermission();
      if (permission !== "granted") {
        setDeliveryState(permission === "denied" ? "denied" : "available");
        return;
      }
      const registration = await navigator.serviceWorker.ready;
      const subscription = await subscribeToWebPush(registration, config.public_key);
      const registered = await registerSubscription(subscription);
      setSubscriptionID(registered.id);
      setDeliveryState("subscribed");
      localStorage.setItem(invitationKey(userID), "dismissed");
      setInvitationDismissed(true);
      setStatus("ready");
    } catch (cause) {
      setError(errorText(cause));
      setDeliveryState("error");
      setStatus("error");
    }
  }

  async function disable() {
    setError("");
    let failure: unknown;
    try {
      if (subscriptionID) await notificationsClient.deleteWebPushSubscription(subscriptionID);
    } catch (cause) {
      failure = cause;
    }
    try {
      if ("serviceWorker" in navigator) await unsubscribeFromWebPush(await navigator.serviceWorker.ready);
    } catch (cause) {
      failure ||= cause;
    }
    setSubscriptionID("");
    setDeliveryState(Notification.permission === "denied" ? "denied" : "available");
    if (failure) {
      setError(errorText(failure));
      setStatus("error");
      throw failure;
    }
    setStatus("ready");
  }

  async function updateCategory(category: NotificationCategory, enabled: boolean) {
    setError("");
    try {
      const next = await notificationsClient.updateSettings({ [category]: enabled });
      setSettings(next);
      setStatus("ready");
    } catch (cause) {
      setError(errorText(cause));
      throw cause;
    }
  }

  function dismissInvitation() {
    if (userID) localStorage.setItem(invitationKey(userID), "dismissed");
    setInvitationDismissed(true);
  }

  const value = useMemo<NotificationContextValue>(() => ({
    status, deliveryState, settings, config, unreadCount, inboxVersion, error,
    enable, disable, refresh, refreshInbox, setUnreadCount, updateCategory, dismissInvitation,
  }), [config, deliveryState, error, inboxVersion, settings, status, subscriptionID, unreadCount, userID, refresh, refreshInbox, setUnreadCount]);

  const showInvitation = Boolean(userID && status === "ready" && deliveryState === "available" && !invitationDismissed);
  return (
    <NotificationContext.Provider value={value}>
      {children}
      {showInvitation ? (
        <aside aria-label="Turn on notifications" className="notification-invitation" role="dialog">
          <div>
            <strong>Stay up to date</strong>
            <p>Turn on browser notifications for agent health, quick-link, storage, shared-note, and Home Assistant alerts.</p>
          </div>
          <div className="button-row">
            <button type="button" onClick={() => void enable()}>Enable notifications</button>
            <button type="button" className="secondary" onClick={dismissInvitation}>Not now</button>
          </div>
        </aside>
      ) : null}
    </NotificationContext.Provider>
  );
}

export function useNotifications(): NotificationContextValue {
  const value = useContext(NotificationContext);
  if (!value) throw new Error("useNotifications must be used within NotificationProvider");
  return value;
}

export function useOptionalNotifications(): NotificationContextValue | null {
  return useContext(NotificationContext);
}
