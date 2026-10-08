export type DesktopPermissionState =
  | "secure_desktop_entered" | "secure_desktop_exited" | "secure_desktop_unavailable" | "secure_attention_unavailable"
  | "screen_recording_required" | "accessibility_required" | "permission_restored"
  | "console_locked" | "console_user_switched" | "helper_restarting" | "helper_failed"
  | "indicator_lost" | "indicator_restored";

const states = new Set<DesktopPermissionState>([
  "secure_desktop_entered", "secure_desktop_exited", "secure_desktop_unavailable", "secure_attention_unavailable",
  "screen_recording_required", "accessibility_required", "permission_restored", "console_locked", "console_user_switched",
  "helper_restarting", "helper_failed", "indicator_lost", "indicator_restored",
]);
export function isDesktopPermissionState(value: unknown): value is DesktopPermissionState { return typeof value === "string" && states.has(value as DesktopPermissionState); }

export type DesktopReadinessOverlay = {
  tone: "info" | "warning" | "error";
  title: string;
  detail: string;
  blocksVideo: boolean;
  blocksInput: boolean;
  localAction?: "open_screen_recording" | "open_accessibility";
};

const overlays: Partial<Record<DesktopPermissionState, DesktopReadinessOverlay>> = {
  secure_desktop_entered: { tone: "info", title: "Windows secure desktop", detail: "The endpoint moved to the protected Windows desktop.", blocksVideo: false, blocksInput: false },
  secure_desktop_unavailable: { tone: "error", title: "Secure desktop unavailable", detail: "Capture and control are paused because the protected desktop could not be authenticated.", blocksVideo: true, blocksInput: true },
  secure_attention_unavailable: { tone: "warning", title: "Secure attention unavailable", detail: "Ctrl+Alt+Delete requires the installed Hank service policy and cannot be simulated as ordinary input.", blocksVideo: false, blocksInput: true },
  screen_recording_required: { tone: "error", title: "Screen Recording required", detail: "A person at the remote Mac must grant Screen Recording to the signed HankAgent app.", blocksVideo: true, blocksInput: true },
  accessibility_required: { tone: "warning", title: "Accessibility required", detail: "Viewing can continue, but a person at the remote Mac must grant Accessibility before control is available.", blocksVideo: false, blocksInput: true },
  console_locked: { tone: "info", title: "Console locked", detail: "Video and input are paused until the same local user unlocks the physical console.", blocksVideo: true, blocksInput: true },
  console_user_switched: { tone: "error", title: "Console user changed", detail: "The authenticated host grant was revoked when the physical console user changed.", blocksVideo: true, blocksInput: true },
  helper_restarting: { tone: "warning", title: "Desktop helper restarting", detail: "The signed console helper is making its single supervised restart attempt.", blocksVideo: true, blocksInput: true },
  helper_failed: { tone: "error", title: "Desktop helper failed", detail: "The helper failed repeatedly, so the session was stopped.", blocksVideo: true, blocksInput: true },
  indicator_lost: { tone: "error", title: "Remote access indicator unavailable", detail: "Capture and control are paused while the required local indicator is restored.", blocksVideo: true, blocksInput: true },
};

export function readinessOverlay(state: DesktopPermissionState): DesktopReadinessOverlay | null {
  return overlays[state] ?? null;
}
