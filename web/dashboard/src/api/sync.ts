import { apiClient, type ApiTransport } from "./client";

export type SyncProfileStatus = {
  service_type?: string;
  status?: string;
  updated_at?: string;
  last_backup_at?: string;
  last_error?: string;
};

export type SyncStatus = {
  notes?: {
    status?: string;
    last_successful_sync_at?: string;
    last_error?: string;
  };
  profiles?: Record<string, SyncProfileStatus>;
};

export class SyncClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  status(): Promise<SyncStatus> {
    return this.api.request<SyncStatus>("/v1/home/sync");
  }
}

export const syncClient = new SyncClient();
