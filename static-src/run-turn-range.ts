// The run's range read: `GET /api/runs/{id}/turns/{turn}?after=<seq>`.

import { join } from "@cplieger/keyenc";
import { apiGetTyped } from "./api-client.js";
import { asArray, asObject, type Decoder } from "./validators.js";
import { decodeEntry, decodeOpenEntry } from "./wire/decoders.gen.js";
import {
  adoptRunOpenEntries,
  appendRunEntry,
  clearRunHole,
  openRunTurn,
  runTurnHeldSeq,
} from "./run-store.js";
import { registerCleanup } from "./actions/index.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

interface RunTurnRangeResponse {
  entries: Entry[];
  open_entries: OpenEntry[];
}

/** Decode a list of entries, DROPPING a member the generated decoder refuses: the `seq` gap it
 *  leaves reaches the same hole check every other gap does, and the warn IS the signal. The
 *  CONTAINER still throws — a reply whose `entries` is not an array is not one bad line. The
 *  chat's loader states the same rule over the same two generated decoders. */
function decodeTolerant<T>(v: unknown, one: Decoder<T>, path: string): T[] {
  const out: T[] = [];
  const raw = asArray(v, path);
  for (let i = 0; i < raw.length; i++) {
    try {
      out.push(one(raw[i]));
    } catch (e) {
      console.warn(
        `${path}[${String(i)}]: dropped an entry:`,
        e instanceof Error ? e.message : String(e),
      );
    }
  }
  return out;
}

/** The route answers the same three keys the chat's range read answers, so no `wire-codegen`
 *  round trip is owed for it. */
const decodeRunTurnRange: Decoder<RunTurnRangeResponse> = (v) => {
  const o = asObject(v, "$.run_turn_range");
  return {
    entries: decodeTolerant(o["entries"], decodeEntry, "$.run_turn_range.entries"),
    open_entries: decodeTolerant(
      o["open_entries"],
      decodeOpenEntry,
      "$.run_turn_range.open_entries",
    ),
  };
};

/** The reads in flight, one per `(workflow, turn)`: a burst of gaps on one turn asks once,
 *  because the answer covers every gap that arrived while it was out. */
const inFlight = new Map<string, AbortController>();

registerCleanup(() => {
  for (const c of inFlight.values()) {
    c.abort();
  }
  inFlight.clear();
});

/** Ask for one step turn's entries past `afterSeq` and adopt them. Fire-and-forget: the answer
 *  reaches every reader through the run store's own log version, so a caller inside an SSE
 *  handler never awaits it. */
export function requestRunTurnRange(workflowID: string, turnID: string, afterSeq?: number): void {
  void runRange(workflowID, turnID, afterSeq);
}

function rangeURL(workflowID: string, turnID: string, afterSeq?: number): string {
  const params = afterSeq === undefined ? "" : `?after=${String(afterSeq)}`;
  return `/api/runs/${encodeURIComponent(workflowID)}/turns/${encodeURIComponent(turnID)}${params}`;
}

async function runRange(workflowID: string, turnID: string, afterSeq?: number): Promise<void> {
  if (workflowID === "" || turnID === "") {
    return;
  }
  const key = join(workflowID, turnID);
  if (inFlight.has(key)) {
    return;
  }
  const controller = new AbortController();
  inFlight.set(key, controller);
  const before = runTurnHeldSeq(workflowID, turnID);
  let d: RunTurnRangeResponse | null;
  // Released the moment it settles, before the seat: a read still counting itself would make the
  // in-flight guard swallow the second-gap re-ask the seat schedules.
  try {
    d = await apiGetTyped(
      rangeURL(workflowID, turnID, afterSeq),
      decodeRunTurnRange,
      controller.signal,
    );
  } finally {
    inFlight.delete(key);
  }
  if (controller.signal.aborted) {
    return;
  }
  if (d === null) {
    // The hole stays marked, so the pane's own step read is still owed: a repair that got no answer
    // must not tell the store the turn is whole.
    console.warn(`run turn range: no answer for ${workflowID} ${turnID}`);
    return;
  }
  adopt(workflowID, turnID, d.entries, d.open_entries, before);
}

/** The turn's open tails are REPLACED by the answer's, after the sealed entries: the answer is
 *  authoritative about them (`run-store.ts` `adoptRunOpenEntries` states why), and it must run after
 *  the seats or it would replace the tails of a turn the answer had not created yet. */
function adopt(
  workflowID: string,
  turnID: string,
  entries: readonly Entry[],
  open: readonly OpenEntry[],
  before: number | undefined,
): void {
  let want = -1;
  for (const e of entries) {
    if (e.turn !== turnID) {
      // The route serves one turn; an entry naming another is not this turn's repair.
      continue;
    }
    if (e.kind === "turn_open") {
      openRunTurn(workflowID, e);
    } else {
      appendRunEntry(workflowID, e);
    }
    want = Math.max(want, e.seq);
  }
  // REPLACED rather than seated, so a tail whose entry this answer sealed does not survive beside
  // it; the store owns that rule for both of the run's reads.
  adoptRunOpenEntries(workflowID, turnID, open);
  const held = runTurnHeldSeq(workflowID, turnID);
  if (held === undefined) {
    // Nothing held and nothing seated: the turn's own `turn_open` did not arrive, so there is no
    // turn to render and asking again would ask the same question.
    console.warn(`run turn range: ${workflowID} ${turnID} answered no turn_open`);
    return;
  }
  if (want >= 0 && held < want) {
    console.warn(`run turn range: ${workflowID} ${turnID} left a gap at seq ${String(held + 1)}`);
    if (before === undefined || held > before) {
      requestRunTurnRange(workflowID, turnID, held);
    }
    return;
  }
  clearRunHole(workflowID, turnID);
}
