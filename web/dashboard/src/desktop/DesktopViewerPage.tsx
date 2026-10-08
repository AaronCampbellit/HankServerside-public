import { useEffect, useMemo, useRef, useState } from "react";
import { desktopClient, type DesktopSessionAuthorization } from "../api/desktop";
import { agentsClient, agentDisplayName, agentHasCapability, agentIsOnline } from "../api/agents";
import { bootstrapClient } from "../api/bootstrap";
import { decodeBase64URL } from "./base64url";
import syntheticMetadata from "../../../../schemas/desktop/v1/synthetic-desktop-640x360.json";
import syntheticClipURL from "../../../../schemas/desktop/v1/synthetic-desktop-640x360.h264?url";
import { BrowserHandshakeDriver } from "./BrowserHandshakeDriver";
import { DesktopDecoder, desktopPlaybackSupported } from "./DesktopDecoder";
import { DesktopSocket, type DesktopSocketCallbacks, type DesktopViewerState } from "./DesktopSocket";
import type { DesktopHealthSample, DesktopQualityName } from "./qualityController";
import { acceptAccessUnit, applyCodecConfiguration, applyDisplayInventory, initialDisplayState, type DisplayState } from "./displayStore";
import { buildAVCDecoderConfigurationRecord, splitAnnexBNALUnits } from "./fmp4";
import { IndexedDBDesktopIdentityStore } from "./identityStore";
import { ensureBrowserDesktopIdentity } from "./autoEnrollment";
import { DesktopMessageType, type CodecConfigPayload, type DesktopInnerMessage, type DisplayInventoryPayload, type StatisticsPayload } from "./protocol";
import { DesktopInputController, fittedContentRect } from "./inputController";
import { DesktopClipboardController } from "./clipboardController";
import { specialKeysForPlatform, type DesktopPlatform } from "./specialKeys";
import { isDesktopPermissionState, readinessOverlay, type DesktopPermissionState } from "./readiness";
import { DesktopSessionHistory } from "./DesktopSessionHistory";

interface AccessResult { allowed: boolean; reason?: string; deviceID: string; agentName: string; platform?: DesktopPlatform; operatorPrivateKey?: CryptoKey; trustRootPublicKeySPKI?: Uint8Array; trustRootGeneration?: number }
interface DesktopConnection { reconnect(session: DesktopSessionAuthorization): Promise<void>; send(message: DesktopInnerMessage): Promise<void>; close(reason: string): Promise<void>; requestMaximumQuality?(level: DesktopQualityName, atMS?: number): Promise<void>; reportHealth?(sample: DesktopHealthSample): Promise<void> }
export interface DesktopViewerDependencies {
  loadAccess(): Promise<AccessResult>;
  supported(): boolean;
  create(agentID: string, deviceID: string, permissions: string[]): Promise<DesktopSessionAuthorization>;
  reconnect(sessionID: string): Promise<DesktopSessionAuthorization>;
  connect(session: DesktopSessionAuthorization, access: AccessResult, callbacks: DesktopSocketCallbacks): Promise<DesktopConnection>;
  terminate(sessionID: string, keepalive?: boolean): Promise<unknown>;
}

const textEncoder = new TextEncoder(), textDecoder = new TextDecoder();
const requestedDesktopPermissions = ["desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write"];
type NativeStatistics = StatisticsPayload & {
  fps?: number; bitrate_bps?: number; dropped_frames?: number; applied_width?: number; applied_height?: number; applied_quality?: string;
  sender_queue_bytes?: number; relay_backpressure_count?: number; capture_to_send_ms?: number; capture_to_send_max_ms?: number;
  render_latency_ms?: number; render_latency_max_ms?: number; playback_mode?: "webcodecs" | "mse" | "none";
};

export function DesktopViewerPage({
  agentID = agentIDFromPath(),
  dependencies,
  embedded = false,
}: {
  agentID?: string;
  dependencies?: DesktopViewerDependencies;
  embedded?: boolean;
}) {
  const deps = useMemo(() => dependencies ?? defaultDependencies(agentID), [agentID, dependencies]);
  const [access, setAccess] = useState<AccessResult | null>(null), [state, setState] = useState<DesktopViewerState>("idle");
  const [reason, setReason] = useState(""), [session, setSession] = useState<DesktopSessionAuthorization | null>(null);
  const [control, setControl] = useState(false), [focused, setFocused] = useState(false), [controlPending, setControlPending] = useState(false), [quality, setQuality] = useState<DesktopQualityName>("balanced"), [clipboardStatus, setClipboardStatus] = useState("");
  const [clipboardReady, setClipboardReady] = useState(false);
  const [readinessState, setReadinessState] = useState<DesktopPermissionState | null>(null);
  const [pendingDisplayID, setPendingDisplayID] = useState<string | null>(null);
  const [displayState, setDisplayState] = useState<DisplayState>(() => initialDisplayState()), [switchingDisplay, setSwitchingDisplay] = useState(false);
  const [statistics, setStatistics] = useState<NativeStatistics | null>(null);
  const viewerRef = useRef<HTMLDivElement | null>(null), canvasRef = useRef<HTMLCanvasElement | null>(null), videoRef = useRef<HTMLVideoElement | null>(null);
  const connectionRef = useRef<DesktopConnection | null>(null), decoderRef = useRef<DesktopDecoder | null>(null), sessionRef = useRef<DesktopSessionAuthorization | null>(null), frameIndex = useRef(0), reconnecting = useRef(false), startingRef = useRef(false), endingRef = useRef(false), mountedRef = useRef(true);
  const displayRef = useRef(displayState), messageQueue = useRef(Promise.resolve());
  const videoBlockedRef = useRef(false);
  const inputRef = useRef<DesktopInputController | null>(null), clipboardRef = useRef<DesktopClipboardController | null>(null), pendingDisplayRef = useRef<string | null>(null);
  const readiness = readinessState ? readinessOverlay(readinessState) : null;
  useEffect(() => { displayRef.current = displayState; }, [displayState]);
  useEffect(() => {
    mountedRef.current = true;
    inputRef.current = new DesktopInputController({ send: sendJSONInBackground, contentRect: () => {
      const current = displayRef.current, bounds = viewerRef.current?.getBoundingClientRect() ?? new DOMRect();
      return current.mode === "fit" ? fittedContentRect(bounds, current.width, current.height) : (canvasRef.current?.getBoundingClientRect() ?? bounds);
    }, requestFrame: callback => requestAnimationFrame(callback), cancelFrame: handle => cancelAnimationFrame(handle) });
    let live = true; deps.loadAccess().then(value => { if (live) setAccess(value); }).catch(error => { if (live) setAccess({ allowed: false, deviceID: "", agentName: agentID, reason: error instanceof Error ? error.message : "Viewer unavailable" }); });
    return () => {
      live = false; mountedRef.current = false;
      // A pagehide can be emitted while a browser temporarily freezes or
      // restores this route. Let the relay reconnect/expire normally; only
      // the explicit End Session action should terminate the server session.
      inputRef.current?.dispose(); clipboardRef.current?.reset();
      decoderRef.current?.close();
    };
  }, [agentID, deps]);
  useEffect(() => {
    inputRef.current?.update({ active: state === "active" && !readiness?.blocksInput, control, reconnecting: state === "reconnecting" || reconnecting.current, visible: document.visibilityState !== "hidden", displayID: displayState.selectedID ?? "", generation: displayState.generation });
    if (state !== "active") { setFocused(false); setControlPending(false); setClipboardReady(false); clipboardRef.current?.reset(); }
  }, [state, control, displayState.selectedID, displayState.generation, readiness?.blocksInput]);
  useEffect(() => {
    if (state === "active" && control && !readiness?.blocksInput) viewerRef.current?.focus({ preventScroll: true });
  }, [state, control, readiness?.blocksInput]);
  useEffect(() => { clipboardRef.current?.updateControl({ active: state === "active", enabled: control, focused }); }, [state, control, focused]);
  useEffect(() => { const hidden = () => { const visible = document.visibilityState !== "hidden"; inputRef.current?.update({ visible }); if (!visible) setFocused(false); }; document.addEventListener("visibilitychange", hidden); return () => document.removeEventListener("visibilitychange", hidden); }, []);

  const unsupported = access && !deps.supported();
  const callbacks: DesktopSocketCallbacks = {
    onMessage: message => {
      messageQueue.current = messageQueue.current.then(() => handleMessage(message)).catch(error => {
        setState("error");
        setReason(error instanceof Error ? error.message : "Remote display stream failed");
      });
    },
    onState: (next, detail) => {
      if (next === "reconnecting" && endingRef.current) return;
      setState(next); setReason(detail || "");
      if (next === "reconnecting" && sessionRef.current && !reconnecting.current) void reconnectSession(sessionRef.current.session_id);
    },
  };

  async function start() {
    if (!access?.allowed || unsupported || startingRef.current || endingRef.current || sessionRef.current) return;
    startingRef.current = true;
    setState("authorizing"); setReason("");
    try {
      const created = await deps.create(agentID, access.deviceID, requestedDesktopPermissions);
      setSession(created); sessionRef.current = created; frameIndex.current = 0; setStatistics(null); setSwitchingDisplay(true); setReadinessState(null);
      clipboardRef.current = new DesktopClipboardController({ permissions: new Set(created.permissions ?? requestedDesktopPermissions), clipboard: navigator.clipboard, send: sendJSONInBackground, status: value => { setClipboardStatus(value); setClipboardReady(value === "clipboard_ready_to_copy"); } });
      const initial = initialDisplayState(displayRef.current.mode); displayRef.current = initial; setDisplayState(initial);
      decoderRef.current?.close(); decoderRef.current = new DesktopDecoder({}, { canvas: canvasRef.current, video: videoRef.current });
      connectionRef.current = await deps.connect(created, access, callbacks);
      if (created.session_id === "desk_preview") {
        const syntheticDisplay = { id: "synthetic-1", name: "Synthetic development display", x: 0, y: 0, width: syntheticMetadata.width, height: syntheticMetadata.height, scale: 1, primary: true, rotation: 0 as const };
        const configured = applyCodecConfiguration(applyDisplayInventory(initial, [syntheticDisplay]), {
          codec: syntheticMetadata.codec, generation: 1, display_id: syntheticDisplay.id, width: syntheticMetadata.width,
          height: syntheticMetadata.height, description_base64url: "fixture",
        });
        displayRef.current = configured; setDisplayState(configured);
        void playSyntheticPreview(decoderRef.current, configured.generation, syntheticDisplay.id, () => setSwitchingDisplay(false)).catch(error => { setState("error"); setReason(error instanceof Error ? error.message : "Synthetic playback failed"); });
      }
    } catch (error) {
      if (!sessionRef.current) startingRef.current = false;
      setState("error"); setReason(error instanceof Error ? error.message : "Session authorization failed");
    }
  }
  async function reconnectSession(sessionID: string) {
    if (endingRef.current) return;
    reconnecting.current = true; setSwitchingDisplay(true);
    try {
      const authorization = await deps.reconnect(sessionID);
      if (endingRef.current || sessionRef.current?.session_id !== sessionID) return;
      setSession(authorization); sessionRef.current = authorization; frameIndex.current = 0; decoderRef.current?.reset();
      const invalidated = { ...displayRef.current, generation: 0, minimumGeneration: Math.max(displayRef.current.minimumGeneration, displayRef.current.generation + 1), codec: undefined, width: 0, height: 0 };
      displayRef.current = invalidated; setDisplayState(invalidated);
      await connectionRef.current?.reconnect(authorization);
    }
    catch (error) {
      if (!endingRef.current) { setState("error"); setReason(error instanceof Error ? error.message : "Reconnect failed"); }
    }
    finally { reconnecting.current = false; }
  }
  async function end() {
    if (endingRef.current) return;
    endingRef.current = true;
    inputRef.current?.blur(); clipboardRef.current?.reset(); pendingDisplayRef.current = null; setFocused(false); setControl(false); setClipboardReady(false); setPendingDisplayID(null);
    const current = sessionRef.current; setState("ending"); setReason("");
    try {
      if (current) await deps.terminate(current.session_id);
    } catch (error) {
      endingRef.current = false;
      setState("error"); setReason(error instanceof Error ? error.message : "Session cleanup failed. Retry End Session.");
      return;
    }
    try { await connectionRef.current?.close("operator_ended"); } catch { /* termination remains immediate */ }
    setSession(null); sessionRef.current = null; startingRef.current = false; connectionRef.current = null; decoderRef.current?.close(); decoderRef.current = null;
    setState("ended"); setReason("Session ended"); setSwitchingDisplay(false); setStatistics(null);
    endingRef.current = false;
  }
  async function fullscreen() { await viewerRef.current?.requestFullscreen?.(); }
  async function sendJSON(type: DesktopMessageType, value: unknown) { await connectionRef.current?.send({ version: 1, flags: 0, type, payload: textEncoder.encode(JSON.stringify(value)), unknownOptional: false }); }
  function sendJSONInBackground(type: DesktopMessageType, value: Record<string, unknown>): void { runInBackground(sendJSON(type, value)); }
  function runInBackground(operation: Promise<void>): void {
    void operation.catch(error => {
      if (!mountedRef.current) return;
      setState("error"); setReason(error instanceof Error ? error.message : "desktop_outbound_send_failed");
      setFocused(false); setControl(false); setControlPending(false);
    });
  }
  function toggleControl() { if (control) { inputRef.current?.blur(); setFocused(false); setControlPending(false); } setControl(value => !value); }
  function focusControl() { if (!control || focused || controlPending) return; const lease = inputRef.current?.focus() ?? 0; setControlPending(lease > 0); }
  function blurControl() { inputRef.current?.blur(); setFocused(false); setControlPending(false); }
  async function selectDisplay(displayID: string) {
    const current = displayRef.current; if (state !== "active" || current.generation <= 0 || displayID === current.selectedID || pendingDisplayRef.current) return;
    blurControl(); pendingDisplayRef.current = displayID; setPendingDisplayID(displayID); await sendJSON(DesktopMessageType.DisplaySelection, { display_id: displayID, generation: current.generation });
  }
  async function cycleQuality() {
    const levels: DesktopQualityName[] = ["low", "balanced", "high", "ultra"], next = levels[(levels.indexOf(quality) + 1) % levels.length];
    setQuality(next); await (connectionRef.current?.requestMaximumQuality?.(next) ?? sendJSON(DesktopMessageType.Quality, { profile: next }));
  }
  function setScaleMode(mode: DisplayState["mode"]) {
    const next = { ...displayRef.current, mode }; displayRef.current = next; setDisplayState(next);
  }
  function clearRenderedFrame(width = 0, height = 0) {
    decoderRef.current?.clearRenderedFrame();
    const canvas = canvasRef.current;
    if (canvas) {
      const context = canvas.getContext("2d");
      context?.clearRect(0, 0, canvas.width, canvas.height);
      if (width > 0) canvas.width = width;
      if (height > 0) canvas.height = height;
    }
    setSwitchingDisplay(true);
  }
  async function handleMessage(message: DesktopInnerMessage) {
    if (message.type === DesktopMessageType.DisplayInventory) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as DisplayInventoryPayload;
      const previous = displayRef.current, next = applyDisplayInventory(previous, value.displays);
      if (next.generation === 0 && previous.generation > 0) clearRenderedFrame();
      else if (previous.inventory.length === 0 && next.inventory.length > 0) setSwitchingDisplay(true);
      displayRef.current = next; setDisplayState(next);
    } else if (message.type === DesktopMessageType.CodecConfig) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as CodecConfigPayload;
      const previous = displayRef.current, next = applyCodecConfiguration(previous, value);
      const changed = previous.generation !== next.generation || previous.selectedID !== next.selectedID || previous.width !== next.width || previous.height !== next.height;
      if (changed) { frameIndex.current = 0; clearRenderedFrame(next.width, next.height); }
      displayRef.current = next; setDisplayState(next);
      if (pendingDisplayRef.current === value.display_id && value.generation > previous.generation) { pendingDisplayRef.current = null; setPendingDisplayID(null); }
      await decoderRef.current?.configure({ codec: value.codec, width: value.width, height: value.height, description: decodeBase64URL(value.description_base64url), generation: value.generation, displayID: value.display_id });
      videoBlockedRef.current = false;
    } else if (message.type === DesktopMessageType.VideoAccessUnit) {
      if (videoBlockedRef.current) return;
      const current = displayRef.current, index = frameIndex.current++, keyframe = splitAnnexBNALUnits(message.payload).some(nal => (nal[0] & 0x1f) === 5);
      const metadata = { generation: current.generation, timestamp_us: index * 33_333, duration_us: 33_333, keyframe };
      if (!acceptAccessUnit(current, metadata)) return;
      const accepted = decoderRef.current?.decode(message.payload, { timestamp: metadata.timestamp_us, duration: metadata.duration_us, keyframe, generation: metadata.generation });
      if (accepted && keyframe) setSwitchingDisplay(false);
    } else if (message.type === DesktopMessageType.Statistics) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as NativeStatistics, decoder = decoderRef.current?.healthSnapshot() ??
        { decoderQueue: 0, decodedFrames: value.frames ?? 0, droppedFrames: 0, renderLatencyMS: 0, renderLatencyMaxMS: 0, playbackMode: "none" as const };
      setStatistics({ ...value, render_latency_ms: decoder.renderLatencyMS, render_latency_max_ms: decoder.renderLatencyMaxMS,
        playback_mode: decoder.playbackMode });
      const sample: DesktopHealthSample = { atMS: Date.now(), rttMS: value.rtt_ms ?? 0, decoderQueue: decoder.decoderQueue,
        decodedFrames: decoder.decodedFrames, droppedFrames: decoder.droppedFrames + (value.dropped_frames ?? 0), senderQueueBytes: value.sender_queue_bytes ?? 0,
        relayBackpressureCount: value.relay_backpressure_count ?? 0 };
      runInBackground(connectionRef.current?.reportHealth?.(sample) ?? Promise.resolve());
    } else if (message.type === DesktopMessageType.Quality) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as { applied_quality?: string; profile?: string; bitrate_bps?: number; width?: number; height?: number };
      setStatistics(previous => ({ frames: previous?.frames ?? 0, bytes: previous?.bytes ?? 0, rtt_ms: previous?.rtt_ms ?? 0, ...previous,
        applied_quality: value.applied_quality ?? value.profile ?? previous?.applied_quality, bitrate_bps: value.bitrate_bps ?? previous?.bitrate_bps,
        applied_width: value.width ?? previous?.applied_width, applied_height: value.height ?? previous?.applied_height }));
    } else if (message.type === DesktopMessageType.ClipboardText) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as { direction?: string; text?: string };
      if (value.direction === "agent_to_browser" && typeof value.text === "string") clipboardRef.current?.acceptRemoteText(value.text);
    } else if (message.type === DesktopMessageType.PermissionState || message.type === DesktopMessageType.SecureState) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as { state?: DesktopPermissionState; clear_video?: boolean };
      if (!isDesktopPermissionState(value.state)) throw new Error("desktop_readiness_state_invalid");
      if (value.clear_video === true) { videoBlockedRef.current = true; frameIndex.current = 0; clearRenderedFrame(); }
      const overlay = readinessOverlay(value.state); setReadinessState(overlay ? value.state : null);
      if (overlay?.blocksVideo) { videoBlockedRef.current = true; clearRenderedFrame(); }
      if (overlay?.blocksInput) { inputRef.current?.blur(); setFocused(false); setControlPending(false); }
    } else if (message.type === DesktopMessageType.ControlMode) {
      const value = JSON.parse(textDecoder.decode(message.payload)) as { enabled?: boolean; focus_lease?: number | string; applied?: boolean };
      const reportedLease = typeof value.focus_lease === "string" ? Number(value.focus_lease) : value.focus_lease;
      const lease = typeof reportedLease === "number" && Number.isSafeInteger(reportedLease) ? reportedLease : inputRef.current?.focusLease ?? 0;
      if (value.applied === true && value.enabled && inputRef.current?.confirmFocus(lease, true)) { setFocused(true); setControlPending(false); }
      else if (value.applied === false || !value.enabled) { inputRef.current?.confirmFocus(lease, false); inputRef.current?.blur(); setFocused(false); setControlPending(false); }
    } else if (message.type === DesktopMessageType.Terminate) { await end(); }
  }

  const pageClass = `desktop-viewer-page${embedded ? " desktop-viewer-embedded" : ""}`;
  if (!access) return <section className={pageClass}><p className="loading-state" role="status">Loading Remote Desktop…</p></section>;
  if (!access.allowed) return <section className={pageClass}>{embedded ? null : <h1>Remote Desktop</h1>}<p role="alert" className="error-state">{access.reason || "Admin access and an online desktop-capable agent are required."}</p><section className="desktop-setup-callout"><h2>Before you can connect</h2><ol><li>Return to <a href="/dashboard/agents">Agents</a> and open this device.</li><li>Use its Remote Desktop setup section to approve this browser and the device.</li><li>Complete any local permission prompts, then open Remote Desktop again once its checklist shows Ready.</li></ol></section></section>;
  if (unsupported) return <section className={pageClass}>{embedded ? null : <h1>Remote Desktop</h1>}<p role="alert" className="error-state">Secure H.264 playback is not supported by this browser.</p></section>;

  const status = state === "joining" ? "Joining encrypted session…" : state === "authorizing" ? "Authorizing…" : state === "active" ? "Connected" : state === "reconnecting" ? "Reconnecting…" : state === "ending" ? "Ending session…" : reason || "Ready to connect";
  const selectedDisplay = displayState.inventory.find(display => display.id === displayState.selectedID);
  const remoteControlsDisabled = state !== "active" || reconnecting.current || Boolean(readiness?.blocksInput);
  const appliedWidth = statistics?.applied_width ?? displayState.width, appliedHeight = statistics?.applied_height ?? displayState.height;
  const appliedQuality = statistics?.applied_quality ?? "—";
  return (
    <section className={pageClass} aria-label={embedded ? `Remote Desktop for ${access.agentName}` : undefined} aria-labelledby={embedded ? undefined : "desktop-viewer-title"} data-session-id={session?.session_id}>
      <header className="desktop-viewer-header"><div><p className="eyebrow">{embedded ? "Remote Desktop" : "Hank"}</p>{embedded ? null : <h1 id="desktop-viewer-title">{access.agentName}</h1>}</div><span className="desktop-native-badge">{session?.session_id === "desk_preview" ? "Synthetic development source" : "Native console viewing"}</span></header>
      <div className="desktop-viewer-stage" data-scale={displayState.mode} data-control-focused={focused} ref={viewerRef} tabIndex={0} aria-label={`Remote display for ${access.agentName}`}
        onClick={focusControl} onFocus={focusControl} onBlur={blurControl}
        onKeyDown={event => inputRef.current?.key(event.nativeEvent)} onKeyUp={event => inputRef.current?.key(event.nativeEvent)}
        onPointerMove={event => inputRef.current?.pointer(event.nativeEvent)} onPointerDown={event => inputRef.current?.pointer(event.nativeEvent)}
        onPointerUp={event => inputRef.current?.pointer(event.nativeEvent)} onPointerCancel={event => inputRef.current?.pointer(event.nativeEvent)}
        onWheel={event => { if (inputRef.current?.pointer(event.nativeEvent)) event.preventDefault(); }}>
        <canvas ref={canvasRef} width={displayState.width || 640} height={displayState.height || 360} aria-label="Remote desktop video" />
        <video ref={videoRef} muted autoPlay playsInline hidden aria-label="Remote desktop MSE video" />
        {readiness ? <div className="desktop-viewer-overlay" role="alert" data-tone={readiness.tone}><strong>{readiness.title}</strong><span>{readiness.detail}</span></div>
          : state !== "active" || switchingDisplay ? <div className="desktop-viewer-overlay"><strong>{state === "active" ? "Switching display…" : status}</strong><span>End-to-end encrypted · desktop.v1</span></div> : null}
      </div>
      <div className="desktop-viewer-toolbar" role="toolbar" aria-label="Remote desktop controls">
        <button type="button" onClick={() => void start()} disabled={state === "authorizing" || state === "joining" || state === "active" || state === "reconnecting" || state === "ending"}>Start secure session</button>
        <button type="button" className="secondary" aria-pressed={displayState.mode === "fit"} disabled={remoteControlsDisabled} onClick={() => setScaleMode("fit")}>Fit</button>
        <button type="button" className="secondary" aria-pressed={displayState.mode === "actual"} disabled={remoteControlsDisabled} onClick={() => setScaleMode("actual")}>Actual Size</button>
        <button type="button" className="secondary" aria-pressed={control} disabled={remoteControlsDisabled || !(session?.permissions ?? requestedDesktopPermissions).includes("desktop.control")} onClick={toggleControl}>{control ? "Disable Control" : "Enable Control"}</button>
        <button type="button" className="secondary" disabled={remoteControlsDisabled || !(session?.permissions ?? requestedDesktopPermissions).includes("desktop.clipboard.read")} onClick={() => void (clipboardReady ? clipboardRef.current?.copyReadyText() : clipboardRef.current?.copyFromRemote())}>{clipboardReady ? "Copy Remote Text" : "Copy From Remote"}</button>
        <button type="button" className="secondary" disabled={remoteControlsDisabled || !control || !focused || !(session?.permissions ?? requestedDesktopPermissions).includes("desktop.control") || !(session?.permissions ?? requestedDesktopPermissions).includes("desktop.clipboard.write")} onClick={() => void clipboardRef.current?.pasteToRemote()}>Paste To Remote</button>
        <button type="button" className="secondary" disabled={remoteControlsDisabled} onClick={() => runInBackground(cycleQuality())}>Quality: {quality}</button>
        <button type="button" className="secondary" disabled={state === "reconnecting"} onClick={() => void fullscreen()}>Enter fullscreen</button>
        <button type="button" className="danger" disabled={!session || state === "ending"} onClick={() => void end()}>End Session</button>
      </div>
      <section className="desktop-display-panel" aria-label="Remote display inventory">
        <div><span className="desktop-display-label">Current display</span><strong>{selectedDisplay?.name ?? "Waiting for endpoint"}</strong>{selectedDisplay ? <small>{selectedDisplay.width}×{selectedDisplay.height} · {selectedDisplay.primary ? "Primary" : "Secondary"}</small> : null}</div>
        <div><span className="desktop-display-label">Other detected displays</span>{displayState.inventory.some(display => display.id !== displayState.selectedID) ? <ul>{displayState.inventory.filter(display => display.id !== displayState.selectedID).map(display => <li key={display.id}><button type="button" className="link-button" disabled={remoteControlsDisabled || Boolean(pendingDisplayID)} onClick={() => runInBackground(selectDisplay(display.id))}>{pendingDisplayID === display.id ? "Switching…" : `${display.name} · ${display.width}×${display.height}`}</button></li>)}</ul> : <small>None</small>}</div>
        <p id="monitor-switch-help">{pendingDisplayID ? "Waiting for endpoint display acknowledgement and a new stream generation." : "Selecting a display invalidates the old frame and input coordinates."}</p>
      </section>
      {specialKeysForPlatform(access.platform ?? "unknown").length ? <section className="desktop-special-keys" aria-label="Special keys">{specialKeysForPlatform(access.platform ?? "unknown").map(key => <button key={key.name} type="button" className="secondary" disabled={remoteControlsDisabled || !control || !focused || key.disabled} title={key.reason} onClick={() => sendJSONInBackground(DesktopMessageType.SpecialKey, { name: key.name })}>{key.label}</button>)}</section> : null}
      <div className="desktop-viewer-status" role="status" aria-live="polite">
        <span>{status}</span><span>{!control ? "View only" : focused ? "Control enabled" : controlPending ? "Enabling control…" : "Click display to control"}</span><span>{statistics ? `Network ${statistics.rtt_ms} ms RTT` : "Network —"}</span>
        <span>{statistics?.capture_to_send_ms === undefined ? "Capture —" : `Capture ${statistics.capture_to_send_ms.toFixed(0)} ms`}</span>
        <span>{statistics?.render_latency_ms === undefined ? "Render —" : `Render ${statistics.render_latency_ms.toFixed(0)} ms`}</span>
        <span>{statistics?.playback_mode ? `Playback ${statistics.playback_mode === "webcodecs" ? "WebCodecs" : statistics.playback_mode.toUpperCase()}` : "Playback —"}</span>
        <span>{statistics?.fps === undefined ? "FPS —" : `${statistics.fps.toFixed(1)} fps`}</span>
        <span>{statistics?.bitrate_bps === undefined ? "Bitrate —" : `${formatMbps(statistics.bitrate_bps)} Mbps`}</span>
        <span>{statistics?.dropped_frames === undefined ? "Dropped —" : `${statistics.dropped_frames} dropped`}</span>
        <span>Requested: {quality}</span><span>Applied: {appliedQuality} {appliedWidth > 0 && appliedHeight > 0 ? `${appliedWidth}×${appliedHeight}` : "—"}</span>
        {clipboardStatus ? <span>{clipboardStatus.replaceAll("_", " ")}</span> : null}
      </div>
      {session ? <DesktopSessionHistory sessionID={session.session_id} /> : null}
    </section>
  );
}

function agentIDFromPath(): string { const match = window.location.pathname.match(/^\/dashboard\/agents\/([^/]+)\/desktop$/); return match ? decodeURIComponent(match[1]) : ""; }
function formatMbps(bitsPerSecond: number): string { return (bitsPerSecond / 1_000_000).toFixed(1).replace(/\.0$/, ""); }
function defaultDependencies(agentID: string): DesktopViewerDependencies {
  if (import.meta.env.DEV && new URLSearchParams(window.location.search).get("desktop-preview") === "synthetic") {
    return {
      supported: () => true, loadAccess: async () => ({ allowed: true, deviceID: "preview_device", agentName: "Synthetic Desktop Preview" }),
      create: async target => ({ session_id: "desk_preview", agent_id: target, state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_preview" }),
      reconnect: async target => ({ session_id: target, agent_id: agentID, state: "reconnecting", key_epoch: 2, websocket_path: `/ws/desktop/browser/${target}` }),
      connect: async (_session, _access, callbacks) => { callbacks.onState("joining"); callbacks.onState("active"); return { reconnect: async () => callbacks.onState("active"), send: async () => {}, close: async reason => callbacks.onState("ended", reason), requestMaximumQuality: async () => {}, reportHealth: async () => {} }; },
      terminate: async () => ({}),
    };
  }
  return {
    supported: () => desktopPlaybackSupported(),
    loadAccess: async () => {
      const bootstrap = await bootstrapClient.load();
      if (bootstrap.permissions?.is_admin && bootstrap.home?.id && bootstrap.user.id && bootstrap.session.id) await ensureBrowserDesktopIdentity(bootstrap.home.id, bootstrap.user.id, bootstrap.session.id);
      const [agents, trust] = await Promise.all([agentsClient.listAgents(), desktopClient.trust()]); const agent = agents.find(value => value.agent_id === agentID);
      const identityStore = new IndexedDBDesktopIdentityStore();
      if (import.meta.env.DEV && new URLSearchParams(window.location.search).get("desktop-acceptance-enroll") === "1") {
        const response = await fetch("/__hank/desktop-acceptance-identity", { cache: "no-store" });
        if (!response.ok) throw new Error("desktop_acceptance_identity_unavailable");
        const material = await response.json() as { device_id: string; private_key_pkcs8: string; public_key_spki: string };
        await identityStore.install(material.device_id, decodeBase64URL(material.private_key_pkcs8), decodeBase64URL(material.public_key_spki));
        localStorage.setItem("hank-desktop-device-id", material.device_id);
      }
      let deviceID = localStorage.getItem("hank-desktop-device-id") || ""; if (!deviceID) { deviceID = `browser_${crypto.randomUUID()}`; localStorage.setItem("hank-desktop-device-id", deviceID); }
      const identity = await identityStore.get(deviceID);
      const approved = trust.identities.some(value => value.identity_type === "operator_device" && value.device_id === deviceID && !value.revoked_at);
      const allowed = Boolean(bootstrap.permissions?.is_admin && agent && agentIsOnline(agent) && agentHasCapability(agent, "desktop.session.open") && agentHasCapability(agent, "desktop.view") && identity && approved && trust.root?.public_key_spki);
      const reason = !identity || !approved ? "This browser does not have an approved remote desktop operator identity." : "Admin access and an online desktop-capable agent are required.";
      const platform = agent?.metadata?.platform?.toLowerCase().includes("win") ? "windows" : agent?.metadata?.platform?.toLowerCase().includes("mac") || agent?.metadata?.platform?.toLowerCase().includes("darwin") ? "macos" : "unknown";
      return { allowed, deviceID, agentName: agent ? agentDisplayName(agent) : agentID, platform, reason: allowed ? undefined : reason, operatorPrivateKey: identity?.privateKey,
        trustRootPublicKeySPKI: trust.root?.public_key_spki ? decodeBase64URL(trust.root.public_key_spki) : undefined, trustRootGeneration: trust.root?.generation };
    },
    create: (target, device, permissions) => desktopClient.create(target, device, permissions), reconnect: sessionID => desktopClient.reconnect(sessionID),
    connect: async (session, access, callbacks) => {
      if (!access.operatorPrivateKey || !access.trustRootPublicKeySPKI || !access.trustRootGeneration) throw new Error("desktop_operator_identity_unavailable");
      const socket = new DesktopSocket(callbacks, new BrowserHandshakeDriver({ operatorPrivateKey: access.operatorPrivateKey,
        trustRootPublicKeySPKI: access.trustRootPublicKeySPKI, trustRootGeneration: access.trustRootGeneration }));
      await socket.start(session); return socket;
    },
    terminate: (sessionID, keepalive) => desktopClient.terminate(sessionID, keepalive),
  };
}

async function playSyntheticPreview(decoder: DesktopDecoder | null, generation: number, displayID: string, onKeyframe: () => void): Promise<void> {
  if (!decoder) throw new Error("desktop_decoder_not_ready");
  const clip = new Uint8Array(await (await fetch(syntheticClipURL)).arrayBuffer());
  const first = syntheticMetadata.access_units[0], firstUnit = clip.slice(first.offset, first.offset + first.length), nals = splitAnnexBNALUnits(firstUnit);
  const sps = nals.find(nal => (nal[0] & 0x1f) === 7), pps = nals.find(nal => (nal[0] & 0x1f) === 8);
  if (!sps || !pps) throw new Error("desktop_synthetic_parameters_missing");
  await decoder.configure({ codec: syntheticMetadata.codec, width: syntheticMetadata.width, height: syntheticMetadata.height, description: buildAVCDecoderConfigurationRecord(sps, pps), generation, displayID });
  for (const unit of syntheticMetadata.access_units) {
    decoder.decode(clip.slice(unit.offset, unit.offset + unit.length), { timestamp: unit.index * 33_333, duration: 33_333, keyframe: unit.keyframe, generation });
    if (unit.keyframe) onKeyframe();
    await new Promise(resolve => window.setTimeout(resolve, 33));
  }
}
