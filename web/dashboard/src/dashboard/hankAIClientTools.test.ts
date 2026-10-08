import { afterEach, describe, expect, it, vi } from "vitest";
import type { HankAIClientToolRequest } from "../api/hankAI";
import type { HankAIDraftAttachment } from "./hankAIWorkflow";

const apiRequest = vi.hoisted(() => vi.fn());
const fileServerClient = vi.hoisted(() => ({
  list: vi.fn(),
  uploadFile: vi.fn(),
}));

vi.mock("../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client")>();
  return { ...actual, apiClient: { request: apiRequest } };
});

vi.mock("../api/fileServer", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/fileServer")>();
  return { ...actual, fileServerClient };
});

import { executeHankAIClientTool } from "./hankAIClientTools";

function draftAttachment(id: string, filename: string, contentType = "application/pdf"): HankAIDraftAttachment {
  const file = new File(["hank attachment"], filename, { type: contentType });
  return {
    id: `draft-${id}`,
    client_attachment_id: id,
    file,
    filename,
    content_type: contentType,
    size_bytes: file.size,
    checksum_sha256: "checksum",
    kind: contentType.startsWith("image/") ? "image" : "pdf",
  };
}

function attachmentRequest(argumentsValue: Record<string, unknown>): HankAIClientToolRequest {
  return { tool_name: "attachments.commit", arguments: argumentsValue };
}

describe("executeHankAIClientTool", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("uploads a staged attachment to the selected note and returns a structured result", async () => {
    const attachment = draftAttachment("client-1", "receipt.pdf");
    const submitted = new Map([[attachment.client_attachment_id, attachment]]);
    apiRequest.mockResolvedValue({ id: "natt-1", filename: "receipt.pdf", content_type: "application/pdf", size_bytes: attachment.size_bytes });

    const result = await executeHankAIClientTool(attachmentRequest({
      attachment_ids: ["client-1"],
      destination_kind: "note_attachment",
      note_scope: "profile",
      note_id: "receipts.md",
      note_title: "Receipts",
    }), submitted);

    expect(apiRequest).toHaveBeenCalledWith("/v1/me/notes/receipts.md/attachments?filename=receipt.pdf", expect.objectContaining({
      method: "POST",
      body: attachment.file,
    }));
    expect(result.error).toBeUndefined();
    expect(result.result).toEqual(expect.objectContaining({
      destination_kind: "note_attachment",
      note_id: "receipts.md",
      attachment_ids: ["client-1"],
      files: [expect.objectContaining({ attachment_id: "natt-1", client_attachment_id: "client-1" })],
    }));
    expect(submitted).not.toHaveProperty("client-1");
    expect(submitted.has("client-1")).toBe(false);
  });

  it("preserves the File Server source and safely renames case-insensitive conflicts", async () => {
    const attachment = draftAttachment("client-2", "report.pdf");
    const submitted = new Map([[attachment.client_attachment_id, attachment]]);
    fileServerClient.list.mockResolvedValue({ items: [{ name: "REPORT.PDF", path: "/Taxes/REPORT.PDF" }] });
    fileServerClient.uploadFile.mockResolvedValue({ ok: true });

    const result = await executeHankAIClientTool(attachmentRequest({
      attachment_ids: ["client-2"],
      destination_kind: "smb",
      source_id: "smb-primary",
      target_path: "/Taxes",
    }), submitted);

    expect(fileServerClient.list).toHaveBeenCalledWith("/Taxes", "smb-primary");
    expect(fileServerClient.uploadFile).toHaveBeenCalledWith(attachment.file, "/Taxes", "smb-primary", undefined, "report copy.pdf");
    expect(result.result).toEqual(expect.objectContaining({
      destination_kind: "smb",
      source_id: "smb-primary",
      files: [expect.objectContaining({ filename: "report copy.pdf", path: "/Taxes/report copy.pdf", source_id: "smb-primary" })],
    }));
  });

  it("reports expired browser staging without attempting an upload", async () => {
    const result = await executeHankAIClientTool(attachmentRequest({
      attachment_ids: ["missing"],
      destination_kind: "smb",
      target_path: "/Taxes",
    }), new Map());

    expect(result.error).toContain("no longer available");
    expect(result.result).toEqual(expect.objectContaining({
      error_code: "missing_staged_attachment",
      expired_attachment_ids: ["missing"],
    }));
    expect(fileServerClient.uploadFile).not.toHaveBeenCalled();
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it("does not risk overwriting a File Server file when the target folder cannot be listed", async () => {
    const attachment = draftAttachment("client-3", "report.pdf");
    fileServerClient.list.mockRejectedValue(new Error("File Server is unavailable"));

    const result = await executeHankAIClientTool(attachmentRequest({
      attachment_ids: ["client-3"],
      destination_kind: "smb",
      source_id: "smb-primary",
      target_path: "/Taxes",
    }), new Map([[attachment.client_attachment_id, attachment]]));

    expect(result.error).toBe("File Server is unavailable");
    expect(result.result).toEqual(expect.objectContaining({
      destination_kind: "smb",
      target_path: "/Taxes",
      files: [],
    }));
    expect(fileServerClient.uploadFile).not.toHaveBeenCalled();
  });

  it("does not execute unknown client tools in the browser", async () => {
    const result = await executeHankAIClientTool({ tool_name: "calendar.create", arguments: {} }, new Map());

    expect(result.error).toContain("supports calendar.create");
    expect(fileServerClient.uploadFile).not.toHaveBeenCalled();
    expect(apiRequest).not.toHaveBeenCalled();
  });
});
