// The run-control vocabulary: which verbs exist, what each button says, and how a verb name off the
// wire is narrowed into one.

/** The run-control verbs, in the order a row presents them. */
export type RunVerb = "pause" | "resume" | "extend" | "finish_loop" | "cancel" | "retry";

/** KAS's WorkflowStatusSchema. Exported so a test can be exhaustive over it rather than over
 *  whatever subset a table happens to name. */
export const RUN_STATUSES = ["running", "paused", "completed", "failed", "aborted"] as const;

/** Button text per verb. */
export const CONTROL_LABEL: Record<RunVerb, string> = {
  pause: "Pause",
  resume: "Resume",
  extend: "Add iterations",
  finish_loop: "End this loop",
  cancel: "Cancel",
  retry: "Retry failed steps",
};

/** Narrow one verb name off the wire, or `undefined` for a word this client has no button for. */
export function asRunVerb(name: string): RunVerb | undefined {
  return Object.hasOwn(CONTROL_LABEL, name) ? (name as RunVerb) : undefined;
}

/** The verbs a control answer offers, narrowed and in the server's row order. Order is the
 *  server's because the row order is part of the answer — a destructive verb last, a re-drive
 *  first — and re-sorting here would be this module deciding a thing it was just relieved of. */
export function offeredVerbs(verbs: readonly string[]): RunVerb[] {
  const out: RunVerb[] = [];
  for (const name of verbs) {
    const verb = asRunVerb(name);
    if (verb !== undefined) {
      out.push(verb);
    }
  }
  return out;
}

/** The refusal sentences to show where the buttons would have been, in verb order so the list
 *  does not reshuffle between fetches. */
export function refusalSentences(refused: Readonly<Record<string, string>> | undefined): string[] {
  if (refused === undefined) {
    return [];
  }
  return Object.keys(refused)
    .sort((a, b) => verbRank(a) - verbRank(b) || a.localeCompare(b))
    .map((verb) => refused[verb] ?? "")
    .filter((text) => text !== "");
}

/** A verb's position in the row order, and a large number for one this client does not know — so
 *  an unrecognised verb's sentence sorts last rather than jumping the queue. */
function verbRank(name: string): number {
  const order: readonly RunVerb[] = ["retry", "resume", "extend", "finish_loop", "pause", "cancel"];
  const i = order.indexOf(name as RunVerb);
  return i < 0 ? order.length : i;
}

/** The sentence a completed retry is reported with, and the channel it belongs in. */
export function retryOutcomeNotice(count: number): { level: "success" | "error"; text: string } {
  if (count <= 0) {
    return {
      level: "error",
      text: "Nothing to retry: the workflow engine reset no step of this run.",
    };
  }
  return {
    level: "success",
    text: `Retrying ${String(count)} ${count === 1 ? "step" : "steps"}`,
  };
}
