import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AgentFleetMap } from "./AgentFleetMap";
import type { HomeAgentEntry } from "../api/agents";

afterEach(cleanup);
const agents: HomeAgentEntry[] = [
  { agent_id: "linux/a", name: "Build host", status: "online", agent_type: "primary", metrics: { cpu_load_1m: 0, memory_used_bytes: 0, memory_total_bytes: 100, disk_used_bytes: 50, disk_total_bytes: 100 } },
  { agent_id: "linux_b", name: "Test host", status: "offline", agent_type: "worker", metrics: { cpu_load_1m: 9 } },
];
describe("AgentFleetMap", () => {
  it("selects a node, preserves zero metrics, and links to the exact device", () => {
    render(<AgentFleetMap agents={agents} />);
    expect(screen.getByText("HankServerside")).toBeVisible();
    expect(screen.getByText(/1 online · 1 offline/)).toBeVisible();
    const node = screen.getByRole("button", { name: "Build host Online · Primary" });
    fireEvent.click(node);
    expect(node).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("0.00")).toBeVisible();
    expect(screen.getByText("0%")).toBeVisible();
    expect(screen.getByRole("link", { name: "Open device workspace" })).toHaveAttribute("href", "/dashboard/agents/linux%2Fa");
  });
  it("does not present offline metrics as live and clears a removed selection", () => {
    const view = render(<AgentFleetMap agents={agents} />);
    fireEvent.click(screen.getByRole("button", { name: "Test host Offline · Worker" }));
    expect(screen.getByText("Offline · no active connection")).toBeVisible();
    expect(screen.queryByText("9.00")).not.toBeInTheDocument();
    view.rerender(<AgentFleetMap agents={[agents[0]]} />);
    expect(screen.getByRole("heading", { name: "Select a device" })).toBeVisible();
  });
  it("updates the selected device when health data refreshes", () => {
    const view = render(<AgentFleetMap agents={agents} />);
    fireEvent.click(screen.getByRole("button", { name: /Build host/ }));
    view.rerender(<AgentFleetMap agents={[{ ...agents[0], metrics: { cpu_load_1m: 1.25 } }]} />);
    expect(screen.getByText("1.25")).toBeVisible();
    expect(screen.queryByText("0.00")).not.toBeInTheDocument();
  });
});
