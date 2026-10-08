import type { ReactNode } from "react";
import { settingsTabs, type SettingsTab } from "../ui/navConfig";

const settingsGroups = [
  { label: "Home", hrefs: ["/dashboard/settings/home", "/dashboard/settings/quick-links", "/dashboard/settings/notifications", "/dashboard/settings/people", "/dashboard/settings/connections", "/dashboard/settings/ai"] },
  { label: "Admin", hrefs: ["/dashboard/settings/apps", "/dashboard/settings/attachments", "/dashboard/settings/backups", "/dashboard/settings/recovery", "/dashboard/settings/logs"] },
  { label: "Membership", hrefs: ["/dashboard/settings/join-home"] },
];

function guideLabel(tab: SettingsTab): string {
  if (tab.href.endsWith("/home")) return "Home";
  if (tab.href.endsWith("/ai")) return "AI";
  if (tab.href.endsWith("/notifications")) return "Notifications";
  if (tab.href.endsWith("/backups")) return "Backups";
  return tab.label;
}

export function settingsLandingPath(isAdmin: boolean): string {
  return settingsTabs.find((tab) => !tab.adminOnly || isAdmin)?.href || "/dashboard/settings/join-home";
}

function iconFor(tab: SettingsTab): string {
  if (tab.href.endsWith("/home")) return "home";
  if (tab.href.endsWith("/quick-links")) return "link";
  if (tab.href.endsWith("/notifications")) return "notifications";
  if (tab.href.endsWith("/people")) return "people";
  if (tab.href.endsWith("/connections")) return "plug";
  if (tab.href.endsWith("/ai")) return "spark";
  if (tab.href.endsWith("/apps")) return "apps";
  if (tab.href.endsWith("/attachments")) return "attachments";
  if (tab.href.endsWith("/backups")) return "backup";
  if (tab.href.endsWith("/recovery")) return "recovery";
  if (tab.href.endsWith("/logs")) return "logs";
  return "join";
}

function SettingsIcon({ name }: { name: string }) {
  const common = {
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.8,
  };
  return (
    <svg className="settings-tab-icon" viewBox="0 0 24 24" aria-hidden="true">
      {name === "home" ? <><path d="m4 11 8-7 8 7" {...common} /><path d="M6.5 10v9h11v-9" {...common} /><path d="M10 19v-5h4v5" {...common} /></> : null}
      {name === "link" ? <><path d="M9.5 14.5 14.5 9.5" {...common} /><path d="M10.5 7.5 12 6a4 4 0 0 1 5.7 5.6l-1.6 1.6" {...common} /><path d="M13.5 16.5 12 18a4 4 0 0 1-5.7-5.6l1.6-1.6" {...common} /></> : null}
      {name === "notifications" ? <><path d="M6 9a6 6 0 0 1 12 0c0 5 2 6 2 6H4s2-1 2-6" {...common} /><path d="M10 19a2 2 0 0 0 4 0" {...common} /></> : null}
      {name === "people" ? <><circle cx="9" cy="8" r="3" {...common} /><path d="M3.8 19a5.2 5.2 0 0 1 10.4 0" {...common} /><path d="M16 11a2.5 2.5 0 0 0 0-5" {...common} /><path d="M15.5 15a4 4 0 0 1 4.7 4" {...common} /></> : null}
      {name === "plug" ? <><path d="M8 4v5M16 4v5" {...common} /><path d="M7 9h10v4a5 5 0 0 1-10 0z" {...common} /><path d="M12 18v2" {...common} /></> : null}
      {name === "spark" ? <><path d="M12 3l1.5 5.2L19 10l-5.5 1.8L12 17l-1.5-5.2L5 10l5.5-1.8z" {...common} /><path d="M5 16l.7 2.2L8 19l-2.3.8L5 22l-.7-2.2L2 19l2.3-.8z" {...common} /></> : null}
      {name === "apps" ? <><rect x="4" y="4" width="6" height="6" rx="1.5" {...common} /><rect x="14" y="4" width="6" height="6" rx="1.5" {...common} /><rect x="4" y="14" width="6" height="6" rx="1.5" {...common} /><rect x="14" y="14" width="6" height="6" rx="1.5" {...common} /></> : null}
      {name === "attachments" ? <><path d="M8.5 12.5 14 7a3 3 0 0 1 4.2 4.2l-7.4 7.4a5 5 0 0 1-7.1-7.1l7.1-7.1" {...common} /><path d="m7 15 7-7a1.5 1.5 0 0 1 2.1 2.1l-6.7 6.7a3 3 0 0 1-4.2-4.2L12 5.8" {...common} /></> : null}
      {name === "backup" ? <><path d="M6 8a7 7 0 1 1 .8 9.2" {...common} /><path d="M6 13v4H2" {...common} /><path d="M12 8v5l3 2" {...common} /></> : null}
      {name === "recovery" ? <><path d="M5 12a7 7 0 0 1 12-5" {...common} /><path d="M17 4v4h-4" {...common} /><path d="M19 12a7 7 0 0 1-12 5" {...common} /><path d="M7 20v-4h4" {...common} /></> : null}
      {name === "logs" ? <><path d="M7 4h10a2 2 0 0 1 2 2v14H7a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z" {...common} /><path d="M9 8h6M9 12h6M9 16h4" {...common} /></> : null}
      {name === "join" ? <><path d="M12 5v14M5 12h14" {...common} /><circle cx="12" cy="12" r="8" {...common} /></> : null}
    </svg>
  );
}

/** Wraps every /dashboard/settings/* page with the redesign's settings sub-nav.
 *  Admin-only tabs are hidden for non-admins (matches the old sidebar gating). */
export function SettingsLayout({
  currentPath,
  isAdmin,
  onPrefetch,
  children,
}: {
  currentPath: string;
  isAdmin: boolean;
  onPrefetch?: (href: string) => void;
  children: ReactNode;
}) {
  const normalizedCurrentPath = currentPath === "/dashboard/settings" ? settingsLandingPath(isAdmin) : currentPath;
  const visibleTabs = settingsTabs.filter((tab) => !tab.adminOnly || isAdmin);
  const activeTab = visibleTabs.find((tab) => tab.href === normalizedCurrentPath) || visibleTabs[0];
  const tabsByHref = new Map(visibleTabs.map((tab) => [tab.href, tab]));

  return (
    <div className="settings-layout">
      <details className="settings-mobile-chooser" aria-label="Mobile settings section">
        <summary>
          <span>Settings</span>
          <strong>{activeTab ? guideLabel(activeTab) : "Home"}</strong>
        </summary>
        <nav aria-label="Mobile settings destinations">
          {visibleTabs.map((tab) => {
            const label = guideLabel(tab);
            const accessibleLabel = tab.adminOnly ? `${label} ADMIN` : label;
            return (
              <a
                key={tab.href}
                href={tab.href}
                aria-current={tab.href === normalizedCurrentPath ? "page" : undefined}
                aria-label={accessibleLabel}
                onFocus={() => onPrefetch?.(tab.href)}
                onTouchStart={() => onPrefetch?.(tab.href)}
              >
                <SettingsIcon name={iconFor(tab)} />
                <span>{label}</span>
                {tab.adminOnly ? <span className="admin-badge" aria-hidden="true">ADMIN</span> : null}
              </a>
            );
          })}
        </nav>
      </details>
      <nav className="settings-subnav" aria-label="Settings sections">
        <h2 className="settings-subnav-title">Settings</h2>
        {settingsGroups.map((group) => {
          const tabs = group.hrefs.map((href) => tabsByHref.get(href)).filter((tab): tab is SettingsTab => Boolean(tab));
          if (!tabs.length) return null;
          return (
            <section className="settings-subnav-group" key={group.label} aria-label={`${group.label} settings`}>
              <p>{group.label}</p>
              <div>
                {tabs.map((tab) => {
                  const label = guideLabel(tab);
                  const accessibleLabel = tab.adminOnly ? `${label} ADMIN` : label;
                  return (
                    <a
                      key={tab.href}
                      href={tab.href}
                      aria-current={tab.href === normalizedCurrentPath ? "page" : undefined}
                      aria-label={accessibleLabel}
                      onFocus={() => onPrefetch?.(tab.href)}
                      onMouseEnter={() => onPrefetch?.(tab.href)}
                      onTouchStart={() => onPrefetch?.(tab.href)}
                    >
                      <SettingsIcon name={iconFor(tab)} />
                      <span>{label}</span>
                      {tab.adminOnly ? <span className="admin-badge" aria-hidden="true">ADMIN</span> : null}
                    </a>
                  );
                })}
              </div>
            </section>
          );
        })}
      </nav>
      <div className="settings-content">{children}</div>
    </div>
  );
}
