// The holding area's one fold rule, shared by the run bar and the steer stack.

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";

/** The fold threshold: the run bar folds behind its header from `FOLD_AT` runs; the steer stack, past `FOLD_AT`
 *  messages, shows its first `FOLD_AT - 1` and a fold row (`stackFold`). */
export const FOLD_AT = 3;

/** A steer stack row as the fold sees it: a waiting message, counted and folded; the paused queue's head,
 *  never folded because it carries the queue's Resume; any other row, folded uncounted. */
export type FoldLine = "message" | "pinned" | "other";

export interface StackFold {
  /** The index among the rows the fold row goes in at. */
  readonly at: number;
  /** The messages it hides, at least two. */
  readonly hidden: number;
  /** Per row, whether it stays shown while the stack is folded. */
  readonly shown: readonly boolean[];
}

/** The steer stack's fold over its rows in send order, or null for `FOLD_AT` messages or fewer: the fold
 *  row goes in after the `FOLD_AT - 1`th message and hides every later row but a pinned one. */
export function stackFold(lines: readonly FoldLine[]): StackFold | null {
  const messages = lines.filter((l) => l === "message").length;
  if (messages <= FOLD_AT) {
    return null;
  }
  let at = 0;
  for (let seen = 0; seen < FOLD_AT - 1; at++) {
    if (lines[at] === "message") {
      seen++;
    }
  }
  return {
    at,
    hidden: messages - (FOLD_AT - 1),
    shown: lines.map((l, i) => i < at || l === "pinned"),
  };
}

export interface FoldRow {
  readonly li: HTMLElement;
  readonly btn: HTMLElement;
  readonly count: HTMLElement;
}

/** The fold row, painted closed; `paintFoldRow` gives it its count. */
export function buildFoldRow(onToggle: () => void): FoldRow {
  const count = el("span", { className: "steer-fold-count" });
  const btn = el(
    "button",
    { type: "button", className: "steer-fold-toggle", "aria-expanded": "false" },
    chevronEl(),
    count,
  );
  btn.addEventListener("click", onToggle);
  return { li: el("li", { className: "steer-fold" }, btn), btn, count };
}

/** The accessible name holds the visible words (WCAG 2.5.3 label in name), so a voice command saying them
 *  reaches the button. */
export function paintFoldRow(row: FoldRow, hidden: number, open: boolean): void {
  row.btn.setAttribute("aria-expanded", String(open));
  row.btn.setAttribute(
    "aria-label",
    open ? "Show fewer waiting messages" : `Show ${String(hidden)} more waiting messages`,
  );
  row.count.textContent = open ? "Show fewer" : `+${String(hidden)} more`;
}
