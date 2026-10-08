import { ApiError, apiClient, type ApiTransport } from "../api/client";

type AppTicketResponse = {
  ticket: string;
  expires_at: string;
  websocket_path: string;
};

type RoutedCommandEnvelope = {
  version: "v1";
  type: "app.command";
  request_id: string;
  timestamp: string;
  agent_id?: string;
  payload: {
    command: string;
    body: unknown;
  };
};

type AppEnvelope = {
  type: "app.response" | "app.error" | "app.event" | string;
  request_id?: string;
  payload?: unknown;
  error?: {
    code: string;
    message: string;
    details?: unknown;
  };
};

export type HankSocketEvent = {
  topic?: string;
  event?: string;
  body?: unknown;
};

type PendingCommand = {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
  timeoutID: number;
};

export class HankSocketError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly details?: unknown,
  ) {
    super(message);
    this.name = "HankSocketError";
  }
}

export class HankSocket {
  private socket: WebSocket | null = null;
  private connecting: Promise<void> | null = null;
  private generation = 0;
  private stopped = false;
  private hasConnected = false;
  private retryTimer: number | null = null;
  private retryAttempt = 0;
  private topics = new Set<string>();
  private pending = new Map<string, PendingCommand>();
  private listeners = new Set<(event: HankSocketEvent) => void>();

  constructor(
    private readonly api: ApiTransport = apiClient,
    private readonly createWebSocket: (url: string) => WebSocket = (url) => new WebSocket(url),
  ) {}

  connect(): Promise<void> {
    this.stopped = false;
    if (this.connecting) return this.connecting;
    if (this.socket?.readyState === WebSocket.OPEN) return Promise.resolve();
    if (this.retryTimer !== null) window.clearTimeout(this.retryTimer);
    this.retryTimer = null;
    const generation = ++this.generation;
    const connecting = this.openConnection(generation);
    this.connecting = connecting;
    void connecting.then(
      () => { if (this.connecting === connecting) this.connecting = null; },
      (error: unknown) => {
        if (this.connecting === connecting) this.connecting = null;
        if (generation !== this.generation) return;
        if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
          this.stopped = true;
          return;
        }
        this.scheduleReconnect();
      },
    );
    return connecting;
  }

  private async openConnection(generation: number): Promise<void> {
    const ticket = await this.api.request<AppTicketResponse>("/v1/ws/app-ticket", { method: "POST", timeoutMs: 10000 });
    if (this.stopped || generation !== this.generation) throw new HankSocketError("socket_closed", "Hank socket closed.");
    const socket = this.createWebSocket(this.websocketURL(ticket.websocket_path));
    this.socket = socket;
    socket.addEventListener("message", (event) => {
      if (this.socket === socket) this.handleMessage(event);
    });
    try {
      await new Promise<void>((resolve, reject) => {
        const timeout = window.setTimeout(() => reject(new HankSocketError("socket_open_timeout", "Hank socket did not open.")), 10000);
        const fail = () => {
          window.clearTimeout(timeout);
          reject(new HankSocketError("socket_closed", "Hank socket closed."));
          this.disconnected(socket);
        };
        socket.addEventListener("open", () => { window.clearTimeout(timeout); resolve(); }, { once: true });
        socket.addEventListener("close", fail);
        socket.addEventListener("error", fail);
      });
      if (this.socket !== socket) throw new HankSocketError("socket_closed", "Hank socket closed.");
      if (this.topics.size > 0) {
        await this.restoreSubscriptions(socket);
      }
      if (this.socket !== socket) throw new HankSocketError("socket_closed", "Hank socket closed.");
      this.retryAttempt = 0;
      const reconnect = this.hasConnected;
      this.hasConnected = true;
      if (reconnect) this.emit({ event: "socket.reconnected" });
    } catch (error) {
      this.disconnected(socket);
      throw error;
    }
  }

  private async restoreSubscriptions(socket: WebSocket) {
    try {
      await this.sendOnSocket(socket, "app.subscribe", { topics: [...this.topics] }, {});
    } catch (error) {
      if (!(error instanceof HankSocketError) || error.code !== "permission_denied") throw error;
      // A closed terminal or revoked scope must not prevent unrelated topics
      // from recovering. The server reauthorizes every individual topic.
      for (const topic of this.topics) {
        try { await this.sendOnSocket(socket, "app.subscribe", { topics: [topic] }, {}); }
        catch (error) {
          if (error instanceof HankSocketError && error.code === "permission_denied") this.topics.delete(topic);
          else throw error;
        }
      }
    }
  }

  private disconnected(socket: WebSocket) {
    if (this.socket !== socket) return;
    this.socket = null;
    this.rejectAll(new HankSocketError("socket_closed", "Connection lost. The command was not retried; check its status before retrying."));
    socket.close();
    this.scheduleReconnect();
  }

  private scheduleReconnect() {
    if (this.stopped || this.retryTimer !== null || (this.topics.size === 0 && this.listeners.size === 0)) return;
    const delay = Math.min(30000, 1000 * 2 ** Math.min(this.retryAttempt++, 5));
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = null;
      if (!this.stopped) void this.connect().catch(() => undefined);
    }, delay);
  }

  close() {
    this.stopped = true;
    this.generation++;
    if (this.retryTimer !== null) window.clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.connecting = null;
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.topics.clear();
    this.hasConnected = false;
    this.rejectAll(new HankSocketError("socket_closed", "Hank socket closed."));
  }

  async sendCommand<T>(command: string, body: unknown = {}, options: { timeoutMs?: number; agentID?: string } = {}): Promise<T> {
    await this.connect();
    const socket = this.socket;
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      throw new HankSocketError("socket_not_open", "Hank socket is not open.");
    }
    return this.sendOnSocket<T>(socket, command, body, options);
  }

  private sendOnSocket<T>(socket: WebSocket, command: string, body: unknown, options: { timeoutMs?: number; agentID?: string }): Promise<T> {
    const requestID = `req_${Date.now()}_${Math.random().toString(16).slice(2)}`;
    const envelope: RoutedCommandEnvelope = {
      version: "v1",
      type: "app.command",
      request_id: requestID,
      timestamp: new Date().toISOString(),
      payload: { command, body },
    };
    if (options.agentID) {
      // Target a specific agent (blank routes to the home's primary agent).
      envelope.agent_id = options.agentID;
    }
    const timeoutMs = options.timeoutMs ?? 30000;
    return new Promise<T>((resolve, reject) => {
      const timeoutID = window.setTimeout(() => {
        this.pending.delete(requestID);
        reject(new HankSocketError("command_timeout", `${command} timed out.`));
      }, timeoutMs);
      this.pending.set(requestID, {
        resolve: (value) => resolve(value as T),
        reject,
        timeoutID,
      });
      try {
        socket.send(JSON.stringify(envelope));
      } catch (error) {
        this.pending.delete(requestID);
        window.clearTimeout(timeoutID);
        reject(error);
      }
    });
  }

  async subscribe(topics: string[], options?: { timeoutMs?: number }) {
    const result = await this.sendCommand("app.subscribe", { topics }, options);
    for (const topic of topics) this.topics.add(topic);
    if (!this.socket) this.scheduleReconnect();
    return result;
  }

  onEvent(listener: (event: HankSocketEvent) => void) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private handleMessage(message: MessageEvent) {
    let envelope: AppEnvelope;
    try { envelope = JSON.parse(String(message.data)) as AppEnvelope; } catch { return; }
    if (!envelope || typeof envelope !== "object") return;
    if (envelope.type === "app.event") {
      if (envelope.payload && typeof envelope.payload === "object") this.emit(envelope.payload as HankSocketEvent);
      return;
    }
    if ((envelope.type !== "app.response" && envelope.type !== "app.error") || !envelope.request_id) return;
    const pending = this.pending.get(envelope.request_id);
    if (!pending) return;
    this.pending.delete(envelope.request_id);
    window.clearTimeout(pending.timeoutID);
    if (envelope.type === "app.error") {
      pending.reject(
        new HankSocketError(
          envelope.error?.code || "app_error",
          envelope.error?.message || "App command failed.",
          envelope.error?.details,
        ),
      );
      return;
    }
    pending.resolve(envelope.payload);
  }

  private emit(event: HankSocketEvent) {
    for (const listener of this.listeners) {
      // A subscriber failure must not interrupt other subscribers or reconnect.
      try { listener(event); } catch { /* Subscriber owns its UI error state. */ }
    }
  }

  private rejectAll(error: Error) {
    for (const pending of this.pending.values()) {
      window.clearTimeout(pending.timeoutID);
      pending.reject(error);
    }
    this.pending.clear();
  }

  private websocketURL(path: string) {
    const url = new URL(path, window.location.origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    return url.toString();
  }
}
