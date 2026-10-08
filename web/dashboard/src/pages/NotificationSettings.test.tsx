import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NotificationSettingsPage } from "./NotificationSettings";

const context = vi.hoisted(() => ({
  status: "ready",
  deliveryState: "available",
  settings: {
    user_id: "usr_1", monitoring: true, agent_health: true, quick_links: true, storage: true, notes: true,
    dashboard_entities: true, updated_at: "2026-08-19T00:00:00Z",
  },
  error: "",
  enable: vi.fn(),
  disable: vi.fn(),
  refresh: vi.fn(),
  updateCategory: vi.fn(),
}));

vi.mock("../notifications/NotificationProvider", () => ({ useNotifications: () => context }));

describe("NotificationSettingsPage", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("shows device delivery and all six default-on categories", () => {
    render(<NotificationSettingsPage />);
    expect(screen.getByRole("heading", { name: "Notifications" })).toBeInTheDocument();
    expect(screen.getByText(/inbox entries are still retained/i)).toBeInTheDocument();
    for (const name of ["Server monitoring", "Agent health", "Quick links", "Storage", "Shared notes", "Home Assistant"]) {
      expect(screen.getByRole("checkbox", { name })).toBeChecked();
    }
    expect(screen.getByRole("button", { name: "Enable notifications" })).toBeInTheDocument();
  });

  it("updates a category and exposes retry and disable controls for relevant states", async () => {
    render(<NotificationSettingsPage />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Shared notes" }));
    await waitFor(() => expect(context.updateCategory).toHaveBeenCalledWith("notes", false));
    fireEvent.click(screen.getByRole("checkbox", { name: "Server monitoring" }));
    await waitFor(() => expect(context.updateCategory).toHaveBeenCalledWith("monitoring", false));
  });
});
