// Builders for the server's run order (`ChatHeader.run`) as a test states it.
import { IDLE_RUN } from "../run-order.js";
import type { QueuedPrompt, RunEntryView, RunView, RunWait } from "../types.js";

/** A run order at `version` holding `entries`, with the composer's facts in `over`. */
export function runAt(
  version: number,
  entries: readonly RunEntryView[],
  over: Partial<Omit<RunView, "entries" | "version">> = {},
): RunView {
  return Object.assign({ ...IDLE_RUN, version, entries: [...entries] }, over);
}

/** A queued row waiting `wait`, with `over` for its state, arrow or row fields. */
export function rowEntry(
  id: string,
  wait: RunWait = { after: "next" },
  over: Partial<RunEntryView> & { row?: QueuedPrompt } = {},
): RunEntryView {
  return Object.assign(
    { id, kind: "row", state: "pending", origin: "queue", row: { id, text: id }, wait },
    over,
  ) as RunEntryView;
}

/** A compaction entry: running at the head, or waiting `wait`. */
export function compactionEntry(
  id: string,
  wait: RunWait,
  over: Partial<RunEntryView> = {},
): RunEntryView {
  return Object.assign(
    {
      id,
      kind: "compaction",
      state: wait.after === "running" ? "running" : "pending",
      origin: "request",
      wait,
    },
    over,
  ) as RunEntryView;
}

/** The failsafe's resume waiting `wait`, carrying marotte's prompt `text`. */
export function resumeEntry(
  id: string,
  text: string,
  wait: RunWait = { after: "compaction" },
  over: Partial<RunEntryView> = {},
): RunEntryView {
  return Object.assign(
    { id, kind: "resume", state: "pending", origin: "failsafe", row: { id, text }, wait },
    over,
  ) as RunEntryView;
}

/** A running turn kind entry at the head (a prompt's or kiro-cli's own), which the dock never draws. */
export function turnEntry(id: string, over: Partial<RunEntryView> = {}): RunEntryView {
  return Object.assign(
    {
      id,
      kind: "turn",
      state: "running",
      origin: "wire",
      turn: id,
      steerable: true,
      wait: { after: "running" },
    },
    over,
  ) as RunEntryView;
}
