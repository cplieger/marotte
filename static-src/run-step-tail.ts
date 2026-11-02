// The newest line a workflow step printed, for the run tab's one-line glance at a running step.

import type { TurnState } from "./types.js";

/** Longer than any dock row shows; the CSS ellipsizes the rest. */
const MAX_CHARS = 200;

/** The newest line the step printed across `turns` (oldest first): a continuation or retry turn
 *  that has printed nothing yet leaves the previous turn's line standing. "" when none printed. */
export function lastPrintedLine(turns: readonly TurnState[]): string {
  for (let i = turns.length - 1; i >= 0; i--) {
    const turn = turns[i];
    const line = turn === undefined ? "" : turnLine(turn);
    if (line !== "") {
      return line;
    }
  }
  return "";
}

/** The newest thing the step's own lane printed in `turn`: its streaming text or thinking, else
 *  its newest sealed text, thinking or tool call (by title). */
function turnLine(turn: TurnState): string {
  const open = turn.openEntries.get("");
  if (open !== undefined) {
    const line = lastLine(open.text);
    if (line !== "") {
      return line;
    }
  }
  for (let i = turn.entries.length - 1; i >= 0; i--) {
    const e = turn.entries[i];
    if (e === undefined || (e.lane ?? "") !== "") {
      continue;
    }
    const field =
      e.kind === "text" || e.kind === "thinking" ? "text" : e.kind === "tool_call" ? "title" : "";
    if (field === "") {
      continue;
    }
    const p = e.payload;
    const value =
      p !== null && typeof p === "object" ? (p as Record<string, unknown>)[field] : undefined;
    const line = typeof value === "string" ? lastLine(value) : "";
    if (line !== "") {
      return line;
    }
  }
  return "";
}

/** Markdown structure that says nothing on its own line: a code fence, a table's rule, a thematic
 *  break. */
const STRUCTURE_ONLY = [
  /^(?:`{3,}|~{3,})[^`]*$/,
  /^\|?(?:\s*:?-+:?\s*\|)*\s*:?-+:?\s*\|?$/,
  /^([-*_])(?:\s*\1){2,}$/,
];

/** Leading block markers: heading hashes, quote marks, list bullets and numbers. */
const BLOCK_MARKERS = /^(?:#{1,6}\s+|>\s?|[-*+]\s+|\d{1,9}[.)]\s+)+/;

/** The last line of `text` that reads as prose, its markdown block markers dropped. */
function lastLine(text: string): string {
  const lines = text.split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    const raw = (lines[i] ?? "").trim();
    if (raw === "" || STRUCTURE_ONLY.some((re) => re.test(raw))) {
      continue;
    }
    const line = raw.replace(BLOCK_MARKERS, "").trim();
    if (line !== "") {
      return capped(line);
    }
  }
  return "";
}

/** `line` cut to `MAX_CHARS` code points, so the cut never splits a surrogate pair. */
function capped(line: string): string {
  if (line.length <= MAX_CHARS) {
    return line;
  }
  return Array.from(line).slice(0, MAX_CHARS).join("");
}
