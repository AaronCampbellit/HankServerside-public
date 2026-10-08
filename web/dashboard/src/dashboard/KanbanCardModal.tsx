import { useEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import type { KanbanCard, KanbanColumn, NoteAttachment } from "../api/profileNotes";
import type { OfflineNotesRepository } from "../offlineNotes/repository";
import { KanbanRichText } from "./KanbanRichText";
import { htmlToMarkdown, markdownToHTML } from "./richTextMarkdown";
import { AttachmentActions } from "./AttachmentActions";

export type DescriptionSelection = { start: number; end: number };

export type KanbanCardModalProps = {
  card: KanbanCard;
  title: string;
  description: string;
  columnID: string;
  columns: KanbanColumn[];
  attachments: NoteAttachment[];
  uploading: boolean;
  deleting: boolean;
  uploadError: string;
  attachmentMutationsEnabled?: boolean;
  attachmentRepository?: Pick<OfflineNotesRepository, "cacheAttachment" | "readCachedAttachment"> | null;
  attachmentNoteLocalKey?: string;
  attachmentOnline?: boolean;
  onTitleChange: (value: string) => void;
  onDescriptionChange: (value: string) => void;
  onMove: (columnID: string) => void;
  onDueDateChange: (value: string) => void;
  onColorChange: (value: string) => void;
  onPriorityChange: (value: string) => void;
  onTagsChange: (value: string[]) => void;
  onUploadFiles: (files: File[], selection?: DescriptionSelection) => Promise<void>;
  onMoveLeft: () => void;
  onMoveRight: () => void;
  canMoveLeft: boolean;
  canMoveRight: boolean;
  onDelete: () => void;
  onClose: () => void;
};

const cardColors = ["default", "cyan", "blue", "green", "amber", "violet"] as const;
const focusableSelector = "textarea:not([disabled]), button:not([disabled]), input:not([disabled]), select:not([disabled]), a[href]";

function createdDateLabel(value?: string): string {
  if (!value) return "Created date unavailable";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) return "Created date unavailable";
  return `Created ${date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })}`;
}

function ModalIcon({ name }: { name: "close" | "upload" | "left" | "right" | "trash" }) {
  const paths: Record<string, ReactNode> = {
    close: <path d="m7 7 10 10M17 7 7 17" />,
    upload: <><path d="M12 16V5m0 0L8 9m4-4 4 4" /><path d="M5 15v4h14v-4" /></>,
    left: <path d="m14 6-6 6 6 6" />,
    right: <path d="m10 6 6 6-6 6" />,
    trash: <><path d="M5 7h14M9 7V5h6v2M8 10v8M12 10v8M16 10v8" /></>,
  };
  return <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}

function referencedAttachments(description: string, attachments: NoteAttachment[]): NoteAttachment[] {
  return attachments.filter((attachment) => description.includes(`hank-note-attachment://${attachment.id}`));
}

function attachmentIDForSelectedImage(editor: HTMLDivElement): string | undefined {
  const selection = window.getSelection?.();
  if (!selection || !selection.rangeCount) return undefined;
  const range = selection.getRangeAt(0);
  if (!editor.contains(range.commonAncestorContainer)) return undefined;
  const images = Array.from(range.cloneContents().querySelectorAll<HTMLImageElement>("img[data-markdown-target]"));
  if (images.length !== 1) return undefined;
  const match = /^hank-note-attachment:\/\/([^?#]+)/i.exec(images[0].dataset.markdownTarget || "");
  return match?.[1];
}

function pastedImageFiles(clipboardData: DataTransfer): File[] {
  const itemFiles = Array.from(clipboardData.items || [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file?.type.startsWith("image/")));
  if (itemFiles.length) return itemFiles;
  return Array.from(clipboardData.files || []).filter((file) => file.type.startsWith("image/"));
}

export function KanbanCardModal(props: KanbanCardModalProps) {
  const {
    card, title, description, columnID, columns, attachments, uploading, deleting, uploadError,
    attachmentMutationsEnabled = true,
    attachmentRepository = null,
    attachmentNoteLocalKey = "",
    attachmentOnline = true,
    onTitleChange, onDescriptionChange, onMove, onDueDateChange, onColorChange, onPriorityChange, onTagsChange,
    onUploadFiles, onMoveLeft, onMoveRight, canMoveLeft, canMoveRight, onDelete, onClose,
  } = props;
  const dialogRef = useRef<HTMLElement>(null);
  const descriptionRef = useRef<HTMLDivElement>(null);
  const lastRenderedDescriptionRef = useRef("");
  const richCommandPendingRef = useRef(false);
  const [editingDescription, setEditingDescription] = useState(false);
  const [clipboardImages, setClipboardImages] = useState<Map<string, File>>(new Map());
  const cardAttachments = referencedAttachments(description, attachments);
  const currentColumn = columns.find((column) => column.id === columnID);

  useEffect(() => {
    const editor = descriptionRef.current;
    if (!editingDescription || !editor) return;
    if (lastRenderedDescriptionRef.current !== description) {
      editor.innerHTML = markdownToHTML(description, {
        resolveImage: (target) => {
          const match = /^hank-note-attachment:\/\/([^?#]+)/i.exec(target);
          if (match) return attachments.find((attachment) => attachment.id === match[1])?.download_url || "";
          return /^(https?:|\/)/i.test(target) ? target : "";
        },
      });
      lastRenderedDescriptionRef.current = description;
    }
    editor.focus();
  }, [attachments, description, editingDescription]);

  useEffect(() => {
    if (!editingDescription) {
      setClipboardImages(new Map());
      return;
    }
    let cancelled = false;
    const imageAttachments = referencedAttachments(description, attachments)
      .filter((attachment) => attachment.content_type.startsWith("image/"));
    void Promise.all(imageAttachments.map(async (attachment) => {
      try {
        const response = await fetch(attachment.download_url, { credentials: "same-origin" });
        if (!response.ok) return undefined;
        const blob = await response.blob();
        if (!blob.type.startsWith("image/")) return undefined;
        return [attachment.id, new File([blob], attachment.filename, { type: blob.type })] as const;
      } catch {
        return undefined;
      }
    })).then((entries) => {
      if (!cancelled) setClipboardImages(new Map(entries.filter((entry): entry is readonly [string, File] => Boolean(entry))));
    });
    return () => {
      cancelled = true;
    };
  }, [attachments, description, editingDescription]);

  function handleDialogKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>(focusableSelector) || []);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function handleBackdropMouseDown(event: MouseEvent<HTMLDivElement>) {
    if (event.target === event.currentTarget) onClose();
  }

  function selectionInDescription(): boolean {
    const editor = descriptionRef.current;
    const selection = window.getSelection?.();
    if (!editor || !selection || !selection.rangeCount) return false;
    return editor.contains(selection.getRangeAt(0).commonAncestorContainer);
  }

  function placeCaretAtEnd(element: HTMLElement) {
    const selection = window.getSelection?.();
    if (!selection) return;
    const range = document.createRange();
    range.selectNodeContents(element);
    range.collapse(false);
    selection.removeAllRanges();
    selection.addRange(range);
  }

  function syncDescriptionFromEditor() {
    const editor = descriptionRef.current;
    if (!editor) return;
    const nextDescription = htmlToMarkdown(editor);
    lastRenderedDescriptionRef.current = nextDescription;
    onDescriptionChange(nextDescription);
  }

  function runRichCommand(command: string, value?: string) {
    const editor = descriptionRef.current;
    if (!editor) return;
    editor.focus();
    if (!selectionInDescription()) placeCaretAtEnd(editor);

    if ((command === "insertUnorderedList" || command === "insertOrderedList") && !(editor.textContent || "").trim()) {
      const list = document.createElement(command === "insertOrderedList" ? "ol" : "ul");
      const item = document.createElement("li");
      item.append(document.createElement("br"));
      list.append(item);
      editor.replaceChildren(list);
      placeCaretAtEnd(item);
      syncDescriptionFromEditor();
      return;
    }

    let effectiveCommand = command;
    let effectiveValue = value;
    if (command === "createLink" && (window.getSelection?.()?.isCollapsed ?? true)) {
      effectiveCommand = "insertHTML";
      effectiveValue = `<a href="${value}" rel="noreferrer">Link</a>`;
    }
    if (typeof document.execCommand === "function") {
      richCommandPendingRef.current = true;
      document.execCommand(effectiveCommand, false, effectiveValue);
      if (richCommandPendingRef.current) {
        richCommandPendingRef.current = false;
        syncDescriptionFromEditor();
      }
      return;
    }
    if (command === "insertUnorderedList" || command === "insertOrderedList") {
      const list = document.createElement(command === "insertOrderedList" ? "ol" : "ul");
      const item = document.createElement("li");
      item.append(document.createElement("br"));
      list.append(item);
      editor.append(list);
      placeCaretAtEnd(item);
      syncDescriptionFromEditor();
    }
  }

  function handleDescriptionKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== "Enter") return;
    const editor = descriptionRef.current;
    const selection = window.getSelection?.();
    if (!editor || !selection || !selection.rangeCount) return;
    const range = selection.getRangeAt(0);
    const selectionElement = range.endContainer.nodeType === Node.ELEMENT_NODE
      ? range.endContainer as Element
      : range.endContainer.parentElement;
    const item = selectionElement?.closest("li");
    if (!item || !editor.contains(item) || !(item.textContent || "").trim()) return;

    event.preventDefault();
    if (!range.collapsed) range.deleteContents();
    const tail = document.createRange();
    tail.selectNodeContents(item);
    tail.setStart(range.endContainer, range.endOffset);
    const remainder = tail.extractContents();
    const nextItem = document.createElement("li");
    if (remainder.childNodes.length) nextItem.append(remainder);
    else nextItem.append(document.createElement("br"));
    item.after(nextItem);
    const nextRange = document.createRange();
    nextRange.selectNodeContents(nextItem);
    nextRange.collapse(true);
    selection.removeAllRanges();
    selection.addRange(nextRange);
    syncDescriptionFromEditor();
  }

  function markdownSelection(): DescriptionSelection | undefined {
    const editor = descriptionRef.current;
    const selection = window.getSelection?.();
    if (!editor || !selection || !selection.rangeCount) return undefined;
    const range = selection.getRangeAt(0);
    if (!editor.contains(range.commonAncestorContainer)) return undefined;
    const startMarker = document.createTextNode("\uE000");
    const endMarker = document.createTextNode("\uE001");
    const endRange = range.cloneRange();
    endRange.collapse(false);
    endRange.insertNode(endMarker);
    const startRange = range.cloneRange();
    startRange.collapse(true);
    startRange.insertNode(startMarker);
    const marked = htmlToMarkdown(editor);
    startMarker.remove();
    endMarker.remove();
    const start = marked.indexOf("\uE000");
    const markedEnd = marked.indexOf("\uE001");
    if (start < 0 || markedEnd < start) return undefined;
    return { start, end: markedEnd - 1 };
  }

  function handleCardPaste(event: ClipboardEvent<HTMLElement>) {
    const files = pastedImageFiles(event.clipboardData);
    if (!files.length) return;
    if (!attachmentMutationsEnabled) {
      event.preventDefault();
      return;
    }
    event.preventDefault();
    const selection = editingDescription ? markdownSelection() : undefined;
    void onUploadFiles(files, selection).then(() => {
      setEditingDescription(false);
    });
  }

  function handleDescriptionCopy(event: ClipboardEvent<HTMLDivElement>) {
    const editor = descriptionRef.current;
    if (!editor) return;
    const attachmentID = attachmentIDForSelectedImage(editor);
    const image = attachmentID ? clipboardImages.get(attachmentID) : undefined;
    if (!image || !event.clipboardData.items?.add) return;
    event.clipboardData.items.add(image);
    event.preventDefault();
  }

  return (
    <div className="kanban-card-modal-backdrop" data-testid="kanban-card-modal-backdrop" onMouseDown={handleBackdropMouseDown}>
      <article
        ref={dialogRef}
        className={`kanban-card-modal color-${card.color || "default"}`}
        role="dialog"
        aria-modal="true"
        aria-label="Task details"
        onKeyDown={handleDialogKeyDown}
        onPaste={handleCardPaste}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="kanban-card-modal-header">
          <label className="kanban-detail-title">
            <span>Task in {currentColumn?.title || "board"}</span>
            <textarea className="kanban-card-title-input" autoFocus aria-label="Task title" rows={1} value={title} placeholder="Untitled task" onChange={(event) => onTitleChange(event.target.value)} />
          </label>
          <button type="button" aria-label="Close task details" onClick={onClose}><ModalIcon name="close" /></button>
        </header>

        <div className="kanban-card-modal-scroll">
          <section className="kanban-card-options" data-testid="kanban-card-options" aria-label="Task options">
            <p className="kanban-card-created"><time dateTime={card.created_at || undefined}>{createdDateLabel(card.created_at)}</time></p>
            <div className="kanban-detail-grid">
              <label>
                <span>Column</span>
                <select aria-label="Column" value={columnID} onChange={(event) => onMove(event.target.value)}>
                  {columns.map((column) => <option key={column.id} value={column.id}>{column.title}</option>)}
                </select>
              </label>
              <label>
                <span>Due date</span>
                <input aria-label="Due date" type="date" value={card.due_date || ""} onChange={(event) => onDueDateChange(event.target.value)} />
              </label>
              <label>
                <span>Priority</span>
                <select aria-label="Priority" value={card.priority || ""} onChange={(event) => onPriorityChange(event.target.value)}>
                  <option value="">No priority</option>
                  <option value="low">Low</option>
                  <option value="medium">Medium</option>
                  <option value="high">High</option>
                </select>
              </label>
              <label>
                <span>Tags</span>
                <input
                  aria-label="Tags"
                  placeholder="Hank, frontend"
                  value={(card.tags || []).join(", ")}
                  onChange={(event) => onTagsChange(Array.from(new Set(event.target.value.split(",").map((tag) => tag.trim()).filter(Boolean))))}
                />
              </label>
            </div>
            <div className="kanban-option-row">
              <div className="kanban-option-colors">
                <span>Card color</span>
                <div className="kanban-color-picker">
                  {cardColors.map((color) => (
                    <button
                      className={`color-${color}`}
                      type="button"
                      key={color}
                      aria-label={`${color === "default" ? "Default" : color[0].toUpperCase() + color.slice(1)} card`}
                      aria-pressed={(card.color || "default") === color}
                      onClick={() => onColorChange(color === "default" ? "" : color)}
                    />
                  ))}
                </div>
              </div>
              <div className="kanban-option-actions">
                <button type="button" aria-label="Move task left" disabled={!canMoveLeft} onClick={onMoveLeft}><ModalIcon name="left" /> Move left</button>
                <button type="button" aria-label="Move task right" disabled={!canMoveRight} onClick={onMoveRight}>Move right <ModalIcon name="right" /></button>
                <button className="danger" type="button" aria-label="Delete task" disabled={deleting} onClick={onDelete}><ModalIcon name="trash" /> {deleting ? "Deleting…" : "Delete"}</button>
              </div>
            </div>
          </section>

          <section className="kanban-detail-section">
            <h3>Description</h3>
            <div className="kanban-formatbar" aria-label="Description formatting">
              <button type="button" aria-label="Bold" disabled={!editingDescription} onClick={() => runRichCommand("bold")}><strong>B</strong></button>
              <button type="button" aria-label="Italic" disabled={!editingDescription} onClick={() => runRichCommand("italic")}><em>I</em></button>
              <button type="button" aria-label="Underline" disabled={!editingDescription} onClick={() => runRichCommand("underline")}><u>U</u></button>
              <button type="button" aria-label="Smaller heading" disabled={!editingDescription} onClick={() => runRichCommand("formatBlock", "p")}>A-</button>
              <button type="button" aria-label="Heading" disabled={!editingDescription} onClick={() => runRichCommand("formatBlock", "h2")}>H</button>
              <button type="button" aria-label="Larger heading" disabled={!editingDescription} onClick={() => runRichCommand("formatBlock", "h1")}>A+</button>
              <button type="button" aria-label="Bulleted list" disabled={!editingDescription} onClick={() => runRichCommand("insertUnorderedList")}>• List</button>
              <button type="button" aria-label="Numbered list" disabled={!editingDescription} onClick={() => runRichCommand("insertOrderedList")}>1. List</button>
              <button type="button" aria-label="Link" disabled={!editingDescription} onClick={() => runRichCommand("createLink", "https://example.com")}>Link</button>
              <button className="kanban-description-mode" type="button" aria-label={editingDescription ? "Preview description" : "Edit description"} onClick={() => setEditingDescription((current) => !current)}>
                {editingDescription ? "Preview" : "Edit"}
              </button>
            </div>
            {editingDescription ? (
              <div
                ref={descriptionRef}
                className="kanban-description-editor"
                contentEditable
                data-placeholder="Add context, links, checklists, or notes…"
                role="textbox"
                aria-label="Description"
                aria-multiline="true"
                suppressContentEditableWarning
                onInput={() => {
                  richCommandPendingRef.current = false;
                  syncDescriptionFromEditor();
                }}
                onKeyDown={handleDescriptionKeyDown}
                onCopy={handleDescriptionCopy}
              />
            ) : (
              <div
                className="kanban-description-preview"
                data-testid="kanban-description-preview"
                tabIndex={0}
                aria-label="Edit description area"
                onClick={(event) => {
                  if ((event.target as HTMLElement).closest("a, button")) return;
                  setEditingDescription(true);
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    setEditingDescription(true);
                  }
                }}
              >
                {description.trim()
                  ? <KanbanRichText value={description} attachments={attachments} />
                  : <button type="button" onClick={() => setEditingDescription(true)}>Add a description</button>}
              </div>
            )}
          </section>

          <section className="kanban-detail-section">
            <h3>Links &amp; files</h3>
            <label
              className={`kanban-upload${uploading ? " is-uploading" : ""}${attachmentMutationsEnabled ? "" : " is-disabled"}`}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => {
                event.preventDefault();
                if (!attachmentMutationsEnabled) return;
                void onUploadFiles(Array.from(event.dataTransfer.files));
              }}
            >
              <ModalIcon name="upload" />
              <span>{!attachmentMutationsEnabled ? "Available when connected" : uploading ? "Uploading…" : "Paste or drop screenshots here, or choose files"}</span>
              <input aria-label="Add screenshot or file" type="file" multiple disabled={uploading || !attachmentMutationsEnabled} onChange={(event) => void onUploadFiles(Array.from(event.target.files || []))} />
            </label>
            {uploadError ? <p className="kanban-upload-error" role="alert">{uploadError}</p> : null}
            <div className="kanban-attachment-list">
			  {cardAttachments.map((attachment) => (
                <AttachmentActions
                  key={attachment.id}
                  attachment={attachment}
                  repository={attachmentRepository}
                  noteLocalKey={attachmentNoteLocalKey}
                  online={attachmentOnline}
                />
              ))}
            </div>
          </section>
        </div>

      </article>
    </div>
  );
}
