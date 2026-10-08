import { apiClient, type ApiTransport } from "./client";
import { arrayFrom, booleanFrom } from "./normalize";

export type HomeQuickLink = {
  id: string;
  home_id: string;
  title: string;
  url: string;
  description?: string;
  sort_order: number;
  health_check_enabled: boolean;
  status: "up" | "down" | "unchecked" | "disabled" | string;
  status_code: number;
  last_checked_at?: string | null;
  last_error?: string;
  created_at: string;
  updated_at: string;
  updated_by: string;
};

export type QuickLinksPayload = {
  links: HomeQuickLink[];
  can_edit: boolean;
};

export type QuickLinkInput = {
  title: string;
  url: string;
  description: string;
  health_check_enabled: boolean;
};

export class QuickLinksClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async list(): Promise<QuickLinksPayload> {
    return normalizeQuickLinksPayload(await this.api.request<QuickLinksPayload>("/v1/home/quick-links"));
  }

  async check(): Promise<QuickLinksPayload> {
    return normalizeQuickLinksPayload(await this.api.request<QuickLinksPayload>("/v1/home/quick-links/checks", { method: "POST", body: {} }));
  }

  create(input: QuickLinkInput) {
    return this.api.request<{ link: HomeQuickLink }>("/v1/home/quick-links", { method: "POST", body: input });
  }

  update(id: string, input: QuickLinkInput) {
    return this.api.request<{ link: HomeQuickLink }>(`/v1/home/quick-links/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: input,
    });
  }

  remove(id: string) {
    return this.api.request<{ ok: true }>(`/v1/home/quick-links/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

  async reorder(ids: string[]): Promise<QuickLinksPayload> {
    return normalizeQuickLinksPayload(await this.api.request<QuickLinksPayload>("/v1/home/quick-links/order", { method: "PUT", body: { ids } }));
  }
}

function normalizeQuickLinksPayload(payload: Partial<QuickLinksPayload>): QuickLinksPayload {
  return {
    links: arrayFrom<HomeQuickLink>(payload.links),
    can_edit: booleanFrom(payload.can_edit),
  };
}

export const quickLinksClient = new QuickLinksClient();
