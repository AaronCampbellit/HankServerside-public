import { useEffect, useState } from "react";
import { noteAttachmentsClient, type NoteAttachmentInventory } from "../api/noteAttachments";
import { useConfirmDialog } from "../ui/primitives";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; inventory: NoteAttachmentInventory; message: string; deletingID: string };

function formatBytes(bytes: number): string {
  if (bytes < 1_024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1_024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1_024; index += 1) {
    value /= 1_024;
    unit = units[index];
  }
  return `${Number.isInteger(value) ? value : value.toFixed(1)} ${unit}`;
}

function fileCount(count: number): string {
  return `${count.toLocaleString()} ${count === 1 ? "file" : "files"}`;
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Attachments could not be loaded.";
}

export function AttachmentsSettings() {
  const [state, setState] = useState<State>({ status: "loading" });
  const dialog = useConfirmDialog();

  async function load(message = "") {
    try {
      const inventory = await noteAttachmentsClient.load();
      setState({ status: "ready", inventory, message, deletingID: "" });
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => { void load(); }, []);

  async function removeAttachment(attachmentID: string) {
    if (state.status !== "ready") return;
    const attachment = state.inventory.attachments.find((item) => item.id === attachmentID);
    if (!attachment) return;
    const confirmed = await dialog.confirm({
      title: `Delete ${attachment.filename}?`,
      message: "This permanently deletes the stored file and removes it everywhere it appears in Notes.",
      detail: `${formatBytes(attachment.size_bytes)} · ${attachment.note_title || attachment.note_id}\n${attachment.reference_count} ${attachment.reference_count === 1 ? "reference" : "references"}`,
      confirmLabel: "Delete file",
      tone: "danger",
    });
    if (!confirmed) return;
    setState({ ...state, deletingID: attachmentID, message: "" });
    try {
      await noteAttachmentsClient.remove(attachmentID);
      await load(`${attachment.filename} was permanently deleted.`);
    } catch (error) {
      setState({ ...state, deletingID: "", message: errorMessage(error) });
    }
  }

  return (
    <section className="settings-page attachment-settings" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Admin</p>
          <h1 id="route-title">Attachments</h1>
          <p className="meta-line">Files uploaded to Notes are stored in the Notes volume; their ownership, size, and references are tracked in PostgreSQL.</p>
        </div>
      </header>

      {state.status === "loading" ? <p className="loading-state">Loading attachments...</p> : null}
      {state.status === "error" ? <p className="error-state">{state.message}</p> : null}
      {state.status === "ready" ? (
        <>
          <div className="attachment-summary" aria-label="Attachment storage summary">
            <div><span>Total files</span><strong>{fileCount(state.inventory.total_files)}</strong></div>
            <div><span>Total storage</span><strong>{formatBytes(state.inventory.total_bytes)}</strong></div>
          </div>
          {state.message ? <p className="status-message" role="status">{state.message}</p> : null}
          {state.inventory.attachments.length === 0 ? (
            <div className="empty-state"><strong>No note attachments</strong><span>Files pasted or uploaded into Notes will appear here.</span></div>
          ) : (
            <div className="attachment-inventory" aria-label="Stored note attachments">
              {state.inventory.attachments.map((attachment) => (
                <article className="attachment-inventory-row" aria-label={attachment.filename} key={attachment.id}>
                  <div className="attachment-file-mark" aria-hidden="true">{attachment.content_type.startsWith("image/") ? "IMG" : "FILE"}</div>
                  <div className="attachment-inventory-main">
                    <div className="attachment-inventory-title">
                      <strong>{attachment.filename}</strong>
                      {attachment.reference_count === 0 ? <span className="warning-badge">Unreferenced</span> : null}
                    </div>
                    <span>{attachment.note_title || attachment.note_id}</span>
                    <small>{attachment.contexts.length ? attachment.contexts.join(" · ") : `${attachment.note_scope} note`} · {attachment.owner_email}</small>
                  </div>
                  <div className="attachment-inventory-meta">
                    <strong>{formatBytes(attachment.size_bytes)}</strong>
                    <span>{attachment.content_type || "application/octet-stream"}</span>
                    <time dateTime={attachment.created_at}>{new Date(attachment.created_at).toLocaleDateString()}</time>
                  </div>
                  <div className="attachment-inventory-actions">
                    <a className="secondary button-link" href={attachment.download_url} target="_blank" rel="noreferrer" aria-label={`Open ${attachment.filename}`}>Open</a>
                    <button type="button" className="danger-outline" disabled={state.deletingID === attachment.id} onClick={() => void removeAttachment(attachment.id)} aria-label={`Delete ${attachment.filename}`}>Delete</button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </>
      ) : null}
    </section>
  );
}
