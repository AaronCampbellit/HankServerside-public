import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { exactBuffer, encodeBase64URL } from "./base64url";
import { p256DERToP1363 } from "./crypto";
import { ensureBrowserDesktopIdentity, signBrowserEnrollment } from "./autoEnrollment";

const desktopTrustMocks = vi.hoisted(() => ({
  autoChallenge: vi.fn(),
  autoEnroll: vi.fn(),
}));

vi.mock("../api/desktopTrust", () => ({ desktopTrustClient: desktopTrustMocks }));

describe("browser desktop auto-enrollment", () => {
  beforeEach(() => {
    localStorage.clear();
    desktopTrustMocks.autoChallenge.mockReset();
    desktopTrustMocks.autoEnroll.mockReset();
  });

  it("uses the DER ECDSA format accepted by the server", async () => {
    const identity = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
    const transcript = new TextEncoder().encode("Hank Desktop Device Enrollment v1");
    const signature = await signBrowserEnrollment(identity.privateKey, transcript);

    expect(signature[0]).toBe(0x30);
    expect(encodeBase64URL(signature)).not.toBe(encodeBase64URL(p256DERToP1363(signature)));
    await expect(crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, identity.publicKey,
      exactBuffer(p256DERToP1363(signature)), exactBuffer(transcript))).resolves.toBe(true);
  });

  it("reuses a successful enrollment for the same authenticated session", async () => {
    desktopTrustMocks.autoChallenge.mockResolvedValue({ challenge_id: "challenge-1", challenge: "challenge-value", expires_at: new Date(Date.now() + 60_000).toISOString() });
    const enrollment = {
      root: { generation: 1, algorithm: "ECDSA_P256_SHA256", fingerprint: "root-fingerprint", public_key_spki: "root-spki" },
      identity: { device_id: "browser-device" },
    };
    desktopTrustMocks.autoEnroll.mockResolvedValue(enrollment);

    const first = await ensureBrowserDesktopIdentity("home-cache", "user-cache", "session-cache");
    const second = await ensureBrowserDesktopIdentity("home-cache", "user-cache", "session-cache");

    expect(first).toBe(enrollment);
    expect(second).toBe(enrollment);
    expect(desktopTrustMocks.autoChallenge).toHaveBeenCalledTimes(1);
    expect(desktopTrustMocks.autoEnroll).toHaveBeenCalledTimes(1);
  });
});
