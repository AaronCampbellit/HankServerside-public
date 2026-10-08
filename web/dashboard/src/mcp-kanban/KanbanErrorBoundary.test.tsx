import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KanbanErrorBoundary } from "./KanbanErrorBoundary";

function BrokenBoard(): never {
  throw new Error("render failed");
}

describe("KanbanErrorBoundary", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows a useful recovery message instead of a blank board", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    render(<KanbanErrorBoundary><BrokenBoard /></KanbanErrorBoundary>);

    expect(screen.getByRole("alert")).toHaveTextContent("Close and reopen this board");
  });
});
