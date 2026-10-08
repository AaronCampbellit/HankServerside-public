import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type StorageConfig = {
  target?: { type?: string; path?: string };
  schedule?: {
    full_backup_cron?: string;
    differential_backup_cron?: string;
    checksum_interval_seconds?: number;
    retention_full?: number;
    amcheck_cron?: string;
    restore_verification_cron?: string;
    restore_verification_enabled?: boolean;
  };
  restore?: { confirmation_phrase?: string; primary_restore_enabled?: boolean };
};

export type StorageBackup = {
  label: string;
  type?: string;
  stopped_at?: string;
  size_bytes?: number;
};

export type StorageTask = {
  operation?: string;
  status?: string;
  message?: string;
  step?: string;
  updated_at?: string;
  backup_label?: string;
  backup_type?: string;
};

export type StorageEvent = {
  operation?: string;
  severity?: string;
  message?: string;
  time?: string;
  backup_label?: string;
};

export type QueryTelemetryRow = {
  calls?: number;
  rows?: number;
  total_exec_ms?: number;
  mean_exec_ms?: number;
  query?: string;
  Calls?: number;
  Rows?: number;
  TotalExecMS?: number;
  MeanExecMS?: number;
  Query?: string;
};

export type StorageStatus = {
  checksum?: {
    enabled?: boolean;
    corruption_detected?: boolean;
    last_check_at?: string;
    failure_count?: number;
    last_error?: string;
  };
  backup?: {
    last_successful_at?: string;
    backups?: StorageBackup[];
  };
  restore?: {
    last_test_at?: string;
  };
  config?: StorageConfig;
  tasks?: StorageTask[];
  events?: StorageEvent[];
};

export class StorageClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async status(): Promise<StorageStatus> {
    const payload = await this.api.request<StorageStatus>("/v1/home/storage/status");
    return {
      ...payload,
      backup: {
        ...payload.backup,
        backups: arrayFrom<StorageBackup>(payload.backup?.backups),
      },
      tasks: arrayFrom<StorageTask>(payload.tasks),
      events: arrayFrom<StorageEvent>(payload.events),
    };
  }

  saveConfig(config: StorageConfig) {
    return this.api.request<{ config: StorageConfig }>("/v1/home/storage/config", { method: "PUT", body: config });
  }

  requestBackup(backupType: "full" | "diff") {
    return this.api.request<{ intent_id?: string }>("/v1/home/storage/backup", {
      method: "POST",
      body: { backup_type: backupType },
    });
  }

  requestRestoreTest(backupLabel: string) {
    return this.api.request<{ intent_id?: string }>("/v1/home/storage/restore-test", {
      method: "POST",
      body: { backup_label: backupLabel },
    });
  }

  reviewPrimaryRestore(backupLabel: string) {
    return this.api.request<{ admin_action_token: string }>("/v1/home/storage/restore-primary", {
      method: "POST", body: { backup_label: backupLabel, request_action_token: true },
    });
  }

  requestPrimaryRestore(backupLabel: string, confirmation: string, token: string) {
    return this.api.request("/v1/home/storage/restore-primary", {
      method: "POST", body: { backup_label: backupLabel, confirmation, admin_action_token: token },
    });
  }

  clearEvents() {
    return this.api.request<{ ok?: boolean }>("/v1/home/storage/events", { method: "DELETE" });
  }

  async queryTelemetry(limit = 20): Promise<{ queries: QueryTelemetryRow[] }> {
    const payload = await this.api.request<{ queries?: QueryTelemetryRow[] }>(`/v1/home/query-telemetry?limit=${limit}`);
    return { queries: arrayFrom<QueryTelemetryRow>(payload.queries) };
  }
}

export const storageClient = new StorageClient();
