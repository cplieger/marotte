// Workflow-run SSE handlers: one store write, one bus emit, toasts. Every surface
// that shows a run reads `run-store.ts` and re-renders when it changes; the history
// list goes over the BUS, because importing history.ts here would drag chat.ts in.

import { onSSE, emitBus, BUS_RUNS_CHANGED } from "../bus.js";
import { info, jumpToast, type ToastLevel } from "../toast.js";
import { runEndJump } from "../notice-subject.js";
import {
  applyRunDelta,
  applyRunProgress,
  appendRunEntry,
  invalidateRun,
  noteRunChat,
  noteRunLabel,
  noteRunResumed,
  applyLiveRunFrame,
  hasLiveRunForChat,
  openRunEntry,
  runChatID,
  openRunTurn,
  reviseRun,
  runLabelOf,
  runRevHeld,
  runStepEnds,
  runTurnHeldSeq,
  sealRunEntry,
} from "../run-store.js";
import { scopedStep } from "../run-content-scope.js";
import {
  noteStepTurn,
  stepTurnPath,
  tailAppended,
  tailDelta,
  tailOpened,
  tailSealed,
} from "../run-step-tail.js";
import { isBroken } from "../turn-severity.js";
import { payloadOf } from "../turns.js";
import { isBusy } from "../store.js";
import { trackRun } from "../run-dots.js";
import { dropRunRequests, dropTurnDecisions } from "../decision-dock.js";
import { applyRunAsks, runAskPosition } from "../run-asks.js";
import { closeRunAsksExcept } from "../notify.js";

// Toasts at each end of a run. A START only for an unattended (scheduled or triggered) run: manual
// and agent launches already have attention. A COMPLETION for any run. `unattended` must come from
// the server: a manual launch is parentless too.

/** `run_start` re-fires on every resume, so without this an unattended run produces duplicate toasts.
 *  Cleared when the run reports finished. */
const announcedStarts = new Set<string>();

function runLabel(workflowID: string, name: string | undefined): string {
  const label = runLabelOf(workflowID);
  if (label !== "") {
    return label;
  }
  return name !== undefined && name !== "" ? name : "Workflow run";
}

/** The start toast's noun for the lease origin the server sent; undefined for an attended run. */
function startKind(origin: string | undefined): string | undefined {
  switch (origin) {
    case "scheduled":
      return "Scheduled";
    case "triggered":
      return "Triggered";
    default:
      return undefined;
  }
}

/** Raise a run's end toast, with the jump `runEndJump` picks. */
function toastCompletion(
  status: string,
  workflowID: string,
  name: string | undefined,
  parentChatID: string,
): void {
  const label = runLabel(workflowID, name);
  const end = completionText(status, label);
  if (end !== undefined) {
    jumpToast(end.message, end.level, runEndJump(workflowID, parentChatID, label));
  }
}

/** The completion toast's text and level; level follows the outcome (failed/aborted fail, cancelled
 *  was asked for). `paused` gets none: an onMaxIterations stop reports here and stays resumable. */
function completionText(
  status: string,
  label: string,
): { message: string; level: ToastLevel } | undefined {
  switch (status) {
    case "completed":
      return { message: `${label} finished`, level: "success" };
    case "failed":
      return { message: `${label} failed`, level: "error" };
    case "aborted":
      return { message: `${label} was aborted`, level: "error" };
    case "cancelled":
      return { message: `${label} was cancelled`, level: "info" };
    case "paused":
      return undefined;
    default:
      return { message: `${label} finished: ${status}`, level: "info" };
  }
}

// The chat id is read rather than discarded: non-empty names the launching
// chat (the parent tab this run nests under); empty means parentless.
onSSE("run_started", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  noteRunLabel(p.workflow_id, p.name);
  noteRunResumed(p.workflow_id, p.initiator ?? "", p.initiator_reason ?? "");
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  const kind = startKind(p.unattended);
  if (kind !== undefined && !announcedStarts.has(p.workflow_id)) {
    announcedStarts.add(p.workflow_id);
    info(`${kind} run started: ${runLabel(p.workflow_id, p.name)}`);
  }
});

onSSE("live_run", (_chatID, p, subject) => {
  applyLiveRunFrame(p, subject);
});

onSSE("run_finished", (chatID, p) => {
  trackRun(p.workflow_id);
  // Recorded even here: a later re-open nests under the launching chat.
  noteRunChat(p.workflow_id, chatID);
  noteRunLabel(p.workflow_id, p.name);
  // `paused` is stopped waiting for something, not over. Anything else is an ending.
  if (p.status !== "paused") {
    // A run that is over cannot still be waiting on a person; its step questions leave with the
    // server's final `run_asks`, its requests here.
    dropRunRequests(p.workflow_id);
    // The ORPHANS: a step's ask with an EMPTY `run_id` is reachable only by the turn-scoped sweep,
    // whose `turn_closed` trigger never fires for a step-driven turn. Gate 1 spares the turn of a user
    // who prompted meanwhile (a live JSON-RPC request); gate 2 spares a still-running sibling run's
    // orphan. An empty chat id is a parentless run, already reached through `run:<id>`. The server
    // sends this run's `live_run` removal before this frame, so gate 2 no longer counts it.
    if (chatID !== "" && !isBusy(chatID) && !hasLiveRunForChat(chatID)) {
      dropTurnDecisions(chatID);
    }
  }
  // Deliberately NOT opened: a run that finished before anyone looked has
  // nothing live to watch. History is the door to a finished run.
  invalidateRun(p.workflow_id);
  emitBus(BUS_RUNS_CHANGED);
  announcedStarts.delete(p.workflow_id);
  toastCompletion(p.status, p.workflow_id, p.name, runChatID(p.workflow_id));
});

// The run's LOG: each event into a run-store operation behind the workflow-id guard, mirroring
// `handlers/entries.ts`. The SSE adapter delivers only the scoped run's frames, and `routeOf` sends
// each to the log or to its step's tail cell.

/** Where a frame of `turnID` goes: the log for a turn it holds or one of the shown step's, the tail
 *  cell for another step's turn, nowhere for a turn no read or `turn_opened` named, which the next
 *  run read maps (its deltas wait for that read in `tailDelta`). */
function routeOf(runID: string, turnID: string): "log" | "tail" | "none" {
  if (runTurnHeldSeq(runID, turnID) !== undefined) {
    return "log";
  }
  const path = stepTurnPath(runID, turnID);
  if (path === undefined) {
    return "none";
  }
  return path === scopedStep(runID) ? "log" : "tail";
}

onSSE("turn_opened", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  const path = payloadOf(p.entry, "turn_open")?.node_path ?? "";
  if (path === "") {
    openRunTurn(runID, p.entry);
    return;
  }
  noteStepTurn(runID, p.entry.turn, path);
  if (routeOf(runID, p.entry.turn) === "log") {
    openRunTurn(runID, p.entry);
  }
  // A step's turn after an interruption is its resume, which only a run read's `step_ends` names.
  const end = runStepEnds(runID).get(path);
  if (end?.outcome === "interrupted" && end.resumed !== true) {
    readUnlessRevised(runID);
  }
});

onSSE("entry_opened", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  switch (routeOf(runID, p.open.turn)) {
    case "log":
      openRunEntry(runID, p.open);
      return;
    case "tail":
      tailOpened(runID, p.open);
      return;
    case "none":
      return;
  }
});

onSSE("entry_delta", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  switch (routeOf(runID, p.turn)) {
    case "log":
      applyRunDelta(runID, p.turn, p.entry_id, p.lane, p.n, p.delta);
      return;
    case "tail":
    case "none":
      tailDelta(runID, p.turn, p.entry_id, p.lane, p.n, p.delta);
      return;
  }
});

onSSE("entry_sealed", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  switch (routeOf(runID, p.turn)) {
    case "log":
      sealRunEntry(runID, p.turn, p.entry_id, p.lane, p.seq, p.ts, p.n);
      return;
    case "tail":
      tailSealed(runID, p.turn, p.entry_id);
      return;
    case "none":
      return;
  }
});

onSSE("entry_appended", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  switch (routeOf(runID, p.entry.turn)) {
    case "log":
      appendRunEntry(runID, p.entry);
      break;
    case "tail":
      tailAppended(runID, p.entry);
      break;
    case "none":
      break;
  }
  if (p.entry.kind === "step_graded") {
    // Both views read a step's grade from the run read's `step_ends`, which projects this entry.
    readUnlessRevised(runID);
  }
});

onSSE("turn_closed", (_chatID, p) => {
  const runID = forRun(p);
  if (runID === "") {
    return;
  }
  // A step reads settled from its own `turn_close` rather than from a flag; a tail keeps its line.
  if (routeOf(runID, p.entry.turn) === "log") {
    appendRunEntry(runID, p.entry);
  }
  // The tree grades a broken step from the run read's `step_ends`, and the applied
  // `node_complete` still says KAS's `completed`, so only a refetch turns the row red live. A
  // grade under KAS's record arrives as its own `step_graded` entry.
  const close = payloadOf(p.entry, "turn_close");
  if (isBroken(close?.outcome)) {
    readUnlessRevised(runID);
  }
});

function forRun(p: { workflow_id?: string }): string {
  return p.workflow_id ?? "";
}

/** Read the run for what a frame moved in its read, unless a held revision means the server's
 *  `run_rev` carries the move. */
function readUnlessRevised(workflowID: string): void {
  if (!runRevHeld(workflowID)) {
    invalidateRun(workflowID);
  }
}

// The run read moved: a held run reads what changed since its held revision.
onSSE("run_rev", (_chatID, p) => {
  reviseRun(p.workflow_id, p.rev, p.status);
});

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

// The one lifecycle frame carrying CONTENT: applied to the cached tree. A frame it cannot apply
// (`loop_iteration` and `steps_queued` reshape the tree, `paused` is run-level, a new iteration's
// path, or no state held) waits for the `run_rev` the server's re-read publishes, or reads the run
// when no revision is held.
onSSE("run_progress", (chatID, p) => {
  trackRun(p.workflow_id);
  noteRunChat(p.workflow_id, chatID);
  if (!applyRunProgress(p)) {
    readUnlessRevised(p.workflow_id);
  }
});
