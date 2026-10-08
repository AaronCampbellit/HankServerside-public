import { describe, expect, it } from "vitest";
import { applyCardResult, applyDeleteResult, applySnapshot, initialKanbanState, visibleColumns } from "./kanban-state";
import type { KanbanSnapshot } from "./types";

const snapshot = (version: string): KanbanSnapshot => ({
  state_version: version,
  boards: [{ board_id: "work", title: "Work", default: true, revision: version, total_card_count: 2, active_card_count: 1 }],
  permissions: { can_read: true, can_write: true, can_delete: true },
  selected_board: {
    board_id: "work", title: "Work", revision: version, intake_column_id: "inbox",
    columns: [
      { column_id: "inbox", title: "Inbox", role: "planning", cards: [{ board_id: "work", board_title: "Work", board_revision: version, column_id: "inbox", column_title: "Inbox", card_id: "one", title: "Research sync", details_markdown: "Offline requirements", tags: ["Hank"] }] },
      { column_id: "done", title: "Done", role: "complete", cards: [{ board_id: "work", board_title: "Work", board_revision: version, column_id: "done", column_title: "Done", card_id: "two", title: "Ship notes", details_markdown: "Released", tags: [] }] },
    ],
  },
});

describe("applyCardResult", () => {
  it("moves the authoritative card to its exact destination and updates revisions", () => {
    const currentSnapshot = snapshot("rev-1");
    const state = applySnapshot(initialKanbanState(), currentSnapshot, 1);
    const original = currentSnapshot.selected_board!.columns[0].cards[0];
    const next = applyCardResult(state, { card: { ...original, column_id: "done", column_title: "Done", board_revision: "rev-2" }, state_version: "rev-2" }, 2, 0);

    expect(next.snapshot?.selected_board?.columns[0].cards).toHaveLength(0);
    expect(next.snapshot?.selected_board?.columns[1].cards[0].card_id).toBe(original.card_id);
    expect(next.snapshot?.selected_board?.revision).toBe("rev-2");
    expect(next.snapshot?.state_version).toBe("rev-2");
  });
});

describe("applyDeleteResult", () => {
  it("ignores stale deletion results and removes an accepted exact card", () => {
    const state = applySnapshot(initialKanbanState(), snapshot("rev-1"), 4);
    const result = { board_id: "work", card_id: "one", board_revision: "rev-2", state_version: "rev-2", deleted: true };
    expect(applyDeleteResult(state, result, 3)).toBe(state);
    const next = applyDeleteResult({ ...state, selectedCardID: "one" }, result, 5);
    expect(next.snapshot?.selected_board?.columns[0].cards).toHaveLength(0);
    expect(next.selectedCardID).toBe("");
    expect(next.snapshot?.boards[0].total_card_count).toBe(1);
  });
});

describe("kanban state", () => {
  it("ignores a result from an older request sequence", () => {
    const current = applySnapshot(initialKanbanState(), snapshot("new"), 4);
    const stale = applySnapshot(current, snapshot("old"), 3);

    expect(stale.snapshot?.state_version).toBe("new");
    expect(stale.lastSequence).toBe(4);
  });

  it("preserves valid selection and clears a removed card", () => {
    const selected = { ...applySnapshot(initialKanbanState(), snapshot("1"), 1), selectedColumnID: "inbox", selectedCardID: "one" };
    expect(applySnapshot(selected, snapshot("2"), 2).selectedCardID).toBe("one");
    const withoutCard = snapshot("3");
    withoutCard.selected_board!.columns[0].cards = [];
    expect(applySnapshot(selected, withoutCard, 3).selectedCardID).toBe("");
  });

  it("filters complete cards and searches title, details, and tags", () => {
    const board = snapshot("1").selected_board!;
    expect(visibleColumns(board, "", false).map((column) => column.column_id)).toEqual(["inbox"]);
    expect(visibleColumns(board, "ship", true)[1].cards.map((card) => card.card_id)).toEqual(["two"]);
    expect(visibleColumns(board, "offline", true)[0].cards.map((card) => card.card_id)).toEqual(["one"]);
    expect(visibleColumns(board, "hank", true)[0].cards.map((card) => card.card_id)).toEqual(["one"]);
  });
});
