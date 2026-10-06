// The step GET: `GET /api/runs/{id}/steps/{path...}`, read on demand, per step.

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

/** One step's read, as this module holds it. */
type StepReadState = "loading" | "unaddressable" | RunStepTranscriptState;

/** One cache entry: the verdict, and nothing else. */
export interface StepRead {
  readonly state: StepReadState;
}

/** ONE signal for every step read, bumped when a fetch RESOLVES. */
export const stepTranscriptVersion = signal(0);

/** The cache, keyed by (workflow, node path). */
const reads = new Map<string, StepRead>();

/** In-flight keys, so a repaint during a fetch cannot start a second one. Separate from the
 *  cache rather than a `loading` entry in it, because the two answer different questions: the
 *  cache answers "what do we know", this answers "is a request outstanding". */
const inFlight = new Set<string>();

/** The cache key. */
function readKey(workflowID: string, nodePath: string): string {
  return join(workflowID, nodePath);
}

/** What this client knows about one step's read, or undefined for a step nobody has asked about.
 *  A step whose content arrived on the STREAM is that case: nothing has been read, and nothing
 *  needs to be. */
export function stepRead(workflowID: string, nodePath: string): StepRead | undefined {
  return reads.get(readKey(workflowID, nodePath));
}

/** Drop every read. Called from the page's own mount, which is this cache's whole bound: a run
 *  tab retargeting or closing is when the entries stop being wanted, and there is no other
 *  moment a step's answer becomes wrong. It clears the VERDICTS and never the run store's turns:
 *  those are the run's record, held under the run's own eviction rules, and a page retarget says
 *  nothing about them. */
export function clearStepTranscripts(): void {
  reads.clear();
  inFlight.clear();
}

/** The URL for one step read. Segments are `encodeURIComponent`-ed INDIVIDUALLY and rejoined
 *  with a raw "/", which is the one encoding rule this route has: the separators must stay raw
 *  because a node path contains them and `internal/server`'s canonical-path gate refuses an
 *  encoded spelling (it compares the DECODED path against what ServeMux would route), while a
 *  segment's own `#`, space or `%` has to be encoded or the path is truncated at the fragment or
 *  mis-split. */
function stepURL(workflowID: string, nodePath: string): string {
  const path = nodePath.split("/").map(encodeURIComponent).join("/");
  return `/api/runs/${encodeURIComponent(workflowID)}/steps/${path}`;
}

/** Whether an answer is settled — never worth asking again. */
function settled(state: StepReadState): boolean {
  return state === "ready" || state === "gone" || state === "unaddressable";
}

/** Ask for one step's transcript, unless the answer is already settled or a request is
 *  outstanding. Fire-and-forget: the answer arrives through the run store plus one version bump,
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

/** Re-ask for a step whose turn the store holds INCOMPLETE. */
export function rereadStepTranscript(workflowID: string, nodePath: string): void {
  const key = readKey(workflowID, nodePath);
  if (inFlight.has(key)) {
    return;
  }
  reads.delete(key);
  requestStepTranscript(workflowID, nodePath);
}

/** Perform one read, adopt what it carries, and record its verdict. `apiGetTypedOrError` rather
 *  than `apiGetTyped`, because the STATUS is what separates a settled refusal from a transient
 *  one and the collapsing helper hands back one null for both. */
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
    // A THROW never reached the server, so it takes the transient verdict via status 0.
    reads.set(key, failedRead(0));
  } finally {
    // In the `finally`, so neither answer path can leave a key in flight and stop that step ever
    // being asked about again.
    inFlight.delete(key);
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
  }
}

/** Commit one answer's entries to the run store. `clearRunHole` per turn the answer named,
 *  because the store's hole marker is what makes the pane re-ask and this answer is the repair
 *  it was waiting for. */
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
  // A tail naming a turn this answer carried no entries for: the whole-turn answer should have held
  // its `turn_open`, so it goes through the operation that marks the hole saying so.
  for (const rest of tails.values()) {
    for (const open of rest) {
      openRunEntry(workflowID, open);
    }
  }
}

/** The verdict for a read that produced no content, keyed on the HTTP status. A 4xx on this
 *  route is SETTLED: `handleStepTranscript`'s only 4xx answers are 400 (missing id, missing
 *  path, a path whose first segment is not this run's id) and 404 (`errStepUnknown`), plus a 405
 *  for a method this client never sends, and asking again cannot change any of them. */
function failedRead(status: number): StepRead {
  const settledRefusal = status >= 400 && status < 500;
  return { state: settledRefusal ? "unaddressable" : "unavailable" };
}
