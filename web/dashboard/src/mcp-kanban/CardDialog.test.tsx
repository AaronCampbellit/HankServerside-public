import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CardDialog } from "./CardDialog";
import type { KanbanCard, KanbanColumn } from "./types";

const card: KanbanCard = {
  board_id: "work", board_title: "Work", board_revision: "rev-1", column_id: "inbox", column_title: "Inbox",
  card_id: "research", title: "Research sync", details_markdown: "Capture requirements", due_date: "2026-08-20", tags: ["Hank"],
  attachments: [{ id: "natt-1", filename: "wireframe.png", content_type: "image/png", size_bytes: 2048 }],
};
const columns: KanbanColumn[] = [
  { column_id: "inbox", title: "Inbox", cards: [card] },
  { column_id: "active", title: "Active", cards: [] },
];

describe("CardDialog", () => {
  afterEach(cleanup);

  it("submits only card fields that changed", () => {
    const onUpdate = vi.fn();
    render(<CardDialog card={card} columns={columns} pending={false} canWrite canDelete={false} onUpdate={onUpdate} onWorklog={vi.fn()} onMove={vi.fn()} onDelete={vi.fn()} onClose={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Card title"), { target: { value: "Research resilient sync" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(onUpdate).toHaveBeenCalledWith({ title: "Research resilient sync" });
  });

  it("submits work logs and explicit moves", () => {
    const onWorklog = vi.fn();
    const onMove = vi.fn();
    render(<CardDialog card={card} columns={columns} pending={false} canWrite canDelete={false} onUpdate={vi.fn()} onWorklog={onWorklog} onMove={onMove} onDelete={vi.fn()} onClose={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Work log entry"), { target: { value: "Tests passed" } });
    fireEvent.change(screen.getByLabelText("Work log kind"), { target: { value: "verification" } });
    fireEvent.click(screen.getByRole("button", { name: "Add work log" }));
    expect(onWorklog).toHaveBeenCalledWith("verification", "Tests passed");

    fireEvent.change(screen.getByLabelText("Move to column"), { target: { value: "active" } });
    fireEvent.click(screen.getByRole("button", { name: "Move card" }));
    expect(onMove).toHaveBeenCalledWith("active");
  });

  it("shows attachment metadata without upload or delete controls", () => {
    render(<CardDialog card={card} columns={columns} pending={false} canWrite canDelete={false} onUpdate={vi.fn()} onWorklog={vi.fn()} onMove={vi.fn()} onDelete={vi.fn()} onClose={vi.fn()} />);

    expect(screen.getByText("wireframe.png")).toBeInTheDocument();
    expect(screen.getByText(/image\/png/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /upload/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete attachment/i })).not.toBeInTheDocument();
  });

  it("requires an explicit local confirmation before deletion", () => {
    const onDelete = vi.fn();
    render(<CardDialog card={card} columns={columns} pending={false} canWrite canDelete onUpdate={vi.fn()} onWorklog={vi.fn()} onMove={vi.fn()} onDelete={onDelete} onClose={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Delete card" }));
    expect(onDelete).not.toHaveBeenCalled();
    expect(screen.getByText(/ChatGPT will ask you to confirm again/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel deletion" }));
    expect(onDelete).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete card" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm delete Research sync" }));
    expect(onDelete).toHaveBeenCalledTimes(1);
  });
});
