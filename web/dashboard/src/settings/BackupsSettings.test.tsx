import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider } from "../ui/primitives";
import { BackupsSettings } from "./BackupsSettings";

const api = vi.hoisted(() => ({ status: vi.fn(), queryTelemetry: vi.fn(), reviewPrimaryRestore: vi.fn(), requestPrimaryRestore: vi.fn(), saveConfig: vi.fn() }));
vi.mock("../api/storage", () => ({ storageClient: api }));
vi.mock("../api/logs", () => ({ logsClient: { listAuditEvents: async () => ({ events: [] }) } }));
const config = { target: { path: "/backup" }, restore: { primary_restore_enabled: true, confirmation_phrase: "RESTORE HANK DATABASE" } };
afterEach(cleanup);
beforeEach(() => { vi.resetAllMocks(); api.status.mockResolvedValue({ backup: {backups:[{label:"20260905-020000F"}]}, config }); api.queryTelemetry.mockResolvedValue({ queries: [] }); api.reviewPrimaryRestore.mockResolvedValue({ admin_action_token: "test-only-restore-review" }); api.requestPrimaryRestore.mockResolvedValue({}); api.saveConfig.mockResolvedValue({}); });
function renderSettings() { render(<ConfirmDialogProvider><BackupsSettings /></ConfirmDialogProvider>); }

describe("paired restore", () => {
  it("binds explicit typed confirmation to the reviewed backup", async () => {
    renderSettings();
    fireEvent.click(await screen.findByRole("button", {name:"Restore database and attachments"}));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("20260905-020000F");
    expect(dialog.textContent).toContain("RESTORE HANK DATABASE");
    expect(api.requestPrimaryRestore).not.toHaveBeenCalled();
    fireEvent.change(within(dialog).getByRole("textbox"),{target:{value:"RESTORE HANK DATABASE"}});
    fireEvent.click(within(dialog).getByRole("button",{name:"Restore this backup"}));
    await waitFor(() => expect(api.requestPrimaryRestore).toHaveBeenCalledWith("20260905-020000F","RESTORE HANK DATABASE","test-only-restore-review"));
    expect(api.requestPrimaryRestore).toHaveBeenCalledOnce();
  });
  it("does not request restore for a mismatched phrase or cancellation", async () => {
    renderSettings();
    fireEvent.click(await screen.findByRole("button",{name:"Restore database and attachments"}));
    let dialog = await screen.findByRole("alertdialog");
    fireEvent.change(within(dialog).getByRole("textbox"),{target:{value:"wrong phrase"}});
    fireEvent.click(within(dialog).getByRole("button",{name:"Restore this backup"}));
    expect(await screen.findByText(/confirmation phrase did not match/)).toBeInTheDocument();
    expect(api.requestPrimaryRestore).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button",{name:"Restore database and attachments"}));
    dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button",{name:"Cancel"}));
    expect(api.requestPrimaryRestore).not.toHaveBeenCalled();
  });
  it("preserves the restore policy when editing backup schedules", async () => {
    renderSettings();
    fireEvent.click(await screen.findByRole("button",{name:"Save backup settings"}));
    await waitFor(() => expect(api.saveConfig).toHaveBeenCalledWith(expect.objectContaining({restore:config.restore})));
  });
});
