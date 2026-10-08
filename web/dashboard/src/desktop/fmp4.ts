function bytes(...values: number[]): Uint8Array { return new Uint8Array(values); }
function uint32(value: number): Uint8Array { const result = new Uint8Array(4); new DataView(result.buffer).setUint32(0, value, false); return result; }
function text(value: string): Uint8Array { return new TextEncoder().encode(value); }
function join(...parts: Uint8Array[]): Uint8Array { const result = new Uint8Array(parts.reduce((sum, part) => sum + part.byteLength, 0)); let offset = 0; for (const part of parts) { result.set(part, offset); offset += part.byteLength; } return result; }
function box(type: string, ...payload: Uint8Array[]): Uint8Array { const body = join(...payload); return join(uint32(body.byteLength + 8), text(type), body); }
function fullBox(type: string, version: number, flags: number, ...payload: Uint8Array[]) { return box(type, bytes(version, flags >> 16 & 255, flags >> 8 & 255, flags & 255), ...payload); }

export function splitAnnexBNALUnits(data: Uint8Array): Uint8Array[] {
  const starts: Array<{ index: number; length: number }> = [];
  for (let index = 0; index + 3 < data.length; index++) {
    if (data[index] === 0 && data[index + 1] === 0 && data[index + 2] === 1) { starts.push({ index, length: 3 }); index += 2; }
    else if (data[index] === 0 && data[index + 1] === 0 && data[index + 2] === 0 && data[index + 3] === 1) { starts.push({ index, length: 4 }); index += 3; }
  }
  return starts.map((start, index) => data.slice(start.index + start.length, starts[index + 1]?.index ?? data.length)).filter(nal => nal.byteLength > 0);
}

export function buildAVCDecoderConfigurationRecord(sps: Uint8Array, pps: Uint8Array): Uint8Array {
  if ((sps[0] & 0x1f) !== 7 || (pps[0] & 0x1f) !== 8 || sps.length < 4) throw new Error("desktop_avc_parameters_invalid");
  return join(bytes(1, sps[1], sps[2], sps[3], 0xff, 0xe1), bytes(sps.length >> 8, sps.length & 255), sps, bytes(1, pps.length >> 8, pps.length & 255), pps);
}

export function annexBAccessUnitToAVCC(data: Uint8Array): Uint8Array { return join(...splitAnnexBNALUnits(data).map(nal => join(uint32(nal.byteLength), nal))); }

export function buildFMP4InitSegment(avcC: Uint8Array, width: number, height: number): Uint8Array {
  const ftyp = box("ftyp", text("isom"), uint32(0x200), text("isomiso6avc1mp41"));
  const mvhd = fullBox("mvhd", 0, 0, uint32(0), uint32(0), uint32(90_000), uint32(0), bytes(0,1,0,0,1,0,0,0), new Uint8Array(68), uint32(2));
  const tkhd = fullBox("tkhd", 0, 7, uint32(0), uint32(0), uint32(1), uint32(0), uint32(0), new Uint8Array(52), uint32(width << 16), uint32(height << 16));
  const mdhd = fullBox("mdhd", 0, 0, uint32(0), uint32(0), uint32(90_000), uint32(0), bytes(0x55,0xc4,0,0));
  const hdlr = fullBox("hdlr", 0, 0, uint32(0), text("vide"), new Uint8Array(12), text("Hank Desktop\0"));
  const avc1 = box("avc1", new Uint8Array(24), bytes(width >> 8, width & 255, height >> 8, height & 255), uint32(0x00480000), uint32(0x00480000), uint32(0), bytes(0,1), new Uint8Array(32), bytes(0,0x18,0xff,0xff), box("avcC", avcC));
  const stsd = fullBox("stsd", 0, 0, uint32(1), avc1);
  const stbl = box("stbl", stsd, fullBox("stts",0,0,uint32(0)), fullBox("stsc",0,0,uint32(0)), fullBox("stsz",0,0,uint32(0),uint32(0)), fullBox("stco",0,0,uint32(0)));
  const minf = box("minf", fullBox("vmhd",0,1,new Uint8Array(8)), box("dinf", fullBox("dref",0,0,uint32(1),fullBox("url ",0,1))), stbl);
  const trak = box("trak", tkhd, box("mdia", mdhd, hdlr, minf));
  const mvex = box("mvex", fullBox("trex",0,0,uint32(1),uint32(1),uint32(0),uint32(0),uint32(0)));
  return join(ftyp, box("moov", mvhd, trak, mvex));
}

export function buildFMP4MediaSegment(sample: Uint8Array, sequence: number, decodeTime: number, duration: number, keyframe: boolean): Uint8Array {
  if (sample.byteLength > 4 << 20) throw new Error("desktop_video_access_unit_oversized");
  const mfhd = fullBox("mfhd",0,0,uint32(sequence));
  const tfhd = fullBox("tfhd",0,0x020000,uint32(1));
  const tfdt = fullBox("tfdt",1,0,uint32(Math.floor(decodeTime / 0x1_0000_0000)),uint32(decodeTime >>> 0));
  let trun = fullBox("trun",0,0x000701,uint32(1),uint32(0),uint32(duration),uint32(sample.byteLength),uint32(keyframe ? 0x02000000 : 0x01010000));
  let moof = box("moof", mfhd, box("traf", tfhd, tfdt, trun));
  const offset = moof.byteLength + 8;
  trun = fullBox("trun",0,0x000701,uint32(1),uint32(offset),uint32(duration),uint32(sample.byteLength),uint32(keyframe ? 0x02000000 : 0x01010000));
  moof = box("moof", mfhd, box("traf", tfhd, tfdt, trun));
  return join(moof, box("mdat", sample));
}
