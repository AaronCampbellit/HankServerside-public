import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type NoteAttachmentInventoryItem = {
  id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  created_at: string;
  note_id: string;
  note_title: string;
  note_scope: string;
  owner_email: string;
  reference_count: number;
  contexts: string[];
  download_url: string;
};

export type NoteAttachmentInventory = {
  total_files: number;
  total_bytes: number;
  attachments: NoteAttachmentInventoryItem[];
};

export type NoteAttachmentDeleteResponse = {
  ok: boolean;
  note_revision: string;
  cleanup_complete: boolean;
};

export class NoteAttachmentsClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  async load(): Promise<NoteAttachmentInventory> {
    const payload = await this.api.request<Partial<NoteAttachmentInventory>>("/v1/home/note-attachments");
    return {
      total_files: Number(payload.total_files || 0),
      total_bytes: Number(payload.total_bytes || 0),
      attachments: arrayFrom<NoteAttachmentInventoryItem>(payload.attachments),
    };
  }

  remove(attachmentID: string) {
    return this.api.request<NoteAttachmentDeleteResponse>(`/v1/home/note-attachments/${encodeURIComponent(attachmentID)}`, { method: "DELETE" });
  }
}

export const noteAttachmentsClient = new NoteAttachmentsClient();
