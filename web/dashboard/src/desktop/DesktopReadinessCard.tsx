import type { DesktopAgentReadiness } from "../api/desktopAudit";

const readinessChecks = ["service", "daemon", "host", "indicator", "capture", "control"] as const;

const checkCopy: Record<(typeof readinessChecks)[number], { label: string; blocked: string }> = {
  service: { label: "Remote Desktop service", blocked: "Install or start the Remote Desktop service on this device." },
  daemon: { label: "Background session service", blocked: "Start HankAgent in this Mac user session, then wait for the device to report again." },
  host: { label: "Interactive desktop host", blocked: "Sign in locally on the device and make sure the Hank desktop host is running." },
  indicator: { label: "Local access indicator", blocked: "Allow the Remote Desktop indicator to run locally; sessions stay blocked without it." },
  capture: { label: "Screen sharing", blocked: "Allow screen recording locally. On macOS, grant Hank Screen Recording permission in System Settings." },
  control: { label: "Keyboard and mouse control", blocked: "Allow Accessibility/control locally if you want more than view-only access." },
};

export function desktopReadinessComplete(readiness: DesktopAgentReadiness | null): boolean {
  return Boolean(readiness?.online && readiness.trusted && !readiness.active_session && readinessChecks.every(check => readiness.checks[check] === "ready"));
}

function statusText(value: string | undefined): string {
  if (value === "ready") return "Ready";
  if (value === "required") return "Needs local permission";
  if (value === "unavailable") return "Unavailable";
  return "Waiting for status";
}

export function DesktopReadinessCard({ readiness }: { readiness: DesktopAgentReadiness | null }) {
  const checks = readiness?.checks ?? {};
  const complete = desktopReadinessComplete(readiness);
  return <section id="remote-desktop-setup" className="agent-info-card desktop-readiness-card" aria-label="Remote Desktop readiness">
    <div className="desktop-readiness-heading"><div><h3>Set up Remote Desktop</h3><p>Complete these checks before opening a session. Hank never bypasses missing trust or local permissions.</p></div><span className={`status-pill ${complete ? "status-online" : ""}`}>{complete ? "Ready" : "Setup needed"}</span></div>
    {!complete ? <a className="button secondary desktop-setup-button" href="#remote-desktop-settings">Set up Remote Desktop</a> : null}
    {!readiness ? <p className="agent-hint">We cannot read this device’s setup status yet. Make sure the agent is online, then refresh this page.</p> : <ol className="desktop-setup-list">
      <li data-ready={readiness.online}><strong>1. Keep this device online</strong><span>{readiness.online ? "Connected to Hank." : "The agent is offline. Start the agent and wait for it to reconnect."}</span></li>
      <li data-ready={readiness.trusted}><strong>2. Approve this device</strong><span>{readiness.trusted ? "This device is approved." : <>Use the Remote Desktop setup below to approve this device, then return here.</>}</span></li>
      {readinessChecks.map((check, index) => <li key={check} data-ready={checks[check] === "ready"}><strong>{index + 3}. {checkCopy[check].label}</strong><span>{checks[check] === "ready" ? "Ready." : `${statusText(checks[check])}. ${checkCopy[check].blocked}`}</span></li>)}
      <li data-ready={!readiness.active_session}><strong>9. Start when no one else is connected</strong><span>{readiness.active_session ? "Another operator is using this device. End that session first." : "No active Remote Desktop session."}</span></li>
    </ol>}
    <p className="agent-hint">Setup status is reported by the device. Last report: {readiness?.reported_at ? new Date(readiness.reported_at).toLocaleString() : "not received yet"}.</p>
  </section>;
}
