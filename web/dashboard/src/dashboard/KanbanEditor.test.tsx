import { cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { KanbanBoard, NoteAttachment } from "../api/profileNotes";
import { boardFromMarkdown, boardToMarkdown, KanbanEditor } from "./KanbanEditor";

const attachments: NoteAttachment[] = [{
  id: "natt-1",
  filename: "wireframe.png",
  content_type: "image/png",
  download_url: "/v1/me/notes/work/attachments/natt-1",
  markdown_reference: "![wireframe.png](hank-note-attachment://natt-1?filename=wireframe.png&scope=profile)",
}];

function workBoard(): KanbanBoard {
  return {
    columns: [
      {
        id: "inbox",
        title: "Inbox",
        sort_order: 0,
        cards: [{ id: "brief", text: "Review brief\nRead **scope** and [open Ajera](https://example.com).", sort_order: 0 }],
      },
      { id: "doing", title: "In progress", sort_order: 1, cards: [] },
      { id: "done", title: "Done", sort_order: 2, cards: [] },
    ],
  };
}

function Harness({
  initial = workBoard(),
  onUpload = vi.fn(),
  confirmDelete = vi.fn(async () => true),
  onDeleteItems = vi.fn(async () => true),
  isDefaultBoard = false,
  onSetDefaultBoard = vi.fn(),
  attachmentMutationsEnabled = true,
}: {
  initial?: KanbanBoard;
  onUpload?: (file: File) => Promise<NoteAttachment>;
  confirmDelete?: (message: string) => Promise<boolean>;
  onDeleteItems?: (board: KanbanBoard, attachments: NoteAttachment[]) => Promise<boolean>;
  isDefaultBoard?: boolean;
  onSetDefaultBoard?: (enabled: boolean) => void;
  attachmentMutationsEnabled?: boolean;
}) {
  let board = initial;
  const change = vi.fn((next: KanbanBoard) => { board = next; rerender(); });
  const props = () => ({
    board,
    attachments,
    onChange: change,
    onUpload,
    confirmDelete,
    onDeleteItems,
    isDefaultBoard,
    onSetDefaultBoard,
    attachmentMutationsEnabled,
  });
  const rendered = render(<KanbanEditor {...props()} />);
  const rerender = () => rendered.rerender(<KanbanEditor {...props()} />);
  return { change, onSetDefaultBoard };
}

describe("KanbanEditor", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });
  it("migrates legacy markdown into stable board data and back", () => {
    const board = boardFromMarkdown("# Client Work\n\n## Inbox\n- Review brief\n\n## Doing\n- Draft proposal");

    expect(board.columns?.map((column) => column.title)).toEqual(["Inbox", "Doing"]);
    expect(board.columns?.[0].cards?.[0].text).toBe("Review brief");
    expect(board.columns?.[0].id).toBeTruthy();
    expect(boardToMarkdown("Client Work", board)).toContain("## Doing\n- Draft proposal");
  });

  it("marks bold card text for stronger visual contrast", () => {
    Harness({});

    expect(screen.getByText("scope")).toHaveClass("kanban-rich-strong");
  });

  it("configures the default board, intake column, and unique workflow roles", () => {
    const initial = workBoard();
    initial.intake_column_id = "inbox";
    initial.columns![0].role = "planning";
    initial.columns![1].role = "active";
    const onSetDefaultBoard = vi.fn();
    const { change } = Harness({ initial, onSetDefaultBoard });

    expect(screen.queryByRole("button", { name: "Set as default board" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Board options" }));
    fireEvent.click(screen.getByRole("button", { name: "Set as default board" }));
    expect(onSetDefaultBoard).toHaveBeenCalledWith(true);
    expect(screen.getByText("Intake")).toBeInTheDocument();
    expect(screen.getByText("Planning")).toBeInTheDocument();
    expect(screen.getByText("Active Work")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Column options for In progress" }));
    fireEvent.click(screen.getByRole("button", { name: "Set In progress as intake" }));
    fireEvent.change(screen.getByLabelText("Role for In progress"), { target: { value: "planning" } });

    const latest = change.mock.calls.at(-1)?.[0];
    expect(latest?.intake_column_id).toBe("doing");
    expect(latest?.columns?.find((column) => column.id === "doing")?.role).toBe("planning");
    expect(latest?.columns?.find((column) => column.id === "inbox")?.role).toBe("");
  });

  it("moves the clear-default action into board options once the board is default", () => {
    const onSetDefaultBoard = vi.fn();
    Harness({ isDefaultBoard: true, onSetDefaultBoard });

    expect(screen.getByText("Default board")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear default board" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Board options" }));
    expect(screen.getByRole("button", { name: "Default board" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Clear default board" }));

    expect(onSetDefaultBoard).toHaveBeenCalledWith(false);
  });

  it("keeps column metadata compact while reserving the header edge for tools", () => {
    const initial = workBoard();
    initial.intake_column_id = "inbox";
    initial.columns![0].role = "planning";
    Harness({ initial });

    const heading = screen.getByRole("heading", { name: "Inbox" });
    const summary = heading.closest(".kanban-column-summary");
    if (!(summary instanceof HTMLElement)) throw new Error("column summary is missing");
    expect(within(summary).getByText("Intake")).toBeInTheDocument();
    expect(within(summary).getByText("Planning")).toBeInTheDocument();
    expect(within(summary).getByText("1")).toBeInTheDocument();

    const actions = screen.getByRole("button", { name: "Column options for Inbox" }).closest(".kanban-column-actions");
    expect(actions).not.toBeNull();
    expect(actions?.parentElement).toHaveClass("kanban-column-head");
  });

  it("dismisses an open column menu when the user presses outside it", () => {
    Harness({});
    const trigger = screen.getByRole("button", { name: "Column options for In progress" });

    fireEvent.click(trigger);
    expect(screen.getByRole("button", { name: "Set In progress as intake" })).toBeInTheDocument();

    fireEvent.pointerDown(document.body);

    expect(screen.queryByRole("button", { name: "Set In progress as intake" })).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("dismisses an open column menu when the user presses Escape", () => {
    Harness({});
    const trigger = screen.getByRole("button", { name: "Column options for In progress" });

    fireEvent.click(trigger);
    expect(screen.getByRole("button", { name: "Set In progress as intake" })).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("button", { name: "Set In progress as intake" })).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("clears intake metadata when its column is deleted", async () => {
    const initial: KanbanBoard = {
      intake_column_id: "ideas",
      columns: [
        { id: "ideas", title: "Ideas", role: "planning", sort_order: 0, cards: [] },
        { id: "work", title: "Work", role: "active", sort_order: 1, cards: [] },
      ],
    };
    const { change } = Harness({ initial });

    fireEvent.click(screen.getByRole("button", { name: "Delete Ideas" }));

    await waitFor(() => {
      const latest = change.mock.calls.at(-1)?.[0];
      expect(latest?.intake_column_id).toBe("");
      expect(latest?.columns?.map((column) => column.id)).toEqual(["work"]);
    });
  });

  it("adds, edits, formats, searches, and explicitly moves a task", async () => {
    const { change } = Harness({});

    expect(screen.getByRole("link", { name: "open Ajera" })).toHaveAttribute("href", "https://example.com");
    fireEvent.click(screen.getAllByRole("button", { name: "Add task to Inbox" })[0]);
    fireEvent.change(screen.getByLabelText("Task title"), { target: { value: "Prepare invoice" } });
    fireEvent.click(screen.getByRole("button", { name: "Create task" }));
    expect(screen.getByRole("button", { name: "Open task Prepare invoice" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open task Prepare invoice" }));
    const modal = screen.getByRole("dialog", { name: "Task details" });
    expect(modal).toHaveAttribute("aria-modal", "true");
    fireEvent.click(within(modal).getByRole("button", { name: "Edit description" }));
    const description = within(modal).getByLabelText("Description");
    description.innerHTML = "<p>Send today</p>";
    fireEvent.input(description, { inputType: "insertText" });
    fireEvent.click(within(modal).getByRole("button", { name: "Bold" }));
    fireEvent.change(within(modal).getByLabelText("Due date"), { target: { value: "2026-07-18" } });
    fireEvent.click(within(modal).getByRole("button", { name: "Cyan card" }));
    fireEvent.click(within(modal).getByRole("button", { name: "Move task right" }));

    expect(screen.getByRole("heading", { name: "In progress" }).closest("section")).toHaveTextContent("Prepare invoice");
    expect(screen.getByRole("dialog", { name: "Task details" })).toBeInTheDocument();
    expect(within(modal).getByLabelText("Due date")).toHaveValue("2026-07-18");
    expect(within(modal).getByRole("button", { name: "Cyan card" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Jul 18").closest("time")).toHaveAttribute("datetime", "2026-07-18");
    expect(change).toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Search cards"), { target: { value: "invoice" } });
    expect(screen.getByRole("button", { name: "Open task Prepare invoice" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open task Review brief" })).not.toBeInTheDocument();
  });

  it("adds a new task at the top of its column with normalized ordering", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-31T01:00:00Z"));
    const { change } = Harness({});

    fireEvent.click(screen.getAllByRole("button", { name: "Add task to Inbox" })[0]);
    fireEvent.change(screen.getByLabelText("Task title"), { target: { value: "Newest task" } });
    fireEvent.click(screen.getByRole("button", { name: "Create task" }));

    const latestCards = change.mock.calls.at(-1)?.[0].columns?.find((column) => column.id === "inbox")?.cards;
    expect(latestCards?.map((card) => card.text?.split("\n", 1)[0])).toEqual(["Newest task", "Review brief"]);
    expect(latestCards?.map((card) => card.sort_order)).toEqual([0, 1]);
    expect(latestCards?.[0]).toMatchObject({
      created_at: "2026-08-31T01:00:00.000Z",
      updated_at: "2026-08-31T01:00:00.000Z",
    });
  });

  it("keeps a cleared task title empty until the user enters its replacement", () => {
    Harness({});

    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    const title = screen.getByLabelText("Task title");
    fireEvent.change(title, { target: { value: "" } });

    expect(title).toHaveValue("");
    expect(title).toHaveAttribute("placeholder", "Untitled task");

    fireEvent.change(title, { target: { value: "Confirm warranty" } });
    expect(title).toHaveValue("Confirm warranty");
    expect(screen.getByRole("button", { name: "Open task Confirm warranty" })).toBeInTheDocument();
  });

  it("disables attachment uploads with an explanation while offline", () => {
    Harness({ attachmentMutationsEnabled: false });

    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));

    expect(screen.getByLabelText("Add screenshot or file")).toBeDisabled();
    expect(screen.getByText("Available when connected")).toBeInTheDocument();
  });

  it("places the add task action beside the column controls", () => {
    Harness({});
    const column = screen.getByRole("heading", { name: "Inbox" }).closest("section")!;
    const actions = column.querySelector<HTMLElement>(".kanban-column-actions")!;

    expect(within(actions).getByRole("button", { name: "Add task to Inbox" })).toBeInTheDocument();
    expect(within(actions).getByRole("button", { name: "Delete Inbox" })).toBeInTheDocument();
  });

  it("reorders columns by dragging a dedicated header grip", () => {
    const { change } = Harness({});
    const grip = screen.getByRole("button", { name: "Drag Inbox column" });
    const target = screen.getByRole("heading", { name: "Done" }).closest("section")!;
    const dataTransfer = { effectAllowed: "none", setData: vi.fn() };

    fireEvent.dragStart(grip, { dataTransfer });
    expect(grip).toHaveAttribute("aria-grabbed", "true");
    fireEvent.dragOver(target, { dataTransfer });
    expect(target).toHaveClass("is-column-drop-target");
    fireEvent.drop(target, { dataTransfer });

    const latest = change.mock.calls.at(-1)?.[0];
    expect(dataTransfer.setData).toHaveBeenCalledWith("application/x-hank-kanban-column", "inbox");
    expect(latest?.columns?.map((column) => column.id)).toEqual(["doing", "done", "inbox"]);
    expect(latest?.columns?.map((column) => column.sort_order)).toEqual([0, 1, 2]);
  });

  it("keeps the named move buttons as a working column reorder fallback", () => {
    const { change } = Harness({});

    fireEvent.click(screen.getByRole("button", { name: "Move Inbox right" }));

    const latest = change.mock.calls.at(-1)?.[0];
    expect(latest?.columns?.map((column) => column.id)).toEqual(["doing", "inbox", "done"]);
    expect(screen.getByRole("button", { name: "Move Inbox left" })).toBeEnabled();
  });

  it("does not change the board when a column is dropped on itself", () => {
    const { change } = Harness({});
    const grip = screen.getByRole("button", { name: "Drag Inbox column" });
    const inbox = screen.getByRole("heading", { name: "Inbox" }).closest("section")!;
    const dataTransfer = { effectAllowed: "none", setData: vi.fn() };

    fireEvent.dragStart(grip, { dataTransfer });
    fireEvent.drop(inbox, { dataTransfer });

    expect(change).not.toHaveBeenCalled();
    expect(inbox).not.toHaveClass("is-column-drop-target");
  });

  it("preserves a trailing space while editing a task description", () => {
    Harness({});
    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const description = screen.getByLabelText("Description");
    const initialText = description.textContent || "";
    description.innerHTML = `<p>${initialText} </p>`;
    fireEvent.input(description, { inputType: "insertText" });
    description.querySelector("p")!.append("next");
    fireEvent.input(description, { inputType: "insertText" });

    expect(screen.getByLabelText("Description")).toHaveTextContent(`${initialText} next`);
  });

  it("moves a dragged card without opening its editor", () => {
    Harness({});
    const open = screen.getByRole("button", { name: "Open task Review brief" });
    const card = open.closest("article");
    const target = screen.getByRole("heading", { name: "In progress" }).closest("section");
    const dataTransfer = { effectAllowed: "none", setData: vi.fn() };

    expect(card).not.toBeNull();
    expect(target).not.toBeNull();
    fireEvent.dragStart(card!, { dataTransfer });
    fireEvent.drop(target!, { dataTransfer });
    fireEvent.dragEnd(card!, { dataTransfer });

    expect(target).toHaveTextContent("Review brief");
    expect(screen.queryByRole("dialog", { name: "Task details" })).not.toBeInTheDocument();
  });

  it("allows a deliberate click after a dropped card unmounts before dragend", async () => {
    vi.useFakeTimers();
    Harness({});
    const open = screen.getByRole("button", { name: "Open task Review brief" });
    const card = open.closest("article");
    const target = screen.getByRole("heading", { name: "In progress" }).closest("section");
    const dataTransfer = { effectAllowed: "none", setData: vi.fn() };

    expect(card).not.toBeNull();
    expect(target).not.toBeNull();
    fireEvent.dragStart(card!, { dataTransfer });
    fireEvent.drop(target!, { dataTransfer });

    const moved = screen.getByRole("button", { name: "Open task Review brief" });
    fireEvent.click(moved);
    expect(screen.queryByRole("dialog", { name: "Task details" })).not.toBeInTheDocument();

    await vi.runAllTimersAsync();
    fireEvent.click(moved);
    expect(screen.getByRole("dialog", { name: "Task details" })).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("suppresses the click emitted immediately after dragging", () => {
    Harness({});
    const open = screen.getByRole("button", { name: "Open task Review brief" });
    const card = open.closest("article");
    const dataTransfer = { effectAllowed: "none", setData: vi.fn() };

    expect(card).not.toBeNull();
    fireEvent.dragStart(card!, { dataTransfer });
    fireEvent.dragEnd(card!, { dataTransfer });
    fireEvent.click(open);

    expect(screen.queryByRole("dialog", { name: "Task details" })).not.toBeInTheDocument();
  });

  it("describes and serializes task attachment deletion before changing the board", async () => {
    const initial = workBoard();
    initial.columns![0].cards![0].text += `\n${attachments[0].markdown_reference}`;
    const confirmDelete = vi.fn<(message: string) => Promise<boolean>>(async () => true);
    const onDeleteItems = vi.fn<(board: KanbanBoard, attachments: NoteAttachment[]) => Promise<boolean>>(async () => true);
    Harness({ initial, confirmDelete, onDeleteItems });

    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete task" }));

    await waitFor(() => expect(onDeleteItems).toHaveBeenCalledTimes(1));
    expect(confirmDelete.mock.calls[0][0]).toContain("wireframe.png");
    expect(confirmDelete.mock.calls[0][0]).toContain("permanently delete 1 attached file");
    expect(onDeleteItems.mock.calls[0][0].columns?.[0].cards).toHaveLength(0);
    expect(onDeleteItems.mock.calls[0][1]).toEqual(attachments);
    expect(screen.queryByRole("button", { name: "Open task Review brief" })).not.toBeInTheDocument();
  });

  it("keeps a task visible when the persisted deletion fails", async () => {
    const onDeleteItems = vi.fn<(board: KanbanBoard, attachments: NoteAttachment[]) => Promise<boolean>>(async () => false);
    Harness({ onDeleteItems });

    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete task" }));

    await waitFor(() => expect(onDeleteItems).toHaveBeenCalled());
    expect(screen.getByRole("dialog", { name: "Task details" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open task Review brief" })).toBeInTheDocument();
  });

  it("uses attachment-aware orchestration when deleting a populated column", async () => {
    const initial = workBoard();
    initial.columns![0].cards![0].text += `\n${attachments[0].markdown_reference}`;
    const confirmDelete = vi.fn<(message: string) => Promise<boolean>>(async () => true);
    const onDeleteItems = vi.fn<(board: KanbanBoard, attachments: NoteAttachment[]) => Promise<boolean>>(async () => true);
    Harness({ initial, confirmDelete, onDeleteItems });

    fireEvent.click(screen.getByRole("button", { name: "Delete Inbox" }));

    await waitFor(() => expect(onDeleteItems).toHaveBeenCalled());
    expect(confirmDelete.mock.calls[0][0]).toContain("wireframe.png");
    expect(onDeleteItems.mock.calls[0][0].columns?.map((column) => column.id)).not.toContain("inbox");
  });

  it("uploads and renders a screenshot in a card", async () => {
    const upload = vi.fn(async () => attachments[0]);
    Harness({ onUpload: upload });
    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    const drawer = screen.getByRole("dialog", { name: "Task details" });
    const file = new File(["image"], "wireframe.png", { type: "image/png" });

    fireEvent.change(within(drawer).getByLabelText("Add screenshot or file"), { target: { files: [file] } });

    expect(upload).toHaveBeenCalledWith(file);
    expect(await within(drawer).findByRole("img", { name: "wireframe.png" })).toHaveAttribute("src", attachments[0].download_url);
    expect(within(screen.getByRole("button", { name: "Open task Review brief" })).getByRole("img", { name: "wireframe.png" })).toHaveAttribute("src", attachments[0].download_url);
  });

  it("keeps successful pasted screenshots when a later upload fails", async () => {
    const upload = vi.fn()
      .mockResolvedValueOnce(attachments[0])
      .mockRejectedValueOnce(new Error("Second screenshot failed"));
    const { change } = Harness({ onUpload: upload });
    fireEvent.click(screen.getByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const description = screen.getByLabelText("Description");
    const range = document.createRange();
    range.selectNodeContents(description);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    const first = new File(["one"], "wireframe.png", { type: "image/png" });
    const second = new File(["two"], "second.png", { type: "image/png" });
    const paste = createEvent.paste(description, {
      bubbles: true,
      cancelable: true,
      clipboardData: {
        items: [
          { kind: "file", type: "image/png", getAsFile: () => first },
          { kind: "file", type: "image/png", getAsFile: () => second },
        ],
      },
    });

    fireEvent(description, paste);

    expect(await screen.findByRole("alert")).toHaveTextContent("Second screenshot failed");
    expect(upload).toHaveBeenNthCalledWith(1, first);
    expect(upload).toHaveBeenNthCalledWith(2, second);
    await waitFor(() => {
      const latest = change.mock.calls.at(-1)?.[0];
      const saved = latest?.columns?.flatMap((column) => column.cards || []).find((card) => card.id === "brief");
      expect(saved?.text).toContain("hank-note-attachment://natt-1");
    });
  });
});
