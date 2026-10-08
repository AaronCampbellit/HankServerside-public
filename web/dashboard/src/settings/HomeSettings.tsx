import { type FormEvent, useEffect, useState } from "react";
import {
  homeClient,
  type AgentPayload,
  type AgentToken,
  type AgentTokensPayload,
  type CreatedAgentToken,
  type Home,
} from "../api/home";
import { authClient } from "../api/auth";

function EntraSSOSettingsPanel() {
  const [settings, setSettings] = useState({ enabled: false, tenant_id: "", client_id: "", client_secret: "", public_base_url: "", client_secret_set: false, managed_by_environment: false });
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => { void authClient.entraSettings().then((value) => setSettings((current) => ({ ...current, ...value, client_secret: "" }))).catch((error) => setMessage(errorMessage(error))); }, []);
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setMessage("");
    try {
      const value = await authClient.saveEntraSettings(settings);
      setSettings((current) => ({ ...current, ...value, client_secret: "" }));
      setMessage(value.enabled ? "Microsoft Entra SSO is enabled." : "Microsoft Entra SSO is disabled.");
    } catch (error) { setMessage(errorMessage(error)); } finally { setBusy(false); }
  }
  return <section className="settings-panel" aria-label="Microsoft Entra sign-in">
    <h2>Microsoft Entra sign-in</h2>
    <p className="meta-line">Configure Hank’s single-tenant Microsoft Entra web app. The client secret is encrypted on the server and is never shown again.</p>
    {settings.managed_by_environment ? <p className="notice-state">Entra is currently supplied by environment configuration. Saving here moves this setting into Hank’s encrypted database.</p> : null}
    {message ? <p role="alert" className="notice-state">{message}</p> : null}
    <form className="quick-link-form" onSubmit={(event) => void save(event)}>
      <label><span>Tenant ID</span><input value={settings.tenant_id} onChange={(event) => setSettings({ ...settings, tenant_id: event.target.value })} /></label>
      <label><span>Application (client) ID</span><input value={settings.client_id} onChange={(event) => setSettings({ ...settings, client_id: event.target.value })} /></label>
      <label><span>Client secret {settings.client_secret_set ? "(set)" : ""}</span><input autoComplete="off" type="password" placeholder={settings.client_secret_set ? "Leave blank to keep current secret" : "Required when enabling"} value={settings.client_secret} onChange={(event) => setSettings({ ...settings, client_secret: event.target.value })} /></label>
      <label><span>Public Hank URL</span><input type="url" placeholder="https://hank.example.com" value={settings.public_base_url} onChange={(event) => setSettings({ ...settings, public_base_url: event.target.value })} /></label>
      <label><span>Enable Microsoft Entra SSO</span><input type="checkbox" checked={settings.enabled} onChange={(event) => setSettings({ ...settings, enabled: event.target.checked })} /></label>
      <div className="form-actions"><button disabled={busy} type="submit">{busy ? "Saving…" : "Save Entra settings"}</button><button disabled={busy || !settings.enabled} className="secondary" type="button" onClick={() => void authClient.startEntraLink().then(({ authorization_url }) => window.location.assign(authorization_url)).catch((error) => setMessage(errorMessage(error)))}>Connect Microsoft account</button></div>
    </form>
  </section>;
}

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      home: Home;
      homeName: string;
      enrollmentPolicy: "admins_only" | "all_users";
      agentPayload: AgentPayload;
      tokens: AgentToken[];
      tokenForm: { agentID: string; name: string; expiresInSeconds: string };
      createdToken: CreatedAgentToken | null;
      message: string;
    };

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Home settings could not be loaded.";
}

function agentIDFromHomeName(homeName: string): string {
  const slug = homeName.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  return slug || "home-agent";
}

function agentNameFromHomeName(homeName: string): string {
  const name = homeName.trim();
  return name ? `${name} Agent` : "Primary Hank Agent";
}

function quotedEnv(value: string | undefined): string {
  return `"${String(value || "").replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

function agentEnvFile(token: CreatedAgentToken, home: Home): string {
  return [
    "HANK_AGENT_CLOUD_URL=ws://cloud:8080/ws/agent",
    `HANK_AGENT_ID=${quotedEnv(token.agent_id || agentIDFromHomeName(home.name))}`,
    `HANK_AGENT_TOKEN=${quotedEnv(token.token)}`,
    `HANK_AGENT_HOME_NAME=${quotedEnv(home.name || "Home")}`,
    "HANK_AGENT_CONFIG_PATH=/app/.env.agent",
    "",
    "HANK_AGENT_FILES_ROOT=/srv/hank/files",
    "HANK_AGENT_SHELL_ENABLED=false",
    "HANK_AGENT_NOTES_ROOT=/srv/hank/notes",
  ].join("\n");
}

function formatDate(value: string | null | undefined): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

export function HomeSettings() {
  const [state, setState] = useState<State>({ status: "loading" });

  async function load(message = "", createdToken: CreatedAgentToken | null = null) {
    try {
      const [home, agentPayload, tokenPayload] = await Promise.all([
        homeClient.getHome(),
        homeClient.getAgent(),
        homeClient.listAgentTokens(),
      ]);
      setState({
        status: "ready",
        home,
        homeName: home.name,
        enrollmentPolicy: home.agent_enrollment_policy === "all_users" ? "all_users" : "admins_only",
        agentPayload,
        tokens: tokenPayload.tokens || [],
        tokenForm: {
          agentID: agentIDFromHomeName(home.name),
          name: agentNameFromHomeName(home.name),
          expiresInSeconds: "",
        },
        createdToken,
        message,
      });
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
        <h1 id="route-title">Home & Agent Setup</h1>
        <p className="loading-state">Loading home settings...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Home & Agent Setup</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  function setReady(next: Partial<Extract<State, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  const readyState = state;

  async function saveHome(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const home = await homeClient.renameHome(readyState.homeName.trim());
      setReady({
        home,
        homeName: home.name,
        tokenForm: {
          ...readyState.tokenForm,
          agentID: agentIDFromHomeName(home.name),
          name: agentNameFromHomeName(home.name),
        },
        message: "Home updated.",
      });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function restartAgent() {
    try {
      const response = await homeClient.restartAgent();
      setReady({ message: response.message || "Agent restart requested." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function saveEnrollmentPolicy(policy: "admins_only" | "all_users") {
    try { const result = await homeClient.setEnrollmentPolicy(policy); setReady({ enrollmentPolicy: result.policy, message: "Mac enrollment policy updated." }); }
    catch (error) { setReady({ message: errorMessage(error) }); }
  }

  async function createSetupFile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const token = await homeClient.createAgentToken({
        agent_id: readyState.tokenForm.agentID.trim() || agentIDFromHomeName(readyState.home.name),
        name: readyState.tokenForm.name.trim() || agentNameFromHomeName(readyState.home.name),
        agent_type: "primary",
        expires_in_seconds: Number.parseInt(readyState.tokenForm.expiresInSeconds || "0", 10) || 0,
      });
      const tokenPayload: AgentTokensPayload = await homeClient.listAgentTokens();
      setReady({ tokens: tokenPayload.tokens || [], createdToken: token, message: "Setup file created." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function revokeToken(token: AgentToken) {
    try {
      await homeClient.revokeAgentToken(token.id);
      const tokenPayload = await homeClient.listAgentTokens();
      setReady({ tokens: tokenPayload.tokens || [], message: "Setup file disabled." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  const agent = readyState.agentPayload.agent;
  const canRestart = readyState.agentPayload.can_restart && agent?.status === "online";

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Home & Agent Setup</h1>
          <p className="meta-line">Home name, primary Hank Agent setup, and enrollment files.</p>
        </div>
      </header>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}

      <EntraSSOSettingsPanel />

      <section className="settings-panel" aria-label="Home name">
        <h2>Home Name</h2>
        <form className="inline-form" onSubmit={saveHome}>
          <label>
            <span>Home name</span>
            <input
              onChange={(event) => setReady({ homeName: event.target.value })}
              type="text"
              value={readyState.homeName}
            />
          </label>
          <label>
            <span>Time zone</span>
            <input readOnly type="text" value={Intl.DateTimeFormat().resolvedOptions().timeZone || "America/Chicago"} />
          </label>
          <button type="submit">Save home name</button>
          <button className="secondary" type="button" onClick={() => setReady({ homeName: readyState.home.name, message: "" })}>
            Discard
          </button>
        </form>
      </section>

      <section className="settings-panel" aria-label="Primary Hank Agent">
        <div className="panel-heading">
          <h2>Primary Hank Agent</h2>
          <span className={`status-pill status-${agent?.status || "offline"}`}>{agent?.status || "offline"}</span>
        </div>
        {agent ? (
          <div className="dashboard-grid">
            <article className="dashboard-tile">
              <span>Name</span>
              <strong>{agent.name}</strong>
            </article>
            <article className="dashboard-tile">
              <span>Agent ID</span>
              <strong>{agent.agent_id}</strong>
            </article>
            <article className="dashboard-tile">
              <span>Last online</span>
              <strong>{formatDate(agent.last_seen_at)}</strong>
              <small>Last heartbeat</small>
            </article>
            <article className="dashboard-tile">
              <span>Agent version</span>
              <strong>{(agent as { version?: string }).version || "v2.4.1"}</strong>
              <small>Agent runtime</small>
            </article>
          </div>
        ) : (
          <p className="empty-state">The primary Hank Agent has not been set up yet.</p>
        )}
        <div className="form-actions">
          <button disabled={!canRestart} onClick={() => void restartAgent()} type="button">
            Restart agent
          </button>
        </div>
      </section>

      <section className="settings-panel" aria-label="Mac enrollment policy">
        <h2>Mac agent enrollment</h2>
        <p className="meta-line">Signing in to HankAgent automatically connects that Mac. Remote Desktop remains administrator-only.</p>
        <label><span>Who can connect a Mac</span><select value={readyState.enrollmentPolicy} onChange={(event) => void saveEnrollmentPolicy(event.target.value as "admins_only" | "all_users")}><option value="admins_only">Administrators only</option><option value="all_users">All home users</option></select></label>
      </section>

      <section className="settings-panel" aria-label="Setup file">
        <h2>Agent setup file</h2>
        <p className="empty-state">Generate a fresh <code>.env.agent</code> for the primary Hank Agent.</p>
        <form className="quick-link-form" onSubmit={createSetupFile}>
          <label>
            <span>Agent ID</span>
            <input
              onChange={(event) => setReady({ tokenForm: { ...readyState.tokenForm, agentID: event.target.value } })}
              required
              type="text"
              value={readyState.tokenForm.agentID}
            />
          </label>
          <label>
            <span>Agent name</span>
            <input
              onChange={(event) => setReady({ tokenForm: { ...readyState.tokenForm, name: event.target.value } })}
              type="text"
              value={readyState.tokenForm.name}
            />
          </label>
          <label>
            <span>Expires after seconds</span>
            <input
              min={0}
              onChange={(event) => setReady({ tokenForm: { ...readyState.tokenForm, expiresInSeconds: event.target.value } })}
              step={1}
              type="number"
              value={readyState.tokenForm.expiresInSeconds}
            />
          </label>
          <button type="submit">Create setup file</button>
        </form>

        {readyState.createdToken ? (
          <pre className="token-output">{agentEnvFile(readyState.createdToken, readyState.home)}</pre>
        ) : null}

        <div aria-label="Setup files" className="quick-links-list settings-list" role="list">
          {readyState.tokens.length > 0 ? readyState.tokens.map((token) => {
            const revoked = Boolean(token.revoked_at);
            return (
              <article className="quick-link-row" key={token.id} role="listitem">
                <div className="quick-link-copy">
                  <strong>{token.agent_id}</strong>
                  <span>Created: {formatDate(token.created_at)}</span>
                  <small>{revoked ? "Disabled" : `Expires: ${formatDate(token.expires_at)}`}</small>
                </div>
                <div className="row-actions">
                  <button className="danger-link" onClick={() => void revokeToken(token)} type="button">
                    {revoked ? `Remove setup file for ${token.agent_id}` : `Disable setup file for ${token.agent_id}`}
                  </button>
                </div>
              </article>
            );
          }) : (
            <p className="empty-state">No setup files have been created for this home yet.</p>
          )}
        </div>
      </section>
    </section>
  );
}
