import { describe, expect, it } from "vitest";
import { annexBAccessUnitToAVCC, buildAVCDecoderConfigurationRecord, buildFMP4InitSegment, buildFMP4MediaSegment, splitAnnexBNALUnits } from "./fmp4";

const accessUnit = new Uint8Array([0,0,0,1,0x67,0x42,0xc0,0x1f,0xda,1, 0,0,0,1,0x68,0xce,6, 0,0,1,0x65,1,2,3]);

describe("desktop H.264 packaging", () => {
  it("parses Annex-B and builds avcC", () => {
    const nals = splitAnnexBNALUnits(accessUnit);
    expect(nals.map((nal) => nal[0] & 0x1f)).toEqual([7, 8, 5]);
    const avcC = buildAVCDecoderConfigurationRecord(nals[0], nals[1]);
    expect(Array.from(avcC.slice(0, 5))).toEqual([1, 0x42, 0xc0, 0x1f, 0xff]);
    expect(annexBAccessUnitToAVCC(accessUnit).byteLength).toBeGreaterThan(12);
  });

  it("wraps AVC in bounded fragmented MP4 boxes", () => {
    const avcC = buildAVCDecoderConfigurationRecord(splitAnnexBNALUnits(accessUnit)[0], splitAnnexBNALUnits(accessUnit)[1]);
    expect(new TextDecoder().decode(buildFMP4InitSegment(avcC, 640, 360))).toContain("ftyp");
    const media = buildFMP4MediaSegment(annexBAccessUnitToAVCC(accessUnit), 1, 0, 3_000, true);
    expect(new TextDecoder().decode(media)).toContain("moof");
    expect(media.byteLength).toBeLessThan(4 << 20);
  });
});
