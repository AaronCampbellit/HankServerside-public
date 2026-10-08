import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type AuditEvent = {
  id?: string;
  home_id?: string;
  event_type?: string;
  severity?: string;
  target_type?: string;
  target_id?: string;
  actor_user_id?: string;
  actor_agent_id?: string;
  actor_label?: string;
  target_label?: string;
  request_id?: string;
  helper_text?: string;
  occurred_at?: string;
  metadata?: Record<string, unknown>;
};

export type AuditEventFilters = {
  event_type?: string;
  severity?: string;
  target_type?: string;
  sort?: string;
  order?: string;
  limit?: number | string;
};

export class LogsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async listAuditEvents(filters: AuditEventFilters = {}): Promise<{ events: AuditEvent[] }> {
    const params = new URLSearchParams();
    if (filters.event_type) params.set("event_type", String(filters.event_type));
    if (filters.severity) params.set("severity", String(filters.severity));
    if (filters.target_type) params.set("target_type", String(filters.target_type));
    params.set("sort", String(filters.sort || "occurred_at"));
    params.set("order", String(filters.order || "desc"));
    params.set("limit", String(filters.limit || 100));
    const payload = await this.api.request<{ events?: AuditEvent[] }>(`/v1/home/audit-events?${params.toString()}`);
    return { events: arrayFrom<AuditEvent>(payload.events) };
  }
}

export const logsClient = new LogsClient();
