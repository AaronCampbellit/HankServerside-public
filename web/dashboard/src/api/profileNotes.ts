import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type ProfileNoteSummary = {
  id?: string;
  note_id?: string;
  title?: string;
  preview?: string;
  revision?: string;
  updated_at?: string;
  deleted_at?: string;
  page_type?: "text" | "kanban" | "notebook" | string;
  parent_id?: string;
  pinned?: boolean;
  shared?: boolean;
  mcp_excluded?: boolean;
  offline_state?: "clean" | "queued" | "syncing" | "conflicted" | "deletion_review" | "failed";
};

export type KanbanCard = {
  id?: string;
  text?: string;
  title?: string;
  sort_order?: number;
  color?: string;
  due_date?: string;
  tags?: string[];
  priority?: "low" | "medium" | "high" | string;
  created_at?: string;
  updated_at?: string;
};

export type KanbanColumn = {
  id?: string;
  title?: string;
  role?: "planning" | "active" | "rework" | "human" | "review" | "complete" | string;
  sort_order?: number;
  cards?: KanbanCard[];
};

export type KanbanBoard = {
  intake_column_id?: string;
  columns?: KanbanColumn[];
};

export type NoteAttachment = {
  id: string;
  note_id?: string;
  note_revision?: string;
  filename: string;
  content_type: string;
  size_bytes?: number;
  download_url: string;
  preview_url?: string;
  markdown_reference: string;
};

export type NoteAttachmentDeleteResponse = {
  ok: boolean;
  note_revision: string;
  cleanup_complete: boolean;
};

export type ProfileNote = ProfileNoteSummary & {
  content?: string;
  body_markdown?: string;
  body_format?: string;
  board?: KanbanBoard | null;
  attachments?: NoteAttachment[];
};

export type SaveProfileNoteInput = {
  note_id: string;
  title: string;
  body_markdown: string;
  expected_revision: string;
  page_type: string;
  parent_id: string;
  pinned?: boolean;
  mcp_excluded?: boolean;
  board?: KanbanBoard | null;
};

export type SaveProfileNoteResponse = {
  note_id: string;
  revision: string;
  updated_at?: string;
};

export function noteID(note: ProfileNoteSummary): string {
  return note.note_id || note.id || "";
}

export class ProfileNotesClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async listNotes(): Promise<{ notes: ProfileNoteSummary[] }> {
    const payload = await this.api.request<{ notes?: ProfileNoteSummary[] }>("/v1/me/notes");
    return { notes: arrayFrom<ProfileNoteSummary>(payload.notes) };
  }

  async listTrash(): Promise<{ notes: ProfileNoteSummary[] }> {
    const payload = await this.api.request<{ notes?: ProfileNoteSummary[] }>("/v1/me/notes/trash");
    return { notes: arrayFrom<ProfileNoteSummary>(payload.notes) };
  }

  restoreNote(id: string) {
    return this.api.request<{ ok: boolean }>(`/v1/me/notes/${encodeURIComponent(id)}/restore`, { method: "POST" });
  }

  permanentlyDeleteNote(id: string) {
    return this.api.request<{ ok: boolean; cleanup_complete?: boolean }>(`/v1/me/notes/${encodeURIComponent(id)}/permanent`, { method: "DELETE" });
  }

  fetchNote(id: string) {
    return this.api.request<ProfileNote>(`/v1/me/notes/${encodeURIComponent(id)}`);
  }

  saveNote(input: SaveProfileNoteInput) {
    const body: Record<string, unknown> = {
      note_id: input.note_id,
      title: input.title,
      content: input.body_markdown,
      body_markdown: input.body_markdown,
      body_format: "markdown",
      expected_revision: input.expected_revision,
      page_type: input.page_type,
      parent_id: input.parent_id,
      pinned: Boolean(input.pinned),
      mcp_excluded: Boolean(input.mcp_excluded),
    };
    if (input.page_type === "kanban") body.board = input.board || { columns: [] };
    if (input.note_id) {
      return this.api.request<SaveProfileNoteResponse>(`/v1/me/notes/${encodeURIComponent(input.note_id)}`, {
        method: "PUT",
        body,
      });
    }
    return this.api.request<SaveProfileNoteResponse>("/v1/me/notes", { method: "POST", body });
  }

  deleteNote(id: string, expectedRevision = "") {
    const headers = expectedRevision ? { "X-Hank-Expected-Revision": expectedRevision } : undefined;
    return this.api.request<{ ok: boolean }>(`/v1/me/notes/${encodeURIComponent(id)}`, {
      method: "DELETE",
      ...(headers ? { headers } : {}),
    });
  }

  uploadAttachment(noteID: string, file: File) {
    return this.api.request<NoteAttachment>(
      `/v1/me/notes/${encodeURIComponent(noteID)}/attachments?filename=${encodeURIComponent(file.name)}`,
      {
        method: "POST",
        headers: { "Content-Type": file.type || "application/octet-stream" },
        body: file,
      },
    );
  }

  deleteAttachment(noteID: string, attachmentID: string) {
    return this.api.request<NoteAttachmentDeleteResponse>(
      `/v1/me/notes/${encodeURIComponent(noteID)}/attachments/${encodeURIComponent(attachmentID)}`,
      { method: "DELETE" },
    );
  }
}

export const profileNotesClient = new ProfileNotesClient();
