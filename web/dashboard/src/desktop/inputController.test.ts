import { describe, expect, it, vi } from "vitest";
import { DesktopInputController, fittedContentRect } from "./inputController";

const rect = { left: 10, top: 20, width: 100, height: 50 } as DOMRect;
const pointer = (type: string, values: Partial<PointerEvent & WheelEvent> = {}) => ({ type, clientX: 60, clientY: 45, button: -1, buttons: 0, deltaX: 0, deltaY: 0, ...values }) as unknown as PointerEvent;
const key = (type: string, values: Partial<KeyboardEvent> = {}) => ({ type, code: "KeyA", location: 0, repeat: false, shiftKey: false, ctrlKey: false, altKey: false, metaKey: false, preventDefault: vi.fn(), ...values }) as unknown as KeyboardEvent;

function fixture() {
  const sent: Array<{ type: number; payload: Record<string, unknown> }> = [];
  let queued: FrameRequestCallback | undefined;
  const controller = new DesktopInputController({
    send: (type, payload) => { sent.push({ type, payload }); },
    contentRect: () => rect,
    requestFrame: callback => { queued = callback; return 1; }, cancelFrame: () => { queued = undefined; },
  });
  controller.update({ active: true, control: true, reconnecting: false, visible: true, displayID: "display-1", generation: 3 });
  return { controller, sent, flushFrame: () => { const callback = queued; queued = undefined; callback?.(0); } };
}

describe("DesktopInputController", () => {
  it("sends input only with active control and focus lease", () => {
    const { controller, sent, flushFrame } = fixture();
    controller.pointer(pointer("pointermove")); flushFrame();
    expect(sent).toEqual([]);
    const lease = controller.focus();
    controller.pointer(pointer("pointermove")); flushFrame();
    expect(sent).toHaveLength(1);
    controller.confirmFocus(lease, true); controller.pointer(pointer("pointermove")); flushFrame();
    expect(sent[1].payload).toMatchObject({ x: 0.5, y: 0.5, display_id: "display-1", generation: 3 });
  });

  it("coalesces moves but never buttons or wheel", () => {
    const { controller, sent, flushFrame } = fixture(); const lease = controller.focus(); controller.confirmFocus(lease, true);
    controller.pointer(pointer("pointermove", { clientX: 20 })); controller.pointer(pointer("pointermove", { clientX: 70 }));
    controller.pointer(pointer("pointerdown", { button: 0, buttons: 1 })); controller.pointer(pointer("wheel", { deltaY: 12 }));
    expect(sent.filter(item => item.payload.kind === "down")).toHaveLength(1);
    expect(sent.filter(item => item.payload.kind === "wheel")).toHaveLength(1);
    flushFrame();
    expect(sent.filter(item => item.payload.kind === "move")).toHaveLength(1);
    expect(sent.at(-1)?.payload.x).toBe(0.6);
  });

  it("releases remote modifiers and buttons on blur", () => {
    const { controller, sent } = fixture(); const lease = controller.focus(); controller.confirmFocus(lease, true);
    controller.key(key("keydown", { code: "ShiftLeft", shiftKey: true }));
    controller.pointer(pointer("pointerdown", { button: 0, buttons: 1 }));
    controller.blur();
    expect(sent.some(item => item.payload.code === "ShiftLeft" && item.payload.down === false)).toBe(true);
    expect(sent.some(item => item.payload.kind === "up" && item.payload.button === 0)).toBe(true);
    expect(sent.find(item => item.payload.kind === "up" && item.payload.button === 0)?.payload).toMatchObject({ x: 0.5, y: 0.5 });
    expect(sent.at(-1)?.payload).toMatchObject({ enabled: false });
  });

  it("releases held buttons at the last pointer location and rejects unsafe wheel deltas", () => {
    const { controller, sent } = fixture(); const lease = controller.focus(); controller.confirmFocus(lease, true);
    controller.pointer(pointer("pointerdown", { clientX: 70, clientY: 30, button: 0, buttons: 1 }));
    expect(controller.pointer(pointer("wheel", { deltaY: 1e308 }))).toBe(false);
    controller.update({ control: false });
    const release = sent.find(item => item.payload.kind === "up" && item.payload.button === 0);
    expect(release?.payload).toMatchObject({ x: 0.6, y: 0.2 });
    expect(sent.some(item => item.payload.kind === "wheel")).toBe(false);
  });

  it("invalidates focus on stale display, reconnect, hidden state, and control disable", () => {
    for (const update of [{ generation: 4 }, { reconnecting: true }, { visible: false }, { control: false }]) {
      const { controller, sent } = fixture(); const lease = controller.focus(); controller.confirmFocus(lease, true);
      controller.update(update); controller.key(key("keydown"));
      expect(sent.filter(item => item.payload.code === "KeyA")).toHaveLength(0);
    }
  });

  it("maps a 4:3 display inside a 16:9 fit box without using letterbox bars", () => {
    const fitted = fittedContentRect({ left: 0, top: 0, width: 160, height: 90 } as DOMRect, 800, 600);
    expect(fitted).toMatchObject({ left: 20, top: 0, width: 120, height: 90 });
  });
});
