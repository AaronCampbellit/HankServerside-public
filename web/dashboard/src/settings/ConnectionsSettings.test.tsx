import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider } from "../ui/primitives";
import { ConnectionsSettings } from "./ConnectionsSettings";

const bootstrapClient = vi.hoisted(() => ({ load: vi.fn() }));
const connectionsClient = vi.hoisted(() => ({ listProfiles: vi.fn(), saveProfile: vi.fn(), testSMB: vi.fn() }));

vi.mock("../api/bootstrap", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/bootstrap")>()),
  bootstrapClient,
}));

vi.mock("../api/connections", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/connections")>()),
  connectionsClient,
}));

const smbConfig = {
  active_source_id: "media",
  host: "nas.local",
  share: "media",
  username: "aaron",
  folders: [{ id: "local", root: "/srv/files" }],
  shares: [
    { id: "media", name: "Media", host: "nas.local", share: "media", username: "aaron", password_set: true, policy: { delete: false } },
    { id: "archive", name: "Archive", host: "backup.local", share: "archive", username: "backup", password_set: true, policy: { write: false } },
  ],
};

function renderSettings() {
  return render(<ConfirmDialogProvider><ConnectionsSettings /></ConfirmDialogProvider>);
}

describe("ConnectionsSettings SMB management", () => {
  beforeEach(() => {
    bootstrapClient.load.mockResolvedValue({ permissions: { can_manage_settings: true } });
    connectionsClient.listProfiles.mockResolvedValue({
      profiles: [{ service_type: "smb", public_config_json: JSON.stringify(smbConfig), status: "healthy", applied_version: 2 }],
    });
    connectionsClient.saveProfile.mockResolvedValue({});
    connectionsClient.testSMB.mockResolvedValue({ ok: true });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("selects and saves one share while preserving every other source", async () => {
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: "Edit Archive" }));
    expect(screen.getByLabelText("Share name")).toHaveValue("archive");
    expect(screen.getByLabelText("Server address")).toHaveValue("backup.local");
    fireEvent.change(screen.getByLabelText("Share label"), { target: { value: "Cold Archive" } });
    fireEvent.click(screen.getByRole("button", { name: "Save File Server" }));

    await waitFor(() => expect(connectionsClient.saveProfile).toHaveBeenCalledTimes(1));
    const input = connectionsClient.saveProfile.mock.calls[0][1];
    expect(input.secrets).toBeUndefined();
    expect(input.public_config.folders).toBeUndefined();
    expect(input.public_config.shares).toEqual([
      smbConfig.shares[0],
      { ...smbConfig.shares[1], name: "Cold Archive", domain: "" },
    ]);
  });

  it("adds a draft and tests it without saving", async () => {
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: "Add SMB share" }));
    expect(screen.getByLabelText("Share label")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Share label"), { target: { value: "Projects" } });
    fireEvent.change(screen.getByLabelText("Server address"), { target: { value: "smb://projects.local/projects" } });
    fireEvent.change(screen.getByLabelText("Share name"), { target: { value: "projects" } });
    fireEvent.change(screen.getByLabelText("SMB password"), { target: { value: "draft-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Test Connection" }));

    await waitFor(() => expect(connectionsClient.testSMB).toHaveBeenCalledWith({
      id: "smb-1",
      name: "Projects",
      host: "projects.local",
      share: "projects",
      username: "",
      password: "draft-secret",
      domain: "",
    }));
    expect(connectionsClient.saveProfile).not.toHaveBeenCalled();
    expect(await screen.findByText("Connection to Projects succeeded.")).toBeInTheDocument();
  });

  it("confirms removal and saves only the remaining shares", async () => {
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: "Edit Media" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove SMB Share" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Remove SMB share" });
    expect(within(dialog).getByText(/File Server access through this source will stop/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove share" }));

    await waitFor(() => expect(connectionsClient.saveProfile).toHaveBeenCalledTimes(1));
    const input = connectionsClient.saveProfile.mock.calls[0][1];
    expect(input.public_config.shares).toEqual([smbConfig.shares[1]]);
    expect(input.public_config.active_source_id).toBe("archive");
    expect(input.public_config.folders).toBeUndefined();
  });
});
