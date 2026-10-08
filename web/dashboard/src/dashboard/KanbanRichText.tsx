import type { ReactNode } from "react";
import type { NoteAttachment } from "../api/profileNotes";

function safeHref(value: string): string {
  const href = value.trim();
  return /^(https?:|mailto:|\/|#)/i.test(href) ? href : "";
}

function attachmentForReference(reference: string, attachments: NoteAttachment[]): NoteAttachment | undefined {
  const match = /^hank-note-attachment:\/\/(.+)$/i.exec(reference.trim());
  if (!match) return undefined;
  const attachmentID = match[1].split(/[?#]/, 1)[0];
  return attachments.find((attachment) => attachment.id === attachmentID);
}

function renderInline(value: string, attachments: NoteAttachment[], keyPrefix: string): ReactNode[] {
  const tokenPattern = /(!\[([^\]]*)\]\(([^)]+)\)|\[([^\]]+)\]\(([^)]+)\)|\*\*([^*\n]+)\*\*|_([^_\n]+)_)/g;
  const nodes: ReactNode[] = [];
  let cursor = 0;
  let tokenIndex = 0;
  for (const match of value.matchAll(tokenPattern)) {
    const index = match.index || 0;
    if (index > cursor) nodes.push(value.slice(cursor, index));
    const key = `${keyPrefix}-${tokenIndex}`;
    if (match[2] !== undefined) {
      const attachment = attachmentForReference(match[3], attachments);
      const src = attachment?.download_url || safeHref(match[3]);
      if (src) nodes.push(<img className="kanban-rich-image" key={key} src={src} alt={match[2] || attachment?.filename || "Task attachment"} />);
      else nodes.push(match[2] || "Attachment");
    } else if (match[4] !== undefined) {
	  const attachment = attachmentForReference(match[5], attachments);
	  const href = attachment?.preview_url || attachment?.download_url || safeHref(match[5]);
      nodes.push(href ? <a key={key} href={href} target="_blank" rel="noopener noreferrer" onClick={(event) => event.stopPropagation()}>{match[4]}</a> : match[4]);
    } else if (match[6] !== undefined) {
      nodes.push(<strong className="kanban-rich-strong" key={key}>{match[6]}</strong>);
    } else if (match[7] !== undefined) {
      nodes.push(<em key={key}>{match[7]}</em>);
    }
    cursor = index + match[0].length;
    tokenIndex += 1;
  }
  if (cursor < value.length) nodes.push(value.slice(cursor));
  return nodes;
}

export function KanbanRichText({ value, attachments, maxLines }: { value: string; attachments: NoteAttachment[]; maxLines?: number }) {
  if (!value.trim()) return null;
  const lines = value.split(/\r?\n/).filter(Boolean);
  const visibleLines = maxLines === undefined ? lines : lines.slice(0, maxLines);
  return (
    <div className="kanban-rich-text">
      {visibleLines.map((line, index) => {
        const bullet = /^[-*]\s+/.test(line);
        const content = line.replace(/^[-*]\s+/, "");
        return bullet
          ? <p className="kanban-card-bullet" key={`${index}-${line}`}>{renderInline(content, attachments, `bullet-${index}`)}</p>
          : <p key={`${index}-${line}`}>{renderInline(content, attachments, `line-${index}`)}</p>;
      })}
    </div>
  );
}
