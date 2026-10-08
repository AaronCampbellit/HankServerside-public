import { HankSocket, type HankSocketEvent } from "../socket/HankSocket";
import { apiClient, type ApiTransport } from "./client";
import type { AgentPayload, HomeAgent } from "./home";
import { arrayFrom } from "./normalize";

export type HomeAssistantEntity = {
  entity_id: string;
  state: string;
  attributes?: Record<string, unknown>;
};

export type UserProfile = {
  revision: number;
  settings: Record<string, unknown>;
};

export type HomeAssistantLoadPayload = {
  agent: HomeAgent | null;
  profile: UserProfile;
  dashboardEntityIDs: string[];
  states: HomeAssistantEntity[];
};

export type HomeAssistantSocket = {
  subscribe(topics: string[]): Promise<unknown>;
  sendCommand<T>(command: string, body?: unknown): Promise<T>;
  onEvent(listener: (event: HankSocketEvent) => void): () => void;
};

type FetchStatesResponse = {
  states?: HomeAssistantEntity[];
};

type FetchStateResponse = {
  state?: HomeAssistantEntity;
};

export function entityName(entity: HomeAssistantEntity): string {
  const name = entity.attributes?.friendly_name;
  return typeof name === "string" && name.trim() ? name : entity.entity_id;
}

export function entityDomain(entityID: string): string {
  return String(entityID || "").split(".")[0] || "entity";
}

export function normalizeDashboardEntityIDs(settings: Record<string, unknown> | undefined): string[] {
  const tiles = Array.isArray(settings?.dashboard_tiles) ? settings.dashboard_tiles : [];
  const seen = new Set<string>();
  const entityIDs: string[] = [];
  for (const tile of tiles) {
    if (!tile || typeof tile !== "object") continue;
    const record = tile as Record<string, unknown>;
    const entityID = String(record.entity_id || "").trim();
    if (!entityID || seen.has(entityID) || record.is_enabled === false) continue;
    seen.add(entityID);
    entityIDs.push(entityID);
  }
  return entityIDs;
}

export class HomeAssistantClient {
  constructor(
    private readonly api: ApiTransport = apiClient,
    private readonly socket: HomeAssistantSocket = new HankSocket(),
  ) {}

  async load(): Promise<HomeAssistantLoadPayload> {
    const [agentPayload, profile] = await Promise.all([
      this.api.request<AgentPayload>("/v1/home/agent"),
      this.api.request<UserProfile>("/v1/me/profile"),
    ]);
    await this.socket.subscribe(["homeassistant.states"]);
    const states = await this.fetchStates();
    return {
      agent: agentPayload.agent,
      profile: { revision: profile.revision || 0, settings: profile.settings || {} },
      dashboardEntityIDs: normalizeDashboardEntityIDs(profile.settings),
      states,
    };
  }

  async fetchStates(): Promise<HomeAssistantEntity[]> {
    const payload = await this.socket.sendCommand<FetchStatesResponse>("homeassistant.fetch_states");
    return arrayFrom<HomeAssistantEntity>(payload.states).sort((left, right) => entityName(left).localeCompare(entityName(right)));
  }

  async fetchState(entityID: string): Promise<HomeAssistantEntity | null> {
    const payload = await this.socket.sendCommand<FetchStateResponse>("homeassistant.fetch_state", { entity_id: entityID });
    return payload.state || null;
  }

  saveDashboardTiles(revision: number, settings: Record<string, unknown>, entityIDs: string[]): Promise<UserProfile> {
    return this.api.request<UserProfile>("/v1/me/profile", {
      method: "PUT",
      body: {
        expected_revision: revision > 0 ? revision : null,
        settings: {
          ...settings,
          dashboard_tiles: entityIDs.map((entityID) => ({ entity_id: entityID, is_enabled: true })),
        },
      },
    });
  }

  callService(entityID: string, domain: string, service: string): Promise<unknown> {
    return this.socket.sendCommand("homeassistant.call_service", {
      domain,
      service,
      body: { entity_id: entityID },
    });
  }

  onStateChanged(listener: (entity: HomeAssistantEntity) => void, onReconnect?: () => void): () => void {
    return this.socket.onEvent((event) => {
      if (event.event === "socket.reconnected") { onReconnect?.(); return; }
      if (event.topic !== "homeassistant.states" || event.event !== "homeassistant.state_changed") return;
      const body = decodeEventBody(event.body);
      const entity = body?.state || body;
      if (entity && typeof entity === "object" && "entity_id" in entity) listener(entity as HomeAssistantEntity);
    });
  }
}

function decodeEventBody(body: unknown): Record<string, unknown> | null {
  if (!body) return null;
  if (typeof body === "string") {
    try {
      return JSON.parse(body) as Record<string, unknown>;
    } catch {
      return null;
    }
  }
  return typeof body === "object" ? (body as Record<string, unknown>) : null;
}

export const homeAssistantClient = new HomeAssistantClient();
