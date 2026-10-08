import { describe, expect, it } from "vitest";
import { specialKeysForPlatform } from "./specialKeys";

describe("specialKeysForPlatform", () => {
  it("shows only Windows keys and disables secure attention", () => {
    const values = specialKeysForPlatform("windows");
    expect(values.map(value => value.name)).toEqual(["alt_tab", "windows_l", "ctrl_alt_delete"]);
    expect(values.at(-1)).toMatchObject({ disabled: true, reason: "Requires privileged control" });
  });
  it("shows only macOS normal keys", () => {
    expect(specialKeysForPlatform("macos").map(value => value.name)).toEqual(["command_space", "command_option_escape", "command_control_q"]);
  });
});
