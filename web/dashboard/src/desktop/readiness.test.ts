import { describe, expect, it } from "vitest";
import { isDesktopPermissionState, readinessOverlay, type DesktopPermissionState } from "./readiness";

describe("desktop privileged readiness", () => {
  it.each<[DesktopPermissionState, string, boolean, boolean]>([
    ["secure_desktop_entered", "Windows secure desktop", false, false],
    ["secure_desktop_unavailable", "Secure desktop unavailable", true, true],
    ["secure_attention_unavailable", "Secure attention unavailable", false, true],
    ["screen_recording_required", "Screen Recording required", true, true],
    ["accessibility_required", "Accessibility required", false, true],
    ["console_locked", "Console locked", true, true],
    ["console_user_switched", "Console user changed", true, true],
    ["helper_restarting", "Desktop helper restarting", true, true],
    ["helper_failed", "Desktop helper failed", true, true],
    ["indicator_lost", "Remote access indicator unavailable", true, true],
  ])("maps %s", (state, title, blocksVideo, blocksInput) => {
    expect(readinessOverlay(state)).toMatchObject({ title, blocksVideo, blocksInput });
  });

  it("clears overlays after safe restoration and never exposes remote settings actions", () => {
    expect(readinessOverlay("secure_desktop_exited")).toBeNull();
    expect(readinessOverlay("permission_restored")).toBeNull();
    expect(readinessOverlay("indicator_restored")).toBeNull();
    expect(readinessOverlay("screen_recording_required")).not.toHaveProperty("localAction");
    expect(readinessOverlay("accessibility_required")).not.toHaveProperty("localAction");
    expect(isDesktopPermissionState("permission_restored")).toBe(true);
    expect(isDesktopPermissionState("unknown_remote_state")).toBe(false);
  });
});
