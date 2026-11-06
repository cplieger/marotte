// The quote form a prompt carries when the reader quotes part of the transcript, and its recognition in a sent prompt.

/** Ends every quote "Quote in reply" writes. Sent prompts are recognised by this exact line, so it never changes. */
export const QUOTE_ATTRIBUTION = "Quoting your earlier reply.";

/** The longest quoted text, in code points, ellipsis included, counted before the `#[[` escape. */
export const QUOTE_TEXT_MAX = 1500;

const ELLIPSIS = "\u2026";
const ATTRIBUTION_LINE = `> ${QUOTE_ATTRIBUTION}`;

/** `text` as a Markdown blockquote: every line prefixed `> `, an empty line a bare `>`. */
export function blockquote(text: string): string {
  return text
    .split("\n")
    .map((line) => (line === "" ? ">" : `> ${line}`))
    .join("\n");
}

/** Cut to `QUOTE_TEXT_MAX` code points at the last whitespace, ending on an ellipsis; a text with no whitespace in
 *  reach is cut at the limit. Counting code points keeps a surrogate pair whole. */
function capQuote(text: string): string {
  const points = Array.from(text);
  if (points.length <= QUOTE_TEXT_MAX) {
    return text;
  }
  const head = points.slice(0, QUOTE_TEXT_MAX - 1);
  const space = head.findLastIndex((c) => /\s/.test(c));
  const kept = space > 0 ? head.slice(0, space) : head;
  return kept.join("").trimEnd() + ELLIPSIS;
}

/** Every `#`, backslashes, `[[` run gains one backslash, so quoted words never read as a context reference the
 *  server would resolve; `unescapeContext` removes exactly that one. */
function escapeContext(text: string): string {
  return text.replace(/#(\\*)\[\[/g, "#\\$1[[");
}

function unescapeContext(text: string): string {
  return text.replace(/#\\(\\*)\[\[/g, "#$1[[");
}

/** `text` as a CommonMark inline code span (spec 6.1): fenced by a backtick run longer than any inside it, and
 *  padded with a space where an edge backtick or edge spaces would otherwise merge with the fence or be stripped. */
export function codeSpan(text: string): string {
  const longest = Math.max(0, ...Array.from(text.matchAll(/`+/g), (m) => m[0].length));
  const fence = "`".repeat(longest + 1);
  const pad = /^`|`$/.test(text) || (/^ .* $/s.test(text) && text.trim() !== "") ? " " : "";
  return `${fence}${pad}${text}${pad}${fence}`;
}

/** The quote block for a selected passage, edge whitespace kept, or "" for blank text: `source` on one line above it,
 *  naming where the words came from, then the text, then `QUOTE_ATTRIBUTION`. Both are escaped by `escapeContext`. */
export function quoteSelection(text: string, source: string): string {
  const normalized = text.replace(/\r\n?/g, "\n");
  if (normalized.trim() === "") {
    return "";
  }
  const from = escapeContext(source.replace(/\s+/g, " ").trim());
  const body = escapeContext(capQuote(normalized));
  return blockquote(`${from}:\n\n${body}\n\n${QUOTE_ATTRIBUTION}`);
}

/** `draft` with `block` after it, one blank line between, and a blank line after so the reader types below. */
export function appendQuote(draft: string, block: string): string {
  const head = draft.trimEnd();
  return head === "" ? `${block}\n\n` : `${head}\n\n${block}\n\n`;
}

/** A prompt opening with a `quoteSelection` block: its source (null for a quote written before quotes named one), the
 *  quoted text, both unescaped and without the attribution, and the rest of the prompt. Null for any other prompt, a
 *  hand-typed blockquote included. */
export function splitLeadingQuote(
  request: string,
): { source: string | null; quote: string; rest: string } | null {
  const lines = request.split("\n");
  let end = 0;
  while (end < lines.length && lines[end]?.startsWith(">") === true) {
    end++;
  }
  if (end < 2 || lines[end - 1] !== ATTRIBUTION_LINE) {
    return null;
  }
  const inner = lines
    .slice(0, end - 1)
    .map((line) => (line.startsWith("> ") ? line.slice(2) : line.slice(1)));
  const head = inner[0] ?? "";
  // The source line `quoteSelection` writes; an older quote opens on its text.
  const named = head.startsWith("From ") && head.endsWith(":") && inner[1] === "";
  const quoted = unescapeContext(
    inner
      .slice(named ? 2 : 0)
      .join("\n")
      .trimEnd(),
  );
  if (quoted === "") {
    return null;
  }
  return {
    source: named ? unescapeContext(head.slice(0, -1)) : null,
    quote: quoted,
    rest: lines.slice(end).join("\n").trim(),
  };
}
