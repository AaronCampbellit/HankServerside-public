import { DESKTOP_MAX_WHEEL_DELTA, DesktopMessageType, validatePointerEvent, type DesktopKeyboardPayload, type DesktopPointerPayload } from "./protocol";

export interface DesktopInputState {
  active: boolean; control: boolean; reconnecting: boolean; visible: boolean; displayID: string; generation: number;
}
interface DesktopInputDependencies {
  send(type: DesktopMessageType, payload: Record<string, unknown>): void;
  contentRect(): DOMRect;
  requestFrame(callback: FrameRequestCallback): number;
  cancelFrame(handle: number): void;
}

let nextLease = BigInt(Date.now()) * 1_000n;
const maxSafeLease = BigInt(Number.MAX_SAFE_INTEGER);

export class DesktopInputController {
  private state: DesktopInputState = { active: false, control: false, reconnecting: false, visible: true, displayID: "", generation: 0 };
  private lease = 0;
  private confirmedLease = 0;
  private heldKeys = new Map<string, DesktopKeyboardPayload>();
  private heldButtons = new Set<number>();
  private pendingMove?: DesktopPointerPayload;
  private frameHandle?: number;
  private lastPointer = { x: 0, y: 0 };

  constructor(private readonly dependencies: DesktopInputDependencies) {}

  update(next: Partial<DesktopInputState>): void {
    const invalidates = next.active === false || next.control === false || next.reconnecting === true || next.visible === false ||
      (next.displayID !== undefined && next.displayID !== this.state.displayID) || (next.generation !== undefined && next.generation !== this.state.generation);
    if (invalidates && this.lease !== 0) this.blur();
    this.state = { ...this.state, ...next };
  }

  focus(): number {
    if (!this.readyWithoutFocus()) return 0;
    if (this.lease !== 0) return this.lease;
    nextLease = nextLease >= maxSafeLease ? 1n : nextLease + 1n;
    this.lease = Number(nextLease);
    this.dependencies.send(DesktopMessageType.ControlMode, { enabled: true, focus_lease: this.lease });
    return this.lease;
  }

  confirmFocus(lease: number, applied: boolean): boolean {
    if (!applied || lease === 0 || lease !== this.lease || !this.readyWithoutFocus()) {
      if (lease === this.lease) this.confirmedLease = 0;
      return false;
    }
    this.confirmedLease = lease;
    return true;
  }

  blur(): void {
    if (this.frameHandle !== undefined) this.dependencies.cancelFrame(this.frameHandle);
    this.frameHandle = undefined; this.pendingMove = undefined;
    for (const value of this.heldKeys.values()) this.dependencies.send(DesktopMessageType.Keyboard, { ...value, down: false, repeat: false, event_unix_ms: Date.now() });
    this.heldKeys.clear();
    for (const button of this.heldButtons) this.dependencies.send(DesktopMessageType.Pointer, this.pointerPayload("up", button, 0, 0, 0));
    this.heldButtons.clear();
    if (this.lease !== 0) this.dependencies.send(DesktopMessageType.ControlMode, { enabled: false, focus_lease: this.lease });
    this.lease = 0; this.confirmedLease = 0;
  }

  dispose(): void { this.blur(); }
  get focused(): boolean { return this.lease !== 0 && this.confirmedLease === this.lease; }
  get focusLease(): number { return this.lease; }

  pointer(event: PointerEvent | WheelEvent): boolean {
    if (!this.ready()) return false;
    const kind = pointerKind(event.type);
    if (!kind) return false;
    if (kind === "wheel") {
      const deltaX = "deltaX" in event ? event.deltaX : Number.NaN, deltaY = "deltaY" in event ? event.deltaY : Number.NaN;
      if (!Number.isFinite(deltaX) || Math.abs(deltaX) > DESKTOP_MAX_WHEEL_DELTA || !Number.isFinite(deltaY) || Math.abs(deltaY) > DESKTOP_MAX_WHEEL_DELTA) return false;
    }
    const payload = this.pointerPayload(kind, "button" in event ? event.button : -1, "buttons" in event ? event.buttons : 0,
      "deltaX" in event ? event.deltaX : 0, "deltaY" in event ? event.deltaY : 0, event.clientX, event.clientY);
    try { validatePointerEvent(payload); } catch { return false; }
    if (kind === "move") {
      this.pendingMove = payload;
      if (this.frameHandle === undefined) this.frameHandle = this.dependencies.requestFrame(() => { this.frameHandle = undefined; const value = this.pendingMove; this.pendingMove = undefined; if (value && this.ready()) this.dependencies.send(DesktopMessageType.Pointer, value); });
      return true;
    }
    if (kind === "down" && payload.button >= 0) this.heldButtons.add(payload.button);
    if (kind === "up" && payload.button >= 0) this.heldButtons.delete(payload.button);
    this.dependencies.send(DesktopMessageType.Pointer, payload);
    return true;
  }

  key(event: KeyboardEvent): boolean {
    if (!this.ready() || !event.code) return false;
    const down = event.type === "keydown";
    if (!down && event.type !== "keyup") return false;
    const payload: DesktopKeyboardPayload = { code: event.code, scan_code: scanCode(event.code), location: event.location, down, repeat: event.repeat,
      shift: event.shiftKey, control: event.ctrlKey, alt: event.altKey, meta: event.metaKey, event_unix_ms: Date.now() };
    if (down) this.heldKeys.set(event.code, payload); else this.heldKeys.delete(event.code);
    this.dependencies.send(DesktopMessageType.Keyboard, payload);
    event.preventDefault();
    return true;
  }

  private readyWithoutFocus(): boolean { return this.state.active && this.state.control && !this.state.reconnecting && this.state.visible && Boolean(this.state.displayID) && this.state.generation > 0; }
  private ready(): boolean { return this.readyWithoutFocus() && this.lease !== 0 && this.confirmedLease === this.lease; }
  private pointerPayload(kind: DesktopPointerPayload["kind"], button: number, buttons: number, wheelX: number, wheelY: number, clientX?: number, clientY?: number): DesktopPointerPayload {
    const rect = this.dependencies.contentRect();
    const x = clientX === undefined ? this.lastPointer.x : rect.width <= 0 ? 0 : clamp((clientX - rect.left) / rect.width);
    const y = clientY === undefined ? this.lastPointer.y : rect.height <= 0 ? 0 : clamp((clientY - rect.top) / rect.height);
    if (clientX !== undefined && clientY !== undefined) this.lastPointer = { x, y };
    return { display_id: this.state.displayID, generation: this.state.generation, kind, x, y, button, buttons, wheel_x: wheelX, wheel_y: wheelY, event_unix_ms: Date.now() };
  }
}

function pointerKind(type: string): DesktopPointerPayload["kind"] | undefined {
  if (type === "pointermove") return "move"; if (type === "pointerdown") return "down"; if (type === "pointerup" || type === "pointercancel") return "up"; if (type === "wheel") return "wheel"; return undefined;
}
function clamp(value: number): number { return Math.max(0, Math.min(1, value)); }

export function fittedContentRect(container: DOMRect, contentWidth: number, contentHeight: number): DOMRect {
  if (container.width <= 0 || container.height <= 0 || contentWidth <= 0 || contentHeight <= 0) return container;
  const scale = Math.min(container.width / contentWidth, container.height / contentHeight);
  const width = contentWidth * scale, height = contentHeight * scale;
  return new DOMRect(container.left + (container.width - width) / 2, container.top + (container.height - height) / 2, width, height);
}

const scanCodes: Record<string, number> = {
  Escape: 0x01, Digit1: 0x02, Digit2: 0x03, Digit3: 0x04, Digit4: 0x05, Digit5: 0x06, Digit6: 0x07, Digit7: 0x08, Digit8: 0x09, Digit9: 0x0a, Digit0: 0x0b,
  Backspace: 0x0e, Tab: 0x0f, KeyQ: 0x10, KeyW: 0x11, KeyE: 0x12, KeyR: 0x13, KeyT: 0x14, KeyY: 0x15, KeyU: 0x16, KeyI: 0x17, KeyO: 0x18, KeyP: 0x19,
  Enter: 0x1c, ControlLeft: 0x1d, KeyA: 0x1e, KeyS: 0x1f, KeyD: 0x20, KeyF: 0x21, KeyG: 0x22, KeyH: 0x23, KeyJ: 0x24, KeyK: 0x25, KeyL: 0x26,
  ShiftLeft: 0x2a, KeyZ: 0x2c, KeyX: 0x2d, KeyC: 0x2e, KeyV: 0x2f, KeyB: 0x30, KeyN: 0x31, KeyM: 0x32, ShiftRight: 0x36, AltLeft: 0x38, Space: 0x39,
  ControlRight: 0x1d, AltRight: 0x38, MetaLeft: 0x5b, MetaRight: 0x5c, ArrowUp: 0x48, ArrowLeft: 0x4b, ArrowRight: 0x4d, ArrowDown: 0x50, Delete: 0x53,
};
function scanCode(code: string): number { return scanCodes[code] ?? 0; }
