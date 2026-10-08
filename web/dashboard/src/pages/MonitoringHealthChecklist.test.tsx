import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { monitoringClient } from "../api/monitoring";
import { MonitoringHealthChecklist } from "./MonitoringHealthChecklist";
vi.mock("../api/monitoring", () => ({ monitoringClient: { health: vi.fn() } }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
describe("Monitoring setup checklist", () => {
  it("keeps failed collection visible and links to configuration", async () => {
    vi.mocked(monitoringClient.health).mockResolvedValue({ ready: false, checked_at: "", checks: [] });
    render(<MonitoringHealthChecklist compact />);
    await screen.findByText("Needs attention");
    expect(screen.getByRole("link", { name: "Configure server alerts" })).toHaveAttribute("href", "/dashboard/settings/notifications#monitoring-delivery-title");
    expect(screen.queryByText("Checks passing")).not.toBeInTheDocument();
  });
  it("does not claim success when unavailable and can recheck", async () => {
    vi.mocked(monitoringClient.health).mockRejectedValueOnce(new Error("unavailable"));
    render(<MonitoringHealthChecklist />);
    await screen.findByText("Could not verify");
    vi.mocked(monitoringClient.health).mockResolvedValue({ ready: true, checked_at: "", checks: [{ id: "rules", label: "Alert rules evaluating", ready: true }] });
    fireEvent.click(screen.getByRole("button", { name: "Recheck monitoring" }));
    await screen.findByText("Checks passing");
    expect(screen.getByText(/passing service checks do not confirm delivery/)).toBeInTheDocument();
  });
});
