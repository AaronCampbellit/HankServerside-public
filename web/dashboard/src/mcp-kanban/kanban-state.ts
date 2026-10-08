import type { KanbanBoard, KanbanCardResult, KanbanColumn, KanbanDeleteResult, KanbanSnapshot } from "./types";

export type KanbanViewState = {
  snapshot: KanbanSnapshot | null;
  selectedColumnID: string;
  selectedCardID: string;
  lastSequence: number;
};

export function initialKanbanState(): KanbanViewState {
  return { snapshot: null, selectedColumnID: "", selectedCardID: "", lastSequence: 0 };
}

export function applySnapshot(state: KanbanViewState, snapshot: KanbanSnapshot, sequence: number): KanbanViewState {
  if (sequence < state.lastSequence) return state;
  const board = snapshot.selected_board;
  const selectedColumnExists = board?.columns.some((column) => column.column_id === state.selectedColumnID) ?? false;
  const selectedColumnID = selectedColumnExists
    ? state.selectedColumnID
    : board?.columns.find((column) => column.column_id === board.intake_column_id)?.column_id || board?.columns[0]?.column_id || "";
  const selectedCardExists = board?.columns.some((column) => column.cards.some((card) => card.card_id === state.selectedCardID)) ?? false;
  return {
    snapshot,
    selectedColumnID,
    selectedCardID: selectedCardExists ? state.selectedCardID : "",
    lastSequence: sequence,
  };
}

export function applyCardResult(state: KanbanViewState, result: KanbanCardResult, sequence: number, targetIndex?: number): KanbanViewState {
  if (sequence < state.lastSequence || !state.snapshot?.selected_board || state.snapshot.selected_board.board_id !== result.card.board_id) return state;
  const board = state.snapshot.selected_board;
  const columns = board.columns.map((column) => ({ ...column, cards: column.cards.filter((card) => card.card_id !== result.card.card_id) }));
  const destination = columns.find((column) => column.column_id === result.card.column_id);
  if (!destination) return state;
  const insertAt = targetIndex === undefined ? destination.cards.length : Math.max(0, Math.min(targetIndex, destination.cards.length));
  destination.cards.splice(insertAt, 0, result.card);
  const snapshot = {
    ...state.snapshot,
    state_version: result.state_version,
    selected_board: { ...board, revision: result.card.board_revision, columns },
    boards: state.snapshot.boards.map((summary) => summary.board_id === board.board_id ? { ...summary, revision: result.card.board_revision } : summary),
  };
  return { ...state, snapshot, lastSequence: sequence };
}

export function applyDeleteResult(state: KanbanViewState, result: KanbanDeleteResult, sequence: number): KanbanViewState {
  if (sequence < state.lastSequence || !result.deleted || !state.snapshot?.selected_board || state.snapshot.selected_board.board_id !== result.board_id) return state;
  const board = state.snapshot.selected_board;
  const columns = board.columns.map((column) => ({ ...column, cards: column.cards.filter((card) => card.card_id !== result.card_id) }));
  const summaries = state.snapshot.boards.map((summary) => summary.board_id === result.board_id ? {
    ...summary,
    revision: result.board_revision,
    total_card_count: Math.max(0, summary.total_card_count - 1),
    active_card_count: Math.max(0, summary.active_card_count - (board.columns.find((column) => column.cards.some((card) => card.card_id === result.card_id))?.role === "complete" ? 0 : 1)),
  } : summary);
  return {
    ...state,
    snapshot: { ...state.snapshot, state_version: result.state_version, boards: summaries, selected_board: { ...board, revision: result.board_revision, columns } },
    selectedCardID: state.selectedCardID === result.card_id ? "" : state.selectedCardID,
    lastSequence: sequence,
  };
}

export function visibleColumns(board: KanbanBoard, query: string, includeComplete: boolean): KanbanColumn[] {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  return board.columns
    .filter((column) => includeComplete || column.role !== "complete")
    .map((column) => ({
      ...column,
      cards: normalizedQuery
        ? column.cards.filter((card) => [card.title, card.details_markdown, ...card.tags].join("\n").toLocaleLowerCase().includes(normalizedQuery))
        : column.cards,
    }));
}
