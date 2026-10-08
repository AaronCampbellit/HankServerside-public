import { useEffect, useRef, useState } from "react";
import type { KanbanCard, KanbanColumn } from "./types";

export type CardUpdateFields = {
  title?: string;
  details_markdown?: string;
  due_date?: string;
  tags?: string[];
};

type Props = {
  card: KanbanCard;
  columns: KanbanColumn[];
  pending: boolean;
  canWrite: boolean;
  canDelete: boolean;
  onUpdate: (fields: CardUpdateFields) => void;
  onWorklog: (kind: string, entry: string) => void;
  onMove: (columnID: string) => void;
  onDelete: () => void;
  onClose: () => void;
};

export function CardDialog({ card, columns, pending, canWrite, canDelete, onUpdate, onWorklog, onMove, onDelete, onClose }: Props) {
  const [title, setTitle] = useState(card.title);
  const [details, setDetails] = useState(card.details_markdown);
  const [dueDate, setDueDate] = useState(card.due_date || "");
  const [tags, setTags] = useState(card.tags.join(", "));
  const [worklogKind, setWorklogKind] = useState("progress");
  const [worklogEntry, setWorklogEntry] = useState("");
  const [targetColumnID, setTargetColumnID] = useState(card.column_id);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLElement>(null);

  useEffect(() => {
    closeButtonRef.current?.focus();
    const handleKeyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !pending) {
        if (confirmingDelete) setConfirmingDelete(false);
        else onClose();
      }
      if (event.key === "Tab") {
        const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled)") || []);
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    };
    window.addEventListener("keydown", handleKeyboard);
    return () => window.removeEventListener("keydown", handleKeyboard);
  }, [confirmingDelete, onClose, pending]);

  function save() {
    const fields: CardUpdateFields = {};
    if (title !== card.title) fields.title = title;
    if (details !== card.details_markdown) fields.details_markdown = details;
    if (dueDate !== (card.due_date || "")) fields.due_date = dueDate;
    const nextTags = tags.split(",").map((tag) => tag.trim()).filter(Boolean);
    if (JSON.stringify(nextTags) !== JSON.stringify(card.tags)) fields.tags = nextTags;
    if (Object.keys(fields).length) onUpdate(fields);
  }

  return <div className="kanban-dialog-backdrop" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section ref={dialogRef} className="kanban-dialog" role="dialog" aria-modal="true" aria-labelledby="card-dialog-title">
      <header><h2 id="card-dialog-title">{card.title}</h2><button ref={closeButtonRef} type="button" onClick={onClose} aria-label="Close card">×</button></header>
      <div className="kanban-dialog-grid">
        <label>Card title<input aria-label="Card title" value={title} disabled={!canWrite || pending} onChange={(event) => setTitle(event.target.value)} /></label>
        <label>Details<textarea aria-label="Card details" value={details} disabled={!canWrite || pending} onChange={(event) => setDetails(event.target.value)} /></label>
        <label>Due date<input aria-label="Due date" type="date" value={dueDate} disabled={!canWrite || pending} onChange={(event) => setDueDate(event.target.value)} /></label>
        <label>Tags<input aria-label="Tags" value={tags} disabled={!canWrite || pending} onChange={(event) => setTags(event.target.value)} /></label>
        <button type="button" disabled={!canWrite || pending || !title.trim()} onClick={save}>Save changes</button>
      </div>

      <fieldset disabled={!canWrite || pending}><legend>Move card</legend>
        <label>Move to column<select aria-label="Move to column" value={targetColumnID} onChange={(event) => setTargetColumnID(event.target.value)}>{columns.map((column) => <option key={column.column_id} value={column.column_id}>{column.title}</option>)}</select></label>
        <button type="button" disabled={targetColumnID === card.column_id} onClick={() => onMove(targetColumnID)}>Move card</button>
      </fieldset>

      <fieldset disabled={!canWrite || pending}><legend>Add work log</legend>
        <label>Kind<select aria-label="Work log kind" value={worklogKind} onChange={(event) => setWorklogKind(event.target.value)}><option value="progress">Progress</option><option value="verification">Verification</option><option value="blocker">Blocker</option><option value="outcome">Outcome</option></select></label>
        <label>Entry<textarea aria-label="Work log entry" value={worklogEntry} onChange={(event) => setWorklogEntry(event.target.value)} /></label>
        <button type="button" disabled={!worklogEntry.trim()} onClick={() => onWorklog(worklogKind, worklogEntry.trim())}>Add work log</button>
      </fieldset>

      {card.attachments?.length ? <section className="kanban-attachments" aria-labelledby="attachments-title"><h3 id="attachments-title">Attachments</h3><ul>{card.attachments.map((attachment) => <li key={attachment.id}><strong>{attachment.filename}</strong><span>{attachment.content_type} · {formatBytes(attachment.size_bytes)}</span></li>)}</ul></section> : null}
      {canDelete && !confirmingDelete ? <button className="kanban-danger" type="button" disabled={pending} onClick={() => setConfirmingDelete(true)}>Delete card</button> : null}
      {canDelete && confirmingDelete ? <section className="kanban-delete-confirm" role="alertdialog" aria-labelledby="delete-confirm-title" aria-describedby="delete-confirm-description">
        <h3 id="delete-confirm-title">Delete {card.title}?</h3>
        <p id="delete-confirm-description">This cannot be undone. ChatGPT will ask you to confirm again before Hank deletes the card.</p>
        <div><button type="button" disabled={pending} onClick={() => setConfirmingDelete(false)}>Cancel deletion</button><button className="kanban-danger" type="button" disabled={pending} onClick={onDelete} aria-label={`Confirm delete ${card.title}`}>Confirm delete</button></div>
      </section> : null}
    </section>
  </div>;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  return `${Math.round(value / 1024)} KB`;
}
