import { apiClient } from "../api/client";
import { fileServerClient, childPath } from "../api/fileServer";
import type { HankAIClientToolRequest, HankAIClientToolResult } from "../api/hankAI";
import type { HankAIDraftAttachment } from "./hankAIWorkflow";

class HankAIClientToolError extends Error {
  constructor(message: string, readonly result: Record<string, unknown>) {
    super(message);
    this.name = "HankAIClientToolError";
  }
}

function stringValue(value: unknown, fallback = ""): string {
  return typeof value === "string" && value.trim() ? value.trim() : fallback;
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.map((item) => String(item || "").trim()).filter(Boolean) : [];
}

function uniqueCopyName(filename: string, existing: Set<string>): string {
  if (!existing.has(filename.toLowerCase())) return filename;
  const dot = filename.lastIndexOf(".");
  const base = dot > 0 ? filename.slice(0, dot) : filename;
  const extension = dot > 0 ? filename.slice(dot) : "";
  for (let counter = 1; ; counter += 1) {
    const suffix = counter === 1 ? " copy" : ` copy ${counter}`;
    const candidate = `${base}${suffix}${extension}`;
    if (!existing.has(candidate.toLowerCase())) return candidate;
  }
}

async function uploadNoteAttachment(scope: string, noteID: string, attachment: HankAIDraftAttachment): Promise<Record<string, unknown>> {
  const root = scope === "home" ? "/v1/home/notes" : "/v1/me/notes";
  return apiClient.request<Record<string, unknown>>(
    `${root}/${encodeURIComponent(noteID)}/attachments?filename=${encodeURIComponent(attachment.filename)}`,
    {
      method: "POST",
      headers: {
        "Content-Type": attachment.content_type || "application/octet-stream",
        "X-Hank-Filename": attachment.filename,
      },
      body: attachment.file,
    },
  );
}

async function commitNoteAttachments(
  args: Record<string, unknown>,
  attachments: HankAIDraftAttachment[],
  attachmentIDs: string[],
): Promise<Record<string, unknown>> {
  const noteID = stringValue(args.note_id);
  const noteScope = stringValue(args.note_scope, "profile");
  const noteTitle = stringValue(args.note_title, "Note");
  if (!noteID) throw new HankAIClientToolError("Hank did not include the target note.", { destination_kind: "note_attachment", attachment_ids: attachmentIDs });
  const files: Record<string, unknown>[] = [];
  try {
    for (const attachment of attachments) {
      const uploaded = await uploadNoteAttachment(noteScope, noteID, attachment);
      files.push({
        client_attachment_id: attachment.client_attachment_id,
        attachment_id: uploaded.id,
        filename: uploaded.filename || attachment.filename,
        content_type: uploaded.content_type || attachment.content_type,
        size_bytes: uploaded.size_bytes || attachment.size_bytes,
      });
    }
  } catch (error) {
    throw new HankAIClientToolError(error instanceof Error ? error.message : "The note upload failed.", {
      destination_kind: "note_attachment",
      note_id: noteID,
      note_scope: noteScope,
      note_title: noteTitle,
      attachment_ids: attachmentIDs,
      files,
    });
  }
  return {
    destination_kind: "note_attachment",
    note_id: noteID,
    note_scope: noteScope,
    note_title: noteTitle,
    attachment_ids: attachmentIDs,
    files,
  };
}

async function commitFileAttachments(
  args: Record<string, unknown>,
  attachments: HankAIDraftAttachment[],
  attachmentIDs: string[],
): Promise<Record<string, unknown>> {
  const targetPath = stringValue(args.target_path, "/");
  const sourceID = stringValue(args.source_id);
  const files: Record<string, unknown>[] = [];
  let existing: Set<string>;
  try {
    const listing = await fileServerClient.list(targetPath, sourceID || undefined);
    existing = new Set((listing.items || []).map((item) => item.name || item.path.split("/").pop() || "").filter(Boolean).map((name) => name.toLowerCase()));
  } catch (error) {
    throw new HankAIClientToolError(error instanceof Error ? error.message : "The target File Server folder could not be checked for filename conflicts.", {
      destination_kind: "smb",
      target_path: targetPath,
      source_id: sourceID,
      attachment_ids: attachmentIDs,
      files,
    });
  }
  try {
    for (const attachment of attachments) {
      const filename = uniqueCopyName(attachment.filename || "Attachment", existing);
      existing.add(filename.toLowerCase());
      const path = childPath(targetPath, filename);
      await fileServerClient.uploadFile(attachment.file, targetPath, sourceID || undefined, undefined, filename);
      files.push({
        client_attachment_id: attachment.client_attachment_id,
        filename,
        path,
        source_id: sourceID,
        content_type: attachment.content_type,
        size_bytes: attachment.size_bytes,
      });
    }
  } catch (error) {
    throw new HankAIClientToolError(error instanceof Error ? error.message : "The File Server upload failed.", {
      destination_kind: "smb",
      target_path: targetPath,
      source_id: sourceID,
      attachment_ids: attachmentIDs,
      files,
    });
  }
  return {
    destination_kind: "smb",
    target_path: targetPath,
    source_id: sourceID,
    attachment_ids: attachmentIDs,
    files,
  };
}

export async function executeHankAIClientTool(
  request: HankAIClientToolRequest,
  submitted: Map<string, HankAIDraftAttachment>,
): Promise<HankAIClientToolResult> {
  if (request.tool_name !== "attachments.commit") {
    return {
      tool_name: request.tool_name,
      error: `This action needs a Hank client that supports ${request.tool_name}.`,
    };
  }
  const args = request.arguments || {};
  const attachmentIDs = stringArray(args.attachment_ids);
  const destinationKind = stringValue(args.destination_kind);
  const attachments = attachmentIDs.map((id) => submitted.get(id));
  if (!attachmentIDs.length || attachments.some((attachment) => !attachment)) {
    return {
      tool_name: request.tool_name,
      error: "The staged upload is no longer available in this browser.",
      result: {
        destination_kind: destinationKind,
        attachment_ids: attachmentIDs,
        expired_attachment_ids: attachmentIDs,
        error_code: "missing_staged_attachment",
      },
    };
  }
  try {
    const available = attachments as HankAIDraftAttachment[];
    const result = destinationKind === "note_attachment"
      ? await commitNoteAttachments(args, available, attachmentIDs)
      : destinationKind === "smb"
        ? await commitFileAttachments(args, available, attachmentIDs)
        : (() => { throw new HankAIClientToolError("Hank did not include a valid attachment destination.", { destination_kind: destinationKind, attachment_ids: attachmentIDs }); })();
    for (const id of attachmentIDs) submitted.delete(id);
    return { tool_name: request.tool_name, result };
  } catch (error) {
    if (error instanceof HankAIClientToolError) {
      return { tool_name: request.tool_name, error: error.message, result: error.result };
    }
    return { tool_name: request.tool_name, error: error instanceof Error ? error.message : "The client tool could not complete." };
  }
}
