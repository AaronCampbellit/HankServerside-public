import { MonitoringDeliverySettings } from "./MonitoringDeliverySettings";
import type { NotificationCategory } from "../api/notifications";
import { useNotifications, type NotificationDeliveryState } from "../notifications/NotificationProvider";

const categories: Array<{ key: NotificationCategory; label: string; detail: string }> = [
  { key: "monitoring", label: "Server monitoring", detail: "Infrastructure warnings and recovery notices." },
  { key: "agent_health", label: "Agent health", detail: "Offline, recovered, and low-disk alerts from your primary Hank Agent." },
  { key: "quick_links", label: "Quick links", detail: "Availability changes for monitored dashboard links." },
  { key: "storage", label: "Storage", detail: "Storage warnings and recovery events." },
  { key: "notes", label: "Shared notes", detail: "Changes made by collaborators to shared notes." },
  { key: "dashboard_entities", label: "Home Assistant", detail: "Alerts for the dashboard entities you follow." },
];

function deliveryCopy(state: NotificationDeliveryState): string {
  switch (state) {
    case "subscribed": return "Notifications are enabled on this device.";
    case "available": return "Notifications are available but not enabled on this device.";
    case "denied": return "Browser notification permission is blocked. Allow it in browser settings, then retry.";
    case "install-required": return "On iPhone and iPad, add Hank to the Home Screen before enabling notifications.";
    case "disabled-server": return "Web Push is not configured on this Hank server.";
    case "unsupported": return "This browser does not support Web Push for Hank.";
    case "error": return "The notification state could not be verified.";
    default: return "Checking this device…";
  }
}

export function NotificationSettingsPage({ isAdmin = false }: { isAdmin?: boolean }) {
  const notifications = useNotifications();
  const ready = notifications.status === "ready" || notifications.status === "error";

  return (
    <section className="settings-page notification-settings" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Notifications</h1>
          <p className="meta-line">Choose browser delivery for this device and push categories for your account.</p>
        </div>
      </header>

      {isAdmin ? <MonitoringDeliverySettings /> : null}

      <section className="settings-panel" aria-labelledby="device-delivery-title">
        <div className="panel-heading">
          <div><h2 id="device-delivery-title">This device</h2><p>{deliveryCopy(notifications.deliveryState)}</p></div>
          <span className={`status-pill ${notifications.deliveryState === "subscribed" ? "ok" : ""}`}>{notifications.deliveryState.replace("-", " ")}</span>
        </div>
        {notifications.error ? <p className="error-state" role="alert">{notifications.error}</p> : null}
        <div className="button-row">
          {["available", "denied", "error"].includes(notifications.deliveryState) ? (
            <button type="button" onClick={() => void notifications.enable()}>Enable notifications</button>
          ) : null}
          {notifications.deliveryState === "subscribed" ? (
            <button type="button" className="secondary" onClick={() => void notifications.disable()}>Disable on this device</button>
          ) : null}
          {notifications.deliveryState === "error" ? (
            <button type="button" className="secondary" onClick={() => void notifications.refresh()}>Retry</button>
          ) : null}
        </div>
      </section>

      <section className="settings-panel" aria-labelledby="push-categories-title">
        <div className="panel-heading"><div><h2 id="push-categories-title">Push categories</h2><p>These settings apply to every enrolled browser for your account.</p></div></div>
        <p className="notification-retention-note">Turning a category off stops push delivery. Its inbox entries are still retained so you can review them later.</p>
        {!ready || !notifications.settings ? <p className="loading-state">Loading notification preferences…</p> : (
          <div className="notification-category-list">
            {categories.map((category) => (
              <label className="notification-category-row" key={category.key}>
                <span><strong>{category.label}</strong><small>{category.detail}</small></span>
                <input
                  aria-label={category.label}
                  checked={notifications.settings?.[category.key] ?? true}
                  onChange={(event) => void notifications.updateCategory(category.key, event.target.checked)}
                  type="checkbox"
                />
              </label>
            ))}
          </div>
        )}
      </section>
    </section>
  );
}
