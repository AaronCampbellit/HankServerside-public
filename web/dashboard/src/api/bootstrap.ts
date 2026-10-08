import { apiClient, type ApiTransport } from "./client";
import { arrayFrom, booleanFrom } from "./normalize";

export type BootstrapUser = {
  id: string;
  email: string;
  display_name: string;
  password_change_required: boolean;
  created_at: string;
  updated_at: string;
};

export type BootstrapHome = {
  id: string;
  user_id: string;
  name: string;
  created_at: string;
  updated_at: string;
};

export type BootstrapMembership = {
  home_id: string;
  user_id: string;
  role: "admin" | "member" | string;
  created_at: string;
  updated_at: string;
};

export type BootstrapPermissions = {
  is_admin: boolean;
  can_manage_people: boolean;
  can_manage_settings: boolean;
  can_use_homeassistant: boolean;
  can_use_files: boolean;
  can_use_notes: boolean;
  can_use_assistant: boolean;
  can_view_storage: boolean;
  can_manage_apps: boolean;
};

export type BootstrapAgent = {
  agent_id: string;
  name: string;
  status: "online" | "offline" | string;
  last_seen_at?: string | null;
  home_id: string;
  home_name: string;
  capabilities?: string[];
};

export type BootstrapNavigationItem = {
  path: string;
  label: string;
  admin_only?: boolean;
};

export type BootstrapState = {
  user: BootstrapUser;
  session: {
    id: string;
    expires_at: string;
  };
  home: BootstrapHome | null;
  membership: BootstrapMembership | null;
  permissions: BootstrapPermissions;
  agent: BootstrapAgent | null;
  setup_status: {
    first_setup_visible: boolean;
  };
  features: {
    mcp_enabled: boolean;
  };
  server: {
    version: string;
  };
  navigation: BootstrapNavigationItem[];
};

export class BootstrapClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async load(): Promise<BootstrapState> {
    return normalizeBootstrapState(await this.api.request<BootstrapState>("/v1/ui/bootstrap"));
  }
}

function normalizeBootstrapState(payload: Partial<BootstrapState>): BootstrapState {
  return {
    user: payload.user
      ? { ...payload.user, display_name: payload.user.display_name || "" }
      : {
          id: "",
          email: "",
          display_name: "",
          password_change_required: false,
          created_at: "",
          updated_at: "",
        },
    session: payload.session || { id: "", expires_at: "" },
    home: payload.home || null,
    membership: payload.membership || null,
    permissions: {
      is_admin: booleanFrom(payload.permissions?.is_admin),
      can_manage_people: booleanFrom(payload.permissions?.can_manage_people),
      can_manage_settings: booleanFrom(payload.permissions?.can_manage_settings),
      can_use_homeassistant: booleanFrom(payload.permissions?.can_use_homeassistant),
      can_use_files: booleanFrom(payload.permissions?.can_use_files),
      can_use_notes: booleanFrom(payload.permissions?.can_use_notes),
      can_use_assistant: booleanFrom(payload.permissions?.can_use_assistant),
      can_view_storage: booleanFrom(payload.permissions?.can_view_storage),
      can_manage_apps: booleanFrom(payload.permissions?.can_manage_apps),
    },
    agent: payload.agent ? { ...payload.agent, capabilities: arrayFrom<string>(payload.agent.capabilities) } : null,
    setup_status: { first_setup_visible: booleanFrom(payload.setup_status?.first_setup_visible) },
    features: { mcp_enabled: booleanFrom(payload.features?.mcp_enabled) },
    server: payload.server || { version: "" },
    navigation: arrayFrom<BootstrapNavigationItem>(payload.navigation),
  };
}

export const bootstrapClient = new BootstrapClient();
