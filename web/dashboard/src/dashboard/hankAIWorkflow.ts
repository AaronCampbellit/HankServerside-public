import type { HankAIAttachment, HankAIResultCard } from "../api/hankAI";

export const MAX_HANK_AI_ATTACHMENT_BYTES = 100 * 1024 * 1024;

export type HankAIDraftAttachment = HankAIAttachment & {
  id: string;
  file: File;
};

function makeID(prefix: string): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `${prefix}-${crypto.randomUUID()}`;
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function attachmentKind(contentType: string): string {
  const normalized = contentType.toLowerCase();
  if (normalized.startsWith("image/")) return "image";
  if (normalized.includes("pdf")) return "pdf";
  return "document";
}

async function sha256Hex(file: File): Promise<string> {
  if (typeof crypto === "undefined" || !crypto.subtle) return "";
  const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function stageHankAIAttachments(files: File[]): Promise<{ attachments: HankAIDraftAttachment[]; rejected: string[] }> {
  const candidates = files.filter((file) => Boolean(file?.name));
  const rejected = candidates.filter((file) => file.size > MAX_HANK_AI_ATTACHMENT_BYTES).map((file) => file.name);
  const accepted = candidates.filter((file) => file.size <= MAX_HANK_AI_ATTACHMENT_BYTES);
  const attachments = await Promise.all(accepted.map(async (file) => {
    const contentType = file.type || "application/octet-stream";
    return {
      id: makeID("hank-upload"),
      client_attachment_id: makeID("client-attachment"),
      file,
      filename: file.name,
      content_type: contentType,
      size_bytes: file.size,
      checksum_sha256: await sha256Hex(file),
      kind: attachmentKind(contentType),
    };
  }));
  return { attachments, rejected };
}

export function attachmentPayload(attachment: HankAIDraftAttachment): HankAIAttachment {
  return {
    client_attachment_id: attachment.client_attachment_id,
    filename: attachment.filename,
    content_type: attachment.content_type,
    size_bytes: attachment.size_bytes,
    checksum_sha256: attachment.checksum_sha256,
    kind: attachment.kind,
  };
}

export function attachmentOnlyMessageText(attachments: HankAIDraftAttachment[]): string {
  return attachments.length === 1
    ? `Uploaded ${attachments[0].filename}.`
    : `Uploaded ${attachments.length} attachments.`;
}

export function formatHankAIBytes(bytes: number): string {
  const value = Number(bytes) || 0;
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB"];
  let size = value / 1024;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size.toFixed(size >= 10 ? 0 : 1)} ${units[unit]}`;
}

export function hankAIResultCardHref(card: HankAIResultCard): string {
  const kind = String(card.kind || "").toLowerCase();
  if (kind === "note" && card.note_id) {
    const params = new URLSearchParams({ note: card.note_id });
    return `/dashboard/profile-notes?${params.toString()}`;
  }
  if (kind === "file" && card.path) {
    const params = new URLSearchParams({ path: card.path });
    if (card.source_id) params.set("source_id", card.source_id);
    if (!card.is_directory) params.set("preview", "1");
    return `/dashboard/file-server?${params.toString()}`;
  }
  if (kind === "homeassistant") {
    const query = card.search_text || card.path;
    if (query) return `/dashboard/home-assistant?${new URLSearchParams({ query }).toString()}`;
  }
  return "";
}

export function hankAIResultImageURL(card: HankAIResultCard): string {
  const raw = String(card.image_url || "").trim();
  if (!raw) return "";
  try {
    const url = new URL(raw, window.location.origin);
    if (!["http:", "https:"].includes(url.protocol)) return "";
    if (String(card.kind || "").toLowerCase() === "media") {
      return `/v1/home/assistant/media-image?${new URLSearchParams({ url: raw }).toString()}`;
    }
    return url.origin === window.location.origin ? url.href : "";
  } catch {
    return "";
  }
}
