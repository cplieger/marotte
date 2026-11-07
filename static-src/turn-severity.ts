// How badly a turn ended, as ONE table no surface may re-derive. Its Go twin is `marotte.SeverityOf`
// + `DefaultFailureReason` (internal/marotte/turns.go), both pinned to testdata/turn_severity.json.
// Pure and DOM-free, so the fold rule, store latch, favicon cue and renderer can all import it.

import type { TurnOutcome, TurnSeverity } from "./wire/types.gen.js";

/** How badly a turn ended, re-exported from the generated wire types: the rule is implemented in
 *  both languages, so a hand-written union would be a second spelling. */
export type { TurnSeverity };

/** Grade a turn outcome. Total and MECE: the `default` arm assigns to `never`, so a ninth outcome
 *  is a COMPILE error; the runtime fallback is `stopped`, never `clean`, for a value the decoder let
 *  through. `undefined` has its own case so it does not consume that check.
 *
 *  `interrupted` is BROKEN: a fault nobody chose stopped the turn. `unknown` is STOPPED, never
 *  broken (`ConcludeStopReason`): an unmeasured stop says nothing about success, and a status mark
 *  may fall back to ambiguous, never to reassuring. */
export function severityOf(outcome: TurnOutcome | undefined): TurnSeverity {
  switch (outcome) {
    case "running":
      return "running";
    case "completed":
      return "clean";
    case "cancelled":
    case "unknown":
    case "empty":
      return "stopped";
    case "interrupted":
    case "failed":
    case "refused":
      return "broken";
    case undefined:
      return "stopped";
    default: {
      outcome satisfies never;
      return "stopped";
    }
  }
}

/** Is this turn a failure? THE predicate, one function so every asker is greppable and spells it
 *  the same way. */
export function isBroken(outcome: TurnOutcome | undefined): boolean {
  return severityOf(outcome) === "broken";
}

/** The outcome as ONE WORD, for an accessible NAME read on every focus. Total over `TurnOutcome`.
 *  Lives here because the header dot and the turn map both read it. Display strings, not part
 *  of the cross-language contract. */
export const OUTCOME_LABEL: Record<TurnOutcome, string> = {
  running: "Running",
  completed: "Completed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  refused: "Refused",
  unknown: "Unknown",
  failed: "Failed",
  empty: "Empty",
};

/** Every `TurnOutcome` member as a runtime array, for a decoder checking persisted strings. DERIVED
 *  from the total record, so it cannot drift; the generated `TURN_OUTCOMES` is module-private. */
export const TURN_OUTCOME_VALUES = Object.keys(OUTCOME_LABEL) as readonly TurnOutcome[];

/** The outcome as a SENTENCE, for hover and description text: what a mark MEANS. Total. Unlike
 *  `defaultFailureReason` (Go-identical, "" for non-failures), it answers for every outcome. */
export const OUTCOME_TOOLTIP: Record<TurnOutcome, string> = {
  running: "This turn is still running",
  completed: "This turn finished normally",
  cancelled: "You stopped this turn",
  interrupted: "This turn was interrupted before it finished",
  refused: "The model declined to continue",
  unknown: "This turn's end could not be read",
  failed: "This turn failed",
  empty: "The agent ended this turn without answering",
};

/** What a turn says when nothing upstream did: the fallback for turns persisted before the server
 *  stamped `turn_failure_reason`. Keyed per OUTCOME (a refusal and a dropped connection want
 *  different words). "" for `completed`, `running` and `empty`, spelled as cases so a ninth outcome
 *  is a compile error. BYTE-IDENTICAL to `marotte.DefaultFailureReason`, pinned by the fixture. */
export function defaultFailureReason(outcome: TurnOutcome | undefined): string {
  switch (outcome) {
    case "failed":
      return "The agent reported an error and the turn stopped.";
    case "interrupted":
      return "The turn was interrupted before the agent finished.";
    case "refused":
      return "The model declined to continue.";
    case "unknown":
      return "The turn ended for a reason marotte could not read.";
    // `cancelled` says nothing: the footer's own outcome word already reads
    // "Cancelled" a row away, so a sentence here is one fact rendered twice.
    case "cancelled":
    case "completed":
    case "running":
    case "empty":
    case undefined:
      return "";
    default: {
      outcome satisfies never;
      return "";
    }
  }
}
