import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, csrfToken } from "./client";

describe("ApiClient", () => {
  beforeEach(() => {
    document.cookie = "hank_csrf=csrf-value";
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.cookie = "hank_csrf=; Max-Age=0";
  });

  it("adds CSRF and JSON content type for cookie-authenticated unsafe JSON requests", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ ok: true }), {
      headers: { "Content-Type": "application/json" },
      status: 200,
    }));
    vi.stubGlobal("fetch", fetchMock);

    await new ApiClient().request("/v1/test", { method: "POST", body: { name: "Hank" } });

    const init = fetchMock.mock.calls[0]?.[1];
    expect(init).toBeDefined();
    const headers = init?.headers as Headers;
    expect(init?.credentials).toBe("same-origin");
    expect(init?.body).toBe(JSON.stringify({ name: "Hank" }));
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("X-Hank-CSRF-Token")).toBe("csrf-value");
  });

  it("does not force JSON content type for FormData", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ ok: true }), {
      headers: { "Content-Type": "application/json" },
      status: 200,
    }));
    vi.stubGlobal("fetch", fetchMock);

    const formData = new FormData();
    formData.set("package", new Blob(["app"]));
    await new ApiClient().request("/v1/upload", { method: "POST", body: formData });

    const init = fetchMock.mock.calls[0]?.[1];
    expect(init).toBeDefined();
    const headers = init?.headers as Headers;
    expect(init?.body).toBe(formData);
    expect(headers.has("Content-Type")).toBe(false);
  });

  it("uses bearer auth without CSRF when a bearer token override is provided", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ ok: true }), {
      headers: { "Content-Type": "application/json" },
      status: 200,
    }));
    vi.stubGlobal("fetch", fetchMock);

    await new ApiClient().request("/v1/test", { method: "POST", bearerToken: "session-token", body: { ok: true } });

    const init = fetchMock.mock.calls[0]?.[1];
    expect(init).toBeDefined();
    const headers = init?.headers as Headers;
    expect(headers.get("Authorization")).toBe("Bearer session-token");
    expect(headers.has("X-Hank-CSRF-Token")).toBe(false);
  });

  it("preserves normalized error fields", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      error: "permission_denied",
      message: "Files are disabled.",
      details: { feature: "files" },
    }), {
      headers: { "Content-Type": "application/json" },
      status: 403,
      statusText: "Forbidden",
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(new ApiClient().request("/v1/test")).rejects.toMatchObject({
      status: 403,
      code: "permission_denied",
      message: "Files are disabled.",
    } satisfies Partial<ApiError>);
  });

  it("reads the Hank CSRF cookie", () => {
    expect(csrfToken("other=1; hank_csrf=abc%20123")).toBe("abc%20123");
  });
});
