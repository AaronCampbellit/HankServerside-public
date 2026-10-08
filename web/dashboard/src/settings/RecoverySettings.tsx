import { type FormEvent, useState } from "react";
import {
  recoveryClient,
  type RecoveryApplyResult,
  type RecoveryBundle,
  type RecoveryPreview,
} from "../api/recovery";

type State = {
  bundleFile: File | null;
  bundle: RecoveryBundle | null;
  preview: RecoveryPreview | null;
  applyResult: RecoveryApplyResult | null;
  message: string;
};

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Recovery request failed.";
}

async function readBundle(file: File | null): Promise<RecoveryBundle> {
  if (!file) throw new Error("Choose a recovery bundle first.");
  if (file.size > 1024 * 1024) throw new Error("Recovery bundle is too large.");
  const parsed = JSON.parse(await file.text());
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Recovery bundle must be a JSON object.");
  }
  return parsed as RecoveryBundle;
}

function downloadJSON(filename: string, payload: unknown) {
  const blob = new Blob([JSON.stringify(payload, null, 2), "\n"], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function RecoverySettings() {
  const [state, setState] = useState<State>({
    bundleFile: null,
    bundle: null,
    preview: null,
    applyResult: null,
    message: "",
  });

  const changes = state.applyResult?.changes || state.preview?.changes || [];
  const secrets = state.applyResult?.required_secrets || state.preview?.required_secrets || [];
  const valid = Boolean(state.preview?.valid);

  async function exportBundle() {
    try {
      downloadJSON("hank-recovery-settings.json", await recoveryClient.exportBundle());
      setState((current) => ({ ...current, message: "Recovery bundle exported." }));
    } catch (error) {
      setState((current) => ({ ...current, message: errorMessage(error) }));
    }
  }

  async function previewImport(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const bundle = await readBundle(state.bundleFile);
      const preview = await recoveryClient.previewImport(bundle);
      setState((current) => ({ ...current, bundle, preview, applyResult: null, message: "Recovery bundle previewed." }));
    } catch (error) {
      setState((current) => ({ ...current, bundle: null, preview: null, applyResult: null, message: errorMessage(error) }));
    }
  }

  async function applyImport() {
    if (!state.bundle || !valid) {
      setState((current) => ({ ...current, message: "Preview a valid recovery bundle first." }));
      return;
    }
    try {
      const applyResult = await recoveryClient.applyImport(state.bundle);
      setState((current) => ({ ...current, applyResult, message: "Non-secret recovery settings applied." }));
    } catch (error) {
      setState((current) => ({ ...current, message: errorMessage(error) }));
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Recovery</h1>
          <p className="meta-line">Export and import redacted settings bundles.</p>
        </div>
        <span className="status-pill">{state.applyResult?.applied ? "Applied" : valid ? "Ready" : "No Bundle"}</span>
      </header>

      {state.message ? <p className="notice-state">{state.message}</p> : null}

      <section className="settings-panel" aria-label="Export settings">
        <h2>Export settings bundle</h2>
        <p className="empty-state">Recovery bundles omit tokens, passwords, API keys, encryption keys, and agent setup tokens.</p>
        <div className="dashboard-grid">
          <article className="dashboard-tile"><span>Home</span><strong>Connections and quick links</strong></article>
          <article className="dashboard-tile"><span>Apps</span><strong>Installed app config</strong></article>
          <article className="dashboard-tile"><span>People</span><strong>Roles and invites</strong></article>
        </div>
        <button aria-label="Export Recovery Bundle" onClick={() => void exportBundle()} type="button">Export bundle</button>
      </section>

      <section className="settings-panel" aria-label="Import settings">
        <h2>Import settings bundle</h2>
        <form className="quick-link-form" onSubmit={previewImport}>
          <label>
            <span>Drop a .hankbundle file</span>
            <input
              aria-label="Recovery Bundle"
              accept="application/json,.json"
              onChange={(event) => setState((current) => ({ ...current, bundleFile: event.target.files?.[0] || null }))}
              type="file"
            />
          </label>
          <div className="button-row">
            <button type="submit">Preview Import</button>
            <button disabled={!valid} onClick={() => void applyImport()} type="button">Apply Non-Secret Settings</button>
          </div>
        </form>
        <p className="empty-state">Database snapshot restore lives in Backups.</p>
      </section>

      <section className="settings-panel" aria-label="Import preview">
        <div className="panel-heading">
          <h2>Import Preview</h2>
          <span className="status-pill">{changes.length} change{changes.length === 1 ? "" : "s"}</span>
        </div>
        <div className="card-list">
          {changes.length ? changes.map((change, index) => (
            <article className="dashboard-tile" key={`${change.area}-${change.target}-${index}`}>
              <span>{change.area || "settings"}</span>
              <strong>{change.target || "setting"}</strong>
              <small>{change.action || "update"}</small>
            </article>
          )) : <p className="empty-state">Upload a recovery bundle to preview changes.</p>}
        </div>
      </section>

      <section className="settings-panel" aria-label="Missing secrets">
        <div className="panel-heading">
          <h2>Missing Secrets</h2>
          <span className="status-pill">{secrets.length} secret{secrets.length === 1 ? "" : "s"}</span>
        </div>
        <div className="card-list">
          {secrets.length ? secrets.map((secret, index) => (
            <article className="dashboard-tile" key={`${secret.id}-${index}`}>
              <span>{secret.service_type || "connection"}</span>
              <strong>{secret.label || secret.id || "Secret"}</strong>
              <small>{secret.target?.field || secret.kind || "secret"}</small>
            </article>
          )) : <p className="empty-state">Missing tokens and passwords will appear here after preview.</p>}
        </div>
      </section>
    </section>
  );
}
