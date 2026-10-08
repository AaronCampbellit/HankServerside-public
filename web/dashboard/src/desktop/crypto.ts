import { exactBuffer } from "./base64url";

const encoder = new TextEncoder();

export interface DesktopHandshakeInput {
  homeID: string; sessionID: string; agentID: string; operatorUserID: string; operatorDeviceID: string;
  permissions: string[]; joinExpiresAtUnixMS: number; hardExpiresAtUnixMS: number; keyEpoch: number;
  browserEphemeralPublicKey?: Uint8Array;
}

export interface BrowserHandshake {
  transcript: Uint8Array;
  transcriptDigest: Uint8Array;
  ephemeralPublicKey: Uint8Array;
  ephemeralPrivateKey: CryptoKey;
  signature: Uint8Array;
}

export interface DesktopDirectionalMaterial {
  browserToAgentKey: Uint8Array;
  agentToBrowserKey: Uint8Array;
  browserNoncePrefix: Uint8Array;
  agentNoncePrefix: Uint8Array;
}

export interface AgentHandshakeResponse {
  endpointCertificateChain: DesktopCertificateChainEntry[];
  trustRootPublicKeySPKI: Uint8Array;
  endpointPublicKeySPKI: Uint8Array;
  endpointEphemeralPublicKey: Uint8Array;
  endpointHandshakeSignature: Uint8Array;
}

export interface DesktopCertificateChainEntry { certificate: Uint8Array; signature: Uint8Array; publicKeySPKI: Uint8Array }

export function encodeHandshakeTranscript(input: DesktopHandshakeInput): Uint8Array {
  const ephemeral = input.browserEphemeralPublicKey;
  if (!input.homeID || !input.sessionID || !input.agentID || !input.operatorUserID || !input.operatorDeviceID ||
      !input.permissions.length || !ephemeral?.length || input.keyEpoch < 1 ||
      input.joinExpiresAtUnixMS <= 0 || input.hardExpiresAtUnixMS <= input.joinExpiresAtUnixMS) {
    throw new Error("desktop_invalid_handshake_transcript");
  }
  const parts: Uint8Array[] = [];
  const field = (value: string | Uint8Array) => {
    const bytes = typeof value === "string" ? encoder.encode(value) : value;
    if (bytes.byteLength > 1 << 20) throw new Error("desktop_transcript_field_oversized");
    const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, bytes.byteLength, false);
    parts.push(length, bytes);
  };
  for (const value of ["Hank Desktop Handshake v1", input.homeID, input.sessionID, input.agentID, input.operatorUserID, input.operatorDeviceID]) field(value);
  const count = new Uint8Array(4); new DataView(count.buffer).setUint32(0, input.permissions.length, false); parts.push(count);
  for (const permission of input.permissions) field(permission);
  field(ephemeral);
  const numbers = new Uint8Array(20), view = new DataView(numbers.buffer);
  view.setBigInt64(0, BigInt(input.joinExpiresAtUnixMS), false); view.setBigInt64(8, BigInt(input.hardExpiresAtUnixMS), false); view.setUint32(16, input.keyEpoch, false);
  parts.push(numbers);
  const length = parts.reduce((sum, part) => sum + part.byteLength, 0), output = new Uint8Array(length);
  let offset = 0; for (const part of parts) { output.set(part, offset); offset += part.byteLength; }
  return output;
}

export async function createBrowserHandshake(input: Omit<DesktopHandshakeInput, "browserEphemeralPublicKey">, operatorPrivateKey: CryptoKey): Promise<BrowserHandshake> {
  const ephemeral = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]);
  const ephemeralPublicKey = new Uint8Array(await crypto.subtle.exportKey("raw", ephemeral.publicKey));
  const transcript = encodeHandshakeTranscript({ ...input, browserEphemeralPublicKey: ephemeralPublicKey });
  const transcriptDigest = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(transcript)));
  const signature = p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, operatorPrivateKey, exactBuffer(transcript))));
  return { transcript, transcriptDigest, ephemeralPublicKey, ephemeralPrivateKey: ephemeral.privateKey, signature };
}

export async function completeBrowserHandshake(browser: BrowserHandshake, response: AgentHandshakeResponse): Promise<DesktopDirectionalMaterial> {
  await verifyDesktopCertificateChain(response.endpointCertificateChain, response.trustRootPublicKeySPKI);
  if (!response.endpointCertificateChain[0]?.publicKeySPKI.every((value, index) => value === response.endpointPublicKeySPKI[index]) ||
      response.endpointCertificateChain[0].publicKeySPKI.byteLength !== response.endpointPublicKeySPKI.byteLength) throw new Error("desktop_endpoint_certificate_invalid");
  const endpoint = await crypto.subtle.importKey("spki", exactBuffer(response.endpointPublicKeySPKI), { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
  const signed = concat(browser.transcript, response.endpointEphemeralPublicKey);
  if (!await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, endpoint, exactBuffer(p256DERToP1363(response.endpointHandshakeSignature)), exactBuffer(signed))) {
    throw new Error("desktop_endpoint_handshake_invalid");
  }
  const agentEphemeral = await crypto.subtle.importKey("raw", exactBuffer(response.endpointEphemeralPublicKey), { name: "ECDH", namedCurve: "P-256" }, false, []);
  const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: agentEphemeral }, browser.ephemeralPrivateKey, 256));
  try { return await deriveDirectionalMaterial(shared, browser.transcriptDigest); }
  finally { shared.fill(0); }
}

export async function verifyDesktopCertificateChain(chain: DesktopCertificateChainEntry[], trustRootPublicKeySPKI: Uint8Array): Promise<void> {
  if (!chain.length || chain.length > 9) throw new Error("desktop_certificate_chain_invalid");
  for (let index = 0; index < chain.length; index++) {
    const value = chain[index], signerSPKI = chain[index + 1]?.publicKeySPKI ?? trustRootPublicKeySPKI;
    const signer = await crypto.subtle.importKey("spki", exactBuffer(signerSPKI), { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
    if (!await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, signer,
      exactBuffer(p256DERToP1363(value.signature)), exactBuffer(value.certificate))) throw new Error("desktop_certificate_chain_invalid");
  }
}

export async function deriveDirectionalMaterial(sharedSecret: Uint8Array, transcriptDigest: Uint8Array): Promise<DesktopDirectionalMaterial> {
  if (!sharedSecret.byteLength || transcriptDigest.byteLength !== 32) throw new Error("desktop_invalid_hkdf_input");
  const key = await crypto.subtle.importKey("raw", exactBuffer(sharedSecret), "HKDF", false, ["deriveBits"]);
  const derive = async (label: string, bits: number) => new Uint8Array(await crypto.subtle.deriveBits({
    name: "HKDF", hash: "SHA-256", salt: exactBuffer(transcriptDigest), info: exactBuffer(encoder.encode(label)),
  }, key, bits));
  return {
    browserToAgentKey: await derive("hank-desktop-v1/browser-to-agent/key", 256),
    agentToBrowserKey: await derive("hank-desktop-v1/agent-to-browser/key", 256),
    browserNoncePrefix: await derive("hank-desktop-v1/browser-to-agent/nonce", 32),
    agentNoncePrefix: await derive("hank-desktop-v1/agent-to-browser/nonce", 32),
  };
}

function concat(left: Uint8Array, right: Uint8Array): Uint8Array {
  const output = new Uint8Array(left.byteLength + right.byteLength); output.set(left); output.set(right, left.byteLength); return output;
}

export function p256P1363ToDER(signature: Uint8Array): Uint8Array {
  if (signature.length !== 64) throw new Error("desktop_p256_signature_invalid");
  const integer = (value: Uint8Array) => { let start = 0; while (start < value.length - 1 && value[start] === 0) start++; let body = value.slice(start); if (body[0] & 0x80) { const prefixed = new Uint8Array(body.length + 1); prefixed.set(body, 1); body = prefixed; } const encoded = new Uint8Array(body.length + 2); encoded[0] = 0x02; encoded[1] = body.length; encoded.set(body, 2); return encoded; };
  const r = integer(signature.slice(0, 32)), s = integer(signature.slice(32));
  const output = new Uint8Array(2 + r.length + s.length); output[0] = 0x30; output[1] = r.length + s.length; output.set(r, 2); output.set(s, 2 + r.length); return output;
}

export function p256DERToP1363(signature: Uint8Array): Uint8Array {
  if (signature.length < 8 || signature[0] !== 0x30 || signature[1] !== signature.length - 2) throw new Error("desktop_p256_signature_invalid");
  let offset = 2;
  const integer = () => { if (signature[offset++] !== 0x02) throw new Error("desktop_p256_signature_invalid"); const length = signature[offset++]; if (!length || offset + length > signature.length) throw new Error("desktop_p256_signature_invalid"); let body = signature.slice(offset, offset + length); offset += length; if (body[0] === 0) body = body.slice(1); if (body.length > 32) throw new Error("desktop_p256_signature_invalid"); const value = new Uint8Array(32); value.set(body, 32 - body.length); return value; };
  const r = integer(), s = integer(); if (offset !== signature.length) throw new Error("desktop_p256_signature_invalid"); return concat(r, s);
}
