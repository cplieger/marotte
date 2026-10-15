// The WORKFLOW door into `exec-view/`, the shared subpage view: wiring only, folding KAS's
// `inspect` through `run-exec-source.ts` into that model, and owning the three things only a
// workflow knows — the status's verbs, what an empty step body means, and which of the run's own
// log turns each step renders.

import { el, effect, signal, touch } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { join } from "@cplieger/keyenc";
import { openRunTab, openTab, parentChatRef, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks, runPendingAsks } from "./decision-dock.js";
import { clearRunStepSteers, removeRunStepSteer } from "./actions/runs.js";
import {
  mountRunComposer,
  restoreRunComposer,
  setRunComposerTarget,
  takeIntoRunComposer,
} from "./run-composer.js";
import { DEFINITE_REFUSALS, mountSteerStack, type SteerStackSource } from "./pending-steers.js";
import { stepSteers } from "./run-step-steers.js";
import { confirm } from "./confirm.js";
import { noticeAbout } from "./actions/subject.js";
import {
  createFollowScroller,
  type FollowScroller,
  type ViewScrollState,
} from "./scroll-controller.js";
import { buildRunControlsView, type RunControlsView } from "./run-controls-view.js";
import { get } from "./store.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight, neverRan, settled } from "./exec-view/status.js";
import { flatten, workNodes, type ExecNode, type ExecRun } from "./exec-view/model.js";
import { runToExec } from "./run-exec-source.js";
import type { RunAsks } from "./run-asks.js";
import { lastPrintedLine } from "./run-step-tail.js";
import type { RunStepPaint, RunStepStream } from "./run-chat-steps.js";
import {
  clearStepTranscripts,
  requestStepTranscript,
  rereadStepTranscript,
  stepRead,
  stepTranscriptVersion,
} from "./run-step-transcript.js";
import {
  invalidateRun,
  invalidateRunControls,
  noteRunChat,
  runState,
  runChatID,
  runPlan,
  runStepEnds,
  runTurnHoles,
  runTurns,
  type RunState,
} from "./run-store.js";
import { projectTurn, turnOpenOf, type TurnSource } from "./turns.js";
import type { TurnState } from "./types.js";
import { refreshRunDots, trackRun } from "./run-dots.js";
import { buildPath } from "./route-path.js";
import { iconEl } from "./icon-el.js";
import { ICON_EXTERNAL, ICON_TAB_CHAT } from "./icons.js";

/** The run this view is currently showing, so an SSE invalidation knows whether it is about the
 *  run on screen. Cleared when the view loads a different one; a closed tab simply stops
 *  matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the shown run was launched by an AGENT, from the run's own state (`paint` sets it
 *  from `inspect`'s `parentSessionId`). It decides what the empty-step note says and whether the
 *  door beside it is offered, and nothing else: the control row is the SERVER's answer now, so
 *  no verb is gated on it here. */
let shownRunChatParented = false;

/** TWO sources: `runChatID` (SSE-fed, so it answers for a live run), then the run tab's persisted
 *  `TabSubject.Parent`, the only answer left once a finished run's lease is released. A non-empty
 *  second answer is written back so every other reader converges on it. */
function launchingChatOf(workflowID: string): string {
  const known = runChatID(workflowID);
  if (known !== "") {
    return known;
  }
  const fromTab = parentChatRef(tabIdFor("run", workflowID));
  if (fromTab !== "") {
    noteRunChat(workflowID, fromTab);
  }
  return fromTab;
}

/** Point the run view at one run and mount its dock. */
export function showRun(workflowID: string): void {
  shownRun = workflowID;
  // Until that reply lands the run is treated as PARENTLESS, which is the arm whose sentences
  // describe THIS tab and so cannot mislead about another one. Reset per show, or the previous
  // run's verdict would answer for this one.
  shownRunChatParented = false;
  const dock = document.getElementById("run-dock");
  if (dock !== null) {
    mountRunDecisionDock(dock, () => shownRun);
  }
  rerenderDocks();
  mountBottomBar();
  // Before any read resolves: until this run's page publishes a step, Send and the step dock address
  // nothing rather than the run shown before.
  if (targetRun !== workflowID) {
    runFlags = { live: false, paused: false };
    pushStepTarget(workflowID, undefined);
  }
  // This run's own page still holds the follower (a hidden box keeps its offset); another run's is
  // restored by the paint that mounts it.
  mountFollower();
  // ONE effect for the life of the module, installed on the first show. It reads `shownRun` through
  // the store, so a tab switch re-points it with no teardown: the previous run's cell simply stops
  // being read. Installing one per show would leak a subscription per tab opened.
  installViewEffect();
}

/** Refetch this run's state and its controls. A run tab's `refresh`. */
export function refreshRun(workflowID: string): void {
  invalidateRun(workflowID);
  // The affordance. `invalidateRunControls` owns the trigger list.
  invalidateRunControls(workflowID);
  // NO chat refresh: a step's entries are the RUN's, so a chat's window cannot move this pane. The
  // step transcript's refresh is the step GET, armed by `armStepRead`.
}

/** The view's single subscription to the store. */
let viewEffectInstalled = false;
function installViewEffect(): void {
  if (viewEffectInstalled) {
    return;
  }
  viewEffectInstalled = true;
  effect(() => {
    const id = shownRun;
    // Every entry of this run's log bumps its own log version, which is how a step's content
    // arrives. Read BEFORE the early returns so the effect stays subscribed on the passes that
    // bail; `paint` returns early on an unresolved `inspect`.
    const turns = id === "" ? [] : runTurns(id);
    // A resolved step GET repaints the page. ONE signal for every step: this page shows one node at
    // a time, so a coarse bump costs one repaint. It carries the VERDICT only — the entries that
    // answer adopted arrive on the log version above.
    touch(stepTranscriptVersion);
    if (id === "") {
      return;
    }
    paint(id, runState(id), turns);
  });
}

/** Open a run's tab on request — every MANUAL door: the Workflows tab's Run button, a History
 *  row, the run card's footer link, a `/run/{id}` deep link. */
/** RETURNS the open, so a DEEP LINK can await it — see `subagent-view.ts` `openSubagentView` for
 *  the measurement. Every other caller is a click with nothing to wait for and voids it. */
export function openRunView(
  workflowID: string,
  name: string,
  parentChatID = "",
  focusNode = "",
): Promise<void> {
  // Replaces any earlier request outright: the last door clicked is the one the reader is waiting
  // on, and two pending picks for one run is a state nothing could resolve honestly.
  focusRequest = focusNode === "" ? undefined : { workflowID, path: focusNode };
  const parentChat = parentChatID === "" ? runChatID(workflowID) : parentChatID;
  // Teach the store the pairing this door already knows.
  noteRunChat(workflowID, parentChat);
  // The PARENT is a tab id, not a chat id, so the nesting question and the id it needs are the
  // same lookup.
  const parentTab = parentChat === "" ? "" : tabIdFor("chat", parentChat);
  return openRunTab(
    workflowID,
    name,
    parentTab === "" ? { owns: false } : { parent: parentTab, owns: false },
  );
}

/** The node a door asked for, pending until a paint has honoured it. */
let focusRequest: { workflowID: string; path: string } | undefined;

/** The page, built once and re-pointed. `exec-view/` knows nothing about workflows:
 *  `run-exec-source.ts` folds KAS's reply into its model, so a subagent tab is a second adapter
 *  into this same page rather than a second page. */
let page: ExecPageView | undefined;
let pageRun = "";
/** The shown run's verbs, built with its page and disposed with it. */
let controls: RunControlsView | undefined;

let follower: FollowScroller | undefined;

/** Where each run's page stood when another run took the one scroller. */
const parkedScroll = new Map<string, ViewScrollState>();

function parkPage(): void {
  if (follower !== undefined && page !== undefined && pageRun !== "") {
    parkedScroll.set(pageRun, follower.detach());
  }
}

function attachFollower(workflowID: string): void {
  const body = document.getElementById("run-body");
  if (follower === undefined || body === null) {
    return;
  }
  follower.attach({
    el: body,
    ...(parkedScroll.get(workflowID) ?? { scrollTop: 0, readingState: "following" }),
  });
}

let runFlags = { live: false, paused: false };

/** The run both step addresses currently name. */
let targetRun = "";

const stackTarget = signal<{ workflowID: string; nodePath: string }>({
  workflowID: "",
  nodePath: "",
});

let barMounted = false;
function mountBottomBar(): void {
  if (barMounted) {
    return;
  }
  const form = document.getElementById("run-composer");
  const input = document.getElementById("run-composer-input");
  const send = document.getElementById("run-send-btn");
  const stack = document.getElementById("run-steer-stack");
  if (
    !(form instanceof HTMLFormElement) ||
    !(input instanceof HTMLTextAreaElement) ||
    !(send instanceof HTMLButtonElement) ||
    !(stack instanceof HTMLUListElement)
  ) {
    return;
  }
  barMounted = true;
  mountRunComposer(form, input, send);
  mountSteerStack(stack, stepStackSource);
}

function mountFollower(): void {
  if (follower !== undefined) {
    return;
  }
  const scroller = document.querySelector<HTMLElement>("[id='run-view'] > .page-content");
  const body = document.getElementById("run-body");
  const resume = document.getElementById("run-scroll-bottom");
  if (scroller === null || body === null || resume === null) {
    return;
  }
  follower = createFollowScroller(body, scroller, resume);
}

/** The one writer of both step addresses, the composer's and the step dock's, so they cannot name
 *  different runs. */
function pushStepTarget(workflowID: string, selected: ExecNode | undefined): void {
  const step = selected?.children.length === 0 ? selected : undefined;
  targetRun = workflowID;
  setRunComposerTarget({
    workflowID,
    runLive: runFlags.live,
    runPaused: runFlags.paused,
    step,
  });
  const next = { workflowID: step === undefined ? "" : workflowID, nodePath: step?.path ?? "" };
  const cur = stackTarget.peek();
  if (cur.workflowID !== next.workflowID || cur.nodePath !== next.nodePath) {
    stackTarget.value = next;
  }
}

/** The run step's dock: the selected step's unread rows and the chat's Edit and Delete, addressed to
 *  the step. No Send now: nothing stops a step's turn for a reader. */
const stepStackSource: SteerStackSource = {
  read: () => {
    const t = stackTarget.value;
    return {
      id: t.workflowID === "" ? "" : join(t.workflowID, t.nodePath),
      steers: t.workflowID === "" ? [] : stepSteers(t.workflowID, t.nodePath),
      queued: [],
    };
  },
  edit: async (steer) => {
    const t = stackTarget.peek();
    const edit = takeIntoRunComposer(steer.text);
    if (edit === undefined) {
      stepRefused(t.workflowID, STILL_SENDING);
      return;
    }
    announce("Message taken back for editing");
    const outcome = await removeRunStepSteer.dispatch({
      workflowID: t.workflowID,
      nodePath: t.nodePath,
      steer_id: steer.id,
      edit: true,
    }).outcome;
    if (outcome.status !== "error") {
      return;
    }
    if (DEFINITE_REFUSALS.has(outcome.error.status ?? 0)) {
      restoreRunComposer(edit);
      stepRefused(t.workflowID, outcome.error.message);
      return;
    }
    stepRefused(t.workflowID, "Could not confirm the message was taken back");
  },
  editByClear: async (steer) => {
    const t = stackTarget.peek();
    if (takeIntoRunComposer(steer.text) === undefined) {
      stepRefused(t.workflowID, STILL_SENDING);
      return;
    }
    announce("Message taken back for editing");
    await clearRunStepSteers.dispatch({ ...t, edit: steer.id }).outcome;
  },
  remove: async (steerID) => {
    const t = stackTarget.peek();
    announce("Deleting the message");
    const outcome = await removeRunStepSteer.dispatch({
      workflowID: t.workflowID,
      nodePath: t.nodePath,
      steer_id: steerID,
    }).outcome;
    if (outcome.status === "error") {
      stepRefused(
        t.workflowID,
        DEFINITE_REFUSALS.has(outcome.error.status ?? 0)
          ? outcome.error.message
          : "Could not delete the message",
      );
    }
  },
  discard: async (waiting) => {
    const t = stackTarget.peek();
    if (waiting > 1) {
      const ok = await confirm(
        `Discard all ${String(waiting)} messages the step has not read yet? A workflow message is waiting beside them, so they clear together.`,
        "Discard all",
        "destructive",
      );
      if (!ok) {
        return;
      }
    }
    announce("Discarding messages the step has not read");
    await clearRunStepSteers.dispatch(t).outcome;
  },
  waitingFor: "the step",
};

const STILL_SENDING =
  "Your last message to this step is still sending. Try Edit again once it has gone.";

function stepRefused(workflowID: string, message: string): void {
  announce(message);
  noticeAbout(`run:${workflowID}`, message, "error");
}

/** The step transcript, LAZILY loaded — ONE stream now, because there is one source. */
let stepStream: RunStepStream | undefined;
let stepStreamLoading = false;

/** The cached "Open the conversation" anchor, per (run, chat). Cached because `detail.ts` guards
 *  its slot on element IDENTITY: `render` runs on every store invalidation, and re-seating a
 *  node BLURS it, so a fresh element per render would drop focus out of the link several times a
 *  minute on a live run. */
let emptyLink: { workflowID: string; chatID: string; el: HTMLElement } | undefined;

/** Build the page into `#run-body` and its dock into `dockHost`, replacing whatever was there. */
function mountPage(
  container: HTMLElement,
  dockHost: HTMLElement,
  workflowID: string,
): ExecPageView {
  parkPage();
  dropPage();
  const verbs = buildRunControlsView(workflowID);
  controls = verbs;
  const built = buildExecPage({
    emptyNote: stepEmptyNote,
    emptyAction: stepEmptyAction,
    dock: dockHost,
    controls: () => verbs.current(),
    notes: () => verbs.refusals(),
    alertAction: answerAction,
    onShowNode: armStepRead,
    // A page still drawn while another run loads publishes nothing.
    onSelect: (node) => {
      if (workflowID === shownRun) {
        pushStepTarget(workflowID, node);
      }
    },
    follow: "newest",
  });
  page = built;
  pageRun = workflowID;
  stepStream?.dispose();
  stepStream = undefined;
  stepStreamLoading = false;
  // The on-demand reads go with the page, which is this cache's whole bound: a run tab retargeting
  // or closing is the one moment a step's answer stops being wanted, and there is no other moment
  // it becomes wrong.
  clearStepTranscripts();
  emptyLink = undefined;
  // Beside them, and for their reason: a stale selection would intersect this run's holes with
  // another run's path, and a stale answer would swallow the first hole here.
  shownNode = undefined;
  holesAnswered = "";
  cancelStepRetry();
  cancelDockCatchUps();
  catchUpsOwed.clear();
  // Beside the other per-run caches, and for their reason: a retarget must not carry another run's
  // pick into the page it is about to build.
  if (focusRequest !== undefined && focusRequest.workflowID !== workflowID) {
    focusRequest = undefined;
  }
  container.replaceChildren(built.root);
  return built;
}

/** Release the page, its dock and its verbs. */
function dropPage(): void {
  page?.dispose();
  page = undefined;
  pageRun = "";
  controls?.dispose();
  controls = undefined;
}

/** A `TurnSource` over one run's log, so `turns.ts`'s pure projection reads it unchanged. Built
 *  per call from the array `runTurns` hands back rather than held, because that array IS the
 *  store's order and a cached copy would be a second answer about it. */
function runSource(turns: readonly [string, TurnState][]): TurnSource {
  return { turns: new Map(turns), turn_order: turns.map(([id]) => id) };
}

function turnsForStep(
  turns: readonly [string, TurnState][],
  nodePath: string,
): [string, TurnState][] {
  return turns.filter(([, t]) => turnOpenOf(t)?.node_path === nodePath);
}

/** Whether a step turn may still grow: its OWN `turn_close` is absent. Never the run's status,
 *  and that is the whole liveness rule now. */
function turnIsLive(state: TurnState): boolean {
  return state.closeAt === undefined;
}

/** The shown node (`undefined` for a container, which hosts nothing), and the holed turns
 *  already answered for it — keyed on the path plus those turn ids, so a hole arriving after a
 *  repair is a new question and a repaint is not a retry loop (`rereadStepTranscript` drops a
 *  settled verdict, so it must not run per frame). */
let shownNode: ExecNode | undefined;
let holesAnswered = "";

/** Ask for the shown step's transcript, on the selection MOVING, which is all `onShowNode` reports:
 *  it notifies only when the shown `(path, state)` moves, the gate that keeps a repaint off the
 *  wire. A turn WITH a `turn_close` and no hole is settled and is never re-read, unless the step's
 *  catch-up is still owed (`catchUpsOwed`). A HOLE is armed
 *  from the paint side instead (`armShownStepHole`), because it arrives while the step is `running`
 *  and moves neither the path nor the state. */
function armStepRead(node: ExecNode | undefined): void {
  shownNode = node?.transcript === true ? node : undefined;
  if (node?.transcript !== true || shownRun === "") {
    return;
  }
  switch (stepReadVerb(shownRun, node)) {
    case "request":
      catchUpsOwed.add(join(shownRun, node.path));
      requestStepTranscript(shownRun, node.path);
      return;
    case "reread":
      rereadStepTranscript(shownRun, node.path);
      return;
    case undefined:
      return;
  }
}

/** The steps whose catch-up read this page started, by run and path, until a read settles them
 *  (`settleCatchUps`). Held apart from the log because output streamed after the tab opened says
 *  nothing about what the step printed before it: a step can open a new turn while an older one
 *  is still unread. */
const catchUpsOwed = new Set<string>();

/** Retire the catch-ups a read answered for good. Run at the top of every paint, which every
 *  verdict triggers, so a later hole's re-read cannot drop a settled verdict before it is seen. */
function settleCatchUps(workflowID: string, nodes: readonly ExecNode[]): void {
  for (const node of nodes) {
    const key = join(workflowID, node.path);
    if (!catchUpsOwed.has(key)) {
      continue;
    }
    const state = stepRead(workflowID, node.path)?.state;
    if (state === "ready" || state === "gone" || state === "unaddressable") {
      catchUpsOwed.delete(key);
    }
  }
}

/** Which read the pane is missing for a step, or undefined when what it holds is enough. */
function stepReadVerb(workflowID: string, node: ExecNode): "request" | "reread" | undefined {
  if (catchUpsOwed.has(join(workflowID, node.path))) {
    return "request";
  }
  const mine = turnsForStep(runTurns(workflowID), node.path);
  if (!settled(node.state)) {
    // Live frames carry only what is appended from now on, and a missed entry is noticed at the
    // next one, which a stalled step (a model throttled for minutes) never sends: a running step
    // this page holds nothing of is read once, so what it already printed shows.
    return inFlight(node.state) && mine.length === 0 ? "request" : undefined;
  }
  if (mine.length === 0) {
    return "request";
  }
  return mine.some(([, t]) => turnIsLive(t)) ? "reread" : undefined;
}

const STEP_RETRY_LIMIT = 3;
const STEP_RETRY_BASE_MS = 1000;

/** The shown step's retry ladder after a transient miss. Keyed on run and path so a new selection
 *  starts its own three. */
let stepRetry: { key: string; attempts: number; timer: ReturnType<typeof setTimeout> | undefined } =
  { key: "", attempts: 0, timer: undefined };

function cancelStepRetry(): void {
  if (stepRetry.timer !== undefined) {
    clearTimeout(stepRetry.timer);
  }
  stepRetry = { key: "", attempts: 0, timer: undefined };
}

/** Re-ask, on a backoff, for the shown step whose last read was `unavailable` while the pane still
 *  lacks it. From the paint side because the verdict moves neither the shown path nor its state, so
 *  `onShowNode` never fires again for it. */
function armShownStepRetry(workflowID: string): void {
  const node = shownNode;
  const key = node === undefined ? "" : join(workflowID, node.path);
  if (key !== stepRetry.key) {
    cancelStepRetry();
    stepRetry.key = key;
  }
  if (node === undefined) {
    return;
  }
  const verdict = stepRead(workflowID, node.path)?.state;
  if (verdict !== "unavailable") {
    if (verdict !== "loading") {
      // An answer, or no read: a later miss on this step is a new question.
      stepRetry.attempts = 0;
    }
    return;
  }
  if (!stepStillMissing(node)) {
    // The stale verdict outlives what it was about, so the budget is restored here: a later hole
    // on this step must still get its three.
    cancelStepRetry();
    stepRetry.key = key;
    return;
  }
  if (stepRetry.timer !== undefined || stepRetry.attempts >= STEP_RETRY_LIMIT) {
    return;
  }
  const delay = STEP_RETRY_BASE_MS * 2 ** stepRetry.attempts;
  stepRetry.timer = setTimeout(() => {
    stepRetry.timer = undefined;
    const shown = shownNode;
    if (shown === undefined || join(shownRun, shown.path) !== key || !stepStillMissing(shown)) {
      return;
    }
    stepRetry.attempts++;
    requestStepTranscript(shownRun, shown.path);
  }, delay);
}

/** The run dock's catch-up reads, per in-flight step path. The shown step's read and ladder are
 *  `armStepRead`'s and `armShownStepRetry`'s, so this owns only the siblings the dock rows summarise. */
const dockCatchUps = new Map<
  string,
  { attempts: number; timer: ReturnType<typeof setTimeout> | undefined }
>();

function cancelDockCatchUps(): void {
  for (const c of dockCatchUps.values()) {
    if (c.timer !== undefined) {
      clearTimeout(c.timer);
    }
  }
  dockCatchUps.clear();
}

/** Read, once per page, each in-flight step the dock summarises that the log holds nothing of, then
 *  re-ask on the shown step's bounded ladder until a read settles it, live output or not. Live
 *  frames carry only what is appended from now on, so without it a sibling's row stays blank until
 *  the step prints again. */
function catchUpDockSteps(
  workflowID: string,
  nodes: readonly ExecNode[],
  turns: readonly [string, TurnState][],
): void {
  const wanted = new Set<string>();
  for (const node of nodes) {
    const path = node.path;
    const owed = catchUpsOwed.has(join(workflowID, path));
    if (
      node.transcript !== true ||
      !inFlight(node.state) ||
      path === shownNode?.path ||
      (!owed && turnsForStep(turns, path).length > 0)
    ) {
      continue;
    }
    wanted.add(path);
    const c = dockCatchUps.get(path);
    if (c === undefined) {
      dockCatchUps.set(path, { attempts: 0, timer: undefined });
      catchUpsOwed.add(join(workflowID, path));
      requestStepTranscript(workflowID, path);
      continue;
    }
    if (stepRead(workflowID, path)?.state !== "unavailable") {
      // Another read answered or is out, so a rung armed by the last miss would only repeat it.
      if (c.timer !== undefined) {
        clearTimeout(c.timer);
        c.timer = undefined;
      }
      continue;
    }
    if (c.timer !== undefined || c.attempts >= STEP_RETRY_LIMIT) {
      continue;
    }
    c.timer = setTimeout(
      () => {
        c.timer = undefined;
        // Re-pointing the tab at another run repaints nothing until that run's state lands.
        if (shownRun !== workflowID) {
          return;
        }
        c.attempts++;
        requestStepTranscript(workflowID, path);
      },
      STEP_RETRY_BASE_MS * 2 ** c.attempts,
    );
  }
  // A step whose catch-up settled, or that settled or became the shown one, needs no rung of this
  // ladder.
  for (const [path, c] of dockCatchUps) {
    if (!wanted.has(path) && c.timer !== undefined) {
      clearTimeout(c.timer);
      c.timer = undefined;
    }
  }
}

/** Whether the pane still lacks what a read would bring: a gate of `stepReadVerb`, or a holed turn. */
function stepStillMissing(node: ExecNode): boolean {
  if (stepReadVerb(shownRun, node) !== undefined) {
    return true;
  }
  const holes = new Set(runTurnHoles(shownRun));
  return turnsForStep(runTurns(shownRun), node.path).some(([id]) => holes.has(id));
}

/** Re-ask for the shown step whose turn the store marked a HOLE, once per hole. The arm the
 *  selection cannot carry: a hole arrives while the step is `running`, so the shown `(path,
 *  state)` never moves and `onShowNode` never fires again — while `appendRunEntry` rejects every
 *  later entry of that turn, so the reader watches a FROZEN live step until it settles or they
 *  click away and back. */
function armShownStepHole(workflowID: string, turns: readonly [string, TurnState][]): void {
  const node = shownNode;
  if (node === undefined) {
    return;
  }
  const holes = new Set(runTurnHoles(workflowID));
  const holed = turnsForStep(turns, node.path)
    .map(([id]) => id)
    .filter((id) => holes.has(id))
    .sort();
  const answered = holed.length === 0 ? "" : join(node.path, ...holed);
  if (answered === holesAnswered) {
    return;
  }
  holesAnswered = answered;
  if (holed.length > 0) {
    // A new hole is a new question, so its repair gets the ladder's full budget.
    cancelStepRetry();
    rereadStepTranscript(workflowID, node.path);
  }
}

function stepEmptyNote(node: ExecNode): string {
  // Ahead of everything else, because a step with no execution behind it is the one case here that
  // no read can change. `.ev-d-state` two rows above already reads "not started" or "skipped";
  // these say what that means for the blank region.
  if (neverRan(node.state)) {
    return node.state === "skipped"
      ? "This step was skipped, so it produced no output."
      : "This step has not started, so there is nothing to show yet.";
  }
  // IN FLIGHT is answered here IN FULL, so the verdict arms below are reached only for a settled
  // step. ONE answer, and the collapse is the point: a live step's entries arrive on the run's own
  // log, for both run populations and on every device.
  if (inFlight(node.state)) {
    return "Waiting for this step to produce output\u2026";
  }
  switch (stepRead(shownRun, node.path)?.state) {
    case "loading":
      return "Loading this step's transcript\u2026";
    case "ready":
      // Reached only with the log holding NO entry for this step's turn after a read the server
      // answered, which is its own fact: the step ran and wrote nothing.
      return "This step ran without producing a transcript.";
    case "gone":
      return "This step's transcript is no longer stored. What the step CAPTURED is above, when it declared captureOutput.";
    case "unavailable":
      return "This step's transcript could not be read right now.";
    case "unaddressable":
      // The one arm that is not the endpoint's own verdict: the server refused the ADDRESS with a
      // 4xx, so unlike the line above it this must not read as transient — `settled()` never
      // re-asks, and offering a retry that fails identically is the affordance this state exists to
      // remove.
      return "This step's transcript cannot be read: the run's plan does not name it.";
    default:
      // A SETTLED step with no read recorded, which is the instant before its request exists rather
      // than a state the pane sits in: `repaint` fires `onShowNode` — and so `armStepRead` — AFTER
      // `detail.render` has built this note, and again whenever the shown node's state moves, so a
      // read is in flight or one instruction away.
      return "Loading this step's transcript\u2026";
  }
}

/** The affordance beside that note: a door into the conversation that LAUNCHED this run. So it
 *  is offered for a step that NEVER RAN as well. */
function stepEmptyAction(node: ExecNode): HTMLElement | null {
  if (node.transcript !== true || !shownRunChatParented) {
    return null;
  }
  const workflowID = shownRun;
  const chatID = launchingChatOf(workflowID);
  if (chatID === "") {
    return null;
  }
  if (emptyLink?.workflowID === workflowID && emptyLink.chatID === chatID) {
    return emptyLink.el;
  }
  const link = el(
    "a",
    { className: "ev-d-link", href: buildPath({ kind: "chat", id: chatID }) },
    "Open the conversation",
    el("span", { className: "ev-d-link-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  );
  link.addEventListener("click", (e) => {
    // A modified click is a deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    void openTab({
      kind: "chat",
      ref: chatID,
      name: get(chatID)?.name ?? "Chat",
    }).then(() => {
      // The reveal is the REFINEMENT and the open is the affordance, which is why it is best-effort
      // and lazily imported: `messages.ts` is the transcript stack, and a run tab must not pull it
      // in to draw a link.
      void import("./messages.js")
        .then((mm) => mm.revealRunCard(chatID, workflowID))
        .catch(() => {
          /* the tab is open, which is what the link promised */
        });
    });
  });
  emptyLink = { workflowID, chatID, el: link };
  return link;
}

/** `undefined` means the first fetch has not resolved, which is the ONLY case that shows a loading
 *  row: a refetch driven by an invalidation must not blank a run the reader is looking at, several
 *  times a minute on a busy one. */
function paint(
  workflowID: string,
  state: RunState | undefined,
  turns: readonly [string, TurnState][],
): void {
  const container = document.getElementById("run-body");
  const dockHost = document.getElementById("run-exec-dock");
  if (container === null || dockHost === null) {
    return;
  }
  if (state === undefined) {
    if (pageRun !== workflowID && page !== undefined) {
      // Another run's page and its dock must not stand in for this run while it loads.
      parkPage();
      dropPage();
      container.replaceChildren();
    }
    if (container.childElementCount === 0) {
      container.replaceChildren(el("div", { className: "list-empty" }, "Loading run\u2026"));
    }
    return;
  }
  // Nudge the tab dot.
  trackRun(workflowID);
  refreshRunDots();

  // Chat-parentedness comes from the RUN, not from client memory or from the door.
  const launchingChat = launchingChatOf(workflowID);
  shownRunChatParented = (state.parentSessionId ?? "") !== "" || launchingChat !== "";

  // Reused only while the page this module built is STILL MOUNTED in this container.
  const mounted = pageRun === workflowID && page?.root.parentElement === container;
  const view = mounted && page !== undefined ? page : mountPage(container, dockHost, workflowID);
  // Two inputs on different clocks, the same pair the transcript's card takes: `inspect` says what
  // the nodes are doing, and the dock says which of them is blocked on a person.
  const focus = focusRequest?.workflowID === workflowID ? focusRequest.path : "";
  shownAsks = runPendingAsks(workflowID);
  const run = runToExec(state, runPlan(workflowID), shownAsks, focus, runStepEnds(workflowID));
  for (const node of workNodes(run.nodes)) {
    const tail = lastPrintedLine(turnsForStep(turns, node.path).map(([, t]) => t));
    if (tail !== "") {
      node.tail = tail;
    }
  }
  // Before the render, whose repaint publishes the selection with them.
  runFlags = { live: run.live, paused: state.status === "paused" };
  // Before the render too, whose `onShowNode` arms reads from what is still owed.
  settleCatchUps(workflowID, workNodes(run.nodes));
  view.render(run);
  if (!mounted) {
    // After the first render, or a parked position lands on an empty page and clamps. The replace
    // also took the follower's edge marker; attaching seats a new one.
    attachFollower(workflowID);
  }
  // Spent only once the page could actually honour it — the plan has to CONTAIN the path, since
  // `page.ts` ignores a focus naming an absent node.
  if (focus !== "" && flatten(run.nodes).some((n) => n.path === focus)) {
    focusRequest = undefined;
  }
  projectStepTranscripts(workflowID, run.nodes, turns);
  // After the projection, so the repair is asked over the entries this frame rendered.
  armShownStepHole(workflowID, turns);
  armShownStepRetry(workflowID);
  // After the render, whose `onShowNode` names the shown step this leaves to its own read.
  catchUpDockSteps(workflowID, workNodes(run.nodes), turns);
}

function projectStepTranscripts(
  workflowID: string,
  nodes: readonly ExecNode[],
  turns: readonly [string, TurnState][],
): void {
  const paints = leafPaints(nodes, turns);
  if (paints.size === 0) {
    return;
  }
  if (stepStream !== undefined) {
    stepStream.apply(paints);
    return;
  }
  if (stepStreamLoading) {
    return;
  }
  stepStreamLoading = true;
  const forRun = pageRun;
  void import("./run-chat-steps.js")
    .then(({ createRunStepStream }) => {
      // The tab may have retargeted during the load, in which case this stream would write into the
      // previous page's detached hosts. `page` rather than a captured view for the same reason: the
      // current page is the mounted one.
      if (pageRun !== forRun || page === undefined) {
        return;
      }
      const host = page;
      const stream = createRunStepStream((nodePath) => host.bodyFor(nodePath));
      stepStream = stream;
      // RE-PROJECTED rather than replayed: the load is async, so the log may have grown past what
      // was captured and the stale map would leave the tail to the next repaint. Outside the view
      // effect, so `runTurns`' own touch registers nothing.
      stream.apply(leafPaints(nodes, runTurns(workflowID)));
    })
    .catch(() => {
      // The step transcript is an enhancement over a page already rendering the plan,
      // the timings and the outputs, so a failed chunk load leaves a usable tab.
    })
    .finally(() => {
      stepStreamLoading = false;
    });
}

function leafPaints(
  nodes: readonly ExecNode[],
  turns: readonly [string, TurnState][],
): Map<string, RunStepPaint[]> {
  const src = runSource(turns);
  const out = new Map<string, RunStepPaint[]>();
  for (const node of workNodes(nodes)) {
    const painted: RunStepPaint[] = [];
    for (const [id, state] of turnsForStep(turns, node.path)) {
      const turn = projectTurn(src, id);
      if (turn === undefined) {
        continue;
      }
      painted.push({ turn, live: turnIsLive(state) });
    }
    if (painted.length > 0) {
      out.set(node.path, painted);
    }
  }
  return out;
}

/** What the shown run waits on a person for, as its last paint read it. */
let shownAsks: RunAsks = { count: 0, asked: [], label: "" };

/** The dock's Answer for a run waiting on an ask. One element for the module's life, so a repaint
 *  seats the same node and focus inside it survives. */
let answerBtn: HTMLButtonElement | undefined;

function answerButton(): HTMLButtonElement {
  answerBtn ??= el(
    "button",
    {
      type: "button",
      className: "icon-btn run-icon-btn",
      "aria-label": "Answer",
      "data-tooltip": "Answer the step waiting for you",
      onclick: answerWaitingAsk,
    },
    iconEl(ICON_TAB_CHAT),
    el("span", { className: "run-control-label" }, "Answer"),
  ) as HTMLButtonElement;
  return answerBtn;
}

/** Where the dock's Answer takes the reader: a step whose question the server would take an answer
 *  for, answered in the run composer; else a request decision, answered in the decision dock
 *  (`mountRunDecisionDock`, which leaves step questions to the composer). Undefined while neither
 *  can be answered yet, and then no Answer is offered. */
function answerTarget(run: ExecRun): { step: ExecNode } | { decision: true } | undefined {
  const step = workNodes(run.nodes).find((n) => n.verb === "answer");
  if (step !== undefined) {
    return { step };
  }
  return shownAsks.asked.some((a) => !a.answer) ? { decision: true } : undefined;
}

/** The run the dock's Answer acts on, as the last paint offered it. */
let answerRun: ExecRun | undefined;

function answerAction(run: ExecRun): HTMLElement | null {
  answerRun = run;
  return run.alert?.kind === "input" && answerTarget(run) !== undefined ? answerButton() : null;
}

function answerWaitingAsk(): void {
  const target = answerRun === undefined ? undefined : answerTarget(answerRun);
  if (target === undefined) {
    return;
  }
  if ("step" in target) {
    page?.select(target.step.path);
    document.getElementById("run-composer-input")?.focus();
    return;
  }
  const fields = [
    ...(document
      .getElementById("run-dock")
      ?.querySelectorAll<HTMLElement>("textarea:enabled, input:enabled, button:enabled") ?? []),
  ].filter((n) => n.checkVisibility());
  const typed = (n: HTMLElement): boolean =>
    n instanceof HTMLTextAreaElement ||
    (n instanceof HTMLInputElement && n.type !== "checkbox" && n.type !== "radio");
  const field = fields.find(typed) ?? fields.find((n) => n instanceof HTMLButtonElement);
  field?.scrollIntoView({ block: "nearest" });
  field?.focus();
}
