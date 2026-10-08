import { describe, expect, it } from "vitest";
import { DesktopQualityController, desktopQualityLevels, type DesktopHealthSample } from "./qualityController";

const healthy = (atMS: number): DesktopHealthSample => ({ atMS, rttMS: 40, decoderQueue: 0, decodedFrames: 300, droppedFrames: 0, senderQueueBytes: 0, relayBackpressureCount: 0 });
const unhealthy = (atMS: number): DesktopHealthSample => ({ atMS, rttMS: 450, decoderQueue: 8, decodedFrames: 90, droppedFrames: 10, senderQueueBytes: 17 << 20, relayBackpressureCount: 2 });

describe("DesktopQualityController", () => {
  it("uses the canonical bounded levels", () => {
    expect(desktopQualityLevels).toEqual([
      { name: "low", scale: .5, fps: 15, bitrateBPS: 1_000_000 },
      { name: "balanced", scale: .75, fps: 30, bitrateBPS: 4_000_000 },
      { name: "high", scale: 1, fps: 30, bitrateBPS: 8_000_000 },
      { name: "ultra", scale: 1, fps: 60, bitrateBPS: 20_000_000 },
    ]);
  });

  it("applies manual quality immediately and keeps deterministic adaptive fallback", () => {
    const controller = new DesktopQualityController();
    expect(controller.reset()).toMatchObject({ level: "balanced", forceKeyframe: true, generation: 1 });
    expect(controller.observe(unhealthy(1_000))).toBeNull();
    expect(controller.observe(unhealthy(6_000))).toMatchObject({ level: "low", forceKeyframe: true, generation: 2 });
    expect(controller.observe(unhealthy(8_000))).toBeNull();
    expect(controller.observe(healthy(10_000))).toBeNull();
    expect(controller.observe(healthy(30_000))).toMatchObject({ level: "balanced", generation: 3 });
    expect(controller.setMaximum("high", 31_000)).toMatchObject({ level: "high", generation: 4, forceKeyframe: true });
    expect(controller.observe(unhealthy(32_000))).toBeNull();
    expect(controller.observe(unhealthy(36_000))).toMatchObject({ level: "balanced", generation: 5, forceKeyframe: true });
  });

  it("reacts to interactive-latency backlog before the old multi-second limits", () => {
    const controller = new DesktopQualityController();
    controller.reset();
    const queued = { ...healthy(1_000), decoderQueue: 3, senderQueueBytes: 513 << 10 };
    expect(controller.observe(queued)).toBeNull();
    expect(controller.observe({ ...queued, atMS: 6_000 })).toMatchObject({ level: "low" });
  });
});
