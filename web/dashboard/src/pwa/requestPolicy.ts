export type PWARequestPolicy = "dashboard-navigation" | "public-navigation" | "network-only";

export function requestPolicyFor(
  request: Request,
  applicationOrigin = self.location.origin,
): PWARequestPolicy {
  const url = new URL(request.url);
  if (request.method !== "GET" || request.mode !== "navigate" || url.origin !== applicationOrigin) {
    return "network-only";
  }
  return url.pathname === "/dashboard" || url.pathname.startsWith("/dashboard/")
    ? "dashboard-navigation"
    : "public-navigation";
}
