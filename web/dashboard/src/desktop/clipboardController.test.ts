import { describe, expect, it, vi } from "vitest";
import { DesktopClipboardController } from "./clipboardController";

function fixture(permissions = ["desktop.clipboard.read", "desktop.clipboard.write"]) {
  const readText = vi.fn().mockResolvedValue("local text"), writeText = vi.fn().mockResolvedValue(undefined), send = vi.fn(), status = vi.fn();
  const controller = new DesktopClipboardController({ permissions: new Set(permissions), clipboard: { readText, writeText }, send, status });
  return { controller, readText, writeText, send, status };
}

describe("DesktopClipboardController", () => {
  it("never reads automatically and requires independent write permission", async () => {
    const { controller, readText, send } = fixture(["desktop.clipboard.read"]);
    expect(readText).not.toHaveBeenCalled();
    await controller.pasteToRemote();
    expect(readText).not.toHaveBeenCalled(); expect(send).not.toHaveBeenCalled();
  });

  it("reads local text only from explicit paste and enforces 1 MiB before send", async () => {
    const allowed = fixture(["desktop.control", "desktop.clipboard.write"]); allowed.controller.updateControl({ active: true, enabled: true, focused: true }); await allowed.controller.pasteToRemote();
    expect(allowed.send).toHaveBeenCalledWith(expect.any(Number), { direction: "browser_to_agent", text: "local text" });
    const oversized = fixture(["desktop.control", "desktop.clipboard.write"]); oversized.controller.updateControl({ active: true, enabled: true, focused: true }); oversized.readText.mockResolvedValue("x".repeat((1 << 20) + 1));
    await oversized.controller.pasteToRemote(); expect(oversized.send).not.toHaveBeenCalled(); expect(oversized.status).toHaveBeenCalledWith("clipboard_too_large");
  });

  it("requires a second explicit gesture to write received remote text", async () => {
    const { controller, send, writeText } = fixture(["desktop.clipboard.read"]);
    await controller.copyFromRemote(); expect(send).toHaveBeenCalled();
    controller.acceptRemoteText("remote text"); await Promise.resolve();
    expect(writeText).not.toHaveBeenCalled(); expect(controller.remoteTextReady).toBe(true);
    await controller.copyReadyText(); expect(writeText).toHaveBeenCalledWith("remote text");
    controller.acceptRemoteText("unsolicited"); await Promise.resolve(); expect(writeText).toHaveBeenCalledTimes(1);
  });

  it("blocks browser-to-agent clipboard in view-only and after stale focus loss", async () => {
    const value = fixture(["desktop.control", "desktop.clipboard.write"]);
    value.controller.updateControl({ active: true, enabled: false, focused: false });
    await value.controller.pasteToRemote(); expect(value.readText).not.toHaveBeenCalled(); expect(value.send).not.toHaveBeenCalled();
    value.controller.updateControl({ active: true, enabled: true, focused: true });
    value.controller.updateControl({ focused: false });
    await value.controller.pasteToRemote(); expect(value.send).not.toHaveBeenCalled(); expect(value.status).toHaveBeenCalledWith("control_focus_required");
  });

  it("treats unsupported or denied clipboard access as non-fatal and content-free", async () => {
    const send = vi.fn(), status = vi.fn();
    const unsupported = new DesktopClipboardController({ permissions: new Set(["desktop.control", "desktop.clipboard.write"]), send, status }); unsupported.updateControl({ active: true, enabled: true, focused: true });
    await unsupported.pasteToRemote(); expect(status).toHaveBeenCalledWith("clipboard_unavailable");
    const denied = fixture(["desktop.control", "desktop.clipboard.write"]); denied.controller.updateControl({ active: true, enabled: true, focused: true }); denied.readText.mockRejectedValue(new Error("secret clipboard value")); await denied.controller.pasteToRemote();
    expect(denied.status).toHaveBeenCalledWith("clipboard_denied"); expect(JSON.stringify(denied.status.mock.calls)).not.toContain("secret");
  });
});
