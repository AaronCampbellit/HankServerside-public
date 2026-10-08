import { describe, expect, it } from "vitest";
import { agentWorkspaceForPath, appRoutes, internalNavigationTarget, navItemsForRoutes, routeCachePath, routeForPath } from "./router";

describe("router", () => {
  it("resolves known routes and returns a not-found route for unknown paths", () => {
    expect(routeForPath("/dashboard/settings").heading).toBe("Settings");
    expect(routeForPath("/missing").heading).toBe("Not Found");
    expect(routeForPath("/missing").publicRoute).toBeUndefined();
  });

  it("resolves device workspace tabs and keeps them in one cached Agents page", () => {
    expect(agentWorkspaceForPath("/dashboard/agents/mac-studio")).toEqual({ agentID: "mac-studio", tab: "overview" });
    expect(agentWorkspaceForPath("/dashboard/agents/mac%20studio/desktop")).toEqual({ agentID: "mac studio", tab: "desktop" });
    expect(agentWorkspaceForPath("/dashboard/agents/mac-studio/terminal")).toEqual({ agentID: "mac-studio", tab: "terminal" });
    expect(agentWorkspaceForPath("/dashboard/agents/mac-studio/security")).toEqual({ agentID: "mac-studio", tab: "security" });
    expect(agentWorkspaceForPath("/dashboard/agents/mac-studio/unknown")).toBeNull();
    expect(routeForPath("/dashboard/agents/mac-studio").heading).toBe("Device Overview");
    expect(routeForPath("/dashboard/agents/mac-studio/desktop").adminOnly).toBe(true);
    expect(routeCachePath(routeForPath("/dashboard/agents/mac-studio/security"))).toBe("/dashboard/agents");
  });

  it("derives shell navigation from route metadata", () => {
    const navItems = navItemsForRoutes(appRoutes);

    expect(navItems.some((item) => item.href === "/dashboard/settings/apps" && item.adminOnly)).toBe(true);
    expect(navItems.some((item) => item.href === "/dashboard/settings/attachments" && item.adminOnly)).toBe(true);
    expect(navItems.some((item) => item.href === "/join")).toBe(false);
  });

  it("intercepts same-origin app links and leaves non-app links alone", () => {
    expect(internalNavigationTarget("/dashboard/settings", "https://hank.local")).toBe("/dashboard/settings");
    expect(internalNavigationTarget("https://hank.local/dashboard?tab=home#top", "https://hank.local")).toBe("/dashboard?tab=home#top");
    expect(internalNavigationTarget("https://example.com/dashboard", "https://hank.local")).toBeNull();
    expect(internalNavigationTarget("mailto:hank@example.com", "https://hank.local")).toBeNull();
  });
});
