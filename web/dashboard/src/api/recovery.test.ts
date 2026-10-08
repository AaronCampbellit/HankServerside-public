import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { RecoveryClient } from "./recovery";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("RecoveryClient", () => {
  it("exports, previews, and applies recovery bundles", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new RecoveryClient(testTransport(request));
    const bundle = { schema_version: 2, product: "hank", home: { name: "Campbell Home" } };

    await client.exportBundle();
    await client.previewImport(bundle);
    await client.applyImport(bundle);

    expect(request).toHaveBeenCalledWith("/v1/home/recovery/export");
    expect(request).toHaveBeenCalledWith("/v1/home/recovery/import/preview", { method: "POST", body: bundle });
    expect(request).toHaveBeenCalledWith("/v1/home/recovery/import/apply", {
      method: "POST",
      body: { bundle, confirm: true },
    });
  });
});
