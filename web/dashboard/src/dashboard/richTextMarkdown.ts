type MarkdownRenderOptions = {
  resolveImage?: (target: string, alt: string) => string;
  resolveLink?: (target: string, text: string) => string;
};

function escapeHTML(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function renderInlineMarkdown(value: string, options: MarkdownRenderOptions): string {
  return escapeHTML(value)
    .replace(/!\[([^\]]*)\]\(([^)\n]+)\)/g, (_match, alt: string, escapedTarget: string) => {
      const target = escapedTarget.replace(/&amp;/g, "&").trim();
      const src = options.resolveImage?.(target, alt) || "";
      if (!src) return alt;
      return `<img src="${escapeHTML(src)}" alt="${alt}" data-markdown-target="${escapeHTML(target)}">`;
    })
    .replace(/&lt;u&gt;([\s\S]*?)&lt;\/u&gt;/g, "<u>$1</u>")
    .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^\w])_([^_\n]+)_/g, "$1<em>$2</em>")
    .replace(/\[([^\]\n]+)\]\(([^)\n]+)\)/g, (_match, text: string, href: string) => {
      const normalizedHref = href.replace(/&amp;/g, "&").trim();
	  const resolvedHref = options.resolveLink?.(normalizedHref, text) || normalizedHref;
	  if (!/^(https?:|mailto:|\/|#)/i.test(resolvedHref)) return text;
	  return `<a href="${escapeHTML(resolvedHref)}" data-markdown-target="${escapeHTML(normalizedHref)}" rel="noreferrer">${text}</a>`;
    });
}

export function markdownToHTML(body: string, options: MarkdownRenderOptions = {}): string {
  const lines = body.split(/\r?\n/);
  const html: string[] = [];
  let list: "ul" | "ol" | "" = "";
  const closeList = () => {
    if (!list) return;
    html.push(`</${list}>`);
    list = "";
  };

  lines.forEach((line) => {
    const heading = /^(#{1,3})\s+(.*)$/.exec(line);
    if (heading) {
      closeList();
      const level = heading[1].length;
      html.push(`<h${level}>${renderInlineMarkdown(heading[2], options) || "<br>"}</h${level}>`);
      return;
    }
    const unordered = /^\s*[-*](?:\s+(.*))?$/.exec(line);
    if (unordered) {
      if (list !== "ul") {
        closeList();
        html.push("<ul>");
        list = "ul";
      }
      html.push(`<li>${renderInlineMarkdown(unordered[1] || "", options) || "<br>"}</li>`);
      return;
    }
    const ordered = /^\s*\d+\.(?:\s+(.*))?$/.exec(line);
    if (ordered) {
      if (list !== "ol") {
        closeList();
        html.push("<ol>");
        list = "ol";
      }
      html.push(`<li>${renderInlineMarkdown(ordered[1] || "", options) || "<br>"}</li>`);
      return;
    }
    closeList();
    html.push(`<p>${line ? renderInlineMarkdown(line, options) : "<br>"}</p>`);
  });
  closeList();
  return html.join("");
}

function inlineHTMLToMarkdown(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent || "";
  if (node.nodeType !== Node.ELEMENT_NODE) return "";
  const element = node as HTMLElement;
  const inner = Array.from(element.childNodes).map(inlineHTMLToMarkdown).join("");
  switch (element.tagName) {
    case "B":
    case "STRONG":
      return inner.trim() ? `**${inner}**` : inner;
    case "I":
    case "EM":
      return inner.trim() ? `_${inner}_` : inner;
    case "U":
      return inner.trim() ? `<u>${inner}</u>` : inner;
    case "A":
	  return inner.trim() ? `[${inner}](${element.dataset.markdownTarget || element.getAttribute("href") || ""})` : inner;
    case "IMG":
      return `![${element.getAttribute("alt") || ""}](${element.dataset.markdownTarget || element.getAttribute("src") || ""})`;
    case "BR":
      return "\n";
    default:
      return inner;
  }
}

const BLOCK_TAGS = new Set(["H1", "H2", "H3", "UL", "OL", "P", "DIV", "BLOCKQUOTE"]);

function walkBlockToMarkdown(node: Node, lines: string[]): void {
  if (node.nodeType === Node.TEXT_NODE) {
    if ((node.textContent || "").trim()) lines.push(node.textContent || "");
    return;
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return;
  const element = node as HTMLElement;
  const inline = () => Array.from(element.childNodes).map(inlineHTMLToMarkdown).join("").trimEnd();
  switch (element.tagName) {
    case "H1":
      lines.push(`# ${inline()}`.trimEnd());
      return;
    case "H2":
      lines.push(`## ${inline()}`.trimEnd());
      return;
    case "H3":
      lines.push(`### ${inline()}`.trimEnd());
      return;
    case "UL":
    case "OL":
      Array.from(element.children).forEach((child, index) => {
        const item = Array.from(child.childNodes).map(inlineHTMLToMarkdown).join("").trimEnd();
        lines.push(element.tagName === "OL" ? `${index + 1}. ${item}`.trimEnd() : `- ${item}`.trimEnd());
      });
      return;
    case "P":
    case "DIV":
    case "BLOCKQUOTE": {
      const hasBlockChild = Array.from(element.children).some((child) => BLOCK_TAGS.has(child.tagName));
      if (!hasBlockChild) {
        lines.push(inline());
        return;
      }
      let run: Node[] = [];
      const flush = () => {
        if (!run.length) return;
        const text = run.map(inlineHTMLToMarkdown).join("").trimEnd();
        if (text) lines.push(text);
        run = [];
      };
      Array.from(element.childNodes).forEach((child) => {
        if (child.nodeType === Node.ELEMENT_NODE && BLOCK_TAGS.has((child as HTMLElement).tagName)) {
          flush();
          walkBlockToMarkdown(child, lines);
        } else {
          run.push(child);
        }
      });
      flush();
      return;
    }
    default:
      lines.push(inlineHTMLToMarkdown(element));
  }
}

export function htmlToMarkdown(root: HTMLElement): string {
  const lines: string[] = [];
  Array.from(root.childNodes).forEach((node) => walkBlockToMarkdown(node, lines));
  return lines.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
}
