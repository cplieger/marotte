// The WORKFLOW door into `exec-view/`, the shared subpage view: wiring only, folding KAS's
// `inspect` through `run-exec-source.ts` into that model, and owning the three things only a
// workflow knows — the status's verbs, what an empty step body means, and which of the run's own
// log turns each step renders.

import { el, effect, touch } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { bindLoadingState } from "./actions/index.js";
import { openRunTab, openTab, parentChatRef, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks, runPendingAsks } from "./decision-dock.js";
import {
  cancelRun,
  extendRunRepeat,
  finishRunRepeat,
  pauseRun,
  resumeRun,
  retryRun,
} from "./actions/runs.js";
import { CONTROL_LABEL, offeredVerbs, refusalSentences, type RunVerb } from "./run-controls.js";
import { get } from "./store.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight, neverRan, settled } from "./exec-view/status.js";
import { flatten, leaves, type ExecNode } from "./exec-view/model.js";
import { runToExec } from "./run-exec-source.js";
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
  runControls,
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
import type { RunControlsResponse } from "./wire/types.gen.js";
import { refreshRunDots, trackRun } from "./run-dots.js";
import { buildPath } from "./route-path.js";
import { iconEl } from "./icon-el.js";
import { ICON_EXTERNAL } from "./icons.js";
import { SharedScroll } from "./view-scroll.js";

/** The two verbs a repeat paused at its iteration cap offers. They address the paused NODE as
 *  well as the run, so they cannot share the one-argument table. */
type RepeatVerb = "extend" | "finish_loop";

/** Largest count one extend may add; the server refuses anything past it. */
const MAX_EXTEND = 1000;

const RUN_ACTION: Record<
  Exclude<RunVerb, RepeatVerb>,
  { readonly name: string; dispatch: (id: string) => Promise<unknown> }
> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

/** The run this view is currently showing, so an SSE invalidation knows whether it is about the
 *  run on screen. Cleared when the view loads a different one; a closed tab simply stops
 *  matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the shown run was launched by an AGENT, from the run's own state (`paint` sets it
 *  from `inspect`'s `parentSessionId`). It decides what the empty-step note says and whether the
 *  door beside it is offered, and nothing else: the control row is the SERVER's answer now, so
 *  no verb is gated on it here. */
let shownRunChatParented = false;

/** The launching chat of a run, or "" for a parentless one. TWO sources: `runChatID` (SSE-fed,
 *  so it answers for a live run), then the run tab's persisted `TabSubject.Parent`, the only
 *  answer left once a finished run's lease is released. A non-empty second answer is written
 *  back so every other reader converges on it. */
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

/** The pending bindings on the control row's buttons, dropped when the row that carries them
 *  goes — either replaced by a new one or discarded with the page. `bindLoadingState`
 *  self-disposes only for an element that was ATTACHED the last time its effect ran, and this
 *  row is built before the caller appends it, so a button replaced before its first pending flip
 *  never reaches that path. */
let controlBindings: (() => void)[] = [];

/** The control row on screen and the affordance it was built from, so a render that did not move
 *  the answer hands the SAME row back. */
let controlRow: HTMLElement | null = null;
let controlSig = "";

/** Drop the row on screen and the bindings its buttons hold. */
function dropControlRow(): void {
  for (const dispose of controlBindings) {
    dispose();
  }
  controlBindings = [];
  controlRow = null;
  controlSig = "";
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
  pageScroll.track();
  // A different run's page is restored by the paint that mounts it.
  if (pageRun === workflowID) {
    pageScroll.restore(workflowID);
  }
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

/** The view's single subscription to the store. Idempotent. */
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

/** `.page-content` is shared by every run tab, so each run keeps its own offset. */
const pageScroll = new SharedScroll(
  () => document.querySelector<HTMLElement>("[id='run-view'] > .page-content"),
  () => (page?.root.isConnected === true ? pageRun : ""),
);

/** The step transcript, LAZILY loaded — ONE stream now, because there is one source. */
let stepStream: RunStepStream | undefined;
let stepStreamLoading = false;

/** The cached "Open the conversation" anchor, per (run, chat). Cached because `detail.ts` guards
 *  its slot on element IDENTITY: `render` runs on every store invalidation, and re-seating a
 *  node BLURS it, so a fresh element per render would drop focus out of the link several times a
 *  minute on a live run. */
let emptyLink: { workflowID: string; chatID: string; el: HTMLElement } | undefined;

/** Build the page into `#run-body`, replacing whatever was there. */
function mountPage(container: HTMLElement, workflowID: string): ExecPageView {
  page?.dispose();
  // The row belonged to the page being replaced, and its host goes with it.
  dropControlRow();
  const built = buildExecPage({
    emptyNote: stepEmptyNote,
    emptyAction: stepEmptyAction,
    controls: (run) => buildRunControls(run.id),
    onShowNode: armStepRead,
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
  // Beside the other per-run caches, and for their reason: a retarget must not carry another run's
  // pick into the page it is about to build.
  if (focusRequest !== undefined && focusRequest.workflowID !== workflowID) {
    focusRequest = undefined;
  }
  container.replaceChildren(built.root);
  return built;
}

/** A `TurnSource` over one run's log, so `turns.ts`'s pure projection reads it unchanged. Built
 *  per call from the array `runTurns` hands back rather than held, because that array IS the
 *  store's order and a cached copy would be a second answer about it. */
function runSource(turns: readonly [string, TurnState][]): TurnSource {
  return { turns: new Map(turns), turn_order: turns.map(([id]) => id) };
}

/** The turns of one node path, in file order. */
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

/** Ask for the shown step's transcript, on the selection MOVING. TWO conditions here, both keyed
 *  on `inspect` reporting the node settled, because that is what `onShowNode` can observe: it
 *  notifies only when the shown `(path, state)` moves, which is the gate that keeps a repaint
 *  off the wire. A turn WITH a `turn_close` and no hole is settled and is never re-read. A HOLE
 *  is armed from the paint side instead (`armShownStepHole`), because it arrives while the step
 *  is `running` and moves neither the path nor the state. */
function armStepRead(node: ExecNode | undefined): void {
  shownNode = node?.transcript === true ? node : undefined;
  if (node?.transcript !== true || shownRun === "" || !settled(node.state)) {
    return;
  }
  const mine = turnsForStep(runTurns(shownRun), node.path);
  if (mine.length === 0) {
    requestStepTranscript(shownRun, node.path);
    return;
  }
  if (mine.some(([, t]) => turnIsLive(t))) {
    rereadStepTranscript(shownRun, node.path);
  }
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
    rereadStepTranscript(workflowID, node.path);
  }
}

/** What a node with a transcript host but nothing in it should say. */
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

/** Paint the view from a store value. `undefined` means the first fetch has not resolved, which
 *  is the ONLY case that shows a loading row: a refetch driven by an invalidation must not blank
 *  a run the reader is looking at, several times a minute on a busy one. */
function paint(
  workflowID: string,
  state: RunState | undefined,
  turns: readonly [string, TurnState][],
): void {
  const container = document.getElementById("run-body");
  if (container === null) {
    return;
  }
  if (state === undefined) {
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
  const view = mounted && page !== undefined ? page : mountPage(container, workflowID);
  // Two inputs on different clocks, the same pair the transcript's card takes: `inspect` says what
  // the nodes are doing, and the dock says which of them is blocked on a person.
  const focus = focusRequest?.workflowID === workflowID ? focusRequest.path : "";
  const run = runToExec(
    workflowID,
    state,
    runPlan(workflowID),
    runPendingAsks(workflowID),
    focus,
    runStepEnds(workflowID),
  );
  view.render(run);
  if (!mounted) {
    pageScroll.restore(workflowID);
  }
  // Spent only once the page could actually honour it — the plan has to CONTAIN the path, since
  // `page.ts` ignores a focus naming an absent node.
  if (focus !== "" && flatten(run.nodes).some((n) => n.path === focus)) {
    focusRequest = undefined;
  }
  projectStepTranscripts(workflowID, run.nodes, turns);
  // After the projection, so the repair is asked over the entries this frame rendered.
  armShownStepHole(workflowID, turns);
}

/** Project this run's step transcripts into the detail pane, from the run's own log. */
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

/** What to paint for every LEAF of this plan the log holds a turn for. */
function leafPaints(
  nodes: readonly ExecNode[],
  turns: readonly [string, TurnState][],
): Map<string, RunStepPaint[]> {
  const src = runSource(turns);
  const out = new Map<string, RunStepPaint[]>();
  for (const node of leaves(nodes)) {
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

/** The run's control row, rendered from the SERVER's answer and rebuilt only when that answer
 *  moved (`controlRow` owns why). */
function buildRunControls(workflowID: string): HTMLElement | null {
  const answer = runControls(workflowID);
  const sig = answer === undefined ? "" : controlSignature(workflowID, answer);
  if (sig === controlSig) {
    return controlRow;
  }
  // The answer moved, so the row on screen is on its way out and its bindings go with it — the
  // caller replaces the host's children with whatever is returned here.
  dropControlRow();
  controlSig = sig;
  controlRow = answer === undefined ? null : renderControls(workflowID, answer);
  return controlRow;
}

/** What the row is a function of, and nothing else: the run it acts on, the verbs it draws and
 *  the sentences it draws instead. The parent chat travels on the same answer and changes
 *  nothing here, so it is left out rather than churning the row. */
function controlSignature(workflowID: string, answer: RunControlsResponse): string {
  // Keyed, not positional: `refused` is a map on the wire, so two answers that differ only in the
  // order the server happened to serialize them are the same row.
  const refused = Object.entries(answer.refused ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([verb, text]) => `${verb}\u0001${text}`)
    .join("\u0002");
  return `${workflowID}\u0000${answer.verbs.join("\u0001")}\u0000${refused}\u0000${answer.pause_node_id ?? ""}`;
}

/** Sends the node the server reported as paused, never one derived here. */
function repeatControl(
  workflowID: string,
  verb: RepeatVerb,
  nodeID: string,
): { nodes: HTMLElement[]; btn: HTMLButtonElement; name: string } {
  if (verb === "finish_loop") {
    const btn = el(
      "button",
      {
        type: "button",
        className: "btn btn-sm",
        onclick: () => void finishRunRepeat.dispatch({ workflowID, nodeID }),
      },
      CONTROL_LABEL[verb],
    ) as HTMLButtonElement;
    return { nodes: [btn], btn, name: finishRunRepeat.name };
  }
  const count = el("input", {
    type: "number",
    className: "tool-form-input run-extend-count",
    min: "1",
    max: String(MAX_EXTEND),
    step: "1",
    value: "1",
    "aria-label": "Iterations to add",
  }) as HTMLInputElement;
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn btn-sm",
      onclick: () => {
        if (!count.reportValidity()) {
          return;
        }
        void extendRunRepeat.dispatch({ workflowID, nodeID, iterations: count.valueAsNumber });
      },
    },
    CONTROL_LABEL[verb],
  ) as HTMLButtonElement;
  return { nodes: [count, btn], btn, name: extendRunRepeat.name };
}

/** The row itself. Split from the decision above so the guard reads as one thing. */
function renderControls(workflowID: string, answer: RunControlsResponse): HTMLElement | null {
  const verbs = offeredVerbs(answer.verbs);
  if (verbs.length === 0) {
    return refusalRow(refusalSentences(answer.refused));
  }
  const row = el("div", { className: "run-controls" });
  for (const verb of verbs) {
    if (verb === "extend" || verb === "finish_loop") {
      const nodeID = answer.pause_node_id ?? "";
      if (nodeID === "") {
        continue;
      }
      const control = repeatControl(workflowID, verb, nodeID);
      controlBindings.push(bindLoadingState(control.name, control.btn));
      row.append(...control.nodes);
      continue;
    }
    const action = RUN_ACTION[verb];
    const btn = el(
      "button",
      {
        type: "button",
        className: verb === "cancel" ? "btn btn-sm btn-danger" : "btn btn-sm",
        onclick: () => {
          // No optimistic state flip. Every one of these verbs settles at a NODE boundary, so the
          // run is still `running` when the reply arrives and a flipped label would be a lie for as
          // long as the node takes. The run_progress invalidation is what repaints this row.
          void action.dispatch(workflowID);
        },
      },
      CONTROL_LABEL[verb],
      // `el` answers HTMLElement; the pending binding needs the `disabled` property, which only the
      // concrete button type declares.
    ) as HTMLButtonElement;
    // A retry starts a process and can legitimately take tens of seconds, so an unbound button
    // looks dead for the whole handshake and can be clicked again meanwhile.
    controlBindings.push(bindLoadingState(action.name, btn));
    row.appendChild(btn);
  }
  return row;
}

/** The row a run with no verbs gets: the server's own sentences, in the place a reader is
 *  already looking for the control. Null when there is nothing to say, so a completed run keeps
 *  its clean header. */
function refusalRow(sentences: readonly string[]): HTMLElement | null {
  if (sentences.length === 0) {
    return null;
  }
  return el(
    "div",
    { className: "run-controls run-controls-refused", role: "note" },
    ...sentences.map((text) => el("p", { className: "run-control-refusal" }, text)),
  );
}
