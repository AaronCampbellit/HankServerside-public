import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Shell } from "./Shell";

const searchClient = vi.hoisted(() => ({
  search: vi.fn(),
}));

vi.mock("../api/search", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/search")>()),
  searchClient,
}));

vi.mock("../pwa/InstallHankAction", () => ({
  InstallHankAction: ({ onComplete }: { onComplete?: () => void }) => (
    <button type="button" onClick={onComplete}>Install Hank</button>
  ),
}));

describe("Shell", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
    localStorage.clear();
  });

  it("keeps a working expand control after the desktop sidebar is collapsed", () => {
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    const shell = screen.getByText("Dashboard content").closest(".app-shell");
    fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }));

    expect(shell).toHaveAttribute("data-nav-collapsed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }));
    expect(shell).toHaveAttribute("data-nav-collapsed", "false");
  });

  it("shows search progress and navigates to the exact selected result", async () => {
    let resolveSearch: (output: { results: Array<{ type: string; title: string; url: string }> }) => void = () => undefined;
    searchClient.search.mockImplementation(() => new Promise((resolve) => {
      resolveSearch = resolve;
    }));
    const onNavigate = vi.fn();
    render(
      <Shell
        navItems={[{ href: "/dashboard/profile-notes", label: "Notes", group: "Main" }]}
        currentPath="/dashboard/profile-notes"
        onNavigate={onNavigate}
        onLogout={vi.fn()}
      >
        <div>Notes content</div>
      </Shell>,
    );

    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), { target: { value: "Roof Warranty" } });
    expect(await screen.findByRole("status")).toHaveTextContent("Searching");
    await waitFor(() => expect(searchClient.search).toHaveBeenCalledWith("Roof Warranty", expect.any(AbortSignal)));

    resolveSearch({ results: [{
      type: "note",
      title: "Roof Warranty",
      url: "/dashboard/profile-notes?note=roof",
    }] });

    const result = await screen.findByRole("option", { name: /Roof Warranty/i });
    fireEvent.mouseDown(result);

    expect(onNavigate).toHaveBeenCalledWith("/dashboard/profile-notes?note=roof");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("labels file results as incomplete while the catalog is indexing", async () => {
    searchClient.search.mockResolvedValue({ results: [], fileIndexStatus: "indexing" });
    render(
      <Shell navItems={[]} currentPath="/dashboard" onNavigate={vi.fn()} onLogout={vi.fn()}>
        <div>Home</div>
      </Shell>,
    );
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), { target: { value: "invoice" } });
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Files are still indexing"));
    expect(screen.queryByText(/No matches for/)).not.toBeInTheDocument();
  });

  it("distinguishes a failed global search from no matches", async () => {
    searchClient.search.mockRejectedValue(new Error("offline"));
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), { target: { value: "router" } });

    expect(await screen.findByRole("alert")).toHaveTextContent("Search is unavailable");
  });

  it("loads and renders notification feed items", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("/v1/me/notifications")) {
        return new Response(JSON.stringify({
          notifications: [{
            id: "agent-offline",
            user_id: "usr_1",
            home_id: "home_1",
            category: "agent_health",
            event_kind: "agent.offline",
            severity: "warning",
            title: "Agent offline",
            body: "Kitchen Mac has not checked in.",
            target_path: "/dashboard/settings/home",
            collapse_key: "agent:offline",
            outcome: "down",
            occurrence_count: 1,
            first_occurred_at: "2026-06-28T00:00:00Z",
            last_occurred_at: "2026-06-28T00:00:00Z",
            created_at: "2026-06-28T00:00:00Z",
          }],
          unread_count: 1,
        }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response("not found", { status: 404 });
    }));

    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));

    expect(await screen.findByRole("dialog", { name: "Notifications" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("Agent offline")).toBeInTheDocument());
    expect(screen.getByText("Kitchen Mac has not checked in.")).toBeInTheDocument();
    expect(screen.queryByText("No new notifications.")).not.toBeInTheDocument();
  });

  it("closes the notifications popover when clicking outside it", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ notifications: [] }), { headers: { "Content-Type": "application/json" } })));

    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));
    expect(await screen.findByRole("dialog", { name: "Notifications" })).toBeInTheDocument();

    fireEvent.pointerDown(screen.getByText("Dashboard content"));
    expect(document.querySelector(".notif-popover")).toHaveAttribute("data-state", "closing");
    await waitFor(() => expect(document.querySelector(".notif-popover")).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));
    expect(await screen.findByRole("dialog", { name: "Notifications" })).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Notifications" })).not.toBeInTheDocument());
  });

  it("renders the reference sidebar status and user footer", () => {
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
        userDisplayName="Aaron Campbell"
        userEmail="owner@example.com"
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    const footer = screen.getByLabelText("Session status");
    expect(footer).toHaveClass("nav-footer");
    expect(within(footer).getByText("Primary agent online")).toBeInTheDocument();
    expect(within(footer).getByText("AC")).toBeInTheDocument();
    expect(within(footer).getByText("Aaron Campbell")).toHaveAttribute("title", "owner@example.com");
    expect(within(footer).getByText("admin")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toHaveClass("nav-footer-signout");
  });

  it("orders mobile header actions as search, notifications, then menu", () => {
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home", group: "Main" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Dashboard content</div>
      </Shell>,
    );

    const header = screen.getByRole("banner");
    const actionGroup = header.querySelector<HTMLElement>(".topbar-actions");

    expect(actionGroup).not.toBeNull();

    const actions = within(actionGroup!)
      .getAllByRole("button")
      .map((button) => button.getAttribute("aria-label"));

    expect(actions).toEqual(["Open search", "Notifications", "Open menu"]);
  });

  it("partitions daily mobile routes from the overflow menu", () => {
    render(
      <Shell
        navItems={[
          { href: "/dashboard", label: "Home" },
          { href: "/dashboard/hank", label: "Hank" },
          { href: "/dashboard/profile-notes", label: "Notes" },
          { href: "/dashboard/home-assistant", label: "Home Assistant" },
          { href: "/dashboard/file-server", label: "File Server" },
          { href: "/dashboard/agents", label: "Agents" },
          { href: "/dashboard/settings", label: "Settings" },
          { href: "/docs/deployment", label: "Setup Guide" },
        ]}
        currentPath="/dashboard/profile-notes"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Notes content</div>
      </Shell>,
    );

    const primary = screen.getByRole("navigation", { name: "Mobile primary" });
    expect(within(primary).getByRole("link", { name: "Notes" })).toHaveAttribute("aria-current", "page");
    expect(within(primary).getAllByRole("link")).toHaveLength(5);

    fireEvent.click(screen.getByRole("button", { name: "Open menu" }));
    const menu = screen.getByRole("dialog", { name: "Mobile menu" });
    expect(within(menu).getByRole("link", { name: "Agents" })).toBeInTheDocument();
    expect(within(menu).getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(within(menu).getByRole("link", { name: "Setup Guide" })).toBeInTheDocument();
  });

  it("offers installation before sign out and closes the menu after installing", () => {
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Home</div>
      </Shell>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open menu" }));
    const menu = screen.getByRole("dialog", { name: "Mobile menu" });
    const buttons = within(menu).getAllByRole("button");
    expect(buttons.map((button) => button.textContent)).toEqual(["×", "Install Hank", "Sign out"]);

    fireEvent.click(within(menu).getByRole("button", { name: "Install Hank" }));
    expect(screen.queryByRole("dialog", { name: "Mobile menu" })).not.toBeInTheDocument();
  });

  it("dismisses mobile overlays with Escape and restores focus", () => {
    render(
      <Shell
        navItems={[{ href: "/dashboard", label: "Home" }]}
        currentPath="/dashboard"
        onNavigate={vi.fn()}
        onLogout={vi.fn()}
      >
        <div>Home</div>
      </Shell>,
    );

    const menuButton = screen.getByRole("button", { name: "Open menu" });
    fireEvent.click(menuButton);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Mobile menu" })).not.toBeInTheDocument();
    expect(menuButton).toHaveFocus();

    const searchButton = screen.getByRole("button", { name: "Open search" });
    fireEvent.click(searchButton);
    expect(screen.getByRole("button", { name: "Close search" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(searchButton).toHaveFocus();
  });
});
