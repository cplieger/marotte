// The run label on a workflow-update note: what the dock row calls the run, kept current as the
// run's state arrives, so a step card says which run it belongs to.

import { effect } from "@cplieger/reactive";
import { setSteerNoteRunLabel } from "./fundamentals/steer-note.js";
import {
  forgetRun,
  invalidateRun,
  peekRunState,
  registerRunStateDemand,
  runLabelOf,
  runState,
  runStateDemanded,
} from "./run-store.js";
import { groupWorkflowUpdates, workflowUpdateHost } from "./workflow-delivery.js";

/** `wf_` plus eight characters: enough to tell two runs apart when no label is known (an old chat). */
const SHORT_ID_LENGTH = 11;

/** The name for a run whose state is not cached, `runLabelOf`'s answer or the id's short form. Untracked. */
export function runLabelText(workflowID: string): string {
  const label = runLabelOf(workflowID);
  return label !== "" ? label : workflowID.slice(0, SHORT_ID_LENGTH);
}

/** Mounted notes watching each run; a run with a count keeps its cell. */
const holds = new Map<string, number>();
const watches = new WeakMap<HTMLElement, () => void>();

registerRunStateDemand((workflowID) => holds.has(workflowID));

/**
 * Keep `note`'s run label equal to the run's name for as long as the note is mounted. The note reads the
 * run once when nothing else does, and lets the cell go with its last watcher unless another registered
 * demand (a transcript run card, a run tab) still reads it.
 */
export function watchStepRunLabel(note: HTMLElement, workflowID: string): void {
  unwatchStepRunLabel(note);
  const count = holds.get(workflowID) ?? 0;
  if (count === 0 && peekRunState(workflowID) === undefined && !runStateDemanded(workflowID)) {
    invalidateRun(workflowID);
  }
  holds.set(workflowID, count + 1);
  let shown = note.dataset["runLabel"] ?? "";
  const stopEffect = effect(() => {
    runState(workflowID);
    const label = runLabelText(workflowID);
    if (label === shown) {
      return;
    }
    shown = label;
    setSteerNoteRunLabel(note, label);
    // The box header names the run, so it follows the label.
    const host = workflowUpdateHost(note);
    if (host !== null) {
      groupWorkflowUpdates(host);
    }
  });
  watches.set(note, () => {
    stopEffect();
    const left = (holds.get(workflowID) ?? 1) - 1;
    if (left > 0) {
      holds.set(workflowID, left);
      return;
    }
    holds.delete(workflowID);
    forgetRun(workflowID);
  });
}

/** Release a note's watch; idempotent, so a redraw and a window drop may both call it. */
export function unwatchStepRunLabel(note: HTMLElement): void {
  const stop = watches.get(note);
  if (stop === undefined) {
    return;
  }
  watches.delete(note);
  stop();
}
