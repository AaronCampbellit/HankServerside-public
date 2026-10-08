import { describe, expect, it } from "vitest";
import fixture from "../../../../schemas/desktop/v1/test-vectors.json";
import { decodeBase64URL } from "./base64url";
import { deriveDirectionalMaterial } from "./crypto";
import { DesktopRecordDirection, DesktopRecordLayer } from "./recordLayer";

async function fixtureLayer() {
  const material = await deriveDirectionalMaterial(
    decodeBase64URL(fixture.valid_initial_join.hkdf.shared_secret_base64url),
    decodeBase64URL(fixture.valid_initial_join.transcript.sha256_base64url),
  );
  return DesktopRecordLayer.create(1, material, DesktopRecordDirection.AgentToBrowser);
}

describe("desktop browser record layer", () => {
  it("decrypts once and rejects a duplicate sequence", async () => {
    const layer = await fixtureLayer();
    const record = new Uint8Array([
      ...decodeBase64URL(fixture.valid_initial_join.record.header_base64url),
      ...decodeBase64URL(fixture.valid_initial_join.record.ciphertext_base64url),
    ]);
    expect(new TextDecoder().decode(await layer.decrypt(record))).toBe("desktop-fixture-payload");
    await expect(layer.decrypt(record)).rejects.toThrow("desktop_sequence_mismatch");
  });

  it("authenticates headers, rejects bad tags, and clears replaced epochs", async () => {
    const layer = await fixtureLayer();
    const record = new Uint8Array([
      ...decodeBase64URL(fixture.valid_initial_join.record.header_base64url),
      ...decodeBase64URL(fixture.valid_initial_join.record.ciphertext_base64url),
    ]);
    record[record.length - 1] ^= 1;
    await expect(layer.decrypt(record)).rejects.toThrow("desktop_invalid_tag");
    const replacement = await deriveDirectionalMaterial(new Uint8Array(32).fill(7), new Uint8Array(32).fill(9));
    await layer.replaceEpoch(2, replacement);
    expect(layer.debugKeyState()).toEqual({ epoch: 2, priorEpochPresent: false });
    await expect(layer.decrypt(record)).rejects.toThrow("desktop_epoch_mismatch");
  });
});
