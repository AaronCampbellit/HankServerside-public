import { apiClient, type ApiTransport } from "./client";

export type AuthUser = {
  email?: string;
  password_change_required?: boolean;
};

export type AuthResponse = {
  user?: AuthUser;
};

export type InvitationPreview = {
  email: string;
  role: string;
};

export type MeResponse = {
  user?: AuthUser;
};

export type EntraStartResponse = { authorization_url: string };
export type EntraSettings = { enabled: boolean; tenant_id: string; client_id: string; public_base_url: string; client_secret_set: boolean; managed_by_environment?: boolean; updated_at?: string };

export class AuthClient {
  constructor(private readonly transport: ApiTransport = apiClient) {}

  login(email: string, password: string): Promise<AuthResponse> {
    return this.transport.request<AuthResponse>("/v1/auth/login", {
      method: "POST",
      body: { email, password },
    });
  }

  register(email: string, password: string): Promise<AuthResponse> {
    return this.transport.request<AuthResponse>("/v1/auth/register", {
      method: "POST",
      body: { email, password },
    });
  }

  previewInvitation(token: string): Promise<InvitationPreview> {
    return this.transport.request<InvitationPreview>("/v1/auth/invitations/preview", {
      method: "POST",
      body: { token },
    });
  }

  signupInvitation(token: string, email: string, password: string): Promise<unknown> {
    return this.transport.request<unknown>("/v1/auth/invitations/signup", {
      method: "POST",
      body: { token, email, password },
    });
  }

  changePassword(currentPassword: string, newPassword: string): Promise<unknown> {
    return this.transport.request<unknown>("/v1/auth/change-password", {
      method: "POST",
      body: { current_password: currentPassword, new_password: newPassword },
    });
  }

  logout(): Promise<unknown> {
    return this.transport.request<unknown>("/v1/auth/logout", {
      method: "POST",
    });
  }

  me(): Promise<MeResponse> {
    return this.transport.request<MeResponse>("/v1/me");
  }

  startEntra(invitationToken = ""): Promise<EntraStartResponse> {
    return this.transport.request<EntraStartResponse>("/v1/auth/entra/start", { method: "POST", body: { invitation_token: invitationToken } });
  }

  startEntraLink(): Promise<EntraStartResponse> {
    return this.transport.request<EntraStartResponse>("/v1/auth/entra/link/start", { method: "POST" });
  }

  entraSettings(): Promise<EntraSettings> {
    return this.transport.request<EntraSettings>("/v1/home/entra-sso");
  }

  saveEntraSettings(value: Omit<EntraSettings, "client_secret_set" | "managed_by_environment" | "updated_at"> & { client_secret: string }): Promise<EntraSettings> {
    return this.transport.request<EntraSettings>("/v1/home/entra-sso", { method: "PUT", body: value });
  }
}

export const authClient = new AuthClient();
