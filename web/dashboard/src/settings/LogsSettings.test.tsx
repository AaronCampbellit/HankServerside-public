import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LogsSettings } from "./LogsSettings";

describe("LogsSettings", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.history.replaceState({}, "", "/");
  });

  it("loads and expands the exact event linked by a notification", async () => {
    window.history.replaceState({}, "", "/dashboard/notifications/event?notification=ntf_1");
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      event: {
        id: "audit_1",
        event_type: "storage.backup.failed",
        severity: "warning",
        target_type: "storage",
        target_id: "storage_1",
        occurred_at: "2026-08-19T18:40:51Z",
        metadata: { backup_label: "nightly" },
      },
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    render(<LogsSettings />);

    expect(await screen.findByRole("heading", { name: "Log event" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/v1/me/notifications/ntf_1/event", expect.anything());
    expect(screen.getByText("storage.backup.failed")).toBeInTheDocument();
    expect(screen.getByText("Details").closest("details")).toHaveAttribute("open");
    expect(screen.queryByLabelText("Audit logs")).not.toBeInTheDocument();
  });

  it("keeps audit metadata in a collapsed details disclosure", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      events: [{
        event_type: "agent.health.recovered",
        severity: "warning",
        target_type: "agent",
        target_id: "mac-c00c018b-ac3b-4ab9-a777-226cd734c9ca",
        actor_user_id: "usr_1",
        actor_label: "Alex Campbell",
        target_label: "Office Mac",
        request_id: "req_1",
        helper_text: "Warning event. Review metadata and related connector or policy state.",
        occurred_at: "2026-08-19T18:40:51Z",
        metadata: { recovered_after_seconds: 120 },
      }],
    }), { headers: { "Content-Type": "application/json" } })));

    render(<LogsSettings />);

    expect(await screen.findByText("agent.health.recovered")).toBeInTheDocument();
    expect(screen.getByText("Warning event. Review metadata and related connector or policy state.")).toBeInTheDocument();

    const summary = screen.getByText("Details");
    const disclosure = summary.closest("details");
    expect(disclosure).not.toBeNull();
    expect(disclosure).not.toHaveAttribute("open");
    expect(within(disclosure!).getByText("Actor")).toBeInTheDocument();
    expect(within(disclosure!).getByText("Alex Campbell")).toBeInTheDocument();
    expect(within(disclosure!).getByText("Office Mac")).toBeInTheDocument();
    expect(within(disclosure!).getByText("recovered_after_seconds")).toBeInTheDocument();

    fireEvent.click(summary);
    expect(disclosure).toHaveAttribute("open");
  });

  it("keeps audit metadata expanded on wider screens", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false })));
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      events: [{
        event_type: "login.succeeded",
        severity: "info",
        target_type: "session",
        actor_user_id: "usr_1",
        occurred_at: "2026-08-19T18:40:51Z",
      }],
    }), { headers: { "Content-Type": "application/json" } })));

    render(<LogsSettings />);

    expect(await screen.findByText("login.succeeded")).toBeInTheDocument();
    expect(screen.getByText("Details").closest("details")).toHaveAttribute("open");
  });

  it("queries the complete storage event category", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false })));
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ events: [] }), {
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    render(<LogsSettings />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const eventFilter = screen.getByLabelText("Event");
    const storageOption = within(eventFilter).getByRole("option", { name: "Storage" }) as HTMLOptionElement;
    fireEvent.change(eventFilter, { target: { value: storageOption.value } });
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/v1/home/audit-events?target_type=storage&sort=occurred_at&order=desc&limit=100");
  });
});
