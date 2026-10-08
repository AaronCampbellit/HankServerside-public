import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type ServiceType = "homeassistant" | "smb" | "hermes" | string;

export type ServiceProfile = {
  home_id: string;
  service_type: ServiceType;
  public_config_json?: string;
  secret_version: number;
  applied_version: number;
  status: string;
  updated_at: string;
  updated_by: string;
  last_backup_at?: string | null;
  last_error?: string;
};

export type ServiceProfilesPayload = {
  profiles: ServiceProfile[];
};

export type SaveServiceProfileInput = {
  public_config: Record<string, unknown>;
  secrets?: Record<string, unknown>;
  persist: boolean;
};

export type SMBTestInput = {
  id: string;
  name: string;
  host: string;
  share: string;
  username: string;
  password?: string;
  domain: string;
};

export type SMBTestResult = { ok: boolean };

export class ConnectionsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async listProfiles(): Promise<ServiceProfilesPayload> {
    const payload = await this.api.request<Partial<ServiceProfilesPayload>>("/v1/home/service-profiles");
    return { profiles: arrayFrom<ServiceProfile>(payload.profiles) };
  }

  saveProfile(serviceType: ServiceType, input: SaveServiceProfileInput) {
    return this.api.request<ServiceProfile>(`/v1/home/service-profiles/${encodeURIComponent(serviceType)}`, {
      method: "PUT",
      body: input,
    });
  }

  testSMB(input: SMBTestInput) {
    return this.api.request<SMBTestResult>("/v1/home/service-profiles/smb/test", {
      method: "POST",
      body: input,
    });
  }
}

export const connectionsClient = new ConnectionsClient();
