import { encodeBase64URL, exactBuffer } from "./base64url";
import { p256P1363ToDER } from "./crypto";
import { IndexedDBDesktopIdentityStore } from "./identityStore";
import { desktopTrustClient, type DesktopAutoEnrollment } from "../api/desktopTrust";

const deviceIDKey = "hank-desktop-device-id";
const enrollmentInFlight = new Map<string, Promise<DesktopAutoEnrollment>>();
const completedEnrollments = new Map<string, DesktopAutoEnrollment>();

export function ensureBrowserDesktopIdentity(homeID: string, userID: string, sessionID: string): Promise<DesktopAutoEnrollment> {
  const scope = `${homeID}:${userID}:${sessionID}`;
  const completed = completedEnrollments.get(scope);
  if (completed) return Promise.resolve(completed);
  const existing = enrollmentInFlight.get(scope);
  if (existing) return existing;
  const operation = enrollBrowserDesktopIdentity(homeID, userID, sessionID)
    .then(enrollment => {
      completedEnrollments.set(scope, enrollment);
      return enrollment;
    })
    .finally(() => enrollmentInFlight.delete(scope));
  enrollmentInFlight.set(scope, operation);
  return operation;
}

async function enrollBrowserDesktopIdentity(homeID: string, userID: string, sessionID: string): Promise<DesktopAutoEnrollment> {
  if (!homeID || !userID || !sessionID) throw new Error("desktop_browser_session_unavailable");
  let deviceID = localStorage.getItem(deviceIDKey) || "";
  if (!deviceID) { deviceID = `browser-${crypto.randomUUID()}`; localStorage.setItem(deviceIDKey, deviceID); }
  const store = new IndexedDBDesktopIdentityStore();
  let keyPair = await store.get(deviceID);
  let spki = await store.getPublicSPKI(deviceID);
  if (!keyPair || !spki) {
    const created = await store.create(deviceID);
    keyPair = created.keyPair;
    spki = created.spki;
  }
  const challenge = await desktopTrustClient.autoChallenge(deviceID);
  const transcript = enrollmentTranscript(homeID, userID, sessionID, "browser_operator", challenge.challenge_id, challenge.challenge, deviceID, spki);
  // WebCrypto returns fixed-width IEEE P1363 signatures; the Hank API uses
  // the canonical ASN.1 DER representation shared by the native agents.
  const signature = await signBrowserEnrollment(keyPair.privateKey, transcript);
  const enrollment = await desktopTrustClient.autoEnroll({ challenge_id: challenge.challenge_id, challenge: challenge.challenge, installation_id: deviceID,
    device_id: deviceID, public_key_spki: encodeBase64URL(spki), signature: encodeBase64URL(signature) });
  window.dispatchEvent(new Event("hank-desktop-auto-enrolled"));
  return enrollment;
}

export async function signBrowserEnrollment(privateKey: CryptoKey, transcript: Uint8Array): Promise<Uint8Array> {
  // WebCrypto returns fixed-width IEEE P1363 signatures; the Hank API uses
  // the canonical ASN.1 DER representation shared by the native agents.
  return p256P1363ToDER(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, privateKey, exactBuffer(transcript))));
}

function enrollmentTranscript(homeID: string, userID: string, sessionID: string, purpose: string, challengeID: string, challenge: string, installationID: string, spki: Uint8Array): Uint8Array {
  return new TextEncoder().encode(["Hank Desktop Device Enrollment v1", homeID, userID, sessionID, purpose, challengeID, challenge, installationID, encodeBase64URL(spki)].join("\n"));
}
