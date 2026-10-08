import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider } from "../ui/primitives";
import { FileJobOwnerAssignment } from "./FileJobOwnerAssignment";

const api = vi.hoisted(() => ({ listAgents: vi.fn(), reviewJobOwner: vi.fn(), assignJobOwner: vi.fn() }));
vi.mock("../api/agents", async (original) => ({ ...await original<typeof import("../api/agents")>(), agentsClient: { listAgents: api.listAgents } }));
vi.mock("../api/fileServer", () => ({ fileServerClient: api }));
const job = { id: "historic", operation: "move", status: "rollback_required", source_id: "source", destination_source_id: "destination", from_path: "/original", to_path: "/copy", updated_at: "2026-09-05T00:00:00Z" };
afterEach(cleanup);
beforeEach(() => { vi.resetAllMocks(); api.listAgents.mockResolvedValue([{ agent_id: "worker-1", name: "Garage", status: "offline" }]); api.reviewJobOwner.mockResolvedValue({ admin_action_token: "test-only-review-token" }); api.assignJobOwner.mockResolvedValue({ ...job, agent_id: "worker-1" }); });

describe("historical file job ownership", () => {
  it("requires an explicit machine choice and confirmation before saving", async () => {
    const onSaved = vi.fn();
    render(<ConfirmDialogProvider><FileJobOwnerAssignment job={job} onSaved={onSaved} /></ConfirmDialogProvider>);
    fireEvent.click(screen.getByRole("button", { name: "Review owner" }));
    const select = await screen.findByLabelText("Machine that performed this move");
    expect((select as HTMLSelectElement).value).toBe("");
    expect(screen.getByRole("button", { name: "Review and confirm" })).toBeDisabled();
    fireEvent.change(select, { target: { value: "worker-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Review and confirm" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Garage (worker-1)");
    expect(dialog.textContent).toContain("source:/original");
    expect(dialog.textContent).toContain("destination:/copy");
    expect(api.assignJobOwner).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm owner" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    expect(api.assignJobOwner).toHaveBeenCalledWith(job.id, "worker-1", job.updated_at, "test-only-review-token");
  });

  it("does not assign when confirmation is cancelled and displays stale-review failures", async () => {
    render(<ConfirmDialogProvider><FileJobOwnerAssignment job={job} onSaved={vi.fn()} /></ConfirmDialogProvider>);
    fireEvent.click(screen.getByRole("button", { name: "Review owner" }));
    fireEvent.change(await screen.findByLabelText("Machine that performed this move"), { target: { value: "worker-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Review and confirm" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Review and confirm" })).not.toBeDisabled());
    expect(api.assignJobOwner).not.toHaveBeenCalled();
    api.reviewJobOwner.mockRejectedValueOnce(new Error("This job changed. Refresh its history."));
    fireEvent.click(screen.getByRole("button", { name: "Review and confirm" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This job changed");
    expect(api.assignJobOwner).not.toHaveBeenCalled();
  });
});
