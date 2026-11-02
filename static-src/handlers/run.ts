// Workflow-run SSE handlers: one store write, one bus emit, toasts. Every surface
// that shows a run reads `run-store.ts` and re-renders when it changes; the history
// list goes over the BUS, because importing history.ts here would drag chat.ts in.

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { info, success, error } from "../toast.js";
import {
  applyRunDelta,
  applyRunProgress,
  appendRunEntry,
  invalidateRun,
  noteRunChat,
  noteRunLabel,
  noteRunResumed,
  noteRunLive,
  noteRunSettled,
  hasLiveRunForChat,
  openRunEntry,
  openRunTurn,
  runLabelOf,
  sealRunEntry,
} from "../run-store.js";
import { isBroken } from "../turn-severity.js";
import { payloadOf } from "../turns.js";
import { isBusy } from "../store.js";
import { trackRun } from "../run-dots.js";
import { dropRunRequests, dropTurnDecisions } from "../decision-dock.js";
import { applyRunAsks, runAskPosition } from "../run-asks.js";
import { closeRunAsksExcept } from "../notify.js";

// Toasts at each end of a run. A START only for a SCHEDULED run (manual and agent launches already
// have attention); a COMPLETION for any run. `scheduled` must come from the server: a manual launch
// is parentless too.

/** `run_start` re-fires on every resume, so without this a scheduled run produces duplicate toasts.
 *  Cleared when the run reports finished. */
const announcedStarts = new Set<string>();

function runLabel(workflowID: string, name: string | undefined): string {
  const label = runLabelOf(workflowID);
  if (label !== "") {
    return label;
  }
  return name !== undefined && name !== "" ? name : "Workflow run";
}

/** The completion toast; level follows the outcome (failed/aborted fail, cancelled was asked for).
 *  `paused` gets none: an onMaxIterations stop reports here and stays resumable. */
function toastCompletion(status: string, workflowID: string, name: string | undefined): void {
  const label = runLabel(workflowID, name);
  switch (status) {
    case "completed":
      success(`${label} finished`);
      return;
    case "failed":
      error(`${label} failed`);
      return;
    case "aborted":
      error(`${label} was aborted`);
      return;
    case "cancelled":
      info(`${label} was cancelled`);
      return;
    case "paused":
      return;
    default:
      info(`${label} finished: ${status}`);
  }
}

// The chat id is read rather than discarded: non-empty names the launching
// chat (the parent tab this run nests under); empty means parentless.
onSSE("run_started", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  noteRunLabel(p.workflow_id, p.name);
  noteRunResumed(p.workflow_id, p.initiator ?? "", p.initiator_reason ?? "");
  // A start frame is proof of execution: it fires on the launch and again on every
  // resume, so it is exactly the moment frames begin arriving into this chat.
  noteRunLive(p.workflow_id, chatID, true);
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  if (p.scheduled === true && !announcedStarts.has(p.workflow_id)) {
    announcedStarts.add(p.workflow_id);
    info(`Scheduled run started: ${runLabel(p.workflow_id, p.name)}`);
  }
});

onSSE("run_finished", (chatID, p) => {
  trackRun(p.workflow_id);
  // Recorded even here: a later re-open nests under the launching chat.
  noteRunChat(p.workflow_id, chatID);
  noteRunLabel(p.workflow_id, p.name);
  // `paused` stays live: stopped waiting for something, not over. Anything
  // else is an ending.
  if (p.status === "paused") {
    // A policy stop (`onMaxIterations`) reports through this same frame, so the run
    // is resumable and its row stays — but it has stopped executing, which is what
    // lets its chat's message window be reclaimed while it sits.
    noteRunLive(p.workflow_id, chatID, false);
  } else {
    noteRunSettled(p.workflow_id);
    // A run that is over cannot still be waiting on a person; its step questions leave with the
    // server's final `run_asks`, its requests here.
    dropRunRequests(p.workflow_id);
    // The ORPHANS: a step's ask with an EMPTY `run_id` is reachable only by the turn-scoped sweep,
    // whose `turn_closed` trigger never fires for a step-driven turn. Gate 1 spares the turn of a user
    // who prompted meanwhile (a live JSON-RPC request); gate 2 spares a still-running sibling run's
    // orphan. An empty chat id is a parentless run, already reached through `run:<id>`.
    if (chatID !== "" && !isBusy(chatID) && !hasLiveRunForChat(chatID)) {
      dropTurnDecisions(chatID);
    }
  }
  // Deliberately NOT opened: a run that finished before anyone looked has
  // nothing live to watch. History is the door to a finished run.
  // The verbs are re-read behind this (`run-store.ts` `invalidateRunControls`).
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  announcedStarts.delete(p.workflow_id);
  toastCompletion(p.status, p.workflow_id, p.name);
});

// The run's LOG: each event into a run-store operation behind the workflow-id guard, mirroring
// `handlers/entries.ts`.

onSSE("turn_opened", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    openRunTurn(runID, p.entry);
  }
});

onSSE("entry_opened", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    openRunEntry(runID, p.open);
  }
});

onSSE("entry_delta", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    applyRunDelta(runID, p.turn, p.entry_id, p.lane, p.n, p.delta);
  }
});

onSSE("entry_sealed", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    sealRunEntry(runID, p.turn, p.entry_id, p.lane, p.seq, p.ts, p.n);
  }
});

onSSE("entry_appended", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    appendRunEntry(runID, p.entry);
  }
});

onSSE("turn_closed", (_chatID, p) => {
  const runID = forRun(p);
  if (runID !== "") {
    // A step reads settled from its own `turn_close` rather than from a flag.
    appendRunEntry(runID, p.entry);
    // The tree grades a broken step from the run read's `step_ends`, and the applied
    // `node_complete` still says KAS's `completed`, so only a refetch turns the row red live. A
    // `tool_use` stop is broken only under KAS's record of the step, which the server reads
    // (`stepClose.graded`).
    const close = payloadOf(p.entry, "turn_close");
    if (isBroken(close?.outcome) || close?.stop_reason_raw === "tool_use") {
      invalidateRun(runID);
    }
  }
});

function forRun(p: { workflow_id?: string }): string {
  return p.workflow_id ?? "";
}

// Which steps of one run wait on a person, replacing the store's snapshot of that run whole. A
// reachable snapshot is the whole truth about the run's waits, so a banner for a wait it does not
// hold is stale wherever it was shown.
onSSE("run_asks", (chatID, p) => {
  if (!applyRunAsks(p)) {
    return;
  }
  if (p.asks.length > 0) {
    trackRun(p.workflow_id);
    // After a reload a snapshot can be the FIRST frame for a run; without the chat the footer
    // link opens the tab top-level. noteRunChat refuses the synthetic `run:<workflowId>`.
    noteRunChat(p.workflow_id, chatID);
  }
  if (p.reachable) {
    void closeRunAsksExcept(
      p.workflow_id,
      new Set(p.asks.map((a) => runAskPosition(p.workflow_id, a))),
    );
  }
});

// The one lifecycle frame carrying CONTENT: applied to the cached tree, refetching only when it
// cannot be (`loop_iteration` and `steps_queued` reshape the tree, `paused` is run-level, or no
// state is held). A refetch per node event is a KAS round trip for the whole tree.
onSSE("run_progress", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  // Proof of life; proof of EXECUTION unless run-level `paused`. A node-level `node_paused` is a step
  // waiting inside a running run, so it reads as executing.
  noteRunLive(p.workflow_id, chatID, p.kind !== "paused");
  if (!applyRunProgress(p)) {
    invalidateRun(p.workflow_id);
  }
});
