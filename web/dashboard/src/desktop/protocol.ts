export const DESKTOP_INNER_VERSION = 1;
export const DESKTOP_MAX_CONTROL_PAYLOAD = 256 << 10;
export const DESKTOP_MAX_VIDEO_PAYLOAD = 4 << 20;
export const DESKTOP_MAX_CLIPBOARD_WIRE_PAYLOAD = 6 * (1 << 20) + 128;
export const DESKTOP_MAX_ENCRYPTED_FRAME_PAYLOAD = DESKTOP_MAX_CLIPBOARD_WIRE_PAYLOAD + 64;
export const DESKTOP_MAX_WHEEL_DELTA = 1_000_000;

export enum DesktopFrameKind { BrowserHandshake = 1, AgentHandshake = 2, EncryptedRecord = 3 }
export enum DesktopMessageFlag { Optional = 1 }
export enum DesktopMessageType {
  CodecConfig = 1, VideoAccessUnit = 2, PointerShape = 3, DisplayInventory = 4,
  Keyboard = 10, Pointer = 11, ClipboardOffer = 12, ClipboardText = 13,
  DisplaySelection = 14, SpecialKey = 15,
  ControlMode = 20, Quality = 21, Ping = 30, Pong = 31, Statistics = 32,
  SecureState = 40, PermissionState = 41, Terminate = 255,
}

const knownTypes = new Set<number>(Object.values(DesktopMessageType).filter((value): value is number => typeof value === "number"));
const dataMagic = new Uint8Array([0x48, 0x44, 0x56, 0x31]);

export interface DesktopDataFrame { kind: DesktopFrameKind; payload: Uint8Array }
export interface DesktopInnerMessage { version: number; flags: number; type: number; payload: Uint8Array; unknownOptional: boolean }

export function encodeDesktopDataFrame(kind: DesktopFrameKind, payload: Uint8Array): Uint8Array {
  if (![1, 2, 3].includes(kind) || payload.byteLength > DESKTOP_MAX_ENCRYPTED_FRAME_PAYLOAD) throw new Error("desktop_data_frame_invalid");
  const output = new Uint8Array(12 + payload.byteLength);
  output.set(dataMagic); output[4] = kind;
  new DataView(output.buffer).setUint32(8, payload.byteLength, false);
  output.set(payload, 12);
  return output;
}

export function decodeDesktopDataFrame(frame: Uint8Array): DesktopDataFrame {
  if (frame.byteLength < 12 || !dataMagic.every((value, index) => frame[index] === value) || frame[5] !== 0 || frame[6] !== 0 || frame[7] !== 0) throw new Error("desktop_data_frame_invalid");
  const kind = frame[4] as DesktopFrameKind;
  const length = new DataView(frame.buffer, frame.byteOffset, frame.byteLength).getUint32(8, false);
  if (![1, 2, 3].includes(kind) || length + 12 !== frame.byteLength || length > DESKTOP_MAX_ENCRYPTED_FRAME_PAYLOAD) throw new Error("desktop_data_frame_invalid");
  return { kind, payload: frame.slice(12) };
}

export function encodeDesktopInnerMessage(type: number, flags: number, payload: Uint8Array): Uint8Array {
  validateInner(type, flags, payload.byteLength);
  if (!knownTypes.has(type) && (flags & DesktopMessageFlag.Optional) === 0) throw new Error("desktop_required_message_unknown");
  const output = new Uint8Array(8 + payload.byteLength);
  const view = new DataView(output.buffer);
  output[0] = DESKTOP_INNER_VERSION; output[1] = flags;
  view.setUint16(2, type, false); view.setUint32(4, payload.byteLength, false);
  output.set(payload, 8);
  return output;
}

export function decodeDesktopInnerMessage(encoded: Uint8Array): DesktopInnerMessage {
  if (encoded.byteLength < 8) throw new Error("desktop_inner_invalid");
  const view = new DataView(encoded.buffer, encoded.byteOffset, encoded.byteLength);
  const version = encoded[0], flags = encoded[1], type = view.getUint16(2, false), length = view.getUint32(4, false);
  if (version !== DESKTOP_INNER_VERSION) throw new Error("desktop_inner_invalid");
  validateInner(type, flags, length);
  if (length + 8 !== encoded.byteLength) throw new Error("desktop_inner_bounds");
  const unknownOptional = !knownTypes.has(type);
  if (unknownOptional && (flags & DesktopMessageFlag.Optional) === 0) throw new Error("desktop_required_message_unknown");
  return { version, flags, type, payload: encoded.slice(8), unknownOptional };
}

function validateInner(type: number, flags: number, length: number): void {
  if (!Number.isInteger(type) || type < 0 || type > 0xffff || (flags & ~DesktopMessageFlag.Optional) !== 0) throw new Error("desktop_inner_invalid");
  const limit = type === DesktopMessageType.VideoAccessUnit ? DESKTOP_MAX_VIDEO_PAYLOAD : type === DesktopMessageType.ClipboardText ? DESKTOP_MAX_CLIPBOARD_WIRE_PAYLOAD : DESKTOP_MAX_CONTROL_PAYLOAD;
  if (length > limit) throw new Error("desktop_inner_bounds");
}

export interface CodecConfigPayload { codec: string; generation: number; display_id: string; width: number; height: number; description_base64url: string }
export interface VideoAccessUnitMetadata { generation: number; timestamp_us: number; duration_us: number; keyframe: boolean }
export interface DesktopDisplay { id: string; name: string; x: number; y: number; width: number; height: number; scale: number; primary: boolean; rotation: 0 | 90 | 180 | 270 }
export interface DisplayInventoryPayload { displays: DesktopDisplay[] }
export interface DesktopKeyboardPayload extends Record<string, unknown> { code: string; scan_code: number; location: number; down: boolean; repeat: boolean; shift: boolean; control: boolean; alt: boolean; meta: boolean; event_unix_ms: number }
export interface DesktopPointerPayload extends Record<string, unknown> { display_id: string; generation: number; kind: "move" | "down" | "up" | "wheel"; x: number; y: number; button: number; buttons: number; wheel_x?: number; wheel_y?: number; event_unix_ms: number }
export interface DesktopClipboardTextPayload { direction: "browser_to_agent" | "agent_to_browser"; text: string }
export interface DesktopControlModePayload { enabled: boolean; focus_lease: number }
export interface DesktopDisplaySelectionPayload { display_id: string; generation: number }
export type DesktopSpecialKeyName = "alt_tab" | "windows_l" | "ctrl_alt_delete" | "command_space" | "command_option_escape" | "command_control_q";
export interface DesktopSpecialKeyPayload { name: DesktopSpecialKeyName }
export type KeyboardPayload = DesktopKeyboardPayload;
export type PointerPayload = DesktopPointerPayload;
export type ClipboardTextPayload = DesktopClipboardTextPayload;
export interface StatisticsPayload { frames: number; bytes: number; rtt_ms: number }
export interface TerminatePayload { reason_code: string; message?: string }

export interface DesktopStreamState { generation: number; display_id: string; width: number; height: number; codec?: string }

export function validateDisplayDescriptor(display: DesktopDisplay): void {
  if (!display.id.trim() || !display.name.trim() || !Number.isInteger(display.width) || display.width <= 0 ||
      !Number.isInteger(display.height) || display.height <= 0 || !Number.isFinite(display.x) || !Number.isFinite(display.y) ||
      !Number.isFinite(display.scale) || display.scale <= 0 || ![0, 90, 180, 270].includes(display.rotation)) {
    throw new Error("desktop_display_invalid");
  }
}

export function validatePointerEvent(event: DesktopPointerPayload): void {
  const wheelX = event.wheel_x ?? 0, wheelY = event.wheel_y ?? 0;
  if (!event.display_id.trim() || event.display_id.length > 128 || !Number.isSafeInteger(event.generation) || event.generation <= 0 ||
      !["move", "down", "up", "wheel"].includes(event.kind) || !Number.isFinite(event.x) || event.x < 0 || event.x > 1 ||
      !Number.isFinite(event.y) || event.y < 0 || event.y > 1 || !Number.isFinite(wheelX) || Math.abs(wheelX) > DESKTOP_MAX_WHEEL_DELTA ||
      !Number.isFinite(wheelY) || Math.abs(wheelY) > DESKTOP_MAX_WHEEL_DELTA || !Number.isInteger(event.button) || event.button < -1 || event.button > 4 ||
      !Number.isInteger(event.buttons) || event.buttons < 0 || event.buttons > 31 || !Number.isSafeInteger(event.event_unix_ms) || event.event_unix_ms < 0) {
    throw new Error("desktop_pointer_invalid");
  }
}

export function validateKeyboardEvent(event: DesktopKeyboardPayload): void {
  if (!event.code || event.code.length > 64 || !Number.isInteger(event.scan_code) || event.scan_code < 0 || event.scan_code > 0xffffffff ||
      !Number.isInteger(event.location) || event.location < 0 || event.location > 3 || !Number.isSafeInteger(event.event_unix_ms) || event.event_unix_ms < 0) {
    throw new Error("desktop_keyboard_invalid");
  }
}

export function validateClipboardText(value: DesktopClipboardTextPayload): void {
  if (!["browser_to_agent", "agent_to_browser"].includes(value.direction) || new TextEncoder().encode(value.text).byteLength > (1 << 20)) throw new Error("desktop_clipboard_invalid");
}
export function validateControlMode(value: DesktopControlModePayload): void { if (value.enabled && (!Number.isSafeInteger(value.focus_lease) || value.focus_lease <= 0)) throw new Error("desktop_control_mode_invalid"); }
export function validateDisplaySelection(value: DesktopDisplaySelectionPayload): void { if (!value.display_id.trim() || value.display_id.length > 128 || !Number.isSafeInteger(value.generation) || value.generation <= 0) throw new Error("desktop_display_selection_invalid"); }
export function validateSpecialKey(value: DesktopSpecialKeyPayload | { name: string }): void { if (!["alt_tab", "windows_l", "ctrl_alt_delete", "command_space", "command_option_escape", "command_control_q"].includes(value.name)) throw new Error("desktop_special_key_invalid"); }

export function validateStreamTransition(previous: DesktopStreamState | null, next: CodecConfigPayload): void {
  if (!Number.isSafeInteger(next.generation) || next.generation <= 0 || !next.display_id || next.width <= 0 || next.height <= 0) {
    throw new Error("desktop_stream_generation_invalid");
  }
  if (!previous) return;
  const changed = previous.display_id !== next.display_id || previous.width !== next.width || previous.height !== next.height ||
    (previous.codec !== undefined && previous.codec !== next.codec);
  if (next.generation < previous.generation || (next.generation === previous.generation && changed)) {
    throw new Error("desktop_stream_generation_invalid");
  }
}
