import { describe, expect, it, vi } from "vitest";
import { AppsClient } from "./apps";
import type { ApiTransport } from "./client";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("AppsClient", () => {
  it("lists home apps", async () => {
    const request = vi.fn(async () => ({ apps: [] }));
    const client = new AppsClient(testTransport(request));

    await client.listApps();

    expect(request).toHaveBeenCalledWith("/v1/home/apps");
  });

  it("normalizes nullable app lists", async () => {
    const request = vi.fn(async () => ({ apps: null }));
    const client = new AppsClient(testTransport(request));

    await expect(client.listApps()).resolves.toEqual({ apps: [] });
  });

  it("previews and activates app packages", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new AppsClient(testTransport(request));
    const formData = new FormData();

    await client.previewPackage(formData);
    await client.activatePackage({ staging_id: "stage_1", package_sha256: "sha", enable: false });

    expect(request).toHaveBeenCalledWith("/v1/home/apps/import/preview", {
      method: "POST",
      body: formData,
    });
    expect(request).toHaveBeenCalledWith("/v1/home/apps/import/activate", {
      method: "POST",
      body: { staging_id: "stage_1", package_sha256: "sha", enable: false },
    });
  });

  it("saves app config", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new AppsClient(testTransport(request));

    await client.saveConfig("hermes", {
      public_config: { api_base_url: "https://hermes.local" },
      secrets: { api_key: "secret" },
      enable: true,
      user_access: "home_members",
    });

    expect(request).toHaveBeenCalledWith("/v1/home/apps/hermes/config", {
      method: "PUT",
      body: {
        public_config: { api_base_url: "https://hermes.local" },
        secrets: { api_key: "secret" },
        enable: true,
        user_access: "home_members",
      },
    });
  });

  it("uninstalls an app", async () => {
    const request = vi.fn(async () => ({ app_id: "gramaton" }));
    const client = new AppsClient(testTransport(request));

    await client.uninstall("gramaton");

    expect(request).toHaveBeenCalledWith("/v1/home/apps/gramaton", {
      method: "DELETE",
    });
  });
});
