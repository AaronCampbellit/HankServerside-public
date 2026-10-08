import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { redirectTo } from "./browser/navigation";
import type { OfflineNotesContextValue } from "./offlineNotes/OfflineNotesProvider";

const homeAssistantClient = vi.hoisted(() => ({
  load: vi.fn(),
  callService: vi.fn(),
  fetchState: vi.fn(),
  onStateChanged: vi.fn(),
}));

const searchClient = vi.hoisted(() => ({
  search: vi.fn(),
}));

const offlineNotesRuntime = vi.hoisted(() => ({
  current: null as OfflineNotesContextValue | null,
}));

vi.mock("./browser/navigation", () => ({
  redirectTo: vi.fn(),
}));

vi.mock("./api/search", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api/search")>()),
  searchClient,
}));

vi.mock("./api/homeAssistant", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api/homeAssistant")>();
  return {
    ...actual,
    homeAssistantClient,
  };
});

vi.mock("./offlineNotes/OfflineNotesProvider", () => ({
  useOfflineNotes: () => offlineNotesRuntime.current,
}));

vi.mock("./pwa/PWAProvider", () => ({
  usePWA: () => ({
    online: true,
    installMode: "none",
    install: vi.fn(),
    updateAvailable: false,
    updatePending: false,
    updateFailed: false,
    applyUpdate: vi.fn(),
    dismissUpdate: vi.fn(),
  }),
}));

function offlineRuntime(overrides: Partial<OfflineNotesContextValue> = {}): OfflineNotesContextValue {
  return {
    mode: "inactive",
    activeUserID: "",
    repository: null,
    sync: {
      pending: 0,
      syncing: 0,
      failed: 0,
      conflicted: 0,
      authenticationRequired: false,
      lastSyncedAt: 0,
    },
    activateAuthenticated: vi.fn().mockResolvedValue(undefined),
    activateOffline: vi.fn().mockResolvedValue(null),
    syncNow: vi.fn().mockResolvedValue(undefined),
    purgeActive: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

describe("App routes", () => {
  beforeEach(() => {
    offlineNotesRuntime.current = offlineRuntime();
    homeAssistantClient.load.mockResolvedValue({
      agent: null,
      profile: { revision: 0, settings: { dashboard_tiles: [] } },
      dashboardEntityIDs: [],
      states: [],
    });
    homeAssistantClient.callService.mockResolvedValue({});
    homeAssistantClient.fetchState.mockResolvedValue(null);
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});
    searchClient.search.mockResolvedValue({ results: [] });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it.each([
    ["/", "Sign in to Hank"],
    ["/join", "Join Home"],
    ["/password-change", "Change Password"],
    ["/dashboard", "Dashboard"],
    ["/dashboard/hank", "HankAI"],
    ["/dashboard/home-assistant", "Home Assistant"],
    ["/dashboard/profile-notes", "Loading notes"],
    ["/dashboard/file-server", "Loading files"],
    ["/dashboard/settings", "Settings"],
    ["/dashboard/settings/home", "Home & Agent Setup"],
    ["/dashboard/settings/quick-links", "Quick Links"],
    ["/dashboard/settings/notifications", "Notifications"],
    ["/dashboard/settings/people", "People"],
    ["/dashboard/settings/connections", "Connections"],
    ["/dashboard/settings/ai", "AI & MCP"],
    ["/dashboard/settings/apps", "Apps"],
    ["/dashboard/settings/attachments", "Attachments"],
    ["/dashboard/settings/backups", "Backups & Storage"],
    ["/dashboard/settings/recovery", "Recovery"],
    ["/dashboard/settings/logs", "Logs"],
    ["/dashboard/settings/join-home", "Join Home"],
    ["/docs/deployment", "Setup Guide"],
  ])("renders %s", (path, heading) => {
    window.history.pushState({}, "", path);
    render(<App />);
    expect(screen.getByRole("heading", { name: heading, level: 1 })).toBeInTheDocument();
  });

  it("keeps visited dashboard tabs mounted so returning does not reload the page", async () => {
    window.history.pushState({}, "", "/dashboard/profile-notes");
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      calls.push(path);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_1", email: "owner@example.com" },
          home: { id: "home_1", name: "Campbell Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true, can_manage_settings: true },
          agent: { agent_id: "agent_1", name: "Agent", status: "online" },
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes") {
        return new Response(JSON.stringify({
          notes: [{ note_id: "daily", title: "Daily", preview: "Remember milk", page_type: "text" }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/daily") {
        return new Response(JSON.stringify({
          note_id: "daily",
          title: "Daily",
          body_markdown: "# Daily\nRemember milk",
          revision: "1",
          page_type: "text",
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByDisplayValue("Daily")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Setup Guide" }));
    expect(await screen.findByRole("heading", { name: "Setup Guide" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Loading notes" })).not.toBeInTheDocument();

    fireEvent.click(within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Notes" }));
    expect(screen.getByDisplayValue("Daily")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Loading notes" })).not.toBeInTheDocument();
    expect(calls.filter((path) => path === "/v1/me/notes")).toHaveLength(1);
    expect(calls.filter((path) => path === "/v1/me/notes/daily")).toHaveLength(1);
  });

  it("opens a global-search result inside an already mounted Notes tab", async () => {
    window.history.pushState({}, "", "/dashboard/profile-notes");
    searchClient.search.mockResolvedValue({ results: [{
      type: "note",
      title: "Roof Warranty",
      subtitle: "Expires 2027",
      url: "/dashboard/profile-notes?note=roof",
    }] });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_1", email: "owner@example.com" },
          home: { id: "home_1", name: "Campbell Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true, can_manage_settings: true },
          agent: { agent_id: "agent_1", name: "Agent", status: "online" },
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes") {
        return new Response(JSON.stringify({
          notes: [
            { note_id: "daily", title: "Daily", preview: "Remember milk", page_type: "text", updated_at: "2026-08-04T12:00:00Z" },
            { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", updated_at: "2026-08-03T12:00:00Z" },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/daily") {
        return new Response(JSON.stringify({
          note_id: "daily",
          title: "Daily",
          body_markdown: "Remember milk",
          revision: "1",
          page_type: "text",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/roof") {
        return new Response(JSON.stringify({
          note_id: "roof",
          title: "Roof Warranty",
          body_markdown: "Expires in 2027.",
          revision: "1",
          page_type: "text",
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByDisplayValue("Daily")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), { target: { value: "Roof Warranty" } });
    fireEvent.mouseDown(await screen.findByRole("option", { name: /Roof Warranty/i }));

    expect(window.location.search).toBe("?note=roof");
    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
  });

  it("lands members on a permitted settings page from the generic Settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_member", email: "member@example.com" },
          home: { id: "home_1", name: "Campbell Home" },
          membership: { role: "member" },
          permissions: { is_admin: false, can_manage_settings: false },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: false, links: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await waitFor(() => expect(screen.getByRole("heading", { name: "Quick Links" })).toBeInTheDocument());
    expect(screen.queryByRole("heading", { name: "Home & Agent Setup" })).not.toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Settings sections" })).getByRole("link", { name: "Quick Links" })).toHaveAttribute("aria-current", "page");
  });

  it("submits login and register from the public auth route", async () => {
    window.history.pushState({}, "", "/");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ path, method, body });
      if (path === "/v1/me") return new Response("unauthorized", { status: 401 });
      if (path === "/v1/auth/login") {
        return new Response(JSON.stringify({ user: { email: "owner@example.com" } }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/auth/register") {
        return new Response(JSON.stringify({ user: { email: "new@example.com", password_change_required: true } }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await screen.findByRole("heading", { name: "Sign in to Hank" });
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "owner@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/auth/login",
      method: "POST",
      body: { email: "owner@example.com", password: "secret" },
    }));
    expect(redirectTo).toHaveBeenCalledWith("/dashboard");

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "new@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/auth/register",
      method: "POST",
      body: { email: "new@example.com", password: "secret" },
    }));
    expect(redirectTo).toHaveBeenCalledWith("/password-change");
  });

  it("previews and accepts a public invite signup", async () => {
    window.history.pushState({}, "", "/join?token=invite-token");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ path, method, body });
      if (path === "/v1/auth/invitations/preview") {
        return new Response(JSON.stringify({ email: "member@example.com", role: "member" }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/auth/invitations/signup") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("member@example.com can join as member.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Join home" }));

    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/auth/invitations/signup",
      method: "POST",
      body: { token: "invite-token", email: "member@example.com", password: "secret" },
    }));
    expect(redirectTo).toHaveBeenCalledWith("/dashboard");
  });

  it("changes a required password before entering the dashboard", async () => {
    window.history.pushState({}, "", "/password-change");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ path, method, body });
      if (path === "/v1/me") {
        return new Response(JSON.stringify({ user: { email: "owner@example.com", password_change_required: true } }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/auth/change-password") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("Signed in as owner@example.com")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "old" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "new-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Update password" }));

    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/auth/change-password",
      method: "POST",
      body: { current_password: "old", new_password: "new-secret" },
    }));
    expect(redirectTo).toHaveBeenCalledWith("/dashboard");
  });

  it("loads Home Assistant entities and sends a service call", async () => {
    window.history.pushState({}, "", "/dashboard/home-assistant");
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online", home_name: "Campbell Home" },
      profile: {
        revision: 2,
        settings: { dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }] },
      },
      dashboardEntityIDs: ["light.kitchen"],
      states: [
        {
          entity_id: "light.kitchen",
          state: "on",
          attributes: { friendly_name: "Kitchen Light", unit_of_measurement: "" },
        },
      ],
    });
    homeAssistantClient.callService.mockResolvedValue({});
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      if (path === "/v1/home/agent") {
        return new Response(JSON.stringify({
          agent: { agent_id: "agent-1", name: "Kitchen Mac", status: "online", home_name: "Campbell Home" },
          can_restart: true,
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/profile") {
        return new Response(JSON.stringify({
          revision: 2,
          settings: { dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }] },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/file-jobs?limit=20") {
        return new Response(JSON.stringify({ jobs: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect((await screen.findAllByText("Kitchen Light")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("light.kitchen").length).toBeGreaterThan(0);
    expect(homeAssistantClient.load).toHaveBeenCalled();

    fireEvent.click(screen.getAllByRole("switch", { name: "Toggle Kitchen Light" })[0]);
    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("light.kitchen", "light", "turn_off"));
  });

  it("keeps the dashboard rendered when bootstrap omits optional user fields", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          home: null,
          membership: null,
          permissions: {},
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("Unknown user")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
  });

  it("renders the dashboard design home with live operational data", async () => {
    window.history.pushState({}, "", "/dashboard");
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      calls.push(path);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com", display_name: "Aaron Campbell" },
          home: { id: "home_1", name: "Campbell Home", created_at: "2026-06-01T00:00:00Z", updated_at: "2026-06-01T00:00:00Z" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z" },
          setup_status: { first_setup_visible: true },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({
          can_edit: true,
          links: [{ id: "ql_1", title: "Home Assistant", url: "https://ha.local", description: "Local HA", status: "up", health_check_enabled: true }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home") {
        return new Response(JSON.stringify({ id: "home_1", name: "Campbell Home" }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent") {
        return new Response(JSON.stringify({
          can_restart: true,
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z", home_name: "Campbell Home" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/tokens") {
        return new Response(JSON.stringify({ tokens: [{ id: "agtok_1", revoked_at: null, created_at: "2026-06-01T00:00:00Z" }] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/home/sync") {
        return new Response(JSON.stringify({
          notes: { status: "healthy", last_successful_sync_at: "2026-06-30T11:00:00Z" },
          profiles: { homeassistant: { service_type: "homeassistant", status: "healthy", updated_at: "2026-06-29T00:00:00Z" } },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/status") {
        return new Response(JSON.stringify({
          backup: { last_successful_at: "2026-06-30T10:00:00Z", backups: [] },
          restore: { last_test_at: "2026-06-30T10:30:00Z" },
          checksum: { corruption_detected: false, failure_count: 0 },
          tasks: [{ operation: "backup", status: "running", step: "Uploading encrypted archive" }],
          events: [{ operation: "backup", severity: "info", message: "Backup completed", time: "2026-06-30T10:00:00Z" }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/apps") {
        return new Response(JSON.stringify({
          apps: [
            { app_id: "plex", name: "Plex", enabled: true },
            { app_id: "gramaton", name: "Gramaton", enabled: true },
            { app_id: "calendar", name: "Calendar", enabled: false },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members") {
        return new Response(JSON.stringify({
          members: [
            { user_id: "user_1", email: "owner@example.com", display_name: "Aaron Campbell", role: "admin", created_at: "2026-06-01T00:00:00Z", updated_at: "2026-06-01T00:00:00Z" },
            { user_id: "user_2", email: "member@example.com", display_name: "Family Member", role: "member", created_at: "2026-06-02T00:00:00Z", updated_at: "2026-06-02T00:00:00Z" },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: /Good/ })).toHaveTextContent("Aaron Campbell");
    expect(screen.getByText("The primary agent and storage checks at Campbell Home look healthy.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart agent" })).toBeEnabled();
    expect(screen.getByRole("link", { name: "Create setup file" })).toHaveAttribute("href", "/dashboard/settings/home");
    expect(screen.getByRole("heading", { name: "Services" })).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "Home connection" })).toHaveTextContent("Campbell Agent");
    expect(screen.getByRole("button", { name: "Show all services" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("heading", { name: "Recent activity" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Quick links" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "People" })).toBeInTheDocument();
    expect(screen.getByText("Monitoring")).toBeInTheDocument();
    expect(screen.queryByText("Installed apps")).not.toBeInTheDocument();
    expect(screen.getByText("Running jobs")).toBeInTheDocument();
    expect(screen.getByText("1 active")).toBeInTheDocument();
    expect(screen.getByText("Last backup")).toBeInTheDocument();
    expect(screen.getByText("Hank server")).toBeInTheDocument();
    expect(screen.getByText("Primary Hank Agent")).toBeInTheDocument();
    expect(screen.getByText("PostgreSQL")).toBeInTheDocument();
    expect(screen.getByText("SMB shares")).toBeInTheDocument();
    expect(screen.getAllByText("Home Assistant").length).toBeGreaterThan(0);
    expect(screen.getByText("Backup completed")).toBeInTheDocument();
    expect(screen.getByText("Family Member")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "First Setup" })).not.toBeInTheDocument();
    expect(calls).toEqual(expect.arrayContaining([
      "/v1/ui/bootstrap",
      "/v1/home",
      "/v1/home/agent",
      "/v1/home/sync",
      "/v1/home/storage/status",
      "/v1/home/quick-links",
      "/v1/home/members",
    ]));
  });

  it("shows saved Home Assistant entities as quick controls on the dashboard home", async () => {
    window.history.pushState({}, "", "/dashboard");
    homeAssistantClient.load.mockResolvedValue({
      agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online" },
      profile: {
        revision: 7,
        settings: { dashboard_tiles: [{ entity_id: "light.kitchen", is_enabled: true }] },
      },
      dashboardEntityIDs: ["light.kitchen"],
      states: [
        { entity_id: "light.kitchen", state: "on", attributes: { friendly_name: "Kitchen Light", brightness: 128 } },
        { entity_id: "sensor.humidity", state: "47", attributes: { friendly_name: "Humidity", unit_of_measurement: "%" } },
      ],
    });
    homeAssistantClient.callService.mockResolvedValue({});
    homeAssistantClient.fetchState.mockResolvedValue({
      entity_id: "light.kitchen",
      state: "off",
      attributes: { friendly_name: "Kitchen Light", brightness: 128 },
    });
    homeAssistantClient.onStateChanged.mockReturnValue(() => {});
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_1", email: "owner@example.com" },
          home: { id: "home_1", name: "Campbell Home", created_at: "2026-06-01T00:00:00Z", updated_at: "2026-06-01T00:00:00Z" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z" },
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home") {
        return new Response(JSON.stringify({ id: "home_1", name: "Campbell Home" }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent") {
        return new Response(JSON.stringify({
          can_restart: true,
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z", home_name: "Campbell Home" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/tokens") {
        return new Response(JSON.stringify({ tokens: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/sync") {
        return new Response(JSON.stringify({ notes: { status: "healthy" }, profiles: { homeassistant: { service_type: "homeassistant", status: "healthy" } } }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/status") {
        return new Response(JSON.stringify({ backup: {}, restore: {}, checksum: {}, tasks: [], events: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/home/apps") {
        return new Response(JSON.stringify({ apps: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members") {
        return new Response(JSON.stringify({ members: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    const controls = await screen.findByRole("region", { name: "Home Assistant controls" });
    expect(within(controls).getByRole("heading", { name: "Home Assistant" })).toBeInTheDocument();
    expect(within(controls).getByText("Kitchen Light")).toBeInTheDocument();
    const toggle = within(controls).getByRole("switch", { name: "Toggle Kitchen Light" });
    expect(toggle).toHaveAttribute("aria-checked", "true");

    fireEvent.click(toggle);

    await waitFor(() => expect(homeAssistantClient.callService).toHaveBeenCalledWith("light.kitchen", "light", "turn_off"));
    await waitFor(() => expect(homeAssistantClient.fetchState).toHaveBeenCalledWith("light.kitchen"));
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  });

  it("does not render canned quick-link cards when no quick links are configured", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_1", email: "owner@example.com" },
          home: { id: "home_1", name: "Campbell Home", created_at: "2026-06-01T00:00:00Z", updated_at: "2026-06-01T00:00:00Z" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z" },
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home") {
        return new Response(JSON.stringify({ id: "home_1", name: "Campbell Home" }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent") {
        return new Response(JSON.stringify({
          can_restart: true,
          agent: { agent_id: "campbell-home", name: "Campbell Agent", status: "online", last_seen_at: "2026-06-30T12:00:00Z", home_name: "Campbell Home" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/tokens") {
        return new Response(JSON.stringify({ tokens: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/sync") {
        return new Response(JSON.stringify({ notes: { status: "healthy" }, profiles: {} }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/status") {
        return new Response(JSON.stringify({ backup: {}, restore: {}, checksum: {}, tasks: [], events: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/home/apps") {
        return new Response(JSON.stringify({ apps: [] }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members") {
        return new Response(JSON.stringify({ members: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: /Good/ })).toHaveTextContent("owner");
    const quickLinks = screen.getByRole("region", { name: "Quick links" });
    expect(within(quickLinks).getByText("No quick links saved.")).toBeInTheDocument();
    expect(within(quickLinks).queryByRole("link", { name: "Home Assistant" })).not.toBeInTheDocument();
    expect(within(quickLinks).queryByRole("link", { name: "File Server" })).not.toBeInTheDocument();
    expect(within(quickLinks).queryByRole("link", { name: "Notes" })).not.toBeInTheDocument();
    expect(within(quickLinks).queryByRole("link", { name: "Backups" })).not.toBeInTheDocument();
  });

  it("filters admin-only shell navigation from non-admin bootstrap permissions", async () => {
    window.history.pushState({}, "", "/docs/deployment");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "member@example.com" },
          home: { name: "Home" },
          membership: { role: "member" },
          permissions: { is_admin: false },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Setup Guide" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).queryByRole("link", { name: "Apps" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Backups & Storage" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Recovery" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Logs" })).not.toBeInTheDocument();
  });

  it("uses in-app routing for sidebar links", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com" },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await screen.findByText("owner@example.com");
    fireEvent.click(screen.getByRole("link", { name: "Settings" }));

    expect(window.location.pathname).toBe("/dashboard/settings");
    expect(screen.getByRole("heading", { name: "Home & Agent Setup" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
  });

  it("redirects protected routes to login when bootstrap is unauthorized", async () => {
    window.history.pushState({}, "", "/dashboard/settings");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") {
        return new Response("unauthorized", { status: 401 });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await waitFor(() => expect(redirectTo).toHaveBeenCalledWith("/?expired=1"));
    expect(offlineNotesRuntime.current?.activateOffline).not.toHaveBeenCalled();
  });

  it("restores the cached Notes identity only after a network bootstrap failure", async () => {
    window.history.pushState({}, "", "/dashboard/file-server");
    const identity = {
      userID: "usr_offline",
      email: "offline@example.com",
      displayName: "Offline User",
      role: "member",
      permissions: { is_admin: false, can_use_notes: true },
      lastAuthenticatedAt: 100,
      offlineInitializedAt: 100,
    };
    offlineNotesRuntime.current = offlineRuntime({
      activateOffline: vi.fn().mockResolvedValue(identity),
    });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") throw new TypeError("Failed to fetch");
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Connection required" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Notes" })).toHaveAttribute("href", "/dashboard/profile-notes");
    expect(screen.getByTitle("offline@example.com")).toHaveTextContent("Offline User");
  });

  it("treats unknown dashboard routes as protected app routes", async () => {
    window.history.pushState({}, "", "/dashboard/missing");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com" },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Not Found" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Main" })).toBeInTheDocument();
  });

  it("redirects protected routes to password change when required", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com", password_change_required: true },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await waitFor(() => expect(redirectTo).toHaveBeenCalledWith("/password-change"));
  });

  it("logs out from the shell and returns to the login route", async () => {
    window.history.pushState({}, "", "/dashboard");
    const calls: Array<{ path: string; method: string }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      calls.push({ path, method });
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com" },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/auth/logout") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    }));

    let releasePurge!: () => void;
    const purge = new Promise<void>((resolve) => { releasePurge = resolve; });
    offlineNotesRuntime.current = offlineRuntime({
      purgeActive: vi.fn().mockReturnValue(purge),
    });
    render(<App />);

    await screen.findByText("owner@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(offlineNotesRuntime.current?.purgeActive).toHaveBeenCalledOnce());
    expect(calls).not.toContainEqual({ path: "/v1/auth/logout", method: "POST" });
    releasePurge();
    await waitFor(() => expect(calls).toContainEqual({ path: "/v1/auth/logout", method: "POST" }));
    expect(redirectTo).toHaveBeenCalledWith("/");
  });

  it("revokes the server session even when local logout cleanup fails", async () => {
    window.history.pushState({}, "", "/dashboard");
    const logout = vi.fn(async () => new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com" },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/auth/logout") return logout();
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));
    offlineNotesRuntime.current = offlineRuntime({
      purgeActive: vi.fn().mockRejectedValue(new Error("IndexedDB unavailable")),
    });
    render(<App />);

    await screen.findByText("owner@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(logout).toHaveBeenCalledOnce());
    expect(redirectTo).toHaveBeenCalledWith("/");
  });

  it("shows a clear session-expired message on the login route", async () => {
    window.history.pushState({}, "", "/?expired=1");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/me") return new Response("unauthorized", { status: 401 });
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Session expired. Sign in again.");
  });

  it("uses in-app routing for same-origin page links inside route content", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { email: "owner@example.com" },
          home: { name: "Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({ can_edit: true, links: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    await screen.findByText("owner@example.com");
    fireEvent.click(screen.getByRole("link", { name: "Manage" }));

    expect(window.location.pathname).toBe("/dashboard/settings/quick-links");
    expect(screen.getByRole("heading", { name: "Quick Links" })).toBeInTheDocument();
  });

  it("renders the deployment guide checklist", () => {
    window.history.pushState({}, "", "/docs/deployment");
    render(<App />);

    expect(screen.getByRole("heading", { name: "Setup Guide" })).toBeInTheDocument();
    expect(screen.getByText("Hank app -> Hank server -> Primary Hank Agent")).toBeInTheDocument();
    expect(screen.getByText(".env.cloud")).toBeInTheDocument();
    expect(screen.getByText("scripts/bootstrap-first-run.sh")).toBeInTheDocument();
    expect(screen.getByText("scripts/doctor.sh")).toBeInTheDocument();
    expect(screen.getByText("The primary Hank Agent shows online.")).toBeInTheDocument();
  });

  it("edits profile notes from the notes route", async () => {
    window.history.pushState({}, "", "/dashboard/profile-notes");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ path, method, body });
      if (path === "/v1/me/notes" && method === "GET") {
        return new Response(JSON.stringify({
          notes: [
            {
              note_id: "daily.md",
              title: "Daily",
              preview: "Remember milk",
              revision: "1",
              updated_at: "2026-06-27T00:00:00Z",
              page_type: "text",
              parent_id: "",
            },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/daily.md" && method === "GET") {
        return new Response(JSON.stringify({
          note_id: "daily.md",
          title: "Daily",
          body_markdown: "Remember milk",
          revision: "1",
          updated_at: "2026-06-27T00:00:00Z",
          page_type: "text",
          parent_id: "",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/daily.md" && method === "PUT") {
        return new Response(JSON.stringify({
          note_id: "daily.md",
          revision: "2",
          updated_at: "2026-06-27T00:01:00Z",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes" && method === "POST") {
        return new Response(JSON.stringify({
          note_id: "new-note.md",
          revision: "1",
          updated_at: "2026-06-27T00:02:00Z",
        }), { status: 201, headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/notes/daily.md" && method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: "Daily" }));
    const existingBody = await screen.findByLabelText("Note body");
    expect(existingBody).toHaveTextContent("Remember milk");
    existingBody.innerHTML = "Remember eggs";
    fireEvent.input(existingBody);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/me/notes/daily.md",
      method: "PUT",
      body: {
        note_id: "daily.md",
        title: "Daily",
        content: "Remember eggs",
        body_markdown: "Remember eggs",
        body_format: "markdown",
        expected_revision: "1",
        mcp_excluded: false,
        page_type: "text",
        parent_id: "",
        pinned: false,
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "New note" }));
    fireEvent.change(screen.getByLabelText("Note title"), { target: { value: "New Note" } });
    const newBody = screen.getByLabelText("Note body");
    newBody.innerHTML = "Fresh content";
    fireEvent.input(newBody);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/me/notes",
      method: "POST",
      body: {
        note_id: "",
        title: "New Note",
        content: "Fresh content",
        body_markdown: "Fresh content",
        body_format: "markdown",
        expected_revision: "",
        mcp_excluded: false,
        page_type: "text",
        parent_id: "",
        pinned: false,
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Daily" }));
    await waitFor(() => expect(screen.getByLabelText("Note body")).toHaveTextContent("Remember milk"));
    fireEvent.click(screen.getByRole("button", { name: "Delete note" }));
    fireEvent.click(await screen.findByRole("button", { name: "Move to Trash" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/me/notes/daily.md",
      method: "DELETE",
      body: undefined,
    }));
  });

  it("browses and manages files from the file server route", async () => {
    window.history.pushState({}, "", "/dashboard/file-server");
    const sentCommands: Array<{ command: string; body: unknown; request_id: string }> = [];

    class FakeWebSocket extends EventTarget {
      static OPEN = 1;
      readyState = FakeWebSocket.OPEN;

      constructor(public readonly url: string) {
        super();
        queueMicrotask(() => this.dispatchEvent(new Event("open")));
      }

      send(message: string) {
        const envelope = JSON.parse(message);
        sentCommands.push({
          command: envelope.payload.command,
          body: envelope.payload.body,
          request_id: envelope.request_id,
        });
        const body = envelope.payload.body || {};
        const items = body.path === "/Photos"
          ? [{ name: "vacation.jpg", path: "/Photos/vacation.jpg", is_directory: false, size: 2048, modified_at: "2026-06-27T00:00:00Z" }]
          : [
              { name: "Photos", path: "/Photos", is_directory: true, size: 0, modified_at: "2026-06-27T00:00:00Z" },
              { name: "todo.txt", path: "/todo.txt", is_directory: false, size: 12, modified_at: "2026-06-27T00:00:00Z" },
            ];
        const payload = envelope.payload.command === "files.list" ? { path: body.path, items } : { ok: true };
        queueMicrotask(() => this.dispatchEvent(new MessageEvent("message", {
          data: JSON.stringify({ type: "app.response", request_id: envelope.request_id, payload }),
        })));
      }

      close() {
        this.dispatchEvent(new Event("close"));
      }
    }

    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      if (path === "/v1/ws/app-ticket") {
        return new Response(JSON.stringify({
          ticket: "ticket",
          expires_at: "2026-06-27T00:00:00Z",
          websocket_path: "/ws/app?app_ticket=ticket",
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("button", { name: "Open Photos" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Folders" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "File list" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Preview" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Transfers" })).toBeInTheDocument();
    expect(screen.getByText("No active transfers")).toBeInTheDocument();
    expect(screen.getByText("Active share")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload" })).toBeEnabled();
    expect(screen.getByLabelText("Choose files to upload")).toHaveAttribute("type", "file");
    expect(screen.getByRole("columnheader", { name: "Name" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Size" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Type" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Modified" })).toBeInTheDocument();
    expect(screen.getAllByText("todo.txt").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Open Photos" }));
    expect((await screen.findAllByText("vacation.jpg")).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Download preview" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move preview" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "New folder" }));
    fireEvent.change(await screen.findByLabelText("Folder name"), { target: { value: "Projects" } });
    fireEvent.click(screen.getByRole("button", { name: "Create folder" }));
    await waitFor(() => expect(sentCommands.some((command) => (
      command.command === "files.create_directory" &&
      JSON.stringify(command.body) === JSON.stringify({ path: "/Photos/Projects" })
    ))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Delete vacation.jpg" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => expect(sentCommands.some((command) => (
      command.command === "files.delete" &&
      JSON.stringify(command.body) === JSON.stringify({ path: "/Photos/vacation.jpg", is_directory: false })
    ))).toBe(true));
  });

  it("runs a HankAI conversation from the HankAI route", async () => {
    window.history.pushState({}, "", "/dashboard/hank");
    const calls: Array<{ path: string; method: string; body?: unknown; execution: string | null }> = [];
    let assistantReplied = false;
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ path, method, body, execution: new Headers(init?.headers).get("X-Hank-Assistant-Execution") });
      if (path === "/v1/home/assistant/status") {
        return new Response(JSON.stringify({ provider: "ollama", ready: true, sources: [], execution_versions: [1, 2] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path === "/v1/home/assistant/sessions" && method === "GET") {
        return new Response(JSON.stringify({
          sessions: [{ id: "session-1", title: "Kitchen", last_message_at: "2026-06-27T00:00:00Z" }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/sessions/session-1/messages" && method === "GET") {
        return new Response(JSON.stringify({
          messages: [
            { id: "msg-1", role: "assistant", text: "Ask Hank anything.", created_at: "2026-06-27T00:00:00Z" },
            ...(assistantReplied ? [{ id: "msg-2", role: "assistant", text: "The kitchen light is on.", created_at: "2026-06-27T00:01:00Z" }] : []),
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/sessions/session-1/messages" && method === "POST") {
        assistantReplied = true;
        return new Response(JSON.stringify({
          schema_version: 2, task_id: "task-1", session_id: "session-1",
          state: "completed", revision: 1, last_event_sequence: 1, cancel_requested: false,
        }), { status: 201, headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("button", { name: "Kitchen" })).toBeInTheDocument();
    expect(await screen.findByText("Ask Hank anything.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Message"), { target: { value: "Kitchen light status" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/assistant/sessions/session-1/messages",
      method: "POST",
      execution: "2",
      body: {
        content: "Kitchen light status",
        submission_id: expect.any(String),
      },
    }));
    expect(await screen.findByText("The kitchen light is on.")).toBeInTheDocument();
  });

  it("loads dashboard bootstrap and quick links for the dashboard route", async () => {
    window.history.pushState({}, "", "/dashboard");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: {
            id: "usr_1",
            email: "owner@example.com",
            password_change_required: false,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          session: { id: "sess_1", expires_at: "2026-01-02T00:00:00Z" },
          home: {
            id: "home_1",
            user_id: "usr_1",
            name: "Campbell Home",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          membership: {
            home_id: "home_1",
            user_id: "usr_1",
            role: "admin",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          permissions: {
            is_admin: true,
            can_manage_people: true,
            can_manage_settings: true,
            can_use_homeassistant: true,
            can_use_files: true,
            can_use_notes: true,
            can_use_assistant: true,
            can_view_storage: true,
            can_manage_apps: true,
          },
          agent: {
            agent_id: "agt_1",
            name: "Kitchen Mac",
            status: "online",
            home_id: "home_1",
            home_name: "Campbell Home",
            capabilities: ["files.list", "homeassistant.fetch_states"],
          },
          setup_status: { first_setup_visible: false },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [{ path: "/dashboard", label: "Dashboard" }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/quick-links") {
        return new Response(JSON.stringify({
          can_edit: true,
          links: [
            {
              id: "ql_1",
              home_id: "home_1",
              title: "Home Assistant",
              url: "https://ha.example.test",
              description: "Local controls",
              sort_order: 0,
              health_check_enabled: true,
              status: "up",
              status_code: 200,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
              updated_by: "usr_1",
            },
            {
              id: "ql_2",
              home_id: "home_1",
              title: "File Server",
              url: "https://files.example.test",
              description: "Files",
              sort_order: 1,
              health_check_enabled: true,
              status: "down",
              status_code: 503,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
              updated_by: "usr_1",
            },
            {
              id: "ql_3",
              home_id: "home_1",
              title: "Notebook",
              url: "https://notes.example.test",
              description: "Notes",
              sort_order: 2,
              health_check_enabled: true,
              status: "unchecked",
              status_code: 0,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
              updated_by: "usr_1",
            },
            {
              id: "ql_4",
              home_id: "home_1",
              title: "Recipes",
              url: "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1",
              description: "Recipe index",
              sort_order: 3,
              health_check_enabled: false,
              status: "disabled",
              status_code: 0,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
              updated_by: "usr_1",
            },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/status") {
        return new Response(JSON.stringify({
          backup: { last_successful_at: "2026-01-01T00:00:00Z", backups: [] },
          restore: { last_test_at: "2026-01-01T01:00:00Z" },
          checksum: { corruption_detected: false, failure_count: 0 },
          tasks: [],
          events: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: /Good/ })).toHaveTextContent("owner");
    expect(screen.getByText("The primary agent and storage checks at Campbell Home look healthy.")).toBeInTheDocument();
    expect(screen.getAllByText(/Kitchen Mac/).length).toBeGreaterThan(0);
    expect(screen.getAllByText("Online").length).toBeGreaterThan(0);
    const quickLinks = screen.getByRole("region", { name: "Quick links" });
    expect(within(quickLinks).getByRole("link", { name: "Home Assistant" })).toHaveAttribute(
      "href",
      "https://ha.example.test",
    );
    expect(within(quickLinks).getByText("up")).toHaveClass("tone-up");
    expect(within(quickLinks).getByText("down")).toHaveClass("tone-down");
    expect(within(quickLinks).getByText("unchecked")).toHaveClass("tone-unknown");
    const recipesLink = within(quickLinks).getByRole("link", { name: "Recipes" });
    expect(recipesLink).toHaveAttribute("href", "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1");
    expect(recipesLink).not.toHaveAttribute("target");
    expect(recipesLink).not.toHaveAttribute("rel");
  });

  it("manages quick links from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/quick-links");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    const quickLinks = [
      {
        id: "ql_1",
        home_id: "home_1",
        title: "Home Assistant",
        url: "https://ha.example.test",
        description: "Local controls",
        sort_order: 0,
        health_check_enabled: true,
        status: "up",
        status_code: 200,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
        updated_by: "usr_1",
      },
      {
        id: "ql_2",
        home_id: "home_1",
        title: "Router",
        url: "https://router.example.test",
        description: "Network",
        sort_order: 10,
        health_check_enabled: false,
        status: "disabled",
        status_code: 0,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
        updated_by: "usr_1",
      },
    ];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home/quick-links" && method === "GET") {
        return new Response(JSON.stringify({ can_edit: true, links: quickLinks }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path.startsWith("/v1/home/quick-links")) {
        return new Response(JSON.stringify({ can_edit: true, links: quickLinks }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    const savedLinks = await screen.findByRole("list", { name: "Saved quick links" });
    expect(within(savedLinks).getByRole("link", { name: "Home Assistant" })).toHaveAttribute(
      "href",
      "https://ha.example.test",
    );
    expect(screen.getByRole("heading", { name: "Quick Links" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add quick link" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Recipes" } });
    fireEvent.change(screen.getByLabelText("URL"), { target: { value: "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1" } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Recipe index" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/quick-links",
      method: "POST",
      body: {
        title: "Recipes",
        url: "/dashboard/file-server?source_id=media&path=%2FRecipes&preview=1",
        description: "Recipe index",
        health_check_enabled: true,
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Edit Home Assistant" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "HA" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/quick-links/ql_1",
      method: "PUT",
      body: {
        title: "HA",
        url: "https://ha.example.test",
        description: "Local controls",
        health_check_enabled: true,
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Move Router up" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/quick-links/order",
      method: "PUT",
      body: { ids: ["ql_2", "ql_1"] },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Delete Home Assistant" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/quick-links/ql_1",
      method: "DELETE",
      body: undefined,
    }));

    fireEvent.click(screen.getByRole("button", { name: "Refresh status checks" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/quick-links/checks",
      method: "POST",
      body: {},
    }));
  });

  it("manages home and agent settings from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/home");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home" && method === "GET") {
        return new Response(JSON.stringify({
          id: "home_1",
          user_id: "usr_1",
          name: "Campbell Home",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home" && method === "PUT") {
        return new Response(JSON.stringify({
          id: "home_1",
          user_id: "usr_1",
          name: (body as { name: string }).name,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-02T00:00:00Z",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent" && method === "GET") {
        return new Response(JSON.stringify({
          can_restart: true,
          agent: {
            agent_id: "agent_1",
            name: "Kitchen Mac",
            status: "online",
            last_seen_at: "2026-01-01T00:00:00Z",
            home_id: "home_1",
            home_name: "Campbell Home",
          },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/tokens" && method === "GET") {
        return new Response(JSON.stringify({
          tokens: [{
            id: "agtok_1",
            home_id: "home_1",
            agent_id: "agent_1",
            created_at: "2026-01-01T00:00:00Z",
            expires_at: "2026-02-01T00:00:00Z",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/tokens" && method === "POST") {
        return new Response(JSON.stringify({
          token_id: "agtok_new",
          home_id: "home_1",
          agent_id: (body as { agent_id: string }).agent_id,
          agent_name: (body as { name: string }).name,
          token: "raw-agent-token",
          created_at: "2026-01-02T00:00:00Z",
        }), { status: 201, headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/agent/restart" && method === "POST") {
        return new Response(JSON.stringify({ ok: true, message: "restart scheduled" }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (path.startsWith("/v1/home/agent/tokens/") && method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByDisplayValue("Campbell Home")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Home & Agent Setup" })).toBeInTheDocument();
    expect(screen.getByText("Kitchen Mac")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
    expect(screen.getAllByText("agent_1").length).toBeGreaterThan(0);

    fireEvent.change(screen.getByRole("textbox", { name: "Home name" }), { target: { value: "Lake House" } });
    fireEvent.click(screen.getByRole("button", { name: "Save home name" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home",
      method: "PUT",
      body: { name: "Lake House" },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Restart agent" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/agent/restart",
      method: "POST",
      body: {},
    }));

    fireEvent.change(screen.getByRole("textbox", { name: "Agent ID" }), { target: { value: "lake-agent" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Agent name" }), { target: { value: "Lake Agent" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: "Expires after seconds" }), { target: { value: "3600" } });
    fireEvent.click(screen.getByRole("button", { name: "Create setup file" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/agent/tokens",
      method: "POST",
      body: { agent_id: "lake-agent", name: "Lake Agent", agent_type: "primary", expires_in_seconds: 3600 },
    }));
    expect(await screen.findByText(/HANK_AGENT_TOKEN="raw-agent-token"/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Disable setup file for agent_1" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/agent/tokens/agtok_1",
      method: "DELETE",
      body: undefined,
    }));
  });

  it("manages people from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/people");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home" && method === "GET") {
        return new Response(JSON.stringify({
          id: "home_1",
          user_id: "usr_admin",
          name: "Campbell Home",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/ui/bootstrap" && method === "GET") {
        return new Response(JSON.stringify({
          user: {
            id: "usr_admin",
            email: "admin@example.com",
            password_change_required: false,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          session: { id: "sess_1", expires_at: "2026-01-02T00:00:00Z" },
          home: {
            id: "home_1",
            user_id: "usr_admin",
            name: "Campbell Home",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          membership: {
            home_id: "home_1",
            user_id: "usr_admin",
            role: "admin",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          permissions: {
            is_admin: true,
            can_manage_people: true,
            can_manage_settings: true,
            can_use_homeassistant: true,
            can_use_files: true,
            can_use_notes: true,
            can_use_assistant: true,
            can_view_storage: true,
            can_manage_apps: true,
          },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members" && method === "GET") {
        return new Response(JSON.stringify({
          members: [
            {
              user_id: "usr_admin",
              email: "admin@example.com",
              role: "admin",
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
            {
              user_id: "usr_member",
              email: "member@example.com",
              role: "member",
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
          ],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members/invitations" && method === "GET") {
        return new Response(JSON.stringify({
          invitations: [{
            id: "invite_1",
            home_id: "home_1",
            email: "pending@example.com",
            role: "member",
            created_at: "2026-01-01T00:00:00Z",
            expires_at: "2026-01-08T00:00:00Z",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/members/invitations" && method === "POST") {
        return new Response(JSON.stringify({
          invitation_id: "invite_new",
          home_id: "home_1",
          email: (body as { email: string }).email,
          role: "member",
          token: "invite-token",
          join_url: "https://hank.example.test/join#token=invite-token",
          expires_at: "2026-01-08T00:00:00Z",
        }), { status: 201, headers: { "Content-Type": "application/json" } });
      }
      if (path.endsWith("/password") && method === "PUT") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      if (path.startsWith("/v1/home/members/") && method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("admin@example.com")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "People" })).toBeInTheDocument();
    expect(screen.getByText("member@example.com")).toBeInTheDocument();
    expect(screen.getByText("pending@example.com")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: "Invite email" }), {
      target: { value: "new@example.com" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create invite" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/members/invitations",
      method: "POST",
      body: { email: "new@example.com" },
    }));
    expect(await screen.findByText("https://hank.example.test/join#token=invite-token")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Cancel invite for pending@example.com" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel invite" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/members/invitations/invite_1",
      method: "DELETE",
      body: undefined,
    }));

    fireEvent.click(screen.getByRole("button", { name: "Reset password for member@example.com" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Temporary password" }), {
      target: { value: "temporary-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save temporary password" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/members/usr_member/password",
      method: "PUT",
      body: { temporary_password: "temporary-password", password_change_required: true },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Remove member@example.com" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/members/usr_member",
      method: "DELETE",
      body: undefined,
    }));
  });

  it("manages connection settings from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/connections");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/ui/bootstrap" && method === "GET") {
        return new Response(JSON.stringify({
          user: {
            id: "usr_admin",
            email: "admin@example.com",
            password_change_required: false,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          session: { id: "sess_1", expires_at: "2026-01-02T00:00:00Z" },
          home: {
            id: "home_1",
            user_id: "usr_admin",
            name: "Campbell Home",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          membership: {
            home_id: "home_1",
            user_id: "usr_admin",
            role: "admin",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          permissions: {
            is_admin: true,
            can_manage_people: true,
            can_manage_settings: true,
            can_use_homeassistant: true,
            can_use_files: true,
            can_use_notes: true,
            can_use_assistant: true,
            can_view_storage: true,
            can_manage_apps: true,
          },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/service-profiles" && method === "GET") {
        return new Response(JSON.stringify({
          profiles: [{
            home_id: "home_1",
            service_type: "homeassistant",
            public_config_json: JSON.stringify({ base_url: "http://ha.local:8123", timeout_seconds: 10 }),
            secret_version: 1,
            applied_version: 1,
            status: "healthy",
            updated_at: "2026-01-01T00:00:00Z",
            updated_by: "usr_admin",
          }, {
            home_id: "home_1",
            service_type: "smb",
            public_config_json: JSON.stringify({
              shares: [
                { id: "media", name: "Media", host: "nas.local", share: "media", username: "aaron", policy: { read: true, write: false } },
                { id: "archive", name: "Archive", host: "nas.local", share: "archive", policy: { read: true } },
              ],
              folders: [{ id: "media-folder", name: "Media Folder", root: "/srv/media", enabled: true, policy: { read: true, write: false } }],
            }),
            secret_version: 1,
            applied_version: 1,
            status: "pending",
            updated_at: "2026-01-01T00:00:00Z",
            updated_by: "usr_admin",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path.startsWith("/v1/home/service-profiles/") && method === "PUT") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByDisplayValue("http://ha.local:8123")).toBeInTheDocument();
    expect(screen.getByText("homeassistant")).toBeInTheDocument();
    expect(screen.getByText("Media")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Media Folder")).toBeInTheDocument();
    expect(screen.getByDisplayValue("/srv/media")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: "Home Assistant address" }), {
      target: { value: "http://homeassistant.local:8123" },
    });
    fireEvent.change(screen.getByLabelText("Home Assistant token"), {
      target: { value: "ha-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Home Assistant" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/service-profiles/homeassistant",
      method: "PUT",
      body: {
        public_config: { base_url: "http://homeassistant.local:8123", timeout_seconds: 10 },
        secrets: { token: "ha-token" },
        persist: true,
      },
    }));

    fireEvent.change(screen.getByRole("textbox", { name: "Share label" }), { target: { value: "Backups" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Server address" }), { target: { value: "smb://backup.local" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Share name" }), { target: { value: "backups" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Username" }), { target: { value: "backup-user" } });
    fireEvent.change(screen.getByLabelText("SMB password"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save File Server" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/service-profiles/smb",
      method: "PUT",
      body: {
        public_config: {
          active_source_id: "media",
          host: "backup.local",
          share: "backups",
          domain: "",
          username: "backup-user",
          shares: [
            {
              id: "media",
              name: "Backups",
              host: "backup.local",
              share: "backups",
              domain: "",
              username: "backup-user",
              policy: { read: true, write: false },
            },
            {
              id: "archive",
              name: "Archive",
              host: "nas.local",
              share: "archive",
              policy: { read: true },
            },
          ],
        },
        secrets: { shares: [{ id: "media", password: "secret" }] },
        persist: true,
      },
    }));

    fireEvent.change(screen.getByRole("textbox", { name: "Host folder label" }), { target: { value: "Documents" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Host folder path" }), { target: { value: "/srv/documents" } });
    fireEvent.click(screen.getByRole("checkbox", { name: "Create folder if it does not exist" }));
    fireEvent.click(screen.getByRole("button", { name: "Save Host Folders" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/service-profiles/smb",
      method: "PUT",
      body: {
        public_config: {
          folders: [{
            id: "media-folder",
            name: "Documents",
            root: "/srv/documents",
            create: true,
            policy: { read: true, write: false },
          }],
        },
        persist: true,
      },
    }));
  });

  it("manages AI and MCP settings from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/ai");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    let canManageSettings = true;
    vi.stubGlobal("open", vi.fn());
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/ui/bootstrap" && method === "GET") {
        return new Response(JSON.stringify({
          user: {
            id: "usr_admin",
            email: "admin@example.com",
            password_change_required: false,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          session: { id: "sess_1", expires_at: "2026-01-02T00:00:00Z" },
          home: {
            id: "home_1",
            user_id: "usr_admin",
            name: "Campbell Home",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          membership: {
            home_id: "home_1",
            user_id: "usr_admin",
            role: "admin",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          permissions: {
            is_admin: true,
            can_manage_people: true,
            can_manage_settings: canManageSettings,
            can_use_homeassistant: true,
            can_use_files: true,
            can_use_notes: true,
            can_use_assistant: true,
            can_view_storage: true,
            can_manage_apps: true,
          },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/oauth/openai/status" && method === "GET") {
        return new Response(JSON.stringify({
          linked: false,
          configured: true,
          auth_provider: "chatgpt_codex",
          auth_mode: "device_code",
          chatgpt_plan_type: "plus",
          updated_at: "2026-01-01T00:00:00Z",
          expires_at: "2026-02-01T00:00:00Z",
          scopes: "openid profile email",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/status" && method === "GET") {
        return new Response(JSON.stringify({
          provider: "ollama",
          chat_model: "llama3.1",
          embedding_model: "nomic-embed-text",
          vector_store: "postgres",
          index: {
            vector_mode: "pgvector",
            chunk_count: 12,
            embedded_chunk_count: 10,
            file_count: 3,
            embedded_file_count: 2,
            conversation_count: 4,
          },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/settings" && method === "GET") {
        return new Response(JSON.stringify({
          settings: {
            profile_notes_enabled: true,
            home_notes_enabled: true,
            files_enabled: true,
            calendar_enabled: false,
            homeassistant_enabled: true,
            project_docs_enabled: true,
            conversations_enabled: true,
            ai_provider: "ollama",
            ollama_base_url: "http://localhost:11434",
            chat_model: "llama3.1",
            embedding_model: "nomic-embed-text",
            planner_enabled: true,
            planner_model: "",
            prompt_profile: "local",
            system_prompt: "Use Hank context.",
          },
          defaults: {
            ollama_base_url: "http://localhost:11434",
            chat_model: "llama3.1",
            embedding_model: "nomic-embed-text",
            system_prompt: "Use Hank context.",
            prompt_profiles: [
              { key: "local", label: "Local", prompt: "Use local context." },
              { key: "chatgpt", label: "ChatGPT", prompt: "Use Hank context." },
              { key: "custom", label: "Custom", prompt: "" },
            ],
            provider_options: [
              { key: "ollama", label: "Local Ollama" },
              { key: "chatgpt_codex", label: "Linked ChatGPT/Codex" },
            ],
          },
          sources: [
            { key: "profile_notes", label: "Personal notes", enabled: true },
            { key: "files", label: "Files", enabled: true },
          ],
          tools: [{ label: "Files", enabled: true, status: "Ready", description: "Search file names." }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/models" && method === "GET") {
        return new Response(JSON.stringify({
          models: ["llama3.1", "gpt-4.1"],
          source: "ollama",
          provider: "ollama",
          default_model: "llama3.1",
          current_model: "llama3.1",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/mcp" && method === "GET") {
        return new Response(JSON.stringify({
          resource_url: "https://hank.example.test/v1/mcp",
          scopes_supported: ["notes:read", "docs:read"],
          connections: [{
            id: "conn_1",
            client_id: "client_1",
            client_name: "ChatGPT",
            scopes: ["notes:read"],
            connected: true,
            created_at: "2026-01-01T00:00:00Z",
            last_used_at: "2026-01-02T00:00:00Z",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/oauth/openai/start" && method === "POST") {
        return new Response(JSON.stringify({
          auth_mode: "device_code",
          verification_url: "https://chatgpt.example.test/device",
          user_code: "HANK-CODE",
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/assistant/settings" && method === "PUT") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/me/mcp/connections/conn_1" && method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("pgvector")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "AI & MCP" })).toBeInTheDocument();
    expect(screen.getAllByText(/ChatGPT\/Codex/).length).toBeGreaterThan(0);
    expect(screen.getByText("pgvector")).toBeInTheDocument();
    expect(screen.getByText("https://hank.example.test/v1/mcp")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect ChatGPT" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Interactive Kanban app")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save MCP settings" })).not.toBeInTheDocument();
    expect(screen.getByText("Interactive Kanban is included whenever Hank MCP is on.")).toBeInTheDocument();
    expect(calls.some((call) => call.path === "/v1/home/mcp/settings")).toBe(false);

    fireEvent.click(screen.getByLabelText("Calendar"));
    fireEvent.change(screen.getByLabelText("Chat model"), { target: { value: "gpt-4.1" } });
    fireEvent.change(screen.getByLabelText("System prompt"), { target: { value: "Use only approved Hank context." } });
    fireEvent.click(screen.getByRole("button", { name: "Save HankAI Settings" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/assistant/settings",
      method: "PUT",
      body: {
        profile_notes_enabled: true,
        home_notes_enabled: true,
        files_enabled: true,
        calendar_enabled: true,
        homeassistant_enabled: true,
        project_docs_enabled: true,
        conversations_enabled: true,
        ai_provider: "ollama",
        ollama_base_url: "http://localhost:11434",
        chat_model: "gpt-4.1",
        embedding_model: "nomic-embed-text",
        planner_enabled: true,
        planner_model: "",
        prompt_profile: "local",
        system_prompt: "Use only approved Hank context.",
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Link ChatGPT/Codex" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/oauth/openai/start",
      method: "POST",
      body: {},
    }));
    expect(window.open).toHaveBeenCalledWith("https://chatgpt.example.test/device", "_blank", "noopener");

    fireEvent.click(screen.getByRole("button", { name: "Disconnect ChatGPT" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/me/mcp/connections/conn_1",
      method: "DELETE",
      body: undefined,
    }));

  });

  it("manages installable apps from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/apps");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      if (init?.body instanceof FormData) body = "form-data";
      calls.push({ path, method, body });
      if (path === "/v1/ui/bootstrap" && method === "GET") {
        return new Response(JSON.stringify({
          user: {
            id: "usr_admin",
            email: "admin@example.com",
            password_change_required: false,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          session: { id: "sess_1", expires_at: "2026-01-02T00:00:00Z" },
          home: {
            id: "home_1",
            user_id: "usr_admin",
            name: "Campbell Home",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          membership: {
            home_id: "home_1",
            user_id: "usr_admin",
            role: "admin",
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
          permissions: {
            is_admin: true,
            can_manage_people: true,
            can_manage_settings: true,
            can_use_homeassistant: true,
            can_use_files: true,
            can_use_notes: true,
            can_use_assistant: true,
            can_view_storage: true,
            can_manage_apps: true,
          },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: { mcp_enabled: true },
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/apps" && method === "GET") {
        return new Response(JSON.stringify({
          apps: [{
            id: "hermes",
            name: "Hermes",
            version: "1.0.0",
            enabled: false,
            status: "installed",
            user_access: "admins_only",
            public_config: { api_base_url: "https://old-hermes.local" },
            secret_fields_set: { api_key: true },
            settings_schema: {
              fields: [
                { key: "api_base_url", label: "Hermes URL", type: "url", order: 1 },
                { key: "api_key", label: "Hermes API key", type: "text", secret: true, secret_key: "api_key", order: 2 },
              ],
            },
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/apps/import/preview" && method === "POST") {
        return new Response(JSON.stringify({
          staging_id: "stage_1",
          package_sha256: "sha",
          replacing: false,
          app: { id: "tools", name: "Tools", version: "0.1.0", enabled: false, status: "preview" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/apps/import/activate" && method === "POST") {
        return new Response(JSON.stringify({
          app: { id: "tools", name: "Tools", version: "0.1.0", enabled: false, status: "installed" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/apps/hermes/config" && method === "PUT") {
        return new Response(JSON.stringify({
          app: { id: "hermes", name: "Hermes", version: "1.0.0", enabled: true, status: "installed" },
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("Hermes")).toBeInTheDocument();
    expect(screen.getByText("installed")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Configure Hermes" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Hermes URL" }), {
      target: { value: "https://hermes.local" },
    });
    fireEvent.change(screen.getByLabelText("Hermes API key"), { target: { value: "secret" } });
    fireEvent.click(screen.getByLabelText("Enabled"));
    fireEvent.change(screen.getByLabelText("User access"), { target: { value: "home_members" } });
    fireEvent.click(screen.getByRole("button", { name: "Save app configuration" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/apps/hermes/config",
      method: "PUT",
      body: {
        public_config: { api_base_url: "https://hermes.local" },
        permission_grants: {},
        secrets: { api_key: "secret" },
        enable: true,
        user_access: "home_members",
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Enable Hermes" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/apps/hermes/config",
      method: "PUT",
      body: { enable: true, user_access: "admins_only" },
    }));

    const packageFile = new File(["package"], "tools.hankapp", { type: "application/vnd.hank.app-package" });
    fireEvent.change(screen.getByLabelText("Package"), { target: { files: [packageFile] } });
    fireEvent.click(screen.getByRole("button", { name: "Preview package" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/apps/import/preview",
      method: "POST",
      body: "form-data",
    }));
    const installApp = screen.getByRole("region", { name: "Install app" });
    expect(await within(installApp).findByText("Tools")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Install preview" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/apps/import/activate",
      method: "POST",
      body: { staging_id: "stage_1", package_sha256: "sha", enable: false },
    }));
  });

  it("manages backup and storage settings from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/backups");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home/storage/status" && method === "GET") {
        return new Response(JSON.stringify({
          checksum: {
            enabled: true,
            last_check_at: "2026-06-27T12:00:00Z",
            failure_count: 0,
          },
          backup: {
            last_successful_at: "2026-06-27T10:00:00Z",
            backups: [{
              label: "20260627-010000F",
              type: "full",
              stopped_at: "2026-06-27T10:00:00Z",
              size_bytes: 2048,
            }, {
              label: "20260627-010000F_20260628-010000D",
              type: "diff",
              stopped_at: "2026-06-28T10:00:00Z",
              size_bytes: 1024,
            }],
          },
          restore: {
            last_test_at: "2026-06-27T11:00:00Z",
          },
          config: {
            target: { type: "posix", path: "/var/lib/pgbackrest" },
            schedule: {
              full_backup_cron: "0 2 * * 0",
              differential_backup_cron: "0 2 * * 1-6",
              checksum_interval_seconds: 900,
              retention_full: 2,
              amcheck_cron: "30 3 * * 0",
              restore_verification_cron: "0 4 * * 0",
              restore_verification_enabled: true,
            },
          },
          tasks: [],
          events: [{
            operation: "backup",
            severity: "info",
            message: "Backup finished.",
            time: "2026-06-27T10:00:00Z",
            backup_label: "20260627-010000F",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/config" && method === "PUT") {
        return new Response(JSON.stringify({ config: body }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/backup" && method === "POST") {
        return new Response(JSON.stringify({ intent_id: "intent_1" }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/restore-test" && method === "POST") {
        return new Response(JSON.stringify({ intent_id: "intent_2" }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/storage/events" && method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } });
      }
      if (path.startsWith("/v1/home/audit-events") && method === "GET") {
        return new Response(JSON.stringify({
          events: [{
            event_type: "storage.backup_requested",
            severity: "info",
            target_type: "storage",
            helper_text: "A backup was requested.",
            occurred_at: "2026-06-27T10:00:00Z",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path.startsWith("/v1/home/query-telemetry") && method === "GET") {
        return new Response(JSON.stringify({
          queries: [{
            calls: 4,
            rows: 12,
            total_exec_ms: 20.5,
            mean_exec_ms: 5.1,
            query: "select * from homes",
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByDisplayValue("/var/lib/pgbackrest")).toBeInTheDocument();
    expect(screen.getAllByText("20260627-010000F").length).toBeGreaterThan(0);
    expect(screen.getByText("Backup finished.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Audit Trail" })).toBeInTheDocument();
    expect(screen.getByText("storage.backup_requested")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Query Telemetry" })).toBeInTheDocument();
    expect(screen.getByText("select * from homes")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Backup target path"), { target: { value: "/backups/hank" } });
    fireEvent.change(screen.getByLabelText("Full backups kept"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save backup settings" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/storage/config",
      method: "PUT",
      body: {
        target: { type: "posix", path: "/backups/hank" },
        schedule: {
          full_backup_cron: "0 2 * * 0",
          differential_backup_cron: "0 2 * * 1-6",
          checksum_interval_seconds: 900,
          retention_full: 3,
          amcheck_cron: "30 3 * * 0",
          restore_verification_cron: "0 4 * * 0",
          restore_verification_enabled: true,
        },
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Run Full Backup" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/storage/backup",
      method: "POST",
      body: { backup_type: "full" },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Test Restore" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/storage/restore-test",
      method: "POST",
      body: { backup_label: "20260627-010000F_20260628-010000D" },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Clear storage logs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Clear logs" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/storage/events",
      method: "DELETE",
      body: undefined,
    }));
  });

  it("manages recovery export and import from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/recovery");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home/recovery/export" && method === "GET") {
        return new Response(JSON.stringify({
          schema_version: 2,
          product: "hank",
          home: { name: "Campbell Home" },
          warnings: ["Secrets are intentionally blank."],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/recovery/import/preview" && method === "POST") {
        return new Response(JSON.stringify({
          valid: true,
          changes: [{ area: "home", target: "name", action: "update" }],
          required_secrets: [{
            id: "homeassistant-token",
            label: "Home Assistant token",
            service_type: "homeassistant",
            kind: "password",
            target: { field: "token" },
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      if (path === "/v1/home/recovery/import/apply" && method === "POST") {
        return new Response(JSON.stringify({
          applied: true,
          changes: [{ area: "home", target: "name", action: "update" }],
          required_secrets: [{
            id: "homeassistant-token",
            label: "Home Assistant token",
            service_type: "homeassistant",
            kind: "password",
            target: { field: "token" },
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Recovery" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Export Recovery Bundle" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/recovery/export",
      method: "GET",
      body: undefined,
    }));

    const bundleFile = new File([
      JSON.stringify({ schema_version: 2, product: "hank", home: { name: "Campbell Home" } }),
    ], "hank-recovery-settings.json", { type: "application/json" });
    fireEvent.change(screen.getByLabelText("Recovery Bundle"), { target: { files: [bundleFile] } });
    fireEvent.click(screen.getByRole("button", { name: "Preview Import" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/recovery/import/preview",
      method: "POST",
      body: { schema_version: 2, product: "hank", home: { name: "Campbell Home" } },
    }));
    expect(await screen.findByText("name")).toBeInTheDocument();
    expect(screen.getByText("Home Assistant token")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Apply Non-Secret Settings" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/recovery/import/apply",
      method: "POST",
      body: {
        bundle: { schema_version: 2, product: "hank", home: { name: "Campbell Home" } },
        confirm: true,
      },
    }));
  });

  it("filters audit logs from the settings route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/logs");
    const calls: Array<{ path: string; method: string }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      calls.push({ path, method });
      if (path.startsWith("/v1/home/audit-events")) {
        return new Response(JSON.stringify({
          events: [{
            event_type: "agent.restart_requested",
            severity: "warning",
            target_type: "agent",
            target_id: "agent_1",
            actor_user_id: "usr_1",
            request_id: "req_1",
            helper_text: "Connector restart was requested from the dashboard.",
            occurred_at: "2026-06-27T12:00:00Z",
            metadata: { restart_at: "2026-06-27T12:00:01Z" },
          }],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(await screen.findByText("agent.restart_requested")).toBeInTheDocument();
    expect(screen.getByText("Connector restart was requested from the dashboard.")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Agent restart" })).toHaveValue("agent.restart_requested");
    expect(screen.getByRole("option", { name: "Login failed" })).toHaveValue("login.failed");
    expect(screen.getByRole("option", { name: "Invite created" })).toHaveValue("invitation.created");
    expect(screen.getByRole("option", { name: "Invite accepted" })).toHaveValue("invitation.accepted");
    expect(screen.getByRole("option", { name: "Agent" })).toHaveValue("agent");
    expect(screen.getByRole("option", { name: "Invitation" })).toHaveValue("invitation");
    expect(screen.getByText("restart_at")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Event"), { target: { value: "file_operation.denied" } });
    fireEvent.change(screen.getByLabelText("Severity"), { target: { value: "warning" } });
    fireEvent.change(screen.getByLabelText("Target"), { target: { value: "file_policy" } });
    fireEvent.change(screen.getByLabelText("Sort By"), { target: { value: "severity" } });
    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "asc" } });
    fireEvent.change(screen.getByLabelText("Limit"), { target: { value: "25" } });
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/audit-events?event_type=file_operation.denied&severity=warning&target_type=file_policy&sort=severity&order=asc&limit=25",
      method: "GET",
    }));
  });

  it("accepts a home invitation from the settings join route", async () => {
    window.history.pushState({}, "", "/dashboard/settings/join-home?token=invite-token");
    const calls: Array<{ path: string; method: string; body?: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      const method = String(init?.method || "GET").toUpperCase();
      let body: unknown;
      if (typeof init?.body === "string") body = JSON.parse(init.body);
      calls.push({ path, method, body });
      if (path === "/v1/home/invitations/accept" && method === "POST") {
        return new Response(JSON.stringify({
          home: {
            id: "home_2",
            user_id: "usr_owner",
            name: "Lake House",
            created_at: "2026-06-27T12:00:00Z",
            updated_at: "2026-06-27T12:00:00Z",
          },
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(<App />);

    expect(screen.getByDisplayValue("invite-token")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Join Home" }));
    await waitFor(() => expect(calls).toContainEqual({
      path: "/v1/home/invitations/accept",
      method: "POST",
      body: { token: "invite-token" },
    }));
    expect(await screen.findByText("Lake House")).toBeInTheDocument();
    expect(screen.getByText(/home_2/)).toBeInTheDocument();
  });

  it("opens the settings root on the Home settings page with the guide rail for admins", async () => {
    window.history.pushState({}, "", "/dashboard/settings");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input) === "/v1/ui/bootstrap") {
        return new Response(JSON.stringify({
          user: { id: "usr_admin", email: "owner@example.com" },
          home: { id: "home_1", name: "Campbell Home" },
          membership: { role: "admin" },
          permissions: { is_admin: true, can_manage_settings: true },
          agent: null,
          setup_status: { first_setup_visible: false },
          features: {},
          server: { version: "dev" },
          navigation: [],
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));
    render(<App />);

    expect(await screen.findByRole("heading", { name: "Home & Agent Setup" })).toBeInTheDocument();
    const settingsRail = screen.getByRole("navigation", { name: "Settings sections" });
    expect(within(settingsRail).getByRole("link", { name: "Home ADMIN" })).toHaveAttribute("aria-current", "page");
    expect(within(settingsRail).getByRole("link", { name: "Quick Links" })).toHaveAttribute("href", "/dashboard/settings/quick-links");
    expect(screen.queryByRole("region", { name: "Settings sections" })).not.toBeInTheDocument();
  });
});
