/// <reference lib="webworker" />

import { cleanupOutdatedCaches, matchPrecache, precacheAndRoute } from "workbox-precaching";
import { handleNotificationClick, handlePushPayload, type NotificationWorkerScope } from "./push";
import { requestPolicyFor } from "./requestPolicy";

declare const self: ServiceWorkerGlobalScope & {
  readonly __WB_MANIFEST: Parameters<typeof precacheAndRoute>[0];
};

cleanupOutdatedCaches();
precacheAndRoute(self.__WB_MANIFEST);

self.addEventListener("message", (event) => {
  if (event.data?.type === "SKIP_WAITING") {
    void self.skipWaiting();
  }
});

self.addEventListener("push", (event) => {
  let payload: unknown;
  try {
    payload = event.data?.json();
  } catch {
    return;
  }
  event.waitUntil(handlePushPayload(self as unknown as NotificationWorkerScope, payload));
});

self.addEventListener("notificationclick", (event) => {
  event.waitUntil(handleNotificationClick(self as unknown as NotificationWorkerScope, event.notification));
});

self.addEventListener("fetch", (event) => {
  const policy = requestPolicyFor(event.request);
  if (policy === "network-only") return;
  event.respondWith(
    fetch(event.request).catch(async () => {
      const fallback = policy === "dashboard-navigation"
        ? await matchPrecache("/index.html") ?? await matchPrecache("/offline.html")
        : await matchPrecache("/offline.html");
      return fallback ?? new Response("Hank is offline.", {
        status: 503,
        headers: { "Content-Type": "text/plain; charset=utf-8" },
      });
    }),
  );
});
