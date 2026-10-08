import { describe, expect, it, vi } from "vitest";
import { subscribeToWebPush, unsubscribeFromWebPush, vapidPublicKeyBytes, webPushCapability } from "./push";

describe("Web Push browser helpers", () => {
  it("decodes a base64url VAPID public key", () => {
    expect([...vapidPublicKeyBytes("AQID-_8")]).toEqual([1, 2, 3, 251, 255]);
  });

  it("reports unsupported and denied browser states", () => {
    expect(webPushCapability(undefined, undefined)).toBe("unsupported");
    expect(webPushCapability({ pushManager: {} } as ServiceWorkerRegistration, { permission: "denied" } as typeof Notification)).toBe("denied");
  });

  it("reuses an existing subscription without enrolling again", async () => {
    const existing = { endpoint: "https://push.example/existing" } as PushSubscription;
    const subscribe = vi.fn();
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(existing), subscribe } } as unknown as ServiceWorkerRegistration;

    await expect(subscribeToWebPush(registration, "AQID", { permission: "granted" } as typeof Notification)).resolves.toBe(existing);
    expect(subscribe).not.toHaveBeenCalled();
  });

  it("requires caller-controlled permission before subscribing", async () => {
    const subscribe = vi.fn();
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(null), subscribe } } as unknown as ServiceWorkerRegistration;

    await expect(subscribeToWebPush(registration, "AQID", { permission: "default" } as typeof Notification)).rejects.toThrow("permission");
    expect(subscribe).not.toHaveBeenCalled();
  });

  it("subscribes with user visibility and the application server key", async () => {
    const created = { endpoint: "https://push.example/new" } as PushSubscription;
    const subscribe = vi.fn().mockResolvedValue(created);
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(null), subscribe } } as unknown as ServiceWorkerRegistration;

    await expect(subscribeToWebPush(registration, "AQID", { permission: "granted" } as typeof Notification)).resolves.toBe(created);
    expect(subscribe).toHaveBeenCalledWith({ userVisibleOnly: true, applicationServerKey: new Uint8Array([1, 2, 3]) });
  });

  it("replaces a stale subscription created with a different VAPID key", async () => {
    const staleUnsubscribe = vi.fn().mockResolvedValue(true);
    const stale = {
      options: { applicationServerKey: new Uint8Array([9, 9, 9]).buffer },
      unsubscribe: staleUnsubscribe,
    } as unknown as PushSubscription;
    const replacement = { endpoint: "https://push.example/replacement" } as PushSubscription;
    const subscribe = vi.fn().mockResolvedValue(replacement);
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(stale), subscribe } } as unknown as ServiceWorkerRegistration;

    await expect(subscribeToWebPush(registration, "AQID", { permission: "granted" } as typeof Notification)).resolves.toBe(replacement);
    expect(staleUnsubscribe).toHaveBeenCalledOnce();
    expect(subscribe).toHaveBeenCalledWith({ userVisibleOnly: true, applicationServerKey: new Uint8Array([1, 2, 3]) });
  });

  it("unsubscribes an enrolled browser and tolerates an empty registration", async () => {
    const unsubscribe = vi.fn().mockResolvedValue(true);
    const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue({ unsubscribe }) } } as unknown as ServiceWorkerRegistration;
    await expect(unsubscribeFromWebPush(registration)).resolves.toBe(true);
    expect(unsubscribe).toHaveBeenCalledOnce();

    const empty = { pushManager: { getSubscription: vi.fn().mockResolvedValue(null) } } as unknown as ServiceWorkerRegistration;
    await expect(unsubscribeFromWebPush(empty)).resolves.toBe(false);
  });
});
