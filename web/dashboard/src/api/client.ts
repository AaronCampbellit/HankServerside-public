export type ApiRequestOptions = Omit<RequestInit, "body"> & {
  body?: BodyInit | Record<string, unknown> | null;
  bearerToken?: string;
  timeoutMs?: number;
};

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly payload: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const unsafeMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export function csrfToken(cookie = document.cookie): string {
  return (
    cookie
      .split("; ")
      .find((part) => part.startsWith("hank_csrf="))
      ?.split("=")[1] ?? ""
  );
}

function isPlainObjectBody(body: ApiRequestOptions["body"]): body is Record<string, unknown> {
  if (!body || typeof body !== "object") return false;
  if (body instanceof Blob) return false;
  if (body instanceof FormData) return false;
  if (body instanceof URLSearchParams) return false;
  if (body instanceof ArrayBuffer) return false;
  if (typeof ReadableStream !== "undefined" && body instanceof ReadableStream) return false;
  return true;
}

function responseMessage(payload: unknown, fallback: string): { code: string; message: string } {
  if (typeof payload === "string") {
    return { code: "request_failed", message: payload || fallback };
  }
  if (payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    const error = record.error;
    if (error && typeof error === "object") {
      const errorRecord = error as Record<string, unknown>;
      return {
        code: String(errorRecord.code || "request_failed"),
        message: String(errorRecord.message || record.message || fallback),
      };
    }
    return {
      code: String(record.error || "request_failed"),
      message: String(record.message || record.error || fallback),
    };
  }
  return { code: "request_failed", message: fallback };
}

export class ApiClient {
  constructor(private readonly basePath = "") {}

  async request<T>(path: string, options: ApiRequestOptions = {}): Promise<T> {
    const headers = new Headers(options.headers);
    const method = String(options.method || "GET").toUpperCase();
    const controller = new AbortController();
    let timeoutID: number | undefined;

    if (options.signal) {
      if (options.signal.aborted) controller.abort(options.signal.reason);
      options.signal.addEventListener("abort", () => controller.abort(options.signal?.reason), { once: true });
    }
    if (options.timeoutMs && options.timeoutMs > 0) {
      timeoutID = window.setTimeout(() => controller.abort(new Error("request timeout")), options.timeoutMs);
    }

    if (options.bearerToken) {
      headers.set("Authorization", `Bearer ${options.bearerToken}`);
    }
    const csrf = csrfToken();
    if (!options.bearerToken && unsafeMethods.has(method) && csrf && !headers.has("X-Hank-CSRF-Token")) {
      headers.set("X-Hank-CSRF-Token", decodeURIComponent(csrf));
    }

    let body = options.body;
    if (isPlainObjectBody(body)) {
      body = JSON.stringify(body);
      if (!headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    } else if (typeof body === "string" && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }

    try {
      const response = await fetch(`${this.basePath}${path}`, {
        ...options,
        method,
        body: body as BodyInit | null | undefined,
        credentials: "same-origin",
        headers,
        signal: controller.signal,
      });
      const contentType = response.headers.get("Content-Type") || "";
      const payload = contentType.includes("application/json") ? await response.json() : await response.text();
      if (!response.ok) {
        const normalized = responseMessage(payload, response.statusText);
        throw new ApiError(response.status, normalized.code, normalized.message, payload);
      }
      return payload as T;
    } finally {
      if (timeoutID !== undefined) window.clearTimeout(timeoutID);
    }
  }
}

export type ApiTransport = Pick<ApiClient, "request">;

export const apiClient = new ApiClient();
