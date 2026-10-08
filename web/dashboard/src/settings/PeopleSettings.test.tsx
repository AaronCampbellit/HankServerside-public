import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider } from "../ui/primitives";
import { PeopleSettings } from "./PeopleSettings";

const bootstrapClient = vi.hoisted(() => ({ load: vi.fn() }));
const peopleClient = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listInvitations: vi.fn(),
  createInvitation: vi.fn(),
  cancelInvitation: vi.fn(),
  removeMember: vi.fn(),
  resetPassword: vi.fn(),
  updateDisplayName: vi.fn(),
}));

vi.mock("../api/bootstrap", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/bootstrap")>()),
  bootstrapClient,
}));

vi.mock("../api/people", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/people")>()),
  peopleClient,
}));

const owner = {
  user_id: "usr_owner",
  email: "owner@example.com",
  display_name: "Aaron Campbell",
  role: "admin",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const member = {
  user_id: "usr_member",
  email: "member@example.com",
  display_name: "",
  role: "member",
  created_at: "2026-08-02T00:00:00Z",
  updated_at: "2026-08-02T00:00:00Z",
};

function renderSettings() {
  return render(<ConfirmDialogProvider><PeopleSettings /></ConfirmDialogProvider>);
}

describe("PeopleSettings display names", () => {
  beforeEach(() => {
    bootstrapClient.load.mockResolvedValue({
      user: { id: owner.user_id, email: owner.email, display_name: owner.display_name },
      membership: { role: "admin" },
      permissions: { can_manage_people: true },
    });
    peopleClient.listMembers.mockResolvedValue({ members: [owner, member] });
    peopleClient.listInvitations.mockResolvedValue({ invitations: [] });
    peopleClient.updateDisplayName.mockImplementation(async (userID: string, displayName: string) => ({
      ...(userID === owner.user_id ? owner : member),
      display_name: displayName,
    }));
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
    window.history.replaceState({}, "", "/dashboard/settings/people");
  });

  it("focuses the exact member targeted by global search", async () => {
    window.history.replaceState({}, "", "/dashboard/settings/people?member=member%40example.com");

    renderSettings();

    const members = await screen.findByRole("list", { name: "Home members" });
    const memberRow = within(members).getByText("member@example.com").closest("article");
    expect(memberRow).not.toBeNull();
    await waitFor(() => expect(memberRow).toHaveFocus());
    expect(memberRow).toHaveAttribute("aria-current", "true");
  });

  it("shows display names with email identity and lets an admin rename any member", async () => {
    renderSettings();

    const members = await screen.findByRole("list", { name: "Home members" });
    expect(within(members).getByText("Aaron Campbell")).toBeInTheDocument();
    expect(within(members).getByText("owner@example.com")).toBeInTheDocument();
    expect(within(members).getByText("member@example.com")).toBeInTheDocument();

    fireEvent.click(within(members).getByRole("button", { name: "Edit display name for member@example.com" }));
    const input = screen.getByRole("textbox", { name: "Display name for member@example.com" });
    fireEvent.change(input, { target: { value: "Family Member" } });
    fireEvent.click(screen.getByRole("button", { name: "Save display name" }));

    await waitFor(() => expect(peopleClient.updateDisplayName).toHaveBeenCalledWith("usr_member", "Family Member"));
    expect(await within(members).findByText("Family Member")).toBeInTheDocument();
    expect(within(members).getByText("member@example.com")).toBeInTheDocument();
  });

  it("lets a member rename only themselves and clearing restores the email fallback", async () => {
    bootstrapClient.load.mockResolvedValue({
      user: { id: member.user_id, email: member.email, display_name: "" },
      membership: { role: "member" },
      permissions: { can_manage_people: false },
    });
    renderSettings();

    const members = await screen.findByRole("list", { name: "Home members" });
    expect(within(members).queryByRole("button", { name: "Edit display name for Aaron Campbell" })).not.toBeInTheDocument();
    fireEvent.click(within(members).getByRole("button", { name: "Edit display name for member@example.com" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Display name for member@example.com" }), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save display name" }));

    await waitFor(() => expect(peopleClient.updateDisplayName).toHaveBeenCalledWith("usr_member", ""));
    expect(within(members).getByText("member@example.com")).toBeInTheDocument();
  });
});
