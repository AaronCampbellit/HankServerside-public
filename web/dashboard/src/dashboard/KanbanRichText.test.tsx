import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { KanbanRichText } from "./KanbanRichText";

describe("KanbanRichText attachments", () => {
  it("resolves a canonical HTML attachment link to its locked preview", () => {
    render(<KanbanRichText
      value="[report.html](hank-note-attachment://natt-html)"
      attachments={[{
        id: "natt-html", filename: "report.html", content_type: "text/html",
        download_url: "/attachment", preview_url: "/attachment?disposition=preview",
        markdown_reference: "[report.html](hank-note-attachment://natt-html)",
      }]}
    />);
    expect(screen.getByRole("link", { name: "report.html" })).toHaveAttribute("href", "/attachment?disposition=preview");
  });
});
