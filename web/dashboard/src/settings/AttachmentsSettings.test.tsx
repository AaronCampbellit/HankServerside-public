import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { noteAttachmentsClient } from "../api/noteAttachments";
import { ConfirmDialogProvider } from "../ui/primitives";
import { AttachmentsSettings } from "./AttachmentsSettings";

vi.mock("../api/noteAttachments", () => ({
  noteAttachmentsClient: { load: vi.fn(), remove: vi.fn() },
}));

const load = vi.mocked(noteAttachmentsClient.load);
const remove = vi.mocked(noteAttachmentsClient.remove);

function renderPage() {
  return render(<ConfirmDialogProvider><AttachmentsSettings /></ConfirmDialogProvider>);
}

describe("AttachmentsSettings", () => {
  beforeEach(() => {
    load.mockResolvedValue({
      total_files: 1,
      total_bytes: 2_048,
      attachments: [{
        id: "natt-1",
        filename: "site-shot.png",
        content_type: "image/png",
        size_bytes: 2_048,
        created_at: "2026-07-17T12:00:00Z",
        note_id: "work.md",
        note_title: "Work board",
        note_scope: "profile",
        owner_email: "admin@example.com",
        reference_count: 1,
        contexts: ["Task: Verify landing page"],
        download_url: "/v1/home/note-attachments/natt-1/content",
      }],
    });
    remove.mockResolvedValue({ ok: true, note_revision: "rev-2", cleanup_complete: true });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("shows attachment totals, storage details, context, and open controls", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { name: "Attachments" })).toBeInTheDocument();
    expect(screen.getByText("1 file")).toBeInTheDocument();
    expect(screen.getAllByText("2 KB").length).toBeGreaterThan(0);
    const row = screen.getByRole("article", { name: "site-shot.png" });
    expect(within(row).getByText("Work board")).toBeInTheDocument();
    expect(within(row).getByText(/Task: Verify landing page/)).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "Open site-shot.png" })).toHaveAttribute("href", "/v1/home/note-attachments/natt-1/content");
  });

  it("confirms deletion with file impact and refreshes inventory", async () => {
    renderPage();
    const row = await screen.findByRole("article", { name: "site-shot.png" });

    fireEvent.click(within(row).getByRole("button", { name: "Delete site-shot.png" }));
    expect(await screen.findByText(/removes it everywhere it appears/i)).toBeInTheDocument();
    expect(screen.getAllByText(/2 KB/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Delete file" }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("natt-1"));
    expect(load).toHaveBeenCalledTimes(2);
  });
});
