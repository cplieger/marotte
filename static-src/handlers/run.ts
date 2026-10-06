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
  invalidateRunControls,
  noteRunChat,
  noteRunLabel,
  noteRunLive,
  noteRunSettled,
  hasLiveRunForChat,
  openRunEntry,
  openRunTurn,
  runChatID,
  runLabelOf,
  sealRunEntry,
} from "../run-store.js";
import { submitPrompt } from "../submit.js";
import { isThinking } from "../store.js";
import { trackRun } from "../run-dots.js";
import {
  pushDecision,
  collapseSettledRunInput,
  dropRunAsks,
  dropTurnDecisions,
} from "../decision-dock.js";
import { answerRunInput, continueRunStep } from "../actions/runs.js";
import { closeNotificationsFor, notifyIfHidden, NOTIFY_TITLE } from "../notify.js";
import { runTarget } from "../push-subject.js";

// Toasts at each end of a run. A START only for a SCHEDULED run (manual and agent launches already
// have attention); a COMPLETION for any run. `scheduled` must come from the server: a manual launch
// is parentless too.

/** Runs whose start has already been announced. `run_start` re-fires on
 *  every resume, so without this a scheduled run produces duplicate
 *  toasts. Cleared when the run reports finished. */
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
    // A run that is over cannot still be waiting on a person. Which asks that
    // reaches, and why each kind needs it, is `dropRunAsks`' own doc.
    dropRunAsks(p.workflow_id);
    // The ORPHANS: a step's ask with an EMPTY `run_id` is reachable only by the turn-scoped sweep,
    // whose `turn_closed` trigger never fires for a step-driven turn. Gate 1 spares the turn of a user
    // who prompted meanwhile (a live JSON-RPC request); gate 2 spares a still-running sibling run's
    // orphan. An empty chat id is a parentless run, already reached through `run:<id>`.
    if (chatID !== "" && !isThinking(chatID) && !hasLiveRunForChat(chatID)) {
      dropTurnDecisions(chatID);
    }
  }
  // Deliberately NOT opened: a run that finished before anyone looked has
  // nothing live to watch. History is the door to a finished run.
  invalidateRun(p.workflow_id);
  // An ending is the one moment the verb set changes: a live run's Pause/Cancel
  // becomes a failed run's Retry, or a completed run's nothing. Fetched here
  // rather than on every frame, so a progressing run costs no extra round trip.
  invalidateRunControls(p.workflow_id);
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
  }
});

/** The workflow id a run-scoped frame carries, or `""` for a chat's own log. */
function forRun(p: { workflow_id?: string }): string {
  return p.workflow_id ?? "";
}

/** Ask the launching agent to answer the run's open question, through `submit.ts` (the owner of
 *  what Send means). THROWS on refusal, re-enabling the card's button; submit.ts surfaces it. Names
 *  the RUN and the answer body's two unguessable fields; embeds neither the question nor the
 *  (opaque, large) ask id. */
async function deferToParentAgent(chatID: string, workflowID: string): Promise<void> {
  const text =
    `Please answer the open question on workflow run ${workflowID}.\n` +
    `Read it with GET /api/runs/${workflowID} (its open_asks), then ` +
    `POST /api/runs/${workflowID}/answer with {"ask_id": "<that ask's id>", "text": "<your answer>"}.`;
  if ((await submitPrompt(chatID, text)) === "failed") {
    throw new Error(`the deferral prompt for run ${workflowID} was refused`);
  }
}

// A workflow STEP asked a person and its run is parked; a payload, not an invalidation, because
// KAS's pause reason says nothing of the question. The ENVELOPE's chat id puts one Decision in the
// parent tab's dock and the run tab's. `notifyIfHidden`: it blocks the run indefinitely.
onSSE("run_input_needed", (chatID, p) => {
  trackRun(p.workflow_id);
  // After a reload the replayed ask can be the FIRST frame for a run; without the chat the footer
  // link opens the tab top-level. noteRunChat refuses the synthetic `run:<workflowId>`.
  noteRunChat(p.workflow_id, chatID);
  // The chat-parented discriminator, and it needs no wire field: noteRunChat refuses
  // both "" and the synthetic `run:` prefix, so a non-empty answer here means this
  // run was launched from a conversation AND names it.
  const parentChat = runChatID(p.workflow_id);
  // Live but PARKED: `executing` false, so the row's mark withholds. `parentChat` already refused
  // both spellings of "no launching chat".
  noteRunLive(p.workflow_id, parentChat, false);
  notifyIfHidden(
    NOTIFY_TITLE,
    "A workflow step is waiting for your answer",
    runTarget(p.workflow_id),
  );
  pushDecision({
    kind: "run_input",
    chatID,
    runID: p.workflow_id,
    askID: p.ask_id,
    payload: p,
    // A conditional spread because `exactOptionalPropertyTypes` refuses an explicit
    // `defer: undefined`, and the card reads PRESENCE rather than a flag.
    ...(parentChat === "" ? {} : { defer: () => deferToParentAgent(parentChat, p.workflow_id) }),
    submit: (text) => {
      if (text === null) {
        // By NODE, which the step-status verb takes (it 400s without one, so the card hides the button).
        // The server settles the ask when the status write lands.
        void continueRunStep.dispatch({ workflowID: p.workflow_id, nodeID: p.node_id });
        return;
      }
      void answerRunInput.dispatch({ workflowID: p.workflow_id, ask_id: p.ask_id, text });
    },
  });
});

// Its twin, and it does the same job `decision_settled` does for the three
// request-shaped asks: every surface is offered the ask and only the first answer
// is accepted, so something has to retire the cards that lost.
onSSE("run_input_settled", (_chatID, p) => {
  collapseSettledRunInput(p.workflow_id, p.ask_id, p.settled_by);
  void closeNotificationsFor(runTarget(p.workflow_id));
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
