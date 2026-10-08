import { describe, expect, it } from "vitest";
import { BrowserHandshakeDriver } from "./BrowserHandshakeDriver";

describe("BrowserHandshakeDriver", () => {
  it("refuses session authorization missing transcript bindings", async () => {
    const keys = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
    const driver = new BrowserHandshakeDriver({ operatorPrivateKey: keys.privateKey, trustRootPublicKeySPKI: new Uint8Array([1]), trustRootGeneration: 1 });
    await expect(driver.browserHandshakeFrame({ session_id: "desk_1", agent_id: "agent_1", state: "offered", key_epoch: 1, websocket_path: "/ws/desktop/browser/desk_1" })).rejects.toThrow("desktop_session_handshake_fields_missing");
  });
});
