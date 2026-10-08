import { describe, expect, it, vi } from "vitest";
import { createDesktopTrustBootstrap } from "./trustCeremony";

describe("desktop trust bootstrap ceremony", () => {
  it("creates the exact first administrator capabilities and a one-time recovery code", async () => {
    let generated: CryptoKeyPair; const store = { create: vi.fn(async () => {
      const keyPair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
      generated = keyPair;
      return { keyPair, spki: new Uint8Array(await crypto.subtle.exportKey("spki", keyPair.publicKey)) };
    }), install: vi.fn(async () => generated), get: vi.fn(), getPublicSPKI: vi.fn(), remove: vi.fn(), removeMany: vi.fn() };
    const result = await createDesktopTrustBootstrap({ homeID: "home_1", userID: "user_1", deviceID: "browser_1", store });
    const operator = result.body.first_operator as Record<string, unknown>;
    expect(operator.capabilities).toEqual(["operator.approve", "endpoint.approve", "trust.recover", "trust.rotate"]);
    expect(result.body.confirmation).toBe("create desktop trust");
    expect(result.recoveryCode).toMatch(/-/);
    expect(store.install).toHaveBeenCalledWith("root:home_1:1", expect.any(Uint8Array), expect.any(Uint8Array));
    expect(JSON.stringify(result.body)).not.toContain(result.recoveryCode);
    await result.cleanup();
    expect(store.remove).toHaveBeenCalledWith("root:home_1:1");
    expect(store.remove).toHaveBeenCalledWith("browser_1");
  });
});
