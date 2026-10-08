import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NotificationProvider, useNotifications } from "./NotificationProvider";

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  getWebPushConfig: vi.fn(),
  registerWebPushSubscription: vi.fn(),
  deleteWebPushSubscription: vi.fn(),
  list: vi.fn(),
  markRead: vi.fn(),
}));

vi.mock("../api/notifications", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/notifications")>()),
  notificationsClient: api,
}));

const settings = {
  user_id: "usr_1", monitoring: true, agent_health: true, quick_links: true, storage: true, notes: true,
  dashboard_entities: true, updated_at: "2026-08-19T00:00:00Z",
};

function StateProbe() {
  const notifications = useNotifications();
  return <div data-testid="state">{notifications.status}:{notifications.deliveryState}:{notifications.unreadCount}:{notifications.settings?.user_id || "none"}</div>;
}

describe("NotificationProvider", () => {
  let permission: NotificationPermission;
  let requestPermission: ReturnType<typeof vi.fn>;
  let subscribe: ReturnType<typeof vi.fn>;
  let unsubscribe: ReturnType<typeof vi.fn>;
  let workerMessage: ((event: MessageEvent) => void) | undefined;

  beforeEach(() => {
    localStorage.clear();
    permission = "default";
    requestPermission = vi.fn(async () => { permission = "granted"; return "granted" as const; });
    unsubscribe = vi.fn().mockResolvedValue(true);
    subscribe = vi.fn().mockResolvedValue({
      endpoint: "https://push.example/new",
      options: { applicationServerKey: new Uint8Array([1, 2, 3]).buffer },
      toJSON: () => ({ endpoint: "https://push.example/new", keys: { p256dh: "p", auth: "a" } }),
      unsubscribe,
    });
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(null), subscribe } };
    workerMessage = undefined;
    Object.defineProperty(window.navigator, "serviceWorker", { configurable: true, value: {
      ready: Promise.resolve(registration),
      addEventListener: vi.fn((type: string, listener: (event: MessageEvent) => void) => { if (type === "message") workerMessage = listener; }),
      removeEventListener: vi.fn(),
    } });
    vi.stubGlobal("Notification", {
      get permission() { return permission; },
      requestPermission,
    });
    api.getSettings.mockResolvedValue(settings);
    api.getWebPushConfig.mockResolvedValue({ enabled: true, public_key: "AQID" });
    api.list.mockResolvedValue({ notifications: [], unread_count: 2 });
    api.registerWebPushSubscription.mockResolvedValue({ subscription: { id: "wps_1" } });
    api.updateSettings.mockImplementation(async (input) => ({ ...settings, ...input }));
    api.deleteWebPushSubscription.mockResolvedValue({ ok: true });
    api.markRead.mockResolvedValue({ ok: true, unread_count: 1 });
  });

  afterEach(() => {
	window.history.replaceState(null, "", "/");
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("offers once after sign-in without prompting until Enable is clicked", async () => {
    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);

    expect(await screen.findByRole("dialog", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(requestPermission).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(screen.queryByRole("dialog", { name: "Turn on notifications" })).not.toBeInTheDocument();
    expect(localStorage.getItem("hank-notifications-invitation:usr_1")).toBe("dismissed");
  });

  it("enrolls and uploads only after the user grants permission", async () => {
    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable notifications" }));

    expect(requestPermission).toHaveBeenCalledOnce();
    await waitFor(() => expect(subscribe).toHaveBeenCalledOnce());
    expect(api.registerWebPushSubscription).toHaveBeenCalledWith({
      endpoint: "https://push.example/new",
      keys: { p256dh: "p", auth: "a" },
      browser_label: expect.any(String),
    });
    expect(await screen.findByTestId("state")).toHaveTextContent("ready:subscribed:2");
  });

  it("shows denied and server-disabled states without an invitation", async () => {
    permission = "denied";
    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
    expect(await screen.findByTestId("state")).toHaveTextContent("ready:denied");
    expect(screen.queryByRole("dialog", { name: "Turn on notifications" })).not.toBeInTheDocument();

    cleanup();
    api.getWebPushConfig.mockResolvedValue({ enabled: false });
    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
    expect(await screen.findByTestId("state")).toHaveTextContent("ready:disabled-server");
    expect(screen.queryByRole("dialog", { name: "Turn on notifications" })).not.toBeInTheDocument();
  });

  it("loads account settings without waiting for a service worker when permission is denied", async () => {
    permission = "denied";
    const serviceWorker = {
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    };
    Object.defineProperty(serviceWorker, "ready", {
      get: () => { throw new Error("service worker readiness should not be read"); },
    });
    Object.defineProperty(window.navigator, "serviceWorker", { configurable: true, value: serviceWorker });

    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);

    expect(await screen.findByTestId("state")).toHaveTextContent("ready:denied:2:usr_1");
    expect(api.getSettings).toHaveBeenCalledOnce();
  });

  it("updates account-wide categories while retaining inbox delivery", async () => {
    function Toggle() {
      const notifications = useNotifications();
      return <button onClick={() => void notifications.updateCategory("notes", false)}>Disable notes</button>;
    }
    render(<NotificationProvider userID="usr_1"><Toggle /></NotificationProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Disable notes" }));
    await waitFor(() => expect(api.updateSettings).toHaveBeenCalledWith({ notes: false }));
  });

  it("marks a pushed notification read when the service worker opens it", async () => {
    render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
    await screen.findByTestId("state");
    workerMessage?.(new MessageEvent("message", { data: { type: "HANK_NOTIFICATION_OPEN", notificationId: "ntf_1" } }));
    await waitFor(() => expect(api.markRead).toHaveBeenCalledWith("ntf_1"));
    expect(await screen.findByTestId("state")).toHaveTextContent("ready:available:1");
  });

  it("marks a cold-click notification read from the bounded URL handoff", async () => {
	window.history.replaceState(null, "", "/dashboard?hank_notification_id=ntf_cold&keep=1");
	render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
	await waitFor(() => expect(api.markRead).toHaveBeenCalledWith("ntf_cold"));
	expect(window.location.search).toBe("?keep=1");
  });

  it("does not restore prior-account state after the authenticated user is cleared", async () => {
    let resolveSettings: (value: typeof settings) => void = () => undefined;
    api.getSettings.mockReturnValueOnce(new Promise((resolve) => { resolveSettings = resolve; }));
    const { rerender } = render(<NotificationProvider userID="usr_1"><StateProbe /></NotificationProvider>);
    rerender(<NotificationProvider><StateProbe /></NotificationProvider>);
    await act(async () => { resolveSettings(settings); });
    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("idle:unsupported:0:none"));
  });
});
