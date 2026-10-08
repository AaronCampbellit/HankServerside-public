import { describe, expect, it, vi } from "vitest";
import { HomeAssistantClient, type HomeAssistantSocket } from "./homeAssistant";
import type { ApiTransport } from "./client";

describe("HomeAssistantClient", () => {
  it("loads agent, profile dashboard tiles, subscribes, and fetches states", async () => {
    const request = vi.fn(async <T>(path: string) => {
      if (path === "/v1/home/agent") {
        return { agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" }, can_restart: true } as T;
      }
      if (path === "/v1/me/profile") {
        return {
          revision: 4,
          settings: {
            dashboard_tiles: [
              { entity_id: "light.kitchen", is_enabled: true },
              { entity_id: "switch.garage", is_enabled: false },
            ],
          },
        } as T;
      }
      throw new Error(`unexpected path ${path}`);
    });
    const socket = {
      subscribe: vi.fn(async () => ({})),
      sendCommand: vi.fn(async () => ({
        states: [{ entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light" } }],
      })),
      onEvent: vi.fn(),
    };
    const client = new HomeAssistantClient({ request: request as unknown as ApiTransport["request"] }, socket as unknown as HomeAssistantSocket);

    const payload = await client.load();

    expect(payload.agent?.name).toBe("Kitchen Mac");
    expect(payload.profile.revision).toBe(4);
    expect(payload.dashboardEntityIDs).toEqual(["light.kitchen"]);
    expect(payload.states).toEqual([
      { entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light" } },
    ]);
    expect(socket.subscribe).toHaveBeenCalledWith(["homeassistant.states"]);
    expect(socket.sendCommand).toHaveBeenCalledWith("homeassistant.fetch_states");
  });

  it("fetches a single canonical entity state", async () => {
    const request = vi.fn();
    const socket = {
      subscribe: vi.fn(),
      sendCommand: vi.fn(async () => ({
        state: { entity_id: "light.kitchen", state: "off", attributes: { friendly_name: "Kitchen Light" } },
      })),
      onEvent: vi.fn(),
    };
    const client = new HomeAssistantClient({ request: request as unknown as ApiTransport["request"] }, socket as unknown as HomeAssistantSocket);

    const state = await client.fetchState("light.kitchen");

    expect(state).toEqual({ entity_id: "light.kitchen", state: "off", attributes: { friendly_name: "Kitchen Light" } });
    expect(socket.sendCommand).toHaveBeenCalledWith("homeassistant.fetch_state", { entity_id: "light.kitchen" });
  });

  it("saves dashboard tiles and calls Home Assistant services", async () => {
    const request = vi.fn(async <T>() => ({ revision: 5, settings: { dashboard_tiles: [] } }) as T);
    const socket = {
      subscribe: vi.fn(),
      sendCommand: vi.fn(async () => ({})),
      onEvent: vi.fn(),
    };
    const client = new HomeAssistantClient({ request: request as unknown as ApiTransport["request"] }, socket as unknown as HomeAssistantSocket);

    await client.saveDashboardTiles(4, { dashboard: { density: "compact" } }, ["light.kitchen"]);
    await client.callService("light.kitchen", "light", "turn_off");

    expect(request).toHaveBeenCalledWith("/v1/me/profile", {
      method: "PUT",
      body: {
        expected_revision: 4,
        settings: {
          dashboard: { density: "compact" },
          dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }],
        },
      },
    });
    expect(socket.sendCommand).toHaveBeenCalledWith("homeassistant.call_service", {
      domain: "light",
      service: "turn_off",
      body: { entity_id: "light.kitchen" },
    });
  });
});
