import { annexBAccessUnitToAVCC, buildFMP4InitSegment, buildFMP4MediaSegment } from "./fmp4";

export interface DesktopDecoderConfiguration { codec: string; width: number; height: number; description: Uint8Array; generation?: number; displayID?: string }
export interface DesktopAccessUnitMetadata { timestamp: number; duration: number; keyframe: boolean; generation?: number }
interface DecoderLike { readonly decodeQueueSize?: number; configure(config: unknown): void; decode(chunk: unknown): void; reset(): void; close(): void }
interface DecoderDependencies { webCodecs?: { isConfigSupported(config: unknown): Promise<{ supported?: boolean }>; create(output: (frame: unknown) => void): DecoderLike }; mse?: DesktopMSEAdapter }
interface DesktopDecoderTargets { canvas?: HTMLCanvasElement | null; video?: HTMLVideoElement | null }
interface DesktopMSEAdapter { configure(value: DesktopDecoderConfiguration): Promise<void>; decode(accessUnit: Uint8Array, metadata: DesktopAccessUnitMetadata): void; reset(): void; close(): void }

export function desktopPlaybackSupported(scope: { VideoDecoder?: unknown; MediaSource?: unknown } = globalThis as never): boolean { return typeof scope.VideoDecoder !== "undefined" || typeof scope.MediaSource !== "undefined"; }

export class DesktopDecoder {
  private decoder: DecoderLike | null = null;
  private webCodecsConfig: unknown | null = null;
  private mse: DesktopMSEAdapter | null = null;
  private generation = 0;
  private displayID = "";
  private decodedFrames = 0;
  private droppedFrames = 0;
  private awaitingKeyframe = false;
  private enqueuedAt = new Map<number, number>();
  private renderLatencyTotalMS = 0;
  private renderLatencyMaxMS = 0;
  private renderLatencySamples = 0;
  constructor(private readonly dependencies: DecoderDependencies = {}, private readonly targets: DesktopDecoderTargets = {}) {}

  async configure(value: DesktopDecoderConfiguration): Promise<void> {
    const changedGeneration = value.generation !== undefined && this.generation > 0 &&
      (value.generation !== this.generation || (value.displayID ?? "") !== this.displayID);
    if (changedGeneration) {
      this.reset();
      this.decoder?.close();
      this.decoder = null;
      this.mse?.close();
      this.mse = null;
    }
    if (value.generation !== undefined) this.generation = value.generation;
    if (value.displayID !== undefined) this.displayID = value.displayID;
    const webCodecs = this.dependencies.webCodecs ?? browserWebCodecs();
    if (!webCodecs) {
      const mse = this.dependencies.mse ?? browserMSE(this.targets.video);
      if (!mse) throw new Error("desktop_playback_unsupported");
      if (this.targets.canvas) this.targets.canvas.hidden = true;
      if (this.targets.video) this.targets.video.hidden = false;
      this.mse = mse; await mse.configure(value); return;
    }
    if (this.targets.canvas) this.targets.canvas.hidden = false;
    if (this.targets.video) this.targets.video.hidden = true;
    const config = { codec: value.codec, codedWidth: value.width, codedHeight: value.height, description: value.description.slice().buffer, optimizeForLatency: true };
    if (!(await webCodecs.isConfigSupported(config)).supported) throw new Error("desktop_codec_unsupported");
    this.decoder?.close(); this.decoder = webCodecs.create(frame => this.draw(frame)); this.decoder.configure(config);
    this.webCodecsConfig = config;
  }

  decode(accessUnit: Uint8Array, metadata: DesktopAccessUnitMetadata): boolean {
    if (metadata.generation !== undefined && this.generation > 0 && metadata.generation !== this.generation) { this.droppedFrames++; return false; }
    if (this.mse) { this.mse.decode(accessUnit, metadata); this.decodedFrames++; return true; }
    if (!this.decoder) throw new Error("desktop_decoder_not_configured");
    if ((this.decoder.decodeQueueSize ?? 0) >= 3) this.awaitingKeyframe = true;
    if (this.awaitingKeyframe) {
      if (!metadata.keyframe) { this.droppedFrames++; return false; }
      this.decoder.reset();
      if (!this.webCodecsConfig) throw new Error("desktop_decoder_not_configured");
      this.decoder.configure(this.webCodecsConfig);
      this.enqueuedAt.clear();
      this.awaitingKeyframe = false;
    }
    const Chunk = (globalThis as unknown as { EncodedVideoChunk?: new (value: unknown) => unknown }).EncodedVideoChunk;
    const value = { type: metadata.keyframe ? "key" : "delta", timestamp: metadata.timestamp, duration: metadata.duration, data: annexBAccessUnitToAVCC(accessUnit) };
    this.enqueuedAt.set(metadata.timestamp, nowMS());
    this.decoder.decode(Chunk ? new Chunk(value) : value);
    return true;
  }
  healthSnapshot(): { decoderQueue: number; decodedFrames: number; droppedFrames: number; renderLatencyMS: number; renderLatencyMaxMS: number; playbackMode: "webcodecs" | "mse" | "none" } {
    return { decoderQueue: Math.max(0, this.decoder?.decodeQueueSize ?? 0), decodedFrames: this.decodedFrames, droppedFrames: this.droppedFrames,
      renderLatencyMS: this.renderLatencySamples > 0 ? this.renderLatencyTotalMS / this.renderLatencySamples : 0,
      renderLatencyMaxMS: this.renderLatencyMaxMS, playbackMode: this.decoder ? "webcodecs" : this.mse ? "mse" : "none" };
  }
  reset(): void { this.decoder?.reset(); this.mse?.reset(); this.awaitingKeyframe = false; this.enqueuedAt.clear(); }
  clearRenderedFrame(): void {
    this.decoder?.reset(); this.decoder?.close(); this.decoder = null;
    this.mse?.close(); this.mse = null;
    const canvas = this.targets.canvas, context = canvas?.getContext("2d");
    if (canvas && context) context.clearRect(0, 0, canvas.width, canvas.height);
    const video = this.targets.video;
    if (video) { video.pause(); video.removeAttribute("src"); video.load(); }
  }
  close(): void {
    this.decoder?.close(); this.decoder = null; this.webCodecsConfig = null; this.mse?.close(); this.mse = null; this.generation = 0; this.displayID = "";
    this.decodedFrames = 0; this.droppedFrames = 0; this.awaitingKeyframe = false; this.enqueuedAt.clear();
    this.renderLatencyTotalMS = 0; this.renderLatencyMaxMS = 0; this.renderLatencySamples = 0;
  }
  private draw(frame: unknown): void {
    this.decodedFrames++;
    const value = frame as { displayWidth?: number; displayHeight?: number; timestamp?: number; close?: () => void };
    if (typeof value.timestamp === "number") {
      const started = this.enqueuedAt.get(value.timestamp);
      if (started !== undefined) {
        const latency = Math.max(0, nowMS() - started);
        this.enqueuedAt.delete(value.timestamp);
        this.renderLatencyTotalMS += latency; this.renderLatencyMaxMS = Math.max(this.renderLatencyMaxMS, latency); this.renderLatencySamples++;
      }
    }
    const canvas = this.targets.canvas, context = canvas?.getContext("2d");
    if (canvas && context) { canvas.width = value.displayWidth || canvas.width; canvas.height = value.displayHeight || canvas.height; context.drawImage(frame as CanvasImageSource, 0, 0, canvas.width, canvas.height); }
    value.close?.();
  }
}

function nowMS(): number { return globalThis.performance?.now?.() ?? Date.now(); }

function browserWebCodecs(): DecoderDependencies["webCodecs"] | undefined {
  const Decoder = (globalThis as unknown as { VideoDecoder?: { new(value: unknown): DecoderLike; isConfigSupported(value: unknown): Promise<{ supported?: boolean }> } }).VideoDecoder;
  if (!Decoder) return undefined;
  return { isConfigSupported: value => Decoder.isConfigSupported(value), create: output => new Decoder({ output, error: () => {} }) };
}

function browserMSE(video?: HTMLVideoElement | null): DesktopMSEAdapter | undefined {
  const Source = globalThis.MediaSource;
  if (!video || typeof Source === "undefined") return undefined;
  let source: MediaSource | null = null, buffer: SourceBuffer | null = null, objectURL = "", sequence = 1, queue = Promise.resolve();
  const append = (value: Uint8Array) => { queue = queue.then(() => new Promise<void>((resolve, reject) => {
    if (!buffer) return reject(new Error("desktop_mse_not_configured"));
    const done = () => { buffer?.removeEventListener("updateend", done); resolve(); };
    buffer.addEventListener("updateend", done, { once: true });
    try { buffer.appendBuffer(value.slice().buffer); } catch (error) { reject(error); }
  })); };
  return {
    async configure(value) {
      const mime = `video/mp4; codecs="${value.codec}"`;
      if (!Source.isTypeSupported(mime)) throw new Error("desktop_codec_unsupported");
      source = new Source(); objectURL = URL.createObjectURL(source); video.src = objectURL;
      await new Promise<void>((resolve, reject) => { source?.addEventListener("sourceopen", () => { try { buffer = source!.addSourceBuffer(mime); resolve(); } catch (error) { reject(error); } }, { once: true }); });
      append(buildFMP4InitSegment(value.description, value.width, value.height));
    },
    decode(accessUnit, metadata) { append(buildFMP4MediaSegment(annexBAccessUnitToAVCC(accessUnit), sequence++, Math.round(metadata.timestamp * 0.09), Math.max(1, Math.round(metadata.duration * 0.09)), metadata.keyframe)); },
    reset() { sequence = 1; buffer?.abort(); },
    close() { buffer?.abort(); if (source?.readyState === "open") source.endOfStream(); if (objectURL) URL.revokeObjectURL(objectURL); video.removeAttribute("src"); video.load(); source = null; buffer = null; },
  };
}
