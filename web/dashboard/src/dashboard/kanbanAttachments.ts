import type { KanbanBoard, NoteAttachment } from "../api/profileNotes";

export type AttachmentDeletionPlan = {
  exclusive: NoteAttachment[];
  shared: NoteAttachment[];
};

const attachmentReference = /hank-note-attachment:\/\/([^\s)?&#"'<>]+)/g;

function referencedIDs(text: string): Set<string> {
  const ids = new Set<string>();
  for (const match of text.matchAll(attachmentReference)) {
    try {
      ids.add(decodeURIComponent(match[1]));
    } catch {
      // Malformed or uncertain references are intentionally preserved.
    }
  }
  return ids;
}

export function attachmentDeletionPlan(board: KanbanBoard, removedCardIDs: Set<string>, attachments: NoteAttachment[]): AttachmentDeletionPlan {
  const removedReferences = new Set<string>();
  const survivingReferences = new Set<string>();
  for (const column of board.columns || []) {
    for (const card of column.cards || []) {
      const target = removedCardIDs.has(card.id || "") ? removedReferences : survivingReferences;
      for (const id of referencedIDs(String(card.text || card.title || ""))) target.add(id);
    }
  }
  return {
    exclusive: attachments.filter((attachment) => removedReferences.has(attachment.id) && !survivingReferences.has(attachment.id)),
    shared: attachments.filter((attachment) => removedReferences.has(attachment.id) && survivingReferences.has(attachment.id)),
  };
}

function formatBytes(bytes: number): string {
  if (bytes < 1_024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1_024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1_024; index += 1) {
    value /= 1_024;
    unit = units[index];
  }
  return `${Number.isInteger(value) ? value : value.toFixed(1)} ${unit}`;
}

export function attachmentDeletionMessage(subject: string, plan: AttachmentDeletionPlan): string {
  const parts = [subject];
  if (plan.exclusive.length) {
    const bytes = plan.exclusive.reduce((total, attachment) => total + (attachment.size_bytes || 0), 0);
    parts.push(`This will permanently delete ${plan.exclusive.length} attached ${plan.exclusive.length === 1 ? "file" : "files"} (${formatBytes(bytes)}): ${plan.exclusive.map((attachment) => attachment.filename).join(", ")}.`);
  }
  if (plan.shared.length) {
    parts.push(`${plan.shared.length} shared ${plan.shared.length === 1 ? "file" : "files"} will be kept because another task uses ${plan.shared.length === 1 ? "it" : "them"}.`);
  }
  parts.push("This can't be undone.");
  return parts.join(" ");
}
