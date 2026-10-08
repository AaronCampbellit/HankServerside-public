import type { DesktopSessionAuthorization } from "../api/desktop";
import { DesktopFrameKind, DesktopMessageType, decodeDesktopDataFrame, decodeDesktopInnerMessage, encodeDesktopDataFrame, encodeDesktopInnerMessage, type DesktopInnerMessage } from "./protocol";
import { DesktopQualityController, type DesktopHealthSample, type DesktopQualityName } from "./qualityController";

export type DesktopViewerState = "idle"|"authorizing"|"joining"|"active"|"reconnecting"|"ending"|"ended"|"error";
export interface DesktopSocketCallbacks { onMessage(message: DesktopInnerMessage): void; onState(state: DesktopViewerState, reason?: string): void }
interface RecordLayerLike { decrypt(record: Uint8Array): Promise<Uint8Array>; encrypt(plaintext: Uint8Array): Promise<Uint8Array> }
export interface DesktopHandshakeDriver { browserHandshakeFrame(session: DesktopSessionAuthorization): Promise<Uint8Array>; completeAgentHandshake(payload: Uint8Array): Promise<RecordLayerLike> }
interface WebSocketLike { binaryType: string; readyState: number; readonly bufferedAmount?: number; onopen: ((event: Event) => unknown) | null; onmessage: ((event: MessageEvent) => unknown) | null; onclose: ((event: CloseEvent) => unknown) | null; onerror: ((event: Event) => unknown) | null; send(value: string | ArrayBufferLike | Blob | ArrayBufferView): void; close(code?: number, reason?: string): void }
interface OutboundQueue { records: RecordLayerLike; socket: WebSocketLike; generation: number; tail: Promise<void>; failure?: Error }
interface InboundQueue { socket: WebSocketLike; generation: number; tail: Promise<void>; failure?: Error }

export class DesktopSocket {
  private socket: WebSocketLike | null = null; private records: RecordLayerLike | null = null; private outbound: OutboundQueue | null = null; private inbound: InboundQueue | null = null;
  private active = false; private connectionGeneration = 0;
  private quality = new DesktopQualityController();
  constructor(private callbacks: DesktopSocketCallbacks, private driver: DesktopHandshakeDriver, private factory: (url: string) => WebSocketLike = url => new WebSocket(url), private origin = window.location.origin) {}

  start(session: DesktopSessionAuthorization): Promise<void> {
    if (!session.websocket_path.startsWith("/ws/desktop/browser/") || /[?&](token|credential)=/i.test(session.websocket_path)) return Promise.reject(new Error("desktop_websocket_path_invalid"));
    this.callbacks.onState("authorizing");
    const url = new URL(session.websocket_path, this.origin); url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const generation = ++this.connectionGeneration;
    return new Promise((resolve, reject) => {
      const socket = this.factory(url.toString()); this.socket = socket; this.inbound = { socket, generation, tail: Promise.resolve() }; socket.binaryType = "arraybuffer";
      socket.onopen = () => { if (!this.current(socket, generation)) return; this.callbacks.onState("joining"); void this.driver.browserHandshakeFrame(session).then(payload => { if (!this.current(socket, generation)) throw new Error("desktop_socket_not_active"); socket.send(encodeDesktopDataFrame(DesktopFrameKind.BrowserHandshake, payload)); }).catch(reject); };
      socket.onmessage = event => { if (!this.current(socket, generation)) return; void this.enqueueReceive(event.data, socket, generation).then(becameActive => { if (becameActive) resolve(); }).catch(error => { if (this.current(socket, generation)) { this.callbacks.onState("error", error instanceof Error ? error.message : "desktop_socket_error"); socket.close(1008, "desktop protocol error"); } reject(error); }); };
      socket.onerror = () => { if (!this.current(socket, generation)) return; const error = new Error("desktop_transport_error"); this.callbacks.onState("error", error.message); reject(error); };
      socket.onclose = event => { if (!this.current(socket, generation)) return; const wasActive = this.active; ++this.connectionGeneration; this.socket = null; this.inbound = null; this.outbound = null; this.records = null; this.active = false; if (wasActive) this.callbacks.onState("reconnecting", stableCloseReason(event.reason)); };
    });
  }

  async send(message: DesktopInnerMessage): Promise<void> {
    const queue = this.outbound;
    if (!queue || !this.active || !this.current(queue.socket, queue.generation)) throw new Error("desktop_socket_not_active");
    const plaintext = encodeDesktopInnerMessage(message.type, message.flags, message.payload);
    const operation = queue.tail.then(async () => {
      if (queue.failure) throw queue.failure;
      if (this.outbound !== queue || !this.active || !this.current(queue.socket, queue.generation)) throw new Error("desktop_socket_not_active");
      const record = await queue.records.encrypt(plaintext);
      if (this.outbound !== queue || !this.active || !this.current(queue.socket, queue.generation)) throw new Error("desktop_socket_not_active");
      queue.socket.send(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, record));
    });
    queue.tail = operation.catch(error => { queue.failure ??= error instanceof Error ? error : new Error("desktop_outbound_send_failed"); });
    return operation;
  }
  requestMaximumQuality(level: DesktopQualityName, atMS = Date.now()): Promise<void> {
    const decision = this.quality.setMaximum(level, atMS);
    return decision ? this.sendQuality(decision) : Promise.resolve();
  }
  reportHealth(sample: DesktopHealthSample): Promise<void> {
    const decision = this.quality.observe({ ...sample, senderQueueBytes: Math.max(sample.senderQueueBytes, this.socket?.bufferedAmount ?? 0) });
    return decision ? this.sendQuality(decision) : Promise.resolve();
  }
  async reconnect(session: DesktopSessionAuthorization): Promise<void> {
    const socket = this.socket; ++this.connectionGeneration; this.inbound = null; this.outbound = null; this.records = null; this.active = false; this.socket = null;
    socket?.close(1000, "new epoch"); return this.start(session);
  }
  async close(reason: string): Promise<void> {
    const socket = this.socket; ++this.connectionGeneration; this.active = false; this.inbound = null; this.outbound = null; this.socket = null; this.records = null;
    socket?.close(1000, reason); this.callbacks.onState("ended", reason);
  }

  private async receive(raw: unknown, socket: WebSocketLike, generation: number, queue: InboundQueue, records: RecordLayerLike | null): Promise<boolean> {
    if (!(raw instanceof ArrayBuffer)) throw new Error("desktop_binary_frame_required");
    const frame = decodeDesktopDataFrame(new Uint8Array(raw));
    if (!records) {
      if (frame.kind !== DesktopFrameKind.AgentHandshake) throw new Error("desktop_agent_handshake_required");
      const completed = await this.driver.completeAgentHandshake(frame.payload);
      if (this.inbound !== queue || this.records !== records || !this.current(socket, generation)) throw new Error("desktop_socket_not_active");
      this.records = completed; this.outbound = { records: completed, socket, generation, tail: Promise.resolve() }; this.active = true;
      await this.sendQuality(this.quality.reset());
      if (!this.currentReceive(queue, completed, socket, generation)) throw new Error("desktop_socket_not_active");
      this.callbacks.onState("active"); return true;
    }
    if (frame.kind !== DesktopFrameKind.EncryptedRecord) throw new Error("desktop_encrypted_record_required");
    const plaintext = await records.decrypt(frame.payload);
    if (!this.currentReceive(queue, records, socket, generation)) throw new Error("desktop_socket_not_active");
    const message = decodeDesktopInnerMessage(plaintext);
    if (!message.unknownOptional) this.callbacks.onMessage(message);
    if (message.type === DesktopMessageType.Ping) {
      if (!this.currentReceive(queue, records, socket, generation)) throw new Error("desktop_socket_not_active");
      await this.send({ ...message, type: DesktopMessageType.Pong });
    }
    return false;
  }

  private enqueueReceive(raw: unknown, socket: WebSocketLike, generation: number): Promise<boolean> {
    const queue = this.inbound;
    if (!queue || queue.socket !== socket || queue.generation !== generation) return Promise.reject(new Error("desktop_socket_not_active"));
    const operation = queue.tail.then(async () => {
      if (queue.failure) throw queue.failure;
      if (this.inbound !== queue || !this.current(socket, generation)) throw new Error("desktop_socket_not_active");
      const records = this.records;
      return this.receive(raw, socket, generation, queue, records);
    });
    queue.tail = operation.then(() => undefined).catch(error => { queue.failure ??= error instanceof Error ? error : new Error("desktop_inbound_receive_failed"); });
    return operation;
  }

  private currentReceive(queue: InboundQueue, records: RecordLayerLike, socket: WebSocketLike, generation: number): boolean {
    return this.inbound === queue && this.records === records && this.active && this.current(socket, generation);
  }
  private sendQuality(value: { level: DesktopQualityName; generation: number; forceKeyframe: true }): Promise<void> {
    return this.send({ version: 1, type: DesktopMessageType.Quality, flags: 0, payload: new TextEncoder().encode(JSON.stringify({ profile: value.level, generation: value.generation, force_keyframe: value.forceKeyframe })), unknownOptional: false });
  }
  private current(socket: WebSocketLike, generation: number): boolean { return this.socket === socket && this.connectionGeneration === generation; }
}

function stableCloseReason(value: string): string {
  return ["join_timeout", "reconnect_timeout", "slow_consumer", "rate_limit", "frame_limit", "idle_timeout", "hard_expired", "revoked", "user_ended", "agent_ended", "local_ended"].includes(value) ? value : "transport_closed";
}
