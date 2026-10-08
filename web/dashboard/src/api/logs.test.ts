import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { LogsClient } from "./logs";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("LogsClient", () => {
  it("loads audit events with filters", async () => {
    const request = vi.fn(async () => ({ events: [] }));
    const client = new LogsClient(testTransport(request));

    await client.listAuditEvents({
      event_type: "login.succeeded",
      severity: "info",
      target_type: "session",
      sort: "event_type",
      order: "asc",
      limit: 25,
    });

    expect(request).toHaveBeenCalledWith(
      "/v1/home/audit-events?event_type=login.succeeded&severity=info&target_type=session&sort=event_type&order=asc&limit=25",
    );
  });
});
