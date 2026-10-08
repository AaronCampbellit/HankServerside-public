import { describe, expect, it, vi } from "vitest";
import { SearchClient } from "./search";
import type { ApiTransport } from "./client";
import type { HomeAssistantClient } from "./homeAssistant";

describe("SearchClient", () => {
  it("merges cloud, file, and Home Assistant search results", async () => {
    const request = vi.fn(async <T>() => ({
      query: "kitchen",
      results: [
        { type: "note", title: "House Manual", subtitle: "The kitchen plan is attached.", url: "/dashboard/profile-notes?note=manual" },
        { type: "page", title: "Kitchen Notes", url: "/dashboard/profile-notes" },
        { type: "file", title: "kitchen-plan.pdf", subtitle: "Documents · Docs/kitchen-plan.pdf", url: "/dashboard/file-server?agent_id=agent-1&path=Docs%2Fkitchen-plan.pdf&preview=1&source_id=docs" },
      ],
    }) as T);
    const homeAssistant = {
      fetchStates: vi.fn(async () => [
        { entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light" } },
        { entity_id: "sensor.garage", state: "closed", attributes: { friendly_name: "Garage Door" } },
      ]),
    };
    const client = new SearchClient(
      { request: request as unknown as ApiTransport["request"] },
      homeAssistant as unknown as HomeAssistantClient,
    );

    const { results } = await client.search("kitchen");

    expect(request).toHaveBeenCalledWith("/v1/home/search?q=kitchen", { signal: undefined, timeoutMs: 3000 });
    expect(homeAssistant.fetchStates).toHaveBeenCalled();
    expect(results.map((result) => result.title)).toEqual([
      "Kitchen Notes",
      "House Manual",
      "kitchen-plan.pdf",
      "Kitchen Light",
    ]);
    expect(results[2]).toMatchObject({
      type: "file",
      subtitle: "Documents · Docs/kitchen-plan.pdf",
      url: "/dashboard/file-server?agent_id=agent-1&path=Docs%2Fkitchen-plan.pdf&preview=1&source_id=docs",
    });
    expect(results[3]).toMatchObject({
      type: "homeassistant",
      subtitle: "light.kitchen · on",
      url: "/dashboard/home-assistant?query=light.kitchen",
    });
  });

  it("orders navigation, docs, notes, files, then live entities", async () => {
    const client = new SearchClient(
      { request: vi.fn(async <T>() => ({
        query: "router",
        results: [
          { type: "note", title: "Router", url: "/dashboard/profile-notes?note=router" },
          { type: "page", title: "Router guide", subtitle: "Docs", url: "/docs/router" },
          { type: "page", title: "Network settings", subtitle: "Router", url: "/dashboard/settings/connections" },
          { type: "file", title: "router.txt", url: "/dashboard/file-server?agent_id=agent-1&source_id=docs&path=router.txt&preview=1" },
        ],
      }) as T) } as unknown as ApiTransport,
      { fetchStates: vi.fn(async () => [{ entity_id: "sensor.router", state: "online", attributes: { friendly_name: "Router" } }]) } as unknown as HomeAssistantClient,
    );

    const { results } = await client.search("router");

    expect(results.map((result) => result.title)).toEqual([
      "Network settings",
      "Router guide",
      "Router",
      "router.txt",
      "Router",
    ]);
  });

  it("reports an unavailable search when every source fails", async () => {
    const client = new SearchClient(
      { request: vi.fn().mockRejectedValue(new Error("cloud offline")) } as unknown as ApiTransport,
      { fetchStates: vi.fn().mockRejectedValue(new Error("home assistant offline")) } as unknown as HomeAssistantClient,
    );

    await expect(client.search("router")).rejects.toThrow("Search is unavailable");
  });
});
