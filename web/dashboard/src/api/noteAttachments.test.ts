import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { NoteAttachmentsClient } from "./noteAttachments";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("NoteAttachmentsClient", () => {
  it("loads and deletes admin note attachments", async () => {
    const request = vi.fn(async () => ({ total_files: 1, total_bytes: 2048, attachments: [{ id: "natt-1" }] }));
    const client = new NoteAttachmentsClient(testTransport(request));

    const inventory = await client.load();
    await client.remove("natt-1");

    expect(inventory.attachments).toHaveLength(1);
    expect(request).toHaveBeenNthCalledWith(1, "/v1/home/note-attachments");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/home/note-attachments/natt-1", { method: "DELETE" });
  });

  it("normalizes a missing attachment collection", async () => {
    const request = vi.fn(async () => ({ total_files: 0, total_bytes: 0, attachments: null }));
    const client = new NoteAttachmentsClient(testTransport(request));

    await expect(client.load()).resolves.toMatchObject({ attachments: [] });
  });
});
