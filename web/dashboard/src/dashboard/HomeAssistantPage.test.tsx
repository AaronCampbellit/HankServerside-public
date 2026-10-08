import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HomeAssistantPage } from "./HomeAssistantPage";

const homeAssistantClient = vi.hoisted(() => ({
  load: vi.fn(),
  saveDashboardTiles: vi.fn(),
  callService: vi.fn(),
  fetchState: vi.fn(),
  fetchStates: vi.fn(),
  onStateChanged: vi.fn(),
}));

vi.mock("../api/homeAssistant", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/homeAssistant")>();
  return {
    ...actual,
    homeAssistantClient,
  };
});

describe("HomeAssistantPage", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("replaces authoritative entity state after reconnect, including removed entities", async () => {
    let reconnect: (() => void) | undefined;
    homeAssistantClient.onStateChanged.mockImplementation((_listener, onReconnect) => { reconnect = onReconnect; return () => {}; });
    homeAssistantClient.load.mockResolvedValue({ agent: null, profile: { revision: 1, settings: {} }, dashboardEntityIDs: [], states: [
      { entity_id: "sensor.removed", state: "1", attributes: { friendly_name: "Removed sensor" } },
    ] });
    homeAssistantClient.fetchStates.mockResolvedValue([{ entity_id: "sensor.current", state: "2", attributes: { friendly_name: "Current sensor" } }]);
    render(<HomeAssistantPage />);
    await screen.findByText("Removed sensor");
    reconnect?.();
    await screen.findByText("Current sensor");
    expect(screen.queryByText("Removed sensor")).not.toBeInTheDocument();
  });

  it("bounds the initial entity result set and reveals more on demand", async () => {
    const states = Array.from({ length: 35 }, (_, index) => ({
      entity_id: `sensor.room_${index + 1}`,
      state: String(index + 1),
      attributes: { friendly_name: `Room ${index + 1}` },
    }));
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: { revision: 2, settings: { dashboard_tiles: [] } },
      dashboardEntityIDs: [],
      states,
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});

    render(<HomeAssistantPage />);

    const table = await screen.findByRole("table", { name: "All Home Assistant entities" });
    expect(within(table).getAllByRole("row")).toHaveLength(25);
    expect(screen.getByText("Showing 24 of 35 entities")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Show more entities" }));

    await waitFor(() => {
      expect(within(screen.getByRole("table", { name: "All Home Assistant entities" })).getAllByRole("row")).toHaveLength(36);
    });
    expect(screen.getByText("Showing 35 of 35 entities")).toBeInTheDocument();
  });

  it("keeps the HTML reference dashboard as the first Home Assistant surface", async () => {
    const dashboardEntityIDs = [
      "light.living_room",
      "light.kitchen",
      "climate.thermostat",
      "lock.front_door",
      "fan.bedroom",
      "media_player.living_room_tv",
      "sensor.humidity",
      "alarm_control_panel.security",
    ];
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: {
        revision: 2,
        settings: { dashboard_tiles: dashboardEntityIDs.map((entity_id) => ({ entity_id, is_enabled: true })) },
      },
      dashboardEntityIDs,
      states: [
        { entity_id: "light.living_room", state: "on", attributes: { friendly_name: "Living Room", brightness: 204 } },
        { entity_id: "light.kitchen", state: "off", attributes: { friendly_name: "Kitchen" } },
        { entity_id: "climate.thermostat", state: "heat", attributes: { friendly_name: "Thermostat", current_temperature: 21.5, unit_of_measurement: "°C" } },
        { entity_id: "lock.front_door", state: "locked", attributes: { friendly_name: "Front Door" } },
        { entity_id: "fan.bedroom", state: "off", attributes: { friendly_name: "Bedroom Fan" } },
        { entity_id: "media_player.living_room_tv", state: "playing", attributes: { friendly_name: "Living Room TV" } },
        { entity_id: "sensor.humidity", state: "47", attributes: { friendly_name: "Humidity", unit_of_measurement: "%" } },
        { entity_id: "alarm_control_panel.security", state: "armed_home", attributes: { friendly_name: "Security" } },
      ],
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});

    render(<HomeAssistantPage />);

    expect(await screen.findByText("8 entities · 8 on your dashboard")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Connector" })).not.toBeInTheDocument();

    const dashboard = screen.getByRole("region", { name: "Your dashboard" });
    expect(dashboard).toHaveClass("ha-dashboard-panel");
    expect(dashboard.querySelector(".ha-dashboard-grid")).not.toBeNull();
    expect(within(dashboard).getAllByRole("article")).toHaveLength(8);
    expect(within(dashboard).getByText("Living Room")).toBeInTheDocument();
    expect(within(dashboard).getByText("On · 80%")).toBeInTheDocument();
    expect(within(dashboard).getByText("21.5°C · heat")).toBeInTheDocument();
    expect(within(dashboard).getByText("Armed · home")).toBeInTheDocument();
  });

  it("renders dashboard entities as switch-style redesign tiles", async () => {
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: { revision: 2, settings: { dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }] } },
      dashboardEntityIDs: ["light.kitchen"],
      states: [
        { entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light" } },
        { entity_id: "sensor.humidity", state: "47", attributes: { friendly_name: "Humidity", unit_of_measurement: "%" } },
      ],
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});
    homeAssistantClient.callService.mockResolvedValue({});
    homeAssistantClient.fetchState.mockResolvedValue({
      entity_id: "light.kitchen",
      state: "off",
      attributes: { friendly_name: "Kitchen Light" },
    });

    render(<HomeAssistantPage />);

    expect((await screen.findAllByText("Kitchen Light")).length).toBeGreaterThan(0);
    const toggle = screen.getByRole("switch", { name: "Toggle Kitchen Light" });
    expect(toggle).toHaveAttribute("aria-checked", "true");

    fireEvent.click(toggle);

    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("light.kitchen", "light", "turn_off"));
    await waitFor(() => expect(homeAssistantClient.fetchState).toHaveBeenCalledWith("light.kitchen"));
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  });

  it("matches the guide layout for the dashboard and all-entities table", async () => {
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: { revision: 2, settings: { dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }] } },
      dashboardEntityIDs: ["light.kitchen"],
      states: [
        { entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light" } },
        { entity_id: "sensor.humidity", state: "47", attributes: { friendly_name: "Humidity", unit_of_measurement: "%" } },
      ],
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});

    render(<HomeAssistantPage />);

    expect(await screen.findByText("2 entities · 1 on your dashboard")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add tile" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Your dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "All entities" })).toBeInTheDocument();

    const table = screen.getByRole("table", { name: "All Home Assistant entities" });
    expect(within(table).getByRole("columnheader", { name: "Entity" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Domain" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "State" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Tile" })).toBeInTheDocument();
    expect(within(table).getByRole("cell", { name: "sensor" })).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Add Humidity to dashboard" })).toBeInTheDocument();
    const humidityRow = within(table).getByRole("row", { name: /Humidity/ });
    expect(humidityRow.querySelector('[data-label="Entity"]')).not.toBeNull();
    expect(humidityRow.querySelector('[data-label="Domain"]')).not.toBeNull();
    expect(humidityRow.querySelector('[data-label="State"]')).not.toBeNull();
    expect(humidityRow.querySelector('[data-label="Tile"]')).not.toBeNull();
  });

  it("offers open/close for covers and run for scripts on dashboard tiles", async () => {
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: {
        revision: 2,
        settings: { dashboard_tiles: [{ entity_id: "cover.garage_door", is_enabled: true }, { entity_id: "script.toggle_garage_lights", is_enabled: true }] },
      },
      dashboardEntityIDs: ["cover.garage_door", "script.toggle_garage_lights"],
      states: [
        { entity_id: "cover.garage_door", state: "closed", attributes: { friendly_name: "Garage Door" } },
        { entity_id: "script.toggle_garage_lights", state: "off", attributes: { friendly_name: "Toggle Garage Lights" } },
      ],
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});
    homeAssistantClient.callService.mockResolvedValue({});

    render(<HomeAssistantPage />);

    const dashboard = await screen.findByRole("region", { name: "Your dashboard" });
    const coverSwitch = within(dashboard).getByRole("switch", { name: "Toggle Garage Door" });
    expect(coverSwitch).toHaveAttribute("aria-checked", "false");

    fireEvent.click(coverSwitch);
    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("cover.garage_door", "cover", "open_cover"));

    fireEvent.click(within(dashboard).getByRole("button", { name: "Run Toggle Garage Lights" }));
    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("script.toggle_garage_lights", "script", "turn_on"));
  });

  it("uses active switches for toggleable entities in the all-entities table", async () => {
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online" },
      profile: { revision: 2, settings: { dashboard_tiles: [] } },
      dashboardEntityIDs: [],
      states: [
        { entity_id: "light.kitchen", state: "off", attributes: { friendly_name: "Kitchen Light" } },
        { entity_id: "sensor.humidity", state: "47", attributes: { friendly_name: "Humidity", unit_of_measurement: "%" } },
      ],
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});
    homeAssistantClient.callService.mockResolvedValue({});

    render(<HomeAssistantPage />);

    const table = await screen.findByRole("table", { name: "All Home Assistant entities" });
    const toggle = within(table).getByRole("switch", { name: "Turn on Kitchen Light" });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(within(table).getByText("47")).toBeInTheDocument();

    fireEvent.click(toggle);

    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("light.kitchen", "light", "turn_on"));
  });
});
