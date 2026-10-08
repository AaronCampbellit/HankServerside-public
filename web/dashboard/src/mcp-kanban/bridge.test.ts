import { describe, expect, it, vi } from "vitest";
import { createKanbanBridge, type AppLike } from "./bridge";

describe("createKanbanBridge", () => {
  it("delivers host tool results to active subscribers", () => {
    const app: AppLike = { callServerTool: vi.fn() };
    const bridge = createKanbanBridge(app);
    const listener = vi.fn();
    const unsubscribe = bridge.subscribe(listener);
    const result = { structuredContent: { state_version: "2" } };

    app.ontoolresult?.(result);
    expect(listener).toHaveBeenCalledWith(result);

    unsubscribe();
    app.ontoolresult?.({ structuredContent: { state_version: "3" } });
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("calls the exact MCP tool and returns its structured result", async () => {
    const callServerTool = vi.fn().mockResolvedValue({ structuredContent: { state_version: "2" } });
    const bridge = createKanbanBridge({ callServerTool });

    await expect(bridge.callTool("open_kanban", { board_id: "work" })).resolves.toEqual({ structuredContent: { state_version: "2" } });
    expect(callServerTool).toHaveBeenCalledWith({ name: "open_kanban", arguments: { board_id: "work" } });
  });

  it("normalizes bridge failures without swallowing the cause", async () => {
    const bridge = createKanbanBridge({ callServerTool: vi.fn().mockRejectedValue(new Error("transport closed")) });

    await expect(bridge.callTool("open_kanban", {})).rejects.toThrow("Hank tool call failed: transport closed");
  });
});
