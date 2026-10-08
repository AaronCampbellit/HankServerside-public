import { describe, expect, it, vi } from "vitest";
import { DesktopTrustClient } from "./desktopTrust";

describe("DesktopTrustClient", () => {
  it("uses exact trust routes and destructive confirmations", async () => {
    const request = vi.fn().mockResolvedValue({});
    const client = new DesktopTrustClient({ request });
    await client.snapshot(); await client.pendingEndpoint("agent/1"); await client.revokeOperator("device 1"); await client.revokeEndpoint("agent/1");
    await client.recoveryChallenge(3, { identity_id: "id" }); await client.reset({ generation: 4 });
    expect(request).toHaveBeenNthCalledWith(1, "/v1/home/desktop-trust");
    expect(request).toHaveBeenNthCalledWith(2, "/v1/home/desktop-trust/pending-endpoints/agent%2F1");
    expect(request).toHaveBeenNthCalledWith(3, "/v1/home/desktop-trust/operator-devices/device%201/revoke", expect.objectContaining({ method: "POST", body: expect.objectContaining({ confirmation: "revoke desktop identity" }) }));
    expect(request).toHaveBeenNthCalledWith(4, "/v1/home/desktop-trust/endpoints/agent%2F1/revoke", expect.objectContaining({ body: expect.objectContaining({ confirmation: "revoke desktop identity" }) }));
    expect(request).toHaveBeenNthCalledWith(5, "/v1/home/desktop-trust/recovery", expect.objectContaining({ body: expect.objectContaining({ confirmation: "recover desktop trust", generation: 3 }) }));
    expect(request).toHaveBeenNthCalledWith(6, "/v1/home/desktop-trust/reset", expect.objectContaining({ body: expect.objectContaining({ confirmation: "reset desktop trust" }) }));
  });

  it("carries changed operator replacement confirmation", async () => {
    const request = vi.fn().mockResolvedValue({});
    const client = new DesktopTrustClient({ request } as never);
    await client.approveOperator({ identity_id: "dop-new", device_id: "browser-1", confirmation: "replace changed desktop identity" });
    expect(request).toHaveBeenCalledWith("/v1/home/desktop-trust/operator-devices", expect.objectContaining({
      body: expect.objectContaining({ confirmation: "replace changed desktop identity" }),
    }));
  });
});
