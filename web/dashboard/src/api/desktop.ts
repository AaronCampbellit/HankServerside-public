import { apiClient, type ApiTransport } from "./client";

export interface DesktopSessionAuthorization {
  session_id: string; home_id?: string; agent_id: string; operator_user_id?: string; operator_device_id?: string;
  permissions?: string[]; state: string; key_epoch: number; join_expires_at?: string;
  reconnect_expires_at?: string | null; hard_expires_at?: string; termination_reason?: string;
  endpoint_certificate?: string; endpoint_certificate_fingerprint?: string; websocket_path: string;
  trust_root_generation?: number; trust_root_public_key_spki?: string; trust_root_fingerprint?: string;
  operator_identity_status?: "active" | "revoked"; operator_identity_checked_at?: string;
}

export interface DesktopTrustSnapshot {
  configured: boolean;
  root?: { generation: number; algorithm: string; authority_mode?: "client_managed" | "server_managed" | string; fingerprint: string; public_key_spki: string; recovery_envelope?: string };
  identities: Array<{ identity_id: string; identity_type: string; user_id: string; device_id: string; agent_id: string; fingerprint: string; capabilities: string[]; created_at?: string; expires_at?: string; revoked_at?: string | null }>;
}

export class DesktopClient {
  constructor(private readonly api: ApiTransport = apiClient) {}
  create(agentID: string, operatorDeviceID: string, permissions: string[]): Promise<DesktopSessionAuthorization> {
    return this.api.request(`/v1/agents/${encodeURIComponent(agentID)}/desktop-sessions`, { method: "POST", body: { operator_device_id: operatorDeviceID, permissions } });
  }
  get(sessionID: string): Promise<DesktopSessionAuthorization> { return this.api.request(`/v1/desktop-sessions/${encodeURIComponent(sessionID)}`); }
  reconnect(sessionID: string): Promise<DesktopSessionAuthorization> { return this.api.request(`/v1/desktop-sessions/${encodeURIComponent(sessionID)}/reconnect`, { method: "POST" }); }
  terminate(sessionID: string, keepalive = false): Promise<DesktopSessionAuthorization> {
    return this.api.request(`/v1/desktop-sessions/${encodeURIComponent(sessionID)}/terminate`, { method: "POST", ...(keepalive ? { keepalive: true } : {}) });
  }
  trust(): Promise<DesktopTrustSnapshot> { return this.api.request("/v1/home/desktop-trust"); }
}

export const desktopClient = new DesktopClient();
