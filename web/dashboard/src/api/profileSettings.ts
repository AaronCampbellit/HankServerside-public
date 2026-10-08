import { apiClient, ApiError, type ApiTransport } from "./client";

export type ProfileSettings = Record<string, unknown>;

export type ProfileSettingsResponse = {
  revision: number;
  settings: ProfileSettings;
};

export class ProfileSettingsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async load(): Promise<ProfileSettingsResponse> {
    try {
      return await this.api.request<ProfileSettingsResponse>("/v1/me/profile");
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return { revision: 0, settings: {} };
      throw error;
    }
  }

  save(expectedRevision: number, settings: ProfileSettings) {
    return this.api.request<ProfileSettingsResponse>("/v1/me/profile", {
      method: "PUT",
      body: { expected_revision: expectedRevision, settings },
    });
  }
}

export function mergeDefaultKanbanBoard(settings: ProfileSettings, boardID: string): ProfileSettings {
  const next = { ...settings };
  if (boardID) next.kanban_default_board_id = boardID;
  else delete next.kanban_default_board_id;
  return next;
}

export const profileSettingsClient = new ProfileSettingsClient();
