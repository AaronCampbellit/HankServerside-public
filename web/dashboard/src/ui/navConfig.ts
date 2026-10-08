import type { NavItem } from "../router";

// NavItem (from router.ts) has no `group`; extend it here so we don't have to
// touch router.ts. Shell renders a group heading when `group` changes.
export type GroupedNavItem = NavItem & { group?: string };

// Curated sidebar: top-level destinations only. Settings sub-tabs are NOT in the
// sidebar — they render in the settings sub-nav (see settings/SettingsLayout.tsx).
export const primaryNav: GroupedNavItem[] = [
  { href: "/dashboard", label: "Home" },
  { href: "/dashboard/hank", label: "Hank", group: "Tools" },
  { href: "/dashboard/profile-notes", label: "Notes", group: "Tools" },
  { href: "/dashboard/home-assistant", label: "Home Assistant", group: "Tools" },
  { href: "/dashboard/file-server", label: "File Server", group: "Tools" },
  { href: "/dashboard/agents", label: "Agents", group: "Tools" },
  { href: "/dashboard/settings", label: "Settings", group: "Settings" },
  { href: "/docs/deployment", label: "Setup Guide", group: "Support" },
];

export type SettingsTab = {
  href: string;
  label: string;
  adminOnly?: boolean;
};

export const settingsTabs: SettingsTab[] = [
  { href: "/dashboard/settings/home", label: "Home & Agent Setup", adminOnly: true },
  { href: "/dashboard/settings/quick-links", label: "Quick Links" },
  { href: "/dashboard/settings/notifications", label: "Notifications" },
  { href: "/dashboard/settings/people", label: "People" },
  { href: "/dashboard/settings/connections", label: "Connections" },
  { href: "/dashboard/settings/ai", label: "AI & MCP" },
  { href: "/dashboard/settings/apps", label: "Apps", adminOnly: true },
  { href: "/dashboard/settings/attachments", label: "Attachments", adminOnly: true },
  { href: "/dashboard/settings/backups", label: "Backups & Storage", adminOnly: true },
  { href: "/dashboard/settings/recovery", label: "Recovery", adminOnly: true },
  { href: "/dashboard/settings/logs", label: "Logs", adminOnly: true },
  { href: "/dashboard/settings/join-home", label: "Join Home" },
];
