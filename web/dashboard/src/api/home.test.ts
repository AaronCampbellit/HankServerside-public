import { describe, expect, it } from "vitest";
import { HomeClient } from "./home";

describe("HomeClient", () => {
  it("uses the existing home and agent endpoints", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return {} as T;
    };
    const client = new HomeClient({ request });

    await client.getHome();
    await client.renameHome("Campbell Home");
    await client.getAgent();
    await client.restartAgent();
    await client.listAgentTokens();
    await client.createAgentToken({ agent_id: "campbell-home", name: "Campbell Home Agent", agent_type: "primary", expires_in_seconds: 3600 });
    await client.revokeAgentToken("agtok_1");
    await client.removeAgentToken("agtok_1");

    expect(calls).toEqual([
      { path: "/v1/home", method: undefined, body: undefined },
      { path: "/v1/home", method: "PUT", body: { name: "Campbell Home" } },
      { path: "/v1/home/agent", method: undefined, body: undefined },
      { path: "/v1/home/agent/restart", method: "POST", body: {} },
      { path: "/v1/home/agent/tokens", method: undefined, body: undefined },
      {
        path: "/v1/home/agent/tokens",
        method: "POST",
        body: { agent_id: "campbell-home", name: "Campbell Home Agent", agent_type: "primary", expires_in_seconds: 3600 },
      },
      { path: "/v1/home/agent/tokens/agtok_1", method: "DELETE", body: undefined },
      { path: "/v1/home/agent/tokens/agtok_1?purge=1", method: "DELETE", body: undefined },
    ]);
  });
});
