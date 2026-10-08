import {render, screen} from "@testing-library/react";
import {describe, expect, it} from "vitest";
import {DesktopReadinessCard, desktopReadinessComplete} from "./DesktopReadinessCard";

describe("DesktopReadinessCard", () => {
  it("turns authoritative readiness into a setup walkthrough without inferring trust from capabilities", () => {
    const readiness = {
      agent_id: "agent",
      online: true,
      platform: "macos",
      trusted: false,
      capabilities: ["desktop.session.open", "desktop.view"],
      active_session: false,
      reported_at: null,
      checks: {capture: "required", control: "ready", daemon: "ready", host: "ready", service: "ready", indicator: "ready"},
    };
    render(<DesktopReadinessCard readiness={readiness} />);
    expect(screen.getByText(/Approve this device/)).toBeInTheDocument();
    expect(screen.getByText(/Needs local permission.*Allow screen recording locally/i)).toBeInTheDocument();
    expect(screen.getByText(/Use the Remote Desktop setup below/i)).toBeInTheDocument();
    expect(screen.getByRole("link", {name: "Set up Remote Desktop"})).toHaveClass("secondary");
    expect(desktopReadinessComplete(readiness)).toBe(false);
  });

  it("only permits launch after every endpoint prerequisite is ready", () => {
    const readiness = {agent_id: "agent", online: true, platform: "macos", trusted: true, capabilities: [], active_session: false, reported_at: "2026-07-24T12:00:00Z", checks: {service: "ready", daemon: "ready", host: "ready", indicator: "ready", capture: "ready", control: "ready"}};
    expect(desktopReadinessComplete(readiness)).toBe(true);
    expect(desktopReadinessComplete({...readiness, active_session: true})).toBe(false);
    expect(desktopReadinessComplete({...readiness, checks: {...readiness.checks, indicator: "unavailable"}})).toBe(false);
  });
});
