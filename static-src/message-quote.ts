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

/** Whether `line` is `QUOTE_ATTRIBUTION` after any run of backslashes: a passage line `escapeAttribution` takes. */
function isAttribution(line: string): boolean {
  return line.replace(/^\\*/, "") === QUOTE_ATTRIBUTION;
}

/** Every passage line reading as the attribution gains one backslash, so only a block's own attribution line closes
 *  it; `readQuote` removes exactly that one. Per `\n` line, as `splitQuotes` cuts, not a multiline regex, whose `$`
 *  also matches before U+2028. */
function escapeAttribution(text: string): string {
  return text
    .split("\n")
    .map((line) => (isAttribution(line) ? `\\${line}` : line))
    .join("\n");
}

function unescapeAttribution(line: string): string {
  return line.startsWith("\\") && isAttribution(line) ? line.slice(1) : line;
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
 *  naming where the words came from, then the text, then `QUOTE_ATTRIBUTION`. Both are escaped by `escapeContext`,
 *  and the text by `escapeAttribution`. */
export function quoteSelection(text: string, source: string): string {
  const normalized = text.replace(/\r\n?/g, "\n");
  if (normalized.trim() === "") {
    return "";
  }
  const from = escapeContext(source.replace(/\s+/g, " ").trim());
  const body = escapeAttribution(escapeContext(capQuote(normalized)));
  return blockquote(`${from}:\n\n${body}\n\n${QUOTE_ATTRIBUTION}`);
}

/** `draft` with `block` after it, one blank line between, and a blank line after so the reader types below. */
export function appendQuote(draft: string, block: string): string {
  const head = draft.trimEnd();
  return head === "" ? `${block}\n\n` : `${head}\n\n${block}\n\n`;
}

/** A `quoteSelection` block read back: its source (null for a quote written before quotes named one) and the quoted
 *  text, both unescaped and without the attribution. */
export interface Quote {
  source: string | null;
  quote: string;
}

/** One stretch of a prompt, in order: a `quoteSelection` block, or the trimmed text around one. */
export type PromptPart = ({ kind: "quote" } & Quote) | { kind: "text"; text: string };

function unquote(line: string): string {
  return line.startsWith("> ") ? line.slice(2) : line.slice(1);
}

/** Whether `run[i]` opens a named quote: the `From …:` line `quoteSelection` writes, then a bare `>`. */
function opensNamed(run: readonly string[], i: number): boolean {
  const head = unquote(run[i] ?? "");
  const next = run[i + 1];
  return (
    head.startsWith("From ") && head.endsWith(":") && next !== undefined && unquote(next) === ""
  );
}

/** The quote `block` holds, its `>` lines ending on the attribution line, or null when nothing is quoted above it. */
function readQuote(block: readonly string[]): Quote | null {
  // An older quote opening on a `From …:` line then a blank line draws that line as its source too.
  const named = opensNamed(block, 0);
  const quoted = unescapeContext(
    block
      .slice(named ? 2 : 0, -1)
      .map((line) => unescapeAttribution(unquote(line)))
      .join("\n")
      .trimEnd(),
  );
  if (quoted === "") {
    return null;
  }
  const source = unquote(block[0] ?? "").slice(0, -1);
  return { source: named ? unescapeContext(source) : null, quote: quoted };
}

/** A run of `>` lines as its quotes, in order, and the hand-typed lines around them. Every attribution line closes a
 *  quote, since `quoteSelection` escapes one inside a passage. A stretch that quotes nothing, and the lines after the
 *  last attribution line, stay text. */
function readRun(run: readonly string[]): (Quote | string[])[] {
  const out: (Quote | string[])[] = [];
  let start = 0;
  for (const [i, line] of run.entries()) {
    if (line === ATTRIBUTION_LINE) {
      const piece = run.slice(start, i + 1);
      out.push(...(readBlock(piece) ?? [piece]));
      start = i + 1;
    }
  }
  out.push(run.slice(start));
  return out;
}

/** The typed lines above `block`'s first named opener, and the quote from there, or null when it quotes nothing. */
function readBlock(block: readonly string[]): [string[], Quote] | null {
  const at = Math.max(
    block.findIndex((_, i) => opensNamed(block, i)),
    0,
  );
  const quote = readQuote(block.slice(at));
  return quote === null ? null : [block.slice(0, at), quote];
}

/** `request` cut into its `quoteSelection` blocks, wherever each sits, and the text between, blank text left out.
 *  Any `>` line no quote takes, a hand-typed blockquote's, stays text. `internal/chat/prompt_quote.go` ports this
 *  cut for the turn map's preview, so a change here changes it too (`testdata/first_line.json` pins both). */
export function splitQuotes(request: string): PromptPart[] {
  const lines = request.split("\n");
  const parts: PromptPart[] = [];
  let text: string[] = [];
  const flushText = (): void => {
    const words = text.join("\n").trim();
    if (words !== "") {
      parts.push({ kind: "text", text: words });
    }
    text = [];
  };
  for (let i = 0; i < lines.length;) {
    let end = i;
    while (lines[end]?.startsWith(">") === true) {
      end++;
    }
    if (end === i) {
      text.push(lines[i] ?? "");
      i++;
      continue;
    }
    for (const piece of readRun(lines.slice(i, end))) {
      if (Array.isArray(piece)) {
        text.push(...piece);
      } else {
        flushText();
        parts.push({ kind: "quote", ...piece });
      }
    }
    i = end;
  }
  flushText();
  return parts;
}

/** `splitQuotes(request)` with the quote opening the prompt, when one does, apart as `lead`. */
export function leadQuote(request: string): { lead: Quote | null; rest: PromptPart[] } {
  const parts = splitQuotes(request);
  const first = parts[0];
  return first?.kind === "quote"
    ? { lead: { source: first.source, quote: first.quote }, rest: parts.slice(1) }
    : { lead: null, rest: parts };
}

/** `text` as its quote bands read: each `quoteSelection` block as `source: quote` without the `>` marks or the
 *  attribution, its parts a blank line apart. Text holding no quote unchanged. For the surfaces that carry a message
 *  as one string (accessible names, button labels). */
export function readQuoted(text: string): string {
  const parts = splitQuotes(text);
  if (!parts.some((p) => p.kind === "quote")) {
    return text;
  }
  return parts
    .map((p) => {
      if (p.kind === "text") {
        return p.text;
      }
      return p.source === null ? p.quote : `${p.source}: ${p.quote}`;
    })
    .join("\n\n");
}
