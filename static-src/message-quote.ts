// The quote form a prompt carries when the reader quotes part of a reply, and its recognition in a sent prompt.

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

/** The quote block for a selected passage, edge whitespace kept, or "" for blank text. Every `#`, backslashes, `[[`
 *  run gains one backslash, so quoted words never read as a context reference the server would resolve and
 *  `splitLeadingQuote` removes exactly that one. */
export function quoteSelection(text: string): string {
  const normalized = text.replace(/\r\n?/g, "\n");
  if (normalized.trim() === "") {
    return "";
  }
  const body = capQuote(normalized).replace(/#(\\*)\[\[/g, "#\\$1[[");
  return blockquote(`${body}\n\n${QUOTE_ATTRIBUTION}`);
}

/** `draft` with `block` after it, one blank line between, and a blank line after so the reader types below. */
export function appendQuote(draft: string, block: string): string {
  const head = draft.trimEnd();
  return head === "" ? `${block}\n\n` : `${head}\n\n${block}\n\n`;
}

/** A prompt opening with a `quoteSelection` block: the quoted text, unescaped and without the attribution, and the
 *  rest of the prompt. Null for any other prompt, a hand-typed blockquote included. */
export function splitLeadingQuote(request: string): { quote: string; rest: string } | null {
  const lines = request.split("\n");
  let end = 0;
  while (end < lines.length && lines[end]?.startsWith(">") === true) {
    end++;
  }
  if (end < 2 || lines[end - 1] !== ATTRIBUTION_LINE) {
    return null;
  }
  const quoted = lines
    .slice(0, end - 1)
    .map((line) => (line.startsWith("> ") ? line.slice(2) : line.slice(1)))
    .join("\n")
    .trimEnd()
    .replace(/#\\(\\*)\[\[/g, "#$1[[");
  if (quoted === "") {
    return null;
  }
  return { quote: quoted, rest: lines.slice(end).join("\n").trim() };
}
