import { describe, expect, it, vi } from "vitest";
import { FileServerClient, type FileServerSocket } from "./fileServer";
import type { ApiTransport } from "./client";

describe("FileServerClient", () => {
  it("sends file commands through the app socket", async () => {
    const socket = {
      sendCommand: vi.fn(async () => ({ items: [] })),
      subscribe: vi.fn(),
      onEvent: vi.fn(),
    };
    const transport = { request: vi.fn(async () => ({ items: [], status: "indexing" })) };
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport as unknown as ApiTransport);

    await client.list("/", "hankdemo");
    await client.search("invoice", "hankdemo");
    await client.stat("/old.txt", "hankdemo");
    await client.createDirectory("/Photos", "hankdemo");
    await client.rename("/old.txt", "/archive/old.txt", "hankdemo");
    await client.move("/old.txt", "/archive/old.txt", false, "hankdemo", "hankdemo2");
    await client.deleteItem("/old.txt", false, "hankdemo");

    expect(socket.sendCommand).toHaveBeenNthCalledWith(1, "files.list", { path: "/", source_id: "hankdemo" }, { timeoutMs: 125000 });
    expect(transport.request).toHaveBeenCalledWith("/v1/home/file-search?q=invoice&source_id=hankdemo", { signal: undefined, timeoutMs: 10000 });
    expect(socket.sendCommand).toHaveBeenNthCalledWith(2, "files.stat", { path: "/old.txt", source_id: "hankdemo" });
    expect(socket.sendCommand).toHaveBeenNthCalledWith(3, "files.create_directory", { path: "/Photos", source_id: "hankdemo" });
    expect(socket.sendCommand).toHaveBeenNthCalledWith(4, "files.rename", { from: "/old.txt", to: "/archive/old.txt", source_id: "hankdemo" });
    expect(socket.sendCommand).toHaveBeenNthCalledWith(5, "files.move", { from: "/old.txt", to: "/archive/old.txt", is_directory: false, source_id: "hankdemo", destination_source_id: "hankdemo2" });
    expect(socket.sendCommand).toHaveBeenNthCalledWith(6, "files.delete", { path: "/old.txt", is_directory: false, source_id: "hankdemo" });
  });

  it("targets worker agents for every file command", async () => {
    const sendCommand = vi.fn(async (_command: string, _body?: unknown, _options?: { agentID?: string }) => ({ items: [] }));
    const socket = {
      sendCommand,
      subscribe: vi.fn(),
      onEvent: vi.fn(),
    };
    const transport = { request: vi.fn(async () => ({ items: [] })) };
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport as unknown as ApiTransport);

    await client.list("/", undefined, "worker-1");
    await client.search("invoice", undefined, "worker-1");
    await client.stat("/old.txt", undefined, "worker-1");
    await client.createDirectory("/Photos", undefined, "worker-1");
    await client.rename("/old.txt", "/archive/old.txt", undefined, "worker-1");
    await client.move("/old.txt", "/archive/old.txt", false, undefined, undefined, "worker-1");
    await client.deleteItem("/old.txt", false, undefined, "worker-1");

    expect(transport.request).toHaveBeenCalledWith("/v1/home/file-search?q=invoice&agent_id=worker-1", { signal: undefined, timeoutMs: 10000 });
    for (const call of sendCommand.mock.calls) {
      expect(call[2]).toEqual(call[0] === "files.list"
        ? { agentID: "worker-1", timeoutMs: 125000 }
        : { agentID: "worker-1" });
    }
  });

  it("cancels a transfer job through the scoped Home route", async () => {
    const transport = { request: vi.fn(async () => ({ id: "job-1", operation: "upload", status: "cancelled" })) };
    const client = new FileServerClient({} as FileServerSocket, transport as unknown as ApiTransport);

    await client.cancelJob("job-1");

    expect(transport.request).toHaveBeenCalledWith("/v1/home/file-jobs/job-1/cancel", { method: "POST" });
  });

  it("sets up browser downloads and binary uploads through the transfer routes", async () => {
    const socket = { sendCommand: vi.fn() };
    const requestCalls: Array<{ path: string; options?: Parameters<ApiTransport["request"]>[1] }> = [];
    const transport: ApiTransport = {
      async request<T>(path: string, options?: Parameters<ApiTransport["request"]>[1]): Promise<T> {
        requestCalls.push({ path, options });
        return {
          url: path.includes("uploads") ? "/v1/file-transfers/upload-1" : "/v1/file-transfers/download-1",
          transfer_token: path.includes("uploads") ? "upload-token" : "download-token",
          method: path.includes("uploads") ? "PUT" : "GET",
          requested: options?.body,
        } as T;
      },
    };
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ size: 5 }), {
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport);
    const file = new File(["hello"], "hello.txt", { type: "text/plain" });

    const download = await client.setupDownload("/Media/hello.txt", "hankdemo", "worker-1");
    await client.uploadFile(file, "/Media", "hankdemo", "worker-1", "hello copy.txt");

    expect(requestCalls[0]).toEqual({
      path: "/v1/home/files/downloads",
      options: {
        method: "POST",
        body: { path: "/Media/hello.txt", source_id: "hankdemo", agent_id: "worker-1" },
      },
    });
    expect(download.url).toBe("/v1/file-transfers/download-1");
    expect(requestCalls[1]).toEqual({
      path: "/v1/home/files/uploads",
      options: {
        method: "POST",
        body: { path: "/Media/hello copy.txt", source_id: "hankdemo", agent_id: "worker-1", size: 5 },
      },
    });
    expect(fetchMock).toHaveBeenCalledWith("/v1/file-transfers/upload-1", expect.objectContaining({
      method: "PUT",
      body: file,
    }));
    expect(fetchMock.mock.calls[0]?.[1]?.headers).toEqual(expect.objectContaining({
      Authorization: "Bearer upload-token",
      "Content-Type": "application/octet-stream",
    }));
  });

  it("lists file jobs and subscribes to job change events", async () => {
    const listener = vi.fn();
    const socket = {
      sendCommand: vi.fn(),
      subscribe: vi.fn(async () => ({})),
      onEvent: vi.fn((handler: (event: { topic?: string; event?: string }) => void) => {
        handler({ topic: "files.jobs", event: "files.job_changed" });
        handler({ topic: "files.jobs", event: "files.history_changed" });
        handler({ topic: "homeassistant.states", event: "homeassistant.state_changed" });
        return () => undefined;
      }),
    };
    const transport: ApiTransport = {
      async request<T>(path: string): Promise<T> {
        expect(path).toBe("/v1/home/file-jobs?limit=10");
        return {
          jobs: [
            { id: "filejob_1", operation: "upload", status: "running", from_path: "/Media/new.txt" },
          ],
        } as T;
      },
    };
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport);

    const jobs = await client.listJobs(10);
    await client.subscribeToJobs();
    client.onJobsChanged(listener);

    expect(jobs).toHaveLength(1);
    expect(jobs[0]?.id).toBe("filejob_1");
    expect(socket.subscribe).toHaveBeenCalledWith(["files.jobs"]);
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it("clears finished file-job history through the authenticated API", async () => {
    const socket = { sendCommand: vi.fn() };
    const transport: ApiTransport = {
      async request<T>(path: string, options?: Parameters<ApiTransport["request"]>[1]): Promise<T> {
        expect(path).toBe("/v1/home/file-jobs");
        expect(options).toEqual({ method: "DELETE" });
        return { cleared: 3 } as T;
      },
    };
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport);

    await expect(client.clearFinishedJobs()).resolves.toBe(3);
  });

  it("preserves structured upload-limit details from the transfer response", async () => {
    const socket = { sendCommand: vi.fn() };
    const transport: ApiTransport = {
      async request<T>(): Promise<T> {
        return { url: "/v1/file-transfers/upload-1", method: "PUT" } as T;
      },
    };
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      error: { code: "upload_too_large", message: "This file is larger than the maximum upload size." },
      max_upload_bytes: 1024,
      attempted_upload_bytes: 2048,
    }), { status: 413, headers: { "Content-Type": "application/json" } })));
    const client = new FileServerClient(socket as unknown as FileServerSocket, transport);
    const file = new File([new Uint8Array(2048)], "large.bin");

    await expect(client.uploadFile(file, "/Media")).rejects.toMatchObject({
      status: 413,
      code: "upload_too_large",
      payload: expect.objectContaining({ max_upload_bytes: 1024, attempted_upload_bytes: 2048 }),
    });
  });
});
