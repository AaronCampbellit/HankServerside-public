import { describe, expect, it } from "vitest";
import { acceptAccessUnit, applyCodecConfiguration, applyDisplayInventory, initialDisplayState } from "./displayStore";

const primary = { id: "display-1", name: "Main Display", x: 0, y: 0, width: 1920, height: 1080, scale: 2, primary: true, rotation: 0 as const };
const secondary = { id: "display-2", name: "Second Display", x: 1920, y: 0, width: 1280, height: 720, scale: 1, primary: false, rotation: 0 as const };

describe("displayStore", () => {
  it("replaces inventory and deterministically falls back when the current display is removed", () => {
    const selected = applyCodecConfiguration(applyDisplayInventory(initialDisplayState(), [primary, secondary]), {
      codec: "avc1.42E01E", generation: 1, display_id: secondary.id, width: secondary.width, height: secondary.height, description_base64url: "AQ==",
    });
    const removed = applyDisplayInventory(selected, [primary]);
    expect(removed.inventory).toEqual([primary]);
    expect(removed.selectedID).toBe(primary.id);
    expect(removed.generation).toBe(0);
    expect(acceptAccessUnit(removed, { generation: 1, timestamp_us: 1, duration_us: 33_333, keyframe: true })).toBe(false);
    expect(() => applyCodecConfiguration(removed, {
      codec: "avc1.42E01E", generation: 1, display_id: primary.id, width: primary.width, height: primary.height, description_base64url: "AQ==",
    })).toThrow("desktop_stream_generation_invalid");
  });

  it("advances generation, rejects stale access units, and retains fit/actual scale", () => {
    let state = applyDisplayInventory(initialDisplayState("actual"), [primary]);
    state = applyCodecConfiguration(state, { codec: "avc1.42E01E", generation: 7, display_id: primary.id, width: 1920, height: 1080, description_base64url: "AQ==" });
    expect(state.mode).toBe("actual");
    expect(acceptAccessUnit(state, { generation: 6, timestamp_us: 1, duration_us: 33_333, keyframe: true })).toBe(false);
    expect(acceptAccessUnit(state, { generation: 7, timestamp_us: 2, duration_us: 33_333, keyframe: true })).toBe(true);
    expect(() => applyCodecConfiguration(state, { codec: "avc1.42E01E", generation: 7, display_id: primary.id, width: 1280, height: 720, description_base64url: "AQ==" })).toThrow("desktop_stream_generation_invalid");
  });

  it("invalidates the stream when selected-display geometry changes", () => {
    const configured = applyCodecConfiguration(applyDisplayInventory(initialDisplayState(), [primary]), {
      codec: "avc1.42E01E", generation: 2, display_id: primary.id, width: primary.width, height: primary.height, description_base64url: "AQ==",
    });
    const resized = applyDisplayInventory(configured, [{ ...primary, width: 1080, height: 1920, rotation: 90 }]);
    expect(resized.generation).toBe(0);
    expect(resized.minimumGeneration).toBe(3);
    expect(acceptAccessUnit(resized, { generation: 2, timestamp_us: 1, duration_us: 33_333, keyframe: true })).toBe(false);
  });
});
