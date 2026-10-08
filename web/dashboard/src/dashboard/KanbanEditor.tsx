import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import type { KanbanBoard, KanbanCard, KanbanColumn, NoteAttachment } from "../api/profileNotes";
import type { OfflineNotesRepository } from "../offlineNotes/repository";
import { KanbanCardModal, type DescriptionSelection } from "./KanbanCardModal";
import { KanbanRichText } from "./KanbanRichText";
import { attachmentDeletionMessage, attachmentDeletionPlan } from "./kanbanAttachments";
import "./kanbanWorkflow.css";

type CardLocation = { columnID: string; cardID: string };

const columnDragDataType = "application/x-hank-kanban-column";

type KanbanEditorProps = {
  board: KanbanBoard;
  attachments?: NoteAttachment[];
  onChange: (board: KanbanBoard) => void;
  onUpload: (file: File) => Promise<NoteAttachment>;
  confirmDelete: (message: string) => Promise<boolean>;
  onDeleteItems: (board: KanbanBoard, attachments: NoteAttachment[]) => Promise<boolean>;
  isDefaultBoard?: boolean;
  onSetDefaultBoard?: (enabled: boolean) => void;
  attachmentMutationsEnabled?: boolean;
  attachmentRepository?: Pick<OfflineNotesRepository, "cacheAttachment" | "readCachedAttachment"> | null;
  attachmentNoteLocalKey?: string;
  attachmentOnline?: boolean;
};

const defaultColumnTitles = ["Inbox", "In progress", "Done"];
const kanbanRoles = [
  ["", "None"],
  ["planning", "Planning"],
  ["active", "Active Work"],
  ["rework", "Rework"],
  ["human", "Needs Human"],
  ["review", "Review"],
  ["complete", "Complete"],
] as const;
const kanbanRoleLabels = Object.fromEntries(kanbanRoles) as Record<string, string>;
let fallbackID = 0;

function uniqueID(prefix: string): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  if (uuid) return `${prefix}-${uuid}`;
  fallbackID += 1;
  return `${prefix}-${fallbackID}`;
}

function ordered<T extends { sort_order?: number }>(items: T[] | undefined): T[] {
  return [...(items || [])].sort((left, right) => (left.sort_order ?? 0) - (right.sort_order ?? 0));
}

function normalizeBoard(board: KanbanBoard): KanbanBoard {
  return {
    ...board,
    intake_column_id: board.intake_column_id || "",
    columns: ordered(board.columns).map((column, columnIndex) => ({
      ...column,
      id: column.id || uniqueID("column"),
      title: column.title?.trim() || "Untitled column",
      sort_order: columnIndex,
      cards: ordered(column.cards).map((card, cardIndex) => ({
        ...card,
        id: card.id || uniqueID("card"),
        text: typeof card.text === "string" ? card.text : card.title || "",
        sort_order: cardIndex,
      })),
    })),
  };
}

export function boardFromMarkdown(body: string): KanbanBoard {
  const columns: KanbanColumn[] = [];
  let current: KanbanColumn | null = null;
  for (const rawLine of body.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line.startsWith("## ")) {
      current = {
        id: uniqueID("column"),
        title: line.replace(/^##\s+/, "").trim() || "Untitled column",
        sort_order: columns.length,
        cards: [],
      };
      columns.push(current);
      continue;
    }
    if (!current || !/^[-*]\s+/.test(line)) continue;
    const text = line.replace(/^[-*]\s+/, "").trim();
    if (!text) continue;
    current.cards?.push({ id: uniqueID("card"), text, sort_order: current.cards.length });
  }
  if (!columns.length) {
    return {
      columns: defaultColumnTitles.map((title, index) => ({
        id: uniqueID("column"),
        title,
        sort_order: index,
        cards: [],
      })),
    };
  }
  return normalizeBoard({ columns });
}

export function boardToMarkdown(title: string, board: KanbanBoard): string {
  const lines = [`# ${title.trim() || "Board"}`, ""];
  for (const column of ordered(board.columns)) {
    lines.push(`## ${column.title?.trim() || "Untitled column"}`);
    for (const card of ordered(column.cards)) {
      const cardTitle = cardTitleAndDescription(card).title.trim();
      if (cardTitle) lines.push(`- ${cardTitle}`);
    }
    lines.push("");
  }
  return lines.join("\n").trim();
}

function cardTitleAndDescription(card: KanbanCard): { title: string; description: string } {
  const lines = String(card.text ?? card.title ?? "").split(/\r?\n/);
  return {
    title: lines[0] || "",
    description: lines.slice(1).join("\n"),
  };
}

function cardText(title: string, description: string): string {
  return description.length ? `${title}\n${description}` : title;
}

function displayCardTitle(title: string): string {
  return title.trim() || "Untitled task";
}

function CardDescription({ value, attachments }: { value: string; attachments: NoteAttachment[] }) {
  if (!value.trim()) return null;
  return <div className="kanban-card-description"><KanbanRichText value={value} attachments={attachments} maxLines={5} /></div>;
}

function SmallIcon({ name }: { name: "plus" | "search" | "left" | "right" | "trash" | "close" | "grip" | "upload" }) {
  const paths: Record<string, ReactNode> = {
    plus: <path d="M12 5v14M5 12h14" />,
    search: <><circle cx="11" cy="11" r="6" /><path d="m16 16 4 4" /></>,
    left: <path d="m14 6-6 6 6 6" />,
    right: <path d="m10 6 6 6-6 6" />,
    trash: <><path d="M5 7h14M9 7V5h6v2M8 10v8M12 10v8M16 10v8" /></>,
    close: <path d="m7 7 10 10M17 7 7 17" />,
    grip: <><circle cx="9" cy="8" r=".8" fill="currentColor" /><circle cx="15" cy="8" r=".8" fill="currentColor" /><circle cx="9" cy="12" r=".8" fill="currentColor" /><circle cx="15" cy="12" r=".8" fill="currentColor" /><circle cx="9" cy="16" r=".8" fill="currentColor" /><circle cx="15" cy="16" r=".8" fill="currentColor" /></>,
    upload: <><path d="M12 16V5m0 0L8 9m4-4 4 4" /><path d="M5 15v4h14v-4" /></>,
  };
  return <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}

export function KanbanEditor({
  board,
  attachments = [],
  onChange,
  onUpload,
  confirmDelete,
  onDeleteItems,
  isDefaultBoard = false,
  onSetDefaultBoard = () => undefined,
  attachmentMutationsEnabled = true,
  attachmentRepository = null,
  attachmentNoteLocalKey = "",
  attachmentOnline = true,
}: KanbanEditorProps) {
  const normalized = useMemo(() => normalizeBoard(board), [board]);
  const boardRef = useRef(normalized);
  boardRef.current = normalized;
  const columns = normalized.columns || [];
  const [query, setQuery] = useState("");
  const [addingColumnID, setAddingColumnID] = useState("");
  const [taskDraft, setTaskDraft] = useState("");
  const [editingColumnID, setEditingColumnID] = useState("");
  const [selected, setSelected] = useState<CardLocation | null>(null);
  const [dragging, setDragging] = useState<CardLocation | null>(null);
  const [dropTarget, setDropTarget] = useState("");
  const [draggingColumnID, setDraggingColumnID] = useState("");
  const [columnDropTargetID, setColumnDropTargetID] = useState("");
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [columnMenuID, setColumnMenuID] = useState("");
  const [boardMenuOpen, setBoardMenuOpen] = useState(false);
  const suppressOpenRef = useRef(false);
  const dragReleaseTimerRef = useRef<number | null>(null);
  const cardButtonRefs = useRef(new Map<string, HTMLButtonElement>());
  const columnRefs = useRef(new Map<string, HTMLElement>());
  const boardMenuRef = useRef<HTMLDivElement>(null);

  const selectedColumn = selected ? columns.find((column) => column.id === selected.columnID) : undefined;
  const selectedCard = selectedColumn?.cards?.find((card) => card.id === selected?.cardID);
  const selectedParts = selectedCard ? cardTitleAndDescription(selectedCard) : null;

  useEffect(() => {
    if (!selected || selectedCard) return;
    setSelected(null);
  }, [selected, selectedCard]);

  useEffect(() => () => {
    if (dragReleaseTimerRef.current !== null) window.clearTimeout(dragReleaseTimerRef.current);
  }, []);

  useEffect(() => {
    if (!columnMenuID && !boardMenuOpen) return;
    const closeOnPointerDown = (event: PointerEvent) => {
      if (!columnRefs.current.get(columnMenuID)?.contains(event.target as Node)) setColumnMenuID("");
      if (!boardMenuRef.current?.contains(event.target as Node)) setBoardMenuOpen(false);
    };
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setColumnMenuID("");
      setBoardMenuOpen(false);
    };
    document.addEventListener("pointerdown", closeOnPointerDown);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnPointerDown);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [boardMenuOpen, columnMenuID]);

  function commit(next: KanbanBoard) {
    const normalizedNext = normalizeBoard({ ...boardRef.current, ...next });
    boardRef.current = normalizedNext;
    onChange(normalizedNext);
  }

  function updateColumn(columnID: string, updater: (column: KanbanColumn) => KanbanColumn) {
    commit({ columns: columns.map((column) => column.id === columnID ? updater(column) : column) });
  }

  function updateCard(location: CardLocation, updater: (card: KanbanCard) => KanbanCard) {
    updateColumn(location.columnID, (column) => ({
      ...column,
      cards: ordered(column.cards).map((card) => card.id === location.cardID
        ? { ...updater(card), created_at: card.created_at, updated_at: new Date().toISOString() }
        : card),
    }));
  }

  function updateCardByID(cardID: string, updater: (card: KanbanCard) => KanbanCard) {
    commit({
      columns: ordered(boardRef.current.columns).map((column) => ({
        ...column,
        cards: ordered(column.cards).map((card) => card.id === cardID
          ? { ...updater(card), created_at: card.created_at, updated_at: new Date().toISOString() }
          : card),
      })),
    });
  }

  function addTask(columnID: string) {
    const title = taskDraft.trim();
    if (!title) return;
    const now = new Date().toISOString();
    const card: KanbanCard = { id: uniqueID("card"), text: title, sort_order: 0, created_at: now, updated_at: now };
    updateColumn(columnID, (column) => ({
      ...column,
      cards: [card, ...ordered(column.cards)].map((item, sortOrder) => ({ ...item, sort_order: sortOrder })),
    }));
    setTaskDraft("");
    setAddingColumnID("");
  }

  function moveColumn(columnID: string, direction: -1 | 1) {
    const index = columns.findIndex((column) => column.id === columnID);
    const target = index + direction;
    if (index < 0 || target < 0 || target >= columns.length) return;
    const next = [...columns];
    [next[index], next[target]] = [next[target], next[index]];
    commit({ columns: next.map((column, sortOrder) => ({ ...column, sort_order: sortOrder })) });
  }

  function reorderColumn(columnID: string, targetColumnID: string) {
    const sourceIndex = columns.findIndex((column) => column.id === columnID);
    const targetIndex = columns.findIndex((column) => column.id === targetColumnID);
    if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) return;
    const next = [...columns];
    const [moved] = next.splice(sourceIndex, 1);
    next.splice(targetIndex, 0, moved);
    commit({ columns: next.map((column, sortOrder) => ({ ...column, sort_order: sortOrder })) });
  }

  function beginColumnDrag(columnID: string, event: DragEvent<HTMLElement>) {
    setDraggingColumnID(columnID);
    setColumnDropTargetID("");
    setDragging(null);
    setDropTarget("");
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData(columnDragDataType, columnID);
  }

  function dropColumn(event: DragEvent, targetColumnID: string) {
    event.preventDefault();
    if (draggingColumnID) reorderColumn(draggingColumnID, targetColumnID);
    endColumnDrag();
  }

  function endColumnDrag() {
    setDraggingColumnID("");
    setColumnDropTargetID("");
  }

  async function deleteColumn(column: KanbanColumn) {
    if (deleting) return;
    const count = column.cards?.length || 0;
    const nextBoard = normalizeBoard({
      ...normalized,
      intake_column_id: normalized.intake_column_id === column.id ? "" : normalized.intake_column_id,
      columns: columns.filter((item) => item.id !== column.id),
    });
    if (!count) { commit(nextBoard); return; }
    const removedCardIDs = new Set((column.cards || []).map((card) => card.id || "").filter(Boolean));
    const plan = attachmentDeletionPlan(normalized, removedCardIDs, attachments);
    const message = attachmentDeletionMessage(`Delete “${column.title}” and its ${count} ${count === 1 ? "task" : "tasks"}?`, plan);
    if (!await confirmDelete(message)) return;
    setDeleting(true);
    try {
      if (await onDeleteItems(nextBoard, plan.exclusive)) commit(nextBoard);
    } finally {
      setDeleting(false);
    }
  }

  function moveCard(
    location: CardLocation,
    targetColumnID: string,
    targetIndex = Number.MAX_SAFE_INTEGER,
    preserveEditor = selected?.cardID === location.cardID,
  ) {
    const sourceColumn = columns.find((column) => column.id === location.columnID);
    const card = sourceColumn?.cards?.find((item) => item.id === location.cardID);
    if (!card) return;
    const nextColumns = columns.map((column) => ({
      ...column,
      cards: ordered(column.cards).filter((item) => item.id !== location.cardID),
    }));
    const target = nextColumns.find((column) => column.id === targetColumnID);
    if (!target) return;
    const cards = [...(target.cards || [])];
    cards.splice(Math.min(targetIndex, cards.length), 0, { ...card, updated_at: new Date().toISOString() });
    target.cards = cards;
    commit({ columns: nextColumns });
    if (preserveEditor) setSelected({ columnID: targetColumnID, cardID: location.cardID });
  }

  function moveSelected(direction: -1 | 1) {
    if (!selected) return;
    const index = columns.findIndex((column) => column.id === selected.columnID);
    const target = columns[index + direction];
    if (target) moveCard(selected, target.id || "");
  }

  function dropCard(event: DragEvent, columnID: string, targetIndex: number) {
    event.preventDefault();
    if (dragging) {
      moveCard(dragging, columnID, targetIndex, false);
      scheduleDragRelease();
    }
    setDragging(null);
    setDropTarget("");
  }

  function beginCardDrag(location: CardLocation, event: DragEvent<HTMLElement>) {
    if (dragReleaseTimerRef.current !== null) window.clearTimeout(dragReleaseTimerRef.current);
    suppressOpenRef.current = true;
    setDragging(location);
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", location.cardID);
  }

  function scheduleDragRelease() {
    if (dragReleaseTimerRef.current !== null) window.clearTimeout(dragReleaseTimerRef.current);
    dragReleaseTimerRef.current = window.setTimeout(() => {
      suppressOpenRef.current = false;
      dragReleaseTimerRef.current = null;
    }, 0);
  }

  function endCardDrag() {
    setDragging(null);
    setDropTarget("");
    scheduleDragRelease();
  }

  function openCard(location: CardLocation) {
    if (suppressOpenRef.current) return;
    setSelected(location);
  }

  function closeCard() {
    const cardID = selected?.cardID || "";
    setSelected(null);
    requestAnimationFrame(() => cardButtonRefs.current.get(cardID)?.focus());
  }

  async function uploadFiles(files: File[], selection?: DescriptionSelection) {
    if (!attachmentMutationsEnabled) {
      setUploadError("Available when connected");
      return;
    }
    if (!files.length || !selected) return;
    const cardID = selected.cardID;
    setUploading(true);
    setUploadError("");
    let insertionStart = selection?.start;
    let replacementEnd = selection?.end;
    try {
      for (const file of files) {
        const attachment = await onUpload(file);
        const reference = attachment.markdown_reference || `![${attachment.filename}](hank-note-attachment://${attachment.id})`;
        updateCardByID(cardID, (card) => {
          const parts = cardTitleAndDescription(card);
          const start = Math.min(insertionStart ?? parts.description.length, parts.description.length);
          const end = Math.max(start, Math.min(replacementEnd ?? start, parts.description.length));
          const before = parts.description.slice(0, start);
          const after = parts.description.slice(end);
          const prefix = before && !before.endsWith("\n") ? "\n\n" : "";
          const suffix = after && !after.startsWith("\n") ? "\n\n" : "";
          const inserted = `${prefix}${reference}`;
          insertionStart = before.length + inserted.length;
          replacementEnd = insertionStart;
          const description = `${before}${inserted}${suffix}${after}`;
          return { ...card, text: cardText(parts.title, description) };
        });
      }
    } catch (error) {
      setUploadError(error instanceof Error ? error.message : "File could not be uploaded.");
    } finally {
      setUploading(false);
    }
  }

  async function deleteSelected() {
    if (deleting || !selected || !selectedCard || !selectedParts) return;
    const nextBoard = normalizeBoard({
      columns: columns.map((column) => column.id === selected.columnID
        ? { ...column, cards: ordered(column.cards).filter((card) => card.id !== selected.cardID) }
        : column),
    });
    const plan = attachmentDeletionPlan(normalized, new Set([selected.cardID]), attachments);
    if (!await confirmDelete(attachmentDeletionMessage(`Delete “${selectedParts.title}”?`, plan))) return;
    setDeleting(true);
    try {
      if (!await onDeleteItems(nextBoard, plan.exclusive)) return;
      commit(nextBoard);
      setSelected(null);
    } finally {
      setDeleting(false);
    }
  }

  function addColumn() {
    const column: KanbanColumn = { id: uniqueID("column"), title: "New column", sort_order: columns.length, cards: [] };
    commit({ columns: [...columns, column] });
    setEditingColumnID(column.id || "");
  }

  function setIntakeColumn(columnID: string) {
    commit({ intake_column_id: columnID, columns });
  }

  function setColumnRole(columnID: string, role: string) {
    commit({
      columns: columns.map((column) => ({
        ...column,
        role: column.id === columnID ? role : role && column.role === role ? "" : column.role,
      })),
    });
  }

  const normalizedQuery = query.trim().toLowerCase();

  return (
    <div className={`kanban-workspace${selectedCard ? " details-open" : ""}`}>
      <div className="kanban-main">
        <header className="kanban-workbar">
          <label className="kanban-search">
            <SmallIcon name="search" />
            <span className="visually-hidden">Search cards</span>
            <input aria-label="Search cards" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search cards" />
          </label>
          <span className="kanban-board-summary">{columns.reduce((total, column) => total + (column.cards?.length || 0), 0)} tasks · {columns.length} columns</span>
          {isDefaultBoard ? <span className="kanban-workflow-badge">Default board</span> : null}
          <div className="kanban-board-options" ref={boardMenuRef}>
            <button
              className="kanban-board-options-trigger"
              type="button"
              aria-label="Board options"
              aria-expanded={boardMenuOpen}
              onClick={() => setBoardMenuOpen((open) => !open)}
            >•••</button>
            {boardMenuOpen ? (
              <div className="kanban-board-options-popover">
                {isDefaultBoard ? (
                  <>
                    <button type="button" disabled>Default board</button>
                    <button type="button" onClick={() => { setBoardMenuOpen(false); onSetDefaultBoard(false); }}>Clear default board</button>
                  </>
                ) : (
                  <button type="button" onClick={() => { setBoardMenuOpen(false); onSetDefaultBoard(true); }}>Set as default board</button>
                )}
              </div>
            ) : null}
          </div>
          <button className="kanban-add-column" type="button" onClick={addColumn}><SmallIcon name="plus" /> Add column</button>
        </header>

        <div className="kanban-board" aria-label="Kanban board">
          {columns.map((column, columnIndex) => {
            const cards = ordered(column.cards);
            const visibleCards = normalizedQuery
              ? cards.filter((card) => [card.text, card.title, card.priority, ...(card.tags || [])].filter(Boolean).join(" ").toLowerCase().includes(normalizedQuery))
              : cards;
            return (
              <section
                className={`kanban-column${dropTarget === column.id ? " is-drop-target" : ""}${draggingColumnID === column.id ? " is-column-dragging" : ""}${columnDropTargetID === column.id ? " is-column-drop-target" : ""}`}
                key={column.id}
                ref={(node) => {
                  const columnID = column.id || "";
                  if (node) columnRefs.current.set(columnID, node);
                  else columnRefs.current.delete(columnID);
                }}
                aria-labelledby={`kanban-column-${column.id}`}
                onDragOver={(event) => {
                  event.preventDefault();
                  if (draggingColumnID) {
                    setColumnDropTargetID(column.id || "");
                    setDropTarget("");
                    return;
                  }
                  setDropTarget(column.id || "");
                }}
                onDragLeave={(event) => {
                  if (event.currentTarget.contains(event.relatedTarget as Node)) return;
                  if (draggingColumnID) setColumnDropTargetID("");
                  else setDropTarget("");
                }}
                onDrop={(event) => draggingColumnID
                  ? dropColumn(event, column.id || "")
                  : dropCard(event, column.id || "", cards.length)}
              >
                <header className="kanban-column-head">
                  <div className="kanban-column-summary">
                    {editingColumnID === column.id ? (
                      <input
                        autoFocus
                        aria-label="Column title"
                        value={column.title || ""}
                        onChange={(event) => updateColumn(column.id || "", (item) => ({ ...item, title: event.target.value }))}
                        onBlur={() => setEditingColumnID("")}
                        onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => { if (event.key === "Enter" || event.key === "Escape") setEditingColumnID(""); }}
                      />
                    ) : (
                      <button className="kanban-column-title" type="button" onClick={() => setEditingColumnID(column.id || "")} title="Rename column">
                        <h2 id={`kanban-column-${column.id}`}>{column.title}</h2>
                      </button>
                    )}
                    {normalized.intake_column_id === column.id ? <span className="kanban-workflow-badge">Intake</span> : null}
                    {column.role ? <span className="kanban-workflow-badge">{kanbanRoleLabels[column.role] || column.role}</span> : null}
                    <span className="kanban-count">{cards.length}</span>
                  </div>
                  <div className="kanban-column-actions">
                    <button
                      className="kanban-column-grip"
                      type="button"
                      draggable
                      aria-label={`Drag ${column.title} column`}
                      aria-grabbed={draggingColumnID === column.id}
                      onDragStart={(event) => beginColumnDrag(column.id || "", event)}
                      onDragEnd={endColumnDrag}
                    >
                      <SmallIcon name="grip" />
                    </button>
                    <button type="button" aria-label={`Move ${column.title} left`} disabled={columnIndex === 0} onClick={() => moveColumn(column.id || "", -1)}><SmallIcon name="left" /></button>
                    <button type="button" aria-label={`Move ${column.title} right`} disabled={columnIndex === columns.length - 1} onClick={() => moveColumn(column.id || "", 1)}><SmallIcon name="right" /></button>
                    <button className="kanban-column-add" type="button" aria-label={`Add task to ${column.title}`} title={`Add task to ${column.title}`} onClick={() => setAddingColumnID(column.id || "")}><SmallIcon name="plus" /></button>
                    <button type="button" aria-label={`Column options for ${column.title}`} aria-expanded={columnMenuID === column.id} onClick={() => setColumnMenuID((current) => current === column.id ? "" : column.id || "")}>•••</button>
                    <button type="button" aria-label={`Delete ${column.title}`} disabled={deleting} onClick={() => void deleteColumn(column)}><SmallIcon name="trash" /></button>
                  </div>
                </header>

                {columnMenuID === column.id ? (
                  <div className="kanban-column-menu">
                    <button type="button" onClick={() => setIntakeColumn(column.id || "")}>
                      {normalized.intake_column_id === column.id ? `${column.title} is intake` : `Set ${column.title} as intake`}
                    </button>
                    <label>
                      <span>Role for {column.title}</span>
                      <select aria-label={`Role for ${column.title}`} value={column.role || ""} onChange={(event) => setColumnRole(column.id || "", event.target.value)}>
                        {kanbanRoles.map(([value, label]) => <option key={value || "none"} value={value}>{label}</option>)}
                      </select>
                    </label>
                  </div>
                ) : null}

                {addingColumnID === column.id ? (
                  <form className="kanban-task-composer" onSubmit={(event) => { event.preventDefault(); addTask(column.id || ""); }}>
                    <label>
                      <span className="visually-hidden">Task title</span>
                      <textarea
                        autoFocus
                        aria-label="Task title"
                        value={taskDraft}
                        onChange={(event) => setTaskDraft(event.target.value)}
                        onKeyDown={(event) => {
                          if (event.key !== "Enter" || event.shiftKey) return;
                          event.preventDefault();
                          addTask(column.id || "");
                        }}
                        placeholder="What needs doing?"
                        rows={2}
                      />
                    </label>
                    <div>
                      <button type="submit" disabled={!taskDraft.trim()}>Create task</button>
                      <button type="button" onClick={() => { setAddingColumnID(""); setTaskDraft(""); }}>Cancel</button>
                    </div>
                  </form>
                ) : null}

                <div className="kanban-card-stack">
                  {visibleCards.map((card) => {
                    const parts = cardTitleAndDescription(card);
                    const displayTitle = displayCardTitle(parts.title);
                    const cardIndex = cards.findIndex((item) => item.id === card.id);
                    return (
                      <article
                        className={`kanban-card color-${card.color || "default"}${dragging?.cardID === card.id ? " is-dragging" : ""}`}
                        key={card.id}
                        draggable
                        onDragStart={(event) => beginCardDrag({ columnID: column.id || "", cardID: card.id || "" }, event)}
                        onDragEnd={endCardDrag}
                        onDragOver={(event) => { if (!draggingColumnID) event.preventDefault(); }}
                        onDrop={(event) => {
                          if (draggingColumnID) return;
                          event.stopPropagation();
                          dropCard(event, column.id || "", cardIndex);
                        }}
                      >
                        <button
                          ref={(node) => {
                            if (node) cardButtonRefs.current.set(card.id || "", node);
                            else cardButtonRefs.current.delete(card.id || "");
                          }}
                          className="kanban-card-open"
                          type="button"
                          aria-label={`Open task ${displayTitle}`}
                          onClick={() => openCard({ columnID: column.id || "", cardID: card.id || "" })}
                        >
                          <span className="kanban-card-grip" aria-hidden="true"><SmallIcon name="grip" /></span>
                          <strong>{displayTitle}</strong>
                          <CardDescription value={parts.description} attachments={attachments} />
                          {(card.priority || card.tags?.length || card.due_date) ? <span className="kanban-card-metadata">
                            {card.priority ? <span className={`kanban-priority priority-${card.priority}`}>{card.priority}</span> : null}
                            {(card.tags || []).slice(0, 3).map((tag) => <span className="kanban-card-tag" key={tag}>{tag}</span>)}
                            {card.due_date ? <time dateTime={card.due_date}>{new Date(`${card.due_date}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</time> : null}
                          </span> : null}
                        </button>
                        <div className="kanban-card-move">
                          <button type="button" aria-label={`Move ${displayTitle} left`} disabled={columnIndex === 0} onClick={() => moveCard({ columnID: column.id || "", cardID: card.id || "" }, columns[columnIndex - 1]?.id || "", Number.MAX_SAFE_INTEGER, false)}><SmallIcon name="left" /></button>
                          <button type="button" aria-label={`Move ${displayTitle} right`} disabled={columnIndex === columns.length - 1} onClick={() => moveCard({ columnID: column.id || "", cardID: card.id || "" }, columns[columnIndex + 1]?.id || "", Number.MAX_SAFE_INTEGER, false)}><SmallIcon name="right" /></button>
                        </div>
                      </article>
                    );
                  })}
                  {!visibleCards.length ? <p className="kanban-column-empty">{normalizedQuery && cards.length ? "No matching tasks" : "Drop tasks here"}</p> : null}
                </div>

              </section>
            );
          })}
          {!columns.length ? (
            <button className="kanban-empty-board" type="button" onClick={addColumn}><SmallIcon name="plus" /> Add your first column</button>
          ) : null}
        </div>
      </div>

      {selectedCard && selected && selectedParts ? (
        <KanbanCardModal
          card={selectedCard}
          title={selectedParts.title}
          description={selectedParts.description}
          columnID={selected.columnID}
          columns={columns}
          attachments={attachments}
          uploading={uploading}
          deleting={deleting}
          uploadError={uploadError}
          attachmentMutationsEnabled={attachmentMutationsEnabled}
          attachmentRepository={attachmentRepository}
          attachmentNoteLocalKey={attachmentNoteLocalKey}
          attachmentOnline={attachmentOnline}
          onTitleChange={(value) => updateCard(selected, (card) => ({ ...card, text: cardText(value, selectedParts.description) }))}
          onDescriptionChange={(value) => updateCard(selected, (card) => ({ ...card, text: cardText(selectedParts.title, value) }))}
          onMove={(columnID) => moveCard(selected, columnID, Number.MAX_SAFE_INTEGER, true)}
          onDueDateChange={(value) => updateCard(selected, (card) => ({ ...card, due_date: value }))}
          onColorChange={(value) => updateCard(selected, (card) => ({ ...card, color: value }))}
          onPriorityChange={(value) => updateCard(selected, (card) => ({ ...card, priority: value }))}
          onTagsChange={(value) => updateCard(selected, (card) => ({ ...card, tags: value }))}
          onUploadFiles={uploadFiles}
          onMoveLeft={() => moveSelected(-1)}
          onMoveRight={() => moveSelected(1)}
          canMoveLeft={columns.findIndex((column) => column.id === selected.columnID) > 0}
          canMoveRight={columns.findIndex((column) => column.id === selected.columnID) < columns.length - 1}
          onDelete={() => void deleteSelected()}
          onClose={closeCard}
        />
      ) : null}
    </div>
  );
}
