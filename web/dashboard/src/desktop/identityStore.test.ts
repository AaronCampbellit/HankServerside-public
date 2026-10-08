import "fake-indexeddb/auto";
import { afterEach, describe, expect, it } from "vitest";
import { IndexedDBDesktopIdentityStore } from "./identityStore";

describe("desktop identity store", () => {
  afterEach(async () => { await new IndexedDBDesktopIdentityStore().clearForTests(); });

  it("reopens and signs with a structured-cloned non-exportable private key", async () => {
    const created = await new IndexedDBDesktopIdentityStore().create("device_1");
    await expect(crypto.subtle.exportKey("pkcs8", created.keyPair.privateKey)).rejects.toThrow();
    const reopened = await new IndexedDBDesktopIdentityStore().get("device_1");
    expect(reopened).not.toBeNull();
    const data = new TextEncoder().encode("desktop identity proof");
    const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, reopened!.privateKey, data);
    expect(await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, reopened!.publicKey, signature, data)).toBe(true);
    expect(await new IndexedDBDesktopIdentityStore().getPublicSPKI("device_1")).toEqual(created.spki);
  });

  it("imports acceptance material as a non-exportable browser identity", async () => {
    const source = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    const pkcs8 = new Uint8Array(await crypto.subtle.exportKey("pkcs8", source.privateKey));
    const spki = new Uint8Array(await crypto.subtle.exportKey("spki", source.publicKey));
    const imported = await new IndexedDBDesktopIdentityStore().install("device_acceptance", pkcs8, spki);
    const reopened = await new IndexedDBDesktopIdentityStore().get("device_acceptance");
    expect(imported.privateKey.extractable).toBe(false);
    expect(reopened?.privateKey.extractable).toBe(false);
  });

  it("removes every superseded identity in one transaction", async () => {
    const store = new IndexedDBDesktopIdentityStore();
    await store.create("old-root"); await store.create("old-operator"); await store.create("replacement");
    await store.removeMany(["old-root", "old-operator", "old-root"]);
    expect(await store.get("old-root")).toBeNull(); expect(await store.get("old-operator")).toBeNull();
    expect(await store.get("replacement")).not.toBeNull();
  });
});
