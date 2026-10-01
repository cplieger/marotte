// ---------------------------------------------------------------------------
// The step GET: `GET /api/runs/{id}/steps/{path...}`, read on demand, per step.
//
// A run's step transcript has ONE source — the run's own entry log, held by
// `run-store.ts` — and this is the door that fills it for a step the STREAM did not
// deliver. Two cases, both of them gaps rather than routes: a step that opened and
// closed entirely inside a connection gap, so the store holds no turn for the path
// at all, and a turn whose `turn_close` was lost, so the store holds a turn that
// reads live forever. The endpoint answers whole turns either way (`turn_open` and
// `turn_close` included, in file order, plus the open tails of any turn still open),
// and when the log holds no turn for the path it falls back to KAS's replay
// projected into the same shape.
//
// SO THIS MODULE ADOPTS RATHER THAN CACHING CONTENT. The answer's entries go into
// the run store through the same five operations the live frames use, so the pane
// has one place to read from and a re-read cannot disagree with a stream. What stays
// here is the VERDICT, which is the only thing the pane's empty-step note is keyed
// on and the only thing the store has no field for.
//
// DOM-FREE and fetch-owning, like `run-store.ts`: the page reads it inside its own
// effect. It is deliberately NOT part of `run-store.ts`, because that module is the
// one owner of `GET /api/runs/{id}` and its per-run signals exist for many cards on
// screen at once; this is a different endpoint with a different key and a different
// repaint shape (see `stepTranscriptVersion`).
// ---------------------------------------------------------------------------

import { signal } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { apiGetTypedOrError } from "./api-client.js";
import { decodeRunStepTranscript } from "./wire/decoders.gen.js";
import {
  adoptRunOpenEntries,
  appendRunEntry,
  clearRunHole,
  openRunEntry,
  openRunTurn,
} from "./run-store.js";
import type { Entry, OpenEntry, RunStepTranscriptState } from "./types.js";

/** One step's read, as this module holds it.
 *
 *  TWO members are this side's own and neither is on the wire (`RunStepTranscriptState`
 *  is unchanged, so nothing here needs regenerating): `loading` means a request is
 *  outstanding, and `unaddressable` means the SERVER refused the address with a 4xx,
 *  which only this side can distinguish from a transport failure. Both are members of
 *  the same union rather than flags beside it, so the consumer's branch is total. */
type StepReadState = "loading" | "unaddressable" | RunStepTranscriptState;

/** One cache entry: the verdict, and nothing else.
 *
 *  The CONTENT is in the run store, adopted under the turn ids the answer named, so
 *  there is no second copy to keep in step with the stream. Before that this held the
 *  blocks and tool calls itself and the pane preferred one route over the other per
 *  step, which is the whole mechanism a run's own log replaces. */
export interface StepRead {
  readonly state: StepReadState;
}

/** ONE signal for every step read, bumped when a fetch RESOLVES.
 *
 *  One rather than one per step, which is the inverse of `run-store.ts`'s per-run
 *  signals — and the reason those exist does not apply here. That module is read by
 *  a transcript full of run cards, so a bump for run A must not repaint run B's
 *  card. This module is read by the run PAGE, which shows one node at a time, so a
 *  coarse bump costs exactly one repaint of the page the reader is looking at.
 *
 *  It carries the VERDICT only: the entries the same answer adopted bump the run
 *  store's own per-run log version, which is what repaints the transcript itself.
 *
 *  Bumped on resolution rather than on request: the request is what the caller just
 *  did, so it needs no telling. */
export const stepTranscriptVersion = signal(0);

/** The cache, keyed by (workflow, node path). */
const reads = new Map<string, StepRead>();

/** In-flight keys, so a repaint during a fetch cannot start a second one.
 *
 *  Separate from the cache rather than a `loading` entry in it, because the two
 *  answer different questions: the cache answers "what do we know", this answers
 *  "is a request outstanding". Folding them would make the no-refetch rule depend
 *  on an entry a failed fetch has to remember to clean up. */
const inFlight = new Set<string>();

/** The cache key.
 *
 *  keyenc `join`, the fleet rule for a composite key over text this app does not
 *  author — a node path is arbitrary text from a workflow file, joined with "/".
 *  Honest scope: with the arbitrary component LAST, a template literal would not
 *  actually collide for the ids KAS mints, so this removes the question rather than
 *  answering a live defect, and it keeps holding if a third component is ever
 *  added. */
function readKey(workflowID: string, nodePath: string): string {
  return join(workflowID, nodePath);
}

/** What this client knows about one step's read, or undefined for a step nobody has
 *  asked about. A step whose content arrived on the STREAM is that case: nothing has
 *  been read, and nothing needs to be. */
export function stepRead(workflowID: string, nodePath: string): StepRead | undefined {
  return reads.get(readKey(workflowID, nodePath));
}

/** Drop every read. Called from the page's own mount, which is this cache's whole
 *  bound: a run tab retargeting or closing is when the entries stop being wanted,
 *  and there is no other moment a step's answer becomes wrong.
 *
 *  It clears the VERDICTS and never the run store's turns: those are the run's
 *  record, held under the run's own eviction rules, and a page retarget says nothing
 *  about them. */
export function clearStepTranscripts(): void {
  reads.clear();
  inFlight.clear();
}

/** The URL for one step read.
 *
 *  Segments are `encodeURIComponent`-ed INDIVIDUALLY and rejoined with a raw "/",
 *  which is the one encoding rule this route has: the separators must stay raw
 *  because a node path contains them and `internal/server`'s canonical-path gate
 *  refuses an encoded spelling (it compares the DECODED path against what ServeMux
 *  would route), while a segment's own `#`, space or `%` has to be encoded or the
 *  path is truncated at the fragment or mis-split.
 *
 *  Measured against Go's ServeMux: `{path...}` hands the handler the DECODED
 *  remainder, so the server compares it to the run tree's own join as it stands. */
function stepURL(workflowID: string, nodePath: string): string {
  const path = nodePath.split("/").map(encodeURIComponent).join("/");
  return `/api/runs/${encodeURIComponent(workflowID)}/steps/${path}`;
}

/** Whether an answer is settled — never worth asking again.
 *
 *  `unavailable` is the one verdict that is NOT settled: it means the read could not
 *  be completed, which is transient by definition. That retry is bounded by the
 *  CALLER arming a read once per shown (node, state) rather than once per repaint,
 *  which is the run page's own gate; there is no backoff here, because a repaint is
 *  not a retry loop.
 *
 *  `unaddressable` IS settled, and that is what the state exists for: the server
 *  refused the address itself, so asking again fails identically and a retry the
 *  reader can spend is a lie about what it costs.
 *
 *  `loading` is deliberately absent, and the reason is that it would decide nothing:
 *  a `loading` entry exists exactly while its key is in `inFlight` (both are written
 *  together and every answer path overwrites both), so the in-flight gate always
 *  returns first and a `loading` arm here is a condition no behaviour can depend on.
 *  Naming it would read as a second guard while being unfalsifiable. */
function settled(state: StepReadState): boolean {
  return state === "ready" || state === "gone" || state === "unaddressable";
}

/** Ask for one step's transcript, unless the answer is already settled or a request
 *  is outstanding.
 *
 *  Fire-and-forget: the answer arrives through the run store plus one version bump,
 *  so a caller inside a reactive effect never awaits it. */
export function requestStepTranscript(workflowID: string, nodePath: string): void {
  if (workflowID === "" || nodePath === "") {
    return;
  }
  const key = readKey(workflowID, nodePath);
  if (inFlight.has(key)) {
    return;
  }
  const known = reads.get(key);
  if (known !== undefined && settled(known.state)) {
    return;
  }
  inFlight.add(key);
  reads.set(key, { state: "loading" });
  void fetchStep(key, workflowID, nodePath);
}

/** Re-ask for a step whose turn the store holds INCOMPLETE.
 *
 *  A `seq` hole and a lost `turn_close` are both repaired by the same whole-turn
 *  answer, and this is the only door to it: a run's log has no range read served, so
 *  the pane's own GET is the repair (`run-store.ts` `clearRunHole` records that it
 *  landed). It drops a settled verdict first, because that verdict describes an
 *  answer this client has since found wanting — without which one hole would settle
 *  the step forever. */
export function rereadStepTranscript(workflowID: string, nodePath: string): void {
  const key = readKey(workflowID, nodePath);
  if (inFlight.has(key)) {
    return;
  }
  reads.delete(key);
  requestStepTranscript(workflowID, nodePath);
}

/** Perform one read, adopt what it carries, and record its verdict.
 *
 *  `apiGetTypedOrError` rather than `apiGetTyped`, because the STATUS is what
 *  separates a settled refusal from a transient one and the collapsing helper hands
 *  back one null for both. Its own doc names this defect class. */
async function fetchStep(key: string, workflowID: string, nodePath: string): Promise<void> {
  try {
    const r = await apiGetTypedOrError(stepURL(workflowID, nodePath), decodeRunStepTranscript);
    if (r.ok && r.data !== null) {
      adopt(workflowID, r.data);
      reads.set(key, { state: r.data.state });
    } else {
      reads.set(key, failedRead(r.status));
    }
  } catch {
    // A THROW never reached the server, so it takes the transient verdict via
    // status 0. Caught rather than left to propagate for two reasons: the caller
    // `void`s this promise (it is fire-and-forget by design), so an escaping
    // rejection is an unhandled one; and the entry would otherwise keep claiming
    // `loading` with no request behind it.
    reads.set(key, failedRead(0));
  } finally {
    // In the `finally`, so neither answer path can leave a key in flight and stop
    // that step ever being asked about again.
    inFlight.delete(key);
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
  }
}

/** Commit one answer's entries to the run store.
 *
 *  Through the SAME operations the live frames use, so a re-read cannot produce a
 *  turn the stream could not have produced: a `turn_open` creates the turn
 *  (idempotent by id), every other entry appends at the position its own `seq`
 *  claims, and an entry already held under that `seq` with the same id is dropped as
 *  a redelivery. That is what makes this repair a HOLE FILL rather than a rewrite —
 *  the prefix the store already holds is recognised and skipped, and the append
 *  resumes at the gap.
 *
 *  Each turn's open tails are REPLACED by the answer's, after the sealed entries
 *  (`run-store.ts` `adoptRunOpenEntries` states why, and why not before them).
 *
 *  `clearRunHole` per turn the answer named, because the store's hole marker is what
 *  makes the pane re-ask and this answer is the repair it was waiting for. Clearing a
 *  turn the store never marked is a no-op there.
 *
 *  The reply's `subject` stamps are NOT observed here, and that is not a gap: the
 *  store's own append path stamps every OPEN turn it commits, under the same
 *  `<turn>:<newest sealed seq>` rule and from the entries it just took, so adopting
 *  through it produces the identical stamp and drops it at the `turn_close` in the
 *  same pass. */
function adopt(workflowID: string, answer: { entries: Entry[]; open_entries: OpenEntry[] }): void {
  const turns = new Set<string>();
  for (const entry of answer.entries) {
    turns.add(entry.turn);
    if (entry.kind === "turn_open") {
      openRunTurn(workflowID, entry);
    } else {
      appendRunEntry(workflowID, entry);
    }
  }
  const tails = new Map<string, OpenEntry[]>();
  for (const open of answer.open_entries) {
    const held = tails.get(open.turn);
    if (held === undefined) {
      tails.set(open.turn, [open]);
    } else {
      held.push(open);
    }
  }
  for (const turnID of turns) {
    adoptRunOpenEntries(workflowID, turnID, tails.get(turnID) ?? []);
    tails.delete(turnID);
    clearRunHole(workflowID, turnID);
  }
  // A tail naming a turn this answer carried no entries for: the whole-turn answer should have
  // held its `turn_open`, so it goes through the operation that marks the hole saying so.
  for (const rest of tails.values()) {
    for (const open of rest) {
      openRunEntry(workflowID, open);
    }
  }
}

/** The verdict for a read that produced no content, keyed on the HTTP status.
 *
 *  A 4xx on this route is SETTLED: `handleStepTranscript`'s only 4xx answers are 400
 *  (missing id, missing path, a path whose first segment is not this run's id) and
 *  404 (`errStepUnknown`), plus a 405 for a method this client never sends, and
 *  asking again cannot change any of them. No route under `/api/runs` emits a 429,
 *  so an unknown future 4xx landing here is the safe direction and a rate limiter
 *  arriving would need its own handling.
 *
 *  Everything else keeps `unavailable`: a 5xx, a status-0 transport failure, and an
 *  undecodable body, which arrives on the failure side carrying its real 2xx status
 *  and is therefore graded transient rather than as the caller's mistake. */
function failedRead(status: number): StepRead {
  const settledRefusal = status >= 400 && status < 500;
  return { state: settledRefusal ? "unaddressable" : "unavailable" };
}
