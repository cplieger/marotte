// SSE handlers for turn lifecycle, the three decision types, and errors. A decision is ENQUEUED in
// `decision-dock.ts`'s per-chat queue, so a background chat's permission still reaches the dock.

import { onSSE } from "../bus.js";
import { appendEntry, setWorkingLabel, get, getActiveId, setTurnOpen } from "../store.js";
import { payloadOf } from "../turns.js";
import { closeNotificationsFor, notifyIfHidden, NOTIFY_TITLE } from "../notify.js";
import { askTarget } from "../push-subject.js";
import { noteAgentFinished } from "../agent-finished-cue.js";
import { pushDecision, collapseSettledDecision, dropTurnDecisions } from "../decision-dock.js";
import { setAgentDown, clearAgentDown } from "../send-state.js";
import { reportFailure } from "../failure-notice.js";
import { refreshGitBadge } from "../git.js";
import type { ToastRetry } from "../toast.js";
import { openSetting } from "../settings-highlight.js";
import { showLoginModal } from "../modals.js";
import { respondPermission, respondElicitation, respondUserInput } from "../actions/chat.js";
import { ERROR_ROUTES, type ErrorAction } from "./error-routing.js";
import { clearTurnState } from "../turn-teardown.js";
import { refreshTurnRail } from "../turn-rail.js";
import { severityOf, defaultFailureReason } from "../turn-severity.js";
import type { TurnOutcome } from "../wire/types.gen.js";
export { ERROR_ROUTES };

// Whether a cue may be raised YET is `agent-finished-cue.ts`'s; this owns what one would say.

/** What an off-screen notification SAYS about a finished turn, "" for silence. A TOTAL switch on
 *  the SEVERITY with no default (a new member fails `noImplicitReturns`); `unknown` says nothing.
 *  `turn-severity.ts` owns the wording, shared with internal/agent/turn_finalize.go. */
function notifyBodyFor(outcome: TurnOutcome | undefined, name: string): string {
  switch (severityOf(outcome)) {
    case "clean":
      return `${name}: Agent finished`;
    case "broken":
      return `${name}: ${defaultFailureReason(outcome)}`;
    case "stopped":
    case "running":
      return "";
  }
}

onSSE("working_label", (chatID, p) => {
  setWorkingLabel(chatID, p.label);
});

/** Whether another turn of this chat has no `turn_close`, asked AFTER appending this close: a
 *  prompt's turn is stored from admission, so an agent turn closing meanwhile leaves the chat live. */
function anotherTurnOpen(chatID: string, closedTurn: string): boolean {
  const s = get(chatID);
  if (s === undefined) {
    return false;
  }
  for (const [id, state] of s.turns) {
    if (id !== closedTurn && state.closeAt === undefined) {
      return true;
    }
  }
  return false;
}

// ONE arm. The frame names its turn in the entry and every turn a chat's log holds is the
// chat's, so there is nothing left to scope. What decides whether this close SETTLES the
// chat is the log itself — whether any other turn of it is still open.
onSSE("turn_closed", (chatID, p) => {
  // A RUN's turn, which `handlers/run.ts` owns: it carries an empty chat id, so without this
  // the settle below would run a chat teardown against "".
  if (p.workflow_id !== undefined && p.workflow_id !== "") {
    return;
  }
  // An APPEND like any other, so the store's `seq` check applies and a close that does not
  // fit asks for the turn's range read rather than settling off a frame in the wrong place.
  appendEntry(chatID, p.entry);
  const close = payloadOf(p.entry, "turn_close");
  const outcome = close?.outcome;
  const settles = !anotherTurnOpen(chatID, p.entry.turn);
  // Read off the close itself: its turn_open may not be resident, and the repair read is async.
  const agentRan = close?.carrier !== true;
  // Every ask this turn raised is over; a workflow run's survives, since it outlives the
  // turn that launched it. Gated with the rest because the sweep keeps only RUN-scoped asks,
  // so on a close leaving another turn open it would strand a live JSON-RPC request.
  if (settles) {
    dropTurnDecisions(chatID);
    // Written at the CALL SITE rather than inside `clearTurnState`, deliberately — that
    // function also runs on `BUS_RECONCILE`, where dropping the server's last liveness
    // statement while `thinking` is also cleared is the gap-path flash `turnLive` removes.
    setTurnOpen(chatID, false);
    clearTurnState(chatID);
  }
  // This chat's turn index changed, so its rail record needs a re-read. Ungated:
  // it is the one effect that reads the server's own authoritative liveness.
  void refreshTurnRail(chatID);
  if (agentRan) {
    clearAgentDown();
  }
  refreshGitBadge();

  // Inside the `settles` branch and AFTER the writes above: the cue is a statement about a
  // turn this handler settled, and `chatSettled` reads the turn state those writes produce.
  if (settles && agentRan) {
    noteAgentFinished(chatID, notifyBodyFor(outcome, get(chatID)?.name ?? "Chat"));
  }
});

// Each ask notifies unconditionally (master switch only): it blocks the turn, so a per-kind mute
// would stall every later turn silently.

onSSE("permission_needed", (chatID, p) => {
  notifyIfHidden(
    NOTIFY_TITLE,
    p.files !== undefined && p.files.length > 0
      ? "Review this turn's changes"
      : "Permission needed",
    askTarget(chatID, p.run_id),
  );
  pushDecision({
    kind: "permission",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (answer) => {
      void respondPermission.dispatch({ chatID, requestID: p.request_id, ...answer });
    },
  });
});

onSSE("elicitation_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "Input requested by a tool", askTarget(chatID, p.run_id));
  pushDecision({
    kind: "elicitation",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, content) => {
      void respondElicitation.dispatch(
        content !== undefined
          ? { chatID, requestID: p.request_id, action, content }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

onSSE("user_input_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "The agent has a question", askTarget(chatID, p.run_id));
  pushDecision({
    kind: "user_input",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, answer) => {
      void respondUserInput.dispatch(
        action === "answered" && answer !== undefined
          ? { chatID, requestID: p.request_id, action, answer }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

// Only the first answer is accepted; this retires the card and its banner everywhere else. The run
// attribution is read back off the dock's record (the frame names the chat); an unknown ask
// retracts the chat's banner. Not `permissions_changed` (the Cedar reload).
onSSE("decision_settled", (chatID, p) => {
  const runID = collapseSettledDecision(chatID, p.kind, p.request_id, p.settled_by);
  void closeNotificationsFor(askTarget(chatID, runID));
});

// --- Data-driven error classification (imported from error-routing.ts) ---

/** Turns a route's declared action into the toast's one action slot. */
function toastActionFor(action: ErrorAction | undefined): ToastRetry | undefined {
  if (action === undefined) {
    return undefined;
  }
  switch (action.kind) {
    case "setting":
      return {
        label: action.label,
        onClick: () => {
          openSetting(action.tab, action.control);
        },
      };
    case "sign-in":
      return { label: action.label, onClick: showLoginModal };
    default:
      action satisfies never;
      return undefined;
  }
}

// This handler touches no turn state: the server closes every turn exactly once
// with a `turn_close` entry, so an error is a report only.
onSSE("error", (chatID, p) => {
  const code = p.code;
  const msg = p.message;

  const route = ERROR_ROUTES[code];
  if (route === undefined) {
    reportFailure(chatID, msg !== "" ? msg : code);
    return;
  }
  switch (route.surface) {
    case "toast":
      // Not reported for the chat on screen when turn-scoped (its card carries the reason);
      // `failure-notice.ts` owns that. `turn_scoped` comes off the FRAME; absent means no.
      reportFailure(chatID, msg, toastActionFor(route.action), p.turn_scoped ?? false);
      break;
    case "agent-down":
      // Active chat only: this DOES paint a shared control, so a background
      // chat's dead bridge must not alert the button of the chat in use.
      if (chatID === getActiveId()) {
        setAgentDown(msg !== "" ? msg : "The agent could not be started for this chat.");
      }
      break;
    default:
      route.surface satisfies never;
  }
});
