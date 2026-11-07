// The band a message opening with a `quoteSelection` block draws in place of its `>` lines.

import { el } from "@cplieger/ui";
import { linkifyPaths } from "./linkify.js";

/** The quoted passage as one band, led by the line naming its source when it has one. */
export function buildQuoteBand(quote: { source: string | null; quote: string }): HTMLElement {
  const text = el("span", { className: "turn-req-quote-text" }, quote.quote);
  linkifyPaths(text);
  return el(
    "blockquote",
    { className: "turn-req-quote" },
    ...(quote.source === null ? [] : [sourceEl(quote.source), " "]),
    text,
  );
}

/** A quote's source line as the reader sees it: its code spans (CommonMark 6.1) drawn as code. */
function sourceEl(source: string): HTMLElement {
  const parts: (Node | string)[] = [];
  let at = 0;
  for (const m of source.matchAll(/(`+)(?!`)(.+?)(?<!`)\1(?!`)/g)) {
    parts.push(source.slice(at, m.index));
    const inner = m[2] ?? "";
    const code = /^ .* $/.test(inner) && inner.trim() !== "" ? inner.slice(1, -1) : inner;
    parts.push(el("code", {}, code));
    at = m.index + m[0].length;
  }
  parts.push(source.slice(at), ":");
  return el("span", { className: "turn-req-quote-from" }, ...parts);
}
