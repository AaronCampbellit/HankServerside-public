import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NotificationInbox } from "./NotificationInbox";
import type { NotificationContextValue } from "./NotificationProvider";

const api = vi.hoisted(() => ({
  list: vi.fn(), markRead: vi.fn(), markAllRead: vi.fn(), remove: vi.fn(), clear: vi.fn(),
}));

vi.mock("../api/notifications", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/notifications")>()),
  notificationsClient: api,
}));

const item = {
  id: "ntf_1", user_id: "usr_1", home_id: "home_1", audit_event_id: "audit_1", category: "agent_health" as const,
  event_kind: "agent.offline", severity: "warning", title: "Agent offline", body: "Kitchen Mac has not checked in.",
  target_path: "/dashboard/settings/home", collapse_key: "agent:offline", outcome: "down", occurrence_count: 3,
  first_occurred_at: "2026-08-19T00:00:00Z", last_occurred_at: "2026-08-19T00:05:00Z", created_at: "2026-08-19T00:00:00Z",
};

function notificationContext(overrides: Partial<NotificationContextValue> = {}): NotificationContextValue {
  return {
    status: "ready", deliveryState: "subscribed", settings: null, config: null, unreadCount: 1, inboxVersion: 0, error: "",
    enable: vi.fn(), disable: vi.fn(), refresh: vi.fn(), refreshInbox: vi.fn(), setUnreadCount: vi.fn(),
    updateCategory: vi.fn(), dismissInvitation: vi.fn(), ...overrides,
  };
}

describe("NotificationInbox", () => {
  beforeEach(() => {
    api.list.mockResolvedValue({ notifications: [item], next_cursor: "next page", unread_count: 1 });
    api.markRead.mockResolvedValue({ ok: true, unread_count: 0 });
    api.markAllRead.mockResolvedValue({ updated: 1, unread_count: 0 });
    api.remove.mockResolvedValue({ ok: true, unread_count: 0 });
    api.clear.mockResolvedValue({ deleted: 1, unread_count: 0 });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it("loads durable entries, coalesced counts, and another page on demand", async () => {
    api.list
      .mockResolvedValueOnce({ notifications: [item], next_cursor: "next page", unread_count: 1 })
      .mockResolvedValueOnce({ notifications: [{ ...item, id: "ntf_2", title: "Agent recovered", occurrence_count: 1 }], unread_count: 1 });
    render(<NotificationInbox context={notificationContext()} onClose={vi.fn()} onNavigate={vi.fn()} />);

    expect(await screen.findByText("Agent offline")).toBeInTheDocument();
    expect(screen.getByText("×3")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more notifications" }));
    expect(await screen.findByText("Agent recovered")).toBeInTheDocument();
    expect(api.list).toHaveBeenLastCalledWith({ cursor: "next page", limit: 50 });
  });

  it("marks one read before navigating and updates the shared unread count", async () => {
    const onNavigate = vi.fn();
    const context = notificationContext();
    render(<NotificationInbox context={context} onClose={vi.fn()} onNavigate={onNavigate} />);
    fireEvent.click(await screen.findByRole("button", { name: "Open Agent offline" }));
    await waitFor(() => expect(api.markRead).toHaveBeenCalledWith("ntf_1"));
    expect(context.setUnreadCount).toHaveBeenCalledWith(0);
    expect(onNavigate).toHaveBeenCalledWith("/dashboard/settings/home");
  });

  it("opens the exact log event from the View action", async () => {
    const onNavigate = vi.fn();
    render(<NotificationInbox context={notificationContext()} onClose={vi.fn()} onNavigate={onNavigate} />);
    fireEvent.click(await screen.findByRole("button", { name: "View log for Agent offline" }));
    await waitFor(() => expect(api.markRead).toHaveBeenCalledWith("ntf_1"));
    expect(onNavigate).toHaveBeenCalledWith("/dashboard/notifications/event?notification=ntf_1");
  });

  it("marks all read, removes one, and confirms clear all", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const context = notificationContext();
    render(<NotificationInbox context={context} onClose={vi.fn()} onNavigate={vi.fn()} />);
    await screen.findByText("Agent offline");

    fireEvent.click(screen.getByRole("button", { name: "Mark all read" }));
    await waitFor(() => expect(api.markAllRead).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole("button", { name: "Delete Agent offline" }));
    await waitFor(() => expect(api.remove).toHaveBeenCalledWith("ntf_1"));

    cleanup();
    api.list.mockResolvedValueOnce({ notifications: [item], unread_count: 1 });
    render(<NotificationInbox context={notificationContext()} onClose={vi.fn()} onNavigate={vi.fn()} />);
    await screen.findByText("Agent offline");
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    await waitFor(() => expect(api.clear).toHaveBeenCalledOnce());
  });

  it("rolls back an optimistic read when the server mutation fails", async () => {
    api.markRead.mockRejectedValue(new Error("offline"));
    render(<NotificationInbox context={notificationContext()} onClose={vi.fn()} onNavigate={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Mark Agent offline read" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("offline");
    expect(screen.getByRole("button", { name: "Mark Agent offline read" })).toBeInTheDocument();
  });

  it("shows empty and retryable error states", async () => {
    api.list.mockRejectedValueOnce(new Error("unavailable"));
    render(<NotificationInbox context={notificationContext()} onClose={vi.fn()} onNavigate={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("unavailable");
    api.list.mockResolvedValueOnce({ notifications: [], unread_count: 0 });
    fireEvent.click(screen.getByRole("button", { name: "Retry notifications" }));
    expect(await screen.findByText("No notifications yet.")).toBeInTheDocument();
  });
});
