import { describe, expect, it, vi } from "vitest";
import { DesktopClient } from "./desktop";

describe("DesktopClient", () => {
  it("creates, reconnects, and terminates without placing credentials in URLs", async () => {
    const request = vi.fn().mockResolvedValue({ session_id: "desk_1", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_1" });
    const client = new DesktopClient({ request });
    const session = await client.create("agent_1", "device_1", ["desktop.view"]);
    expect(request).toHaveBeenNthCalledWith(1, "/v1/agents/agent_1/desktop-sessions", expect.objectContaining({ method: "POST" }));
    expect(session.websocket_path).not.toMatch(/[?&](token|credential)=/);
    await client.reconnect("desk_1"); await client.terminate("desk_1");
    expect(request).toHaveBeenNthCalledWith(3, "/v1/desktop-sessions/desk_1/terminate", { method: "POST" });
  });
});
