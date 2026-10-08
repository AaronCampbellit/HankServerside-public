import { describe, expect, it } from "vitest";
import { QuickLinksClient } from "./quickLinks";

describe("QuickLinksClient", () => {
  it("loads home quick links", async () => {
    const payload = {
      can_edit: true,
      links: [{
        id: "ql_1",
        home_id: "home_1",
        title: "Home Assistant",
        url: "https://ha.example.test",
        sort_order: 0,
        health_check_enabled: true,
        status: "up",
        status_code: 200,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
        updated_by: "usr_1",
      }],
    };
    const paths: string[] = [];
    const request = async <T,>(path: string) => {
      paths.push(path);
      return payload as T;
    };

    const result = await new QuickLinksClient({ request }).list();

    expect(paths).toEqual(["/v1/home/quick-links"]);
    expect(result.links).toHaveLength(1);
    expect(result.can_edit).toBe(true);
  });

  it("normalizes nullable quick link lists", async () => {
    const request = async <T,>() => ({ can_edit: true, links: null }) as T;

    const result = await new QuickLinksClient({ request }).list();

    expect(result.links).toEqual([]);
    expect(result.can_edit).toBe(true);
  });

  it("writes quick link mutations to the existing home endpoints", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return { can_edit: true, links: [] } as T;
    };
    const client = new QuickLinksClient({ request });
    const input = {
      title: "Home Assistant",
      url: "https://ha.example.test",
      description: "Local controls",
      health_check_enabled: true,
    };

    await client.create(input);
    await client.update("ql_1", input);
    await client.remove("ql_1");
    await client.reorder(["ql_2", "ql_1"]);
    await client.check();

    expect(calls).toEqual([
      { path: "/v1/home/quick-links", method: "POST", body: input },
      { path: "/v1/home/quick-links/ql_1", method: "PUT", body: input },
      { path: "/v1/home/quick-links/ql_1", method: "DELETE", body: undefined },
      { path: "/v1/home/quick-links/order", method: "PUT", body: { ids: ["ql_2", "ql_1"] } },
      { path: "/v1/home/quick-links/checks", method: "POST", body: {} },
    ]);
  });
});
