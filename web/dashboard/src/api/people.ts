import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type HomeMember = {
  user_id: string;
  email: string;
  display_name: string;
  role: "admin" | "member" | string;
  created_at: string;
  updated_at: string;
};

export type HomeInvitation = {
  id: string;
  home_id: string;
  email: string;
  role: "member" | string;
  accepted_at?: string | null;
  expires_at?: string | null;
  created_at: string;
};

export type MembersPayload = {
  members: HomeMember[];
};

export type InvitationsPayload = {
  invitations: HomeInvitation[];
};

export type CreatedInvitation = {
  invitation_id: string;
  home_id: string;
  email: string;
  role: string;
  token: string;
  join_url: string;
  expires_at?: string | null;
};

export type PasswordResetInput = {
  temporary_password: string;
  password_change_required: boolean;
};

export class PeopleClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async listMembers(): Promise<MembersPayload> {
    const payload = await this.api.request<Partial<MembersPayload>>("/v1/home/members");
    return { members: arrayFrom<HomeMember>(payload.members) };
  }

  async listInvitations(): Promise<InvitationsPayload> {
    const payload = await this.api.request<Partial<InvitationsPayload>>("/v1/home/members/invitations");
    return { invitations: arrayFrom<HomeInvitation>(payload.invitations) };
  }

  createInvitation(email: string) {
    return this.api.request<CreatedInvitation>("/v1/home/members/invitations", {
      method: "POST",
      body: { email },
    });
  }

  cancelInvitation(id: string) {
    return this.api.request<{ ok: true }>(`/v1/home/members/invitations/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  removeMember(userID: string) {
    return this.api.request<{ ok: true }>(`/v1/home/members/${encodeURIComponent(userID)}`, { method: "DELETE" });
  }

  resetPassword(userID: string, input: PasswordResetInput) {
    return this.api.request<{ ok: boolean }>(`/v1/home/members/${encodeURIComponent(userID)}/password`, {
      method: "PUT",
      body: input,
    });
  }

  updateDisplayName(userID: string, displayName: string) {
    return this.api.request<HomeMember>(`/v1/home/members/${encodeURIComponent(userID)}/display-name`, {
      method: "PUT",
      body: { display_name: displayName },
    });
  }
}

export const peopleClient = new PeopleClient();
