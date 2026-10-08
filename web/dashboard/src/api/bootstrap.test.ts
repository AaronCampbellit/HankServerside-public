import { describe, expect, it } from "vitest";
import { BootstrapClient, type BootstrapState } from "./bootstrap";

describe("BootstrapClient", () => {
  it("loads the UI bootstrap payload", async () => {
    const payload: BootstrapState = {
      user: {
        id: "usr_1",
        email: "hank@example.com",
        display_name: "Hank User",
        password_change_required: false,
        created_at: "2026-06-27T00:00:00Z",
        updated_at: "2026-06-27T00:00:00Z",
      },
      session: { id: "sess_1", expires_at: "2026-06-28T00:00:00Z" },
      home: {
        id: "home_1",
        user_id: "usr_1",
        name: "Home",
        created_at: "2026-06-27T00:00:00Z",
        updated_at: "2026-06-27T00:00:00Z",
      },
      membership: {
        home_id: "home_1",
        user_id: "usr_1",
        role: "admin",
        created_at: "2026-06-27T00:00:00Z",
        updated_at: "2026-06-27T00:00:00Z",
      },
      permissions: {
        is_admin: true,
        can_manage_people: true,
        can_manage_settings: true,
        can_use_homeassistant: true,
        can_use_files: true,
        can_use_notes: true,
        can_use_assistant: true,
        can_view_storage: true,
        can_manage_apps: true,
      },
      agent: null,
      setup_status: { first_setup_visible: false },
      features: { mcp_enabled: true },
      server: { version: "dev" },
      navigation: [{ path: "/dashboard", label: "Dashboard" }],
    };
    const api = {
      request: async <T,>(path: string) => {
        expect(path).toBe("/v1/ui/bootstrap");
        return payload as T;
      },
    };

    await expect(new BootstrapClient(api).load()).resolves.toEqual(payload);
  });

  it("normalizes sparse bootstrap payloads", async () => {
    const api = {
      request: async <T,>() => ({
        permissions: null,
        features: null,
        setup_status: null,
        navigation: null,
        agent: { agent_id: "agent_1", name: "Agent", status: "online", home_id: "home_1", home_name: "Home", capabilities: null },
      }) as T,
    };

    const payload = await new BootstrapClient(api).load();

    expect(payload.permissions.is_admin).toBe(false);
    expect(payload.features.mcp_enabled).toBe(false);
    expect(payload.setup_status.first_setup_visible).toBe(false);
    expect(payload.navigation).toEqual([]);
    expect(payload.agent?.capabilities).toEqual([]);
  });
});
