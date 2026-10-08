import { apiClient, type ApiTransport } from "./client";
import { arrayFrom, booleanFrom } from "./normalize";

export type Home = {
  id: string;
  user_id: string;
  name: string;
  agent_enrollment_policy?: "admins_only" | "all_users";
  created_at: string;
  updated_at: string;
};

export type HomeAgent = {
  agent_id: string;
  name: string;
  status: "online" | "offline" | string;
  last_seen_at?: string | null;
  home_id: string;
  home_name: string;
  capabilities?: string[];
};

export type AgentPayload = {
  agent: HomeAgent | null;
  can_restart: boolean;
};

export type AgentToken = {
  id: string;
  home_id: string;
  agent_id: string;
  revoked_at?: string | null;
  expires_at?: string | null;
  created_at: string;
};

export type AgentTokensPayload = {
  tokens: AgentToken[];
};

export type CreateAgentTokenInput = {
  agent_id: string;
  name: string;
  agent_type: "primary" | "worker";
  expires_in_seconds: number;
};

export type CreatedAgentToken = {
  token_id: string;
  home_id: string;
  agent_id: string;
  agent_name: string;
  agent_type: "primary" | "worker";
  token: string;
  expires_at?: string | null;
  created_at: string;
  agent_status?: string;
};

export class HomeClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  getHome() {
    return this.api.request<Home>("/v1/home");
  }

  renameHome(name: string) {
    return this.api.request<Home>("/v1/home", { method: "PUT", body: { name } });
  }

  enrollmentPolicy() { return this.api.request<{ policy: "admins_only" | "all_users" }>("/v1/home/agent/enrollment-policy"); }
  setEnrollmentPolicy(policy: "admins_only" | "all_users") { return this.api.request<{ policy: "admins_only" | "all_users" }>("/v1/home/agent/enrollment-policy", { method: "PUT", body: { policy } }); }

  async getAgent(): Promise<AgentPayload> {
    const payload = await this.api.request<Partial<AgentPayload>>("/v1/home/agent");
    return { agent: payload.agent || null, can_restart: booleanFrom(payload.can_restart) };
  }

  restartAgent() {
    return this.api.request<{ ok: boolean; message?: string; restart_at?: string }>("/v1/home/agent/restart", {
      method: "POST",
      body: {},
    });
  }

  async listAgentTokens(): Promise<AgentTokensPayload> {
    const payload = await this.api.request<Partial<AgentTokensPayload>>("/v1/home/agent/tokens");
    return { tokens: arrayFrom<AgentToken>(payload.tokens) };
  }

  createAgentToken(input: CreateAgentTokenInput) {
    return this.api.request<CreatedAgentToken>("/v1/home/agent/tokens", { method: "POST", body: input });
  }

  revokeAgentToken(id: string) {
    return this.api.request<{ ok: true }>(`/v1/home/agent/tokens/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

  removeAgentToken(id: string) {
    return this.api.request<{ ok: true }>(`/v1/home/agent/tokens/${encodeURIComponent(id)}?purge=1`, {
      method: "DELETE",
    });
  }

  removeAgent(agentID: string) {
    return this.api.request<{ ok: true }>(`/v1/home/agents/${encodeURIComponent(agentID)}`, { method: "DELETE" });
  }
}

export const homeClient = new HomeClient();
