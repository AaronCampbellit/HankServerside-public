import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AttachmentActions } from "./AttachmentActions";

describe("AttachmentActions", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  it("offers a locked preview and forced original download", () => {
    render(<AttachmentActions attachment={{
      id: "natt-html",
      filename: "report.html",
      content_type: "text/html",
      download_url: "/v1/me/notes/report/attachments/natt-html",
      preview_url: "/v1/me/notes/report/attachments/natt-html?disposition=preview",
      markdown_reference: "[report.html](hank-note-attachment://natt-html)",
    }} />);

    expect(screen.getByRole("link", { name: "Open report.html" })).toHaveAttribute("href", "/v1/me/notes/report/attachments/natt-html?disposition=preview");
    expect(screen.getByRole("link", { name: "Download report.html" })).toHaveAttribute("href", "/v1/me/notes/report/attachments/natt-html?disposition=download");
  });

  it("offers only download for an ordinary non-previewable file", () => {
    render(<AttachmentActions attachment={{
      id: "natt-pdf", filename: "brief.pdf", content_type: "application/pdf",
      download_url: "/attachment", markdown_reference: "[brief.pdf](hank-note-attachment://natt-pdf)",
    }} />);
    expect(screen.queryByRole("link", { name: "Open brief.pdf" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Download brief.pdf" })).toHaveAttribute("href", "/attachment?disposition=download");
  });

  it("opens an offline cached attachment with an object URL and revokes it", async () => {
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:cached-image"), revokeObjectURL });
    const repository = {
      readCachedAttachment: vi.fn(async () => new Blob(["image"], { type: "image/png" })),
      cacheAttachment: vi.fn(),
    };

    const rendered = render(<AttachmentActions
      attachment={{
        id: "natt-image", filename: "roof.png", content_type: "image/png",
        download_url: "/attachment", markdown_reference: "![roof](hank-note-attachment://natt-image)",
      }}
      repository={repository as never}
      noteLocalKey="server:daily.md"
      online={false}
    />);

    expect(await screen.findByRole("link", { name: "Open roof.png" })).toHaveAttribute("href", "blob:cached-image");
    expect(screen.getByRole("link", { name: "Download roof.png" })).toHaveAttribute("href", "blob:cached-image");
    expect(repository.readCachedAttachment).toHaveBeenCalledWith("natt-image");
    rendered.unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:cached-image");
  });

  it("explains when an attachment was not cached for offline use", async () => {
    const repository = {
      readCachedAttachment: vi.fn(async () => null),
      cacheAttachment: vi.fn(),
    };

    render(<AttachmentActions
      attachment={{
        id: "natt-pdf", filename: "brief.pdf", content_type: "application/pdf",
        download_url: "/attachment", markdown_reference: "[brief](hank-note-attachment://natt-pdf)",
      }}
      repository={repository as never}
      noteLocalKey="server:daily.md"
      online={false}
    />);

    expect(await screen.findByText("Available when connected")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("link", { name: "Download brief.pdf" })).not.toBeInTheDocument());
  });

  it("stores a successful online attachment response for later offline viewing", async () => {
    const blob = new Blob(["cached online"], { type: "text/plain" });
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, blob: async () => blob })));
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:online-copy"), revokeObjectURL: vi.fn() });
    const repository = {
      readCachedAttachment: vi.fn(async () => null),
      cacheAttachment: vi.fn(async () => undefined),
    };
    const attachment = {
      id: "natt-text", filename: "notes.txt", content_type: "text/plain",
      download_url: "/attachment", markdown_reference: "[notes](hank-note-attachment://natt-text)",
    };

    render(<AttachmentActions
      attachment={attachment}
      repository={repository as never}
      noteLocalKey="server:daily.md"
      online
    />);

    await waitFor(() => expect(repository.cacheAttachment).toHaveBeenCalledWith("server:daily.md", attachment, blob));
    expect(screen.getByRole("link", { name: "Download notes.txt" })).toHaveAttribute("href", "blob:online-copy");
  });

  it("does not render an untrusted HTML download from a cached Blob", async () => {
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:raw-html"), revokeObjectURL: vi.fn() });
    const repository = {
      readCachedAttachment: vi.fn(async () => new Blob(["<script>alert(1)</script>"], { type: "text/html" })),
      cacheAttachment: vi.fn(),
    };

    render(<AttachmentActions
      attachment={{
        id: "natt-html", filename: "unsafe.html", content_type: "text/html",
        download_url: "/attachment", markdown_reference: "[unsafe](hank-note-attachment://natt-html)",
      }}
      repository={repository as never}
      noteLocalKey="server:daily.md"
      online={false}
    />);

    expect(await screen.findByRole("link", { name: "Download unsafe.html" })).toHaveAttribute("href", "blob:raw-html");
    expect(screen.queryByRole("link", { name: "Open unsafe.html" })).not.toBeInTheDocument();
  });
});
