import { useEffect, useState } from "react";
import { monitoringClient, type MonitoringHealth } from "../api/monitoring";

export function MonitoringHealthChecklist({ compact = false }: { compact?: boolean }) {
  const [health, setHealth] = useState<MonitoringHealth | null>(null);
  const [error, setError] = useState(false);
  const [busy, setBusy] = useState(true);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true); setError(false);
    void monitoringClient.health(controller.signal).then((value) => {
      if (!controller.signal.aborted) setHealth(value);
    }).catch(() => { if (!controller.signal.aborted) { setHealth(null); setError(true); } })
      .finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => controller.abort();
  }, [refresh]);
  if (compact) {
    const status = busy ? "Checking…" : error ? "Could not verify" : health?.ready ? "Checks passing" : "Needs attention";
    return <a className="home-metric home-monitoring-metric" href="/dashboard/settings/notifications#monitoring-delivery-title" aria-label="Configure server alerts">
      <span>Monitoring</span>
      <strong role="status">{status}</strong>
      <small>Server checks · Manage alerts</small>
    </a>;
  }
  return <section className="settings-panel" aria-label="Monitoring checks">
    <h2>Monitoring checks</h2>
    <p><strong>Server monitoring: </strong>{busy ? "Checking…" : error ? "Could not verify" : health?.ready ? "Checks passing" : "Needs attention"}</p>
    {health ? <ul>{health.checks.map((check) => <li key={check.id}>{check.ready ? "✓" : "!"} {check.label}: {check.ready ? "Ready" : "Needs attention"}</li>)}</ul> : null}
    {!busy && !error && !health?.ready ? <p>Monitoring is not fully working. A server administrator may need to finish its installation or repair a service.</p> : null}
    <p>Configure inbox and email alerts in Notifications. Send a test alert and check its arrival; passing service checks do not confirm delivery.</p>
    <div className="button-row">
      <button type="button" className="secondary" disabled={busy} onClick={() => setRefresh((value) => value + 1)}>Recheck monitoring</button>
    </div>
  </section>;
}
