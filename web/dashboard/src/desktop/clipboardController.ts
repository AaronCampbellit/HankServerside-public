import { DesktopMessageType } from "./protocol";

interface ClipboardPort { readText(): Promise<string>; writeText(value: string): Promise<void> }
interface DesktopClipboardDependencies {
  permissions: ReadonlySet<string>;
  clipboard?: ClipboardPort;
  send(type: DesktopMessageType, payload: Record<string, unknown>): void;
  status(reason: string): void;
}

export class DesktopClipboardController {
  private remoteReadPending = false;
  private readyText?: string;
  private control = { active: false, enabled: false, focused: false };
  constructor(private readonly dependencies: DesktopClipboardDependencies) {}

  updateControl(next: Partial<typeof this.control>): void { this.control = { ...this.control, ...next }; }
  get remoteTextReady(): boolean { return this.readyText !== undefined; }

  async pasteToRemote(): Promise<void> {
    if (!this.dependencies.permissions.has("desktop.clipboard.write")) { this.dependencies.status("clipboard_write_not_granted"); return; }
    if (!this.dependencies.permissions.has("desktop.control") || !this.control.active || !this.control.enabled || !this.control.focused) { this.dependencies.status("control_focus_required"); return; }
    if (!this.dependencies.clipboard) { this.dependencies.status("clipboard_unavailable"); return; }
    try {
      const text = await this.dependencies.clipboard.readText();
      if (new TextEncoder().encode(text).byteLength > (1 << 20)) { this.dependencies.status("clipboard_too_large"); return; }
      this.dependencies.send(DesktopMessageType.ClipboardText, { direction: "browser_to_agent", text });
    } catch { this.dependencies.status("clipboard_denied"); }
  }

  async copyFromRemote(): Promise<void> {
    if (!this.dependencies.permissions.has("desktop.clipboard.read")) { this.dependencies.status("clipboard_read_not_granted"); return; }
    if (!this.dependencies.clipboard) { this.dependencies.status("clipboard_unavailable"); return; }
    this.remoteReadPending = true; this.readyText = undefined; this.dependencies.status("clipboard_request_sent");
    this.dependencies.send(DesktopMessageType.ClipboardOffer, { direction: "agent_to_browser", formats: ["text/plain"] });
  }

  acceptRemoteText(text: string): void {
    if (!this.remoteReadPending) return;
    this.remoteReadPending = false;
    if (!this.dependencies.permissions.has("desktop.clipboard.read") || !this.dependencies.clipboard) return;
    if (new TextEncoder().encode(text).byteLength > (1 << 20)) { this.dependencies.status("clipboard_too_large"); return; }
    this.readyText = text; this.dependencies.status("clipboard_ready_to_copy");
  }

  async copyReadyText(): Promise<void> {
    const text = this.readyText; this.readyText = undefined;
    if (text === undefined || !this.dependencies.permissions.has("desktop.clipboard.read") || !this.dependencies.clipboard) { this.dependencies.status("clipboard_not_ready"); return; }
    try { await this.dependencies.clipboard.writeText(text); this.dependencies.status("clipboard_copied"); }
    catch { this.dependencies.status("clipboard_denied"); }
  }

  reset(): void { this.remoteReadPending = false; this.readyText = undefined; this.control = { active: false, enabled: false, focused: false }; }
}
