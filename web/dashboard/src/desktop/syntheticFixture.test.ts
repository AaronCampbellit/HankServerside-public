import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import metadata from "../../../../schemas/desktop/v1/synthetic-desktop-640x360.json";

describe("committed synthetic desktop clip", () => {
  it("matches its immutable hash and access-unit inventory", () => {
    const clip = readFileSync("../../schemas/desktop/v1/synthetic-desktop-640x360.h264");
    expect(clip.byteLength).toBe(metadata.byte_length);
    expect(createHash("sha256").update(clip).digest("hex")).toBe(metadata.sha256);
    expect(metadata.access_units).toHaveLength(60);
    expect(metadata.access_units.filter(unit => unit.keyframe).map(unit => unit.index)).toEqual(metadata.keyframe_indexes);
    expect(metadata.access_units.at(-1)!.offset + metadata.access_units.at(-1)!.length).toBe(clip.byteLength);
  });
});
