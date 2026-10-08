import { SidebarDividerToggle } from "../ui/SidebarDividerToggle";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  type KanbanBoard,
  type NoteAttachment,
  noteID,
  profileNotesClient,
  type ProfileNote,
  type ProfileNoteSummary,
} from "../api/profileNotes";
import { ApiError } from "../api/client";
import {
  mergeDefaultKanbanBoard,
  profileSettingsClient,
  type ProfileSettingsResponse,
} from "../api/profileSettings";
import { useConfirmDialog, useToast } from "../ui/primitives";
import { boardFromMarkdown, boardToMarkdown, KanbanEditor } from "./KanbanEditor";
import { htmlToMarkdown, markdownToHTML } from "./richTextMarkdown";
import { AttachmentActions } from "./AttachmentActions";
import { useOfflineNotes } from "../offlineNotes/OfflineNotesProvider";
import { usePWA } from "../pwa/PWAProvider";
import type { OfflineNoteRecord } from "../offlineNotes/types";

type Editor = {
  instanceKey: string;
  noteID: string;
  title: string;
  body: string;
  revision: string;
  pageType: string;
  parentID: string;
  pinned: boolean;
  mcpExcluded: boolean;
  board: KanbanBoard | null;
  attachments: NoteAttachment[];
  updatedAt: string;
  shared: boolean;
};

function attachmentForMarkdownTarget(target: string, attachments: NoteAttachment[]): NoteAttachment | undefined {
  const match = /^hank-note-attachment:\/\/([^?#]+)/i.exec(target.trim());
  if (!match) return undefined;
  try {
    const attachmentID = decodeURIComponent(match[1]);
    return attachments.find((attachment) => attachment.id === attachmentID);
  } catch {
    return undefined;
  }
}

type ReadyState = {
  status: "ready";
  notes: ProfileNoteSummary[];
  selectedID: string;
  editor: Editor;
  query: string;
  message: string;
  railOpen: boolean;
  notebookDialogOpen: boolean;
  notebookDraft: string;
  moveDialogNoteID: string;
  moveDialogTargetID: string;
  savedEditor: Editor;
  saving: boolean;
  persistence: NotePersistenceState;
};

type NotePersistenceState =
  | "saved"
  | "saved_offline"
  | "syncing"
  | "conflict"
  | "deletion_review"
  | "failed"
  | "local_save_failed";

const notePersistenceLabels: Record<NotePersistenceState, string> = {
  saved: "Saved",
  saved_offline: "Saved offline",
  syncing: "Syncing",
  conflict: "Conflict copy created",
  deletion_review: "Deletion needs review",
  failed: "Sync failed",
  local_save_failed: "Local save failed",
};

function persistenceFromRecord(record: OfflineNoteRecord | undefined): NotePersistenceState {
  if (!record || record.state === "clean") return "saved";
  if (record.state === "syncing") return "syncing";
  if (record.state === "conflicted") return "conflict";
  if (record.state === "deletion_review") return "deletion_review";
  if (record.state === "failed") return "failed";
  return "saved_offline";
}

function noteOfflineStateLabel(note: ProfileNoteSummary): string {
  if (note.offline_state === "queued") return noteID(note).startsWith("local:") ? "Local only" : "Saved offline";
  if (note.offline_state === "syncing") return "Syncing";
  if (note.offline_state === "conflicted") return "Conflict copy";
  if (note.offline_state === "deletion_review") return "Deletion review";
  if (note.offline_state === "failed") return "Sync failed";
  return "";
}

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | ReadyState;

const emptyEditor: Editor = {
  instanceKey: "empty",
  noteID: "",
  title: "",
  body: "",
  revision: "",
  pageType: "text",
  parentID: "",
  pinned: false,
  mcpExcluded: false,
  board: null,
  attachments: [],
  updatedAt: "",
  shared: false,
};

const NOTE_AUTOSAVE_DELAY_MS = 750;
const NOTE_HISTORY_GROUP_DELAY_MS = 750;
const NOTE_HISTORY_LIMIT = 50;
const RECENT_NOTE_WINDOW_MS = 12 * 60 * 60 * 1000;
const RECENT_NOTES_STORAGE_KEY = "hank.profile-notes.recently-opened";

type HistoryActionKind = "typing" | "deletion" | "paste" | "formatting";

type HistoryEntry = {
  body: string;
  action: HistoryActionKind;
};

type HistoryState = {
  past: HistoryEntry[];
  future: HistoryEntry[];
};

type PendingSave = {
  editor: Editor;
  background: boolean;
};

function loadRecentNoteVisits(now = Date.now()): Record<string, number> {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_NOTES_STORAGE_KEY) || "{}") as Record<string, unknown>;
    return Object.fromEntries(Object.entries(parsed).filter(([, visitedAt]) => (
      typeof visitedAt === "number" && visitedAt <= now && now - visitedAt <= RECENT_NOTE_WINDOW_MS
    ))) as Record<string, number>;
  } catch {
    return {};
  }
}

function storeRecentNoteVisits(visits: Record<string, number>) {
  try {
    window.localStorage.setItem(RECENT_NOTES_STORAGE_KEY, JSON.stringify(visits));
  } catch {
    // Recent navigation is an enhancement; storage restrictions must not block Notes.
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Profile notes could not be loaded.";
}

function noteTitle(note: ProfileNoteSummary): string {
  return note.title?.trim() || noteID(note) || "Untitled";
}

function isNotebook(note: ProfileNoteSummary | Editor): boolean {
  return noteKind(note) === "notebook";
}

function editorFromNote(note: ProfileNote): Editor {
  const id = noteID(note);
  return {
    instanceKey: id || "empty",
    noteID: id,
    title: note.title || "",
    body: note.body_markdown || note.content || "",
    revision: note.revision || "",
    pageType: note.page_type || "text",
    parentID: note.parent_id || "",
    pinned: Boolean(note.pinned),
    mcpExcluded: Boolean(note.mcp_excluded),
    board: note.board || null,
    attachments: note.attachments || [],
    updatedAt: note.updated_at || "",
    shared: Boolean(note.shared),
  };
}

function editorChanged(left: Editor, right: Editor): boolean {
  return left.noteID !== right.noteID
    || left.title !== right.title
    || left.body !== right.body
    || left.revision !== right.revision
    || left.pageType !== right.pageType
    || left.parentID !== right.parentID
    || left.pinned !== right.pinned
    || left.mcpExcluded !== right.mcpExcluded
    || JSON.stringify(left.board) !== JSON.stringify(right.board);
}

function sortNotes(notes: ProfileNoteSummary[]): ProfileNoteSummary[] {
  return [...notes].sort((left, right) => String(right.updated_at || "").localeCompare(String(left.updated_at || "")));
}

function noteKind(note: ProfileNoteSummary | Editor): string {
  const pageType = "pageType" in note ? note.pageType : note.page_type;
  if (pageType === "notebook") return "notebook";
  if (pageType === "kanban") return "kanban";
  return "text";
}

function noteTag(note: ProfileNoteSummary): string {
  const title = noteTitle(note).toLowerCase();
  if (note.page_type === "kanban") return "board";
  if (note.page_type === "notebook") return "notebook";
  if (title.includes("grocery")) return "shopping";
  if (title.includes("wifi") || title.includes("password")) return "private";
  if (title.includes("trip")) return "travel";
  return "note";
}

function notebooksFrom(notes: ProfileNoteSummary[]): ProfileNoteSummary[] {
  return notes
    .filter(isNotebook)
    .sort((left, right) => noteTitle(left).localeCompare(noteTitle(right)));
}

function notebookTitle(notes: ProfileNoteSummary[], notebookID?: string): string {
  if (!notebookID) return "";
  const notebook = notes.find((note) => noteID(note) === notebookID);
  return notebook ? noteTitle(notebook) : "";
}

function notebookChildCount(notes: ProfileNoteSummary[], notebookID: string): number {
  return notes.filter((note) => note.parent_id === notebookID).length;
}

function noteOwnMcpExcluded(note: ProfileNoteSummary | Editor): boolean {
  return "mcpExcluded" in note ? note.mcpExcluded : Boolean(note.mcp_excluded);
}

function parentNotebook(notes: ProfileNoteSummary[], notebookID?: string): ProfileNoteSummary | undefined {
  if (!notebookID) return undefined;
  return notes.find((note) => noteID(note) === notebookID);
}

function noteInheritedMcpExcluded(note: ProfileNoteSummary | Editor, notes: ProfileNoteSummary[]): boolean {
  if (isNotebook(note) || noteOwnMcpExcluded(note)) return false;
  const notebookID = "parentID" in note ? note.parentID : note.parent_id;
  return Boolean(parentNotebook(notes, notebookID)?.mcp_excluded);
}

function noteEffectiveMcpExcluded(note: ProfileNoteSummary | Editor, notes: ProfileNoteSummary[]): boolean {
  return noteOwnMcpExcluded(note) || noteInheritedMcpExcluded(note, notes);
}

function activeNotebookForNewNote(state: ReadyState): string {
  if (state.editor.pageType === "notebook" && state.editor.noteID) return state.editor.noteID;
  return "";
}

function noteMatchesQuery(note: ProfileNoteSummary, notes: ProfileNoteSummary[], query: string): boolean {
  if (!query) return true;
  return [note.title, note.preview, note.note_id, note.parent_id, notebookTitle(notes, note.parent_id)]
    .filter(Boolean)
    .join(" ")
    .toLowerCase()
    .includes(query);
}

function noteIconName(note: ProfileNoteSummary | Editor): string {
  const kind = noteKind(note);
  if (kind === "notebook") return "book";
  if (kind === "kanban") return "kanban";
  return "note";
}

function updatedLabel(note?: ProfileNoteSummary): string {
  if (!note?.updated_at) return "No timestamp";
  const date = new Date(note.updated_at);
  if (Number.isNaN(date.getTime())) return "No timestamp";
  const minutes = Math.max(1, Math.round((Date.now() - date.getTime()) / 60000));
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/* Note rows hide their actions off-canvas; touch users swipe them in, mouse users hover. */
function finePointer(): boolean {
  return Boolean(window.matchMedia?.("(hover: hover) and (pointer: fine)").matches);
}

type BodyMutation = { body: string; selectionStart: number; selectionEnd: number };

export function wrapSelection(body: string, start: number, end: number, prefix: string, suffix: string, placeholder: string): BodyMutation {
  const selected = body.slice(start, end) || placeholder;
  const next = `${body.slice(0, start)}${prefix}${selected}${suffix}${body.slice(end)}`;
  return {
    body: next,
    selectionStart: start + prefix.length,
    selectionEnd: start + prefix.length + selected.length,
  };
}

export function stripLinePrefix(line: string): string {
  return line.replace(/^\s*(#{1,6}\s+|[-*]\s+|\d+\.\s+)/, "");
}

export function prefixLines(body: string, start: number, end: number, prefixFor: (line: string, index: number) => string): BodyMutation {
  const blockStart = body.lastIndexOf("\n", Math.max(0, start - 1)) + 1;
  const newlineAfter = body.indexOf("\n", end);
  const blockEnd = newlineAfter === -1 ? body.length : newlineAfter;
  const block = body.slice(blockStart, blockEnd);
  const nextBlock = block.split("\n").map((line, index) => prefixFor(line, index)).join("\n");
  return {
    body: `${body.slice(0, blockStart)}${nextBlock}${body.slice(blockEnd)}`,
    selectionStart: blockStart,
    selectionEnd: blockStart + nextBlock.length,
  };
}

function Icon({ name }: { name: string }) {
  const common = {
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.8,
  };
  return (
    <svg className="ui-icon" viewBox="0 0 24 24" aria-hidden="true">
      {name === "panel" ? <><rect x="4" y="4" width="16" height="16" rx="3" {...common} /><path d="M9 4v16" {...common} /></> : null}
      {name === "plus" ? <><path d="M12 5v14M5 12h14" {...common} /></> : null}
      {name === "book-plus" ? <><path d="M5 5.5A2.5 2.5 0 0 1 7.5 3H19v15H7.5A2.5 2.5 0 0 0 5 20.5z" {...common} /><path d="M9 7h5M15 10v5M12.5 12.5h5" {...common} /></> : null}
      {name === "search" ? <><circle cx="11" cy="11" r="6.5" {...common} /><path d="m16.5 16.5 3.5 3.5" {...common} /></> : null}
      {name === "note" ? <><path d="M7 4h10l2 2v14H7z" {...common} /><path d="M10 9h6M10 13h6M10 17h4" {...common} /></> : null}
      {name === "kanban" ? <><rect x="4" y="5" width="16" height="14" rx="2" {...common} /><path d="M9 5v14M15 5v14" {...common} /></> : null}
      {name === "book" ? <><path d="M5 5.5A2.5 2.5 0 0 1 7.5 3H19v15H7.5A2.5 2.5 0 0 0 5 20.5z" {...common} /><path d="M8 7h7M8 11h7" {...common} /></> : null}
      {name === "pin" ? <><path d="m9 4 6 6M8 9l7 7M14 5l5 5-3 1-4 4-1 3-5-5 3-1 4-4zM8 16l-4 4" {...common} /></> : null}
      {name === "trash" ? <><path d="M5 7h14M9 7V5h6v2M8 10v8M12 10v8M16 10v8" {...common} /></> : null}
      {name === "undo" ? <><path d="M9 7 5 11l4 4" {...common} /><path d="M5 11h8a5 5 0 0 1 5 5v1" {...common} /></> : null}
      {name === "redo" ? <><path d="m15 7 4 4-4 4" {...common} /><path d="M19 11h-8a5 5 0 0 0-5 5v1" {...common} /></> : null}
      {name === "list" ? <><path d="M8 7h12M8 12h12M8 17h12" {...common} /><path d="M4 7h.01M4 12h.01M4 17h.01" {...common} /></> : null}
      {name === "ordered" ? <><path d="M10 7h10M10 12h10M10 17h10" {...common} /><path d="M4 6h1v3M4 17h3M4 14h2a1 1 0 0 1 0 2H4" {...common} /></> : null}
      {name === "tag" ? <><path d="M4 12V5h7l9 9-6 6z" {...common} /><circle cx="8" cy="8" r="1" fill="currentColor" /></> : null}
      {name === "link" ? <><path d="M10 13a4 4 0 0 0 5.5.5l2-2a4 4 0 0 0-5.5-5.5l-1 1" {...common} /><path d="M14 11a4 4 0 0 0-5.5-.5l-2 2a4 4 0 0 0 5.5 5.5l1-1" {...common} /></> : null}
      {name === "lock" ? <><rect x="6" y="11" width="12" height="9" rx="2" {...common} /><path d="M9 11V8a3 3 0 0 1 6 0v3" {...common} /></> : null}
      {name === "unlock" ? <><rect x="6" y="11" width="12" height="9" rx="2" {...common} /><path d="M15 11V8a3 3 0 1 0-6 0" {...common} /></> : null}
      {name === "x" ? <><path d="M7 7l10 10M17 7 7 17" {...common} /></> : null}
      {name === "check" ? <path d="m5 12 4 4L19 6" {...common} /> : null}
    </svg>
  );
}

export function ProfileNotesPage() {
  const offlineNotes = useOfflineNotes();
  const { online } = usePWA();
  const repository = offlineNotes?.repository ?? null;
  const attachmentMutationsEnabled = online && (!repository || offlineNotes?.mode === "online");
  const [state, setState] = useState<State>({ status: "loading" });
  const [mobilePane, setMobilePane] = useState<"browser" | "editor">("browser");
  const [moreFormattingOpen, setMoreFormattingOpen] = useState(false);
  const [profileSettings, setProfileSettings] = useState<ProfileSettingsResponse>({ revision: 0, settings: {} });
  const [history, setHistory] = useState<HistoryState>({ past: [], future: [] });
  const [recentNoteVisits, setRecentNoteVisits] = useState<Record<string, number>>(() => loadRecentNoteVisits());
  const [trash, setTrash] = useState<{ open: boolean; loading: boolean; notes: ProfileNoteSummary[] }>({ open: false, loading: false, notes: [] });
  const bodyInputRef = useRef<HTMLDivElement>(null);
  const lastRenderedBodyRef = useRef("");
  const lastHistoryActionRef = useRef<{ action: HistoryActionKind; at: number } | null>(null);
  const richCommandPendingRef = useRef(false);
  const pendingSelectionRef = useRef<{ start: number; end: number } | null>(null);
  const savingRef = useRef(false);
  const deletingNoteIDsRef = useRef(new Set<string>());
  const activeSaveRef = useRef<PendingSave | null>(null);
  const pendingSaveRef = useRef<PendingSave[]>([]);
  const autosaveTimerRef = useRef<number | null>(null);
  const latestEditorRef = useRef<Editor>(emptyEditor);
  const latestSavedEditorRef = useRef<Editor>(emptyEditor);
  const draftSequenceRef = useRef(0);
  const rowRevealTimersRef = useRef(new Map<HTMLDivElement, number>());

  function cancelRowReveal(row: HTMLDivElement) {
    const timer = rowRevealTimersRef.current.get(row);
    if (timer !== undefined) window.clearTimeout(timer);
    rowRevealTimersRef.current.delete(row);
  }

  function cancelAllRowReveals() {
    for (const timer of rowRevealTimersRef.current.values()) window.clearTimeout(timer);
    rowRevealTimersRef.current.clear();
  }

  function scheduleRowReveal(row: HTMLDivElement) {
    if (!finePointer()) return;
    cancelRowReveal(row);
    const timer = window.setTimeout(() => {
      rowRevealTimersRef.current.delete(row);
      const reducedMotion = Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);
      row.scrollTo({ left: row.scrollWidth, behavior: reducedMotion ? "auto" : "smooth" });
    }, 1000);
    rowRevealTimersRef.current.set(row, timer);
  }

  // Restore the caret/selection after a toolbar action once React has committed the new body.
  useEffect(() => {
    const pending = pendingSelectionRef.current;
    const node = bodyInputRef.current;
    if (!pending || !node) return;
    pendingSelectionRef.current = null;
    node.focus();
  });

  useEffect(() => {
    if (state.status !== "ready") return;
    latestEditorRef.current = state.editor;
    latestSavedEditorRef.current = state.savedEditor;
    if (state.editor.pageType !== "text") {
      // Non-text pages unmount the editable body. Reset the render cache so a
      // later text-page mount is painted even when its Markdown is unchanged.
      lastRenderedBodyRef.current = "";
      return;
    }
    const node = bodyInputRef.current;
    if (!node || lastRenderedBodyRef.current === state.editor.body) return;
		node.innerHTML = markdownToHTML(state.editor.body, {
			resolveImage: (target) => {
				const attachment = attachmentForMarkdownTarget(target, state.editor.attachments);
				return attachment?.preview_url || attachment?.download_url || "";
			},
			resolveLink: (target) => {
				const attachment = attachmentForMarkdownTarget(target, state.editor.attachments);
				return attachment?.preview_url || attachment?.download_url || "";
			},
		});
    lastRenderedBodyRef.current = state.editor.body;
  }, [state]);

  useEffect(() => () => {
    if (autosaveTimerRef.current !== null) window.clearTimeout(autosaveTimerRef.current);
    cancelAllRowReveals();
    const editor = latestEditorRef.current;
    if (editorHasSavableContent(editor) && editorChanged(editor, latestSavedEditorRef.current)) {
      void saveNote(editor, true);
    }
  }, []);
  const dialog = useConfirmDialog();
  const { showToast } = useToast();

  async function load(message = "") {
    try {
      const [payload, settings] = repository
        ? await Promise.all([
            repository.listNotes().then((notes) => ({ notes })),
            repository.getDefaultBoardLocalKey().then((defaultBoardLocalKey) => ({
              revision: 0,
              settings: defaultBoardLocalKey ? { kanban_default_board_id: defaultBoardLocalKey } : {},
            })),
          ])
        : await Promise.all([profileNotesClient.listNotes(), profileSettingsClient.load()]);
      setProfileSettings({ revision: settings.revision || 0, settings: settings.settings || {} });
      const notes = sortNotes(payload.notes || []);
      const currentSelected = state.status === "ready" ? state.selectedID : "";
      const requestedID = new URLSearchParams(window.location.search).get("note") || "";
      const requestedLocalKey = requestedID && repository
        ? (await repository.getNoteRecord(requestedID))?.localKey ?? requestedID
        : requestedID;
      const selectedID = requestedLocalKey && notes.some((note) => noteID(note) === requestedLocalKey)
        ? requestedLocalKey
        : currentSelected && notes.some((note) => noteID(note) === currentSelected)
        ? currentSelected
        : noteID(notes[0] || {});
      const editor = selectedID
        ? editorFromNote(await (repository ? repository.fetchNote(selectedID) : profileNotesClient.fetchNote(selectedID)))
        : emptyEditor;
      const persistence = selectedID && repository
        ? persistenceFromRecord(await repository.getNoteRecord(selectedID))
        : "saved";
      setState((current) => ({
        status: "ready",
        notes,
        selectedID,
        editor,
        query: current.status === "ready" ? current.query : "",
        message,
        railOpen: current.status === "ready" ? current.railOpen : true,
        notebookDialogOpen: current.status === "ready" ? current.notebookDialogOpen : false,
        notebookDraft: current.status === "ready" ? current.notebookDraft : "",
        moveDialogNoteID: current.status === "ready" ? current.moveDialogNoteID : "",
        moveDialogTargetID: current.status === "ready" ? current.moveDialogTargetID : "",
        savedEditor: editor,
        saving: false,
        persistence,
      }));
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => {
    void load();
    // Initial load only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repository]);

  useEffect(() => {
    if (!repository) return;
    return repository.subscribe(() => {
      void (async () => {
        const selectedID = latestEditorRef.current.noteID;
        const [notes, defaultBoardLocalKey, record] = await Promise.all([
          repository.listNotes(),
          repository.getDefaultBoardLocalKey(),
          selectedID ? repository.getNoteRecord(selectedID) : Promise.resolve(undefined),
        ]);
        const canonicalEditor = selectedID && (record?.state === "clean" || record?.state === "deletion_review")
          ? editorFromNote(await repository.fetchNote(selectedID))
          : null;
        setProfileSettings((current) => ({
          ...current,
          settings: mergeDefaultKanbanBoard(current.settings, defaultBoardLocalKey),
        }));
        setState((current) => {
          if (current.status !== "ready") return current;
          const selectedEditor = current.editor.noteID === selectedID;
          const canRefreshEditor = selectedEditor
            && canonicalEditor
            && !editorChanged(current.editor, current.savedEditor);
          const refreshedEditor = canRefreshEditor
            ? { ...canonicalEditor, instanceKey: current.editor.instanceKey }
            : current.editor;
          return {
            ...current,
            notes: sortNotes(notes),
            editor: refreshedEditor,
            savedEditor: canRefreshEditor ? refreshedEditor : current.savedEditor,
            persistence: selectedEditor ? persistenceFromRecord(record) : current.persistence,
          };
        });
      })();
    });
  }, [repository]);

  async function saveDefaultBoard(boardID: string) {
    if (repository) {
      try {
        await repository.setDefaultBoard(boardID);
        setProfileSettings((current) => ({
          ...current,
          settings: mergeDefaultKanbanBoard(current.settings, boardID),
        }));
      } catch (error) {
        showToast(errorMessage(error), "error");
      }
      return;
    }
    let current = profileSettings;
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const settings = mergeDefaultKanbanBoard(current.settings, boardID);
      try {
        const saved = await profileSettingsClient.save(current.revision, settings);
        setProfileSettings({ revision: saved.revision || current.revision, settings: saved.settings || settings });
        return;
      } catch (error) {
        if (attempt === 0 && error instanceof ApiError && error.status === 409) {
          const latest = await profileSettingsClient.load();
          current = { revision: latest.revision || 0, settings: latest.settings || {} };
          continue;
        }
        showToast(errorMessage(error), "error");
        return;
      }
    }
  }

  const navigationNotes = useMemo(() => {
    if (state.status !== "ready") return { primary: [], recent: [] };
    const query = state.query.trim().toLowerCase();
    const primary = state.notes.filter((note) => !note.parent_id && !isNotebook(note));
    let recent: ProfileNoteSummary[] = [];
    const cutoff = Date.now() - RECENT_NOTE_WINDOW_MS;
    recent = state.notes
      .filter((note) => Boolean(note.parent_id) && !isNotebook(note) && (recentNoteVisits[noteID(note)] || 0) >= cutoff)
      .sort((left, right) => (recentNoteVisits[noteID(right)] || 0) - (recentNoteVisits[noteID(left)] || 0));
    return {
      primary: primary.filter((note) => noteMatchesQuery(note, state.notes, query)),
      recent: recent.filter((note) => noteMatchesQuery(note, state.notes, query)),
    };
  }, [recentNoteVisits, state]);
  const visibleNotes = [...navigationNotes.primary, ...navigationNotes.recent];

  if (state.status === "loading") {
    return (
      <section className="dashboard-page notes-guide-page" aria-labelledby="route-title">
        <h1 id="route-title">Loading notes</h1>
        <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading notes...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="dashboard-page notes-guide-page" aria-labelledby="route-title">
        <h1 id="route-title">Notes</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const readyState = state;
  const selectedSummary = readyState.notes.find((note) => noteID(note) === readyState.selectedID);
  const notebookItems = notebooksFrom(readyState.notes);
  const notebookNoteCount = notebookChildCount(readyState.notes, readyState.editor.noteID);
  const visibleNotebookItems = notebookItems.filter((note) => noteMatchesQuery(note, readyState.notes, readyState.query.trim().toLowerCase()));
  const editorInheritedExclusion = noteInheritedMcpExcluded(readyState.editor, readyState.notes);
  const editorEffectiveExclusion = noteEffectiveMcpExcluded(readyState.editor, readyState.notes);

  function setReady(next: Partial<ReadyState>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  function clearAutosaveTimer() {
    if (autosaveTimerRef.current === null) return;
    window.clearTimeout(autosaveTimerRef.current);
    autosaveTimerRef.current = null;
  }

  function editorHasSavableContent(editor: Editor): boolean {
    return Boolean(editor.noteID || editor.title.trim() || editor.body.trim() || editor.pageType !== "text");
  }

  function scheduleAutosave(editor: Editor) {
    latestEditorRef.current = editor;
    clearAutosaveTimer();
    if (!editorHasSavableContent(editor) || !editorChanged(editor, readyState.savedEditor)) return;
    autosaveTimerRef.current = window.setTimeout(() => {
      autosaveTimerRef.current = null;
      void saveNote(latestEditorRef.current, true);
    }, NOTE_AUTOSAVE_DELAY_MS);
  }

  function updateEditor(editor: Editor, autosave = true) {
    latestEditorRef.current = editor;
    setReady({ editor });
    if (autosave) scheduleAutosave(editor);
  }

  function flushAutosave() {
    clearAutosaveTimer();
    const editor = latestEditorRef.current;
    if (!editorHasSavableContent(editor) || !editorChanged(editor, readyState.savedEditor)) return;
    void saveNote(editor, true);
  }

  function newDraftEditor(parentID = ""): Editor {
    draftSequenceRef.current++;
    return { ...emptyEditor, instanceKey: `draft-${draftSequenceRef.current}`, parentID };
  }

  function resetHistory() {
    setHistory({ past: [], future: [] });
    lastHistoryActionRef.current = null;
  }

  function rememberRecentNote(note: ProfileNoteSummary) {
    const id = noteID(note);
    if (!id || isNotebook(note)) return;
    const now = Date.now();
    setRecentNoteVisits((current) => {
      const cutoff = now - RECENT_NOTE_WINDOW_MS;
      const next = Object.fromEntries(Object.entries(current).filter(([, visitedAt]) => visitedAt >= cutoff));
      next[id] = now;
      storeRecentNoteVisits(next);
      return next;
    });
  }

  async function selectNote(id: string) {
    flushAutosave();
    try {
      const note = await (repository ? repository.fetchNote(id) : profileNotesClient.fetchNote(id));
      rememberRecentNote(note);
      resetHistory();
      const editor = editorFromNote(note);
      latestEditorRef.current = editor;
      setReady({ selectedID: id, editor, savedEditor: editor, message: "" });
      setMobilePane("editor");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  function newNote() {
    flushAutosave();
    const parentID = activeNotebookForNewNote(readyState);
    resetHistory();
    const editor = newDraftEditor(parentID);
    latestEditorRef.current = editor;
    setReady({
      selectedID: "",
      editor,
      savedEditor: { ...emptyEditor, instanceKey: editor.instanceKey },
      message: "",
    });
    setMobilePane("editor");
  }

  function newNoteInNotebook(parentID: string) {
    flushAutosave();
    resetHistory();
    const editor = newDraftEditor(parentID);
    latestEditorRef.current = editor;
    setReady({
      selectedID: "",
      editor,
      savedEditor: { ...emptyEditor, instanceKey: editor.instanceKey },
      message: "",
    });
    setMobilePane("editor");
  }

  function openNotebookDialog() {
    setReady({ notebookDialogOpen: true, notebookDraft: "" });
  }

  async function createNotebook() {
    flushAutosave();
    const title = readyState.notebookDraft.trim() || "Untitled notebook";
    try {
      const input = {
        note_id: "",
        title,
        body_markdown: "",
        expected_revision: "",
        page_type: "notebook",
        parent_id: "",
        pinned: false,
        mcp_excluded: false,
      };
      const localResult = repository ? await repository.saveNote(input) : null;
      const response = localResult
        ? { note_id: localResult.noteID, revision: localResult.revision, updated_at: new Date().toISOString() }
        : await profileNotesClient.saveNote(input);
      const savedID = localResult?.localKey || response.note_id;
      const updatedAt = response.updated_at;
      const summary: ProfileNoteSummary = {
        note_id: savedID,
        title,
        preview: "Notebook",
        revision: response.revision,
        updated_at: updatedAt,
        page_type: "notebook",
        pinned: false,
        mcp_excluded: false,
      };
      setReady({
        notes: sortNotes([summary, ...readyState.notes.filter((note) => noteID(note) !== savedID)]),
        selectedID: savedID,
        editor: { ...emptyEditor, instanceKey: savedID, noteID: savedID, title, revision: response.revision || "", pageType: "notebook", updatedAt: updatedAt || "" },
        savedEditor: { ...emptyEditor, instanceKey: savedID, noteID: savedID, title, revision: response.revision || "", pageType: "notebook", updatedAt: updatedAt || "" },
        notebookDialogOpen: false,
        notebookDraft: "",
        message: "Notebook created.",
        persistence: localResult ? "saved_offline" : "saved",
      });
      setMobilePane("editor");
      showToast("Notebook created.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  function recordBodyChange(previousBody: string, action: HistoryActionKind) {
    const now = Date.now();
    const lastAction = lastHistoryActionRef.current;
    const canCoalesce = (action === "typing" || action === "deletion")
      && lastAction?.action === action
      && now - lastAction.at < NOTE_HISTORY_GROUP_DELAY_MS;
    setHistory((current) => {
      if (canCoalesce && current.past.length) {
        return current.future.length ? { past: current.past, future: [] } : current;
      }
      return {
        past: [...current.past, { body: previousBody, action }].slice(-NOTE_HISTORY_LIMIT),
        future: [],
      };
    });
    lastHistoryActionRef.current = { action, at: now };
  }

  function syncBodyFromEditor(action: HistoryActionKind) {
    const element = bodyInputRef.current;
    if (!element) return;
    const nextBody = htmlToMarkdown(element);
    if (nextBody === readyState.editor.body) return;
    recordBodyChange(readyState.editor.body, action);
    lastRenderedBodyRef.current = nextBody;
    updateEditor({ ...readyState.editor, body: nextBody });
  }

  function inputActionKind(inputType: string): HistoryActionKind {
    if (inputType.startsWith("delete")) return "deletion";
    if (inputType === "insertFromPaste" || inputType === "insertFromDrop") return "paste";
    if (inputType.startsWith("format")) return "formatting";
    return "typing";
  }

  function syncInputFromEditor(inputType: string) {
    const action = richCommandPendingRef.current ? "formatting" : inputActionKind(inputType || "insertText");
    richCommandPendingRef.current = false;
    syncBodyFromEditor(action);
  }

  function undoBody() {
    if (!history.past.length) return;
    const previous = history.past[history.past.length - 1];
    setHistory({
      past: history.past.slice(0, -1),
      future: [{ body: readyState.editor.body, action: previous.action }, ...history.future].slice(0, NOTE_HISTORY_LIMIT),
    });
    lastHistoryActionRef.current = null;
    updateEditor({ ...readyState.editor, body: previous.body });
  }

  function redoBody() {
    if (!history.future.length) return;
    const next = history.future[0];
    setHistory({
      past: [...history.past, { body: readyState.editor.body, action: next.action }].slice(-NOTE_HISTORY_LIMIT),
      future: history.future.slice(1),
    });
    lastHistoryActionRef.current = null;
    updateEditor({ ...readyState.editor, body: next.body });
  }

  function mutateBody(mutator: (body: string, start: number, end: number) => { body: string; selectionStart: number; selectionEnd: number }) {
    const body = readyState.editor.body;
    const start = body.length;
    const end = body.length;
    const result = mutator(body, start, end);
    recordBodyChange(body, "formatting");
    pendingSelectionRef.current = { start: result.selectionStart, end: result.selectionEnd };
    updateEditor({ ...readyState.editor, body: result.body });
  }

  function selectionInEditor(): boolean {
    const element = bodyInputRef.current;
    const selection = window.getSelection?.();
    if (!element || !selection || !selection.rangeCount) return false;
    const range = selection.getRangeAt(0);
    return element.contains(range.commonAncestorContainer);
  }

  function runRichCommand(command: string, value?: string): boolean {
    const element = bodyInputRef.current;
    if (!element || typeof document.execCommand !== "function") return false;
    element.focus();
    // If the selection lives outside the note, park the caret at the end so the
    // command always applies to the note instead of silently doing nothing.
    if (!selectionInEditor()) {
      const selection = window.getSelection?.();
      if (selection) {
        const range = document.createRange();
        range.selectNodeContents(element);
        range.collapse(false);
        selection.removeAllRanges();
        selection.addRange(range);
      }
    }
    let effectiveCommand = command;
    let effectiveValue = value;
    if (command === "createLink" && (window.getSelection?.()?.isCollapsed ?? true)) {
      // createLink needs a selection; with a bare caret insert a ready-made link instead.
      effectiveCommand = "insertHTML";
      effectiveValue = `<a href="${value}" rel="noreferrer">Link</a>`;
    }
    if (command === "formatBlock" && typeof document.queryCommandValue === "function") {
      const currentBlock = String(document.queryCommandValue("formatBlock") || "").toLowerCase();
      if (currentBlock === value) effectiveValue = "p";
    }
    richCommandPendingRef.current = true;
    document.execCommand(effectiveCommand, false, effectiveValue);
    // Browsers normally emit an input event for execCommand. The fallback keeps
    // the editor in sync in environments that do not, without recording twice.
    if (richCommandPendingRef.current) {
      richCommandPendingRef.current = false;
      syncBodyFromEditor("formatting");
    }
    return true;
  }

  function applyEditorAction(action: string) {
    if (action === "kanban") {
      const board = readyState.editor.board || boardFromMarkdown(readyState.editor.body);
      updateEditor({
        ...readyState.editor,
        pageType: "kanban",
        board,
        body: boardToMarkdown(readyState.editor.title, board),
      });
      return;
    }
    if (action === "text") {
      updateEditor({
        ...readyState.editor,
        pageType: "text",
        body: readyState.editor.board ? boardToMarkdown(readyState.editor.title, readyState.editor.board) : readyState.editor.body,
        board: null,
      });
      return;
    }
    if (action === "undo") { undoBody(); return; }
    if (action === "redo") { redoBody(); return; }
    const wrappers: Record<string, { prefix: string; suffix: string; placeholder: string }> = {
      bold: { prefix: "**", suffix: "**", placeholder: "bold text" },
      italic: { prefix: "_", suffix: "_", placeholder: "italic text" },
      underline: { prefix: "<u>", suffix: "</u>", placeholder: "underlined text" },
      tag: { prefix: "#", suffix: "", placeholder: "tag" },
    };
    const headingLevels: Record<string, string> = { heading: "# ", large: "## ", small: "### " };
    if (wrappers[action]) {
      const commandForAction: Record<string, string> = { bold: "bold", italic: "italic", underline: "underline" };
      if (commandForAction[action] && runRichCommand(commandForAction[action])) return;
      const { prefix, suffix, placeholder } = wrappers[action];
      mutateBody((body, start, end) => wrapSelection(body, start, end, prefix, suffix, placeholder));
      return;
    }
    if (headingLevels[action]) {
      const tagForAction: Record<string, string> = { heading: "h1", large: "h2", small: "h3" };
      if (runRichCommand("formatBlock", tagForAction[action])) return;
      mutateBody((body, start, end) => prefixLines(body, start, end, (line) => `${headingLevels[action]}${stripLinePrefix(line)}`));
      return;
    }
    if (action === "bullets") {
      if (runRichCommand("insertUnorderedList")) return;
      mutateBody((body, start, end) => prefixLines(body, start, end, (line) => `- ${stripLinePrefix(line)}`));
      return;
    }
    if (action === "numbers") {
      if (runRichCommand("insertOrderedList")) return;
      mutateBody((body, start, end) => prefixLines(body, start, end, (line, index) => `${index + 1}. ${stripLinePrefix(line)}`));
      return;
    }
    if (action === "link") {
      if (runRichCommand("createLink", "https://example.com")) return;
      mutateBody((body, start, end) => {
        const selected = body.slice(start, end) || "Link";
        const url = "https://example.com";
        const next = `${body.slice(0, start)}[${selected}](${url})${body.slice(end)}`;
        const urlStart = start + selected.length + 3;
        return { body: next, selectionStart: urlStart, selectionEnd: urlStart + url.length };
      });
    }
  }

  async function saveNote(editor = readyState.editor, background = false): Promise<Editor | null> {
    if (deletingNoteIDsRef.current.has(editor.noteID)) return null;
    if (!background) clearAutosaveTimer();
    if (savingRef.current) {
      const active = activeSaveRef.current;
      if (active?.editor.instanceKey === editor.instanceKey && !editorChanged(active.editor, editor)) return null;
      const pendingIndex = pendingSaveRef.current.findIndex((pending) => pending.editor.instanceKey === editor.instanceKey);
      const pending = pendingIndex >= 0 ? pendingSaveRef.current[pendingIndex] : null;
      const nextPending = {
        editor,
        background: pending?.background === false ? false : background,
      };
      if (pendingIndex >= 0) pendingSaveRef.current[pendingIndex] = nextPending;
      else pendingSaveRef.current.push(nextPending);
      return null;
    }
    savingRef.current = true;
    activeSaveRef.current = { editor, background };
    setReady({ saving: true });
    let queuedSave: PendingSave | null = null;
    let savedResult: Editor | null = null;
    try {
      const input = {
        note_id: editor.noteID,
        title: editor.title.trim() || "Untitled",
        body_markdown: editor.body,
        expected_revision: editor.revision,
        page_type: editor.pageType,
        parent_id: editor.parentID,
        pinned: editor.pinned,
        mcp_excluded: editor.mcpExcluded,
        board: editor.pageType === "kanban" ? editor.board : undefined,
      };
      const localResult = repository ? await repository.saveNote(input) : null;
      const response = localResult
        ? { note_id: localResult.localKey, revision: localResult.revision, updated_at: new Date().toISOString() }
        : await profileNotesClient.saveNote(input);
      const savedEditor = {
        ...editor,
        noteID: response.note_id || editor.noteID,
        revision: response.revision || editor.revision,
        updatedAt: response.updated_at || editor.updatedAt,
      };
      savedResult = savedEditor;
      if (latestEditorRef.current.instanceKey === editor.instanceKey) {
        latestEditorRef.current = {
          ...latestEditorRef.current,
          noteID: savedEditor.noteID,
          revision: savedEditor.revision,
          updatedAt: savedEditor.updatedAt,
          pinned: savedEditor.pinned,
        };
        latestSavedEditorRef.current = savedEditor;
      }
      const savedID = savedEditor.noteID;
      setState((current) => {
        if (current.status !== "ready") return current;
        const matchesCurrentEditor = current.editor.instanceKey === editor.instanceKey;
        const nextEditor = matchesCurrentEditor
          ? { ...current.editor, noteID: savedID, revision: savedEditor.revision, updatedAt: savedEditor.updatedAt, pinned: savedEditor.pinned }
          : current.editor;
        return {
          ...current,
          selectedID: matchesCurrentEditor ? savedID : current.selectedID,
          notes: sortNotes([
            {
              note_id: savedID,
              title: editor.title.trim() || "Untitled",
              preview: editor.pageType === "notebook" ? "Notebook" : editor.pageType === "kanban" ? "Kanban board" : editor.body.replace(/\s+/g, " ").trim().slice(0, 96),
              revision: savedEditor.revision,
              updated_at: response.updated_at,
              page_type: editor.pageType,
              parent_id: editor.pageType === "notebook" ? "" : editor.parentID,
              pinned: editor.pageType === "notebook" || !editor.parentID ? false : editor.pinned,
              mcp_excluded: editor.mcpExcluded,
            },
            ...current.notes.filter((note) => noteID(note) !== savedID && noteID(note) !== editor.noteID),
          ]),
          editor: nextEditor,
          savedEditor: matchesCurrentEditor ? savedEditor : current.savedEditor,
          saving: false,
          persistence: localResult ? "saved_offline" : "saved",
          message: background ? current.message : "Note saved.",
        };
      });
      pendingSaveRef.current = pendingSaveRef.current.map((pending) => pending.editor.instanceKey === editor.instanceKey
        ? { ...pending, editor: { ...pending.editor, noteID: savedID, revision: savedEditor.revision, updatedAt: savedEditor.updatedAt, pinned: savedEditor.pinned } }
        : pending);
      queuedSave = pendingSaveRef.current.shift() || null;
      if (queuedSave && queuedSave.editor.instanceKey === editor.instanceKey) {
        queuedSave = { ...queuedSave, editor: { ...queuedSave.editor, noteID: savedID, revision: savedEditor.revision, updatedAt: savedEditor.updatedAt, pinned: savedEditor.pinned } };
      }
      if (!background) showToast("Note saved.");
    } catch (error) {
      pendingSaveRef.current = pendingSaveRef.current.filter((pending) => pending.editor.instanceKey !== editor.instanceKey);
      queuedSave = pendingSaveRef.current.shift() || null;
      setReady({ saving: false, persistence: "local_save_failed" });
      showToast(errorMessage(error), "error");
    } finally {
      activeSaveRef.current = null;
      savingRef.current = false;
      if (queuedSave) void saveNote(queuedSave.editor, queuedSave.background);
    }
    return savedResult;
  }

  async function uploadKanbanAttachment(file: File): Promise<NoteAttachment> {
    if (!attachmentMutationsEnabled) throw new Error("Available when connected");
    if (savingRef.current) {
      await new Promise<void>((resolve, reject) => {
        const startedAt = Date.now();
        const waitForSave = () => {
          if (!savingRef.current) { resolve(); return; }
          if (Date.now() - startedAt > 10_000) { reject(new Error("The board is still saving. Try the upload again.")); return; }
          window.setTimeout(waitForSave, 40);
        };
        waitForSave();
      });
    }
    let editor = latestEditorRef.current;
    if (!editor.noteID || editorChanged(editor, latestSavedEditorRef.current)) {
      const saved = await saveNote(editor, true);
      if (saved) editor = saved;
    }
    if (!editor.noteID) throw new Error("Save the board before adding a file.");
    const attachment = await profileNotesClient.uploadAttachment(editor.noteID, file);
    const attachments = [...editor.attachments.filter((item) => item.id !== attachment.id), attachment];
    const revision = attachment.note_revision || editor.revision;
    latestEditorRef.current = { ...editor, revision, attachments };
    latestSavedEditorRef.current = { ...latestSavedEditorRef.current, revision, attachments };
    setState((current) => {
      if (current.status !== "ready" || current.editor.instanceKey !== editor.instanceKey) return current;
      return {
        ...current,
        editor: { ...current.editor, revision, attachments },
        savedEditor: { ...current.savedEditor, revision, attachments },
      };
    });
    return attachment;
  }

  async function deleteKanbanItems(nextBoard: KanbanBoard, exclusiveAttachments: NoteAttachment[]): Promise<boolean> {
    clearAutosaveTimer();
    if (savingRef.current) {
      try {
        await new Promise<void>((resolve, reject) => {
          const startedAt = Date.now();
          const waitForSave = () => {
            if (!savingRef.current) { resolve(); return; }
            if (Date.now() - startedAt > 10_000) { reject(new Error("The board is still saving. Try deleting again.")); return; }
            window.setTimeout(waitForSave, 40);
          };
          waitForSave();
        });
      } catch (error) {
        showToast(errorMessage(error), "error");
        return false;
      }
    }

    const current = latestEditorRef.current;
    const nextEditor: Editor = {
      ...current,
      board: nextBoard,
      body: boardToMarkdown(current.title, nextBoard),
    };
    const saved = await saveNote(nextEditor, true);
    if (!saved) return false;

    if (!attachmentMutationsEnabled) {
      if (exclusiveAttachments.length) {
        showToast("Task saved offline. Attachment cleanup is available when connected.", "neutral");
      }
      return true;
    }

    let revision = saved.revision;
    let remaining = [...saved.attachments];
    let cleanupIncomplete = false;
    for (const attachment of exclusiveAttachments) {
      try {
        const response = await profileNotesClient.deleteAttachment(saved.noteID, attachment.id);
        revision = response.note_revision || revision;
        remaining = remaining.filter((item) => item.id !== attachment.id);
        if (!response.cleanup_complete) cleanupIncomplete = true;
      } catch {
        cleanupIncomplete = true;
      }
    }

    const finalEditor: Editor = { ...saved, revision, attachments: remaining };
    latestEditorRef.current = finalEditor;
    latestSavedEditorRef.current = finalEditor;
    setState((state) => {
      if (state.status !== "ready" || state.editor.instanceKey !== current.instanceKey) return state;
      return {
        ...state,
        editor: finalEditor,
        savedEditor: finalEditor,
        notes: state.notes.map((note) => noteID(note) === finalEditor.noteID ? { ...note, revision } : note),
        message: cleanupIncomplete ? "Task deleted, but attachment cleanup is incomplete." : state.message,
      };
    });
    if (cleanupIncomplete) {
      showToast("Task deleted, but one or more files could not be removed. Check Settings > Attachments.", "error");
    }
    return true;
  }

  async function deleteNote() {
    if (!readyState.editor.noteID) return;
    await deleteNoteByID(readyState.editor.noteID, readyState.editor.title || readyState.editor.noteID);
  }

  async function deleteNoteByID(id: string, title: string) {
    const confirmed = await dialog.confirm({
      title: "Delete note",
      message: `Move "${title}" to Trash? You can restore it later.`,
      confirmLabel: "Move to Trash",
      tone: "danger",
    });
    if (!confirmed) return;
    if (deletingNoteIDsRef.current.has(id)) return;
    deletingNoteIDsRef.current.add(id);
    if (latestEditorRef.current.noteID === id) clearAutosaveTimer();
    pendingSaveRef.current = pendingSaveRef.current.filter((pending) => pending.editor.noteID !== id);
    try {
      // A save already sent must finish before deletion so it cannot recreate
      // the note, and the delete request uses its latest revision.
      const startedAt = Date.now();
      while (activeSaveRef.current?.editor.noteID === id) {
        if (Date.now() - startedAt > 10_000) throw new Error("The note is still saving. Try deleting again.");
        await new Promise<void>((resolve) => window.setTimeout(resolve, 40));
      }
      const currentEditor = latestEditorRef.current;
      const revision = currentEditor.noteID === id ? currentEditor.revision : "";
      if (repository) await repository.deleteNote(id);
      else await profileNotesClient.deleteNote(id, revision);
      const selectedDeleted = latestEditorRef.current.noteID === id;
      if (selectedDeleted) {
        clearAutosaveTimer();
        const parentID = latestEditorRef.current.parentID;
        // Reset refs before navigating: opening the notebook must not flush the
        // deleted editor back into storage.
        const parent = readyState.notes.find((note) => noteID(note) === parentID && isNotebook(note));
        const editor = parent ? editorFromNote(parent) : { ...emptyEditor, instanceKey: "browser" };
        latestEditorRef.current = editor;
        latestSavedEditorRef.current = editor;
        resetHistory();
        setReady({ selectedID: editor.noteID, editor, savedEditor: editor, railOpen: true });
        setMobilePane(parent ? "editor" : "browser");
      }
      setState((current) => current.status === "ready" ? {
        ...current,
        notes: current.notes.filter((note) => noteID(note) !== id),
        persistence: "saved",
        message: "",
      } : current);
      showToast("Note moved to Trash.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    } finally {
      deletingNoteIDsRef.current.delete(id);
    }
  }

  async function openTrash() {
    if (!online) {
      showToast("Trash is available when Hank is online.", "neutral");
      return;
    }
    setTrash({ open: true, loading: true, notes: [] });
    try {
      const payload = await profileNotesClient.listTrash();
      setTrash({ open: true, loading: false, notes: payload.notes });
    } catch (error) {
      setTrash({ open: true, loading: false, notes: [] });
      showToast(errorMessage(error), "error");
    }
  }

  async function restoreTrashedNote(note: ProfileNoteSummary) {
    const id = noteID(note);
    try {
      await profileNotesClient.restoreNote(id);
      setTrash((current) => ({ ...current, notes: current.notes.filter((item) => noteID(item) !== id) }));
      await load("Note restored from Trash.");
      showToast("Note restored.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function permanentlyDeleteTrashedNote(note: ProfileNoteSummary) {
    const id = noteID(note);
    const confirmed = await dialog.confirm({
      title: "Delete forever",
      message: `Permanently delete "${noteTitle(note)}"? This cannot be undone.`,
      confirmLabel: "Delete forever",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      const result = await profileNotesClient.permanentlyDeleteNote(id);
      setTrash((current) => ({ ...current, notes: current.notes.filter((item) => noteID(item) !== id) }));
      showToast(result.cleanup_complete === false ? "Note deleted; attachment cleanup will finish during maintenance." : "Note permanently deleted.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  function openMoveDialog(note: ProfileNoteSummary) {
    setReady({ moveDialogNoteID: noteID(note), moveDialogTargetID: note.parent_id || "" });
  }

  async function moveNote() {
    if (!readyState.moveDialogNoteID) return;
    try {
      const note = await (repository
        ? repository.fetchNote(readyState.moveDialogNoteID)
        : profileNotesClient.fetchNote(readyState.moveDialogNoteID));
      const id = noteID(note);
      const input = {
        note_id: id,
        title: note.title?.trim() || "Untitled",
        body_markdown: note.body_markdown || note.content || "",
        expected_revision: note.revision || "",
        page_type: note.page_type || "text",
        parent_id: readyState.moveDialogTargetID,
        pinned: Boolean(readyState.moveDialogTargetID && note.pinned),
        mcp_excluded: Boolean(note.mcp_excluded),
        board: note.page_type === "kanban" ? note.board || undefined : undefined,
      };
      const localResult = repository ? await repository.saveNote(input) : null;
      const response = localResult
        ? { note_id: localResult.localKey, revision: localResult.revision, updated_at: new Date().toISOString() }
        : await profileNotesClient.saveNote(input);
      const movedID = response.note_id || id;
      const movedTitle = note.title?.trim() || "Untitled";
      setReady({
        notes: sortNotes([
          {
            note_id: movedID,
            title: movedTitle,
            preview: note.preview || (note.body_markdown || note.content || "").replace(/\s+/g, " ").trim().slice(0, 96),
            revision: response.revision || note.revision,
            updated_at: response.updated_at || note.updated_at,
            page_type: note.page_type || "text",
            parent_id: readyState.moveDialogTargetID,
            pinned: Boolean(readyState.moveDialogTargetID && note.pinned),
            mcp_excluded: Boolean(note.mcp_excluded),
          },
          ...readyState.notes.filter((summary) => noteID(summary) !== movedID),
        ]),
        editor: readyState.editor.noteID === movedID ? { ...readyState.editor, parentID: readyState.moveDialogTargetID, pinned: Boolean(readyState.moveDialogTargetID && note.pinned), revision: response.revision || readyState.editor.revision } : readyState.editor,
        savedEditor: readyState.editor.noteID === movedID ? { ...readyState.savedEditor, parentID: readyState.moveDialogTargetID, pinned: Boolean(readyState.moveDialogTargetID && note.pinned), revision: response.revision || readyState.savedEditor.revision } : readyState.savedEditor,
        moveDialogNoteID: "",
        moveDialogTargetID: "",
        message: "Note moved.",
        persistence: localResult && readyState.editor.noteID === id ? "saved_offline" : readyState.persistence,
      });
      showToast("Note moved.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function setNotePinned(summary: ProfileNoteSummary, pinned: boolean) {
    const id = noteID(summary);
    if (!id || !summary.parent_id || isNotebook(summary)) return;
    if (readyState.editor.noteID === id) {
      await saveNote({ ...readyState.editor, pinned }, true);
      return;
    }
    try {
      const note = await (repository ? repository.fetchNote(id) : profileNotesClient.fetchNote(id));
      const input = {
        note_id: id,
        title: note.title?.trim() || "Untitled",
        body_markdown: note.body_markdown || note.content || "",
        expected_revision: note.revision || "",
        page_type: note.page_type || "text",
        parent_id: note.parent_id || "",
        pinned,
        mcp_excluded: Boolean(note.mcp_excluded),
        board: note.page_type === "kanban" ? note.board || undefined : undefined,
      };
      const localResult = repository ? await repository.saveNote(input) : null;
      const response = localResult
        ? { note_id: localResult.localKey, revision: localResult.revision, updated_at: new Date().toISOString() }
        : await profileNotesClient.saveNote(input);
      setReady({
        notes: sortNotes([
          {
            ...summary,
            revision: response.revision || note.revision || summary.revision,
            updated_at: response.updated_at || note.updated_at || summary.updated_at,
            pinned,
          },
          ...readyState.notes.filter((current) => noteID(current) !== id),
        ]),
      });
      showToast(pinned ? "Note pinned." : "Note unpinned.", "neutral");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  function renderNoteRows(notes: ProfileNoteSummary[]) {
    return notes.map((note) => {
      const id = noteID(note);
      const title = noteTitle(note);
      const parentTitle = notebookTitle(readyState.notes, note.parent_id);
      const excluded = noteEffectiveMcpExcluded(note, readyState.notes);
      const offlineState = noteOfflineStateLabel(note);
      return (
        <div
          className="notes-guide-row"
          key={id}
          onMouseEnter={(event) => scheduleRowReveal(event.currentTarget)}
          onMouseLeave={(event) => {
            cancelRowReveal(event.currentTarget);
            if (!finePointer()) return;
            const reducedMotion = Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);
            event.currentTarget.scrollTo({ left: 0, behavior: reducedMotion ? "auto" : "smooth" });
          }}
          onScroll={(event) => cancelRowReveal(event.currentTarget)}
        >
          <button
            aria-label={title}
            className={`${id === readyState.selectedID ? "notes-guide-item active" : "notes-guide-item"}${excluded ? " is-mcp-excluded" : ""}`}
            onClick={() => { cancelAllRowReveals(); void selectNote(id); }}
            type="button"
          >
            <span className="notes-guide-icon" aria-hidden="true"><Icon name={noteIconName(note)} /></span>
            <span className="notes-guide-copy">
              <strong>{title}{note.pinned ? <span className="notes-pin-indicator" aria-hidden="true"><Icon name="pin" /></span> : null}{excluded ? <span className="notes-lock-indicator" aria-hidden="true"><Icon name="lock" /></span> : null}</strong>
              <span>{note.preview || "No preview"}</span>
              <span className="notes-tag-row"><em className="notes-tag">{parentTitle || noteTag(note)}</em><small>{updatedLabel(note)}</small></span>
              {offlineState ? <small className={`notes-sync-state state-${note.offline_state}`}>{offlineState}</small> : null}
            </span>
          </button>
          <div className="notes-row-actions" aria-label={`Actions for ${title}`}>
            {note.parent_id && !isNotebook(note) ? <button className="icon-button" type="button" aria-label={`${note.pinned ? "Unpin" : "Pin"} ${title}`} title={note.pinned ? "Unpin note" : "Pin note"} onClick={() => void setNotePinned(note, !note.pinned)}><Icon name="pin" /></button> : null}
            <button className="icon-button" type="button" aria-label={`Move ${title}`} title="Move note" onClick={() => openMoveDialog(note)}><Icon name="book" /></button>
            <button className="icon-button danger" type="button" aria-label={`Delete ${title}`} title="Delete note" onClick={() => void deleteNoteByID(id, title)}><Icon name="trash" /></button>
          </div>
        </div>
      );
    });
  }

  return (
    <section className="dashboard-page notes-guide-page" aria-label="Notes">
      {state.message ? <p className="notice-state">{state.message}</p> : null}

      <div
        className={`notes-guide-layout${state.railOpen ? "" : " rail-closed"}`}
        data-mobile-pane={mobilePane}
        data-testid="notes-mobile-workspace"
      >
        {state.railOpen ? (
          <aside id="notes-sidebar" className="notes-guide-rail" aria-label="Notes list">
            <label className="notes-search">
              <Icon name="search" />
              <span className="visually-hidden">Search notes</span>
              <input type="search" placeholder="Search notes" value={state.query} onChange={(event) => setReady({ query: event.target.value })} />
            </label>
            <div className="notes-create-actions" role="group" aria-label="Create note or notebook">
              <button className="notes-new-note" type="button" aria-label="New notebook" onClick={openNotebookDialog}><Icon name="plus" />New notebook</button>
              <button className="notes-new-note" type="button" aria-label="New note" onClick={newNote}><Icon name="plus" />New note</button>
            </div>
            <button className="notes-trash-button" type="button" onClick={() => void openTrash()}><Icon name="trash" />Trash</button>
            <section className="notes-notebooks-section" aria-labelledby="notebooks-heading">
              <div className="notes-section-head">
                <h2 id="notebooks-heading">Notebooks</h2>
              </div>
              {visibleNotebookItems.length ? (
                <div className="notes-notebook-list">
                  {visibleNotebookItems.map((note) => {
                    const id = noteID(note);
                    const title = noteTitle(note);
                    const childCount = notebookChildCount(state.notes, id);
                    const excluded = noteEffectiveMcpExcluded(note, state.notes);
                    const offlineState = noteOfflineStateLabel(note);
                    return (
                      <button
                        aria-label={`Open notebook ${title}`}
                        className={`${id === state.selectedID ? "notes-notebook-item active" : "notes-notebook-item"}${excluded ? " is-mcp-excluded" : ""}`}
                        key={id}
                        onClick={() => void selectNote(id)}
                        type="button"
                      >
                        <span className="notes-guide-icon" aria-hidden="true"><Icon name="book" /></span>
                        <span>
                          <strong>{title}{excluded ? <span className="notes-lock-indicator" aria-hidden="true"><Icon name="lock" /></span> : null}</strong>
                          <small>{childCount} {childCount === 1 ? "page" : "pages"}</small>
                          {offlineState ? <small className={`notes-sync-state state-${note.offline_state}`}>{offlineState}</small> : null}
                        </span>
                      </button>
                    );
                  })}
                </div>
              ) : (
                <p className="notes-notebook-empty">No notebooks yet.</p>
              )}
            </section>
            <section className="notes-navigation-section" aria-labelledby="notes-navigation-heading">
              <div className="notes-section-head">
                <h2 id="notes-navigation-heading">Recent &amp; loose notes</h2>
                <span>{visibleNotes.length}</span>
              </div>
              {visibleNotes.length ? (
                <div className="notes-guide-list" aria-label="Note cards">
                  {renderNoteRows(visibleNotes)}
                </div>
              ) : (
                <p className="empty-state" aria-label="Note cards">No notes found.</p>
              )}
            </section>
          </aside>
        ) : (
          <aside id="notes-sidebar" className="notes-rail-collapsed" aria-label="Notes rail">
            <button className="icon-button" type="button" aria-label="New notebook" title="New notebook" onClick={openNotebookDialog}><Icon name="plus" /></button>
            <button className="icon-button" type="button" aria-label="New note" title="New note" onClick={newNote}><Icon name="plus" /></button>
            {visibleNotebookItems.map((note) => {
              const id = noteID(note);
              const title = noteTitle(note);
              return <button className={id === state.selectedID ? "icon-button active" : "icon-button"} key={id} type="button" aria-label={`Open notebook ${title}`} title={title} onClick={() => void selectNote(id)}><Icon name="book" /></button>;
            })}
            {visibleNotebookItems.length && visibleNotes.length ? <span className="notes-rail-divider" aria-hidden="true" /> : null}
            {visibleNotes.map((note) => {
              const id = noteID(note);
              const title = noteTitle(note);
              return <button className={id === state.selectedID ? "icon-button active" : "icon-button"} key={id} type="button" aria-label={`Open note ${title}`} title={title} onClick={() => void selectNote(id)}><Icon name={noteIconName(note)} /></button>;
            })}
          </aside>
        )}

        <SidebarDividerToggle
          className="notes-rail-toggle"
          expanded={state.railOpen}
          controls="notes-sidebar"
          expandLabel="Expand notes rail"
          collapseLabel="Collapse notes rail"
          onToggle={() => setReady({ railOpen: !state.railOpen })}
        />

        {state.editor.instanceKey === "browser" ? (
          <section className="notes-guide-editor" aria-label="Notes browser">
            <p className="empty-state">Select a note or create a new one.</p>
          </section>
        ) : <section className="notes-guide-editor" aria-label="Note editor">
          <header className="notes-editor-header">
            <button className="notes-mobile-back" type="button" aria-label="Back to notes" onClick={() => setMobilePane("browser")}>
              <span aria-hidden="true">‹</span> Notes
            </button>
            <label className="visually-hidden" htmlFor="noteTitle">Note title</label>
            <input
              id="noteTitle"
              className="notes-title-input"
              value={state.editor.title}
              placeholder="Untitled"
              onBlur={flushAutosave}
              onChange={(event) => updateEditor({ ...state.editor, title: event.target.value })}
            />
            <button className="notes-save-pill" type="button" aria-label="Save note" onClick={() => void saveNote()}>
              <span>{state.saving
                ? "Saving…"
                : state.editor.noteID && !editorChanged(state.editor, state.savedEditor)
                  ? notePersistenceLabels[state.persistence]
                  : state.persistence === "local_save_failed"
                    ? notePersistenceLabels.local_save_failed
                    : "Unsaved"}</span>
              <small>{selectedSummary ? updatedLabel(selectedSummary) : "Not saved"}</small>
            </button>
            {state.editor.pageType === "notebook" ? (
              <small className="notes-notebook-count">{notebookNoteCount} {notebookNoteCount === 1 ? "note" : "notes"}</small>
            ) : null}
            {editorEffectiveExclusion ? (
              <p className={`notes-mcp-state${editorInheritedExclusion ? " inherited" : ""}`}>
                {editorInheritedExclusion ? "Excluded because its notebook is locked" : "Excluded from MCP"}
              </p>
            ) : null}
          </header>

          <div className="notes-toolbar" aria-label="Editor tools">
            <button className="icon-button danger" disabled={!state.editor.noteID} type="button" aria-label="Delete note" title="Delete note" onClick={() => void deleteNote()}><Icon name="trash" /></button>
            <span className="notes-toolbar-separator" aria-hidden="true" />
            {state.editor.parentID && state.editor.pageType !== "notebook" ? (
              <ToolbarButton
                label={state.editor.pinned ? "Unpin note" : "Pin note"}
                icon="pin"
                pressed={state.editor.pinned}
                onClick={() => void saveNote({ ...state.editor, pinned: !state.editor.pinned }, true)}
              />
            ) : null}
            <ToolbarButton
              label={state.editor.mcpExcluded ? "Include in MCP" : "Exclude from MCP"}
              icon={state.editor.mcpExcluded ? "unlock" : "lock"}
              pressed={state.editor.mcpExcluded}
              onClick={() => {
                const editor = { ...state.editor, mcpExcluded: !state.editor.mcpExcluded };
                updateEditor(editor, false);
                void saveNote(editor, true);
              }}
            />
            {state.editor.pageType === "notebook" ? (
              <button
                className="notebook-new-note"
                disabled={!state.editor.noteID}
                type="button"
                aria-label={`New note in ${state.editor.title || "Notebook"}`}
                onClick={() => newNoteInNotebook(state.editor.noteID)}
              >
                <Icon name="plus" />New note
              </button>
            ) : (
              <>
                <span className="notes-toolbar-separator" aria-hidden="true" />
                <ToolbarButton label="Undo" icon="undo" disabled={!history.past.length} onClick={() => applyEditorAction("undo")} />
                <ToolbarButton label="Redo" icon="redo" disabled={!history.future.length} onClick={() => applyEditorAction("redo")} />
                <button
                  className="notes-more-formatting-toggle"
                  type="button"
                  aria-expanded={moreFormattingOpen}
                  aria-controls="notes-more-formatting-controls"
                  aria-label="More formatting"
                  onClick={() => setMoreFormattingOpen((open) => !open)}
                >
                  More
                </button>
                <div id="notes-more-formatting-controls" className={`notes-more-formatting-controls${moreFormattingOpen ? " is-open" : ""}`}>
                  {state.editor.pageType === "text" ? (
                    <>
                      <span className="notes-toolbar-separator" aria-hidden="true" />
                    <ToolbarButton label="Bold" text="B" onClick={() => applyEditorAction("bold")} />
                    <ToolbarButton label="Italic" text="I" onClick={() => applyEditorAction("italic")} />
                    <ToolbarButton label="Underline" text="U" onClick={() => applyEditorAction("underline")} />
                      <ToolbarButton label="Smaller heading" text="A-" onClick={() => applyEditorAction("small")} />
                      <ToolbarButton label="Heading" text="H" onClick={() => applyEditorAction("heading")} />
                      <ToolbarButton label="Larger heading" text="A+" onClick={() => applyEditorAction("large")} />
                      <ToolbarButton label="Bulleted list" icon="list" onClick={() => applyEditorAction("bullets")} />
                      <ToolbarButton label="Numbered list" icon="ordered" onClick={() => applyEditorAction("numbers")} />
                    </>
                  ) : null}
                  <span className="notes-toolbar-separator" aria-hidden="true" />
                  <ToolbarButton label="Text page" icon="note" pressed={state.editor.pageType === "text"} onClick={() => applyEditorAction("text")} />
                  <ToolbarButton label="Kanban page" icon="kanban" pressed={state.editor.pageType === "kanban"} onClick={() => applyEditorAction("kanban")} />
                  <ToolbarButton label="Tag" icon="tag" onClick={() => applyEditorAction("tag")} />
                  <ToolbarButton label="Link" icon="link" onClick={() => applyEditorAction("link")} />
                </div>
              </>
            )}
          </div>

          <div className={`notes-content-scroll${state.editor.pageType === "kanban" ? " is-kanban" : ""}`}>
            {state.editor.pageType === "kanban" ? (
              <KanbanEditor
                board={state.editor.board || boardFromMarkdown(state.editor.body)}
                attachments={state.editor.attachments}
                isDefaultBoard={profileSettings.settings.kanban_default_board_id === state.editor.noteID}
                onSetDefaultBoard={(enabled) => void saveDefaultBoard(enabled ? state.editor.noteID : "")}
                onChange={(board) => {
                  const editor = latestEditorRef.current;
                  updateEditor({
                    ...editor,
                    board,
                    body: boardToMarkdown(editor.title, board),
                  });
                }}
                onUpload={uploadKanbanAttachment}
                attachmentMutationsEnabled={attachmentMutationsEnabled}
                attachmentRepository={repository}
                attachmentNoteLocalKey={state.editor.noteID}
                attachmentOnline={attachmentMutationsEnabled}
                onDeleteItems={deleteKanbanItems}
                confirmDelete={(message) => dialog.confirm({
                  title: "Delete from board",
                  message,
                  confirmLabel: "Delete",
                  tone: "danger",
                })}
              />
            ) : state.editor.pageType === "notebook" ? (
              <NotebookEditor
                notes={state.notes}
                parentID={state.editor.noteID}
                onOpenNote={(id) => void selectNote(id)}
              />
            ) : (
              <>
                <label className="visually-hidden" htmlFor="noteBody">Note body</label>
                <div
                  id="noteBody"
                  ref={bodyInputRef}
                  className="notes-body-input"
                  contentEditable
                  data-placeholder="Start writing here."
                  role="textbox"
                  aria-label="Note body"
                  aria-multiline="true"
                  suppressContentEditableWarning
                  onInput={(event) => syncInputFromEditor((event.nativeEvent as InputEvent).inputType)}
                />
				{state.editor.attachments.length ? (
				  <div className="note-attachment-list" aria-label="Note attachments">
					{state.editor.attachments.map((attachment) => (
                      <AttachmentActions
                        key={attachment.id}
                        attachment={attachment}
                        repository={repository}
                        noteLocalKey={state.editor.noteID}
                        online={attachmentMutationsEnabled}
                      />
                    ))}
				  </div>
				) : null}
              </>
            )}
          </div>
        </section>}
      </div>

      {state.notebookDialogOpen ? (
        <NotebookDialog
          draft={state.notebookDraft}
          onDraft={(notebookDraft) => setReady({ notebookDraft })}
          onClose={() => setReady({ notebookDialogOpen: false })}
          onCreate={createNotebook}
        />
      ) : null}
      {trash.open ? (
        <TrashDialog
          loading={trash.loading}
          notes={trash.notes}
          onClose={() => setTrash((current) => ({ ...current, open: false }))}
          onRestore={(note) => void restoreTrashedNote(note)}
          onDelete={(note) => void permanentlyDeleteTrashedNote(note)}
        />
      ) : null}
      {state.moveDialogNoteID ? (
        <MoveNoteDialog
          notebooks={notebookItems}
          targetID={state.moveDialogTargetID}
          onTarget={(moveDialogTargetID) => setReady({ moveDialogTargetID })}
          onClose={() => setReady({ moveDialogNoteID: "", moveDialogTargetID: "" })}
          onMove={moveNote}
        />
      ) : null}
    </section>
  );
}

function TrashDialog({
  loading,
  notes,
  onClose,
  onRestore,
  onDelete,
}: {
  loading: boolean;
  notes: ProfileNoteSummary[];
  onClose: () => void;
  onRestore: (note: ProfileNoteSummary) => void;
  onDelete: (note: ProfileNoteSummary) => void;
}) {
  return (
    <div className="guide-dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="guide-dialog notes-trash-dialog" role="dialog" aria-modal="true" aria-labelledby="notes-trash-title" onMouseDown={(event) => event.stopPropagation()}>
        <header>
          <div><p className="eyebrow">Notes</p><h2 id="notes-trash-title">Trash</h2></div>
          <button className="icon-button" type="button" aria-label="Close Trash" onClick={onClose}>×</button>
        </header>
        <p className="meta-line">Restore notes or permanently delete them.</p>
        {loading ? <p className="loading-state">Loading Trash…</p> : null}
        {!loading && notes.length === 0 ? <p className="empty-state">Trash is empty.</p> : null}
        <div className="notes-trash-list">
          {notes.map((note) => (
            <article className="dashboard-tile notes-trash-item" key={noteID(note)}>
              <div><strong>{noteTitle(note)}</strong><small>Deleted {formatTrashDate(note.deleted_at)}</small></div>
              <div className="notes-trash-actions">
                <button type="button" onClick={() => onRestore(note)}>Restore</button>
                <button className="danger" type="button" onClick={() => onDelete(note)}>Delete forever</button>
              </div>
            </article>
          ))}
        </div>
      </section>
    </div>
  );
}

function formatTrashDate(value?: string): string {
  if (!value) return "recently";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "recently" : date.toLocaleString();
}

function ToolbarButton({
  label,
  icon,
  text,
  pressed,
  disabled,
  onClick,
}: {
  label: string;
  icon?: string;
  text?: string;
  pressed?: boolean;
  disabled?: boolean;
  onClick?: () => void;
}) {
  return (
    <button
      className="icon-button"
      type="button"
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      disabled={disabled}
      // Keep focus and the text selection inside the note while using the toolbar.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      {icon ? <Icon name={icon} /> : <span>{text}</span>}
    </button>
  );
}

function NotebookEditor({
  notes,
  parentID,
  onOpenNote,
}: {
  notes: ProfileNoteSummary[];
  parentID: string;
  onOpenNote: (id: string) => void;
}) {
  const notebookChildren = notes.filter((note) => note.parent_id && note.parent_id === parentID && noteID(note));
  const childNotes = [
    ...notebookChildren.filter((note) => note.pinned),
    ...notebookChildren.filter((note) => !note.pinned),
  ];
  return (
    <div className="notebook-surface" aria-label="Notebook pages">
      {childNotes.length ? (
        <div className="notebook-grid">
          {childNotes.map((note) => {
            const id = noteID(note);
            const title = noteTitle(note);
            const excluded = noteEffectiveMcpExcluded(note, notes);
            return (
              <button
                aria-label={`Open ${title}`}
                className={`notebook-card${excluded ? " is-mcp-excluded" : ""}`}
                key={id}
                onClick={() => onOpenNote(id)}
                type="button"
              >
                <span className="note-kind" aria-hidden="true"><Icon name="note" /></span>
                <strong>
                  {title}
                  {note.pinned ? <span className="notes-pin-indicator" aria-hidden="true"><Icon name="pin" /></span> : null}
                  {excluded ? <span className="notes-lock-indicator" aria-hidden="true"><Icon name="lock" /></span> : null}
                </strong>
                <span>{note.preview || "No preview"}</span>
              </button>
            );
          })}
        </div>
      ) : <p className="empty-state">No pages in this notebook yet.</p>}
    </div>
  );
}

function NotebookDialog({
  draft,
  onDraft,
  onClose,
  onCreate,
}: {
  draft: string;
  onDraft: (value: string) => void;
  onClose: () => void;
  onCreate: () => void;
}) {
  return (
    <div className="guide-dialog-scrim" role="presentation" onClick={onClose}>
      <section className="guide-dialog notebook-dialog" role="dialog" aria-modal="true" aria-label="New notebook" onClick={(event) => event.stopPropagation()}>
        <header>
          <span className="guide-dialog-icon" aria-hidden="true"><Icon name="book-plus" /></span>
          <h2>New notebook</h2>
          <button className="file-icon-action" type="button" aria-label="Close dialog" onClick={onClose}><Icon name="x" /></button>
        </header>
        <label className="guide-dialog-field">
          <span>Notebook name</span>
          <input autoFocus placeholder="Notebook" value={draft} onChange={(event) => onDraft(event.target.value)} />
        </label>
        <footer>
          <button className="secondary" type="button" onClick={onClose}>Cancel</button>
          <button type="button" onClick={onCreate}>Create notebook</button>
        </footer>
      </section>
    </div>
  );
}

function MoveNoteDialog({
  notebooks,
  targetID,
  onTarget,
  onClose,
  onMove,
}: {
  notebooks: ProfileNoteSummary[];
  targetID: string;
  onTarget: (value: string) => void;
  onClose: () => void;
  onMove: () => void;
}) {
  return (
    <div className="guide-dialog-scrim" role="presentation" onClick={onClose}>
      <section className="guide-dialog notebook-dialog" role="dialog" aria-modal="true" aria-label="Move note" onClick={(event) => event.stopPropagation()}>
        <header>
          <span className="guide-dialog-icon" aria-hidden="true"><Icon name="book" /></span>
          <h2>Move note</h2>
          <button className="file-icon-action" type="button" aria-label="Close dialog" onClick={onClose}><Icon name="x" /></button>
        </header>
        <label className="guide-dialog-field">
          <span>Move to notebook</span>
          <select autoFocus aria-label="Move to notebook" value={targetID} onChange={(event) => onTarget(event.target.value)}>
            <option value="">No Notebook</option>
            {notebooks.map((note) => (
              <option key={noteID(note)} value={noteID(note)}>{noteTitle(note)}</option>
            ))}
          </select>
        </label>
        <footer>
          <button className="secondary" type="button" onClick={onClose}>Cancel</button>
          <button type="button" onClick={onMove}>Move note</button>
        </footer>
      </section>
    </div>
  );
}
