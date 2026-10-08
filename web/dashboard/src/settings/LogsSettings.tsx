import { type FormEvent, useEffect, useState } from "react";
import { logsClient, type AuditEvent } from "../api/logs";
import { notificationsClient } from "../api/notifications";

type Filters = {
  eventType: string;
  severity: string;
  targetType: string;
  sort: string;
  order: string;
  limit: string;
};

type State =
  | { status: "loading"; filters: Filters }
  | { status: "error"; filters: Filters; message: string }
  | { status: "ready"; filters: Filters; events: AuditEvent[]; message: string };

const defaultFilters: Filters = {
  eventType: "",
  severity: "",
  targetType: "",
  sort: "occurred_at",
  order: "desc",
  limit: "100",
};

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Logs could not be loaded.";
}

function formatDate(value?: string): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

function queryFilters(filters: Filters) {
  const storageCategory = filters.eventType === "storage";
  return {
    event_type: storageCategory ? "" : filters.eventType.trim(),
    severity: filters.severity,
    target_type: storageCategory ? "storage" : filters.targetType.trim(),
    sort: filters.sort,
    order: filters.order,
    limit: filters.limit,
  };
}

export function LogsSettings() {
  const notificationID = new URLSearchParams(window.location.search).get("notification")?.trim() || "";
  const [state, setState] = useState<State>({ status: "loading", filters: defaultFilters });

  async function load(filters = state.filters, message = "") {
    try {
      const payload = await logsClient.listAuditEvents(queryFilters(filters));
      setState({ status: "ready", filters, events: payload.events || [], message });
    } catch (error) {
      setState({ status: "error", filters, message: errorMessage(error) });
    }
  }

  useEffect(() => {
    if (notificationID) {
      void notificationsClient.getEvent(notificationID)
        .then((event) => setState({ status: "ready", filters: defaultFilters, events: [event], message: "" }))
        .catch((error) => setState({ status: "error", filters: defaultFilters, message: errorMessage(error) }));
      return;
    }
    void load(defaultFilters);
  }, []);

  function setFilters(next: Partial<Filters>) {
    setState((current) => ({ ...current, filters: { ...current.filters, ...next } }) as State);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await load(state.filters);
  }

  const events = state.status === "ready" ? state.events : [];
  const compactLogLayout = typeof window.matchMedia === "function" && window.matchMedia("(max-width: 760px)").matches;

  return (
    <section className="settings-page logs-settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">{notificationID ? "Log event" : "Logs"}</h1>
          <p className="meta-line">{notificationID ? "Full details for this notification event." : "Login, resource access, transfer, storage, package, and security events."}</p>
        </div>
        <span className="status-pill">{state.status === "ready" ? `${events.length} event${events.length === 1 ? "" : "s"}` : state.status === "error" ? "Unavailable" : "Loading"}</span>
      </header>

      {state.status === "error" ? <p className="error-state">{state.message}</p> : null}
      {state.status === "ready" && state.message ? <p className="notice-state">{state.message}</p> : null}

      {!notificationID ? <section className="settings-panel" aria-label="Audit logs">
        <h2>Audit trail</h2>
        <form className="quick-link-form" onSubmit={submit}>
          <label>
            <span>Event</span>
            <select onChange={(event) => setFilters({ eventType: event.target.value })} value={state.filters.eventType}>
              <option value="">All events</option>
              <option value="login.succeeded">Login succeeded</option>
              <option value="login.failed">Login failed</option>
              <option value="session.created">Session created</option>
              <option value="session.revoked">Session revoked</option>
              <option value="password.changed">Password changed</option>
              <option value="password.reset">Password reset</option>
              <option value="invitation.created">Invite created</option>
              <option value="invitation.accepted">Invite accepted</option>
              <option value="invitation.signup">Invite signup</option>
              <option value="invitation.cancelled">Invite cancelled</option>
              <option value="file_operation.denied">File operation</option>
              <option value="file_operation.requested">File operation requested</option>
              <option value="file_operation.completed">File operation completed</option>
              <option value="file_operation.failed">File operation failed</option>
              <option value="file_transfer.requested">File transfer requested</option>
              <option value="file_transfer.setup_failed">File transfer setup failed</option>
              <option value="file_transfer.failed">File transfer failed</option>
              <option value="agent.restart_requested">Agent restart</option>
              <option value="agent_token.created">Agent token</option>
              <option value="agent_token.revoked">Agent token revoked</option>
              <option value="service_profile.changed">Service profile</option>
              <option value="permission.changed">Permission changed</option>
              <option value="homeassistant.control.succeeded">Home Assistant control succeeded</option>
              <option value="homeassistant.control.failed">Home Assistant control failed</option>
              <option value="storage">Storage</option>
              <option value="app_package.previewed">App package previewed</option>
              <option value="app_package.preview_failed">App package preview failed</option>
              <option value="app_package.activated">App package activated</option>
              <option value="app_package.activate_failed">App package activation failed</option>
              <option value="app.uninstalled">App uninstalled</option>
              <option value="app.uninstall_failed">App uninstall failed</option>
              <option value="notes_api_token.created">Notes API token created</option>
              <option value="notes_api_token.revoked">Notes API token revoked</option>
              <option value="notes_api_token.denied">Notes API token denied</option>
            </select>
          </label>
          <label>
            <span>Severity</span>
            <select onChange={(event) => setFilters({ severity: event.target.value })} value={state.filters.severity}>
              <option value="">All severities</option>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </select>
          </label>
          <label>
            <span>Target</span>
            <select onChange={(event) => setFilters({ targetType: event.target.value })} value={state.filters.targetType}>
              <option value="">All targets</option>
              <option value="session">Login</option>
              <option value="user">User</option>
              <option value="email">Email</option>
              <option value="invitation">Invitation</option>
              <option value="file_policy">File operation</option>
              <option value="file_transfer">File transfer</option>
              <option value="file_operation_job">File job</option>
              <option value="agent">Agent</option>
              <option value="agent_token">Agent token</option>
              <option value="service_profile">Service profile</option>
              <option value="home_permissions">Home permissions</option>
              <option value="home_member_permissions">Member permissions</option>
              <option value="homeassistant_entity">Home Assistant entity</option>
              <option value="storage">Storage</option>
              <option value="app_package">App package</option>
              <option value="app">App</option>
              <option value="notes_api_token">Notes API token</option>
            </select>
          </label>
          <label>
            <span>Sort By</span>
            <select onChange={(event) => setFilters({ sort: event.target.value })} value={state.filters.sort}>
              <option value="occurred_at">Time</option>
              <option value="event_type">Event</option>
              <option value="severity">Severity</option>
              <option value="target_type">Target</option>
            </select>
          </label>
          <label>
            <span>Order</span>
            <select onChange={(event) => setFilters({ order: event.target.value })} value={state.filters.order}>
              <option value="desc">Newest first</option>
              <option value="asc">Oldest first</option>
            </select>
          </label>
          <label>
            <span>Limit</span>
            <input min={1} max={200} onChange={(event) => setFilters({ limit: event.target.value })} type="number" value={state.filters.limit} />
          </label>
          <button aria-label="Refresh" type="submit">Apply filters</button>
        </form>
      </section> : null}

      <section className="settings-panel logs-events-panel" aria-label="Audit events">
        <div className="card-list">
          {state.status === "loading" ? <p className="loading-state">Loading logs...</p> : null}
          {state.status === "ready" && !events.length ? <p className="empty-state">No logs match these filters.</p> : null}
          {events.map((event, index) => (
            <article className="dashboard-tile logs-event-card" key={event.id || `${event.event_type}-${event.occurred_at}-${index}`}>
              <span className="logs-event-severity">{event.severity || "info"}</span>
              <strong className="logs-event-title">{event.event_type || "audit event"}</strong>
              <small className="logs-event-meta">{formatDate(event.occurred_at)} · {event.target_type || "system"}</small>
              <p className="logs-event-helper">{event.helper_text || "Review metadata for context."}</p>
              <details className="logs-event-details" open={Boolean(notificationID) || !compactLogLayout}>
                <summary>Details</summary>
                <dl className="kv-list">
                  <div><strong>Actor</strong><span>{event.actor_label || event.actor_user_id || event.actor_agent_id || "System"}</span></div>
                  <div><strong>Target</strong><span>{event.target_label || event.target_id || "none"}</span></div>
                  <div><strong>Request</strong><span>{event.request_id || "none"}</span></div>
                  {Object.entries(event.metadata || {}).map(([key, value]) => (
                    <div key={key}><strong>{key}</strong><span>{typeof value === "object" ? JSON.stringify(value) : String(value)}</span></div>
                  ))}
                </dl>
              </details>
            </article>
          ))}
        </div>
      </section>
    </section>
  );
}
