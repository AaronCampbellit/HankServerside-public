import { describe, expect, it } from "vitest";
import fixture from "../../../../schemas/desktop/v1/test-vectors.json";
import { decodeBase64URL, encodeBase64URL, exactBuffer } from "./base64url";
import { createBrowserHandshake, deriveDirectionalMaterial, encodeHandshakeTranscript, p256DERToP1363, p256P1363ToDER, verifyDesktopCertificateChain } from "./crypto";

describe("desktop browser crypto", () => {
  it("derives the canonical directional material", async () => {
    const material = await deriveDirectionalMaterial(
      decodeBase64URL(fixture.valid_initial_join.hkdf.shared_secret_base64url),
      decodeBase64URL(fixture.valid_initial_join.transcript.sha256_base64url),
    );
    expect(encodeBase64URL(material.browserToAgentKey)).toBe(fixture.valid_initial_join.hkdf.browser_to_agent_key_base64url);
    expect(encodeBase64URL(material.agentToBrowserKey)).toBe(fixture.valid_initial_join.hkdf.agent_to_browser_key_base64url);
    expect(encodeBase64URL(material.browserNoncePrefix)).toBe(fixture.valid_initial_join.hkdf.browser_nonce_prefix_base64url);
    expect(encodeBase64URL(material.agentNoncePrefix)).toBe(fixture.valid_initial_join.hkdf.agent_nonce_prefix_base64url);
  });

  it("encodes the canonical transcript and signs with a non-exportable identity", async () => {
    const value = fixture.valid_initial_join.transcript;
    const transcript = encodeHandshakeTranscript({
      homeID: value.home_id, sessionID: value.session_id, agentID: value.agent_id,
      operatorUserID: value.operator_user_id, operatorDeviceID: value.operator_device_id,
      permissions: value.permissions, joinExpiresAtUnixMS: value.join_expires_at_unix_ms,
      hardExpiresAtUnixMS: value.hard_expires_at_unix_ms, keyEpoch: value.key_epoch,
      browserEphemeralPublicKey: decodeBase64URL(value.browser_ephemeral_public_key_base64url),
    });
    expect(encodeBase64URL(transcript)).toBe(value.encoded_base64url);
    const identity = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
    const handshake = await createBrowserHandshake({
      homeID: "home_1", sessionID: "desk_1", agentID: "agent_1", operatorUserID: "usr_1",
      operatorDeviceID: "device_1", permissions: ["desktop.view"], joinExpiresAtUnixMS: Date.now() + 60_000,
      hardExpiresAtUnixMS: Date.now() + 3_600_000, keyEpoch: 1,
    }, identity.privateKey);
    expect(handshake.ephemeralPrivateKey.extractable).toBe(false);
    expect(await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, identity.publicKey, exactBuffer(p256DERToP1363(handshake.signature)), exactBuffer(handshake.transcript))).toBe(true);
    expect(p256P1363ToDER(p256DERToP1363(handshake.signature))).toEqual(handshake.signature);
  });

  it("verifies delegated certificate chains and rejects a substituted root", async () => {
    const root = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    const issuer = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    const leaf = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    const issuerCertificate = new TextEncoder().encode("issuer-certificate"), leafCertificate = new TextEncoder().encode("leaf-certificate");
    const sign = async (key: CryptoKey, value: Uint8Array) => p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, exactBuffer(value))));
    const chain = [
      { certificate: leafCertificate, signature: await sign(issuer.privateKey, leafCertificate), publicKeySPKI: new Uint8Array(await crypto.subtle.exportKey("spki", leaf.publicKey)) },
      { certificate: issuerCertificate, signature: await sign(root.privateKey, issuerCertificate), publicKeySPKI: new Uint8Array(await crypto.subtle.exportKey("spki", issuer.publicKey)) },
    ];
    const rootSPKI = new Uint8Array(await crypto.subtle.exportKey("spki", root.publicKey));
    await expect(verifyDesktopCertificateChain(chain, rootSPKI)).resolves.toBeUndefined();
    const other = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    await expect(verifyDesktopCertificateChain(chain, new Uint8Array(await crypto.subtle.exportKey("spki", other.publicKey)))).rejects.toThrow("desktop_certificate_chain_invalid");
  });
});
