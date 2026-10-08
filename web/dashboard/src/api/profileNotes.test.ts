import { describe, expect, it, vi } from "vitest";
import { ProfileNotesClient } from "./profileNotes";
import type { ApiTransport } from "./client";

describe("ProfileNotesClient", () => {
  it("lists fetches saves and deletes profile notes", async () => {
    const request = vi.fn(async <T>() => ({}) as T);
    const client = new ProfileNotesClient({ request: request as unknown as ApiTransport["request"] });

    await client.listNotes();
    await client.fetchNote("daily.md");
    await client.saveNote({
      note_id: "",
      title: "Daily",
      body_markdown: "Remember milk",
      expected_revision: "",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
    });
    await client.saveNote({
      note_id: "daily.md",
      title: "Daily",
      body_markdown: "Remember eggs",
      expected_revision: "2",
      page_type: "text",
      parent_id: "",
      mcp_excluded: true,
      pinned: true,
    });
    await client.deleteNote("daily.md");

    expect(request).toHaveBeenNthCalledWith(1, "/v1/me/notes");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/me/notes/daily.md");
    expect(request).toHaveBeenNthCalledWith(3, "/v1/me/notes", {
      method: "POST",
      body: {
        note_id: "",
        title: "Daily",
        content: "Remember milk",
        body_markdown: "Remember milk",
        body_format: "markdown",
        expected_revision: "",
        page_type: "text",
        parent_id: "",
        mcp_excluded: false,
        pinned: false,
      },
    });
    expect(request).toHaveBeenNthCalledWith(4, "/v1/me/notes/daily.md", {
      method: "PUT",
      body: {
        note_id: "daily.md",
        title: "Daily",
        content: "Remember eggs",
        body_markdown: "Remember eggs",
        body_format: "markdown",
        expected_revision: "2",
        page_type: "text",
        parent_id: "",
        mcp_excluded: true,
        pinned: true,
      },
    });
    expect(request).toHaveBeenNthCalledWith(5, "/v1/me/notes/daily.md", { method: "DELETE" });
  });

  it("persists kanban board data and uploads note attachments as binary", async () => {
    const request = vi.fn(async <T>() => ({
      id: "natt-1",
      filename: "board.png",
      content_type: "image/png",
      download_url: "/v1/me/notes/work/attachments/natt-1",
      markdown_reference: "![board.png](hank-note-attachment://natt-1)",
    }) as T);
    const client = new ProfileNotesClient({ request: request as unknown as ApiTransport["request"] });
    const board = {
      intake_column_id: "todo",
      columns: [{
        id: "todo",
        title: "To do",
        role: "planning",
        sort_order: 0,
        cards: [{ id: "task", text: "Ship it", sort_order: 0, color: "cyan", due_date: "2026-07-18", tags: ["Hank", "Release"] }],
      }],
    };

    await client.saveNote({
      note_id: "work",
      title: "Work",
      body_markdown: "",
      expected_revision: "1",
      page_type: "kanban",
      parent_id: "",
      board,
    });
    const file = new File([new Uint8Array([137, 80, 78, 71])], "board.png", { type: "image/png" });
    const attachment = await client.uploadAttachment("work", file);
    await client.deleteAttachment("work", "natt-1");

    expect(request).toHaveBeenNthCalledWith(1, "/v1/me/notes/work", {
      method: "PUT",
      body: expect.objectContaining({ board }),
    });
    expect(request).toHaveBeenNthCalledWith(2, "/v1/me/notes/work/attachments?filename=board.png", {
      method: "POST",
      headers: { "Content-Type": "image/png" },
      body: file,
    });
    expect(request).toHaveBeenNthCalledWith(3, "/v1/me/notes/work/attachments/natt-1", { method: "DELETE" });
    expect(attachment.markdown_reference).toContain("hank-note-attachment://natt-1");
  });

  it("adds an optional expected revision to conditional deletes", async () => {
    const request = vi.fn(async <T>() => ({ ok: true }) as T);
    const client = new ProfileNotesClient({ request: request as unknown as ApiTransport["request"] });

    await client.deleteNote("work.md", "rev-2");

    expect(request).toHaveBeenCalledWith("/v1/me/notes/work.md", {
      method: "DELETE",
      headers: { "X-Hank-Expected-Revision": "rev-2" },
    });
  });

  it("lists, restores, and permanently deletes trashed notes", async () => {
    const request = vi.fn(async <T>() => ({ notes: [], ok: true }) as T);
    const client = new ProfileNotesClient({ request: request as unknown as ApiTransport["request"] });

    await client.listTrash();
    await client.restoreNote("old note.md");
    await client.permanentlyDeleteNote("old note.md");

    expect(request).toHaveBeenNthCalledWith(1, "/v1/me/notes/trash");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/me/notes/old%20note.md/restore", { method: "POST" });
    expect(request).toHaveBeenNthCalledWith(3, "/v1/me/notes/old%20note.md/permanent", { method: "DELETE" });
  });
});
