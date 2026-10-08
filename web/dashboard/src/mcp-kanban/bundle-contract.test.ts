// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const MAX_UNCOMPRESSED_BYTES = 1_461_000;
const MAX_GZIP_BYTES = 342_000;
const embeddedPath = resolve(process.cwd(), "../../internal/cloud/ui/mcp/kanban-v1.html");

describe("Kanban MCP app bundle contract", () => {
  it("is one self-contained, private, bounded HTML resource", () => {
    const html = readFileSync(embeddedPath, "utf8");
    const document = new DOMParser().parseFromString(html, "text/html");

    expect(document.querySelectorAll("#hank-kanban-root")).toHaveLength(1);
    expect(document.querySelectorAll("script[type='module']:not([src])")).toHaveLength(1);
    expect(document.querySelectorAll("style")).toHaveLength(1);
    expect(document.querySelector("script[src], link[href], iframe")).toBeNull();
    expect(html).not.toContain("sourceMappingURL");
    expect(html).not.toContain("HANK_SESSION");
    expect(html).not.toContain("hank_session");
    expect(Buffer.byteLength(html)).toBeLessThanOrEqual(MAX_UNCOMPRESSED_BYTES);
    expect(gzipSync(html).byteLength).toBeLessThanOrEqual(MAX_GZIP_BYTES);
  });
});
