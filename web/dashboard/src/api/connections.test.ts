import { describe, expect, it } from "vitest";
import { ConnectionsClient } from "./connections";

describe("ConnectionsClient", () => {
  it("uses the existing service profile endpoints", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return {} as T;
    };
    const client = new ConnectionsClient({ request });

    await client.listProfiles();
    await client.saveProfile("homeassistant", {
      public_config: { base_url: "http://ha.local:8123", timeout_seconds: 10 },
      secrets: { token: "ha-token" },
      persist: true,
    });
    await client.testSMB({
      id: "archive",
      name: "Archive",
      host: "backup.local",
      share: "archive",
      username: "backup",
      password: "secret",
      domain: "WORKGROUP",
    });

    expect(calls).toEqual([
      { path: "/v1/home/service-profiles", method: undefined, body: undefined },
      {
        path: "/v1/home/service-profiles/homeassistant",
        method: "PUT",
        body: {
          public_config: { base_url: "http://ha.local:8123", timeout_seconds: 10 },
          secrets: { token: "ha-token" },
          persist: true,
        },
      },
      {
        path: "/v1/home/service-profiles/smb/test",
        method: "POST",
        body: {
          id: "archive",
          name: "Archive",
          host: "backup.local",
          share: "archive",
          username: "backup",
          password: "secret",
          domain: "WORKGROUP",
        },
      },
    ]);
  });
});
