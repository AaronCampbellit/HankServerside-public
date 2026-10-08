import { decodeBase64URL, encodeBase64URL, exactBuffer } from "../base64url";
import { p256P1363ToDER } from "../crypto";
import type { DesktopIdentityStore } from "../identityStore";
import { decodeRecoveryCode, encodeRecoveryCode, generateRecoverySecret } from "./recoveryCode";

const adminCapabilities = ["operator.approve", "endpoint.approve", "trust.recover", "trust.rotate"];
const endpointCapabilities = ["desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write", "desktop.elevate", "desktop.secure_desktop", "desktop.unattended"];
export interface DesktopApprovalRequest { identity_type: "operator_device"|"endpoint"; identity_id: string; device_id?: string; agent_id?: string; public_key_spki: string; capabilities: string[]; created_at: string; expires_at: string; platform?: string }
export interface ReviewedDesktopApproval { request: DesktopApprovalRequest; fingerprint: string }
export interface BootstrapInput { homeID: string; userID: string; deviceID: string; store: DesktopIdentityStore; now?: Date }
export interface DesktopTrustCeremony { body: Record<string, unknown>; recoveryCode: string; cleanup: () => Promise<void> }

export async function createDesktopTrustBootstrap(input: BootstrapInput): Promise<DesktopTrustCeremony> {
	const generated = await createTrustGeneration(input, 1);
	return { recoveryCode: generated.recoveryCode, cleanup: generated.cleanup, body: { generation: 1, public_key_spki: generated.publicKeySPKI, recovery_envelope: generated.recoveryEnvelope, confirmation: "create desktop trust", first_operator: generated.operator } };
}

export async function createDesktopTrustReset(input: BootstrapInput, generation: number): Promise<DesktopTrustCeremony> {
	const generated = await createTrustGeneration(input, generation);
	return { recoveryCode: generated.recoveryCode, cleanup: generated.cleanup, body: { generation, public_key_spki: generated.publicKeySPKI, recovery_envelope: generated.recoveryEnvelope, confirmation: "reset desktop trust", replacement_operator: generated.operator } };
}

export async function createDesktopTrustRotation(input: BootstrapInput, oldGeneration: number, oldRootPrivateKey: CryptoKey): Promise<DesktopTrustCeremony> {
	const generation = oldGeneration + 1, generated = await createTrustGeneration(input, generation);
	const envelope = decodeBase64URL(generated.recoveryEnvelope), envelopeHash = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(envelope)));
	const operator = generated.operator as Record<string, unknown>;
	const proof = encodeRootRotationProof(input.homeID, oldGeneration, generation, decodeBase64URL(generated.publicKeySPKI), envelopeHash, String(operator.identity_id), generated.createdAt);
	const signature = p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, oldRootPrivateKey, exactBuffer(proof))));
	return { recoveryCode: generated.recoveryCode, cleanup: generated.cleanup, body: { generation, public_key_spki: generated.publicKeySPKI, recovery_envelope: generated.recoveryEnvelope, replacement_operator: generated.operator, old_root_signature: encodeBase64URL(signature), confirmation: "rotate desktop trust" } };
}

async function createTrustGeneration(input: BootstrapInput, generation: number): Promise<{ recoveryCode: string; publicKeySPKI: string; recoveryEnvelope: string; operator: Record<string, unknown>; createdAt: number; cleanup: () => Promise<void> }> {
  if (!input.homeID || !input.userID || !input.deviceID) throw new Error("desktop_trust_scope_required");
  const now = input.now ?? new Date(), createdAt = now.getTime(), expiresAt = createdAt + 2 * 365 * 24 * 60 * 60 * 1000;
  const root = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  const rootPKCS8 = new Uint8Array(await crypto.subtle.exportKey("pkcs8", root.privateKey));
  const rootSPKI = new Uint8Array(await crypto.subtle.exportKey("spki", root.publicKey));
  const secret = generateRecoverySecret();
  const rootID = `root:${input.homeID}:${generation}`; let rootInstalled = false, operatorInstalled = false;
  try {
    const recoveryCode = await encodeRecoveryCode(secret);
    const recoveryEnvelope = await encryptRootEnvelope(secret, input.homeID, generation, rootPKCS8);
    await input.store.install(rootID, rootPKCS8, rootSPKI); rootInstalled = true;
    const operator = await input.store.create(input.deviceID);
    operatorInstalled = true;
    const identityID = `dop_${crypto.randomUUID().replaceAll("-", "")}`;
    const claims = encodeIdentityClaims(input.homeID, identityID, input.userID, input.deviceID, operator.spki, generation, createdAt, expiresAt);
    const signature = p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, root.privateKey, exactBuffer(claims))));
    const certificate = encodeBase64URL(new TextEncoder().encode(JSON.stringify({ claims: encodeBase64URL(claims), signature: encodeBase64URL(signature) })));
    return { recoveryCode, publicKeySPKI: encodeBase64URL(rootSPKI), recoveryEnvelope: encodeBase64URL(recoveryEnvelope), createdAt,
      cleanup: async () => { await Promise.allSettled([input.store.remove(rootID), input.store.remove(input.deviceID)]); },
      operator: { identity_id: identityID, device_id: input.deviceID, public_key_spki: encodeBase64URL(operator.spki), certificate, capabilities: adminCapabilities, expires_at: new Date(expiresAt).toISOString() } };
  } catch (error) {
    const removals: Promise<void>[] = []; if (rootInstalled) removals.push(input.store.remove(rootID)); if (operatorInstalled) removals.push(input.store.remove(input.deviceID));
    await Promise.allSettled(removals); throw error;
  } finally { rootPKCS8.fill(0); secret.fill(0); }
}

export async function decryptRecoveryRoot(code: string, homeID: string, generation: number, encodedEnvelope: string): Promise<CryptoKey> {
  const secret = await decodeRecoveryCode(code), context = encodeRecoveryContext(homeID, generation);
  try {
    const envelope = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(decodeBase64URL(encodedEnvelope))) as { version: number; nonce: string; ciphertext: string };
    if (envelope.version !== 1) throw new Error("desktop_recovery_envelope_invalid");
    const salt = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(context)));
    const material = await crypto.subtle.importKey("raw", exactBuffer(secret), "HKDF", false, ["deriveKey"]);
    const key = await crypto.subtle.deriveKey({ name: "HKDF", hash: "SHA-256", salt: exactBuffer(salt), info: exactBuffer(new TextEncoder().encode("hank-desktop-v1/root-recovery/key")) }, material, { name: "AES-GCM", length: 256 }, false, ["decrypt"]);
    const pkcs8 = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: exactBuffer(decodeBase64URL(envelope.nonce)), additionalData: exactBuffer(context) }, key, exactBuffer(decodeBase64URL(envelope.ciphertext))));
    try { return await crypto.subtle.importKey("pkcs8", exactBuffer(pkcs8), { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]); }
    finally { pkcs8.fill(0); }
  } finally { secret.fill(0); }
}

export async function createDesktopRecoveryOperator(input: BootstrapInput, generation: number, rootPrivateKey: CryptoKey): Promise<{ operator: Record<string, unknown>; identityID: string; spki: Uint8Array; issuedAt: number; cleanup: () => Promise<void> }> {
  const issuedAt = (input.now ?? new Date()).getTime(), expiresAt = issuedAt + 2 * 365 * 24 * 60 * 60 * 1000;
  const created = await input.store.create(input.deviceID), identityID = `dop_${crypto.randomUUID().replaceAll("-", "")}`;
  try {
    const claims = encodeIdentityClaims(input.homeID, identityID, input.userID, input.deviceID, created.spki, generation, issuedAt, expiresAt);
    const signature = p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, rootPrivateKey, exactBuffer(claims))));
    const certificate = encodeBase64URL(new TextEncoder().encode(JSON.stringify({ claims: encodeBase64URL(claims), signature: encodeBase64URL(signature) })));
    return { identityID, spki: created.spki, issuedAt, cleanup: () => input.store.remove(input.deviceID), operator: { identity_id: identityID, device_id: input.deviceID, public_key_spki: encodeBase64URL(created.spki), certificate, capabilities: adminCapabilities, expires_at: new Date(expiresAt).toISOString() } };
  } catch (error) { await input.store.remove(input.deviceID); throw error; }
}

export async function signDesktopRecoveryEnrollment(rootPrivateKey: CryptoKey, homeID: string, generation: number, operator: { identityID: string; spki: Uint8Array; issuedAt: number }, deviceID: string, challenge: Uint8Array): Promise<string> {
  const parts: Uint8Array[] = [], encoder = new TextEncoder(), field = (value: string | Uint8Array) => { const bytes = typeof value === "string" ? encoder.encode(value) : value; const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, bytes.length, false); parts.push(length, bytes); };
  field("Hank Desktop Recovery Enrollment v1"); field("recover_operator_device"); field(homeID); const gen = new Uint8Array(4); new DataView(gen.buffer).setUint32(0,generation,false); parts.push(gen); field(operator.identityID); field(deviceID); field(operator.spki); const issued = new Uint8Array(8); new DataView(issued.buffer).setBigInt64(0,BigInt(operator.issuedAt),false); parts.push(issued); field(challenge);
  const signature = new Uint8Array(await crypto.subtle.sign({name:"ECDSA",hash:"SHA-256"},rootPrivateKey,exactBuffer(concat(parts)))); return encodeBase64URL(p256P1363ToDER(signature));
}

export async function reviewDesktopApprovalRequest(value: unknown): Promise<ReviewedDesktopApproval> {
  const request = value as Partial<DesktopApprovalRequest>, type = request.identity_type;
  const capabilities = Array.isArray(request.capabilities) ? request.capabilities : [], allowed = type === "operator_device" ? adminCapabilities : endpointCapabilities;
  const created = Date.parse(request.created_at ?? ""), expires = Date.parse(request.expires_at ?? ""), spki = decodeBase64URL(request.public_key_spki ?? "");
  if ((type !== "operator_device" && type !== "endpoint") || !request.identity_id?.trim() || spki.length === 0 || !Number.isFinite(created) || !Number.isFinite(expires) || expires <= Date.now() || expires-created > 2*365*24*60*60*1000 ||
      (type === "operator_device" ? !request.device_id?.trim() || Boolean(request.agent_id) : !request.agent_id?.trim() || Boolean(request.device_id)) || capabilities.length === 0 || capabilities.some(value => !allowed.includes(value)) || new Set(capabilities).size !== capabilities.length) throw new Error("desktop_approval_request_invalid");
  const fingerprint = encodeBase64URL(new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(spki))));
  return { request: request as DesktopApprovalRequest, fingerprint };
}

export async function signDesktopApproval(reviewed: ReviewedDesktopApproval, approverPrivateKey: CryptoKey, homeID: string, userID: string, generation: number): Promise<{identity_id:string;device_id?:string;agent_id?:string;public_key_spki:string;certificate:string;capabilities:string[];expires_at:string}> {
  const value = reviewed.request, created = Date.parse(value.created_at), expires = Date.parse(value.expires_at), spki = decodeBase64URL(value.public_key_spki);
  const claims = encodeIdentityClaimsFor(value.identity_type, homeID, value.identity_id, value.identity_type === "operator_device" ? userID : "", value.device_id ?? "", value.agent_id ?? "", spki, value.capabilities, generation, created, expires);
  const signature = p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({name:"ECDSA",hash:"SHA-256"},approverPrivateKey,exactBuffer(claims))));
  return { identity_id:value.identity_id, ...(value.device_id ? {device_id:value.device_id}:{}), public_key_spki:value.public_key_spki,
    certificate:encodeBase64URL(new TextEncoder().encode(JSON.stringify({claims:encodeBase64URL(claims),signature:encodeBase64URL(signature)}))), capabilities:value.capabilities, expires_at:value.expires_at };
}

async function encryptRootEnvelope(secret: Uint8Array, homeID: string, generation: number, pkcs8: Uint8Array): Promise<Uint8Array> {
  const context = encodeRecoveryContext(homeID, generation);
  const salt = new Uint8Array(await crypto.subtle.digest("SHA-256", exactBuffer(context)));
  const material = await crypto.subtle.importKey("raw", exactBuffer(secret), "HKDF", false, ["deriveKey"]);
  const key = await crypto.subtle.deriveKey({ name: "HKDF", hash: "SHA-256", salt: exactBuffer(salt), info: exactBuffer(new TextEncoder().encode("hank-desktop-v1/root-recovery/key")) }, material, { name: "AES-GCM", length: 256 }, false, ["encrypt"]);
  const nonce = new Uint8Array(12); crypto.getRandomValues(nonce);
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: exactBuffer(nonce), additionalData: exactBuffer(context) }, key, exactBuffer(pkcs8)));
  return new TextEncoder().encode(JSON.stringify({ version: 1, nonce: encodeBase64URL(nonce), ciphertext: encodeBase64URL(ciphertext) }));
}

function encodeRecoveryContext(homeID: string, generation: number): Uint8Array {
  const output: Uint8Array[] = [], field = (value: Uint8Array) => { const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, value.length, false); output.push(length, value); };
  field(new TextEncoder().encode("Hank Desktop Root Recovery v1")); field(new TextEncoder().encode(homeID));
  const number = new Uint8Array(4); new DataView(number.buffer).setUint32(0, generation, false); output.push(number); return concat(output);
}
function encodeIdentityClaims(homeID: string, identityID: string, userID: string, deviceID: string, spki: Uint8Array, generation: number, created: number, expires: number): Uint8Array {
	return encodeIdentityClaimsFor("operator_device",homeID,identityID,userID,deviceID,"",spki,adminCapabilities,generation,created,expires);
}
function encodeIdentityClaimsFor(identityType: string, homeID: string, identityID: string, userID: string, deviceID: string, agentID: string, spki: Uint8Array, capabilities: string[], generation: number, created: number, expires: number): Uint8Array {
  const parts: Uint8Array[] = [], encoder = new TextEncoder(), field = (value: string | Uint8Array) => { const bytes = typeof value === "string" ? encoder.encode(value) : value; const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, bytes.length, false); parts.push(length, bytes); };
  for (const value of ["Hank Desktop Identity Certificate v1", "desktop.v1", homeID, identityID, identityType, userID, deviceID, agentID]) field(value);
  field(spki); const count = new Uint8Array(4); new DataView(count.buffer).setUint32(0, capabilities.length, false); parts.push(count); for (const capability of capabilities) field(capability);
  const numbers = new Uint8Array(20), view = new DataView(numbers.buffer); view.setUint32(0, generation, false); view.setBigInt64(4, BigInt(created), false); view.setBigInt64(12, BigInt(expires), false); parts.push(numbers); return concat(parts);
}
function encodeRootRotationProof(homeID: string, oldGeneration: number, newGeneration: number, spki: Uint8Array, envelopeHash: Uint8Array, identityID: string, issuedAt: number): Uint8Array {
  const parts: Uint8Array[] = [], encoder = new TextEncoder(), field = (value: string | Uint8Array) => { const bytes = typeof value === "string" ? encoder.encode(value) : value; const length = new Uint8Array(4); new DataView(length.buffer).setUint32(0, bytes.length, false); parts.push(length, bytes); };
  field("Hank Desktop Root Rotation v1"); field(homeID); const generations = new Uint8Array(8), view = new DataView(generations.buffer); view.setUint32(0, oldGeneration, false); view.setUint32(4, newGeneration, false); parts.push(generations); field(spki); field(envelopeHash); field(identityID); const timestamp = new Uint8Array(8); new DataView(timestamp.buffer).setBigInt64(0, BigInt(issuedAt), false); parts.push(timestamp); return concat(parts);
}
function concat(parts: Uint8Array[]): Uint8Array { const value = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0)); let offset = 0; for (const part of parts) { value.set(part, offset); offset += part.length; } return value; }
