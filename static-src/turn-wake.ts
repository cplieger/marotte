// What started an agent-initiated turn, read off its `turn_wake` entry: the header's one-line label
// and the message under it, and which workflow-message rows the wake draws in place of their own
// cards. Pure and DOM-free.

import { payloadOf } from "./turns.js";
import type { Turn } from "./turns.js";
import type { Entry, TurnState } from "./types.js";
import type { EntryTurnWake, WakeSteer } from "./wire/types.gen.js";

/** The line of a wake that names nothing marotte can show (`marotte.WakeFallbackLine`). */
export const WAKE_FALLBACK_LINE = "Agent-initiated turn";

/** `wf_` plus eight characters (`marotte.shortRunIDLength`, `workflow-run-label.ts`). */
const SHORT_RUN_ID_LENGTH = 11;

const SEP = " \u00b7 ";

export interface WakeLine {
  /** The run the label opens with, when it names one; `rest` follows its name. */
  run?: { id: string; name: string };
  rest: string;
  /** The whole one-line label: the run's name, then `rest`. */
  label: string;
  /** The message under the label, "" when the wake has none. */
  body: string;
  /** The wake names nothing marotte can show, so the header keeps the typed system line. */
  fallback: boolean;
}

/** What a wake calls a run: its recorded label, else the id's short form (`marotte.WakeRunName`). */
function wakeRunName(run: string, label: string | undefined): string {
  if (label !== undefined && label !== "") {
    return label;
  }
  return run.slice(0, SHORT_RUN_ID_LENGTH);
}

/** The wake's label and message. The twin of `marotte.WakeLine`; `internal/chat/testdata/wake_line.json`
 *  pins both. */
export function wakeLine(w: EntryTurnWake): WakeLine {
  switch (w.reason) {
    case "step_message":
      return stepWakeLine(w);
    case "run_finished":
      return runFinishedLine(w);
    case "rewake":
      return rewakeLine(w.steers ?? []);
    case "unknown":
      return FALLBACK;
    default:
      w.reason satisfies never;
      return FALLBACK;
  }
}

const FALLBACK: WakeLine = {
  rest: WAKE_FALLBACK_LINE,
  label: WAKE_FALLBACK_LINE,
  body: "",
  fallback: true,
};

function named(run: string, name: string, rest: string, body: string): WakeLine {
  return { run: { id: run, name }, rest, label: name + rest, body, fallback: false };
}

function stepWakeLine(w: EntryTurnWake): WakeLine {
  const s = w.steers?.[0];
  if (s === undefined) {
    return FALLBACK;
  }
  const run = s.run ?? "";
  const name = wakeRunName(run, s.run_label);
  const loop = w.loop ?? "";
  const max = w.max_iterations ?? 0;
  if (w.note === "repeat_cap" && loop !== "" && max > 0) {
    return named(run, name, ` paused: ${loop} reached its round limit (${String(max)})`, "");
  }
  const from = SEP + (s.step ?? "");
  if (w.note === "network") {
    return named(run, name, `${from} is waiting for the network`, s.text);
  }
  if (s.severity === "warning") {
    return named(run, name, `${from} asks you`, s.text);
  }
  if (s.severity === "error") {
    return named(run, name, `${from} reports an error`, s.text);
  }
  return named(run, name, `${from} to you`, s.text);
}

function runFinishedLine(w: EntryTurnWake): WakeLine {
  const run = w.run ?? "";
  if (run === "") {
    return FALLBACK;
  }
  const name = wakeRunName(run, w.run_label);
  const byUser = w.initiator === "user";
  let rest: string;
  switch (w.run_status) {
    case "completed":
      if (byUser) {
        return named(run, name, " marked complete by you", "");
      }
      rest = " completed";
      break;
    case "failed":
      rest = " failed";
      break;
    case "aborted":
    case "cancelled":
      rest = byUser ? " stopped by you" : " stopped";
      break;
    default:
      return FALLBACK;
  }
  const reason = w.stop_reason ?? "";
  return named(run, name, reason === "" ? rest : `${rest}: ${reason}`, "");
}

function rewakeLine(steers: readonly WakeSteer[]): WakeLine {
  if (steers.length === 0) {
    return FALLBACK;
  }
  const label =
    steers.length === 1
      ? "Resumed with 1 workflow message you stopped before it was read"
      : `Resumed with ${String(steers.length)} workflow messages you stopped before they were read`;
  const body = steers
    .map((s) => `${wakeRunName(s.run ?? "", s.run_label)}${SEP}${s.step ?? ""}: ${s.text}`)
    .join("\n");
  return { rest: label, label, body, fallback: false };
}

/** The turn's wake, the first lane-`""` `turn_wake` of its body; undefined for a prompt turn, which
 *  never records one. */
export function turnWakeOf(t: Turn): EntryTurnWake | undefined {
  if (t.trigger !== undefined) {
    return undefined;
  }
  for (const e of t.body) {
    if (e.kind === "turn_wake" && (e.lane ?? "") === "") {
      return payloadOf(e, "turn_wake");
    }
  }
  return undefined;
}

/** What the wake says as one string, label then message, for the rail and Copy as text; "" for a
 *  turn whose wake names nothing. */
export function wakeText(t: Turn): string {
  const w = turnWakeOf(t);
  if (w === undefined) {
    return "";
  }
  const line = wakeLine(w);
  if (line.fallback) {
    return "";
  }
  return line.body === "" ? line.label : `${line.label}\n${line.body}`;
}

/** The held turn whose step-message wake draws `steerID` as its trigger, or undefined. */
export function wakeTurnFor(
  turns: ReadonlyMap<string, TurnState>,
  steerID: string,
): string | undefined {
  for (const [id, t] of turns) {
    for (const e of t.entries) {
      const w = payloadOf(e, "turn_wake");
      if (w?.reason === "step_message" && (w.steers ?? []).some((s) => s.id === steerID)) {
        return id;
      }
    }
  }
  return undefined;
}

/** Rows a held step-message wake draws as its trigger. A WeakSet over the store's own entry objects,
 *  so a page that replaces them starts unmarked until `markWakingRows` reads it. */
const wakingRows = new WeakSet<Entry>();

/** Whether a later turn's wake draws this workflow message as its trigger, so the row draws nothing
 *  where it was filed. */
export function isWakingRow(e: Entry): boolean {
  return wakingRows.has(e);
}

/** Mark every held `steer` row a held step-message wake names, and unmark the rest, so a rewound
 *  wake's row draws again. Idempotent; the ids marked now. */
export function markWakingRows(turns: Iterable<TurnState>): ReadonlySet<string> {
  const held = [...turns];
  const ids = new Set<string>();
  for (const t of held) {
    for (const e of t.entries) {
      const w = payloadOf(e, "turn_wake");
      if (w?.reason === "step_message") {
        for (const s of w.steers ?? []) {
          ids.add(s.id);
        }
      }
    }
  }
  for (const t of held) {
    for (const e of t.entries) {
      if (e.kind !== "steer") {
        continue;
      }
      if (ids.has(e.id)) {
        wakingRows.add(e);
      } else {
        wakingRows.delete(e);
      }
    }
  }
  return ids;
}
