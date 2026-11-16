// The holding area's one fold rule, shared by the run bar and the steer stack.

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";

/** Folded, a holding-area list shows at most this many lines until the reader chooses: the run bar folds behind
 *  its header from `FOLD_AT` runs, the steer stack past `FOLD_AT` rows shows its first `FOLD_AT - 1` and a fold row. */
export const FOLD_AT = 3;

/** The index the steer stack's fold row takes among `count` rows, or null when it needs none: a fold row
 *  hiding a single row would save no line. */
export function foldAt(count: number): number | null {
  return count > FOLD_AT ? FOLD_AT - 1 : null;
}

/** The fold row, painted closed; `paintFoldRow` gives it its count. */
export function buildFoldRow(onToggle: () => void): HTMLElement {
  const btn = el(
    "button",
    { type: "button", className: "steer-fold-toggle", "aria-expanded": "false" },
    chevronEl(),
    el("span", { className: "steer-fold-count" }),
  );
  btn.addEventListener("click", onToggle);
  return el("li", { className: "steer-fold" }, btn);
}

/** The accessible name holds the visible words (WCAG 2.5.3 label in name), so a voice command saying them
 *  reaches the button. */
export function paintFoldRow(li: HTMLElement, hidden: number, open: boolean): void {
  const btn = li.querySelector<HTMLElement>(".steer-fold-toggle");
  if (btn === null) {
    return;
  }
  btn.setAttribute("aria-expanded", String(open));
  btn.setAttribute(
    "aria-label",
    open ? "Show fewer waiting messages" : `Show ${String(hidden)} more waiting messages`,
  );
  const count = btn.querySelector(".steer-fold-count");
  if (count !== null) {
    count.textContent = open ? "Show fewer" : `+${String(hidden)} more`;
  }
}
