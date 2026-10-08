import { MonitoringHealthChecklist } from "./MonitoringHealthChecklist";
import { useEffect, useState, type FormEvent } from "react";
import { monitoringClient, type MonitoringSettings, type MonitoringStatus } from "../api/monitoring";

export function MonitoringDeliverySettings() {
  const [status, setStatus] = useState<MonitoringStatus | null>(null);
  const [form, setForm] = useState<MonitoringSettings | null>(null);
  const [password, setPassword] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void monitoringClient.get(controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      setStatus(value); setForm(value.settings); setDirty(false); setPassword(""); setClearPassword(false); setMessage("");
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Could not load alert delivery settings.");
    });
    return () => controller.abort();
  }, [refresh]);

  function update<K extends keyof MonitoringSettings>(key: K, value: MonitoringSettings[K]) {
    setForm((previous) => previous ? { ...previous, [key]: value } : previous);
    setDirty(true); setMessage("");
  }
  async function save(event: FormEvent) {
    event.preventDefault(); if (!form) return;
    setBusy(true); setError(""); setMessage("");
    try {
      const value = await monitoringClient.save(form, password, clearPassword);
      setStatus(value); setForm(value.settings); setPassword(""); setClearPassword(false); setDirty(false);
      setMessage(value.delivery_status === "ready" ? "Alert delivery settings saved and applied." : "Settings saved. Delivery is waiting for the monitoring service; Hank will retry automatically.");
    } catch (err) { setError(err instanceof Error ? err.message : "Could not save alert delivery settings."); }
    finally { setBusy(false); }
  }
  async function test() {
    setBusy(true); setError(""); setMessage("");
    try {
      await monitoringClient.test();
      setMessage("Test alert queued. Check your enabled inbox and email destinations in about 30 seconds; a recovery notice follows. Queued does not confirm mailbox delivery.");
    } catch (err) { setError(err instanceof Error ? err.message : "Could not queue a test alert."); }
    finally { setBusy(false); }
  }

  return <><MonitoringHealthChecklist /><section className="settings-panel monitoring-delivery" aria-labelledby="monitoring-delivery-title">
    <div className="panel-heading"><div><h2 id="monitoring-delivery-title">Server alert delivery</h2><p>Configure alerts for this Home. Only administrators can change these settings.</p></div></div>
    {error ? <p className="error-state" role="alert">{error}</p> : null}
    {message ? <p role="status">{message}</p> : status?.delivery_status === "pending" ? <p role="status">Waiting to apply delivery settings. Hank retries automatically.</p> : null}
    {!form ? <><p>{error ? "Alert settings could not be loaded." : "Loading alert delivery settings…"}</p>{error ? <button type="button" onClick={() => setRefresh((value) => value + 1)}>Retry alert settings</button> : null}</> : <form onSubmit={(event) => void save(event)}>
      <fieldset disabled={busy}>
        <legend>Hank inbox</legend>
        <label className="monitoring-toggle"><input type="checkbox" checked={form.inbox_enabled} onChange={(event) => update("inbox_enabled", event.target.checked)} /> Add server alerts to the Hank inbox</label>
        <label>Inbox recipients<select value={form.inbox_audience} onChange={(event) => update("inbox_audience", event.target.value as MonitoringSettings["inbox_audience"])}>
          <option value="admins">Home administrators</option><option value="members">All Home members</option>
        </select></label>
        <p className="meta-line">Changes affect future alerts. Existing inbox history is retained.</p>
      </fieldset>
      <fieldset disabled={busy}>
        <legend>Email</legend>
        <label className="monitoring-toggle"><input type="checkbox" checked={form.email_enabled} onChange={(event) => update("email_enabled", event.target.checked)} /> Send server alerts by email</label>
        <p className="meta-line">Email continues through the monitoring service if Hank becomes unavailable. Use a public SMTP server with STARTTLS, usually port 587.</p>
        <div className="monitoring-fields">
          <label>Recipient email<input type="email" autoComplete="off" value={form.email_to} required={form.email_enabled} onChange={(event) => update("email_to", event.target.value)} /></label>
          <label>Sender email<input type="email" autoComplete="off" value={form.email_from} required={form.email_enabled} onChange={(event) => update("email_from", event.target.value)} /></label>
          <label>SMTP hostname<input autoComplete="off" placeholder="smtp.example.com" value={form.smtp_host} required={form.email_enabled} onChange={(event) => update("smtp_host", event.target.value)} /></label>
          <label>SMTP port<input type="number" min="1" max="65535" value={form.smtp_port} required onChange={(event) => update("smtp_port", Number(event.target.value))} /></label>
          <label>SMTP username<input autoComplete="off" value={form.smtp_username} onChange={(event) => update("smtp_username", event.target.value)} /></label>
          <label>SMTP password<input type="password" autoComplete="new-password" value={password} placeholder={status?.smtp_password_set ? "Saved — leave blank to keep" : "Enter an SMTP or app password"} onChange={(event) => { setPassword(event.target.value); setDirty(true); setClearPassword(false); }} /></label>
        </div>
        <p className="meta-line">Passwords are stored encrypted and never displayed again. Your provider may require an app password.</p>
        {status?.smtp_password_set ? <label className="monitoring-toggle"><input type="checkbox" checked={clearPassword} onChange={(event) => { setClearPassword(event.target.checked); setPassword(""); setDirty(true); }} /> Remove saved SMTP password</label> : null}
      </fieldset>
      <div className="button-row">
        <button type="submit" disabled={busy || !dirty}>{busy ? "Working…" : "Save alert settings"}</button>
        <button type="button" className="secondary" disabled={busy || dirty || (!form.inbox_enabled && !form.email_enabled)} onClick={() => void test()}>Send test alert</button>
        {status?.delivery_status === "pending" ? <button type="button" className="secondary" disabled={busy || dirty} onClick={() => setRefresh((value) => value + 1)}>Check delivery status</button> : null}
      </div>
      {dirty ? <p className="meta-line">Save your changes before sending a test.</p> : null}
    </form>}
  </section></>;
}
