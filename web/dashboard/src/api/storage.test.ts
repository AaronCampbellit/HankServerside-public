import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { StorageClient } from "./storage";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("StorageClient", () => {
  it("loads storage status", async () => {
    const request = vi.fn(async () => ({ backup: {} }));
    const client = new StorageClient(testTransport(request));

    await client.status();

    expect(request).toHaveBeenCalledWith("/v1/home/storage/status");
  });

  it("normalizes nullable storage collections", async () => {
    const request = vi.fn(async () => ({ backup: { backups: null }, tasks: null, events: null }));
    const client = new StorageClient(testTransport(request));

    await expect(client.status()).resolves.toMatchObject({
      backup: { backups: [] },
      tasks: [],
      events: [],
    });
  });

  it("saves config and requests storage jobs", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new StorageClient(testTransport(request));
    const config = {
      target: { type: "posix", path: "/var/lib/pgbackrest" },
      schedule: { retention_full: 3 },
    };

    await client.saveConfig(config);
    await client.requestBackup("full");
    await client.requestRestoreTest("20260627-010000F");
    await client.clearEvents();

    expect(request).toHaveBeenCalledWith("/v1/home/storage/config", { method: "PUT", body: config });
    expect(request).toHaveBeenCalledWith("/v1/home/storage/backup", { method: "POST", body: { backup_type: "full" } });
    expect(request).toHaveBeenCalledWith("/v1/home/storage/restore-test", {
      method: "POST",
      body: { backup_label: "20260627-010000F" },
    });
    expect(request).toHaveBeenCalledWith("/v1/home/storage/events", { method: "DELETE" });
  });

  it("loads query telemetry", async () => {
    const request = vi.fn(async () => ({ queries: [{ query: "select 1", calls: 2 }] }));
    const client = new StorageClient(testTransport(request));

    const result = await client.queryTelemetry(10);

    expect(request).toHaveBeenCalledWith("/v1/home/query-telemetry?limit=10");
    expect(result.queries).toEqual([{ query: "select 1", calls: 2 }]);
  });
});
