// The one owner of a run step's dock rows, from the optimistic draw on Send to the `steer` entry in
// the run's log that retires them. `run-store.ts` reports each such entry and each forgotten run.

import { signal, touch } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { payloadOf } from "./turns.js";
import {
  foldSteerFrame,
  pendingSteerRow,
  retireSettledSteers,
  upsertSteerRow,
  type SteerFrame,
} from "./steer-rows.js";
import type { Entry, PendingSteer } from "./types.js";

interface RunRows {
  /** Rows per node path, in arrival order. */
  readonly byStep: Map<string, readonly PendingSteer[]>;
  /** (node path, id) pairs the run's log holds a `steer` entry for: a late frame naming one stays
   *  retired. */
  readonly settled: Set<string>;
}

const runs = new Map<string, RunRows>();

/** One signal for every step's dock: the run tab shows one step at a time. */
const stepSteerVersion = signal(0);

function bump(): void {
  stepSteerVersion.value = stepSteerVersion.peek() + 1;
}

function runOf(workflowID: string): RunRows {
  let r = runs.get(workflowID);
  if (r === undefined) {
    r = { byStep: new Map(), settled: new Set() };
    runs.set(workflowID, r);
  }
  return r;
}

function rowsAt(workflowID: string, nodePath: string): readonly PendingSteer[] {
  return runs.get(workflowID)?.byStep.get(nodePath) ?? [];
}

function settledAt(workflowID: string, nodePath: string, steerID: string): boolean {
  return runs.get(workflowID)?.settled.has(join(nodePath, steerID)) === true;
}

function write(workflowID: string, nodePath: string, rows: readonly PendingSteer[]): void {
  const r = runOf(workflowID);
  if (rows.length === 0) {
    r.byStep.delete(nodePath);
  } else {
    r.byStep.set(nodePath, rows);
  }
  bump();
}

/** In arrival order. Subscribes the caller to the rows. */
export function stepSteers(workflowID: string, nodePath: string): readonly PendingSteer[] {
  touch(stepSteerVersion);
  return rowsAt(workflowID, nodePath);
}

export function recordStepSteerQueued(
  workflowID: string,
  nodePath: string,
  frame: SteerFrame,
): void {
  if (workflowID === "" || nodePath === "" || frame.id === "") {
    return;
  }
  const fold = foldSteerFrame(rowsAt(workflowID, nodePath), frame, (steerID) =>
    settledAt(workflowID, nodePath, steerID),
  );
  if (fold !== null) {
    write(workflowID, nodePath, fold.rows);
  }
}

/** One message POST to a step, settled once by `settleStepMessage` whatever verb the server took. */
interface StepMessageSend {
  readonly workflowID: string;
  readonly nodePath: string;
  /** The steer id the server mints from the message id, which its frames and log entry carry. */
  readonly id: string;
  readonly text: string;
}

/** Open a message POST's settlement; `draw` puts its row in the dock before any frame, for a send the
 *  composer predicts will steer. */
export function beginStepMessage(
  workflowID: string,
  nodePath: string,
  messageID: string,
  text: string,
  draw: boolean,
): StepMessageSend {
  const send = { workflowID, nodePath, id: `steer-${messageID}`, text };
  if (draw) {
    write(
      workflowID,
      nodePath,
      upsertSteerRow(rowsAt(workflowID, nodePath), pendingSteerRow(send.id, text)),
    );
  }
  return send;
}

type StepMessageAnswer =
  | { readonly kind: "steered"; readonly steerID: string }
  | { readonly kind: "other" }
  | { readonly kind: "failed" };

/** Settle a send by its POST's answer. Answers whether the server holds the words: a frame or the
 *  run's log already confirmed them, drawn row or not, which a lost or refused reply cannot take back. */
export function settleStepMessage(send: StepMessageSend, answer: StepMessageAnswer): boolean {
  const { workflowID, nodePath, id, text } = send;
  if (answer.kind === "steered") {
    // A fact, as the chat's steer reply is: a replayed reply sends no second frame to confirm it.
    recordStepSteerQueued(workflowID, nodePath, {
      id: answer.steerID === "" ? id : answer.steerID,
      text,
      origin: "user",
      state: "queued",
    });
    return true;
  }
  const rows = rowsAt(workflowID, nodePath);
  const row = rows.find((r) => r.id === id);
  const held = (row !== undefined && row.pending !== true) || settledAt(workflowID, nodePath, id);
  if (answer.kind === "failed" && held) {
    return true;
  }
  if (row?.pending === true) {
    write(
      workflowID,
      nodePath,
      rows.filter((r) => r !== row),
    );
  }
  return answer.kind === "other";
}

/** Retire the rows a `steer` entry in the step's turn settles; called by `run-store.ts` for each
 *  one its log takes. */
export function retireStepSteers(workflowID: string, nodePath: string, entry: Entry): void {
  if (entry.kind !== "steer" || nodePath === "") {
    return;
  }
  const r = runOf(workflowID);
  r.settled.add(join(nodePath, entry.id));
  for (const resent of payloadOf(entry, "steer")?.resends ?? []) {
    r.settled.add(join(nodePath, resent));
  }
  const rest = retireSettledSteers(rowsAt(workflowID, nodePath), entry);
  if (rest !== null) {
    write(workflowID, nodePath, rest);
  }
}

export function forgetRunStepSteers(workflowID: string): void {
  const r = runs.get(workflowID);
  if (r === undefined) {
    return;
  }
  runs.delete(workflowID);
  if (r.byStep.size > 0) {
    bump();
  }
}

/** Forget every step's rows WITHOUT promoting them: a connect's pending snapshot re-offers what the
 *  server still holds. The settled ids stay, since the logs they came from do. */
export function forgetAllStepSteers(): void {
  let had = false;
  for (const r of runs.values()) {
    had ||= r.byStep.size > 0;
    r.byStep.clear();
  }
  if (had) {
    bump();
  }
}
