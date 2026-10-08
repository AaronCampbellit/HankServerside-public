import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { KanbanBridge } from "./bridge";
import { KanbanApp } from "./KanbanApp";
import type { KanbanSnapshot, ToolResult } from "./types";

function testSnapshot(): KanbanSnapshot {
  return {
    state_version: "rev-1",
    boards: [
      { board_id: "work", title: "Work", default: true, revision: "rev-1", total_card_count: 2, active_card_count: 1 },
      { board_id: "home", title: "Home", default: false, revision: "rev-home", total_card_count: 0, active_card_count: 0 },
    ],
    permissions: { can_read: true, can_write: true, can_delete: true },
    selected_board: {
      board_id: "work", title: "Work", revision: "rev-1", intake_column_id: "inbox",
      columns: [
        { column_id: "inbox", title: "Inbox", role: "planning", cards: [{ board_id: "work", board_title: "Work", board_revision: "rev-1", column_id: "inbox", column_title: "Inbox", card_id: "research", title: "Research sync", details_markdown: "Capture offline requirements", due_date: "2026-08-20", tags: ["Hank"] }] },
        { column_id: "done", title: "Done", role: "complete", cards: [{ board_id: "work", board_title: "Work", board_revision: "rev-1", column_id: "done", column_title: "Done", card_id: "shipped", title: "Ship notes", details_markdown: "Released", tags: [] }] },
      ],
    },
  };
}

function fakeBridge() {
  let listener: ((result: ToolResult) => void) | undefined;
  const callTool = vi.fn<(name: string, args?: Record<string, unknown>) => Promise<ToolResult>>();
  const bridge: KanbanBridge = {
    subscribe(next) { listener = next; return () => { listener = undefined; }; },
    async callTool<T>(name: string, args?: Record<string, unknown>) { return await callTool(name, args) as ToolResult<T>; },
  };
  return { bridge, callTool, emit(result: ToolResult) { act(() => listener?.(result)); } };
}

describe("KanbanApp browsing", () => {
  afterEach(cleanup);
  beforeEach(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({ matches: false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
    });
  });

  it("renders board controls, active columns, and searchable cards", () => {
    const host = fakeBridge();
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: testSnapshot() });

    expect(screen.getByRole("heading", { name: "Work" })).toBeInTheDocument();
    expect(screen.getByLabelText("Board")).toHaveValue("work");
    expect(screen.getByText("Default board")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Inbox/ })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Done/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Research sync/ })).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Show completed cards"));
    expect(screen.getByRole("button", { name: /Ship notes/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search cards"), { target: { value: "offline" } });
    expect(screen.getByRole("button", { name: /Research sync/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ship notes/ })).not.toBeInTheDocument();
  });

  it("switches boards through open_kanban and keeps the last good board on failure", async () => {
    const host = fakeBridge();
    host.callTool
      .mockResolvedValueOnce({ structuredContent: { ...testSnapshot(), state_version: "rev-home", selected_board: { board_id: "home", title: "Home", revision: "rev-home", columns: [] } } })
      .mockRejectedValueOnce(new Error("offline"));
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: testSnapshot() });

    fireEvent.change(screen.getByLabelText("Board"), { target: { value: "home" } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Home" })).toBeInTheDocument());
    expect(host.callTool).toHaveBeenCalledWith("open_kanban", { board_id: "home" });

    fireEvent.click(screen.getByRole("button", { name: "Refresh board" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("offline"));
    expect(screen.getByRole("heading", { name: "Home" })).toBeInTheDocument();
  });

  it("uses a single-column selector in narrow mode", () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({ matches: query.includes("720"), media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() } as unknown as MediaQueryList));
    const host = fakeBridge();
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: testSnapshot() });
    fireEvent.click(screen.getByLabelText("Show completed cards"));

    const selector = screen.getByLabelText("Column");
    expect(selector).toBeInTheDocument();
    expect(within(selector).getByRole("option", { name: "Inbox (1)" })).toBeInTheDocument();
    fireEvent.change(selector, { target: { value: "done" } });
    expect(screen.getByRole("button", { name: /Ship notes/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Research sync/ })).not.toBeInTheDocument();
  });

  it("creates a card through the dedicated tool and refreshes the board", async () => {
    const host = fakeBridge();
    const current = testSnapshot();
    const created = { ...current.selected_board!.columns[0].cards[0], card_id: "created", title: "New card", board_revision: "rev-2" };
    const refreshed = testSnapshot();
    refreshed.state_version = "rev-2";
    refreshed.selected_board!.revision = "rev-2";
    refreshed.selected_board!.columns[0].cards.push(created);
    host.callTool.mockImplementation(async (name) => name === "create_kanban_card"
      ? { structuredContent: { card: created, state_version: "rev-2" } }
      : { structuredContent: refreshed });
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: current });

    fireEvent.change(screen.getByLabelText("Add card to Inbox"), { target: { value: "New card" } });
    fireEvent.click(screen.getByRole("button", { name: "Add to Inbox" }));

    await waitFor(() => expect(host.callTool).toHaveBeenCalledWith("create_kanban_card", { board_id: "work", column_id: "inbox", title: "New card" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /New card/ })).toBeInTheDocument());
    expect(host.callTool).toHaveBeenCalledWith("open_kanban", { board_id: "work" });
  });

  it("opens a card dialog and performs an exact move", async () => {
    const host = fakeBridge();
    const current = testSnapshot();
    const moved = { ...current.selected_board!.columns[0].cards[0], column_id: "done", column_title: "Done", board_revision: "rev-2" };
    host.callTool.mockImplementation(async (name) => name === "move_kanban_card"
      ? { structuredContent: { card: moved, state_version: "rev-2" } }
      : { structuredContent: { ...current, state_version: "rev-2" } });
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: current });

    fireEvent.click(screen.getByRole("button", { name: /Research sync/ }));
    fireEvent.change(screen.getByLabelText("Move to column"), { target: { value: "done" } });
    fireEvent.click(screen.getByRole("button", { name: "Move card" }));

    await waitFor(() => expect(host.callTool).toHaveBeenCalledWith("move_kanban_card", { board_id: "work", card_id: "research", target_column_id: "done" }));
  });

  it("disables write affordances without notes write permission", () => {
    const host = fakeBridge();
    const readOnly = testSnapshot();
    readOnly.permissions.can_write = false;
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: readOnly });

    expect(screen.getByLabelText("Add card to Inbox")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Add to Inbox" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Research sync/ })).toHaveAttribute("draggable", "false");
  });

  it("reorders by exact zero-based index in wide mode", async () => {
    const host = fakeBridge();
    const current = testSnapshot();
    host.callTool.mockImplementation(async (name) => name === "move_kanban_card"
      ? { structuredContent: { card: current.selected_board!.columns[0].cards[0], state_version: "rev-2" } }
      : { structuredContent: current });
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: current });
    fireEvent.click(screen.getByLabelText("Show completed cards"));

    const card = screen.getByRole("button", { name: /Research sync/ });
    fireEvent.dragStart(card);
    fireEvent.dragOver(screen.getByTestId("drop-done-0"));
    fireEvent.drop(screen.getByTestId("drop-done-0"));

    await waitFor(() => expect(host.callTool).toHaveBeenCalledWith("move_kanban_card", {
      board_id: "work", card_id: "research", target_column_id: "done", target_index: 0,
    }));
  });

  it("deletes only after local confirmation with exact IDs and revision", async () => {
    const host = fakeBridge();
    const current = testSnapshot();
    const refreshed = testSnapshot();
    refreshed.selected_board!.columns[0].cards = [];
    host.callTool.mockImplementation(async (name) => name === "delete_kanban_card"
      ? { structuredContent: { board_id: "work", card_id: "research", board_revision: "rev-2", state_version: "rev-2", deleted: true } }
      : { structuredContent: refreshed });
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: current });

    fireEvent.click(screen.getByRole("button", { name: /Research sync/ }));
    fireEvent.click(screen.getByRole("button", { name: "Delete card" }));
    expect(host.callTool).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Confirm delete Research sync" }));

    await waitFor(() => expect(host.callTool).toHaveBeenCalledWith("delete_kanban_card", {
      board_id: "work", card_id: "research", expected_board_revision: "rev-1", confirmed: true,
    }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Research sync/ })).not.toBeInTheDocument());
  });

  it("retains the card after a deletion failure and offers reload", async () => {
    const host = fakeBridge();
    host.callTool.mockRejectedValueOnce(new Error("revision conflict"));
    render(<KanbanApp bridge={host.bridge} />);
    host.emit({ structuredContent: testSnapshot() });

    fireEvent.click(screen.getByRole("button", { name: /Research sync/ }));
    fireEvent.click(screen.getByRole("button", { name: "Delete card" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm delete Research sync" }));

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("revision conflict"));
    expect(screen.getByDisplayValue("Research sync")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload board" })).toBeInTheDocument();
  });
});
