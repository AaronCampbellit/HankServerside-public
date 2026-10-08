import { exactBuffer } from "./base64url";
import type { DesktopDirectionalMaterial } from "./crypto";

export enum DesktopRecordDirection { BrowserToAgent = 1, AgentToBrowser = 2 }
const recordVersion = 1, headerLength = 18, tagLength = 16;

interface EpochKeys { browserToAgent: CryptoKey; agentToBrowser: CryptoKey; browserNonce: Uint8Array; agentNonce: Uint8Array }

export class DesktopRecordLayer {
  private sendSequence = 0n;
  private receiveSequence = 0n;
  private priorEpochPresent = false;

  private constructor(private epoch: number, private keys: EpochKeys, private outboundDirection: DesktopRecordDirection) {}

  static async create(epoch: number, material: DesktopDirectionalMaterial, outboundDirection = DesktopRecordDirection.BrowserToAgent): Promise<DesktopRecordLayer> {
    if (epoch < 1) throw new Error("desktop_epoch_invalid");
    return new DesktopRecordLayer(epoch, await importKeys(material), outboundDirection);
  }

  async encrypt(plaintext: Uint8Array): Promise<Uint8Array> {
    const direction = this.outboundDirection, key = direction === 1 ? this.keys.browserToAgent : this.keys.agentToBrowser;
    const prefix = direction === 1 ? this.keys.browserNonce : this.keys.agentNonce;
    const header = encodeHeader(direction, this.epoch, this.sendSequence, plaintext.byteLength + tagLength);
    const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce(prefix, this.sendSequence), additionalData: exactBuffer(header), tagLength: 128 }, key, exactBuffer(plaintext)));
    this.sendSequence++;
    const record = new Uint8Array(headerLength + ciphertext.byteLength); record.set(header); record.set(ciphertext, headerLength); return record;
  }

  async decrypt(record: Uint8Array): Promise<Uint8Array> {
    if (record.byteLength < headerLength + tagLength) throw new Error("desktop_record_invalid");
    const view = new DataView(record.buffer, record.byteOffset, record.byteLength), direction = record[1] as DesktopRecordDirection;
    const epoch = view.getUint32(2, false), sequence = view.getBigUint64(6, false), length = view.getUint32(14, false);
    if (record[0] !== recordVersion || (direction !== 1 && direction !== 2) || length + headerLength !== record.byteLength) throw new Error("desktop_record_invalid");
    if (direction === this.outboundDirection) throw new Error("desktop_direction_mismatch");
    if (epoch !== this.epoch) throw new Error("desktop_epoch_mismatch");
    if (sequence !== this.receiveSequence) throw new Error("desktop_sequence_mismatch");
    const key = direction === 1 ? this.keys.browserToAgent : this.keys.agentToBrowser, prefix = direction === 1 ? this.keys.browserNonce : this.keys.agentNonce;
    try {
      const plaintext = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: nonce(prefix, sequence), additionalData: exactBuffer(record.slice(0, headerLength)), tagLength: 128 }, key, exactBuffer(record.slice(headerLength))));
      this.receiveSequence++;
      return plaintext;
    } catch { throw new Error("desktop_invalid_tag"); }
  }

  async replaceEpoch(epoch: number, material: DesktopDirectionalMaterial): Promise<void> {
    if (epoch !== this.epoch + 1) throw new Error("desktop_epoch_transition_invalid");
    this.priorEpochPresent = true;
    this.keys.browserNonce.fill(0); this.keys.agentNonce.fill(0);
    this.keys = await importKeys(material); this.epoch = epoch; this.sendSequence = 0n; this.receiveSequence = 0n;
    this.priorEpochPresent = false;
  }

  debugKeyState() { return { epoch: this.epoch, priorEpochPresent: this.priorEpochPresent }; }
}

async function importKeys(material: DesktopDirectionalMaterial): Promise<EpochKeys> {
  return {
    browserToAgent: await crypto.subtle.importKey("raw", exactBuffer(material.browserToAgentKey), { name: "AES-GCM" }, false, ["encrypt", "decrypt"]),
    agentToBrowser: await crypto.subtle.importKey("raw", exactBuffer(material.agentToBrowserKey), { name: "AES-GCM" }, false, ["encrypt", "decrypt"]),
    browserNonce: material.browserNoncePrefix.slice(), agentNonce: material.agentNoncePrefix.slice(),
  };
}

function encodeHeader(direction: DesktopRecordDirection, epoch: number, sequence: bigint, ciphertextLength: number): Uint8Array {
  const output = new Uint8Array(headerLength), view = new DataView(output.buffer); output[0] = recordVersion; output[1] = direction;
  view.setUint32(2, epoch, false); view.setBigUint64(6, sequence, false); view.setUint32(14, ciphertextLength, false); return output;
}

function nonce(prefix: Uint8Array, sequence: bigint): ArrayBuffer {
  const value = new Uint8Array(12); value.set(prefix, 0); new DataView(value.buffer).setBigUint64(4, sequence, false); return value.buffer;
}
