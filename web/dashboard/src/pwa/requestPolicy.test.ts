import { describe, expect, it } from "vitest";
import { requestPolicyFor } from "./requestPolicy";

const origin = "https://hank.example";

function navigationRequest(path: string, requestOrigin = origin): Request {
  const request = new Request(`${requestOrigin}${path}`);
  Object.defineProperty(request, "mode", { value: "navigate" });
  return request;
}

describe("requestPolicyFor", () => {
  it("uses fallback handling for same-origin GET document navigation", () => {
    expect(requestPolicyFor(navigationRequest("/dashboard/profile-notes"), origin)).toBe("dashboard-navigation");
  });

  it("uses the static offline page for public document navigation", () => {
    expect(requestPolicyFor(navigationRequest("/login"), origin)).toBe("public-navigation");
  });

  it.each([
    ["API GET", new Request(`${origin}/v1/me`)],
    ["WebSocket ticket", new Request(`${origin}/ws/app-ticket`)],
    ["download GET", new Request(`${origin}/v1/home/files/downloads?id=1`)],
    ["mutation", new Request(`${origin}/dashboard/profile-notes`, { method: "POST" })],
    ["cross-origin navigation", navigationRequest("/", "https://example.com")],
  ])("keeps %s network-only", (_name, request) => {
    expect(requestPolicyFor(request, origin)).toBe("network-only");
  });
});
