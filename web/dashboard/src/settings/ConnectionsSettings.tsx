import { type FormEvent, useEffect, useState } from "react";
import { bootstrapClient, type BootstrapState } from "../api/bootstrap";
import { connectionsClient, type ServiceProfile } from "../api/connections";
import { useConfirmDialog } from "../ui/primitives";
import {
  cleanSMBID,
  newShareDraft,
  normalizeSMBHost,
  removeSMBShare,
  smbSourceRecords,
  type SMBShare,
  upsertSMBShare,
  validateSMBShare,
} from "./smbShareEditor";

type HostFolder = {
  id: string;
  name: string;
  root: string;
  create: boolean;
  policy?: Record<string, unknown>;
};

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      bootstrap: BootstrapState;
      profiles: ServiceProfile[];
      ha: { baseURL: string; timeoutSeconds: string; token: string; persist: boolean };
      smbConfig: Record<string, unknown>;
      smbShares: SMBShare[];
      smbDraft: SMBShare & { password: string; persist: boolean };
      originalSMBID: string;
      smbBusy: "" | "save" | "test" | "remove";
      folders: HostFolder[];
      message: string;
    };

function parseConfig(profile: ServiceProfile | undefined): Record<string, unknown> {
  if (!profile?.public_config_json) return {};
  try {
    const parsed = JSON.parse(profile.public_config_json);
    return parsed && typeof parsed === "object" ? parsed as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function profileByType(profiles: ServiceProfile[], type: string): ServiceProfile | undefined {
  return profiles.find((profile) => profile.service_type === type);
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value;
  }
  return "";
}

function hostFolders(profile: ServiceProfile | undefined): HostFolder[] {
  const config = parseConfig(profile);
  const folders = Array.isArray(config.folders) ? config.folders : [];
  const parsed = folders.flatMap((entry, index) => {
    if (!entry || typeof entry !== "object") return [];
    const record = entry as Record<string, unknown>;
    const root = firstString(record.root, record.path);
    if (!root) return [];
    const id = cleanSMBID(firstString(record.id, record.source_id, record.name, `local-${index + 1}`)) || `local-${index + 1}`;
    return [{
      id,
      name: firstString(record.name, id),
      root,
      create: false,
      ...(record.policy && typeof record.policy === "object" ? { policy: { ...(record.policy as Record<string, unknown>) } } : {}),
    }];
  });
  if (parsed.length > 0) return parsed;

  const legacyRoot = firstString(config.root);
  return legacyRoot ? [{ id: "local", name: "Primary Hank Agent files", root: legacyRoot, create: false }] : [];
}

function publicConfigForFolders(folders: HostFolder[]): Record<string, unknown> {
  const publicFolders = folders.flatMap((folder, index) => {
    const root = folder.root.trim();
    if (!root) return [];
    const id = cleanSMBID(folder.id || folder.name || `local-${index + 1}`) || `local-${index + 1}`;
    return [{
      id,
      name: folder.name.trim() || id,
      root,
      create: folder.create,
      ...(folder.policy ? { policy: folder.policy } : {}),
    }];
  });
  return { folders: publicFolders };
}

function publicConfigForShares(config: Record<string, unknown>): Record<string, unknown> {
  return {
    active_source_id: config.active_source_id,
    host: config.host,
    share: config.share,
    domain: config.domain,
    username: config.username,
    shares: config.shares,
  };
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Connections could not be loaded.";
}

export function ConnectionsSettings() {
  const [state, setState] = useState<State>({ status: "loading" });
  const dialog = useConfirmDialog();

  async function load(message = "", preferredSMBID = "") {
    try {
      const [bootstrap, payload] = await Promise.all([bootstrapClient.load(), connectionsClient.listProfiles()]);
      const profiles = payload.profiles || [];
      const haConfig = parseConfig(profileByType(profiles, "homeassistant"));
      const smbProfile = profileByType(profiles, "smb");
      const smbConfig = parseConfig(smbProfile);
      const smbShares = smbSourceRecords(smbConfig);
      const selectedShare = smbShares.find((share) => share.id === preferredSMBID) || smbShares[0] || newShareDraft(smbShares);
      setState({
        status: "ready",
        bootstrap,
        profiles,
        ha: {
          baseURL: firstString(haConfig.base_url, haConfig.url),
          timeoutSeconds: String(Number(haConfig.timeout_seconds || 10) || 10),
          token: "",
          persist: true,
        },
        smbConfig,
        smbShares,
        smbDraft: { ...selectedShare, password: "", persist: true },
        originalSMBID: smbShares.some((share) => share.id === selectedShare.id) ? selectedShare.id : "",
        smbBusy: "",
        folders: hostFolders(smbProfile),
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
        <h1 id="route-title">Connections</h1>
        <p className="loading-state">Loading connections...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Connections</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const readyState = state;
  const canManage = readyState.bootstrap.permissions.can_manage_settings;

  function setReady(next: Partial<Extract<State, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  async function saveHA(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const input = {
        public_config: {
          base_url: readyState.ha.baseURL.trim().replace(/\/+$/, ""),
          timeout_seconds: Number.parseInt(readyState.ha.timeoutSeconds, 10) || 10,
        },
        persist: readyState.ha.persist,
        ...(readyState.ha.token.trim() ? { secrets: { token: readyState.ha.token.trim() } } : {}),
      };
      await connectionsClient.saveProfile("homeassistant", input);
      await load("Home Assistant settings saved.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function saveSMB(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validation = validateSMBShare(readyState.smbDraft, readyState.smbShares, readyState.originalSMBID);
    if (validation) {
      setReady({ message: validation });
      return;
    }
    try {
      setReady({ smbBusy: "save", message: "" });
      const host = normalizeSMBHost(readyState.smbDraft.host);
      const shareID = cleanSMBID(readyState.smbDraft.id);
      const share: SMBShare = {
        id: shareID,
        name: readyState.smbDraft.name.trim(),
        host,
        share: readyState.smbDraft.share.trim(),
        domain: readyState.smbDraft.domain.trim(),
        username: readyState.smbDraft.username.trim(),
        password_set: readyState.smbDraft.password_set,
        ...(readyState.smbDraft.policy ? { policy: readyState.smbDraft.policy } : {}),
      };
      const publicConfig = upsertSMBShare(readyState.smbConfig, share, readyState.originalSMBID);
      const input = {
        public_config: publicConfigForShares(publicConfig),
        persist: readyState.smbDraft.persist,
        ...(readyState.smbDraft.password ? { secrets: { shares: [{ id: share.id, password: readyState.smbDraft.password }] } } : {}),
      };
      await connectionsClient.saveProfile("smb", input);
      await load(`${share.name} saved.`, share.id);
    } catch (error) {
      setReady({ message: errorMessage(error), smbBusy: "" });
    }
  }

  function selectSMBShare(share: SMBShare) {
    setReady({ smbDraft: { ...share, password: "", persist: readyState.smbDraft.persist }, originalSMBID: share.id, message: "" });
  }

  function addSMBShare() {
    setReady({ smbDraft: { ...newShareDraft(readyState.smbShares), password: "", persist: readyState.smbDraft.persist }, originalSMBID: "", message: "" });
  }

  async function testSMB() {
    const validation = validateSMBShare(readyState.smbDraft, readyState.smbShares, readyState.originalSMBID);
    if (validation) {
      setReady({ message: validation });
      return;
    }
    const share = readyState.smbDraft;
    try {
      setReady({ smbBusy: "test", message: "" });
      await connectionsClient.testSMB({
        id: cleanSMBID(share.id),
        name: share.name.trim(),
        host: normalizeSMBHost(share.host),
        share: share.share.trim(),
        username: share.username.trim(),
        password: share.password,
        domain: share.domain.trim(),
      });
      setReady({ smbBusy: "", message: `Connection to ${share.name.trim()} succeeded.` });
    } catch (error) {
      setReady({ smbBusy: "", message: errorMessage(error) });
    }
  }

  async function removeSelectedSMB() {
    if (!readyState.originalSMBID) return;
    const label = readyState.smbDraft.name || readyState.originalSMBID;
    const confirmed = await dialog.confirm({
      title: "Remove SMB share",
      message: `Remove ${label}? File Server access through this source will stop.`,
      confirmLabel: "Remove share",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      setReady({ smbBusy: "remove", message: "" });
      const publicConfig = removeSMBShare(readyState.smbConfig, readyState.originalSMBID);
      const remaining = smbSourceRecords(publicConfig);
      await connectionsClient.saveProfile("smb", { public_config: publicConfigForShares(publicConfig), persist: readyState.smbDraft.persist });
      await load(`${label} removed.`, remaining[0]?.id || "");
    } catch (error) {
      setReady({ smbBusy: "", message: errorMessage(error) });
    }
  }

  function updateFolder(index: number, next: Partial<HostFolder>) {
    setReady({ folders: readyState.folders.map((folder, folderIndex) => folderIndex === index ? { ...folder, ...next } : folder) });
  }

  function addFolder() {
    const index = readyState.folders.length + 1;
    setReady({ folders: [...readyState.folders, { id: `local-${index}`, name: "", root: "", create: false }] });
  }

  function removeFolder(index: number) {
    setReady({ folders: readyState.folders.filter((_, folderIndex) => folderIndex !== index) });
  }

  async function saveFolders(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await connectionsClient.saveProfile("smb", {
        public_config: publicConfigForFolders(readyState.folders),
        persist: readyState.smbDraft.persist,
      });
      await load("Host folders saved.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Connections</h1>
          <p className="meta-line">Credentials are stored only on the primary Hank Agent.</p>
        </div>
        <span className="status-pill">{canManage ? "Admin" : "View Only"}</span>
      </header>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}

      <section className="settings-panel" aria-label="Connection status">
        <h2>Connection Status</h2>
        <div className="dashboard-grid">
          {readyState.profiles.length > 0 ? readyState.profiles.map((profile) => (
            <article className="dashboard-tile" key={profile.service_type}>
              <span>{profile.service_type}</span>
              <strong>{profile.status || "unknown"}</strong>
              <small>Version {profile.applied_version}</small>
            </article>
          )) : (
            <p className="empty-state">No connections saved for this home yet.</p>
          )}
        </div>
      </section>

      <section className="settings-panel" aria-label="Home Assistant">
        <div className="panel-heading">
          <h2>Home Assistant</h2>
          <span className="status-pill status-online">Connected</span>
        </div>
        <form className="quick-link-form" onSubmit={saveHA}>
          <label>
            <span>Base URL</span>
            <input
              aria-label="Home Assistant address"
              disabled={!canManage}
              onChange={(event) => setReady({ ha: { ...readyState.ha, baseURL: event.target.value } })}
              type="url"
              value={readyState.ha.baseURL}
            />
          </label>
          <label>
            <span>Request timeout</span>
            <input
              disabled={!canManage}
              min={1}
              onChange={(event) => setReady({ ha: { ...readyState.ha, timeoutSeconds: event.target.value } })}
              type="number"
              value={readyState.ha.timeoutSeconds}
            />
          </label>
          <label>
            <span>Long-lived access token</span>
            <input
              aria-label="Home Assistant token"
              disabled={!canManage}
              onChange={(event) => setReady({ ha: { ...readyState.ha, token: event.target.value } })}
              type="password"
              value={readyState.ha.token}
            />
          </label>
          <label className="checkbox-field">
            <input
              checked={readyState.ha.persist}
              disabled={!canManage}
              onChange={(event) => setReady({ ha: { ...readyState.ha, persist: event.target.checked } })}
              type="checkbox"
            />
            <span>Save on primary Hank Agent</span>
          </label>
          <div className="button-row">
            <button disabled={!canManage} type="submit">Save Home Assistant</button>
            <button className="secondary" disabled={!canManage} type="button">Test</button>
          </div>
        </form>
      </section>

      <section className="settings-panel" aria-label="File Server">
        <div className="panel-heading">
          <div>
            <h2>SMB Shares</h2>
            <p className="meta-line">Select a share to edit, test, or remove it.</p>
          </div>
          <button aria-label="Add SMB share" className="secondary" disabled={!canManage || Boolean(readyState.smbBusy)} onClick={addSMBShare} type="button">Add share</button>
        </div>
        {readyState.smbShares.length ? (
          <div aria-label="Configured SMB shares" className="quick-links-list settings-list smb-share-list" role="list">
            {readyState.smbShares.map((share) => (
              <div key={share.id} role="listitem">
                <button
                  aria-label={`Edit ${share.name}`}
                  className={`smb-share-row${readyState.originalSMBID === share.id ? " selected" : ""}`}
                  disabled={Boolean(readyState.smbBusy)}
                  onClick={() => selectSMBShare(share)}
                  type="button"
                >
                  <span className="quick-link-copy">
                    <strong>{share.name}</strong>
                    <span>{share.host}/{share.share}</span>
                    <small>{share.password_set ? "Password saved" : "No saved password"}</small>
                  </span>
                  <span className="status-pill">{readyState.originalSMBID === share.id ? "Editing" : "Select"}</span>
                </button>
              </div>
            ))}
          </div>
        ) : <p className="empty-state">No SMB shares configured.</p>}
        <form className="quick-link-form" onSubmit={saveSMB}>
          <label>
            <span>Share label</span>
            <input
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, name: event.target.value } })}
              type="text"
              value={readyState.smbDraft.name}
            />
          </label>
          <label>
            <span>Server address</span>
            <input
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, host: event.target.value } })}
              type="text"
              value={readyState.smbDraft.host}
            />
          </label>
          <label>
            <span>Share name</span>
            <input
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, share: event.target.value } })}
              type="text"
              value={readyState.smbDraft.share}
            />
          </label>
          <label>
            <span>Domain</span>
            <input
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, domain: event.target.value } })}
              type="text"
              value={readyState.smbDraft.domain}
            />
          </label>
          <label>
            <span>Username</span>
            <input
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, username: event.target.value } })}
              type="text"
              value={readyState.smbDraft.username}
            />
          </label>
          <label>
            <span>SMB password</span>
            <input
              aria-label="SMB password"
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, password: event.target.value } })}
              placeholder={readyState.smbDraft.password_set ? "Leave blank to keep saved password" : "Optional for guest shares"}
              type="password"
              value={readyState.smbDraft.password}
            />
          </label>
          <label className="checkbox-field">
            <input
              checked={readyState.smbDraft.persist}
              disabled={!canManage || Boolean(readyState.smbBusy)}
              onChange={(event) => setReady({ smbDraft: { ...readyState.smbDraft, persist: event.target.checked } })}
              type="checkbox"
            />
            <span>Save on primary Hank Agent</span>
          </label>
          <div className="button-row">
            <button disabled={!canManage || Boolean(readyState.smbBusy)} type="submit">{readyState.smbBusy === "save" ? "Saving..." : "Save File Server"}</button>
            <button className="secondary" disabled={!canManage || Boolean(readyState.smbBusy)} onClick={() => void testSMB()} type="button">{readyState.smbBusy === "test" ? "Testing..." : "Test Connection"}</button>
            <button className="danger-link" disabled={!canManage || !readyState.originalSMBID || Boolean(readyState.smbBusy)} onClick={() => void removeSelectedSMB()} type="button">{readyState.smbBusy === "remove" ? "Removing..." : "Remove SMB Share"}</button>
          </div>
        </form>
      </section>

      <section className="settings-panel" aria-label="Host Folders">
        <div className="panel-heading">
          <div>
            <h2>Host Folders</h2>
            <p className="meta-line">Expose selected directories from the primary Hank Agent as File Server sources.</p>
          </div>
          <button className="secondary" disabled={!canManage} onClick={addFolder} type="button">Add folder</button>
        </div>
        <form className="quick-link-form" onSubmit={saveFolders}>
          {readyState.folders.length ? readyState.folders.map((folder, index) => (
            <fieldset className="quick-link-form" key={`${folder.id}-${index}`}>
              <label>
                <span>Folder label</span>
                <input
                  aria-label="Host folder label"
                  disabled={!canManage}
                  onChange={(event) => updateFolder(index, { name: event.target.value })}
                  type="text"
                  value={folder.name}
                />
              </label>
              <label>
                <span>Absolute path</span>
                <input
                  aria-label="Host folder path"
                  disabled={!canManage}
                  onChange={(event) => updateFolder(index, { root: event.target.value })}
                  placeholder="/srv/media"
                  type="text"
                  value={folder.root}
                />
              </label>
              <label className="checkbox-field">
                <input
                  aria-label="Create folder if it does not exist"
                  checked={folder.create}
                  disabled={!canManage}
                  onChange={(event) => updateFolder(index, { create: event.target.checked })}
                  type="checkbox"
                />
                <span>Create folder if it does not exist</span>
              </label>
              <button
                className="secondary"
                disabled={!canManage}
                onClick={() => removeFolder(index)}
                type="button"
              >Remove {folder.name || `folder ${index + 1}`}</button>
            </fieldset>
          )) : <p className="empty-state">No host folders configured.</p>}
          <button disabled={!canManage} type="submit">Save Host Folders</button>
        </form>
      </section>
    </section>
  );
}
