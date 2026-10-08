export function encodeBase64URL(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

export function decodeBase64URL(value: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]+$/.test(value)) throw new Error("desktop_invalid_base64url");
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4);
  const binary = atob(padded);
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}

export function exactBuffer(value: Uint8Array): ArrayBuffer {
  return value.slice().buffer as ArrayBuffer;
}
