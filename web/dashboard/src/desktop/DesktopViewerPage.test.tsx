import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DesktopMessageType, type DesktopInnerMessage } from "./protocol";
import type { DesktopSocketCallbacks } from "./DesktopSocket";
import { DesktopViewerPage } from "./DesktopViewerPage";
import { DesktopDecoder } from "./DesktopDecoder";

const encoder = new TextEncoder();
const message = (type: DesktopMessageType, value: unknown): DesktopInnerMessage => ({ version: 1, flags: 0, type, payload: encoder.encode(JSON.stringify(value)), unknownOptional: false });

describe("DesktopViewerPage", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  it("embeds in a device workspace without repeating the device heading", async () => {
    render(<DesktopViewerPage embedded agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac Studio" }),
      supported: () => true,
      create: vi.fn(),
      reconnect: vi.fn(),
      connect: vi.fn(),
      terminate: vi.fn(),
    }} />);

    expect(await screen.findByText("Native console viewing")).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Mac Studio", level: 1 })).not.toBeInTheDocument();
  });

  it("keeps the session recoverable when the viewer page is hidden", async () => {
    const terminate = vi.fn().mockResolvedValue({});
    render(<DesktopViewerPage agentID="agent_exit" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_exit", agentName: "Mac" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_exit", agent_id: "agent_exit", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_exit" }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, callbacks) => { callbacks.onState("active"); return { close: vi.fn(), send: vi.fn(), reconnect: vi.fn() }; }), terminate,
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Connected"));

    fireEvent(window, new PageTransitionEvent("pagehide"));

    await vi.waitFor(() => expect(terminate).not.toHaveBeenCalled());
  });

  it("leaves an active relay recoverable when the viewer lifecycle is cleaned up", async () => {
    const close = vi.fn().mockResolvedValue(undefined), terminate = vi.fn().mockResolvedValue({});
    const view = render(<DesktopViewerPage agentID="agent_cleanup" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_cleanup", agentName: "Windows PC" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_cleanup", agent_id: "agent_cleanup", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_cleanup" }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, callbacks) => { callbacks.onState("active"); return { close, send: vi.fn(), reconnect: vi.fn() }; }), terminate,
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Connected"));

    view.unmount();

    expect(close).not.toHaveBeenCalled();
    expect(terminate).not.toHaveBeenCalled();
  });

  it("waits for authoritative termination before allowing another session", async () => {
    const create = vi.fn().mockResolvedValue({ session_id: "desk_1", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_1", permissions: ["desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write"] });
    let finishTermination: (() => void) | undefined;
    const terminate = vi.fn(() => new Promise<void>(resolve => { finishTermination = resolve; }));
    const close = vi.fn().mockResolvedValue(undefined), send = vi.fn().mockResolvedValue(undefined), connect = vi.fn(async (_session, _access, callbacks) => { callbacks.onState("active"); return { close, send, reconnect: vi.fn() }; });
    render(<DesktopViewerPage agentID="agent_1" dependencies={{ loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => true, create, reconnect: vi.fn(), connect, terminate }} />);
    expect(await screen.findByText("Native console viewing")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Connected"));
    fireEvent.click(screen.getByRole("button", { name: "End Session" }));
    await vi.waitFor(() => expect(terminate).toHaveBeenCalledWith("desk_1"));
    expect(screen.getByRole("status")).toHaveTextContent("Ending session…");
    expect(screen.getByRole("button", { name: "Start secure session" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "End Session" })).toBeDisabled();
    expect(close).not.toHaveBeenCalled();
    finishTermination?.();
    await vi.waitFor(() => expect(close).toHaveBeenCalledWith("operator_ended"));
    expect(terminate.mock.invocationCallOrder[0]).toBeLessThan(close.mock.invocationCallOrder[0]);
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Session ended"));
    expect(screen.getByRole("button", { name: "Enter fullscreen" })).toBeEnabled();
  });

  it("does not reconnect when the relay closes during an explicit end", async () => {
    let callbacks: DesktopSocketCallbacks | undefined;
    const reconnect = vi.fn();
    const terminate = vi.fn(async () => { callbacks?.onState("reconnecting", "user_ended"); });
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_1", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_1" }),
      reconnect, connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send: vi.fn(), reconnect: vi.fn() }; }), terminate,
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Connected"));
    fireEvent.click(screen.getByRole("button", { name: "End Session" }));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Session ended"));
    expect(reconnect).not.toHaveBeenCalled();
  });

  it("admits only one start while session creation is in flight", async () => {
    let finishCreate: ((value: { session_id: string; agent_id: string; state: string; key_epoch: number; websocket_path: string }) => void) | undefined;
    const create = vi.fn(() => new Promise<{ session_id: string; agent_id: string; state: string; key_epoch: number; websocket_path: string }>(resolve => { finishCreate = resolve; }));
    const connect = vi.fn(async (_session, _access, callbacks: DesktopSocketCallbacks) => { callbacks.onState("active"); return { close: vi.fn(), send: vi.fn(), reconnect: vi.fn() }; });
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => true,
      create, reconnect: vi.fn(), connect, terminate: vi.fn(),
    }} />);
    const start = await screen.findByRole("button", { name: "Start secure session" });
    fireEvent.click(start);
    fireEvent.click(start);
    expect(create).toHaveBeenCalledTimes(1);
    finishCreate?.({ session_id: "desk_1", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_1" });
    await vi.waitFor(() => expect(connect).toHaveBeenCalledTimes(1));
  });

  it("keeps monitor selection pending until a new endpoint generation arrives", async () => {
    vi.spyOn(DesktopDecoder.prototype, "configure").mockResolvedValue(undefined);
    let callbacks: DesktopSocketCallbacks | undefined;
    const send = vi.fn(), reportHealth = vi.fn().mockResolvedValue(undefined), requestMaximumQuality = vi.fn().mockResolvedValue(undefined);
    const connect = vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send, reconnect: vi.fn(), reportHealth, requestMaximumQuality }; });
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Windows PC" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_2", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_2", permissions: ["desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write"] }),
      reconnect: vi.fn(), connect, terminate: vi.fn(),
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(callbacks).toBeDefined());
    await act(async () => {
      callbacks!.onMessage(message(DesktopMessageType.DisplayInventory, { displays: [
        { id: "display-1", name: "Main Display", x: 0, y: 0, width: 1920, height: 1080, scale: 2, primary: true, rotation: 0 },
        { id: "display-2", name: "Second Display", x: 1920, y: 0, width: 1280, height: 720, scale: 1, primary: false, rotation: 0 },
      ] }));
      callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 1, display_id: "display-1", width: 1920, height: 1080, description_base64url: "AQ" }));
      callbacks!.onMessage(message(DesktopMessageType.Statistics, { frames: 40, rtt_ms: 12, fps: 29.8, bitrate_bps: 5_200_000, dropped_frames: 2, sender_queue_bytes: 2048, relay_backpressure_count: 1, capture_to_send_ms: 18.4, applied_width: 1920, applied_height: 1080, applied_quality: "high" }));
    });
    expect(await screen.findByText("Main Display")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Second Display · 1280×720" }));
    expect(await screen.findByRole("button", { name: "Switching…" })).toBeDisabled();
    const selection = send.mock.calls.map(([value]) => value as DesktopInnerMessage).find(value => value.type === DesktopMessageType.DisplaySelection);
    expect(JSON.parse(new TextDecoder().decode(selection?.payload))).toEqual({ display_id: "display-2", generation: 1 });
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 2, display_id: "display-2", width: 1280, height: 720, description_base64url: "AQ" })));
    expect(await screen.findByText("Second Display")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Actual Size" }));
    expect(screen.getByRole("button", { name: "Actual Size" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("status")).toHaveTextContent("29.8 fps");
    expect(screen.getByRole("status")).toHaveTextContent("5.2 Mbps");
    expect(screen.getByRole("status")).toHaveTextContent("2 dropped");
    expect(screen.getByRole("status")).toHaveTextContent("Network 12 ms RTT");
    expect(screen.getByRole("status")).toHaveTextContent("Capture 18 ms");
    expect(screen.getByRole("status")).toHaveTextContent("Applied: high 1920×1080");
    expect(screen.getByRole("status")).toHaveTextContent("Requested: balanced");
    expect(reportHealth).toHaveBeenCalledWith(expect.objectContaining({ rttMS: 12, droppedFrames: 2, senderQueueBytes: 2048, relayBackpressureCount: 1 }));
    fireEvent.click(screen.getByRole("button", { name: "Quality: balanced" }));
    await vi.waitFor(() => expect(requestMaximumQuality).toHaveBeenCalledWith("high"));
    expect(screen.getByRole("button", { name: "Quality: high" })).toBeInTheDocument();
  });

  it("shows distinct view-only, focus-ready, and focused control states", async () => {
    vi.spyOn(DesktopDecoder.prototype, "configure").mockResolvedValue(undefined);
    let callbacks: DesktopSocketCallbacks | undefined; const send = vi.fn();
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac", platform: "macos" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_3", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_3", permissions: ["desktop.view", "desktop.control", "desktop.clipboard.write"] }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send, reconnect: vi.fn() }; }), terminate: vi.fn(),
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(callbacks).toBeDefined());
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.DisplayInventory, { displays: [{ id: "display-1", name: "Main", x: 0, y: 0, width: 100, height: 50, scale: 2, primary: true, rotation: 0 }] })));
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 1, display_id: "display-1", width: 100, height: 50, description_base64url: "AQ" })));
    expect(screen.getByRole("status")).toHaveTextContent("View only");
    expect(screen.getByRole("button", { name: "Paste To Remote" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Enable Control" })); expect(screen.getByRole("status")).toHaveTextContent("Enabling control…");
    expect(screen.getByRole("button", { name: "Paste To Remote" })).toBeDisabled();
    const request = send.mock.calls.map(([value]) => value as DesktopInnerMessage).find(value => value.type === DesktopMessageType.ControlMode);
    const lease = (JSON.parse(new TextDecoder().decode(request?.payload)) as { focus_lease: number }).focus_lease;
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.ControlMode, { enabled: true, focus_lease: String(lease), applied: true })));
    expect(screen.getByRole("status")).toHaveTextContent("Control enabled");
    expect(screen.getByRole("button", { name: "Paste To Remote" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Command+Space" })).toBeEnabled();
  });

  it("handles a rejected fire-and-forget input send without an unhandled rejection", async () => {
    vi.spyOn(DesktopDecoder.prototype, "configure").mockResolvedValue(undefined);
    let callbacks: DesktopSocketCallbacks | undefined;
    const send = vi.fn().mockRejectedValue(new Error("wire_failed"));
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_send_error", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_send_error", permissions: ["desktop.view", "desktop.control"] }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send, reconnect: vi.fn() }; }), terminate: vi.fn(),
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(callbacks).toBeDefined());
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.DisplayInventory, { displays: [{ id: "display-1", name: "Main", x: 0, y: 0, width: 100, height: 50, scale: 2, primary: true, rotation: 0 }] })));
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 1, display_id: "display-1", width: 100, height: 50, description_base64url: "AQ" })));
    fireEvent.click(screen.getByRole("button", { name: "Enable Control" }));
    fireEvent.focus(screen.getByLabelText("Remote display for Mac"));
    await vi.waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("wire_failed"));
  });

  it("does not authorize when playback is unsupported", async () => {
    const create = vi.fn();
    render(<DesktopViewerPage agentID="agent_1" dependencies={{ loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => false, create, reconnect: vi.fn(), connect: vi.fn(), terminate: vi.fn() }} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("not supported");
    expect(create).not.toHaveBeenCalled();
  });

  it("requires a second explicit click before writing remote clipboard text", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined), original = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { readText: vi.fn(), writeText } });
    let callbacks: DesktopSocketCallbacks | undefined; const send = vi.fn();
    try {
      render(<DesktopViewerPage agentID="agent_1" dependencies={{
        loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Mac" }), supported: () => true,
        create: vi.fn().mockResolvedValue({ session_id: "desk_copy", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_copy", permissions: ["desktop.view", "desktop.clipboard.read"] }),
        reconnect: vi.fn(), connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send, reconnect: vi.fn() }; }), terminate: vi.fn(),
      }} />);
      fireEvent.click(await screen.findByRole("button", { name: "Start secure session" })); await vi.waitFor(() => expect(callbacks).toBeDefined());
      fireEvent.click(screen.getByRole("button", { name: "Copy From Remote" }));
      await act(async () => callbacks!.onMessage(message(DesktopMessageType.ClipboardText, { direction: "agent_to_browser", text: "remote text" })));
      expect(writeText).not.toHaveBeenCalled();
      fireEvent.click(await screen.findByRole("button", { name: "Copy Remote Text" })); await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("remote text"));
    } finally { Object.defineProperty(navigator, "clipboard", { configurable: true, value: original }); }
  });

  it("fails closed on privileged readiness states without offering remote settings actions", async () => {
    let callbacks: DesktopSocketCallbacks | undefined;
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Remote Mac", platform: "macos" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_permission", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_permission", permissions: ["desktop.view", "desktop.control"] }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send: vi.fn(), reconnect: vi.fn() }; }), terminate: vi.fn(),
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" }));
    await vi.waitFor(() => expect(callbacks).toBeDefined());
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.PermissionState, { state: "screen_recording_required" })));
    expect(screen.getByRole("alert")).toHaveTextContent("Screen Recording required");
    expect(screen.getByRole("button", { name: "Enable Control" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /settings/i })).not.toBeInTheDocument();
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.PermissionState, { state: "permission_restored" })));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("synchronously clears stale pixels and decoder before accepting secure desktop media", async () => {
    let callbacks: DesktopSocketCallbacks | undefined;
    const order: string[] = [];
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ clearRect: () => order.push("clear") } as unknown as CanvasRenderingContext2D);
    vi.spyOn(DesktopDecoder.prototype, "clearRenderedFrame").mockImplementation(() => { order.push("decoder_clear"); });
    vi.spyOn(DesktopDecoder.prototype, "configure").mockImplementation(async value => { order.push(`codec:${value.generation}`); });
    vi.spyOn(DesktopDecoder.prototype, "decode").mockImplementation((_data, metadata) => { order.push(`frame:${metadata.generation}`); return true; });
    render(<DesktopViewerPage agentID="agent_1" dependencies={{
      loadAccess: async () => ({ allowed: true, deviceID: "device_1", agentName: "Windows PC", platform: "windows" }), supported: () => true,
      create: vi.fn().mockResolvedValue({ session_id: "desk_clear", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_clear", permissions: ["desktop.view", "desktop.control"] }),
      reconnect: vi.fn(), connect: vi.fn(async (_session, _access, value: DesktopSocketCallbacks) => { callbacks = value; value.onState("active"); return { close: vi.fn(), send: vi.fn(), reconnect: vi.fn() }; }), terminate: vi.fn(),
    }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start secure session" })); await vi.waitFor(() => expect(callbacks).toBeDefined());
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.DisplayInventory, { displays: [{ id: "default", name: "Default", x: 0, y: 0, width: 100, height: 50, scale: 1, primary: true, rotation: 0 }] })));
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 1, display_id: "default", width: 100, height: 50, description_base64url: "AQ" })));
    await act(async () => callbacks!.onMessage({ version: 1, flags: 0, type: DesktopMessageType.VideoAccessUnit, payload: new Uint8Array([0, 0, 0, 1, 0x65]), unknownOptional: false }));
    order.length = 0;
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.SecureState, { state: "secure_desktop_entered", generation: 2, clear_video: true })));
    expect(screen.getByRole("alert")).toHaveTextContent("Windows secure desktop");
    await act(async () => callbacks!.onMessage({ version: 1, flags: 0, type: DesktopMessageType.VideoAccessUnit, payload: new Uint8Array([0, 0, 0, 1, 0x65]), unknownOptional: false }));
    expect(order).toEqual(["decoder_clear", "clear"]);
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.DisplayInventory, { displays: [{ id: "secure", name: "Secure", x: 0, y: 0, width: 100, height: 50, scale: 1, primary: true, rotation: 0 }] })));
    await act(async () => callbacks!.onMessage(message(DesktopMessageType.CodecConfig, { codec: "avc1.42E01E", generation: 2, display_id: "secure", width: 100, height: 50, description_base64url: "AQ" })));
    await act(async () => callbacks!.onMessage({ version: 1, flags: 0, type: DesktopMessageType.VideoAccessUnit, payload: new Uint8Array([0, 0, 0, 1, 0x65]), unknownOptional: false }));
    expect(order.indexOf("decoder_clear")).toBeLessThan(order.indexOf("codec:2"));
    expect(order.indexOf("clear")).toBeLessThan(order.indexOf("frame:2"));
  });
});
