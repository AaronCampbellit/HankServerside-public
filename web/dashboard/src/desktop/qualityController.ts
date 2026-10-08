export type DesktopQualityName = "low" | "balanced" | "high" | "ultra";
export interface DesktopQualityLevel { name: DesktopQualityName; scale: number; fps: number; bitrateBPS: number }
export interface DesktopHealthSample { atMS: number; rttMS: number; decoderQueue: number; decodedFrames: number; droppedFrames: number; senderQueueBytes: number; relayBackpressureCount: number }
export interface DesktopQualityDecision extends DesktopQualityLevel { level: DesktopQualityName; generation: number; forceKeyframe: true }

export const desktopQualityLevels: readonly DesktopQualityLevel[] = Object.freeze([
  { name: "low", scale: .5, fps: 15, bitrateBPS: 1_000_000 },
  { name: "balanced", scale: .75, fps: 30, bitrateBPS: 4_000_000 },
  { name: "high", scale: 1, fps: 30, bitrateBPS: 8_000_000 },
  { name: "ultra", scale: 1, fps: 60, bitrateBPS: 20_000_000 },
]);

export class DesktopQualityController {
  private current = 1; private maximum = 3; private generation = 0; private unhealthy = 0;
  private lastDowngradeMS = Number.NEGATIVE_INFINITY; private healthySinceMS: number | null = null;

  reset(): DesktopQualityDecision {
    this.current = Math.min(1, this.maximum); this.unhealthy = 0; this.healthySinceMS = null; this.lastDowngradeMS = Number.NEGATIVE_INFINITY;
    return this.decision();
  }
  setMaximum(name: DesktopQualityName, atMS: number): DesktopQualityDecision | null {
    this.maximum = desktopQualityLevels.findIndex(level => level.name === name);
    if (this.current === this.maximum) return null;
    this.current = this.maximum; this.unhealthy = 0; this.lastDowngradeMS = atMS; this.healthySinceMS = null; return this.decision();
  }
  observe(sample: DesktopHealthSample): DesktopQualityDecision | null {
    const total = sample.decodedFrames + sample.droppedFrames;
    const unhealthy = sample.rttMS >= 180 || sample.decoderQueue >= 3 || sample.senderQueueBytes > (512 << 10) || sample.relayBackpressureCount > 0 || (total > 0 && sample.droppedFrames / total >= .05);
    if (unhealthy) {
      this.healthySinceMS = null; this.unhealthy++;
      if (this.unhealthy >= 2 && this.current > 0 && sample.atMS - this.lastDowngradeMS >= 5_000) {
        this.current--; this.unhealthy = 0; this.lastDowngradeMS = sample.atMS; return this.decision();
      }
      return null;
    }
    this.unhealthy = 0; this.healthySinceMS ??= sample.atMS;
    if (this.current < this.maximum && sample.atMS - this.healthySinceMS >= 20_000) {
      this.current++; this.healthySinceMS = sample.atMS; return this.decision();
    }
    return null;
  }
  private decision(): DesktopQualityDecision {
    const level = desktopQualityLevels[this.current];
    return { ...level, level: level.name, generation: ++this.generation, forceKeyframe: true };
  }
}
