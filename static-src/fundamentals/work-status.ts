// ---------------------------------------------------------------------------
// Fundamental: the ONE status glyph for every progress surface.
//
// One table for the three surfaces that render `PlanStatus` (the todo
// checklist, the task pill and the spec tree), so they cannot drift. The glyphs
// are CSS-drawn (a ring, a half ring, a filled ring with a check), so this
// table carries class hooks and words, never characters, and no emoji reaches
// the DOM.
//
// `queued` is Kiro's fourth checkbox mark (`~`) and is NOT a fourth status: KAS
// treats a queued task as pending everywhere it decides anything, so it is a
// tint over a pending glyph rather than a member of the record.
// ---------------------------------------------------------------------------

import type { PlanStatus } from "../types.js";

/** The class hook and the announced word for one status. */
export interface StatusLook {
  readonly className: string;
  readonly word: string;
}

/** Class carried by every element `paintStatus` paints; the CSS draws the glyph from it. */
export const STATUS_CLASS = "work-status";

/** Modifier class for a pending glyph the orchestrator has queued. */
export const QUEUED_CLASS = "work-status-queued";

/** One look per `PlanStatus`, total by type: a new member fails the typecheck here. */
export const STATUS: Readonly<Record<PlanStatus, StatusLook>> = {
  pending: { className: "work-status-pending", word: "pending" },
  in_progress: { className: "work-status-in-progress", word: "in progress" },
  completed: { className: "work-status-completed", word: "completed" },
};

/** Paint options for `paintStatus`. */
export interface PaintStatusOpts {
  readonly queued?: boolean;
}

/** Paint `status` onto `el`: exactly one status class, the queued tint toggled, and the
 *  word as the element's accessible name. Idempotent, so a reconcile update may call it
 *  on every pass. */
export function paintStatus(el: HTMLElement, status: PlanStatus, opts: PaintStatusOpts = {}): void {
  el.classList.add(STATUS_CLASS);
  for (const s of Object.keys(STATUS) as PlanStatus[]) {
    el.classList.toggle(STATUS[s].className, s === status);
  }
  const queued = opts.queued === true && status === "pending";
  el.classList.toggle(QUEUED_CLASS, queued);
  el.setAttribute("role", "img");
  el.setAttribute("aria-label", queued ? "queued" : STATUS[status].word);
}
