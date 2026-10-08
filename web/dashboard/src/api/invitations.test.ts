import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { InvitationsClient } from "./invitations";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("InvitationsClient", () => {
  it("accepts a home invitation", async () => {
    const request = vi.fn(async () => ({ home: { name: "Lake House" } }));
    const client = new InvitationsClient(testTransport(request));

    await client.acceptHomeInvitation("invite-token");

    expect(request).toHaveBeenCalledWith("/v1/home/invitations/accept", {
      method: "POST",
      body: { token: "invite-token" },
    });
  });
});
