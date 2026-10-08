import { useEffect, useRef, useState } from "react";
import type { KanbanBridge } from "./bridge";
import { CardDialog, type CardUpdateFields } from "./CardDialog";
import { applyCardResult, applyDeleteResult, applySnapshot, initialKanbanState, visibleColumns } from "./kanban-state";
import type { KanbanCard, KanbanCardResult, KanbanDeleteResult, KanbanSnapshot } from "./types";
import "./kanban-app.css";

function useNarrowLayout() {
  const [narrow, setNarrow] = useState(() => window.matchMedia?.("(max-width: 720px)").matches ?? false);
  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 720px)");
    if (!media) return;
    const update = () => setNarrow(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return narrow;
}

export function KanbanApp({ bridge }: { bridge: KanbanBridge }) {
  const [state, setState] = useState(initialKanbanState);
  const [query, setQuery] = useState("");
  const [includeComplete, setIncludeComplete] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [announcement, setAnnouncement] = useState("");
  const sequenceRef = useRef(0);
  const mutationsRef = useRef(new Set<string>());
  const draggedCardRef = useRef<KanbanCard | null>(null);
  const cardButtonRefs = useRef(new Map<string, HTMLButtonElement>());
  const columnHeadingRefs = useRef(new Map<string, HTMLHeadingElement>());
  const narrow = useNarrowLayout();

  useEffect(() => bridge.subscribe((result) => {
    if (result.structuredContent && "boards" in result.structuredContent) {
      const sequence = ++sequenceRef.current;
      setState((current) => applySnapshot(current, result.structuredContent as unknown as KanbanSnapshot, sequence));
    }
  }), [bridge]);

  async function loadBoard(boardID: string) {
    const sequence = ++sequenceRef.current;
    setPending(true);
    setError("");
    try {
      const result = await bridge.callTool<KanbanSnapshot>("open_kanban", boardID ? { board_id: boardID } : {});
      if (result.isError || !result.structuredContent) throw new Error(result.content?.map((item) => item.text || "").join(" ") || "Hank returned no board data.");
      setState((current) => applySnapshot(current, result.structuredContent!, sequence));
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : String(loadError));
    } finally {
      setPending(false);
    }
  }

  async function mutateCard(tool: "create_kanban_card" | "update_kanban_card" | "append_kanban_worklog" | "move_kanban_card", args: Record<string, unknown>, successMessage: string, targetIndex?: number) {
    const mutationKey = typeof args.card_id === "string" ? args.card_id : `${args.column_id}:create`;
    if (mutationsRef.current.has(mutationKey)) return false;
    mutationsRef.current.add(mutationKey);
    const sequence = ++sequenceRef.current;
    setPending(true);
    setError("");
    try {
      const result = await bridge.callTool<KanbanCardResult>(tool, args);
      if (result.isError || !result.structuredContent?.card) throw new Error(result.content?.map((item) => item.text || "").join(" ") || "Hank did not return the updated card.");
      setState((current) => applyCardResult(current, result.structuredContent!, sequence, targetIndex));
      setAnnouncement(successMessage);
      await loadBoard(result.structuredContent.card.board_id);
      return true;
    } catch (mutationError) {
      setError(mutationError instanceof Error ? mutationError.message : String(mutationError));
      return false;
    } finally {
      mutationsRef.current.delete(mutationKey);
      setPending(false);
    }
  }

  async function deleteCard(card: KanbanCard) {
    if (mutationsRef.current.has(card.card_id)) return;
    mutationsRef.current.add(card.card_id);
    const sequence = ++sequenceRef.current;
    setPending(true);
    setError("");
    try {
      const result = await bridge.callTool<KanbanDeleteResult>("delete_kanban_card", {
        board_id: activeBoard.board_id,
        card_id: card.card_id,
        expected_board_revision: activeBoard.revision,
        confirmed: true,
      });
      if (result.isError || !result.structuredContent?.deleted) throw new Error(result.content?.map((item) => item.text || "").join(" ") || "Hank did not confirm the deletion.");
      setState((current) => applyDeleteResult(current, result.structuredContent!, sequence));
      setAnnouncement(`Deleted ${card.title}`);
      requestAnimationFrame(() => columnHeadingRefs.current.get(card.column_id)?.focus());
      await loadBoard(activeBoard.board_id);
    } catch (deleteError) {
      setError(deleteError instanceof Error ? deleteError.message : String(deleteError));
    } finally {
      mutationsRef.current.delete(card.card_id);
      setPending(false);
    }
  }

  const snapshot = state.snapshot;
  if (!snapshot) {
    return <main className="kanban-app kanban-centered" aria-live="polite">Waiting for Hank Kanban…</main>;
  }
  const board = snapshot.selected_board;
  if (!board) {
    return <main className="kanban-app"><h1>Hank Kanban</h1><p>No MCP-visible Kanban boards are available. Create one in Hank Notes first.</p></main>;
  }
  const activeBoard = board;
  const activeSnapshot = snapshot;
  const columns = visibleColumns(board, query, includeComplete);
  const displayedColumns = narrow ? columns.filter((column) => column.column_id === state.selectedColumnID) : columns;
  const selectedSummary = snapshot.boards.find((item) => item.board_id === board.board_id);
  const selectedCard = board.columns.flatMap((column) => column.cards).find((card) => card.card_id === state.selectedCardID);

  async function createCard(columnID: string) {
    const title = drafts[columnID]?.trim();
    if (!title) return;
    if (await mutateCard("create_kanban_card", { board_id: activeBoard.board_id, column_id: columnID, title }, `Created ${title}`)) {
      setDrafts((current) => ({ ...current, [columnID]: "" }));
    }
  }

  function cardArgs(card: KanbanCard) {
    return { board_id: activeBoard.board_id, card_id: card.card_id };
  }

  function dropCard(columnID: string, targetIndex: number) {
    const card = draggedCardRef.current;
    draggedCardRef.current = null;
    if (!card || !activeSnapshot.permissions.can_write) return;
    void mutateCard("move_kanban_card", { ...cardArgs(card), target_column_id: columnID, target_index: targetIndex }, `Moved ${card.title}`, targetIndex);
  }

  function closeCard(cardID: string) {
    setState((current) => ({ ...current, selectedCardID: "" }));
    requestAnimationFrame(() => cardButtonRefs.current.get(cardID)?.focus());
  }

  return <main className="kanban-app">
    <header className="kanban-header">
      <div>
        <div className="kanban-title-row"><h1>{board.title}</h1>{selectedSummary?.default ? <span className="kanban-badge">Default board</span> : null}</div>
        <p>{selectedSummary?.total_card_count ?? columns.reduce((sum, column) => sum + column.cards.length, 0)} cards</p>
      </div>
      <div className="kanban-controls">
        <label>Board<select aria-label="Board" value={board.board_id} disabled={pending} onChange={(event) => void loadBoard(event.target.value)}>{snapshot.boards.map((item) => <option key={item.board_id} value={item.board_id}>{item.title}</option>)}</select></label>
        <button type="button" disabled={pending} onClick={() => void loadBoard(board.board_id)} aria-label="Refresh board">{pending ? "Refreshing…" : "Refresh"}</button>
        <label className="kanban-search">Search<input aria-label="Search cards" type="search" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
        <label className="kanban-check"><input aria-label="Show completed cards" type="checkbox" checked={includeComplete} onChange={(event) => setIncludeComplete(event.target.checked)} />Show completed</label>
      </div>
    </header>
    {error ? <div role="alert" className="kanban-error"><span>{error}</span><button type="button" onClick={() => void loadBoard(board.board_id)}>Reload board</button></div> : null}
    <p className="kanban-sr-only" aria-live="polite">{announcement}</p>
    {narrow ? <label className="kanban-column-select">Column<select aria-label="Column" value={state.selectedColumnID} onChange={(event) => setState((current) => ({ ...current, selectedColumnID: event.target.value }))}>{columns.map((column) => <option key={column.column_id} value={column.column_id}>{column.title} ({column.cards.length})</option>)}</select></label> : null}
    <section className={`kanban-board ${narrow ? "is-narrow" : ""}`} aria-label={`${board.title} board`}>
      {displayedColumns.map((column) => <section className="kanban-column" key={column.column_id} aria-labelledby={`column-${column.column_id}`}>
        <header><h2 ref={(node) => { if (node) columnHeadingRefs.current.set(column.column_id, node); else columnHeadingRefs.current.delete(column.column_id); }} tabIndex={-1} id={`column-${column.column_id}`}>{column.title} <span>{column.cards.length}</span></h2>{column.role ? <small>{column.role}</small> : null}</header>
        <div className="kanban-card-list">
          {column.cards.map((card, cardIndex) => <div key={card.card_id}>
            {!narrow ? <div className="kanban-drop-target" data-testid={`drop-${column.column_id}-${cardIndex}`} onDragOver={(event) => event.preventDefault()} onDrop={() => dropCard(column.column_id, cardIndex)} /> : null}
            <button ref={(node) => { if (node) cardButtonRefs.current.set(card.card_id, node); else cardButtonRefs.current.delete(card.card_id); }} type="button" className="kanban-card" draggable={snapshot.permissions.can_write ? "true" : "false"} onDragStart={() => { draggedCardRef.current = card; }} onDragEnd={() => { draggedCardRef.current = null; }} onClick={() => setState((current) => ({ ...current, selectedCardID: card.card_id }))} aria-label={`${card.title}, ${column.title}`}>
            <strong>{card.title}</strong>
            {card.details_markdown ? <span>{card.details_markdown}</span> : null}
            <footer>{card.due_date ? <time dateTime={card.due_date}>Due {card.due_date}</time> : null}{card.tags.map((tag) => <em key={tag}>{tag}</em>)}</footer>
            </button>
          </div>)}
          {!narrow ? <div className="kanban-drop-target" data-testid={`drop-${column.column_id}-${column.cards.length}`} onDragOver={(event) => event.preventDefault()} onDrop={() => dropCard(column.column_id, column.cards.length)} /> : null}
          {column.cards.length === 0 ? <p className="kanban-empty">No matching cards</p> : null}
          <form className="kanban-quick-add" onSubmit={(event) => { event.preventDefault(); void createCard(column.column_id); }}>
            <label>Add card to {column.title}<input aria-label={`Add card to ${column.title}`} value={drafts[column.column_id] || ""} disabled={!snapshot.permissions.can_write || pending} onChange={(event) => setDrafts((current) => ({ ...current, [column.column_id]: event.target.value }))} /></label>
            <button type="submit" disabled={!snapshot.permissions.can_write || pending || !drafts[column.column_id]?.trim()}>Add to {column.title}</button>
          </form>
        </div>
      </section>)}
    </section>
    {selectedCard ? <CardDialog
      card={selectedCard}
      columns={board.columns}
      pending={pending}
      canWrite={snapshot.permissions.can_write}
      canDelete={snapshot.permissions.can_delete}
      onClose={() => closeCard(selectedCard.card_id)}
      onUpdate={(fields: CardUpdateFields) => void mutateCard("update_kanban_card", { ...cardArgs(selectedCard), ...fields }, `Updated ${selectedCard.title}`)}
      onWorklog={(kind, entry) => void mutateCard("append_kanban_worklog", { ...cardArgs(selectedCard), kind, entry_markdown: entry }, `Added work log to ${selectedCard.title}`)}
      onMove={(targetColumnID) => void mutateCard("move_kanban_card", { ...cardArgs(selectedCard), target_column_id: targetColumnID }, `Moved ${selectedCard.title}`)}
      onDelete={() => void deleteCard(selectedCard)}
    /> : null}
  </main>;
}
