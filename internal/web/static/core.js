// Small, dependency-free primitives shared by the console views.
export const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export const number = (value) =>
  new Intl.NumberFormat().format(Number(value) || 0);
export const compact = (value) =>
  new Intl.NumberFormat("en", {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(Number(value) || 0);
export const duration = (ms) =>
  ms == null
    ? "—"
    : Number(ms) < 1000
      ? `${Math.round(ms)} ms`
      : `${(Number(ms) / 1000).toFixed(1)} s`;
export const countdown = (seconds) =>
  seconds >= 3600
    ? `${Math.floor(seconds / 3600)}h ${Math.ceil((seconds % 3600) / 60)}m`
    : seconds >= 60
      ? `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`
      : `${Math.max(0, Math.ceil(seconds))}s`;
export function timestamp(value, full = false) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : date.toLocaleString(
        undefined,
        full
          ? {
              year: "numeric",
              month: "2-digit",
              day: "2-digit",
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            }
          : {
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
              hour12: false,
            },
      );
}
const paths = {
  overview:
    '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  requests: '<path d="M6 3h9l4 4v14H6zM14 3v5h5M9 12h7M9 16h7"/>',
  accounts:
    '<circle cx="9" cy="8" r="3"/><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6M18 15a5 5 0 0 1 3 5"/>',
  platforms: '<path d="m12 3 10 5-10 5L2 8zM2 12l10 5 10-5M2 16l10 5 10-5"/>',
  models: '<path d="m12 3 9 5v9l-9 5-9-5V8zM3 8l9 5 9-5M12 13v9"/>',
  chat: '<path d="M21 11a9 9 0 0 1-9 9H3l1.5-4A9 9 0 1 1 21 11Z"/><path d="M8 10h8M8 14h5"/>',
  settings:
    '<path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z"/><path d="m9 3-.5 3-2 .9-2.5-1L2 10l2.5 1.7v2.6L2 16l2 4 2.5-1 2 .9.5 3h6l.5-3 2-.9 2.5 1 2-4-2.5-1.7v-2.6L22 10l-2-4-2.5 1-2-.9L15 3Z" transform="translate(0 -1) scale(1 .96)"/>',
  search: '<circle cx="10" cy="10" r="6.5"/><path d="m15 15 6 6"/>',
  refresh:
    '<path d="M20 9a8 8 0 0 0-14-4L3 8M3 3v5h5M4 15a8 8 0 0 0 14 4l3-3M21 21v-5h-5"/>',
  copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M15 8V3H3v13h5"/>',
  arrow: '<path d="M4 12h16m-6-6 6 6-6 6"/>',
  chevron: '<path d="m9 5 7 7-7 7"/>',
  left: '<path d="m15 5-7 7 7 7"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  close: '<path d="m6 6 12 12M6 18 18 6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7v.2"/>',
  activity: '<path d="M2 12h5l3-8 4 16 3-8h5"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  bolt: '<path d="m13 2-9 12h7l-1 8 10-13h-7z"/>',
  tokens:
    '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 4 16 4 16 0V6M4 12c0 4 16 4 16 0"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  send: '<path d="m3 3 18 9-18 9 4-9zM7 12h14"/>',
  stop: '<rect x="5" y="5" width="14" height="14" rx="2"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  panel: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>',
  rows: '<path d="M4 5h16M4 12h16M4 19h16M4 8h16M4 15h16"/>',
  external: '<path d="M14 3h7v7M21 3l-11 11M11 3H3v18h18v-8"/>',
};
export const icon = (name) =>
  `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">${paths[name] || paths.info}</svg>`;

export async function responseError(response, language = "en") {
  const text = await response.text();
  try {
    const data = JSON.parse(text);
    return (
      data.error?.message ||
      data.message ||
      (typeof data.error === "string" ? data.error : text)
    );
  } catch {
    if (response.headers.get("content-type")?.includes("text/html") || /^\s*(?:<!doctype\s+html|<html\b)/i.test(text)) {
      return language === "zh" ? `网关或上游服务返回了错误页面（HTTP ${response.status}），请检查连接或稍后重试。` : `The gateway or upstream returned an error page (HTTP ${response.status}). Check the connection or retry later.`;
    }
    return text || `HTTP ${response.status}`;
  }
}

// Reader handles split UTF-8 characters, CRLF split across network chunks,
// multi-line data and usage-only events. Cancel releases the stream on [DONE].
export async function* readEvents(body) {
  if (!body) throw new Error("The server returned no response stream.");
  const reader = body.getReader(),
    decoder = new TextDecoder();
  let buffer = "",
    data = [],
    completed = false;
  function parseLine(line) {
    if (line.endsWith("\r")) line = line.slice(0, -1);
    if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
    if (line !== "" || !data.length) return null;
    const raw = data.join("\n");
    data = [];
    if (raw === "[DONE]") {
      completed = true;
      return { done: true };
    }
    return JSON.parse(raw);
  }
  try {
    while (!completed) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      let newline;
      while ((newline = buffer.indexOf("\n")) !== -1) {
        const event = parseLine(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        if (event) yield event;
        if (completed) return;
      }
      if (done) {
        if (buffer) parseLine(buffer);
        const event = parseLine("");
        if (event) yield event;
        return;
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

// Escape first. A deliberately small Markdown renderer never accepts raw HTML,
// remote images, script URLs or untrusted attributes from model responses.
export function markdown(source) {
  const blocks = [];
  let text = String(source).replace(
    /```([^\n]*)\n([\s\S]*?)(?:```|$)/g,
    (_, language, code) => {
      const raw = code.replace(/\n$/, "");
      const index =
        blocks.push(
          `<pre><button class="code-copy" data-action="copy-code" data-code="${escapeHTML(raw)}" aria-label="Copy code">Copy</button><code>${escapeHTML(raw)}</code></pre>`,
        ) - 1;
      return `\n\n\u0000${index}\u0000\n\n`;
    },
  );
  const math = (tex, display) =>
    `\u0000${blocks.push(`<span class="math ${display ? "math-display" : ""}" data-tex="${escapeHTML(tex)}" data-display="${display}"><code>${escapeHTML(tex)}</code></span>`) - 1}\u0000`;
  text = text
    .replace(/\$\$([\s\S]+?)\$\$|\\\[([\s\S]+?)\\\]/g, (_, a, b) =>
      math(a ?? b, true),
    )
    .replace(/\\\(([\s\S]+?)\\\)/g, (_, tex) => math(tex, false))
    .replace(/(^|[^\w$])\$([^$\n]+?)\$(?!\w)/g, (match, before, tex) =>
      /[\^_\\{}]/.test(tex) && !/^\s|\s$/.test(tex)
        ? before + math(tex, false)
        : match,
    );
  text = escapeHTML(text);
  const inline = (line) =>
    line
      .replace(/`([^`\n]+)`/g, "<code>$1</code>")
      .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
      .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
      .replace(
        /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g,
        '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
      );
  return text
    .split(/\n\s*\n/)
    .map((block) => {
      const match = block.trim().match(/^\u0000(\d+)\u0000$/);
      if (match) return blocks[Number(match[1])];
      const heading = block.match(/^(#{1,3}) (.*)$/);
      if (heading)
        return `<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`;
      const lines = block.split("\n");
      if (
        lines.length >= 2 &&
        lines[0].includes("|") &&
        /^\|?\s*:?-{2,}/.test(lines[1])
      ) {
        const cells = (line) =>
          line
            .trim()
            .replace(/^\||\|$/g, "")
            .split("|")
            .map((cell) => cell.trim());
        const head = cells(lines[0]);
        if (cells(lines[1]).every((cell) => /^:?-{2,}:?$/.test(cell))) {
          return `<div class="table-wrap"><table class="markdown-table"><thead><tr>${head.map((cell) => `<th>${inline(cell)}</th>`).join("")}</tr></thead><tbody>${lines
            .slice(2)
            .map((line) => {
              const row = cells(line);
              return `<tr>${head.map((_, i) => `<td>${inline(row[i] || "")}</td>`).join("")}</tr>`;
            })
            .join("")}</tbody></table></div>`;
        }
      }
      if (lines.every((line) => /^[-*] /.test(line)))
        return `<ul>${lines.map((line) => `<li>${inline(line.slice(2))}</li>`).join("")}</ul>`;
      if (lines.every((line) => /^\d+\. /.test(line)))
        return `<ol>${lines.map((line) => `<li>${inline(line.replace(/^\d+\. /, ""))}</li>`).join("")}</ol>`;
      if (lines.every((line) => /^&gt; /.test(line)))
        return `<blockquote>${lines.map((line) => inline(line.slice(5))).join("<br>")}</blockquote>`;
      return block.trim()
        ? `<p>${inline(block).replace(/\n/g, "<br>")}</p>`
        : "";
    })
    .join("")
    .replace(/\u0000(\d+)\u0000/g, (_, index) => blocks[Number(index)] || "");
}
