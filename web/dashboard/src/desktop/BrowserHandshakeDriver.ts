import type { DesktopSessionAuthorization } from "../api/desktop";
import { decodeBase64URL, encodeBase64URL, exactBuffer } from "./base64url";
import { completeBrowserHandshake, createBrowserHandshake, type BrowserHandshake } from "./crypto";
import { DesktopRecordLayer } from "./recordLayer";
import type { DesktopHandshakeDriver } from "./DesktopSocket";

interface DriverOptions { operatorPrivateKey: CryptoKey; trustRootPublicKeySPKI: Uint8Array; trustRootGeneration: number }
interface SignedCertificateEnvelope { claims: string; signature: string; issuer_certificate?: string }

export class BrowserHandshakeDriver implements DesktopHandshakeDriver {
  private browser: BrowserHandshake | null = null;
  private session: DesktopSessionAuthorization | null = null;
  constructor(private readonly options: DriverOptions) {}

  async browserHandshakeFrame(session: DesktopSessionAuthorization): Promise<Uint8Array> {
    if (!session.home_id || !session.operator_user_id || !session.operator_device_id || !session.permissions?.length || !session.join_expires_at || !session.hard_expires_at) throw new Error("desktop_session_handshake_fields_missing");
    this.session = session;
    this.browser = await createBrowserHandshake({
      homeID: session.home_id, sessionID: session.session_id, agentID: session.agent_id,
      operatorUserID: session.operator_user_id, operatorDeviceID: session.operator_device_id,
      permissions: session.permissions, joinExpiresAtUnixMS: Date.parse(session.join_expires_at),
      hardExpiresAtUnixMS: Date.parse(session.hard_expires_at), keyEpoch: session.key_epoch,
    }, this.options.operatorPrivateKey);
    return encodeJSON({
      transcript_base64url: encodeBase64URL(this.browser.transcript),
      browser_ephemeral_public_key_base64url: encodeBase64URL(this.browser.ephemeralPublicKey),
      operator_signature_base64url: encodeBase64URL(this.browser.signature),
    });
  }

  async completeAgentHandshake(payload: Uint8Array): Promise<DesktopRecordLayer> {
    const browser = this.browser, session = this.session;
    if (!browser || !session?.endpoint_certificate) throw new Error("desktop_browser_handshake_missing");
    const agent = decodeJSON(payload) as { endpoint_ephemeral_public_key_base64url?: string; endpoint_handshake_signature_base64url?: string };
    if (!agent.endpoint_ephemeral_public_key_base64url || !agent.endpoint_handshake_signature_base64url) throw new Error("desktop_agent_handshake_invalid");
    const certificateBytes = decodeBase64URL(session.endpoint_certificate);
    const certificateChain = decodeCertificateChain(certificateBytes, session, this.options.trustRootGeneration);
    const endpointPublicKeySPKI = certificateChain[0].publicKeySPKI;
    if (!session.endpoint_certificate_fingerprint || await fingerprint(endpointPublicKeySPKI) !== session.endpoint_certificate_fingerprint) throw new Error("desktop_endpoint_certificate_invalid");
    const material = await completeBrowserHandshake(browser, {
      endpointCertificateChain: certificateChain,
      trustRootPublicKeySPKI: this.options.trustRootPublicKeySPKI, endpointPublicKeySPKI,
      endpointEphemeralPublicKey: decodeBase64URL(agent.endpoint_ephemeral_public_key_base64url),
      endpointHandshakeSignature: decodeBase64URL(agent.endpoint_handshake_signature_base64url),
    });
    return DesktopRecordLayer.create(session.key_epoch, material);
  }
}

function decodeCertificateChain(encoded: Uint8Array, session: DesktopSessionAuthorization, rootGeneration: number): Array<{ certificate: Uint8Array; signature: Uint8Array; publicKeySPKI: Uint8Array }> {
  const result: Array<{ certificate: Uint8Array; signature: Uint8Array; publicKeySPKI: Uint8Array }> = [];
  const metadata: IdentityClaims[] = [];
  let current: Uint8Array | undefined = encoded;
  while (current) {
    if (result.length >= 9) throw new Error("desktop_endpoint_certificate_chain_invalid");
    const envelope = decodeJSON(current) as SignedCertificateEnvelope;
    if (!envelope.claims || !envelope.signature) throw new Error("desktop_endpoint_certificate_chain_invalid");
    const claims = decodeBase64URL(envelope.claims);
    const parsed = identityClaims(claims);
    metadata.push(parsed);
    result.push({ certificate: claims, signature: decodeBase64URL(envelope.signature), publicKeySPKI: parsed.publicKeySPKI });
    current = envelope.issuer_certificate ? decodeBase64URL(envelope.issuer_certificate) : undefined;
  }
  const now = Date.now(), leaf = metadata[0];
  if (!leaf || leaf.certificateVersion !== "desktop.v1" || leaf.homeID !== session.home_id || leaf.identityType !== "endpoint" ||
      leaf.agentID !== session.agent_id || leaf.trustRootGeneration !== rootGeneration || leaf.createdAt > now + 300_000 || leaf.expiresAt <= now) throw new Error("desktop_endpoint_certificate_invalid");
  for (let index = 1; index < metadata.length; index++) {
    const issuer = metadata[index], required = index === 1 ? "endpoint.approve" : "operator.approve";
    if (issuer.homeID !== session.home_id || issuer.identityType !== "operator_device" || issuer.trustRootGeneration !== rootGeneration ||
        issuer.createdAt > now + 300_000 || issuer.expiresAt <= now || !issuer.capabilities.includes(required)) throw new Error("desktop_endpoint_certificate_chain_invalid");
  }
  return result;
}

function encodeJSON(value: unknown): Uint8Array { return new TextEncoder().encode(JSON.stringify(value)); }
function decodeJSON(value: Uint8Array): unknown { try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(value)); } catch { throw new Error("desktop_handshake_json_invalid"); } }
interface IdentityClaims { certificateVersion: string; homeID: string; identityType: string; agentID: string; publicKeySPKI: Uint8Array; capabilities: string[]; trustRootGeneration: number; createdAt: number; expiresAt: number }
function identityClaims(claims: Uint8Array): IdentityClaims {
  let offset = 0;
  const field = () => { if (offset + 4 > claims.length) throw new Error("desktop_endpoint_claims_invalid"); const length = new DataView(claims.buffer, claims.byteOffset + offset, 4).getUint32(0, false); offset += 4; if (offset + length > claims.length) throw new Error("desktop_endpoint_claims_invalid"); const value = claims.slice(offset, offset + length); offset += length; return value; };
  const text = () => new TextDecoder("utf-8", { fatal: true }).decode(field());
  const fields = Array.from({ length: 8 }, text), publicKeySPKI = field();
  if (offset + 4 > claims.length) throw new Error("desktop_endpoint_claims_invalid");
  const count = new DataView(claims.buffer, claims.byteOffset + offset, 4).getUint32(0, false); offset += 4;
  if (count > 32) throw new Error("desktop_endpoint_claims_invalid");
  const capabilities = Array.from({ length: count }, text);
  if (offset + 20 !== claims.length || !publicKeySPKI.length) throw new Error("desktop_endpoint_claims_invalid");
  const view = new DataView(claims.buffer, claims.byteOffset + offset, 20);
  const trustRootGeneration = view.getUint32(0, false), createdAt = Number(view.getBigInt64(4, false)), expiresAt = Number(view.getBigInt64(12, false));
  return { certificateVersion: fields[1], homeID: fields[2], identityType: fields[4], agentID: fields[7], publicKeySPKI, capabilities, trustRootGeneration, createdAt, expiresAt };
}

async function fingerprint(value: Uint8Array): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(value)));
  return encodeBase64URL(digest);
}
