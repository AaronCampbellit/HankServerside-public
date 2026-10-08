import { describe, expect, it } from "vitest";
import type { KanbanBoard, NoteAttachment } from "../api/profileNotes";
import { attachmentDeletionMessage, attachmentDeletionPlan } from "./kanbanAttachments";

const attachments: NoteAttachment[] = [
  { id: "exclusive", filename: "site.png", content_type: "image/png", size_bytes: 2_048, download_url: "/exclusive", markdown_reference: "![site.png](hank-note-attachment://exclusive)" },
  { id: "shared", filename: "field.pdf", content_type: "application/pdf", size_bytes: 1_024, download_url: "/shared", markdown_reference: "[field.pdf](hank-note-attachment://shared)" },
];

const board: KanbanBoard = { columns: [{
  id: "todo", title: "To do", cards: [
    { id: "one", text: "Site review\n![site](hank-note-attachment://exclusive)\n![again](hank-note-attachment://exclusive)\n[field](hank-note-attachment://shared)\n![unknown](hank-note-attachment://missing)" },
    { id: "two", text: "Follow up\n[field](hank-note-attachment://shared)" },
  ],
}] };

describe("kanban attachment deletion planning", () => {
  it("classifies unique exclusive and shared files while preserving unknown IDs", () => {
    expect(attachmentDeletionPlan(board, new Set(["one"]), attachments)).toEqual({
      exclusive: [attachments[0]],
      shared: [attachments[1]],
    });
  });

  it("handles deleting every card in a column", () => {
    const plan = attachmentDeletionPlan(board, new Set(["one", "two"]), attachments);

    expect(plan.exclusive.map((item) => item.id)).toEqual(["exclusive", "shared"]);
    expect(plan.shared).toEqual([]);
  });

  it("formats a permanent deletion prompt with totals and shared preservation", () => {
    const message = attachmentDeletionMessage("Delete “Site review”?", {
      exclusive: [attachments[0]],
      shared: [attachments[1]],
    });

    expect(message).toContain("1 attached file (2 KB): site.png");
    expect(message).toContain("1 shared file will be kept");
    expect(message).toContain("can't be undone");
  });
});
