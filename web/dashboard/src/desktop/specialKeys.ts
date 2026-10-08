import type { DesktopSpecialKeyName } from "./protocol";

export type DesktopPlatform = "windows" | "macos" | "unknown";
export interface DesktopSpecialKeyOption { name: DesktopSpecialKeyName; label: string; disabled: boolean; reason?: string }

export function specialKeysForPlatform(platform: DesktopPlatform): DesktopSpecialKeyOption[] {
  if (platform === "windows") return [
    { name: "alt_tab", label: "Alt+Tab", disabled: false },
    { name: "windows_l", label: "Windows+L", disabled: false },
    { name: "ctrl_alt_delete", label: "Ctrl+Alt+Delete", disabled: true, reason: "Requires privileged control" },
  ];
  if (platform === "macos") return [
    { name: "command_space", label: "Command+Space", disabled: false },
    { name: "command_option_escape", label: "Command+Option+Escape", disabled: false },
    { name: "command_control_q", label: "Command+Control+Q", disabled: false },
  ];
  return [];
}
