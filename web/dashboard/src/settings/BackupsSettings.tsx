import { type FormEvent, useEffect, useState } from "react";
import { logsClient, type AuditEvent } from "../api/logs";
import { storageClient, type QueryTelemetryRow, type StorageConfig, type StorageStatus } from "../api/storage";
import { useConfirmDialog } from "../ui/primitives";

type FormState = {
  targetPath: string;
  fullBackupCron: string;
  differentialBackupCron: string;
  checksumIntervalSeconds: string;
  retentionFull: string;
  amcheckCron: string;
  restoreVerificationCron: string;
};

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      storage: StorageStatus;
      form: FormState;
      restoreLabel: string;
      auditEvents: AuditEvent[];
      auditFilters: { eventType: string; severity: string; targetType: string };
      queryTelemetry: QueryTelemetryRow[];
      queryTelemetryError: string;
      message: string;
    };

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Storage status could not be loaded.";
}

function formatDate(value?: string): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

function formatBytes(value?: number): string {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function telemetryValue(row: QueryTelemetryRow, snakeKey: keyof QueryTelemetryRow, goKey: keyof QueryTelemetryRow): string | number {
  return row[snakeKey] ?? row[goKey] ?? "";
}

function formFromConfig(config: StorageConfig | undefined): FormState {
  const schedule = config?.schedule || {};
  return {
    targetPath: config?.target?.path || "/var/lib/pgbackrest",
    fullBackupCron: schedule.full_backup_cron || "0 2 * * 0",
    differentialBackupCron: schedule.differential_backup_cron || "0 2 * * 1-6",
    checksumIntervalSeconds: String(schedule.checksum_interval_seconds || 900),
    retentionFull: String(schedule.retention_full || 2),
    amcheckCron: schedule.amcheck_cron || "30 3 * * 0",
    restoreVerificationCron: schedule.restore_verification_cron || "0 4 * * 0",
  };
}

function configFromForm(form: FormState): StorageConfig {
  return {
    target: { type: "posix", path: form.targetPath.trim() },
    schedule: {
      full_backup_cron: form.fullBackupCron.trim() || "0 2 * * 0",
      differential_backup_cron: form.differentialBackupCron.trim() || "0 2 * * 1-6",
      checksum_interval_seconds: Number(form.checksumIntervalSeconds || 900) || 900,
      retention_full: Number(form.retentionFull || 2) || 2,
      amcheck_cron: form.amcheckCron.trim() || "30 3 * * 0",
      restore_verification_cron: form.restoreVerificationCron.trim() || "0 4 * * 0",
      restore_verification_enabled: true,
    },
  };
}

function latestBackupLabel(backups: NonNullable<StorageStatus["backup"]>["backups"]): string {
  return (backups || []).reduce((latest, backup) => backup.label > latest ? backup.label : latest, "");
}

export function BackupsSettings() {
  const [restoreSubmitting, setRestoreSubmitting] = useState(false);
  const [state, setState] = useState<State>({ status: "loading" });
  const dialog = useConfirmDialog();

  async function load(message = "") {
    try {
      const [storage, auditPayload, telemetryPayload] = await Promise.all([
        storageClient.status(),
        logsClient.listAuditEvents({ limit: 25 }),
        storageClient.queryTelemetry(20)
          .then((payload) => ({ ...payload, error: "" }))
          .catch((error) => ({
            queries: [],
            error: error instanceof Error ? error.message : "Query telemetry is unavailable.",
          })),
      ]);
      const backups = storage.backup?.backups || [];
      setState({
        status: "ready",
        storage,
        form: formFromConfig(storage.config),
        restoreLabel: latestBackupLabel(backups),
        auditEvents: auditPayload.events,
        auditFilters: { eventType: "", severity: "", targetType: "" },
        queryTelemetry: telemetryPayload.queries,
        queryTelemetryError: telemetryPayload.error,
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
        <h1 id="route-title">Backups & Storage</h1>
        <p className="loading-state">Loading storage status...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Backups & Storage</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const readyState = state;
  const backups = readyState.storage.backup?.backups || [];
  const tasks = readyState.storage.tasks || [];
  const events = readyState.storage.events || [];
  const busy = tasks.some((task) => task.status === "queued" || task.status === "running");
  const lastFullBackup = backups.find((backup) => String(backup.type || "").toLowerCase() === "full") || backups[0];
  const lastDiffBackup = backups.find((backup) => String(backup.type || "").toLowerCase().startsWith("diff"));

  function setReady(next: Partial<Extract<State, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  function setForm(next: Partial<FormState>) {
    setState((current) => current.status === "ready" ? { ...current, form: { ...current.form, ...next } } : current);
  }

  function setAuditFilters(next: Partial<Extract<State, { status: "ready" }>["auditFilters"]>) {
    setState((current) => current.status === "ready" ? { ...current, auditFilters: { ...current.auditFilters, ...next } } : current);
  }

  async function refreshAuditEvents() {
    try {
      const payload = await logsClient.listAuditEvents({
        event_type: readyState.auditFilters.eventType,
        severity: readyState.auditFilters.severity,
        target_type: readyState.auditFilters.targetType,
        limit: 25,
      });
      setReady({ auditEvents: payload.events, message: "Audit filters applied." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function refreshQueryTelemetry() {
    try {
      const payload = await storageClient.queryTelemetry(20);
      setReady({ queryTelemetry: payload.queries, queryTelemetryError: "", message: "Query telemetry refreshed." });
    } catch (error) {
      setReady({
        queryTelemetry: [],
        queryTelemetryError: error instanceof Error ? error.message : "Query telemetry is unavailable.",
        message: errorMessage(error),
      });
    }
  }

  async function saveConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await storageClient.saveConfig({ ...configFromForm(readyState.form), restore: readyState.storage.config?.restore });
      await load("Backup settings saved.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function requestBackup(backupType: "full" | "diff") {
    try {
      await storageClient.requestBackup(backupType);
      await load(`${backupType === "full" ? "Full" : "Diff"} backup requested.`);
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function requestRestoreTest() {
    try {
      await storageClient.requestRestoreTest(readyState.restoreLabel);
      await load("Restore test requested.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function requestPrimaryRestore() {
    if (restoreSubmitting || !readyState.restoreLabel) return;
    const label = readyState.restoreLabel;
    const phrase = readyState.storage.config?.restore?.confirmation_phrase;
    if (!phrase) { setReady({ message: "Restore confirmation settings are unavailable. Refresh and try again." }); return; }
    setRestoreSubmitting(true);
    try {
      const review = await storageClient.reviewPrimaryRestore(label);
      const confirmation = await dialog.prompt({
        title: "Restore database and attachments",
        message: `Replace the Home database and attachment files with backup ${label}? Hank will be unavailable during replacement. Accounts and settings return to their backed-up state. Type ${phrase} to confirm.`,
        label: `Type ${phrase} to confirm`,
        confirmLabel: "Restore this backup",
        tone: "danger",
      });
      if (confirmation === null) return;
      if (confirmation !== phrase) { setReady({ message: "The restore confirmation phrase did not match. Nothing was requested." }); return; }
      await storageClient.requestPrimaryRestore(label, confirmation, review.admin_action_token);
      await load("Paired restore requested. Hank will reconnect after recovery completes.");
    } catch (error) { setReady({ message: errorMessage(error) }); }
    finally { setRestoreSubmitting(false); }
  }

  async function clearEvents() {
    const confirmed = await dialog.confirm({
      title: "Clear storage logs",
      message: "Clear storage logs? Backups and settings will not be changed.",
      confirmLabel: "Clear logs",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      await storageClient.clearEvents();
      await load("Storage logs cleared.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Backups & Storage</h1>
          <p className="meta-line">pgBackRest schedules, checksum checks, and restore tests.</p>
        </div>
        <span className="status-pill">{backups.length} backup{backups.length === 1 ? "" : "s"}</span>
      </header>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}

      <section className="settings-panel" aria-label="Storage health">
        <h2>Storage Health</h2>
        <div className="dashboard-grid">
          <article className="dashboard-tile">
            <span>Checksums</span>
            <strong>{readyState.storage.checksum?.enabled ? "Healthy" : "Needs setup"}</strong>
            <small>Checked {formatDate(readyState.storage.checksum?.last_check_at)} · {readyState.storage.checksum?.failure_count || 0} failures</small>
          </article>
          <article className="dashboard-tile">
            <span>Last full</span>
            <strong>{lastFullBackup?.label || "None"}</strong>
            <small>{formatDate(lastFullBackup?.stopped_at || readyState.storage.backup?.last_successful_at)}</small>
          </article>
          <article className="dashboard-tile">
            <span>Last diff</span>
            <strong>{lastDiffBackup?.label || "None"}</strong>
            <small>{formatDate(lastDiffBackup?.stopped_at)}</small>
          </article>
          <article className="dashboard-tile">
            <span>Restore test</span>
            <strong>{formatDate(readyState.storage.restore?.last_test_at)}</strong>
            <small>Last verification run</small>
          </article>
        </div>
      </section>

      <section className="settings-panel" aria-label="Backup settings">
        <h2>Backup Settings</h2>
        <form className="quick-link-form" onSubmit={saveConfig}>
          <label>
            <span>Backup folder</span>
            <input aria-label="Backup target path" onChange={(event) => setForm({ targetPath: event.target.value })} type="text" value={readyState.form.targetPath} />
          </label>
          <label>
            <span>Full backup day/time</span>
            <input onChange={(event) => setForm({ fullBackupCron: event.target.value })} type="text" value={readyState.form.fullBackupCron} />
          </label>
          <label>
            <span>Diff backup days</span>
            <input onChange={(event) => setForm({ differentialBackupCron: event.target.value })} type="text" value={readyState.form.differentialBackupCron} />
          </label>
          <label>
            <span>Checksum interval seconds</span>
            <input min={60} onChange={(event) => setForm({ checksumIntervalSeconds: event.target.value })} type="number" value={readyState.form.checksumIntervalSeconds} />
          </label>
          <label>
            <span>Full backups kept</span>
            <input min={1} onChange={(event) => setForm({ retentionFull: event.target.value })} type="number" value={readyState.form.retentionFull} />
          </label>
          <label>
            <span>Deep check cron</span>
            <input onChange={(event) => setForm({ amcheckCron: event.target.value })} type="text" value={readyState.form.amcheckCron} />
          </label>
          <label>
            <span>Restore verification cron</span>
            <input onChange={(event) => setForm({ restoreVerificationCron: event.target.value })} type="text" value={readyState.form.restoreVerificationCron} />
          </label>
          <button type="submit">Save backup settings</button>
        </form>
      </section>

      <section className="settings-panel" aria-label="Backups">
        <div className="panel-heading">
          <h2>Backups</h2>
          <div className="button-row">
            <span className="status-pill">Task status {busy ? "Busy" : "Idle"}</span>
            <button disabled={busy} onClick={() => void requestBackup("diff")} type="button">Run Diff Backup</button>
            <button disabled={busy} onClick={() => void requestBackup("full")} type="button">Run Full Backup</button>
            <button disabled={busy || !readyState.restoreLabel} onClick={() => void requestRestoreTest()} type="button">Test Restore</button>
          </div>
        </div>
        <label>
          <span>Backup to restore</span>
          <select onChange={(event) => setReady({ restoreLabel: event.target.value })} value={readyState.restoreLabel}>
            {backups.length ? backups.map((backup) => <option key={backup.label} value={backup.label}>{backup.label}</option>) : <option value="">No backup yet</option>}
          </select>
        </label>
        <p className="empty-state">Restore replaces the database and its matching attachments together. The backup is checked before live data is replaced, and the original pair is retained for recovery.</p>
        <button className="danger" type="button" disabled={busy || restoreSubmitting || !readyState.restoreLabel || readyState.storage.config?.restore?.primary_restore_enabled !== true} onClick={() => void requestPrimaryRestore()}>{restoreSubmitting ? "Reviewing restore…" : "Restore database and attachments"}</button>
        <div className="card-list">
          {backups.length ? backups.map((backup) => (
            <article className="dashboard-tile" key={backup.label}>
              <span>{backup.type || "backup"}</span>
              <strong>{backup.label}</strong>
              <small>{formatBytes(backup.size_bytes)} finished {formatDate(backup.stopped_at)}</small>
            </article>
          )) : <p className="empty-state">No backups reported yet.</p>}
        </div>
      </section>

      <section className="settings-panel" aria-label="Storage logs">
        <div className="panel-heading">
          <h2>Storage Logs</h2>
          <button className="secondary" onClick={() => void clearEvents()} type="button">Clear storage logs</button>
        </div>
        <div className="card-list">
          {events.length ? events.map((event, index) => (
            <article className="dashboard-tile" key={`${event.operation}-${event.time}-${index}`}>
              <span>{event.severity || "info"}</span>
              <strong>{event.message || event.operation || "Storage event"}</strong>
              <small>{event.backup_label || formatDate(event.time)}</small>
            </article>
          )) : <p className="empty-state">No storage logs reported.</p>}
        </div>
      </section>

      <section className="settings-panel" aria-label="Audit trail">
        <div className="panel-heading">
          <h2>Audit Trail</h2>
          <span className="status-pill">{readyState.auditEvents.length} event{readyState.auditEvents.length === 1 ? "" : "s"}</span>
        </div>
        <form className="quick-link-form" onSubmit={(event) => { event.preventDefault(); void refreshAuditEvents(); }}>
          <label>
            <span>Event</span>
            <select onChange={(event) => setAuditFilters({ eventType: event.target.value })} value={readyState.auditFilters.eventType}>
              <option value="">All events</option>
              <option value="agent_token.created">Agent token created</option>
              <option value="file_operation.denied">File operation denied</option>
              <option value="file_operation.requested">File operation requested</option>
              <option value="service_profile.changed">Service profile changed</option>
              <option value="storage.backup_requested">Backup requested</option>
              <option value="storage.restore_test_requested">Restore test requested</option>
            </select>
          </label>
          <label>
            <span>Severity</span>
            <select onChange={(event) => setAuditFilters({ severity: event.target.value })} value={readyState.auditFilters.severity}>
              <option value="">All severities</option>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </select>
          </label>
          <label>
            <span>Target</span>
            <select onChange={(event) => setAuditFilters({ targetType: event.target.value })} value={readyState.auditFilters.targetType}>
              <option value="">All targets</option>
              <option value="agent_token">Agent token</option>
              <option value="file_policy">File policy</option>
              <option value="file_transfer">File transfer</option>
              <option value="service_profile">Service profile</option>
              <option value="storage">Storage</option>
            </select>
          </label>
          <button className="secondary" type="submit">Apply Filters</button>
        </form>
        <div className="card-list">
          {readyState.auditEvents.length ? readyState.auditEvents.map((event, index) => (
            <article className="dashboard-tile" key={`${event.event_type}-${event.occurred_at}-${index}`}>
              <span>{event.severity || "info"}</span>
              <strong>{event.event_type || "audit event"}</strong>
              <small>{event.helper_text || event.target_type || "Audit event"} · {formatDate(event.occurred_at)}</small>
            </article>
          )) : <p className="empty-state">No audit events reported.</p>}
        </div>
      </section>

      <section className="settings-panel" aria-label="Query telemetry">
        <div className="panel-heading">
          <h2>Query Telemetry</h2>
          <span className="status-pill">
            {readyState.queryTelemetryError ? "Unavailable" : `${readyState.queryTelemetry.length} quer${readyState.queryTelemetry.length === 1 ? "y" : "ies"}`}
          </span>
        </div>
        <button className="secondary" onClick={() => void refreshQueryTelemetry()} type="button">Refresh</button>
        <div className="card-list">
          {readyState.queryTelemetryError ? <p className="empty-state">{readyState.queryTelemetryError}</p> : null}
          {!readyState.queryTelemetryError && readyState.queryTelemetry.length ? readyState.queryTelemetry.map((row, index) => {
            const totalMS = Number(telemetryValue(row, "total_exec_ms", "TotalExecMS") || 0);
            const meanMS = Number(telemetryValue(row, "mean_exec_ms", "MeanExecMS") || 0);
            return (
              <article className="dashboard-tile" key={`${telemetryValue(row, "query", "Query")}-${index}`}>
                <span>{Number(telemetryValue(row, "calls", "Calls") || 0).toLocaleString()} calls</span>
                <strong>{Number(telemetryValue(row, "rows", "Rows") || 0).toLocaleString()} rows</strong>
                <small>{totalMS.toFixed(1)} ms total · {meanMS.toFixed(2)} ms mean</small>
                <code className="mono-block">{telemetryValue(row, "query", "Query")}</code>
              </article>
            );
          }) : null}
          {!readyState.queryTelemetryError && !readyState.queryTelemetry.length ? <p className="empty-state">No query telemetry reported.</p> : null}
        </div>
      </section>
    </section>
  );
}
