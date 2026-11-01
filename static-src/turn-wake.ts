// What started an agent-initiated turn, read off its `turn_wake` entry: the header's one-line label
// and the message under it, and which workflow-message rows the wake draws in place of their own
// cards. Pure and DOM-free.

import { payloadOf } from "./turns.js";
import type { Turn } from "./turns.js";
import type { TurnState } from "./types.js";
import { setWakingRow } from "./waking-rows.js";
import type { EntryTurnWake, WakeSteer } from "./wire/types.gen.js";

/** The line of a wake that names nothing marotte can show (`marotte.WakeFallbackLine`). */
export const WAKE_FALLBACK_LINE = "Agent-initiated turn";

/** `wf_` plus eight characters (`marotte.shortRunIDLength`, `workflow-run-label.ts`). */
const SHORT_RUN_ID_LENGTH = 11;

const SEP = " \u00b7 ";

/** What a wake calls run `id`, given the name the wake recorded ("" for none). */
export type RunNamer = (id: string, recorded: string) => string;

/** The wake's own names: the recorded one, else the id's short form (`marotte.WakeRunName`). The header
 *  and the rail pass the run store's (`workflow-run-label.ts` `wakeRunName`). */
export const recordedRunName: RunNamer = (id, recorded) =>
  recorded !== "" ? recorded : id.slice(0, SHORT_RUN_ID_LENGTH);

/** A named run in a wake: the label's or a re-delivered row's, with the name the wake recorded ("" for
 *  none), which a later name falls back to. */
export interface WakeRun {
  id: string;
  name: string;
  recorded: string;
}

/** One re-delivered message: its run's name, then `rest`, then its text. */
export interface WakeRow {
  run: WakeRun;
  rest: string;
  text: string;
}

export interface WakeLine {
  /** The run the label opens with, when it names one; `rest` follows its name. */
  run?: WakeRun;
  rest: string;
  /** The whole one-line label: the run's name, then `rest`. */
  label: string;
  /** The message under the label, "" when the wake has none. */
  body: string;
  /** A re-delivery's messages, which `body` joins one per line. */
  rows?: WakeRow[];
  /** The wake names nothing marotte can show, so the header keeps the typed system line. */
  fallback: boolean;
}

/** The wake's label and message. The twin of `marotte.WakeLine`; `internal/chat/testdata/wake_line.json`
 *  pins both. */
export function wakeLine(w: EntryTurnWake, nameOf: RunNamer = recordedRunName): WakeLine {
  switch (w.reason) {
    case "step_message":
      return stepWakeLine(w, nameOf);
    case "run_finished":
      return runFinishedLine(w, nameOf);
    case "rewake":
      return rewakeLine(w.steers ?? [], nameOf);
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

function named(run: WakeRun, rest: string, body: string): WakeLine {
  return { run, rest, label: run.name + rest, body, fallback: false };
}

function stepWakeLine(w: EntryTurnWake, nameOf: RunNamer): WakeLine {
  const s = w.steers?.[0];
  if (s === undefined) {
    return FALLBACK;
  }
  const id = s.run ?? "";
  const run = { id, name: nameOf(id, ""), recorded: "" };
  const loop = w.loop ?? "";
  const max = w.max_iterations ?? 0;
  if (w.note === "repeat_cap" && loop !== "" && max > 0) {
    return named(run, ` paused: ${loop} reached its round limit (${String(max)})`, "");
  }
  const from = SEP + (s.step ?? "");
  if (w.note === "network") {
    return named(run, `${from} is waiting for the network`, s.text);
  }
  if (w.note === "network_lost") {
    return named(run, `${from} lost the network`, s.text);
  }
  if (s.severity === "warning") {
    return named(run, `${from} asks you`, s.text);
  }
  if (s.severity === "error") {
    return named(run, `${from} reports an error`, s.text);
  }
  return named(run, `${from} to you`, s.text);
}

function runFinishedLine(w: EntryTurnWake, nameOf: RunNamer): WakeLine {
  const id = w.run ?? "";
  if (id === "") {
    return FALLBACK;
  }
  const recorded = w.run_label ?? "";
  const run = { id, name: nameOf(id, recorded), recorded };
  const byUser = w.initiator === "user";
  let rest: string;
  switch (w.run_status) {
    case "completed":
      if (byUser) {
        return named(run, " marked complete by you", "");
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
  return named(run, reason === "" ? rest : `${rest}: ${reason}`, "");
}

function rewakeLine(steers: readonly WakeSteer[], nameOf: RunNamer): WakeLine {
  if (steers.length === 0) {
    return FALLBACK;
  }
  const label =
    steers.length === 1
      ? "Resumed with 1 workflow message you stopped before it was read"
      : `Resumed with ${String(steers.length)} workflow messages you stopped before they were read`;
  const rows = steers.map((s): WakeRow => {
    const id = s.run ?? "";
    return {
      run: { id, name: nameOf(id, ""), recorded: "" },
      rest: `${SEP}${s.step ?? ""}: `,
      text: s.text,
    };
  });
  const body = rows.map((r) => r.run.name + r.rest + r.text).join("\n");
  return { rest: label, label, body, rows, fallback: false };
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
export function wakeText(t: Turn, nameOf: RunNamer = recordedRunName): string {
  const w = turnWakeOf(t);
  if (w === undefined) {
    return "";
  }
  const line = wakeLine(w, nameOf);
  if (line.fallback) {
    return "";
  }
  return line.body === "" ? line.label : `${line.label}\n${line.body}`;
}

/** The rows a wake draws in its header in place of their own cards (`marotte.WakeDrawnSteers`). */
function drawnSteers(w: EntryTurnWake): readonly WakeSteer[] {
  return w.reason === "step_message" || w.reason === "rewake" ? (w.steers ?? []) : [];
}

/** Mark every held `steer` row a held wake draws (`isWakingRow`), and unmark the rest, so a rewound
 *  wake's row draws again. Idempotent; the ids marked now. */
export function markWakingRows(turns: Iterable<TurnState>): ReadonlySet<string> {
  const held = [...turns];
  const ids = new Set<string>();
  for (const t of held) {
    for (const e of t.entries) {
      const w = payloadOf(e, "turn_wake");
      if (w !== undefined) {
        for (const s of drawnSteers(w)) {
          ids.add(s.id);
        }
      }
    }
  }
  for (const t of held) {
    for (const e of t.entries) {
      if (e.kind === "steer") {
        setWakingRow(e, ids.has(e.id));
      }
    }
  }
  return ids;
}
