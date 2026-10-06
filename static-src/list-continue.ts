// Markdown list continuation for a plain textarea, pure. Later items are never renumbered: in a chat a typed `2.` often
// answers item 2.

import type { Edit } from "./text-edit.js";

const ITEM = /^([ \t]*)([-*+]|\d{1,9}[.)])(?: (\[[ xX]\]))?(?:([ \t]+)(.*))?$/;
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/;

type Decision =
  | { readonly kind: "continue"; readonly prefix: string }
  | { readonly kind: "exit"; readonly start: number; readonly end: number };

/** Whether the text before a line leaves a ``` or ~~~ fence open. */
function insideFence(before: string): boolean {
  let open: { readonly ch: string; readonly len: number } | null = null;
  for (const line of before.split("\n")) {
    const m = FENCE.exec(line);
    const run = m?.[1];
    if (m === null || run === undefined) {
      continue;
    }
    if (open === null) {
      open = { ch: run.charAt(0), len: run.length };
    } else if (run.startsWith(open.ch) && run.length >= open.len && (m[2] ?? "").trim() === "") {
      open = null;
    }
  }
  return open !== null;
}

/** The marker after `marker`, or null when a number would pass nine digits. */
function nextMarker(marker: string): string | null {
  const delim = marker.slice(-1);
  if (delim !== "." && delim !== ")") {
    return marker;
  }
  const digits = marker.slice(0, -1);
  const next = String(Number(digits) + 1).padStart(digits.length, "0");
  return next.length > 9 ? null : next + delim;
}

function decide(value: string, caret: number): Decision | null {
  const lineStart = value.lastIndexOf("\n", caret - 1) + 1;
  const newline = value.indexOf("\n", caret);
  const lineEnd = newline === -1 ? value.length : newline;
  const line = value.slice(lineStart, lineEnd);
  const m = ITEM.exec(line);
  if (m === null || RULE.test(line) || insideFence(value.slice(0, lineStart))) {
    return null;
  }
  const [, indent = "", marker = "", task, gap, content = ""] = m;
  // `-foo` is not an item; `- [ ]` with nothing after it is an empty one.
  if (task === undefined && gap === undefined) {
    return null;
  }
  const prefixLen =
    indent.length + marker.length + (task === undefined ? 0 : task.length + 1) + (gap?.length ?? 0);
  if (caret - lineStart < prefixLen) {
    return null;
  }
  if (content.trim() === "") {
    return { kind: "exit", start: lineStart, end: lineEnd };
  }
  const next = nextMarker(marker);
  if (next === null) {
    return null;
  }
  return {
    kind: "continue",
    prefix: indent + next + (task === undefined ? "" : " [ ]") + (gap ?? " "),
  };
}

/** The edit a new line typed at `caret` should make instead of a plain line
 *  break, or null to let the browser insert one. Text after the caret moves
 *  onto the new item. */
export function continueList(value: string, caret: number): Edit | null {
  const d = decide(value, caret);
  if (d === null) {
    return null;
  }
  if (d.kind === "exit") {
    return { start: d.start, end: d.end, text: "", caret: d.start };
  }
  const text = `\n${d.prefix}`;
  return { start: caret, end: caret, text, caret: caret + text.length };
}

/** The same decision AFTER the browser inserted the line break that ends at
 *  `caret`, for touch keyboards whose Return cannot be cancelled reliably. An
 *  exit also takes the inserted break back. */
export function continueAfterBreak(value: string, caret: number): Edit | null {
  if (caret < 1 || value.charAt(caret - 1) !== "\n") {
    return null;
  }
  const d = decide(value.slice(0, caret - 1) + value.slice(caret), caret - 1);
  if (d === null) {
    return null;
  }
  if (d.kind === "exit") {
    return { start: d.start, end: d.end + 1, text: "", caret: d.start };
  }
  return { start: caret, end: caret, text: d.prefix, caret: caret + d.prefix.length };
}
