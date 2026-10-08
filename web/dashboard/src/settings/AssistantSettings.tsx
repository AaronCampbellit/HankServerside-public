import { type FormEvent, useEffect, useState } from "react";
import {
  assistantClient,
  type AssistantSettings as AssistantSettingsForm,
  type AssistantSettingsPayload,
  type AssistantSettingsView,
  type MCPContextSourceInput,
  type PromptProfile,
  type ProviderOption,
} from "../api/assistant";
import { bootstrapClient, type BootstrapState } from "../api/bootstrap";
import { apiClient } from "../api/client";
import { connectionsClient } from "../api/connections";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; bootstrap: BootstrapState; view: AssistantSettingsView; form: AssistantSettingsForm; message: string };

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "AI settings could not be loaded.";
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value;
  }
  return "";
}

function boolSetting(settings: Partial<AssistantSettingsForm>, key: keyof AssistantSettingsForm, fallback = true): boolean {
  const value = settings[key];
  return typeof value === "boolean" ? value : fallback;
}

function normalizeForm(payload: AssistantSettingsPayload): AssistantSettingsForm {
  const settings = payload.settings || {};
  const defaults = payload.defaults || {};
  return {
    profile_notes_enabled: boolSetting(settings, "profile_notes_enabled"),
    home_notes_enabled: boolSetting(settings, "home_notes_enabled"),
    files_enabled: boolSetting(settings, "files_enabled"),
    calendar_enabled: boolSetting(settings, "calendar_enabled"),
    homeassistant_enabled: boolSetting(settings, "homeassistant_enabled"),
    project_docs_enabled: boolSetting(settings, "project_docs_enabled"),
    conversations_enabled: boolSetting(settings, "conversations_enabled"),
    ai_provider: firstString(settings.ai_provider, defaults.ai_provider),
    ollama_base_url: firstString(settings.ollama_base_url, defaults.ollama_base_url),
    chat_model: firstString(settings.chat_model, defaults.chat_model),
    embedding_model: firstString(settings.embedding_model, defaults.embedding_model),
    planner_enabled: boolSetting(settings, "planner_enabled"),
    planner_model: firstString(settings.planner_model, defaults.planner_model),
    prompt_profile: firstString(settings.prompt_profile, defaults.prompt_profile, "chatgpt"),
    system_prompt: firstString(settings.system_prompt, defaults.system_prompt),
  };
}

function providerKey(option: ProviderOption): string {
  return String(option.key ?? option.value ?? "");
}

function providerLabel(option: ProviderOption): string {
  const key = providerKey(option);
  return String(option.label ?? option.name ?? (key || "Configured default"));
}

function uniqueValues(values: Array<string | undefined>): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const raw of values) {
    const value = String(raw || "").trim();
    if (!value || seen.has(value)) continue;
    seen.add(value);
    result.push(value);
  }
  return result;
}

function accountLabel(status: AssistantSettingsView["openAI"]): string {
  return status.auth_provider === "chatgpt_codex" || status.auth_mode === "device_code" ? "ChatGPT / Codex" : "OpenAI";
}

function formatDate(value?: string): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

function promptForProfile(profiles: PromptProfile[] | undefined, key: string): string {
  return profiles?.find((profile) => profile.key === key)?.prompt || "";
}

type FileSourceOption = { id: string; label: string };
function mcpFileSources(publicConfigJSON?: string): FileSourceOption[] {
  try {
    const config = JSON.parse(publicConfigJSON || "{}") as Record<string, unknown>;
    const rows = [config.shares, config.sources, config.file_sources].flatMap((value) => Array.isArray(value) ? value : []);
    return rows.flatMap((value) => {
      if (!value || typeof value !== "object") return [];
      const row = value as Record<string, unknown>;
      const id = firstString(row.id, row.source_id, row.key, row.share, row.name);
      return id ? [{ id, label: firstString(row.label, row.name, row.share, id) }] : [];
    });
  } catch { return []; }
}

const emptyContextSource: MCPContextSourceInput = { name: "", file_source_id: "", root_path: "", enabled: true };

export function AssistantSettings() {
  const [state, setState] = useState<State>({ status: "loading" });
  const [contextDraft, setContextDraft] = useState<MCPContextSourceInput>(emptyContextSource);
  const [editingContextID, setEditingContextID] = useState("");
  const [fileSources, setFileSources] = useState<FileSourceOption[]>([]);

  async function load(message = "") {
    try {
      const [bootstrap, view, profiles] = await Promise.all([bootstrapClient.load(), assistantClient.loadSettings(), connectionsClient.listProfiles().catch(() => ({ profiles: [] }))]);
      const smb = profiles.profiles.find((profile) => profile.service_type === "smb");
      setFileSources(mcpFileSources(smb?.public_config_json));
      setState({ status: "ready", bootstrap, view, form: normalizeForm(view.settings), message });
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => {
    void load();
  }, []);

  if (state.status === "loading") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">AI & MCP</h1>
        <p className="loading-state">Loading AI settings...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">AI & MCP</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const readyState = state;
  const canManage = readyState.bootstrap.permissions.can_manage_settings;
  const defaults = readyState.view.settings.defaults || {};
  const profiles = defaults.prompt_profiles || [];
  const providerOptions = defaults.provider_options || [
    { key: "", label: "Configured default" },
    { key: "auto", label: "Auto" },
    { key: "ollama", label: "Local Ollama" },
    { key: "chatgpt_codex", label: "Linked ChatGPT/Codex" },
    { key: "openai", label: "OpenAI API key" },
  ];
  const modelOptions = uniqueValues([
    ...(readyState.view.models.models || []),
    ...(defaults.chat_model_options || []),
    readyState.form.chat_model,
    readyState.view.models.default_model,
    readyState.view.assistant.chat_model,
  ]);
  const embeddingOptions = uniqueValues([
    ...(defaults.embedding_model_options || []),
    readyState.form.embedding_model,
    readyState.view.assistant.embedding_model,
  ]);
  const assistantIndex = readyState.view.assistant.index || {};
  const openAI = readyState.view.openAI;
  const label = accountLabel(openAI);
  const enabledSources = readyState.view.settings.sources?.filter((source) => source.enabled) || [];
  const mcpConnections = readyState.view.mcp.connections || [];
  const mcpContextSources = readyState.view.mcp.context_sources || [];

  function setForm(next: Partial<AssistantSettingsForm>) {
    setState((current) => current.status === "ready" ? { ...current, form: { ...current.form, ...next } } : current);
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await assistantClient.saveSettings(readyState.form);
      await load("HankAI settings saved.");
    } catch (error) {
      setState((current) => current.status === "ready" ? { ...current, message: errorMessage(error) } : current);
    }
  }

  async function startLink() {
    try {
      const response = await assistantClient.startOpenAILink();
      if (response.verification_url) {
        window.open(response.verification_url, "_blank", "noopener");
      }
      await load(response.user_code ? `Enter code ${response.user_code} to finish linking.` : "OpenAI link started.");
    } catch (error) {
      setState((current) => current.status === "ready" ? { ...current, message: errorMessage(error) } : current);
    }
  }

  async function revokeMCPConnection(id: string) {
    try {
      await assistantClient.revokeMCPConnection(id);
      await load("MCP connection disconnected.");
    } catch (error) {
      setState((current) => current.status === "ready" ? { ...current, message: errorMessage(error) } : current);
    }
  }

  async function saveContextSource(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      if (editingContextID) await assistantClient.updateMCPContextSource(editingContextID, contextDraft);
      else await assistantClient.createMCPContextSource(contextDraft);
      setContextDraft(emptyContextSource); setEditingContextID(""); await load("MCP context source saved.");
    } catch (error) { setState((current) => current.status === "ready" ? { ...current, message: errorMessage(error) } : current); }
  }

  async function testContextSource(id: string) { try { await assistantClient.testMCPContextSource(id); await load("Live context source test passed."); } catch (error) { await load(errorMessage(error)); } }
  async function toggleContextSource(id: string) { const source = mcpContextSources.find((item) => item.id === id); if (!source) return; await assistantClient.updateMCPContextSource(id, { name: source.name, file_source_id: source.file_source_id, root_path: source.root_path, enabled: !source.enabled }); await load(source.enabled ? "MCP context source disabled." : "MCP context source enabled."); }
  async function removeContextSource(id: string) { await assistantClient.deleteMCPContextSource(id); await load("MCP context source removed."); }

  function resetPrompt() {
    const profile = readyState.form.prompt_profile === "local" ? "local" : "chatgpt";
    setForm({
      prompt_profile: profile,
      system_prompt: promptForProfile(profiles, profile) || defaults.system_prompt || "",
    });
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">AI & MCP</h1>
          <p className="meta-line">OpenAI account, MCP connector, and the HankAI harness.</p>
        </div>
        <span className="status-pill">{canManage ? "Admin" : "View Only"}</span>
      </header>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}

      <section className="settings-panel" aria-label="OpenAI account">
        <h2>OpenAI Account</h2>
        <div className="dashboard-grid">
          <article className="dashboard-tile">
            <span>Account</span>
            <strong>{openAI.linked ? `${label} linked` : `${label} not linked`}</strong>
            <small>{openAI.chatgpt_plan_type || "Plan not reported"}</small>
          </article>
          <article className="dashboard-tile">
            <span>Server setup</span>
            <strong>{openAI.configured ? "Ready" : "Needs setup"}</strong>
            <small>{openAI.scopes || openAI.scope || "Scopes not reported"}</small>
          </article>
          <article className="dashboard-tile">
            <span>Current provider</span>
            <strong>{readyState.view.assistant.provider || "local"}</strong>
            <small>{readyState.view.assistant.chat_model || "local fallback"}</small>
          </article>
          <article className="dashboard-tile">
            <span>Vector mode</span>
            <strong>{assistantIndex.vector_mode || "unavailable"}</strong>
            <small>{assistantIndex.chunk_count || 0} chunks</small>
          </article>
        </div>
        <div className="button-row">
          <button
            aria-label={openAI.linked ? `Relink ${label.replace(" / ", "/")}` : `Link ${label.replace(" / ", "/")}`}
            disabled={!openAI.configured}
            onClick={startLink}
            type="button"
          >
            {openAI.linked ? `Relink ${label}` : `Link ${label}`}
          </button>
        </div>
        <p className="meta-line">Last linked {formatDate(openAI.updated_at)}. Expires {formatDate(openAI.expires_at)}.</p>
      </section>

      <section className="settings-panel" aria-label="HankAI settings">
        <h2>HankAI sources</h2>
        <form className="quick-link-form" onSubmit={save}>
          <fieldset className="checkbox-group">
            <legend>Context Sources</legend>
            <label className="checkbox-field">
              <input checked={readyState.form.profile_notes_enabled} disabled={!canManage} onChange={(event) => setForm({ profile_notes_enabled: event.target.checked })} type="checkbox" />
              <span>Personal notes</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.home_notes_enabled} disabled={!canManage} onChange={(event) => setForm({ home_notes_enabled: event.target.checked })} type="checkbox" />
              <span>Shared notes</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.files_enabled} disabled={!canManage} onChange={(event) => setForm({ files_enabled: event.target.checked })} type="checkbox" />
              <span>Files</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.calendar_enabled} disabled={!canManage} onChange={(event) => setForm({ calendar_enabled: event.target.checked })} type="checkbox" />
              <span>Calendar</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.homeassistant_enabled} disabled={!canManage} onChange={(event) => setForm({ homeassistant_enabled: event.target.checked })} type="checkbox" />
              <span>Home Assistant</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.project_docs_enabled} disabled={!canManage} onChange={(event) => setForm({ project_docs_enabled: event.target.checked })} type="checkbox" />
              <span>Project docs</span>
            </label>
            <label className="checkbox-field">
              <input checked={readyState.form.conversations_enabled} disabled={!canManage} onChange={(event) => setForm({ conversations_enabled: event.target.checked })} type="checkbox" />
              <span>Past conversations</span>
            </label>
          </fieldset>

          <div className="form-section-title">
            <h3>Models & prompt</h3>
            <p className="meta-line">Choose the provider, models, planner, and system prompt Hank should use.</p>
          </div>
          <label>
            <span>AI provider</span>
            <select disabled={!canManage} onChange={(event) => setForm({ ai_provider: event.target.value })} value={readyState.form.ai_provider}>
              {providerOptions.map((option) => (
                <option key={providerKey(option)} value={providerKey(option)}>{providerLabel(option)}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Ollama URL</span>
            <input disabled={!canManage} onChange={(event) => setForm({ ollama_base_url: event.target.value })} type="url" value={readyState.form.ollama_base_url} />
          </label>
          <label>
            <span>Chat model</span>
            <select disabled={!canManage} onChange={(event) => setForm({ chat_model: event.target.value })} value={readyState.form.chat_model}>
              {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}
            </select>
          </label>
          <label>
            <span>Embedding model</span>
            <select disabled={!canManage} onChange={(event) => setForm({ embedding_model: event.target.value })} value={readyState.form.embedding_model}>
              {embeddingOptions.map((model) => <option key={model} value={model}>{model}</option>)}
            </select>
          </label>
          <label className="checkbox-field">
            <input checked={readyState.form.planner_enabled} disabled={!canManage} onChange={(event) => setForm({ planner_enabled: event.target.checked })} type="checkbox" />
            <span>Local planner</span>
          </label>
          <label>
            <span>Planner model</span>
            <select disabled={!canManage} onChange={(event) => setForm({ planner_model: event.target.value })} value={readyState.form.planner_model}>
              <option value="">Reuse chat model</option>
              {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}
            </select>
          </label>
          <label>
            <span>Prompt profile</span>
            <select
              disabled={!canManage}
              onChange={(event) => {
                const prompt = promptForProfile(profiles, event.target.value);
                setForm({ prompt_profile: event.target.value, ...(prompt ? { system_prompt: prompt } : {}) });
              }}
              value={readyState.form.prompt_profile}
            >
              {profiles.map((profile) => <option key={profile.key} value={profile.key}>{profile.label}</option>)}
            </select>
          </label>
          <label>
            <span>System prompt</span>
            <textarea disabled={!canManage} onChange={(event) => setForm({ system_prompt: event.target.value })} value={readyState.form.system_prompt} />
          </label>
          <div className="button-row">
            <button aria-label="Save HankAI Settings" disabled={!canManage} type="submit">Save HankAI settings</button>
            <button aria-label="Reset Prompt" disabled={!canManage} onClick={resetPrompt} type="button">Reset prompt</button>
          </div>
        </form>
        <p className="meta-line">Enabled sources: {enabledSources.length ? enabledSources.map((source) => source.label).join(", ") : "None"}</p>
      </section>

      <section className="settings-panel" aria-label="MCP Connector">
        <div className="panel-heading">
          <h2>Hank MCP connector</h2>
          <span className="status-pill">{readyState.view.mcp.resource_url ? "On" : "Off"}</span>
        </div>
        <button type="button" disabled={!canManage} onClick={async () => {
          try { await apiClient.request("/v1/me/mcp", { method: "PATCH", body: { enabled: !readyState.view.mcp.resource_url } }); await load("MCP connector setting saved."); } catch (error) { await load(errorMessage(error)); }
        }}>{readyState.view.mcp.resource_url ? "Disable MCP connector" : "Enable MCP connector"}</button>
        <p><a href="/dashboard/agents">Manage account device access</a></p>
        {readyState.view.mcp.resource_url ? (
          <div className="kv-list">
            <div><strong>Connector URL</strong><span>{readyState.view.mcp.resource_url}</span></div>
            <div><strong>Scopes</strong><span>{(readyState.view.mcp.scopes_supported || []).join(", ")}</span></div>
          </div>
        ) : <p className="empty-state">The MCP connector is not enabled on this server.</p>}
        {readyState.view.mcp.resource_url ? (
          <button
            className="secondary"
            type="button"
            onClick={() => void navigator.clipboard?.writeText(readyState.view.mcp.resource_url || "")}
          >
            Copy connector URL
          </button>
        ) : null}
        {readyState.view.mcp.resource_url ? (
          <p className="meta-line">Interactive Kanban is included whenever Hank MCP is on.</p>
        ) : null}
        <h3>Apps connected to your account</h3>
        <p className="meta-line">Device access is shared across your account’s MCP apps. Approve it once on the Agents page. Disconnecting an app stops that app from using Hank; revoke account device access to stop access from every app.</p>
        <div className="card-list">
          {mcpConnections.length ? mcpConnections.map((connection) => (
            <article className="dashboard-tile" key={connection.id}>
              <span>{connection.connected === false ? "Disconnected" : "Connected"}</span>
              <strong>{connection.client_name || connection.client_id || "Connected app"}</strong>
              <small>{(connection.scopes || []).join(", ") || "No scopes"}</small>
              <button onClick={() => revokeMCPConnection(connection.id)} type="button">
                Disconnect {connection.client_name || connection.client_id || "app"}
              </button>
            </article>
          )) : <p className="empty-state">No AI apps are connected yet.</p>}
        </div>
        <div className="panel-heading mcp-context-heading"><h3>MCP Context Sources</h3><span className="status-pill">Live agent reads</span></div>
        <p className="meta-line">Choose read-only project folders from an existing File Server share. Sources are available only while the primary Hank Agent and share are online.</p>
        <form className="settings-form mcp-context-form" onSubmit={saveContextSource}>
          <label><span>Project name</span><input aria-label="Project name" disabled={!canManage} value={contextDraft.name} onChange={(event) => setContextDraft({ ...contextDraft, name: event.target.value })} /></label>
          <label><span>File Server share</span><select aria-label="File Server share" disabled={!canManage || !fileSources.length} value={contextDraft.file_source_id} onChange={(event) => setContextDraft({ ...contextDraft, file_source_id: event.target.value })}><option value="">Select a share</option>{fileSources.map((source) => <option key={source.id} value={source.id}>{source.label}</option>)}</select></label>
          <label><span>Project folder path</span><input aria-label="Project folder path" disabled={!canManage} placeholder="Projects/MiniHank" value={contextDraft.root_path} onChange={(event) => setContextDraft({ ...contextDraft, root_path: event.target.value })} /></label>
          <label className="checkbox-field"><input checked={contextDraft.enabled} disabled={!canManage} type="checkbox" onChange={(event) => setContextDraft({ ...contextDraft, enabled: event.target.checked })} /><span>Enabled for MCP</span></label>
          <div className="button-row"><button disabled={!canManage || !contextDraft.name || !contextDraft.file_source_id || !contextDraft.root_path} type="submit">{editingContextID ? "Save source" : "Add source"}</button>{editingContextID ? <button className="secondary" type="button" onClick={() => { setEditingContextID(""); setContextDraft(emptyContextSource); }}>Cancel</button> : null}</div>
        </form>
        {!fileSources.length ? <p className="empty-state">Configure a File Server share before adding MCP context.</p> : null}
        <div className="card-list mcp-context-list">
          {mcpContextSources.map((source) => <article className="dashboard-tile" key={source.id}><span>{source.enabled ? "Enabled" : "Disabled"}</span><strong>{source.name}</strong><small>{source.file_source_id}:/{source.root_path}</small><small>{source.last_test_error ? source.last_test_error : source.last_tested_at ? `Tested ${formatDate(source.last_tested_at)}` : "Not tested yet"}</small><div className="button-row"><button className="secondary" disabled={!canManage} type="button" onClick={() => { setEditingContextID(source.id); setContextDraft({ name: source.name, file_source_id: source.file_source_id, root_path: source.root_path, enabled: source.enabled }); }}>Edit</button><button className="secondary" disabled={!canManage} type="button" onClick={() => void testContextSource(source.id)}>Test</button><button className="secondary" disabled={!canManage} type="button" onClick={() => void toggleContextSource(source.id)}>{source.enabled ? "Disable" : "Enable"}</button><button className="danger-link" disabled={!canManage} type="button" onClick={() => void removeContextSource(source.id)}>Remove</button></div></article>)}
          {!mcpContextSources.length ? <p className="empty-state">No MCP context sources configured.</p> : null}
        </div>
      </section>
    </section>
  );
}
