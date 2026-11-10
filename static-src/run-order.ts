import type { RunEntryView, RunView, RunWait } from "./types.js";
import { decodeRunView } from "./wire/decoders.gen.js";

/** The run order of a chat with nothing on it: what a header that carries none means. */
export const IDLE_RUN: RunView = {
  send: "prompt",
  entries: [],
  next_wait: { after: "next" },
  unread_wait: { after: "next" },
  version: 0,
};

/** The run order to hold once `next` arrives: an answer's and a header frame's `run` race, so the
 *  one with the higher `version` wins and an equal or older one is ignored. */
export function newerRun(held: RunView | undefined, next: RunView | undefined): RunView {
  if (next === undefined) {
    return held ?? IDLE_RUN;
  }
  if (held !== undefined && next.version <= held.version) {
    return held;
  }
  return next;
}

/** The `run` an answer carries (`{..., run}`), or undefined when it carries none that decodes: the
 *  header frame then brings the list. */
export function runOfAnswer(body: unknown): RunView | undefined {
  if (typeof body !== "object" || body === null || !("run" in body)) {
    return undefined;
  }
  try {
    return decodeRunView(body.run);
  } catch {
    return undefined;
  }
}

const LIVE: ReadonlySet<string> = new Set(["starting", "running", "stopping", "closing"]);

export function entryLive(e: RunEntryView): boolean {
  return LIVE.has(e.state);
}

function turnKind(e: RunEntryView): boolean {
  return e.kind === "turn" || e.kind === "row" || e.kind === "unread" || e.kind === "retry";
}

/** Whether a turn runs on the chat by the server's list: any live turn-kind entry at its head, a
 *  wire turn and kiro-cli's own run included. */
export function runLive(run: RunView): boolean {
  for (const e of run.entries) {
    if (!entryLive(e)) {
      return false;
    }
    if (turnKind(e)) {
      return true;
    }
  }
  return false;
}

/** The entries the dock draws: every compaction, and a row whose message is not in the transcript
 *  yet. A turn, retry or revert entry is the transcript's (or has no words), and an unread entry is
 *  drawn as its steers' rows. */
export function dockEntries(run: RunView): RunEntryView[] {
  return run.entries.filter(
    (e) =>
      e.kind === "compaction" || (e.kind === "row" && e.row !== undefined && e.turn === undefined),
  );
}

/** The ids a close stops that the server lists as queued: row, unread and retry entries waiting,
 *  or taken with no turn open yet (`queuedPromptsPending`'s rule). */
export function runQueuedIDs(run: RunView): string[] {
  return run.entries
    .filter(
      (e) =>
        (e.kind === "row" || e.kind === "unread" || e.kind === "retry") &&
        e.turn === undefined &&
        (!entryLive(e) || e.state === "starting"),
    )
    .map((e) => e.id);
}

/** A wait's words: the dock's state label, and with `afterWords` the composer's placeholder. */
export function waitWords(wait: RunWait): string {
  switch (wait.after) {
    case "running":
      return "Running";
    case "starting":
      return "Starting";
    case "next":
      return "Sends next";
    case "held":
      return "Not sent \u00b7 the server restarted";
    case "compaction":
    case "turns":
      return `After ${afterWords(wait)}`;
  }
}

/** What "after …" completes for a queued wait; a wait that is not queued reads as the next turn. */
export function afterWords(wait: RunWait): string {
  if (wait.after === "compaction") {
    return "the compaction";
  }
  const turns = wait.turns ?? 1;
  if (wait.after === "turns" && turns > 1) {
    return `${String(turns)} turns`;
  }
  return wait.this === true ? "this turn" : "the next turn";
}
