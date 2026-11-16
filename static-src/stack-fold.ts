// The holding area's one fold rule, shared by the run bar and the steer stack.

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";

/** Folded, a holding-area list shows this many lines until the reader chooses: the run bar folds behind its
 *  header from `FOLD_AT` runs, the steer stack past `FOLD_AT` rows shows its first `FOLD_AT - 1` and a fold
 *  row, further down only to keep the paused queue's head (`placeFold`, pending-steers.ts). */
export const FOLD_AT = 3;

/** The index the steer stack's fold row takes among `count` rows, or null when it needs none: a fold row
 *  hiding a single row would save no line. */
export function foldAt(count: number): number | null {
  return count > FOLD_AT ? FOLD_AT - 1 : null;
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
