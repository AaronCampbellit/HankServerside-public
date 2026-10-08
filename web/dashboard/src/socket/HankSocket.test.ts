import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, type ApiTransport } from "../api/client";
import { HankSocket } from "./HankSocket";

class FakeWebSocket extends EventTarget {
  static OPEN = 1;
  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];

  constructor(public readonly url: string) {
    super();
  }

  send(message: string) {
    this.sent.push(message);
  }

  close() {
    this.readyState = 3;
    this.dispatchEvent(new Event("close"));
  }

  receive(payload: unknown) {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(payload) }));
  }

  open() {
    this.readyState = 1;
    this.dispatchEvent(new Event("open"));
  }
}

describe("HankSocket", () => {
  it("opens with an app ticket and sends command envelopes", async () => {
    const requests: Array<[string, unknown]> = [];
    const api = {
      async request<T>(path: string, options?: unknown) {
        requests.push([path, options]);
        return {
          ticket: "ticket",
          expires_at: "2026-06-27T00:00:00Z",
          websocket_path: "/ws/app?app_ticket=ticket",
        } as T;
      },
    };
    const sockets: FakeWebSocket[] = [];
    const hankSocket = new HankSocket(api, (url) => {
      const socket = new FakeWebSocket(url);
      sockets.push(socket);
      queueMicrotask(() => socket.open());
      return socket as unknown as WebSocket;
    });

    const response = hankSocket.sendCommand<{ ok: boolean }>("files.list", { path: "/" });
    await vi.waitFor(() => expect(sockets[0]?.sent.length).toBe(1));
    const socket = sockets[0];
    const envelope = JSON.parse(socket.sent[0] || "{}");
    expect(requests).toEqual([["/v1/ws/app-ticket", { method: "POST", timeoutMs: 10000 }]]);
    expect(socket.url).toBe("ws://localhost:3000/ws/app?app_ticket=ticket");
    expect(envelope.type).toBe("app.command");
    expect(envelope.version).toBe("v1");
    expect(envelope.payload.command).toBe("files.list");

    socket.receive({ type: "app.response", request_id: envelope.request_id, payload: { ok: true } });
    await expect(response).resolves.toEqual({ ok: true });
  });

  it("emits app events", async () => {
    const api = {
      async request<T>() {
        return {
          ticket: "ticket",
          expires_at: "2026-06-27T00:00:00Z",
          websocket_path: "/ws/app?app_ticket=ticket",
        } as T;
      },
    };
    const sockets: FakeWebSocket[] = [];
    const hankSocket = new HankSocket(api, (url) => {
      const socket = new FakeWebSocket(url);
      sockets.push(socket);
      queueMicrotask(() => socket.open());
      return socket as unknown as WebSocket;
    });
    const listener = vi.fn();
    hankSocket.onEvent(listener);

    await hankSocket.connect();
    sockets[0].receive({ type: "app.event", payload: { topic: "files.jobs", event: "files.job_changed" } });

    expect(listener).toHaveBeenCalledWith({ topic: "files.jobs", event: "files.job_changed" });
  });
});

function reconnectFixture(autoOpen = true) {
  const sockets: FakeWebSocket[] = [];
  const api = { request: vi.fn(async <T,>() => ({ websocket_path: "/ws/app?app_ticket=fresh" }) as T) };
  const client = new HankSocket(api as ApiTransport, (url) => {
    const socket = new FakeWebSocket(url);
    socket.readyState = 0;
    const send = socket.send.bind(socket);
    socket.send = (message) => {
      send(message);
      const envelope = JSON.parse(message);
      if (envelope.payload.command === "app.subscribe") {
        queueMicrotask(() => socket.receive({ type: "app.response", request_id: envelope.request_id, payload: { topics: envelope.payload.body.topics } }));
      }
    };
    sockets.push(socket);
    if (autoOpen) queueMicrotask(() => socket.open());
    return socket as unknown as WebSocket;
  });
  return { api, client, sockets };
}

afterEach(() => vi.useRealTimers());

describe("HankSocket recovery", () => {
  it("shares concurrent opens and ignores malformed or unrelated responses", async () => {
    const { client, sockets, api } = reconnectFixture();
    const first = client.sendCommand("files.list");
    const second = client.sendCommand("files.stat");
    await vi.waitFor(() => expect(sockets[0]?.sent).toHaveLength(2));
    expect(api.request).toHaveBeenCalledTimes(1);
    expect(sockets).toHaveLength(1);
    const requests = sockets[0].sent.map((item) => JSON.parse(item));
    sockets[0].dispatchEvent(new MessageEvent("message", { data: "not JSON" }));
    sockets[0].receive(null);
    sockets[0].receive({ type: "unknown", request_id: requests[0].request_id, payload: "bad" });
    for (const request of requests) sockets[0].receive({ type: "app.response", request_id: request.request_id, payload: request.payload.command });
    await expect(first).resolves.toBe("files.list");
    await expect(second).resolves.toBe("files.stat");
    client.close();
  });

  it("restores subscriptions, refreshes listeners, and never replays interrupted writes", async () => {
    vi.useFakeTimers();
    const { client, sockets } = reconnectFixture();
    const listener = vi.fn();
    client.onEvent(listener);
    await client.subscribe(["files.jobs", "agents.health"]);
    const writing = client.sendCommand("host.restart", {}, { agentID: "agent" });
    const rejected = expect(writing).rejects.toThrow("not retried");
    await vi.advanceTimersByTimeAsync(0);
    sockets[0].close();
    await rejected;
    await vi.advanceTimersByTimeAsync(1000);
    expect(sockets).toHaveLength(2);
    expect(sockets[1].sent.map((item) => JSON.parse(item).payload)).toEqual([{ command: "app.subscribe", body: { topics: ["files.jobs", "agents.health"] } }]);
    expect(listener).toHaveBeenCalledWith({ event: "socket.reconnected" });
    const reading = client.sendCommand("files.list");
    await vi.advanceTimersByTimeAsync(0);
    const request = JSON.parse(sockets[1].sent[1]);
    sockets[0].close();
    sockets[0].receive({ type: "app.response", request_id: request.request_id, payload: "stale" });
    sockets[1].receive({ type: "app.response", request_id: request.request_id, payload: "fresh" });
    await expect(reading).resolves.toBe("fresh");
    client.close();
    await vi.advanceTimersByTimeAsync(60000);
    expect(sockets).toHaveLength(2);
  });

  it("rejects a socket closed before opening and bounds an open that never completes", async () => {
    vi.useFakeTimers();
    const { client, sockets } = reconnectFixture(false);
    const connecting = client.connect();
    const rejected = expect(connecting).rejects.toThrow("closed");
    await vi.advanceTimersByTimeAsync(0);
    sockets[0].close();
    await rejected;
    const retry = client.connect();
    const timedOut = expect(retry).rejects.toThrow("did not open");
    await vi.advanceTimersByTimeAsync(10000);
    await timedOut;
    expect(sockets[1].readyState).toBe(3);
    client.close();
  });

  it("does not open a late ticket after explicit close", async () => {
    const { api, client, sockets } = reconnectFixture();
    let release!: (value: unknown) => void;
    api.request.mockImplementationOnce(() => new Promise((resolve) => { release = resolve; }));
    const connecting = client.connect();
    const rejected = expect(connecting).rejects.toThrow("closed");
    client.close();
    release({ websocket_path: "/ws/app?app_ticket=late" });
    await rejected;
    expect(sockets).toHaveLength(0);
  });

  it("stops background retries when authentication has expired", async () => {
    vi.useFakeTimers();
    const { api, client, sockets } = reconnectFixture();
    await client.subscribe(["files.jobs"]);
    api.request.mockRejectedValue(new ApiError(401, "unauthorized", "Sign in again", null));
    sockets[0].close();
    await vi.advanceTimersByTimeAsync(60000);
    expect(api.request).toHaveBeenCalledTimes(2);
    expect(sockets).toHaveLength(1);
    client.close();
  });
});
