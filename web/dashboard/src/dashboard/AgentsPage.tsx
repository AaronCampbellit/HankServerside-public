import { FleetAccessPanel } from "./FleetAccessPanel";
import { AgentFleetMap } from "./AgentFleetMap";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  agentDisplayName,
  agentHasCapability,
  agentIsOnline,
  agentIsPrimary,
  agentsClient,
  type AgentAlert,
	type CreatedAgentEnrollment,
  type AgentMetrics,
  type HomeAgentEntry,
	type AgentEnrollment,
	type LinuxUpdatesPayload,
} from "../api/agents";
import { terminalStore } from "../api/terminalStore";
import { bootstrapClient } from "../api/bootstrap";
import { homeClient, type AgentToken, type CreatedAgentToken } from "../api/home";
import { useConfirmDialog, useToast } from "../ui/primitives";
import { DesktopReadinessCard, desktopReadinessComplete } from "../desktop/DesktopReadinessCard";
import {desktopAuditClient,type DesktopAgentReadiness} from "../api/desktopAudit";
import { desktopClient } from "../api/desktop";
import { DesktopTrustSettings } from "../desktop/trust/DesktopTrustSettings";
import { DesktopViewerPage } from "../desktop/DesktopViewerPage";
import type { AgentWorkspaceTab } from "../router";

type PageState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "unsupported" }
  | {
      status: "ready";
      isAdmin: boolean;
      homeID: string;
      userID: string;
      agents: HomeAgentEntry[];
      tokens: AgentToken[];
	  enrollments: AgentEnrollment[];
	  linuxUpdates: LinuxUpdatesPayload | null;
      alerts: AgentAlert[];
    };

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Agents could not be loaded.";
}

function formatBytes(bytes: number | undefined): string {
  if (!bytes || bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function percentOf(used: number | undefined, total: number | undefined): number | null {
  if (!used || !total || total <= 0) return null;
  return Math.round((used / total) * 100);
}

function formatUptime(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return "—";
  const days = Math.floor(seconds / 86400);
  if (days > 0) return `${days}d ${Math.floor((seconds % 86400) / 3600)}h`;
  const hours = Math.floor(seconds / 3600);
  if (hours > 0) return `${hours}h ${Math.floor((seconds % 3600) / 60)}m`;
  return `${Math.floor(seconds / 60)}m`;
}

function relativeTime(value: string | null | undefined): string {
  if (!value) return "never";
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "unknown";
  const diff = Date.now() - then;
  const minutes = Math.round(diff / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

function agentKindLabel(agent: HomeAgentEntry): string {
  return agentIsPrimary(agent) ? "Primary Hank Agent" : "Worker";
}

function MetricStat({ label, value, tone }: { label: string; value: string; tone?: "warn" | "bad" }) {
  return (
    <div className={`agent-stat${tone ? ` agent-stat-${tone}` : ""}`}>
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}

function MetricRow({ metrics }: { metrics: AgentMetrics | undefined }) {
  if (!metrics) return <span className="agent-tile-idle">no metrics</span>;
  const ram = percentOf(metrics.memory_used_bytes, metrics.memory_total_bytes);
  const disk = percentOf(metrics.disk_used_bytes, metrics.disk_total_bytes);
  return (
    <div className="agent-tile-metrics">
      {typeof metrics.cpu_load_1m === "number" ? <span>CPU {metrics.cpu_load_1m.toFixed(2)}</span> : null}
      {ram !== null ? <span>RAM {ram}%</span> : null}
      {disk !== null ? <span className={disk >= 90 ? "agent-metric-bad" : undefined}>Disk {disk}%</span> : null}
      {typeof metrics.battery_percent === "number" ? (
        <span>{metrics.battery_charging ? "⚡" : "Batt"} {metrics.battery_percent}%</span>
      ) : null}
    </div>
  );
}

function AgentCard({ agent }: { agent: HomeAgentEntry }) {
  const online = agentIsOnline(agent);
  return (
    <a className="agent-card" href={`/dashboard/agents/${encodeURIComponent(agent.agent_id)}`}>
      <div className="agent-card-top">
        <span className={`agent-avatar ${agentIsPrimary(agent) ? "is-primary" : "is-worker"}`} aria-hidden="true">
          {agentIsPrimary(agent) ? "⌂" : "▤"}
        </span>
        <div className="agent-card-identity">
          <strong>{agentDisplayName(agent)}</strong>
          <span>{agentKindLabel(agent)}</span>
        </div>
        <span className={`status-pill ${online ? "status-online" : "status-offline"}`}>
          {online ? "Online" : "Offline"}
        </span>
      </div>
      {online ? (
        <MetricRow metrics={agent.metrics} />
      ) : (
        <span className="agent-tile-idle">last seen {relativeTime(agent.last_seen_at)}</span>
      )}
    </a>
  );
}

function LinuxUpdatePanel({ update, agents, isAdmin, onPause, onRetry, onPin, onUnpin }: {
  update: LinuxUpdatesPayload;
  agents: HomeAgentEntry[];
  isAdmin: boolean;
  onPause: (paused: boolean) => void;
  onRetry: (assignmentID: string) => void;
  onPin: (agentID: string, version: string) => void;
  onUnpin: (agentID: string) => void;
}) {
  const pinByAgent = new Map(update.pins.map((pin) => [pin.agent_id, pin.version]));
  const nameFor = (agentID: string) => agentDisplayName(agents.find((agent) => agent.agent_id === agentID) ?? { agent_id: agentID, status: "offline" });
  return (
    <section className="agent-panel linux-update-panel" aria-labelledby="linux-update-title">
      <div className="agent-panel-head">
        <div>
          <p className="eyebrow">Automatic updates</p>
          <h2 id="linux-update-title">Linux fleet</h2>
          <p className="meta-line">{update.rollout ? `Target ${update.rollout.version} · ${update.rollout.state}` : "No active rollout"}</p>
        </div>
        {isAdmin && update.rollout ? <button type="button" className="secondary" onClick={() => onPause(update.rollout?.state === "active")}>{update.rollout.state === "active" ? "Pause rollout" : "Resume rollout"}</button> : null}
      </div>
      {update.assignments.length ? (
        <div className="linux-update-list">
          {update.assignments.map((assignment) => {
            const agent = agents.find((candidate) => candidate.agent_id === assignment.agent_id);
            const current = agent?.metadata?.app_version || assignment.from_version || "unknown";
            const pin = pinByAgent.get(assignment.agent_id);
            const retryable = ["failed", "rolled_back", "recovery_failed"].includes(assignment.state);
            return <div className="linux-update-row" key={assignment.id}>
              <div><strong>{nameFor(assignment.agent_id)}</strong><span>{current} → {assignment.to_version}{pin ? ` · pinned ${pin}` : ""}</span></div>
              <span className={`status-pill ${assignment.state === "healthy" ? "status-online" : ""}`}>{assignment.state.replaceAll("_", " ")}</span>
              {isAdmin ? <div className="linux-update-actions">
                {retryable ? <button type="button" className="secondary compact" onClick={() => onRetry(assignment.id)}>Retry</button> : null}
                {pin ? <button type="button" className="secondary compact" onClick={() => onUnpin(assignment.agent_id)}>Unpin</button> : current !== "unknown" ? <button type="button" className="secondary compact" onClick={() => onPin(assignment.agent_id, current)}>Pin {current}</button> : null}
              </div> : null}
              {assignment.error_code ? <small>{assignment.error_code}</small> : null}
            </div>;
          })}
        </div>
      ) : <p className="empty-state">Linux system agents will appear here when a tagged release activates.</p>}
    </section>
  );
}

function ShellConsole({ agent }: { agent: HomeAgentEntry }) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const writtenRef = useRef(0);
  const snapshot = useSyncExternalStore(
    (listener) => terminalStore.subscribe(agent.agent_id, listener),
    () => terminalStore.snapshot(agent.agent_id),
  );

  useEffect(() => {
    const host = hostRef.current;
    if (!host || navigator.userAgent.toLowerCase().includes("jsdom")) return;
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: false,
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      scrollback: 5000,
      theme: { background: "#0b1118", foreground: "#d7e0ea", cursor: "#57a6ff", selectionBackground: "#2c5c88" },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    fit.fit();
    terminalRef.current = terminal;
    fitRef.current = fit;
    const input = terminal.onData((data) => { void terminalStore.write(agent.agent_id, data); });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => {
      fit.fit();
      void terminalStore.resize(agent.agent_id, terminal.cols, terminal.rows);
    });
    observer?.observe(host);
    return () => { observer?.disconnect(); input.dispose(); terminal.dispose(); terminalRef.current = null; fitRef.current = null; writtenRef.current = 0; };
  }, [agent.agent_id]);

  useEffect(() => {
    const terminal = terminalRef.current;
    if (!terminal) return;
    if (snapshot.output.length < writtenRef.current) { terminal.clear(); writtenRef.current = 0; }
    const next = snapshot.output.slice(writtenRef.current);
    if (next) terminal.write(next);
    writtenRef.current = snapshot.output.length;
  }, [snapshot.output]);

  useEffect(() => {
    if (!snapshot.sessionID || snapshot.status === "closed" || snapshot.status === "exited") return;
    void terminalStore.attach(agent.agent_id);
    const timer = window.setInterval(() => { void terminalStore.attach(agent.agent_id); }, 10_000);
    return () => window.clearInterval(timer);
  }, [agent.agent_id, snapshot.sessionID]);

  async function start() {
    const terminal = terminalRef.current;
    await terminalStore.open(agent.agent_id, terminal?.cols || 100, terminal?.rows || 30);
    terminal?.focus();
  }

  return (
    <div className="agent-shell">
      <div className="agent-shell-head">
        <div><h3>Live shell</h3><span className="agent-shell-badge">{snapshot.status} · audited · admin only</span></div>
        <div className="agent-shell-actions">
          {snapshot.status === "closed" || snapshot.status === "exited" || snapshot.status === "error" ? (
            <button type="button" className="secondary" onClick={() => void start()}>New terminal</button>
          ) : (
            <button type="button" className="danger" onClick={() => void terminalStore.close(agent.agent_id)}>Close</button>
          )}
        </div>
      </div>
      <div className="agent-shell-terminal" ref={hostRef} aria-label={`Live terminal on ${agentDisplayName(agent)}`} />
      {snapshot.error ? <p className="agent-shell-error">{snapshot.error}</p> : null}
    </div>
  );
}

function AgentDetail({
  agent,
  tab,
  isAdmin,
  homeID,
  userID,
  onAction,
  onRemove,
	onRotateCredentials,
	onRevokeCredentials,
}: {
  agent: HomeAgentEntry;
  tab: AgentWorkspaceTab;
  isAdmin: boolean;
  homeID: string;
  userID: string;
  onAction: (kind: "lock" | "restart" | "wake", agent: HomeAgentEntry) => void;
  onRemove: (agent: HomeAgentEntry) => void;
	onRotateCredentials: (agent: HomeAgentEntry) => void;
	onRevokeCredentials: (agent: HomeAgentEntry) => void;
}) {
  const online = agentIsOnline(agent);
  const [desktopReadiness, setDesktopReadiness] = useState<DesktopAgentReadiness | null>(null);
  const [endingDesktopSession, setEndingDesktopSession] = useState(false);
  const { confirm } = useConfirmDialog();
  const { showToast } = useToast();
  const refreshDesktopReadiness = useCallback(async () => {
    const value = await desktopAuditClient.readiness(agent.agent_id);
    setDesktopReadiness(value);
    return value;
  }, [agent.agent_id]);
  useEffect(() => {
    if (tab !== "desktop") return;
    let live = true;
    desktopAuditClient.readiness(agent.agent_id)
      .then((value) => { if (live) setDesktopReadiness(value); })
      .catch(() => { if (live) setDesktopReadiness(null); });
    return () => { live = false; };
  }, [agent.agent_id, tab]);
  async function endActiveDesktopSession() {
    const sessionID = desktopReadiness?.active_session_id;
    if (!sessionID || endingDesktopSession) return;
    if (!await confirm({
      title: "End Remote Desktop session?",
      message: "This disconnects the current Remote Desktop viewer and releases this device for a new session.",
      confirmLabel: "End session",
      tone: "danger",
    })) return;
    setEndingDesktopSession(true);
    try {
      await desktopClient.terminate(sessionID);
      await refreshDesktopReadiness();
      showToast("Remote Desktop session ended.", "neutral");
    } catch (error) {
      showToast(errorMessage(error), "error");
    } finally {
      setEndingDesktopSession(false);
    }
  }
  const metrics = agent.metrics;
  const desktopReady = desktopReadinessComplete(desktopReadiness);
  const ram = percentOf(metrics?.memory_used_bytes, metrics?.memory_total_bytes);
  const disk = percentOf(metrics?.disk_used_bytes, metrics?.disk_total_bytes);
  const basePath = `/dashboard/agents/${encodeURIComponent(agent.agent_id)}`;
  const tabs: Array<{ id: AgentWorkspaceTab; label: string; href: string; adminOnly?: boolean }> = [
    { id: "overview", label: "Overview", href: basePath },
    { id: "desktop", label: "Remote Desktop", href: `${basePath}/desktop`, adminOnly: true },
    { id: "terminal", label: "Terminal", href: `${basePath}/terminal`, adminOnly: true },
    { id: "security", label: "Security", href: `${basePath}/security`, adminOnly: true },
  ];

  return (
    <section className="agent-detail">
      <div className="agent-detail-head">
        <a className="button secondary agent-back" href="/dashboard/agents">← All devices</a>
        <div className="agent-detail-title">
          <span className={`agent-avatar ${agentIsPrimary(agent) ? "is-primary" : "is-worker"}`} aria-hidden="true">
            {agentIsPrimary(agent) ? "⌂" : "▤"}
          </span>
          <div>
            <h1 id="route-title">{agentDisplayName(agent)}</h1>
            <span>{agent.metadata?.os_version || agentKindLabel(agent)}</span>
          </div>
        </div>
        <span className={`status-pill ${online ? "status-online" : "status-offline"}`}>
          {online ? "Online" : `Offline · ${relativeTime(agent.last_seen_at)}`}
        </span>
      </div>

      <nav className="agent-workspace-tabs" aria-label="Device workspace">
        {tabs.filter((item) => isAdmin || !item.adminOnly).map((item) => (
          <a key={item.id} href={item.href} aria-current={tab === item.id ? "page" : undefined}>{item.label}</a>
        ))}
      </nav>

      {tab === "overview" ? (
        <>
          <div className="agent-stat-grid">
            {typeof metrics?.cpu_load_1m === "number" ? <MetricStat label="CPU load (1m)" value={metrics.cpu_load_1m.toFixed(2)} /> : null}
            {ram !== null ? <MetricStat label={`Memory (${formatBytes(metrics?.memory_used_bytes)} / ${formatBytes(metrics?.memory_total_bytes)})`} value={`${ram}%`} tone={ram >= 90 ? "warn" : undefined} /> : null}
            {disk !== null ? <MetricStat label={`Disk (${formatBytes(metrics?.disk_used_bytes)} / ${formatBytes(metrics?.disk_total_bytes)})`} value={`${disk}%`} tone={disk >= 90 ? "bad" : undefined} /> : null}
            {typeof metrics?.battery_percent === "number" ? <MetricStat label={metrics.battery_charging ? "Battery (charging)" : "Battery"} value={`${metrics.battery_percent}%`} /> : null}
            {typeof metrics?.uptime_seconds === "number" ? <MetricStat label="Uptime" value={formatUptime(metrics.uptime_seconds)} /> : null}
          </div>
          <div className="agent-detail-columns">
            <div className="agent-info-card">
              <h2>Details</h2>
              <dl className="agent-info-list">
                <div><dt>Agent ID</dt><dd>{agent.agent_id}</dd></div>
                <div><dt>Type</dt><dd>{agentKindLabel(agent)}</dd></div>
                <div><dt>Status</dt><dd>{online ? "Online" : "Offline"}</dd></div>
                <div><dt>Last seen</dt><dd>{relativeTime(agent.last_seen_at)}</dd></div>
                {agent.metadata?.hostname ? <div><dt>Hostname</dt><dd>{agent.metadata.hostname}</dd></div> : null}
                {agent.metadata?.platform ? <div><dt>Platform</dt><dd>{agent.metadata.platform}</dd></div> : null}
                {agent.metadata?.app_version ? <div><dt>Agent version</dt><dd>{agent.metadata.app_version}</dd></div> : null}
              </dl>
            </div>
            <div className="agent-info-card">
              <h2>Device actions</h2>
              {isAdmin ? (
                <div className="agent-actions">
                  {agentHasCapability(agent, "host.lock") ? <button type="button" className="secondary" disabled={!online} onClick={() => onAction("lock", agent)}>Lock screen</button> : null}
                  {agentHasCapability(agent, "wol.send") ? <button type="button" className="secondary" disabled={!online} onClick={() => onAction("wake", agent)}>Wake device…</button> : null}
                  <button type="button" className="danger" disabled={!online} onClick={() => onAction("restart", agent)}>Restart agent</button>
                </div>
              ) : <p className="empty-state">Device actions require an admin account.</p>}
            </div>
          </div>
        </>
      ) : null}

      {tab === "desktop" ? (
        <div className="agent-desktop-workspace">
          <div className="agent-detail-columns">
            <DesktopReadinessCard readiness={desktopReadiness} />
            <div className="agent-info-card">
              <h2>Session</h2>
              <p className="agent-hint">{desktopReady ? "This device is ready for an encrypted Remote Desktop session." : "Complete every readiness check before starting a session."}</p>
              {desktopReadiness?.active_session_id ? (
                <button type="button" className="danger" disabled={endingDesktopSession} onClick={() => void endActiveDesktopSession()}>
                  {endingDesktopSession ? "Ending session…" : "End active Remote Desktop session"}
                </button>
              ) : null}
            </div>
          </div>
          {isAdmin ? <DesktopViewerPage embedded agentID={agent.agent_id} /> : <p className="empty-state">Remote Desktop requires an admin account.</p>}
        </div>
      ) : null}

      {tab === "terminal" ? (
        isAdmin && agentHasCapability(agent, "shell.session.open")
          ? <ShellConsole agent={agent} />
          : <p className="empty-state">Remote shell is disabled or unavailable for this account.</p>
      ) : null}

      {tab === "security" ? (
        <div className="agent-security-workspace">
          <div className="agent-info-card">
            <h2>Capabilities</h2>
            {agent.capabilities?.length ? (
              <div className="agent-capabilities">
                {agent.capabilities.map((capability) => <span className="agent-capability" key={capability}>{capability}</span>)}
              </div>
            ) : <p className="empty-state">This device has not reported any capabilities.</p>}
          </div>
          {isAdmin ? <DesktopTrustSettings homeID={homeID} userID={userID} agentID={agent.agent_id} agentName={agentDisplayName(agent)} /> : null}
		  {isAdmin ? <div className="agent-info-card">
			<h2>Device credentials</h2>
			<p className="agent-hint">Credentials rotate every 30 days. A requested rotation completes automatically when this agent checks in.</p>
			<div className="agent-actions">
			  <button type="button" className="secondary" onClick={() => onRotateCredentials(agent)}>Rotate credentials</button>
			  <button type="button" className="danger" onClick={() => onRevokeCredentials(agent)}>Revoke credentials</button>
			</div>
		  </div> : null}
          {isAdmin ? <div className="agent-info-card agent-danger-zone">
            <h2>Danger zone</h2>
            <div className="agent-actions">
              <button type="button" className="danger" onClick={() => onRemove(agent)}>Remove device</button>
            </div>
          </div> : null}
        </div>
      ) : null}
    </section>
  );
}

function linuxEnrollmentStatus(enrollment: AgentEnrollment): string {
  if (enrollment.consumed_at) return "Used";
  if (enrollment.revoked_at) return "Revoked";
  if (new Date(enrollment.expires_at).getTime() <= Date.now()) return "Expired";
  if (enrollment.downloaded_at) return "Downloaded";
  return "Pending";
}

function AddDeviceSection({
  enrollments,
  onCreate,
  onRevoke,
}: {
  enrollments: AgentEnrollment[];
  onCreate: (nameHint: string, pairing?: boolean, platform?: "linux" | "macos" | "windows") => Promise<CreatedAgentEnrollment | null>;
  onRevoke: (enrollment: AgentEnrollment) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [nameHint, setNameHint] = useState("");
  const [created, setCreated] = useState<CreatedAgentEnrollment | null>(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
 const [pairing, setPairing] = useState(true);
 const [platform, setPlatform] = useState<"linux" | "macos" | "windows">("linux");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!created) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [created]);

  async function create() {
    if (busy) return;
    setBusy(true);
    setCopied(false);
    try {
      const result = await onCreate(nameHint.trim(), platform !== "linux" || pairing, platform);
      setNow(Date.now());
      setCreated(result);
    } finally {
      setBusy(false);
    }
  }

  async function copyCommand() {
    if (!created || !navigator.clipboard) return;
    await navigator.clipboard.writeText(created.pairing_code || created.install_command);
    setCopied(true);
  }

  const secondsRemaining = created ? Math.max(0, Math.ceil((new Date(created.expires_at).getTime() - now) / 1000)) : 0;

  return (
    <section className="agent-panel linux-installer-panel">
      <div className="agent-panel-head">
        <div>
          <h2>Add device</h2>
          <span className="meta-line">Connect Linux, Mac, or Windows with a one-time pairing code</span>
        </div>
        <button type="button" onClick={() => setExpanded((value) => !value)}>{expanded ? "Close setup" : "Add device"}</button>
      </div>
      {expanded ? (
        <div className="linux-installer-body">
          <label>Device platform<select value={platform} disabled={busy} onChange={(event) => { setPlatform(event.target.value as typeof platform); setCreated(null); setCopied(false); }}><option value="linux">Linux</option><option value="macos">Mac</option><option value="windows">Windows</option></select></label>
          {platform === "linux" ? <div className="linux-root-warning" role="note">
            <strong>Unrestricted root access</strong>
            <span>This installs a system-wide privileged RMM service with full file, terminal, package, process, and service control.</span>
          </div> : <p>In Hank Agent settings on this device, enter the pairing code below. Use the same server address shown here.</p>}
          {platform === "linux" ? <label className="checkbox-field"><input type="checkbox" checked={pairing} onChange={(event) => setPairing(event.target.checked)} /><span>Pair an installed Linux agent with a short code</span></label> : null}
 <div className="linux-installer-create">
            <label>
              <span>Machine label (optional)</span>
              <input value={nameHint} placeholder="Demo server" autoComplete="off" onChange={(event) => setNameHint(event.target.value)} />
            </label>
            <button type="button" disabled={busy} onClick={() => void create()}>{busy ? "Creating…" : (platform !== "linux" || pairing) ? "Create pairing code" : "Create one-time link"}</button>
          </div>
          {created ? (
            <div className="agent-token-created linux-install-command">
              <p>{created.pairing_code ? "Enter this server address and code in Hank Agent setup." : "Copy this command now."} It enrolls one machine and expires in {secondsRemaining}s.</p>
 {created.pairing_code && <><label className="linux-pair-server">Server address<input readOnly value={created.server_url} /></label>{platform === "linux" ? <p>On the Linux machine, open setup with <code>sudo hankagent --system setup</code>.</p> : <p>Open Hank Agent → Settings → Pair this device.</p>}</>}
              <div className="linux-install-command-row">
                <input aria-label={created.pairing_code ? "Pairing code" : "Linux install command"} readOnly value={secondsRemaining ? created.pairing_code || created.install_command : "Expired — create a new code"} />
                <button type="button" className="secondary" disabled={!secondsRemaining} onClick={() => void copyCommand()}>{copied ? "Copied" : "Copy"}</button>
              </div>
            </div>
          ) : null}
          <details><summary>Enrollment history</summary>{enrollments.length ? (
            <table className="agent-token-table">
              <thead><tr><th scope="col">Label</th><th scope="col">Created</th><th scope="col">Status</th><th scope="col" /></tr></thead>
              <tbody>
                {enrollments.map((enrollment) => {
                  const status = linuxEnrollmentStatus(enrollment);
                  return <tr key={enrollment.id}>
                    <td>{enrollment.name_hint || "Linux machine"}</td>
                    <td>{relativeTime(enrollment.created_at)}</td>
                    <td><span className={`status-pill ${status === "Pending" || status === "Downloaded" ? "status-online" : "status-offline"}`}>{status}</span></td>
                    <td className="agent-token-actions">{status === "Pending" || status === "Downloaded" ? <button type="button" className="danger" onClick={() => onRevoke(enrollment)}>Revoke</button> : null}</td>
                  </tr>;
                })}
              </tbody>
            </table>
          ) : <p className="empty-state">No enrollment history yet.</p>}</details>
        </div>
      ) : null}
    </section>
  );
}

function TokenSection({
  tokens,
  onCreate,
  onRevoke,
}: {
  tokens: AgentToken[];
  onCreate: (agentID: string, name: string) => Promise<CreatedAgentToken | null>;
  onRevoke: (token: AgentToken) => void;
}) {
  const [agentID, setAgentID] = useState("");
  const [name, setName] = useState("");
  const [created, setCreated] = useState<CreatedAgentToken | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!agentID.trim() || busy) return;
    setBusy(true);
    try {
      const result = await onCreate(agentID.trim(), name.trim() || agentID.trim());
      if (result) {
        setCreated(result);
        setAgentID("");
        setName("");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="agent-panel">
      <div className="agent-panel-head">
        <h2>Enrollment tokens</h2>
        <span className="meta-line">Admin only · one token per device</span>
      </div>
      <form
        className="agent-token-form"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <label>
          <span>Agent ID</span>
          <input value={agentID} placeholder="mac-studio" autoComplete="off" onChange={(event) => setAgentID(event.target.value)} />
        </label>
        <label>
          <span>Display name</span>
          <input value={name} placeholder="Mac Studio" autoComplete="off" onChange={(event) => setName(event.target.value)} />
        </label>
        <button type="submit" disabled={busy || !agentID.trim()}>{busy ? "Creating…" : "Create token"}</button>
      </form>

      {created ? (
        <div className="agent-token-created">
          <p>Token for <strong>{created.agent_id}</strong> — copy it now, it won't be shown again:</p>
          <code className="agent-token-value">{created.token}</code>
        </div>
      ) : null}

      {tokens.length ? (
        <table className="agent-token-table">
          <thead>
            <tr><th scope="col">Agent</th><th scope="col">Created</th><th scope="col">Status</th><th scope="col" /></tr>
          </thead>
          <tbody>
            {tokens.map((token) => (
              <tr key={token.id}>
                <td>{token.agent_id}</td>
                <td>{relativeTime(token.created_at)}</td>
                <td>{token.revoked_at ? <span className="status-pill status-offline">Revoked</span> : <span className="status-pill status-online">Active</span>}</td>
                <td className="agent-token-actions">
                  {!token.revoked_at ? (
                    <button type="button" className="danger" onClick={() => onRevoke(token)}>Revoke</button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="empty-state">No enrollment tokens yet.</p>
      )}
    </section>
  );
}

export function AgentsPage({
  initialAgentID = null,
  initialTab = "overview",
}: {
  initialAgentID?: string | null;
  initialTab?: AgentWorkspaceTab;
}) {
  const [state, setState] = useState<PageState>({ status: "loading" });
  const { showToast } = useToast();
  const { confirm, prompt } = useConfirmDialog();

  const refresh = useCallback(async (): Promise<HomeAgentEntry[] | null> => {
    try {
      const [agents, linuxUpdates] = await Promise.all([agentsClient.listAgents(), typeof agentsClient.linuxUpdates === "function" ? agentsClient.linuxUpdates().catch(() => null) : Promise.resolve(null)]);
      setState((current) =>
        current.status === "ready"
          ? { ...current, agents, linuxUpdates }
          : current,
      );
      return agents;
    } catch {
      return null;
    }
  }, []);

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const bootstrap = await bootstrapClient.load();
        const isAdmin = Boolean(bootstrap.permissions?.is_admin);
		const [agents, tokens, enrollments, linuxUpdates] = await Promise.all([
		  agentsClient.listAgents(),
		  isAdmin ? homeClient.listAgentTokens().then((payload) => payload.tokens).catch(() => []) : Promise.resolve([]),
		  isAdmin ? agentsClient.listEnrollments().catch(() => []) : Promise.resolve([]),
		  typeof agentsClient.linuxUpdates === "function" ? agentsClient.linuxUpdates().catch(() => null) : Promise.resolve(null),
		]);
        if (!active) return;
		setState({ status: "ready", isAdmin, homeID: bootstrap.home?.id ?? "", userID: bootstrap.user.id, agents, tokens, enrollments, linuxUpdates, alerts: [] });
        void agentsClient.subscribeHealth().catch(() => {
          // The periodic HTTP refresh keeps the workspace usable when live health is unavailable.
        });
      } catch (error) {
        if (!active) return;
        if (error instanceof Error && /not found|404/i.test(error.message)) {
          setState({ status: "unsupported" });
        } else {
          setState({ status: "error", message: errorMessage(error) });
        }
      }
    })();

    const unsubscribe = agentsClient.onAlert((alert) => {
      if (!active) return;
      setState((current) => (current.status === "ready" ? { ...current, alerts: [alert, ...current.alerts].slice(0, 25) } : current));
      showToast(alert.summary, alert.severity === "info" ? "neutral" : "error");
      void refresh();
    }, () => { if (active) void refresh(); });

    const timer = window.setInterval(() => void refresh(), 15000);
    return () => {
      active = false;
      unsubscribe();
      window.clearInterval(timer);
    };
  }, [refresh, showToast]);

  const ready = state.status === "ready" ? state : null;
  const selected = useMemo(
    () => (ready && initialAgentID ? ready.agents.find((agent) => agent.agent_id === initialAgentID) ?? null : null),
    [initialAgentID, ready],
  );

  async function performAction(kind: "lock" | "restart" | "wake", agent: HomeAgentEntry) {
    try {
      if (kind === "lock") {
        await agentsClient.lock(agent.agent_id);
        showToast(`Locked ${agentDisplayName(agent)}`);
      } else if (kind === "restart") {
        const ok = await confirm({
          title: `Restart ${agentDisplayName(agent)}?`,
          message: "The agent process will restart and briefly disconnect.",
          confirmLabel: "Restart",
          tone: "danger",
        });
        if (!ok) return;
        await agentsClient.restart(agent.agent_id);
        showToast(`Restart requested for ${agentDisplayName(agent)}`);
      } else if (kind === "wake") {
        const mac = await prompt({
          title: "Wake a device",
          message: `${agentDisplayName(agent)} will broadcast a wake-on-LAN packet to this MAC address.`,
          placeholder: "AA:BB:CC:DD:EE:FF",
          confirmLabel: "Send wake packet",
        });
        if (!mac) return;
        await agentsClient.wakeOnLAN(agent.agent_id, mac);
        showToast(`Sent wake packet from ${agentDisplayName(agent)}`);
      }
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function createToken(agentID: string, name: string): Promise<CreatedAgentToken | null> {
    try {
      const token = await homeClient.createAgentToken({ agent_id: agentID, name, agent_type: "worker", expires_in_seconds: 0 });
      const tokens = await homeClient.listAgentTokens().then((payload) => payload.tokens).catch(() => []);
      setState((current) => (current.status === "ready" ? { ...current, tokens } : current));
      showToast(`Token created for ${agentID}`);
      return token;
    } catch (error) {
      showToast(errorMessage(error), "error");
      return null;
    }
  }

  async function createEnrollment(nameHint: string, pairing = false, platform: "linux" | "macos" | "windows" = "linux"): Promise<CreatedAgentEnrollment | null> {
    try {
      const created = await agentsClient.createEnrollment(nameHint, pairing, platform);
      const enrollments = await agentsClient.listEnrollments().catch(() => []);
      setState((current) => (current.status === "ready" ? { ...current, enrollments } : current));
      showToast(pairing ? "Pairing code created" : "One-time Linux installer link created");
      return created;
    } catch (error) {
      showToast(errorMessage(error), "error");
      return null;
    }
  }

  async function revokeEnrollment(enrollment: AgentEnrollment) {
    const ok = await confirm({
      title: "Revoke this Linux installer link?",
      message: "The copied command will stop working immediately if it has not already enrolled a machine.",
      confirmLabel: "Revoke link",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await agentsClient.revokeEnrollment(enrollment.id);
      const enrollments = await agentsClient.listEnrollments().catch(() => []);
      setState((current) => (current.status === "ready" ? { ...current, enrollments } : current));
      showToast("Linux installer link revoked");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function pauseLinuxRollout(paused: boolean) {
    try {
      await agentsClient.setLinuxRolloutPaused(paused);
      await refresh();
      showToast(paused ? "Linux rollout paused" : "Linux rollout resumed");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function retryLinuxUpdate(assignmentID: string) {
    try {
      await agentsClient.retryLinuxUpdate(assignmentID);
      await refresh();
      showToast("Linux update retry queued");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function pinLinuxAgent(agentID: string, version: string) {
    try {
      await agentsClient.pinLinuxAgent(agentID, version);
      await refresh();
      showToast(`Linux agent pinned to ${version}`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function unpinLinuxAgent(agentID: string) {
    try {
      await agentsClient.unpinLinuxAgent(agentID);
      await refresh();
      showToast("Linux agent pin removed");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function rotateAgentCredentials(agent: HomeAgentEntry) {
    try {
      await agentsClient.requestCredentialRotation(agent.agent_id);
      showToast(`Credential rotation requested for ${agentDisplayName(agent)}`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function revokeAgentCredentials(agent: HomeAgentEntry) {
    const ok = await confirm({
      title: `Revoke credentials for ${agentDisplayName(agent)}?`,
      message: "This immediately disconnects the device and blocks every current credential. A new one-time installer link is required to enroll it again.",
      confirmLabel: "Revoke credentials",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await agentsClient.revokeAgentCredentials(agent.agent_id);
      await refresh();
      showToast(`Credentials revoked for ${agentDisplayName(agent)}`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function removeAgent(agent: HomeAgentEntry) {
    const ok = await confirm({
      title: `Remove ${agentDisplayName(agent)}?`,
      message: "This permanently removes the agent, its enrollment credentials, Remote Desktop identity, and stored device record. The device must sign in again to reconnect.",
      confirmLabel: "Remove device",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await homeClient.removeAgent(agent.agent_id);
      await refresh();
      const tokens = await homeClient.listAgentTokens().then((payload) => payload.tokens).catch(() => []);
      setState((current) => (current.status === "ready" ? { ...current, tokens } : current));
      showToast(`${agentDisplayName(agent)} removed`);
      window.history.pushState({}, "", "/dashboard/agents");
      window.dispatchEvent(new PopStateEvent("popstate"));
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function revokeToken(token: AgentToken) {
    const ok = await confirm({
      title: `Revoke ${token.agent_id}?`,
      message: "That device will be disconnected and can no longer authenticate.",
      confirmLabel: "Revoke",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await homeClient.revokeAgentToken(token.id);
      const tokens = await homeClient.listAgentTokens().then((payload) => payload.tokens).catch(() => []);
      setState((current) => (current.status === "ready" ? { ...current, tokens } : current));
      showToast(`Revoked ${token.agent_id}`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  if (state.status === "loading") {
    return (
      <section className="dashboard-page agents-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Agents</h1>
        <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading agents…</p>
      </section>
    );
  }

  if (state.status === "unsupported") {
    return (
      <section className="dashboard-page agents-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Agents</h1>
        <p className="notice-state">This server doesn't support multiple agents yet. Deploy the multi-agent update to manage devices here.</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="dashboard-page agents-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Agents</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const onlineCount = state.agents.filter(agentIsOnline).length;

  return (
    <section className="dashboard-page agents-page" aria-labelledby="route-title">
      {!selected ? <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Agents</h1>
          <p className="meta-line">{state.agents.length} device{state.agents.length === 1 ? "" : "s"} · {onlineCount} online</p>
        </div>
        <div className="settings-actions">
          <button type="button" className="secondary" onClick={() => void refresh()}>Refresh</button>
        </div>
      </header> : null}

      {state.alerts.length ? (
        <section className="agent-alerts" aria-label="Recent alerts">
          {state.alerts.slice(0, 4).map((alert, index) => (
            <div className={`agent-alert agent-alert-${alert.severity}`} key={`${alert.agent_id}-${alert.time}-${index}`}>
              <span className="agent-alert-dot" aria-hidden="true" />
              <span className="agent-alert-body">
                <strong>{state.agents.find((agent) => agent.agent_id === alert.agent_id)?.name || alert.agent_id}</strong>
                {" "}{alert.summary}
              </span>
              <span className="agent-alert-time">{relativeTime(alert.time)}</span>
            </div>
          ))}
        </section>
      ) : null}


      {selected ? (
		<AgentDetail agent={selected} tab={state.isAdmin ? initialTab : "overview"} isAdmin={state.isAdmin} homeID={state.homeID} userID={state.userID} onAction={(kind, agent) => void performAction(kind, agent)} onRemove={(agent) => void removeAgent(agent)} onRotateCredentials={(agent) => void rotateAgentCredentials(agent)} onRevokeCredentials={(agent) => void revokeAgentCredentials(agent)} />
      ) : initialAgentID ? (
        <section className="agent-panel">
          <h2>Device not found</h2>
          <p className="empty-state">This device is no longer registered or you no longer have access to it.</p>
          <a className="button secondary" href="/dashboard/agents">Return to all devices</a>
        </section>
      ) : state.agents.length ? (
        <div className="agent-grid">
          {state.agents.map((agent) => (
            <AgentCard agent={agent} key={agent.agent_id} />
          ))}
        </div>
      ) : (
        <p className="empty-state">No agents are registered yet. Choose Add device below to connect your first device.</p>
      )}

      {!initialAgentID && state.agents.length > 0 ? <AgentFleetMap agents={state.agents} /> : null}
 {!initialAgentID ? <FleetAccessPanel agents={state.agents} isAdmin={state.isAdmin} userID={state.userID} /> : null}

	  {!initialAgentID && state.linuxUpdates ? <LinuxUpdatePanel update={state.linuxUpdates} agents={state.agents} isAdmin={state.isAdmin} onPause={(paused) => void pauseLinuxRollout(paused)} onRetry={(id) => void retryLinuxUpdate(id)} onPin={(agentID, version) => void pinLinuxAgent(agentID, version)} onUnpin={(agentID) => void unpinLinuxAgent(agentID)} /> : null}

      {state.isAdmin && !initialAgentID ? (
		<>
		  <AddDeviceSection enrollments={state.enrollments} onCreate={createEnrollment} onRevoke={(enrollment) => void revokeEnrollment(enrollment)} />
		  <details className="agent-panel"><summary>Advanced: manual tokens and credentials</summary><TokenSection tokens={state.tokens} onCreate={createToken} onRevoke={(token) => void revokeToken(token)} /></details>
		</>
      ) : null}
    </section>
  );
}
