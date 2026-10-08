import { describe, expect, it, vi } from "vitest";
import { ApiError, type ApiTransport } from "./client";
import { mergeDefaultKanbanBoard, ProfileSettingsClient } from "./profileSettings";

describe("ProfileSettingsClient", () => {
  it("loads and saves profile settings with the expected revision", async () => {
    const request = vi.fn(async <T>() => ({ revision: 8, settings: {} }) as T);
    const client = new ProfileSettingsClient({ request: request as unknown as ApiTransport["request"] });

    await client.load();
    await client.save(7, { dashboard: { density: "compact" }, kanban_default_board_id: "work" });

    expect(request).toHaveBeenNthCalledWith(1, "/v1/me/profile");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/me/profile", {
      method: "PUT",
      body: {
        expected_revision: 7,
        settings: { dashboard: { density: "compact" }, kanban_default_board_id: "work" },
      },
    });
  });

  it("merges and clears only the default Kanban board", () => {
    const current = { dashboard: { density: "compact" }, assistant: { model: "gpt" } };
    expect(mergeDefaultKanbanBoard(current, "work")).toEqual({ ...current, kanban_default_board_id: "work" });
    expect(mergeDefaultKanbanBoard({ ...current, kanban_default_board_id: "work" }, "")).toEqual(current);
  });

  it("treats missing profile settings as an empty first revision", async () => {
    const request = vi.fn(async () => {
      throw new ApiError(404, "not_found", "not found", { error: "not found" });
    });
    const client = new ProfileSettingsClient({ request: request as unknown as ApiTransport["request"] });

    await expect(client.load()).resolves.toEqual({ revision: 0, settings: {} });
  });
});
