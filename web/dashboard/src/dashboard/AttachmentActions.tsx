import { useEffect, useState } from "react";
import type { NoteAttachment } from "../api/profileNotes";
import type { OfflineNotesRepository } from "../offlineNotes/repository";

export function attachmentDownloadURL(attachment: NoteAttachment): string {
  const separator = attachment.download_url.includes("?") ? "&" : "?";
  return `${attachment.download_url}${separator}disposition=download`;
}

type AttachmentActionsProps = {
  attachment: NoteAttachment;
  repository?: Pick<OfflineNotesRepository, "cacheAttachment" | "readCachedAttachment"> | null;
  noteLocalKey?: string;
  online?: boolean;
};

export function AttachmentActions({
  attachment,
  repository = null,
  noteLocalKey = "",
  online = true,
}: AttachmentActionsProps) {
  const [objectURL, setObjectURL] = useState("");
  const [cacheChecked, setCacheChecked] = useState(!repository);
  const previewURL = attachment.preview_url || attachment.download_url;
  const canDirectPreview = attachment.content_type === "text/html" || attachment.content_type.startsWith("image/");
  const canObjectPreview = /image\/(?:png|jpeg|gif|webp|avif)/i.test(attachment.content_type)
    || (attachment.content_type === "text/html" && Boolean(attachment.preview_url));

  useEffect(() => {
    if (!repository) {
      setObjectURL("");
      setCacheChecked(true);
      return;
    }
    let cancelled = false;
    let createdURL = "";
    setCacheChecked(false);
    void (async () => {
      let blob: Blob | null = null;
      if (online) {
        try {
          const response = await fetch(previewURL, { credentials: "same-origin" });
          if (response.ok) {
            blob = await response.blob();
            await repository.cacheAttachment(noteLocalKey, attachment, blob);
          }
        } catch {
          // Fall through to an existing cached copy after a network failure.
        }
      }
      if (!blob) blob = await repository.readCachedAttachment(attachment.id);
      if (!cancelled && blob) {
        createdURL = URL.createObjectURL(blob);
        setObjectURL(createdURL);
      }
      if (!cancelled) setCacheChecked(true);
    })();
    return () => {
      cancelled = true;
      if (createdURL) URL.revokeObjectURL(createdURL);
    };
  }, [attachment, noteLocalKey, online, previewURL, repository]);

  const openURL = objectURL || (online ? previewURL : "");
  const downloadURL = objectURL || (online ? attachmentDownloadURL(attachment) : "");
  const canPreview = objectURL ? canObjectPreview : canDirectPreview;
  return (
    <span className="note-attachment-actions">
      <span className="note-attachment-name">{attachment.filename}</span>
      {canPreview && openURL ? <a aria-label={`Open ${attachment.filename}`} href={openURL} target="_blank" rel="noopener noreferrer">Open</a> : null}
      {downloadURL ? <a aria-label={`Download ${attachment.filename}`} href={downloadURL} download={attachment.filename}>Download</a> : null}
      {!online && cacheChecked && !objectURL ? <span className="note-attachment-unavailable">Available when connected</span> : null}
    </span>
  );
}
