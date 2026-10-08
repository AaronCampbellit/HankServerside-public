import { describe, expect, it, vi } from "vitest";
import { DesktopFrameKind, DesktopMessageType, decodeDesktopDataFrame, decodeDesktopInnerMessage, encodeDesktopDataFrame, encodeDesktopInnerMessage, type DesktopInnerMessage } from "./protocol";
import { DesktopSocket } from "./DesktopSocket";
import { exactBuffer } from "./base64url";
import { DesktopInputController } from "./inputController";
import type { DesktopSessionAuthorization } from "../api/desktop";

const session: DesktopSessionAuthorization = { session_id: "desk_1", agent_id: "agent_1", state: "offered", websocket_path: "/ws/desktop/browser/desk_1", key_epoch: 1 };
const message = (type: DesktopMessageType, value: unknown = {}): DesktopInnerMessage => ({ version: 1, flags: 0, type, payload: new TextEncoder().encode(JSON.stringify(value)), unknownOptional: false });

describe("DesktopSocket", () => {
  it("constructs a credential-free URL and dispatches decrypted binary records", async () => {
    const socket = new FakeWebSocket();
    const onMessage = vi.fn(), onState = vi.fn();
    const record = { decrypt: vi.fn().mockResolvedValue(encodeDesktopInnerMessage(DesktopMessageType.Statistics, 0, new TextEncoder().encode('{"rtt_ms":8}'))), encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)) };
    const driver = { browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockResolvedValue(record) };
    const desktop = new DesktopSocket({ onMessage, onState }, driver, (url) => { expect(url).toBe("wss://hank.example/ws/desktop/browser/desk_1"); return socket as never; }, "https://hank.example");
    const started = desktop.start(session);
    socket.open();
    await vi.waitFor(() => expect(socket.sent).toHaveLength(1));
    socket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.AgentHandshake, new Uint8Array([2]))));
    await vi.waitFor(() => expect(onState).toHaveBeenCalledWith("active"));
    socket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, new Uint8Array([3]))));
    await vi.waitFor(() => expect(onMessage).toHaveBeenCalledWith(expect.objectContaining({ type: DesktopMessageType.Statistics })));
    await started;
  });

  it("sends the canonical endpoint quality profile field", async () => {
    const socket = new FakeWebSocket(); let plaintext: Uint8Array | undefined;
    const record = { decrypt: vi.fn(), encrypt: vi.fn(async (value: Uint8Array) => { plaintext = value; return new Uint8Array(18); }) };
    const desktop = new DesktopSocket({ onMessage: vi.fn(), onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockResolvedValue(record),
    }, () => socket as never, "https://hank.example");
    await activate(desktop, socket); plaintext = undefined;
    await desktop.requestMaximumQuality("low");
    const inner = decodeDesktopInnerMessage(plaintext!);
    expect(inner.type).toBe(DesktopMessageType.Quality);
    expect(JSON.parse(new TextDecoder().decode(inner.payload))).toEqual({ profile: "low", generation: 2, force_keyframe: true });
  });

  it("sends a balanced keyframe reset after every completed handshake", async () => {
    const firstSocket = new FakeWebSocket(), secondSocket = new FakeWebSocket();
    const plaintext: Uint8Array[] = [];
    const records = [1, 2].map(() => ({ decrypt: vi.fn(), encrypt: vi.fn(async (value: Uint8Array) => { plaintext.push(value); return new Uint8Array(18); }) }));
    const sockets = [firstSocket, secondSocket];
    const desktop = new DesktopSocket({ onMessage: vi.fn(), onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockImplementation(() => Promise.resolve(records.shift())),
    }, () => sockets.shift() as never, "https://hank.example");
    await activate(desktop, firstSocket);
    const reconnecting = desktop.reconnect({ ...session, key_epoch: 2 }); secondSocket.open();
    await vi.waitFor(() => expect(secondSocket.sent).toHaveLength(1));
    secondSocket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.AgentHandshake, new Uint8Array([2]))));
    await reconnecting;
    expect(plaintext.map(value => JSON.parse(new TextDecoder().decode(decodeDesktopInnerMessage(value).payload)))).toEqual([
      { profile: "balanced", generation: 1, force_keyframe: true },
      { profile: "balanced", generation: 2, force_keyframe: true },
    ]);
  });

  it("serializes a blur release burst with unique monotonic records in call order", async () => {
    const socket = new FakeWebSocket();
    const pending: Array<() => void> = [], encryptedTypes: number[] = [];
    let sequence = 0n;
    const record = {
      decrypt: vi.fn(),
      encrypt: vi.fn((plaintext: Uint8Array) => new Promise<Uint8Array>(resolve => {
        const type = decodeDesktopInnerMessage(plaintext).type;
        if (type === DesktopMessageType.Quality) { const output = new Uint8Array(18); new DataView(output.buffer).setBigUint64(6, sequence++, false); resolve(output); return; }
        encryptedTypes.push(type);
        pending.push(() => {
          const output = new Uint8Array(18); new DataView(output.buffer).setBigUint64(6, sequence++, false); resolve(output);
        });
      })),
    };
    const desktop = new DesktopSocket({ onMessage: vi.fn(), onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockResolvedValue(record),
    }, () => socket as never, "https://hank.example");
    await activate(desktop, socket); socket.sent = []; sequence = 0n;

    const sends: Promise<void>[] = [];
    const controller = new DesktopInputController({
      send: (type, payload) => { const result = desktop.send(message(type, payload)); sends.push(result); void result.catch(() => undefined); },
      contentRect: () => new DOMRect(0, 0, 100, 100), requestFrame: () => 1, cancelFrame: vi.fn(),
    });
    controller.update({ active: true, control: true, reconnecting: false, visible: true, displayID: "display-1", generation: 1 });
    const lease = controller.focus(); controller.confirmFocus(lease, true);
    controller.key({ type: "keydown", code: "ShiftLeft", location: 1, repeat: false, shiftKey: true, ctrlKey: false, altKey: false, metaKey: false, preventDefault: vi.fn() } as unknown as KeyboardEvent);
    controller.pointer({ type: "pointerdown", clientX: 50, clientY: 50, button: 0, buttons: 1 } as PointerEvent);
    controller.blur();

    await vi.waitFor(() => expect(record.encrypt).toHaveBeenCalledTimes(1));
    for (let index = 0; index < sends.length; index++) {
      await vi.waitFor(() => expect(pending).toHaveLength(1));
      pending.shift()?.();
    }
    await Promise.all(sends);
    expect(encryptedTypes).toEqual([
      DesktopMessageType.ControlMode, DesktopMessageType.Keyboard, DesktopMessageType.Pointer,
      DesktopMessageType.Keyboard, DesktopMessageType.Pointer, DesktopMessageType.ControlMode,
    ]);
    const sequences = socket.sent.map(value => new DataView(decodeDesktopDataFrame(new Uint8Array(value as ArrayBuffer)).payload.buffer).getBigUint64(6, false));
    expect(sequences).toEqual([0n, 1n, 2n, 3n, 4n, 5n]);
  });

  it("poisons the ordered queue after a wire failure so later sends cannot overtake", async () => {
    const socket = new FakeWebSocket();
    const record = { decrypt: vi.fn(), encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)) };
    const desktop = new DesktopSocket({ onMessage: vi.fn(), onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockResolvedValue(record),
    }, () => socket as never, "https://hank.example");
    await activate(desktop, socket); record.encrypt.mockClear(); socket.failEncryptedSend = new Error("wire_failed");
    const first = desktop.send(message(DesktopMessageType.Keyboard));
    const second = desktop.send(message(DesktopMessageType.Pointer));
    await expect(first).rejects.toThrow("wire_failed");
    await expect(second).rejects.toThrow("wire_failed");
    expect(record.encrypt).toHaveBeenCalledTimes(1);
  });

  it("does not send stale encrypted work after close and starts a fresh reconnect queue", async () => {
    const firstSocket = new FakeWebSocket(), secondSocket = new FakeWebSocket();
    let releaseFirst: ((value: Uint8Array) => void) | undefined;
    const firstRecord = { decrypt: vi.fn(), encrypt: vi.fn((plaintext: Uint8Array) => decodeDesktopInnerMessage(plaintext).type === DesktopMessageType.Quality ? Promise.resolve(new Uint8Array(18)) : new Promise<Uint8Array>(resolve => { releaseFirst = resolve; })) };
    const secondRecord = { decrypt: vi.fn(), encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)) };
    const sockets = [firstSocket, secondSocket], records = [firstRecord, secondRecord];
    const desktop = new DesktopSocket({ onMessage: vi.fn(), onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockImplementation(() => Promise.resolve(records.shift())),
    }, () => sockets.shift() as never, "https://hank.example");
    await activate(desktop, firstSocket); firstSocket.sent = [];
    const stale = desktop.send(message(DesktopMessageType.Keyboard)); void stale.catch(() => undefined);
    await vi.waitFor(() => expect(releaseFirst).toBeDefined());
    const reconnecting = desktop.reconnect({ ...session, key_epoch: 2 } as never); secondSocket.open();
    await vi.waitFor(() => expect(secondSocket.sent).toHaveLength(1));
    secondSocket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.AgentHandshake, new Uint8Array([2]))));
    await reconnecting; secondRecord.encrypt.mockClear();
    releaseFirst?.(new Uint8Array(18));
    await expect(stale).rejects.toThrow("desktop_socket_not_active");
    expect(firstSocket.sent).toEqual([]);
    await expect(desktop.send(message(DesktopMessageType.Pointer))).resolves.toBeUndefined();
    expect(secondRecord.encrypt).toHaveBeenCalledTimes(1);
  });

  it("keeps asynchronous inbound record decryption in WebSocket delivery order", async () => {
    const socket = new FakeWebSocket(), onMessage = vi.fn();
    const pending: Array<(value: Uint8Array) => void> = [];
    const record = { encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)), decrypt: vi.fn(() => new Promise<Uint8Array>(resolve => pending.push(resolve))) };
    const desktop = new DesktopSocket({ onMessage, onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockResolvedValue(record),
    }, () => socket as never, "https://hank.example");
    await activate(desktop, socket);
    socket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, new Uint8Array([10]))));
    socket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, new Uint8Array([11]))));
    await vi.waitFor(() => expect(record.decrypt).toHaveBeenCalledTimes(1));
    pending.shift()?.(encodeDesktopInnerMessage(DesktopMessageType.Statistics, 0, new Uint8Array([1])));
    await vi.waitFor(() => expect(record.decrypt).toHaveBeenCalledTimes(2));
    pending.shift()?.(encodeDesktopInnerMessage(DesktopMessageType.PermissionState, 0, new Uint8Array([2])));
    await vi.waitFor(() => expect(onMessage).toHaveBeenCalledTimes(2));
    expect(onMessage.mock.calls.map(([value]) => value.type)).toEqual([DesktopMessageType.Statistics, DesktopMessageType.PermissionState]);
  });

  it.each([
    ["clipboard after reconnect", DesktopMessageType.ClipboardText, "reconnect"],
    ["terminate after close", DesktopMessageType.Terminate, "close"],
    ["ping after reconnect", DesktopMessageType.Ping, "reconnect"],
  ] as const)("drops stale delayed %s without callback or Pong", async (_name, staleType, transition) => {
    const firstSocket = new FakeWebSocket(), secondSocket = new FakeWebSocket(), onMessage = vi.fn();
    let releaseStale: ((value: Uint8Array) => void) | undefined;
    const firstRecord = { encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)), decrypt: vi.fn(() => new Promise<Uint8Array>(resolve => { releaseStale = resolve; })) };
    const secondRecord = { encrypt: vi.fn().mockResolvedValue(new Uint8Array(18)), decrypt: vi.fn().mockResolvedValue(encodeDesktopInnerMessage(DesktopMessageType.Statistics, 0, new Uint8Array([9]))) };
    const sockets = [firstSocket, secondSocket], records = [firstRecord, secondRecord];
    const desktop = new DesktopSocket({ onMessage, onState: vi.fn() }, {
      browserHandshakeFrame: vi.fn().mockResolvedValue(new Uint8Array([1])), completeAgentHandshake: vi.fn().mockImplementation(() => Promise.resolve(records.shift())),
    }, () => sockets.shift() as never, "https://hank.example");
    await activate(desktop, firstSocket); firstSocket.sent = [];
    firstSocket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, new Uint8Array([20]))));
    await vi.waitFor(() => expect(releaseStale).toBeDefined());

    if (transition === "reconnect") {
      const reconnecting = desktop.reconnect({ ...session, key_epoch: 2 } as never); secondSocket.open();
      await vi.waitFor(() => expect(secondSocket.sent).toHaveLength(1));
      secondSocket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.AgentHandshake, new Uint8Array([2]))));
      await reconnecting; secondSocket.sent = []; secondRecord.encrypt.mockClear();
    } else {
      await desktop.close("test_closed");
    }

    releaseStale?.(encodeDesktopInnerMessage(staleType, 0, new Uint8Array([7])));
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(onMessage).not.toHaveBeenCalled();
    expect(firstSocket.sent).toEqual([]);
    expect(secondRecord.encrypt).not.toHaveBeenCalled();

    if (transition === "reconnect") {
      secondSocket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, new Uint8Array([21]))));
      await vi.waitFor(() => expect(onMessage).toHaveBeenCalledTimes(1));
      expect(onMessage).toHaveBeenCalledWith(expect.objectContaining({ type: DesktopMessageType.Statistics }));
    }
  });
});

async function activate(desktop: DesktopSocket, socket: FakeWebSocket): Promise<void> {
  const started = desktop.start(session); socket.open();
  await vi.waitFor(() => expect(socket.sent).toHaveLength(1));
  socket.message(exactBuffer(encodeDesktopDataFrame(DesktopFrameKind.AgentHandshake, new Uint8Array([2]))));
  await started;
}

class FakeWebSocket {
  binaryType = ""; sent: unknown[] = []; readyState = 0; bufferedAmount = 0;
  failEncryptedSend?: Error;
  onopen: ((event: Event) => unknown) | null = null; onmessage: ((event: MessageEvent) => unknown) | null = null; onclose: ((event: CloseEvent) => unknown) | null = null; onerror: ((event: Event) => unknown) | null = null;
  send(value: unknown) {
    if (this.failEncryptedSend && this.readyState === 1) throw this.failEncryptedSend;
    this.sent.push(value);
  }
  close() { this.readyState = 3; this.onclose?.(new CloseEvent("close")); }
  open() { this.readyState = 1; this.onopen?.(new Event("open")); }
  message(data: ArrayBuffer) { this.onmessage?.({ data } as MessageEvent); }
}
