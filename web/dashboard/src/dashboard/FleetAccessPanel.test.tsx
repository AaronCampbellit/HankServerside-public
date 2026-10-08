import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "../api/client";
import { ConfirmDialogProvider } from "../ui/primitives";
import { FleetAccessPanel } from "./FleetAccessPanel";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const operations = ["workspace.read", "workspace.write", "job.run", "job.read", "job.cancel"];
const account = { id: "grant_123", user_id: "user", agents: [], operations, expires_at: null, state: "pending", mcp_account: true };
function panel(isAdmin = true) {
  return <ConfirmDialogProvider><FleetAccessPanel agents={[]} isAdmin={isAdmin} userID="user" /></ConfirmDialogProvider>;
}

describe("Account device access", () => {
  it.each(["1_month", "3_months", "6_months", "1_year", "infinite"])("requests %s access without connection, device or permission choices", async (duration) => {
    const calls: { path: string; body: unknown }[] = [];
    vi.spyOn(apiClient, "request").mockImplementation(async (path, options = {}) => {
      calls.push({ path, body: options.body });
      if (path.endsWith("/approve")) return { confirmation: "APPROVE grant_123", action_token: "review_123", authority: "Commands run as root" };
      if (options.method === "POST") return { grant: account };
      return { grants: [] };
    });
    render(panel());
    await screen.findByLabelText("Access duration");
    expect(screen.queryByLabelText("MCP connection")).toBeNull();
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Access duration"), { target: { value: duration } });
    fireEvent.click(screen.getByRole("button", { name: "Review and enable access" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("all current and future Home devices");
    expect(dialog.textContent).toContain("every MCP app connected to this account");
    expect(dialog.textContent).toContain("Commands run as root");
    expect(calls.filter((call) => call.path.endsWith("/approve"))).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Enable access" }));
    await waitFor(() => expect(calls.filter((call) => call.path.endsWith("/approve"))).toHaveLength(2));
    expect(calls.find((call) => call.path === "/v1/fleet/grants" && call.body)?.body).toEqual({ mcp_account: true, duration });
    expect(calls.some((call) => call.path === "/v1/me/mcp")).toBe(false);
  });

  it("lets a member request account access without self-approval", async () => {
    const request = vi.spyOn(apiClient, "request").mockImplementation(async (_path, options = {}) => options.method === "POST" ? { grant: account } : { grants: [] });
    render(panel(false));
    fireEvent.click(await screen.findByRole("button", { name: "Request account access" }));
    await screen.findByText("Request saved. A Home administrator must approve it.");
    expect(request.mock.calls.some(([path]) => path.endsWith("/approve"))).toBe(false);
  });

  it("keeps a cancelled review pending and offers approval instead of another request", async () => {
    let created = false;
    const request = vi.spyOn(apiClient, "request").mockImplementation(async (path, options = {}) => {
      if (path.endsWith("/approve")) return { confirmation: "APPROVE grant_123", action_token: "review_123" };
      if (options.method === "POST") { created = true; return { grant: account }; }
      return { grants: created ? [account] : [] };
    });
    render(panel());
    fireEvent.click(await screen.findByRole("button", { name: "Review and enable access" }));
    await screen.findByRole("alertdialog");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await screen.findByRole("button", { name: "Approve" });
    expect(screen.queryByRole("button", { name: "Review and enable access" })).toBeNull();
    expect(request.mock.calls.filter(([path]) => path.endsWith("/approve"))).toHaveLength(1);
  });

  it("shares an enabled account approval without showing a connection selector", async () => {
    vi.spyOn(apiClient, "request").mockResolvedValue({ grants: [{ ...account, state: "approved" }] });
    render(panel());
    await screen.findByText("Your account has device access.");
    expect(screen.getByText("All devices · Full access · All connected MCP apps")).toBeDefined();
    expect(screen.queryByLabelText("Access duration")).toBeNull();
    expect(screen.getByRole("button", { name: "Revoke" })).toBeDefined();
  });

  it("allows expired approvals to be dismissed and a new account review requested", async () => {
    const request = vi.spyOn(apiClient, "request").mockResolvedValue({ grants: [{ ...account, state: "approved", expires_at: "2020-01-01T00:00:00Z" }] });
    render(panel());
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(request).toHaveBeenCalledWith("/v1/fleet/grants/grant_123/dismiss", { method: "POST", body: {} }));
    expect(screen.getByRole("button", { name: "Review and enable access" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Revoke" })).toBeNull();
  });

  it("identifies other accounts by name and keeps legacy approval scope visible", async () => {
    vi.spyOn(apiClient, "request").mockResolvedValue({ grants: [{ ...account, user_id: "other", requester_name: "Alex", mcp_account: false, mcp_token_id: "old-app", agents: ["old-device"], operations: ["job.read"] }] });
    render(panel());
    await screen.findByText("Alex");
    expect(screen.getByText("Earlier app approval")).toBeDefined();
    expect(screen.getByText("job.read")).toBeDefined();
    expect(screen.getByRole("button", { name: "Review and enable access" })).toBeDefined();
  });
});
