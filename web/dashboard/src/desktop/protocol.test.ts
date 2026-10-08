import { describe, expect, it } from "vitest";
import {
  DesktopFrameKind,
  DesktopMessageFlag,
  DesktopMessageType,
  decodeDesktopDataFrame,
  decodeDesktopInnerMessage,
  encodeDesktopDataFrame,
  encodeDesktopInnerMessage,
  validateDisplayDescriptor,
	validateClipboardText,
	validateControlMode,
	validateDisplaySelection,
	validateKeyboardEvent,
	validatePointerEvent,
	validateSpecialKey,
  validateStreamTransition,
} from "./protocol";
import vectors from "../../../../schemas/desktop/v1/test-vectors.json";

describe("desktop encrypted data contract", () => {
  it("matches the canonical inner type ids", () => {
    expect(DesktopMessageType.VideoAccessUnit).toBe(2);
    expect(DesktopMessageType.Keyboard).toBe(10);
    expect(DesktopMessageType.Terminate).toBe(255);
    expect(DesktopFrameKind.BrowserHandshake).toBe(1);
  });

  it("round trips pre-key and inner frames", () => {
    const payload = new TextEncoder().encode('{"code":"KeyA"}');
    const inner = encodeDesktopInnerMessage(DesktopMessageType.Keyboard, 0, payload);
    expect(Array.from(decodeDesktopInnerMessage(inner).payload)).toEqual(Array.from(payload));
    const frame = encodeDesktopDataFrame(DesktopFrameKind.EncryptedRecord, inner);
    expect(decodeDesktopDataFrame(frame)).toEqual({ kind: DesktopFrameKind.EncryptedRecord, payload: inner });
  });

  it("skips optional unknown messages and rejects required unknown messages", () => {
    const optional = encodeDesktopInnerMessage(0x7fff, DesktopMessageFlag.Optional, new Uint8Array());
    expect(decodeDesktopInnerMessage(optional).unknownOptional).toBe(true);
    const required = optional.slice();
    required[1] = 0;
    expect(() => decodeDesktopInnerMessage(required)).toThrow("desktop_required_message_unknown");
  });

  it("keeps generic control at 256 KiB while allowing a 1 MiB clipboard JSON record", () => {
    expect(() => encodeDesktopInnerMessage(DesktopMessageType.Quality, 0, new Uint8Array(256 << 10))).not.toThrow();
    expect(() => encodeDesktopInnerMessage(DesktopMessageType.Quality, 0, new Uint8Array((256 << 10) + 1))).toThrow("desktop_inner_bounds");
    const clipboard = new TextEncoder().encode(JSON.stringify({ direction: "browser_to_agent", text: "x".repeat(1 << 20) }));
    expect(clipboard.byteLength).toBeGreaterThan(256 << 10);
    const inner = encodeDesktopInnerMessage(DesktopMessageType.ClipboardText, 0, clipboard);
    expect(decodeDesktopInnerMessage(inner).payload.byteLength).toBe(clipboard.byteLength);
  });

  it("requires stable display identity and positive geometry", () => {
    expect(() => validateDisplayDescriptor({ id: "display-1", name: "Primary", x: 0, y: 0, width: 1920, height: 1080, scale: 2, primary: true, rotation: 0 })).not.toThrow();
    expect(() => validateDisplayDescriptor({ id: "", name: "Primary", x: 0, y: 0, width: 1920, height: 1080, scale: 2, primary: true, rotation: 0 })).toThrow("desktop_display_invalid");
    expect(() => validateDisplayDescriptor({ id: "display-1", name: "Primary", x: 0, y: 0, width: 0, height: 1080, scale: 2, primary: true, rotation: 0 })).toThrow("desktop_display_invalid");
  });

  it("rejects geometry changes without a generation increment", () => {
    const previous = { generation: 4, display_id: "display-1", width: 1920, height: 1080 };
    expect(() => validateStreamTransition(previous, { codec: "avc1.42E01E", generation: 4, display_id: "display-1", width: 1280, height: 720, description_base64url: "" })).toThrow("desktop_stream_generation_invalid");
    expect(() => validateStreamTransition(previous, { codec: "avc1.42E01E", generation: 5, display_id: "display-1", width: 1280, height: 720, description_base64url: "" })).not.toThrow();
  });

	it("validates bounded native-control payloads", () => {
		expect(() => validatePointerEvent({ display_id: "display-1", generation: 3, kind: "move", x: 0.5, y: 0.25, button: -1, buttons: 0, event_unix_ms: 1 })).not.toThrow();
		expect(() => validatePointerEvent({ display_id: "display-1", generation: 3, kind: "move", x: 1.01, y: 0.25, button: -1, buttons: 0, event_unix_ms: 1 })).toThrow("desktop_pointer_invalid");
		expect(() => validatePointerEvent({ display_id: "display-1", generation: 3, kind: "wheel", x: 0.5, y: 0.5, button: -1, buttons: 0, wheel_y: 1_000_000, event_unix_ms: 1 })).not.toThrow();
		expect(() => validatePointerEvent({ display_id: "display-1", generation: 3, kind: "wheel", x: 0.5, y: 0.5, button: -1, buttons: 0, wheel_y: 1_000_001, event_unix_ms: 1 })).toThrow("desktop_pointer_invalid");
		expect(() => validateKeyboardEvent({ code: "ShiftLeft", scan_code: 42, location: 1, down: true, repeat: false, shift: true, control: false, alt: false, meta: false, event_unix_ms: 1 })).not.toThrow();
		expect(() => validateClipboardText({ direction: "browser_to_agent", text: "x".repeat((1 << 20) + 1) })).toThrow("desktop_clipboard_invalid");
		expect(() => validateControlMode({ enabled: true, focus_lease: 0 })).toThrow("desktop_control_mode_invalid");
		expect(() => validateDisplaySelection({ display_id: "display-2", generation: 0 })).toThrow("desktop_display_selection_invalid");
		expect(() => validateSpecialKey({ name: "arbitrary_command" })).toThrow("desktop_special_key_invalid");
	});

	it("consumes the canonical native-control JSON bytes", () => {
		const decode = (value: string) => JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(value.replaceAll("-", "+").replaceAll("_", "/")), character => character.charCodeAt(0)))) as Record<string, unknown>;
		const control = vectors.control_semantics;
		const pointer = decode(control.pointer_edges_base64url); validatePointerEvent(pointer as never); expect(pointer.generation).toBe(0xffffffff);
		const wheel = decode(control.wheel_bound_base64url); validatePointerEvent(wheel as never); expect(wheel.wheel_y).toBe(1_000_000);
		expect(() => validatePointerEvent(decode(control.wheel_overflow_base64url) as never)).toThrow("desktop_pointer_invalid");
		const keyboard = decode(control.keyboard_modifiers_base64url); validateKeyboardEvent(keyboard as never); expect(keyboard).toMatchObject({ repeat: true, location: 2 });
		expect(() => validateSpecialKey(decode(control.special_key_base64url) as never)).not.toThrow();
		expect(() => validateSpecialKey(decode(control.unknown_special_key_base64url) as never)).toThrow("desktop_special_key_invalid");
	});
});
