import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type OpenAIStatus = {
  linked?: boolean;
  configured?: boolean;
  auth_provider?: string;
  auth_mode?: string;
  chatgpt_plan_type?: string;
  updated_at?: string;
  expires_at?: string;
  scopes?: string;
  scope?: string;
  pending?: {
    state?: string;
    user_code?: string;
    verification_url?: string;
    expires_at?: string;
    poll_after_seconds?: number;
  } | null;
};

export type AssistantStatus = {
  provider?: string;
  chat_model?: string;
  embedding_model?: string;
  vector_store?: string;
  chat_configured?: boolean;
  embedding_configured?: boolean;
  index?: {
    vector_mode?: string;
    chunk_count?: number;
    embedded_chunk_count?: number;
    file_count?: number;
    embedded_file_count?: number;
    conversation_count?: number;
  };
};

export type PromptProfile = {
  key: string;
  label: string;
  prompt?: string;
};

export type ProviderOption = {
  key?: string;
  value?: string;
  label?: string;
  name?: string;
};

export type AssistantSettings = {
  profile_notes_enabled: boolean;
  home_notes_enabled: boolean;
  files_enabled: boolean;
  calendar_enabled: boolean;
  homeassistant_enabled: boolean;
  project_docs_enabled: boolean;
  conversations_enabled: boolean;
  ai_provider: string;
  ollama_base_url: string;
  chat_model: string;
  embedding_model: string;
  planner_enabled: boolean;
  planner_model: string;
  prompt_profile: string;
  system_prompt: string;
};

export type AssistantSettingsPayload = {
  settings?: Partial<AssistantSettings>;
  defaults?: Partial<AssistantSettings> & {
    chat_model_options?: string[];
    embedding_model_options?: string[];
    provider_options?: ProviderOption[];
    prompt_profiles?: PromptProfile[];
    max_context_items?: number;
  };
  sources?: Array<{ key?: string; label: string; enabled: boolean }>;
  tools?: Array<{ label: string; enabled: boolean; status?: string; description?: string; requirements?: string[] }>;
};

export type AssistantModelsPayload = {
  models?: string[];
  source?: string;
  provider?: string;
  default_model?: string;
  current_model?: string;
};

export type MCPStatus = {
  resource_url?: string;
  scopes_supported?: string[];
  connections?: Array<{
    id: string;
    client_id?: string;
    client_name?: string;
    scopes?: string[];
    connected?: boolean;
    created_at?: string;
    last_used_at?: string;
  }>;
  context_sources?: MCPContextSource[];
};

export type MCPContextSource = { id: string; home_id?: string; name: string; file_source_id: string; root_path: string; enabled: boolean; last_tested_at?: string; last_test_error?: string; created_at?: string; updated_at?: string };
export type MCPContextSourceInput = Pick<MCPContextSource, "name" | "file_source_id" | "root_path" | "enabled">;

export type AssistantSettingsView = {
  openAI: OpenAIStatus;
  assistant: AssistantStatus;
  settings: AssistantSettingsPayload;
  models: AssistantModelsPayload;
  mcp: MCPStatus;
};

export type OpenAILinkResponse = {
  auth_mode?: string;
  verification_url?: string;
  user_code?: string;
};

export class AssistantClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async loadSettings(): Promise<AssistantSettingsView> {
    const [openAI, assistant, settings, models, mcp] = await Promise.all([
      this.api.request<OpenAIStatus>("/v1/oauth/openai/status"),
      this.api.request<AssistantStatus>("/v1/home/assistant/status"),
      this.api.request<AssistantSettingsPayload>("/v1/home/assistant/settings"),
      this.api.request<AssistantModelsPayload>("/v1/home/assistant/models"),
      this.api.request<MCPStatus>("/v1/me/mcp").catch((): MCPStatus => ({ connections: [] })),
    ]);
    return {
      openAI,
      assistant,
      settings: {
        ...settings,
        defaults: {
          ...settings.defaults,
          chat_model_options: arrayFrom<string>(settings.defaults?.chat_model_options),
          embedding_model_options: arrayFrom<string>(settings.defaults?.embedding_model_options),
          provider_options: arrayFrom<ProviderOption>(settings.defaults?.provider_options),
          prompt_profiles: arrayFrom<PromptProfile>(settings.defaults?.prompt_profiles),
        },
        sources: arrayFrom(settings.sources),
        tools: arrayFrom(settings.tools),
      },
      models: {
        ...models,
        models: arrayFrom<string>(models.models),
      },
      mcp: {
        ...mcp,
        scopes_supported: arrayFrom<string>(mcp.scopes_supported),
        connections: arrayFrom(mcp.connections),
        context_sources: arrayFrom(mcp.context_sources),
      },
    };
  }

  saveSettings(settings: AssistantSettings) {
    return this.api.request<{ ok?: boolean }>("/v1/home/assistant/settings", {
      method: "PUT",
      body: settings,
    });
  }

  startOpenAILink() {
    return this.api.request<OpenAILinkResponse>("/v1/oauth/openai/start", { method: "POST", body: {} });
  }

  revokeMCPConnection(id: string) {
    return this.api.request<{ ok?: boolean }>(`/v1/me/mcp/connections/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  createMCPContextSource(input: MCPContextSourceInput) { return this.api.request<MCPContextSource>("/v1/me/mcp/context-sources", { method: "POST", body: input }); }
  updateMCPContextSource(id: string, input: MCPContextSourceInput) { return this.api.request<MCPContextSource>(`/v1/me/mcp/context-sources/${encodeURIComponent(id)}`, { method: "PUT", body: input }); }
  testMCPContextSource(id: string) { return this.api.request<{ ok: boolean; source?: MCPContextSource }>(`/v1/me/mcp/context-sources/${encodeURIComponent(id)}/test`, { method: "POST", body: {} }); }
  deleteMCPContextSource(id: string) { return this.api.request<{ ok: boolean }>(`/v1/me/mcp/context-sources/${encodeURIComponent(id)}`, { method: "DELETE" }); }
}

export const assistantClient = new AssistantClient();
