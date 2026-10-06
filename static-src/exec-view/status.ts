// One status vocabulary for every surface reporting delegated work (tree,
// timeline, detail pane, transcript run card).

import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";
import type { ClassifiedRunNodeStatus, ClassifiedRunStatus } from "../run-status.js";

/** A node's state, as every exec surface reports it. */
export type ExecState =
  "pending" | "running" | "waiting" | "input" | "unknown" | "ok" | "fail" | "warn" | "skipped";

/** The one mark a state carries, tagged so a state cannot carry two. */
type StateMark =
  | { readonly kind: "none" }
  | { readonly kind: "char"; readonly text: string }
  | { readonly kind: "icon"; readonly svg: string };

/** Exactly one mark per state, so tint is never the only channel (WCAG 1.4.1).
 *  A `kind: "none"` state is drawn by CSS as a ring, so a new member here needs a
 *  `.ev-row[data-state=…]` arm or it renders nothing at all. */
export const STATE_MARK: Readonly<Record<ExecState, StateMark>> = {
  pending: { kind: "none" },
  running: { kind: "none" },
  waiting: { kind: "none" },
  input: { kind: "char", text: "?" },
  unknown: { kind: "none" },
  ok: { kind: "icon", svg: outcomeIcon("ok") },
  fail: { kind: "icon", svg: outcomeIcon("fail") },
  warn: { kind: "icon", svg: outcomeIcon("warn") },
  skipped: { kind: "char", text: "\u2013" },
};

/** Write a state's mark into a slot. */
export function paintStateMark(slot: HTMLElement, state: ExecState): void {
  const mark = STATE_MARK[state];
  // Every kind named, no `default`, so a kind added later fails the type check
  // here rather than being absorbed silently into one of these arms.
  switch (mark.kind) {
    case "icon":
      slot.replaceChildren(iconEl(mark.svg));
      break;
    case "char":
      slot.replaceChildren(mark.text);
      break;
    case "none":
      slot.replaceChildren();
      break;
  }
}

/** The word an accessible name uses. Not the wire enum: "aborted"/"failed" read as
 *  one thing to a listener, and "pending" reads better as "not started". */
export const STATE_WORD: Readonly<Record<ExecState, string>> = {
  pending: "not started",
  running: "running",
  waiting: "waiting",
  input: "waiting for your answer",
  unknown: "unknown",
  ok: "succeeded",
  fail: "failed",
  warn: "stopped",
  skipped: "skipped",
};

/** Whether a state is still in flight, so a caller keeps a clock going without
 *  re-deriving the set. `input` counts (the turn is open, merely blocked on a
 *  person) and so does `unknown` (not knowing is not the same as finished). */
export function inFlight(state: ExecState): boolean {
  return state === "running" || state === "waiting" || state === "input" || state === "unknown";
}

/** Whether a node produced nothing because it never ran: `pending` has not started and `skipped`
 *  never will. Not `!inFlight`. Exported so consumers do not re-derive the set. */
export function neverRan(state: ExecState): boolean {
  return state === "pending" || state === "skipped";
}

/** Whether a node RAN and stopped, the third bucket beside `inFlight` and `neverRan`. `fail` and
 *  `warn` are IN, `skipped` is OUT. No `default`, so a new state fails the type check. */
export function settled(state: ExecState): boolean {
  switch (state) {
    case "ok":
    case "fail":
    case "warn":
      return true;
    case "pending":
    case "running":
    case "waiting":
    case "input":
    case "unknown":
    case "skipped":
      return false;
  }
}

/** Fold a classified wire status onto the presentation vocabulary. `skipped` stays its own state;
 *  `aborted` and `cancelled` map to `warn` (a stop is not a fault). No `default`, so a new wire
 *  status fails this fold rather than reading as `pending`. */
export function stateOf(
  status: ClassifiedRunNodeStatus | ClassifiedRunStatus | undefined,
): ExecState {
  switch (status) {
    case undefined:
    case "pending":
      return "pending";
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    case "aborted":
    case "cancelled":
      return "warn";
    case "paused":
      return "waiting";
    case "running":
      return "running";
    case "skipped":
      return "skipped";
    case "unknown":
      return "unknown";
  }
}

/** Reclassify an in-flight node whose ask is unanswered. Guarded on in-flight: the workflow wire's
 *  `node_id` is shared by a repeat's iterations, so a finished pass would light up too. */
export function withAsk(state: ExecState, asked: boolean): ExecState {
  return asked && (state === "running" || state === "waiting" || state === "unknown")
    ? "input"
    : state;
}
