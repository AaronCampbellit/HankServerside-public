import { describe, expect, it, vi } from "vitest";
import { DesktopDecoder, desktopPlaybackSupported } from "./DesktopDecoder";

describe("DesktopDecoder", () => {
  it("prefers supported WebCodecs and emits encoded chunks", async () => {
    const configure = vi.fn(), decode = vi.fn(), close = vi.fn(), reset = vi.fn();
    const decoder = new DesktopDecoder({
      webCodecs: { isConfigSupported: vi.fn().mockResolvedValue({ supported: true }), create: () => ({ configure, decode, close, reset }) },
    });
    await decoder.configure({ codec: "avc1.42c01f", width: 640, height: 360, description: new Uint8Array([1]) });
    expect(configure).toHaveBeenCalledWith(expect.objectContaining({ optimizeForLatency: true }));
    decoder.decode(new Uint8Array([0,0,0,1,0x65,1]), { timestamp: 0, duration: 33_333, keyframe: true });
    expect(decode).toHaveBeenCalled();
  });

  it("reports the live decoder queue and sampled frame health", async () => {
    const native = { decodeQueueSize: 7, configure: vi.fn(), decode: vi.fn(), close: vi.fn(), reset: vi.fn() };
    let output: ((frame: unknown) => void) | undefined;
    const decoder = new DesktopDecoder({ webCodecs: { isConfigSupported: vi.fn().mockResolvedValue({ supported: true }), create: callback => { output = callback; return native; } } });
    await decoder.configure({ codec: "avc1.42c01f", width: 640, height: 360, description: new Uint8Array([1]), generation: 2 });
    decoder.decode(new Uint8Array([0,0,0,1,0x65]), { timestamp: 0, duration: 33_333, keyframe: true, generation: 2 });
    output?.({ timestamp: 0, close: vi.fn() });
    decoder.decode(new Uint8Array([0,0,0,1,0x65]), { timestamp: 1, duration: 33_333, keyframe: true, generation: 1 });
    expect(decoder.healthSnapshot()).toEqual(expect.objectContaining({ decoderQueue: 7, decodedFrames: 1, droppedFrames: 1, playbackMode: "webcodecs" }));
  });

  it("refuses unsupported browsers before authorization", () => {
    expect(desktopPlaybackSupported({ VideoDecoder: undefined, MediaSource: undefined })).toBe(false);
  });

  it("uses the fragmented-MP4 adapter when WebCodecs is unavailable", async () => {
    const mse = { configure: vi.fn().mockResolvedValue(undefined), decode: vi.fn(), reset: vi.fn(), close: vi.fn() };
    const decoder = new DesktopDecoder({ mse });
    const config = { codec: "avc1.42c01f", width: 640, height: 360, description: new Uint8Array([1, 0x42, 0xc0, 0x1f]) };
    await decoder.configure(config);
    decoder.decode(new Uint8Array([0,0,0,1,0x65,1]), { timestamp: 0, duration: 33_333, keyframe: true });
    expect(mse.configure).toHaveBeenCalledWith(config); expect(mse.decode).toHaveBeenCalled();
  });

  it("resets on generation changes and rejects stale access units", async () => {
    const configure = vi.fn(), decode = vi.fn(), close = vi.fn(), reset = vi.fn();
    const decoder = new DesktopDecoder({
      webCodecs: { isConfigSupported: vi.fn().mockResolvedValue({ supported: true }), create: () => ({ configure, decode, close, reset }) },
    });
    await decoder.configure({ codec: "avc1.42c01f", width: 640, height: 360, description: new Uint8Array([1]), generation: 3, displayID: "display-1" });
    await decoder.configure({ codec: "avc1.42c01f", width: 1280, height: 720, description: new Uint8Array([1]), generation: 4, displayID: "display-1" });
    expect(reset).toHaveBeenCalled();
    expect(decoder.decode(new Uint8Array([0,0,0,1,0x65]), { timestamp: 0, duration: 33_333, keyframe: true, generation: 3 })).toBe(false);
    expect(decoder.decode(new Uint8Array([0,0,0,1,0x65]), { timestamp: 0, duration: 33_333, keyframe: true, generation: 4 })).toBe(true);
    expect(decode).toHaveBeenCalledTimes(1);
  });

  it("drops stale delta frames when WebCodecs falls behind and resumes at a keyframe", async () => {
    let configured = false;
    const native = {
      decodeQueueSize: 3,
      configure: vi.fn(() => { configured = true; }),
      decode: vi.fn(() => { if (!configured) throw new Error("Cannot call decode on an unconfigured codec."); }),
      close: vi.fn(),
      reset: vi.fn(() => { configured = false; }),
    };
    const decoder = new DesktopDecoder({ webCodecs: { isConfigSupported: vi.fn().mockResolvedValue({ supported: true }), create: () => native } });
    await decoder.configure({ codec: "avc1.42c01f", width: 640, height: 360, description: new Uint8Array([1]), generation: 1 });
    expect(decoder.decode(new Uint8Array([0,0,0,1,0x41]), { timestamp: 1, duration: 33_333, keyframe: false, generation: 1 })).toBe(false);
    expect(decoder.decode(new Uint8Array([0,0,0,1,0x65]), { timestamp: 2, duration: 33_333, keyframe: true, generation: 1 })).toBe(true);
    expect(native.reset).toHaveBeenCalled();
    expect(native.configure).toHaveBeenCalledTimes(2);
    expect(native.decode).toHaveBeenCalledTimes(1);
  });
});
