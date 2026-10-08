import { exactBuffer } from "../base64url";

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
const lookup = new Map([...alphabet].map((value, index) => [value, index]));

export function generateRecoverySecret(): Uint8Array { const value = new Uint8Array(32); crypto.getRandomValues(value); return value; }

export async function encodeRecoveryCode(secret: Uint8Array): Promise<string> {
  if (secret.byteLength !== 32) throw new Error("desktop_recovery_code_invalid");
  const checksum = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(secret))).slice(0, 4);
  const raw = new Uint8Array(36); raw.set(secret); raw.set(checksum, 32);
  const encoded = base32Encode(raw);
  return encoded.match(/.{1,6}/g)!.join("-");
}

export async function decodeRecoveryCode(code: string): Promise<Uint8Array> {
  const normalized = code.toUpperCase().replace(/[\s-]/g, "");
  if (/[ILOU]/.test(normalized)) throw new Error("desktop_recovery_code_invalid");
  const raw = base32Decode(normalized);
  if (raw.byteLength !== 36) throw new Error("desktop_recovery_code_invalid");
  const secret = raw.slice(0, 32), expected = raw.slice(32);
  const actual = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(secret))).slice(0, 4);
  let mismatch = 0; for (let index = 0; index < 4; index++) mismatch |= actual[index] ^ expected[index];
  if (mismatch) { secret.fill(0); throw new Error("desktop_recovery_code_checksum"); }
  return secret;
}

function base32Encode(value: Uint8Array): string {
  let bits = 0, buffer = 0, result = "";
  for (const byte of value) { buffer = (buffer << 8) | byte; bits += 8; while (bits >= 5) { bits -= 5; result += alphabet[(buffer >>> bits) & 31]; buffer &= (1 << bits) - 1; } }
  if (bits) result += alphabet[(buffer << (5 - bits)) & 31];
  return result;
}
function base32Decode(value: string): Uint8Array {
  let bits = 0, buffer = 0; const output: number[] = [];
  for (const character of value) { const digit = lookup.get(character); if (digit === undefined) throw new Error("desktop_recovery_code_invalid"); buffer = (buffer << 5) | digit; bits += 5; if (bits >= 8) { bits -= 8; output.push((buffer >>> bits) & 255); buffer &= (1 << bits) - 1; } }
  if (bits && buffer !== 0) throw new Error("desktop_recovery_code_invalid");
  return new Uint8Array(output);
}
