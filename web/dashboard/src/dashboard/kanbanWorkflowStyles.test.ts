import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("Kanban workflow controls stylesheet", () => {
  it("styles configuration badges and the column menu without affecting the card modal", () => {
    const styles = readFileSync("src/dashboard/kanbanWorkflow.css", "utf8");
    expect(styles).toContain(".kanban-workflow-badge");
    expect(styles).toContain(".kanban-column-menu");
    expect(styles).toContain("position: absolute");
    expect(styles).not.toContain(".kanban-card-modal");
  });
});
