import { describe, expect, it } from "vitest";
import { arrayFrom, booleanFrom, objectFrom } from "./normalize";

describe("api normalization", () => {
  it("normalizes nullable arrays to stable empty arrays", () => {
    expect(arrayFrom(null)).toEqual([]);
    expect(arrayFrom(undefined)).toEqual([]);
    expect(arrayFrom([{ id: "one" }])).toEqual([{ id: "one" }]);
    expect(arrayFrom({ id: "not-array" })).toEqual([]);
  });

  it("normalizes nullable objects to stable empty objects", () => {
    expect(objectFrom(null)).toEqual({});
    expect(objectFrom(undefined)).toEqual({});
    expect(objectFrom({ ok: true })).toEqual({ ok: true });
    expect(objectFrom(["not-object"])).toEqual({});
  });

  it("normalizes booleans with a default", () => {
    expect(booleanFrom(true)).toBe(true);
    expect(booleanFrom(false, true)).toBe(false);
    expect(booleanFrom(null, true)).toBe(true);
  });
});
