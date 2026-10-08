const settingsSections = [
  { href: "/dashboard/settings/home", label: "Home & Agent Setup", detail: "Home name, primary-agent setup, and enrollment tokens." },
  { href: "/dashboard/settings/quick-links", label: "Quick Links", detail: "Dashboard shortcuts and status checks." },
  { href: "/dashboard/settings/people", label: "People", detail: "Members, invitations, and password resets." },
  { href: "/dashboard/settings/connections", label: "Connections", detail: "Home Assistant and file server profiles." },
  { href: "/dashboard/settings/ai", label: "AI & MCP", detail: "HankAI sources, provider settings, and MCP connectors." },
  { href: "/dashboard/settings/apps", label: "Apps", detail: "Installable Hank apps and app configuration." },
  { href: "/dashboard/settings/backups", label: "Backups & Storage", detail: "Backup schedules, retention, restore tests, and storage health." },
  { href: "/dashboard/settings/recovery", label: "Recovery", detail: "Redacted settings export and import." },
  { href: "/dashboard/settings/logs", label: "Logs", detail: "Audit events and operational log filters." },
  { href: "/dashboard/settings/join-home", label: "Join Home", detail: "Accept an invitation to another home." },
];

export function SettingsIndex() {
  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Settings</h1>
        </div>
      </header>

      <section className="settings-panel" aria-label="Settings sections">
        <div className="dashboard-grid">
          {settingsSections.map((section) => (
            <a aria-label={section.label} className="dashboard-tile" href={section.href} key={section.href}>
              <span>Settings</span>
              <strong>{section.label}</strong>
              <small>{section.detail}</small>
            </a>
          ))}
        </div>
      </section>
    </section>
  );
}
