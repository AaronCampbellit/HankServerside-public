import { apiClient, type ApiTransport } from "./client";

export type AcceptedInvitationPayload = {
  home?: {
    id: string;
    name: string;
    created_at?: string;
  };
};

export class InvitationsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  acceptHomeInvitation(token: string) {
    return this.api.request<AcceptedInvitationPayload>("/v1/home/invitations/accept", {
      method: "POST",
      body: { token },
    });
  }
}

export const invitationsClient = new InvitationsClient();
