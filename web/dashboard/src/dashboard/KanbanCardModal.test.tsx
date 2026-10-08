import { cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KanbanCardModal, type KanbanCardModalProps } from "./KanbanCardModal";

function modalProps(overrides: Partial<KanbanCardModalProps> = {}): KanbanCardModalProps {
  return {
    card: { id: "brief", text: "Review brief\nRead the scope", sort_order: 0, color: "cyan" },
    title: "Review brief",
    description: "Read the scope",
    columnID: "inbox",
    columns: [
      { id: "inbox", title: "Inbox", sort_order: 0, cards: [] },
      { id: "doing", title: "In progress", sort_order: 1, cards: [] },
    ],
    attachments: [],
    uploading: false,
    deleting: false,
    uploadError: "",
    onTitleChange: vi.fn(),
    onDescriptionChange: vi.fn(),
    onMove: vi.fn(),
    onDueDateChange: vi.fn(),
    onColorChange: vi.fn(),
    onPriorityChange: vi.fn(),
    onTagsChange: vi.fn(),
    onUploadFiles: vi.fn(async () => undefined),
    onMoveLeft: vi.fn(),
    onMoveRight: vi.fn(),
    canMoveLeft: false,
    canMoveRight: true,
    onDelete: vi.fn(),
    onClose: vi.fn(),
    ...overrides,
  };
}

describe("KanbanCardModal", () => {
  afterEach(() => {
    cleanup();
    Reflect.deleteProperty(document, "execCommand");
  });

  it("opens as a modal card and focuses its title", () => {
    render(<KanbanCardModal {...modalProps()} />);

    const dialog = screen.getByRole("dialog", { name: "Task details" });
    const title = screen.getByLabelText("Task title");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveClass("color-cyan");
    expect(title).toHaveFocus();
  });

  it("shows the immutable card creation date and handles legacy cards", () => {
    const { rerender } = render(<KanbanCardModal {...modalProps({ card: {
      id: "brief", text: "Review brief", sort_order: 0, created_at: "2026-08-31T01:00:00Z",
    } })} />);

    expect(screen.getByText("Created Aug 31, 2026")).toBeInTheDocument();

    rerender(<KanbanCardModal {...modalProps()} />);
    expect(screen.getByText("Created date unavailable")).toBeInTheDocument();
  });

  it("edits priority and tags as structured card metadata", () => {
    const onPriorityChange = vi.fn();
    const onTagsChange = vi.fn();
    render(<KanbanCardModal {...modalProps({
      card: { id: "brief", text: "Review brief", sort_order: 0, priority: "medium", tags: ["Hank"] },
      onPriorityChange,
      onTagsChange,
    })} />);

    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "high" } });
    fireEvent.change(screen.getByLabelText("Tags"), { target: { value: "Hank, frontend, Hank" } });

    expect(onPriorityChange).toHaveBeenCalledWith("high");
    expect(onTagsChange).toHaveBeenCalledWith(["Hank", "frontend"]);
  });

  it("closes from Escape and the backdrop but not an inside click", () => {
    const onClose = vi.fn();
    render(<KanbanCardModal {...modalProps({ onClose })} />);
    const dialog = screen.getByRole("dialog", { name: "Task details" });

    fireEvent.mouseDown(dialog);
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);

    onClose.mockClear();
    fireEvent.mouseDown(screen.getByTestId("kanban-card-modal-backdrop"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("wraps keyboard focus within the modal", () => {
    render(<KanbanCardModal {...modalProps()} />);
    const title = screen.getByLabelText("Task title");
    const lastControl = screen.getByLabelText("Add screenshot or file");
    const dialog = screen.getByRole("dialog", { name: "Task details" });

    lastControl.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(title).toHaveFocus();

    title.focus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(lastControl).toHaveFocus();
  });

  it("routes pasted images through inline upload at the caret", () => {
    const onUploadFiles = vi.fn(async () => undefined);
    render(<KanbanCardModal {...modalProps({ onUploadFiles, description: "Before after" })} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const description = screen.getByLabelText("Description");
    const text = description.querySelector("p")?.firstChild;
    expect(text).not.toBeNull();
    const range = document.createRange();
    range.setStart(text!, 7);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    const image = new File(["png"], "capture.png", { type: "image/png" });
    const paste = createEvent.paste(description, {
      bubbles: true,
      cancelable: true,
      clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => image }] },
    });

    fireEvent(description, paste);

    expect(paste.defaultPrevented).toBe(true);
    expect(onUploadFiles).toHaveBeenCalledWith([image], { start: 7, end: 7 });
  });

  it("uploads a screenshot pasted anywhere in the open card", () => {
    const onUploadFiles = vi.fn(async () => undefined);
    render(<KanbanCardModal {...modalProps({ onUploadFiles })} />);
    const title = screen.getByLabelText("Task title");
    const image = new File(["png"], "capture.png", { type: "image/png" });
    const paste = createEvent.paste(title, {
      bubbles: true,
      cancelable: true,
      clipboardData: { items: [], files: [image] },
    });

    fireEvent(title, paste);

    expect(paste.defaultPrevented).toBe(true);
    expect(onUploadFiles).toHaveBeenCalledWith([image], undefined);
  });

  it("leaves ordinary text paste to the browser", () => {
    const onUploadFiles = vi.fn(async () => undefined);
    render(<KanbanCardModal {...modalProps({ onUploadFiles })} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const description = screen.getByLabelText("Description");
    const paste = createEvent.paste(description, {
      bubbles: true,
      cancelable: true,
      clipboardData: { items: [{ kind: "string", type: "text/plain", getAsFile: () => null }] },
    });

    fireEvent(description, paste);

    expect(paste.defaultPrevented).toBe(false);
    expect(onUploadFiles).not.toHaveBeenCalled();
  });

  it("renders referenced screenshots inside the description instead of exposing their markdown", () => {
    const attachment = {
      id: "natt-1",
      filename: "capture.png",
      content_type: "image/png",
      download_url: "/v1/me/notes/work/attachments/natt-1",
      markdown_reference: "![capture.png](hank-note-attachment://natt-1)",
    };
    render(<KanbanCardModal {...modalProps({ description: attachment.markdown_reference, attachments: [attachment] })} />);

    const descriptionSection = screen.getByRole("heading", { name: "Description" }).closest("section");
    expect(descriptionSection).not.toBeNull();
    expect(within(descriptionSection!).getByRole("img", { name: "capture.png" })).toHaveAttribute("src", attachment.download_url);
    expect(within(descriptionSection!).queryByText(attachment.markdown_reference)).not.toBeInTheDocument();

    fireEvent.click(within(descriptionSection!).getByRole("button", { name: "Edit description" }));
    const editor = screen.getByLabelText("Description");
    expect(within(editor).getByRole("img", { name: "capture.png" })).toHaveAttribute("src", attachment.download_url);
    expect(editor).not.toHaveTextContent(attachment.markdown_reference);
  });

  it("offers open and download actions for referenced HTML", () => {
    const attachment = {
      id: "natt-html",
      filename: "report.html",
      content_type: "text/html",
      download_url: "/v1/me/notes/work/attachments/natt-html",
      preview_url: "/v1/me/notes/work/attachments/natt-html?disposition=preview",
      markdown_reference: "[report.html](hank-note-attachment://natt-html)",
    };
    render(<KanbanCardModal {...modalProps({ description: attachment.markdown_reference, attachments: [attachment] })} />);

    expect(screen.getAllByRole("link", { name: "Open report.html" })[0]).toHaveAttribute("href", attachment.preview_url);
    expect(screen.getByRole("link", { name: "Download report.html" })).toHaveAttribute("href", attachment.download_url + "?disposition=download");
  });

  it("copies a selected screenshot as its PNG file instead of its fallback text", async () => {
    const attachment = {
      id: "natt-1",
      filename: "capture.png",
      content_type: "image/png",
      download_url: "/v1/me/notes/work/attachments/natt-1",
      markdown_reference: "![capture.png](hank-note-attachment://natt-1)",
    };
    const originalFetch = globalThis.fetch;
    const fetchImage = vi.fn().mockResolvedValue({ ok: true, blob: async () => new Blob(["png-bytes"], { type: "image/png" }) });
    Object.defineProperty(globalThis, "fetch", { configurable: true, value: fetchImage });
    try {
      render(<KanbanCardModal {...modalProps({ description: attachment.markdown_reference, attachments: [attachment] })} />);
      fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
      const editor = screen.getByLabelText("Description");
      await waitFor(() => expect(fetchImage).toHaveBeenCalled());
      const image = within(editor).getByRole("img", { name: "capture.png" });
      const range = document.createRange();
      range.selectNode(image);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      const add = vi.fn();
      const copied = createEvent.copy(editor, { bubbles: true, cancelable: true, clipboardData: { items: { add }, setData: vi.fn() } });

      fireEvent(editor, copied);

      expect(copied.defaultPrevented).toBe(true);
      expect(add).toHaveBeenCalledWith(expect.objectContaining({ name: "capture.png", type: "image/png" }));
    } finally {
      Object.defineProperty(globalThis, "fetch", { configurable: true, value: originalFetch });
    }
  });

  it("renders markdown formatting while the description is being edited", () => {
    render(<KanbanCardModal {...modalProps({ description: "**bold text**" })} />);

    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));

    const editor = screen.getByLabelText("Description");
    expect(editor.tagName).toBe("DIV");
    expect(editor.querySelector("strong")).toHaveTextContent("bold text");
    expect(editor).not.toHaveTextContent("**");
  });

  it("starts an empty bullet without inserting placeholder text", () => {
    const onDescriptionChange = vi.fn();
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: vi.fn(() => true),
    });
    render(<KanbanCardModal {...modalProps({ description: "", onDescriptionChange })} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));

    fireEvent.click(screen.getByRole("button", { name: "Bulleted list" }));

    const editor = screen.getByLabelText("Description");
    expect(editor.querySelectorAll("li")).toHaveLength(1);
    expect(editor).not.toHaveTextContent("list item");
    expect(onDescriptionChange).toHaveBeenLastCalledWith("-");
  });

  it("continues a bullet on the next line when Enter is pressed", () => {
    const onDescriptionChange = vi.fn();
    render(<KanbanCardModal {...modalProps({ description: "- First item", onDescriptionChange })} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const editor = screen.getByLabelText("Description");
    const item = editor.querySelector("li");
    expect(item).not.toBeNull();
    const range = document.createRange();
    range.selectNodeContents(item!);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    fireEvent.keyDown(editor, { key: "Enter" });

    expect(editor.querySelectorAll("li")).toHaveLength(2);
    expect(onDescriptionChange).toHaveBeenLastCalledWith("- First item\n-");
  });

  it("does not save empty inline formatting nodes as markdown markers", () => {
    const onDescriptionChange = vi.fn();
    render(<KanbanCardModal {...modalProps({ description: "", onDescriptionChange })} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    const editor = screen.getByLabelText("Description");
    editor.innerHTML = "<ul><li>First item</li><li>Second item<strong><br></strong></li></ul>";

    fireEvent.input(editor, { inputType: "insertText" });

    expect(onDescriptionChange).toHaveBeenLastCalledWith("- First item\n- Second item");
  });

  it("opens the editor when the description surface is clicked", () => {
    render(<KanbanCardModal {...modalProps()} />);

    fireEvent.click(screen.getByTestId("kanban-description-preview"));

    expect(screen.getByLabelText("Description")).toHaveFocus();
  });

  it("keeps all task options together above the description", () => {
    render(<KanbanCardModal {...modalProps()} />);
    const options = screen.getByTestId("kanban-card-options");
    const description = screen.getByRole("heading", { name: "Description" }).closest("section");

    expect(screen.getByLabelText("Task title")).toHaveAttribute("rows", "1");
    expect(within(options).getByLabelText("Column")).toBeInTheDocument();
    expect(within(options).getByLabelText("Due date")).toBeInTheDocument();
    expect(within(options).getByRole("button", { name: "Move task right" })).toBeInTheDocument();
    expect(within(options).getByRole("button", { name: "Delete task" })).toBeInTheDocument();
    expect(options.compareDocumentPosition(description!)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(screen.getByText("Paste or drop screenshots here, or choose files")).toBeInTheDocument();
  });
});
