import { describe, expect, it, vi } from "vitest";
import { HankAIClient } from "./hankAI";
import type { ApiTransport } from "./client";

describe("HankAIClient", () => {
  it("uses v2 task controls with exact revisions and idempotency identities", async () => {
    const request = vi.fn(async <T>() => ({}) as T);
    const client = new HankAIClient({ request: request as unknown as ApiTransport["request"] });
    await client.submitTask("s/1", "Find notes", "submission-1");
    await client.followupTask("t/1", "Second one", "input-1", 7);
    await client.stopTask("t/1");
    await client.decideTaskApproval("t/1", "a/1", 9, "digest", false);
    expect(request).toHaveBeenNthCalledWith(1, "/v1/home/assistant/sessions/s%2F1/messages", { method: "POST", headers: { "X-Hank-Assistant-Execution": "2" }, body: { content: "Find notes", submission_id: "submission-1" } });
    expect(request).toHaveBeenNthCalledWith(2, "/v1/home/assistant/tasks/t%2F1/followups", { method: "POST", body: { text: "Second one", submission_id: "input-1", expected_revision: 7 } });
    expect(request).toHaveBeenNthCalledWith(3, "/v1/home/assistant/tasks/t%2F1/stop", { method: "POST" });
    expect(request).toHaveBeenNthCalledWith(4, "/v1/home/assistant/tasks/t%2F1/approvals/a%2F1", { method: "POST", body: { expected_revision: 9, action_digest: "digest", approved: false } });
  });

  it("loads sessions messages status and sends chat messages", async () => {
    const request = vi.fn(async <T>() => ({}) as T);
    const client = new HankAIClient({ request: request as unknown as ApiTransport["request"] });

    await client.status();
    await client.listSessions();
    await client.createSession();
    await client.listMessages("session-1");
    await client.sendMessage("session-1", "What is up?", {
      timezone: "America/Chicago",
      attachments: [{
        client_attachment_id: "attachment-1",
        filename: "receipt.pdf",
        content_type: "application/pdf",
        size_bytes: 42,
        checksum_sha256: "abc123",
        kind: "pdf",
      }],
    });
    await client.getRun("run-1");
    await client.confirmRun("run-1", true);
    await client.submitClientToolResult("run-1", { tool_name: "calendar.create", result: { ok: true } });
    await client.discardAttachment("session-1", "attachment-1");
    await client.deleteSession("session-1");

    expect(request).toHaveBeenNthCalledWith(1, "/v1/home/assistant/status");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/home/assistant/sessions");
    expect(request).toHaveBeenNthCalledWith(3, "/v1/home/assistant/sessions", { method: "POST" });
    expect(request).toHaveBeenNthCalledWith(4, "/v1/home/assistant/sessions/session-1/messages");
    expect(request).toHaveBeenNthCalledWith(5, "/v1/home/assistant/sessions/session-1/messages", {
      method: "POST",
      body: {
        content: "What is up?",
        attachments: [{
          client_attachment_id: "attachment-1",
          filename: "receipt.pdf",
          content_type: "application/pdf",
          size_bytes: 42,
          checksum_sha256: "abc123",
          kind: "pdf",
        }],
        device_context: {
          device_id: "hankserverside-dashboard",
          timezone: "America/Chicago",
        },
      },
    });
    expect(request).toHaveBeenNthCalledWith(6, "/v1/home/assistant/runs/run-1");
    expect(request).toHaveBeenNthCalledWith(7, "/v1/home/assistant/runs/run-1/confirm", { method: "POST", body: { approved: true } });
    expect(request).toHaveBeenNthCalledWith(8, "/v1/home/assistant/runs/run-1/client-tool-results", {
      method: "POST",
      body: { tool_name: "calendar.create", result: { ok: true } },
    });
    expect(request).toHaveBeenNthCalledWith(9, "/v1/home/assistant/sessions/session-1/attachments/attachment-1/discard", { method: "DELETE" });
    expect(request).toHaveBeenNthCalledWith(10, "/v1/home/assistant/sessions/session-1", { method: "DELETE" });
  });
});
