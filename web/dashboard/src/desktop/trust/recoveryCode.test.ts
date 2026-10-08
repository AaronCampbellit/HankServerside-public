import { describe, expect, it } from "vitest";
import { decodeRecoveryCode, encodeRecoveryCode, generateRecoverySecret } from "./recoveryCode";

describe("desktop recovery code", () => {
  it("round trips 256 random bits plus a four byte checksum", async () => {
    const secret = generateRecoverySecret();
    expect(secret).toHaveLength(32);
    const code = await encodeRecoveryCode(secret);
    expect(code).toMatch(/^[0-9A-HJKMNP-TV-Z]{6}(?:-[0-9A-HJKMNP-TV-Z]{6})*-[0-9A-HJKMNP-TV-Z]{1,6}$/);
    expect(await decodeRecoveryCode(code.toLowerCase().replaceAll("-", " "))).toEqual(secret);
  });
  it("rejects ambiguity, length, and checksum changes", async () => {
    const code = await encodeRecoveryCode(new Uint8Array(32).fill(7));
    await expect(decodeRecoveryCode(code.replace("7", "I"))).rejects.toThrow("desktop_recovery_code_invalid");
    await expect(decodeRecoveryCode(code.slice(6))).rejects.toThrow("desktop_recovery_code_invalid");
    const changed = code.slice(0, -1) + (code.endsWith("0") ? "1" : "0");
    await expect(decodeRecoveryCode(changed)).rejects.toThrow("desktop_recovery_code_checksum");
  });
});
