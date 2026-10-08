import {render, screen} from "@testing-library/react";
import {describe, expect, it, vi} from "vitest";
import {DesktopSessionHistory} from "./DesktopSessionHistory";

describe("DesktopSessionHistory", () => {
  it("renders stable reason guidance without content", async () => {
    const client = {events: vi.fn().mockResolvedValue({events: [{session_id: "desk", sequence: 1, event_type: "relay.closed", actor_type: "server", occurred_at: "2026-01-01T00:00:00Z", severity: "warning", reason_code: "slow_consumer", metadata: {epoch: "2"}}], next_after_sequence: 1})};
    render(<DesktopSessionHistory sessionID="desk" client={client as never} />);
    expect(await screen.findByText(/network or decoder could not keep up/i)).toBeInTheDocument();
    expect(screen.queryByText(/clipboard|ciphertext|frame payload/i)).not.toBeInTheDocument();
  });

  it("renders paginated terminal aggregates without content fields", async () => {
    const client = {
      events: vi.fn().mockResolvedValue({events: [], next_after_sequence: 0}),
      history: vi.fn().mockResolvedValue({sessions: [{session_id: "desk-old", agent_id: "agent", operator_user_id: "user", state: "terminated", termination_reason: "user_ended", platform: "windows", requested_at: "2026-01-01T00:00:00Z", active_at: "2026-01-01T00:00:01Z", terminated_at: "2026-01-01T00:01:01Z", duration_ms: 60000, epoch_count: 3, browser_to_agent_bytes: 100, agent_to_browser_bytes: 200, permissions: ["desktop.view"]}], next_after: 25}),
    };
    render(<DesktopSessionHistory sessionID="desk" client={client as never} />);
    expect(await screen.findByText(/windows · terminated/i)).toBeInTheDocument();
    expect(screen.getByText(/60s · epoch 3 · 300 encrypted bytes/i)).toBeInTheDocument();
    expect(screen.queryByText(/clipboard|ciphertext|frame payload/i)).not.toBeInTheDocument();
  });

  it("contains history fetch failures without an unhandled rejection", async () => {
    const client = {events: vi.fn().mockRejectedValue(new Error("offline"))};
    render(<DesktopSessionHistory sessionID="desk" client={client as never} />);
    expect(await screen.findByText(/temporarily unavailable/i)).toBeInTheDocument();
  });
});
