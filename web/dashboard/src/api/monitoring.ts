import { apiClient } from "./client";

export type MonitoringSettings = {
  inbox_enabled: boolean;
  inbox_audience: "admins" | "members";
  email_enabled: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  email_from: string;
  email_to: string;
  updated_at: string;
};
export type MonitoringStatus = {
  settings: MonitoringSettings;
  smtp_password_set: boolean;
  delivery_status: "ready" | "pending";
};
export type MonitoringHealth = { ready: boolean; checked_at: string; checks: { id: string; label: string; ready: boolean }[] };
export const monitoringClient = {
  health: (signal?: AbortSignal) => apiClient.request<MonitoringHealth>("/v1/home/monitoring-settings/health", { signal }),
  get: (signal?: AbortSignal) => apiClient.request<MonitoringStatus>("/v1/home/monitoring-settings", { signal }),
  save: (settings: MonitoringSettings, password: string, clearPassword: boolean) => apiClient.request<MonitoringStatus>("/v1/home/monitoring-settings", {
    method: "PUT", body: { ...settings, smtp_password: password || undefined, clear_password: clearPassword },
  }),
  test: () => apiClient.request<{ queued: boolean }>("/v1/home/monitoring-settings/test", { method: "POST", body: {} }),
};
