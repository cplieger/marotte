// The WORKFLOW door into `exec-view/`, the shared subpage view: wiring only, folding
// KAS's `inspect` through `run-exec-source.ts` into that model, and owning the three
// things only a workflow knows — the status's verbs, what an empty step body means, and
// which of the run's own log turns each step renders. It is also the ONLY surface
// hosting a step transcript. Closing a run tab stops nothing (`owns: false` at every
// door, no `onClose`); stopping a run is the Cancel VERB.

import { el, effect, touch } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { bindLoadingState } from "./actions/index.js";
import { openRunTab, openTab, parentChatRef, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks, runPendingAsks } from "./decision-dock.js";
import { cancelRun, pauseRun, resumeRun, retryRun } from "./actions/runs.js";
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

/** Verb → its action. Separate from run-controls.ts on purpose: that module is
 *  the pure RULE and must stay importable without the actions framework; this is
 *  the wiring. */
const RUN_ACTION: Record<
  RunVerb,
  { readonly name: string; dispatch: (id: string) => Promise<unknown> }
> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

/** The run this view is currently showing, so an SSE invalidation knows whether
 *  it is about the run on screen. Cleared when the view loads a different one;
 *  a closed tab simply stops matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the shown run was launched by an AGENT, from the run's own state
 *  (`paint` sets it from `inspect`'s `parentSessionId`). It decides what the empty-step
 *  note says and whether the door beside it is offered, and nothing else: the control
 *  row is the SERVER's answer now, so no verb is gated on it here. */
let shownRunChatParented = false;

/** The launching chat of a run, or "" for a parentless one. TWO sources: `runChatID`
 *  (SSE-fed, so it answers for a live run), then the run tab's persisted
 *  `TabSubject.Parent`, the only answer left once a finished run's lease is released. A
 *  non-empty second answer is written back so every other reader converges on it. */
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

/** The pending bindings on the control row's buttons, dropped when the row that
 *  carries them goes — either replaced by a new one or discarded with the page.
 *
 *  `bindLoadingState` self-disposes only for an element that was ATTACHED the last
 *  time its effect ran, and this row is built before the caller appends it, so a
 *  button replaced before its first pending flip never reaches that path. Nothing
 *  else would drop it. */
let controlBindings: (() => void)[] = [];

/** The control row on screen and the affordance it was built from, so a render that
 *  did not move the answer hands the SAME row back.
 *
 *  The row is a function of that answer and the pending signals its buttons carry,
 *  and neither moves at the rate this is called: it is built inside the exec page's
 *  one render pass, which runs per `run_progress` frame. Rebuilding there threw away
 *  a live button several times a minute on a busy run. `""` is the no-answer
 *  signature; a real one always carries its separators. */
let controlRow: HTMLElement | null = null;
let controlSig = "";

/** Drop the row on screen and the bindings its buttons hold.
 *
 *  Both callers own a moment the row stops being current: `buildRunControls` when the
 *  answer moved, `mountPage` when the whole page it lived in is disposed. Without the
 *  second, the last row's disposers were held until some later build — bounded to one
 *  row, and still a live effect following an action for a button nothing can see. */
function dropControlRow(): void {
  for (const dispose of controlBindings) {
    dispose();
  }
  controlBindings = [];
  controlRow = null;
  controlSig = "";
}

/** Point the run view at one run and mount its dock.
 *
 *  Exported for the tab factory (tab-materialize.ts): this is a run tab's `onShow`,
 *  and the factory has to name it without importing the openers above, which build
 *  tabs.
 *
 *  ONE argument now. It used to take `parentless`, and the composition root answered
 *  it from the run store's record of which chat launched the run — a map written only
 *  by SSE frames, so every client that had reloaded answered `true` for a
 *  chat-parented run. Parentage is a durable property of the RUN, so `paint` reads it
 *  off `inspect`'s own `parentSessionId` when the first reply lands and no door has to
 *  guess it. */
export function showRun(workflowID: string): void {
  shownRun = workflowID;
  // Until that reply lands the run is treated as PARENTLESS, which is the arm whose
  // sentences describe THIS tab and so cannot mislead about another one. Reset per
  // show, or the previous run's verdict would answer for this one.
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
  // ONE effect for the life of the module, installed on the first show. It reads
  // `shownRun` through the store, so a tab switch re-points it with no teardown:
  // the previous run's cell simply stops being read. Installing one per show would
  // leak a subscription per tab opened.
  installViewEffect();
}

/** Refetch this run's state and its controls. A run tab's `refresh`. */
export function refreshRun(workflowID: string): void {
  invalidateRun(workflowID);
  // The affordance. `invalidateRunControls` owns the trigger list.
  invalidateRunControls(workflowID);
  // NO chat refresh: a step's entries are the RUN's, so a chat's window cannot move
  // this pane. The step transcript's refresh is the step GET, armed by `armStepRead`.
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
    // Every entry of this run's log bumps its own log version, which is how a step's
    // content arrives. Read BEFORE the early returns so the effect stays subscribed on
    // the passes that bail; `paint` returns early on an unresolved `inspect`. The RUN's
    // log rather than the launching chat's version: a chat subscription would repaint
    // for conversation traffic and miss every step frame.
    const turns = id === "" ? [] : runTurns(id);
    // A resolved step GET repaints the page. ONE signal for every step: this page
    // shows one node at a time, so a coarse bump costs one repaint. It carries the
    // VERDICT only — the entries that answer adopted arrive on the log version above.
    touch(stepTranscriptVersion);
    if (id === "") {
      return;
    }
    paint(id, runState(id), turns);
  });
}

/** Open a run's tab on request — every MANUAL door: the Workflows tab's Run button,
 *  a History row, the run card's footer link, a `/run/{id}` deep link.
 *
 *  NOT read-only, and that reversed with the close contract. History used to open a
 *  review that carried no verbs, on the reasoning that reaching a live run's controls
 *  was the launching tab's job and its × was the stop. With the × disarmed
 *  (tab-materialize.ts) that leaves a live run readable from History with no way to
 *  stop it, so `buildRunControls` gates on the RUN rather than on the door, and every
 *  door offers the same verbs for the same run. `GET /api/sessions` does not filter by
 *  status, so History genuinely does list running and paused runs.
 *
 *  PARENTLESSNESS is not an argument for the DOOR either, and it is now only one
 *  verb's argument at all: a chat-parented run offers pause, resume and cancel
 *  (`hostBridge` resolves the launching chat's bridge) and withholds only retry.
 *  `showRun`'s second parameter asks whether the RUN has a parent agent session,
 *  which is the run's own fact, and the composition root answers it from the run
 *  store when it wires the factory's opener — so a History row and a restored tab
 *  agree instead of each door deciding.
 *
 *  `parentChatID` is a HINT, not the authority: the coordinator fills a run tab's
 *  parent from the run's own lease, so a `/run/{id}` deep link on a browser holding
 *  no state still nests under its conversation. Passing it only saves that lookup,
 *  and a client-supplied parent WINS, which is what lets History name the parent of
 *  a FINISHED run whose lease has been released (`run.parent_chat_id`; nothing new
 *  is fetched for it). Every other door passes the chat it already holds, and
 *  `runChatID` covers the caller that genuinely has none.
 *
 *  It absorbed `openLiveRunView`, which differed only in `owns: true`. With the ×
 *  disarmed there was nothing left to distinguish, and a launcher-opened run is
 *  parentless so it lands top-level through this door too.
 *
 *  `focusNode` is the node the caller wants SELECTED on arrival — the transcript
 *  card's step row is the one door that names one, which is what makes that row a
 *  door rather than a disclosure. Recorded as a one-shot request rather than held,
 *  because a permanent pick would fight the reader the moment they clicked
 *  elsewhere in the tree. */
/** RETURNS the open, so a DEEP LINK can await it — see `subagent-view.ts`
 *  `openSubagentView` for the measurement. Every other caller is a click with nothing to
 *  wait for and voids it. */
export function openRunView(
  workflowID: string,
  name: string,
  parentChatID = "",
  focusNode = "",
): Promise<void> {
  // Replaces any earlier request outright: the last door clicked is the one the
  // reader is waiting on, and two pending picks for one run is a state nothing
  // could resolve honestly.
  focusRequest = focusNode === "" ? undefined : { workflowID, path: focusNode };
  const parentChat = parentChatID === "" ? runChatID(workflowID) : parentChatID;
  // Teach the store the pairing this door already knows. History carries
  // `run.parent_chat_id` for a FINISHED run whose lease is long gone, and the
  // transcript card carries the chat it lives in — so without this the run's own
  // page would have to re-derive the launching chat from a tab that may not be open
  // yet, and every other reader would keep answering "".
  noteRunChat(workflowID, parentChat);
  // The PARENT is a tab id, and a chat id is no longer one — so the nesting
  // question and the id it needs are the same lookup.
  const parentTab = parentChat === "" ? "" : tabIdFor("chat", parentChat);
  return openRunTab(
    workflowID,
    name,
    parentTab === "" ? { owns: false } : { parent: parentTab, owns: false },
  );
}

/** The node a door asked for, pending until a paint has honoured it.
 *
 *  ONE-SHOT, and both halves of that matter. It is CLEARED once a render has
 *  actually carried it, so the reader's own later clicks are never overruled; and
 *  it is left PENDING while the plan does not contain the path, which is the
 *  click-beats-fetch race — the row was clicked before `inspect` described the run,
 *  so the request waits for the reply rather than being lost to it. A path the plan
 *  never contains simply stays pending and the page auto-follows, which is the right
 *  answer for a row whose node the run does not have.
 *
 *  Keyed by run, so a request made for one run cannot be spent on another: the
 *  shared `#run-body` serves every run tab. */
let focusRequest: { workflowID: string; path: string } | undefined;

/** The page, built once and re-pointed. `exec-view/` knows nothing about
 *  workflows: `run-exec-source.ts` folds KAS's reply into its model, so a subagent
 *  tab is a second adapter into this same page rather than a second page. */
let page: ExecPageView | undefined;
let pageRun = "";

/** `.page-content` is shared by every run tab, so each run keeps its own offset. */
const pageScroll = new SharedScroll(
  () => document.querySelector<HTMLElement>("[id='run-view'] > .page-content"),
  () => (page?.root.isConnected === true ? pageRun : ""),
);

/** The step transcript, LAZILY loaded — ONE stream now, because there is one source.
 *
 *  `run-chat-steps.ts` reaches the real tool card through `messages-blocks.ts`, and
 *  that module's graph runs on through the editor openers and the navigator into
 *  `chat.ts` — the whole transcript stack. A run tab that statically imported it
 *  would pull all of that in to draw a tree, which is what this lazy edge exists to
 *  prevent. The first pass with content is the right moment to pay for it, and
 *  nothing has to be queued across the load: the entries are in the run store, so the
 *  pass that lands re-reads them rather than replaying a captured map. */
let stepStream: RunStepStream | undefined;
let stepStreamLoading = false;

/** The cached "Open the conversation" anchor, per (run, chat).
 *
 *  Cached because `detail.ts` guards its slot on element IDENTITY: `render` runs on
 *  every store invalidation, and re-seating a node BLURS it, so a fresh element per
 *  render would drop focus out of the link several times a minute on a live run. */
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
  // Retargeting drops the previous run's stream with the DOM it wrote into: a stream
  // holding the old page's hosts would append into detached elements, and a render
  // left registered in `messages-blocks.ts` would keep its disposers alive under the
  // next run's page.
  stepStream?.dispose();
  stepStream = undefined;
  stepStreamLoading = false;
  // The on-demand reads go with the page, which is this cache's whole bound: a run
  // tab retargeting or closing is the one moment a step's answer stops being wanted,
  // and there is no other moment it becomes wrong.
  clearStepTranscripts();
  emptyLink = undefined;
  // Beside them, and for their reason: a stale selection would intersect this run's holes
  // with another run's path, and a stale answer would swallow the first hole here.
  shownNode = undefined;
  holesAnswered = "";
  // Beside the other per-run caches, and for their reason: a retarget must not carry
  // another run's pick into the page it is about to build. A request for the run
  // being retargeted TO survives because the guard only clears one naming ANOTHER
  // run: `openRunView` records it BEFORE it opens the tab, and that open is what
  // triggers this mount.
  if (focusRequest !== undefined && focusRequest.workflowID !== workflowID) {
    focusRequest = undefined;
  }
  container.replaceChildren(built.root);
  return built;
}

/** A `TurnSource` over one run's log, so `turns.ts`'s pure projection reads it
 *  unchanged.
 *
 *  Built per call from the array `runTurns` hands back rather than held, because that
 *  array IS the store's order and a cached copy would be a second answer about it. */
function runSource(turns: readonly [string, TurnState][]): TurnSource {
  return { turns: new Map(turns), turn_order: turns.map(([id]) => id) };
}

/** The turns of one node path, in file order.
 *
 *  Several is the normal answer for a healed step, not an edge case: a resume after a
 *  restart, and a heal after the hosting bridge was retired, re-open the same path as
 *  a NEW turn, so a run's log carries one closed turn per attempt. `turn_open.node_path`
 *  is the join, and it is the only one available — `TurnState` carries no path of its
 *  own. */
function turnsForStep(
  turns: readonly [string, TurnState][],
  nodePath: string,
): [string, TurnState][] {
  return turns.filter(([, t]) => turnOpenOf(t)?.node_path === nodePath);
}

/** Whether a step turn may still grow: its OWN `turn_close` is absent.
 *
 *  Never the run's status, and that is the whole liveness rule now. A `parallel` node
 *  holds several open turns in one log, so a step that closed while its siblings carry
 *  on is finished, and the run reading `running` says nothing about it. */
function turnIsLive(state: TurnState): boolean {
  return state.closeAt === undefined;
}

/** The shown node (`undefined` for a container, which hosts nothing), and the holed
 *  turns already answered for it — keyed on the path plus those turn ids, so a hole
 *  arriving after a repair is a new question and a repaint is not a retry loop
 *  (`rereadStepTranscript` drops a settled verdict, so it must not run per frame). */
let shownNode: ExecNode | undefined;
let holesAnswered = "";

/** Ask for the shown step's transcript, on the selection MOVING.
 *
 *  TWO conditions here, both keyed on `inspect` reporting the node settled, because that
 *  is what `onShowNode` can observe: it notifies only when the shown `(path, state)`
 *  moves, which is the gate that keeps a repaint off the wire.
 *
 *   - the store holds NO turn for the path. That is a step which opened and closed
 *     entirely inside a connection gap, so no frame for it was ever delivered — and it
 *     is also where a hole recorded for a turn this client never SAW converges, since
 *     such an id belongs to no `TurnState` and so can be attributed to no node path.
 *   - it holds a turn with NO `turn_close`. That is a client holding the turn's earlier
 *     entries which missed its close, and it would otherwise read live for the tab's
 *     life. The `gone` verdict on that turn's `run_turn` stamp repairs the same thing
 *     from the digest's side; both read this log, so they cannot disagree and whichever
 *     lands second changes nothing.
 *
 *  A turn WITH a `turn_close` and no hole is settled and is never re-read. The settled
 *  gate is a DEFERRAL rather than a refusal: `select()` pins the selection, so a step
 *  clicked while running is read once it settles, and the transient `unavailable`
 *  verdict is re-asked no more often.
 *
 *  A HOLE is armed from the paint side instead (`armShownStepHole`), because it arrives
 *  while the step is `running` and moves neither the path nor the state. */
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

/** Re-ask for the shown step whose turn the store marked a HOLE, once per hole.
 *
 *  The arm the selection cannot carry: a hole arrives while the step is `running`, so the
 *  shown `(path, state)` never moves and `onShowNode` never fires again — while
 *  `appendRunEntry` rejects every later entry of that turn, so the reader watches a FROZEN
 *  live step until it settles or they click away and back. Paint sees it because a hole
 *  bumps the run's log version, the signal this module's one effect already reads. */
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

/** What a node with a transcript host but nothing in it should say. It is called on
 *  EVERY render of a hostable node, host or no host, so it must stay pure and cheap —
 *  what keeps its strings honest is that `detail.ts` HIDES the note element rather
 *  than withholding the call: `render` sets `empty.hidden = anyShown` (the shown
 *  node's host having children), and `bodyFor` hides the note and its action slot on
 *  the first frame. So no string here is ever on screen beside content it contradicts.
 *
 *  THREE questions, in this order, and only the last one is about the transcript:
 *
 *   - did the step RUN at all? `pending` and `skipped` opened no turn, so no read can
 *     change the answer, and `run-exec-source.ts` marks every leaf hostable whatever
 *     its state — so both reach here as ordinary members of the tree.
 *   - is it still IN FLIGHT? Then the step's entries are arriving on the run's own log
 *     and the region fills as they land, so there is one answer for both run
 *     populations and every device. Nothing here is keyed on where the reader is.
 *   - otherwise the step is SETTLED and every remaining answer is keyed on the READ's
 *     own verdict. That is the structural point of the on-demand read: nothing on this
 *     pane is route-dependent any more, the DOOR (`stepEmptyAction`) included, which
 *     offers the launching CONVERSATION rather than a second copy of the transcript.
 *
 *  ONE SOURCE, so no sentence here may name a second one. Both of the route sentences
 *  this note used to carry were false before the collapse and are unsayable after it: a
 *  step's entries are the RUN's, in `runs/<workflowId>/entries.jsonl`, and the step GET
 *  ADOPTS into that same store rather than reading a transcript from anywhere else. */
function stepEmptyNote(node: ExecNode): string {
  // Ahead of everything else, because a step with no execution behind it is the one
  // case here that no read can change. `.ev-d-state` two rows above already reads
  // "not started" or "skipped"; these say what that means for the blank region.
  if (neverRan(node.state)) {
    return node.state === "skipped"
      ? "This step was skipped, so it produced no output."
      : "This step has not started, so there is nothing to show yet.";
  }
  // IN FLIGHT is answered here IN FULL, so the verdict arms below are reached only for
  // a settled step. ONE answer, and the collapse is the point: a live step's entries
  // arrive on the run's own log, for both run populations and on every device. The two
  // answers this replaced were split on whether the client held the launching chat's
  // window, a question the run's own log removes rather than answers.
  if (inFlight(node.state)) {
    return "Waiting for this step to produce output\u2026";
  }
  switch (stepRead(shownRun, node.path)?.state) {
    case "loading":
      return "Loading this step's transcript\u2026";
    case "ready":
      // Reached only with the log holding NO entry for this step's turn after a read
      // the server answered, which is its own fact: the step ran and wrote nothing.
      // Deliberately not "captured nothing" — `capturedOutput` is a different thing
      // this pane already names two regions above, and `skipped` already owns "produced
      // no output".
      return "This step ran without producing a transcript.";
    case "gone":
      return "This step's transcript is no longer stored. What the step CAPTURED is above, when it declared captureOutput.";
    case "unavailable":
      return "This step's transcript could not be read just now.";
    case "unaddressable":
      // The one arm that is not the endpoint's own verdict: the server refused the
      // ADDRESS with a 4xx, so unlike the line above it this must not read as
      // transient — `settled()` never re-asks, and offering a retry that fails
      // identically is the affordance this state exists to remove.
      return "This step's transcript cannot be read: the run's plan does not name it.";
    default:
      // A SETTLED step with no read recorded, which is the instant before its request
      // exists rather than a state the pane sits in: `repaint` fires `onShowNode` —
      // and so `armStepRead` — AFTER `detail.render` has built this note, and again
      // whenever the shown node's state moves, so a read is in flight or one
      // instruction away. A step whose entries the log ALREADY holds reaches the same
      // instant from the other side, its rows being inside the lazy
      // `run-chat-steps.js` import, and `bodyFor` retires the note when they land.
      return "Loading this step's transcript\u2026";
  }
}

/** The affordance beside that note: a door into the conversation that LAUNCHED this run.
 *
 *  Rendered for a chat-parented run whose launching chat is known, and its subject is
 *  that CONVERSATION rather than this step's transcript. Under one log a step's entries
 *  are never in a chat's window, so this link is not a second route to the region's own
 *  content and cannot contradict any note beside it: what it offers is the CONTEXT — the
 *  turn that started the run, and the run's card among it, which is why `revealRunCard`
 *  is the reveal target and why that card's step rows lead straight back here.
 *  `detail.ts` hides it whenever content is on screen.
 *
 *  So it is offered for a step that NEVER RAN as well. The withholding this replaced was
 *  justified by the link's old subject, a transcript sitting in the launching chat; a
 *  conversation exists whatever the step did, and a reader looking at a skipped step is
 *  exactly one who wants to read why it was skipped.
 *
 *  Built the way `fundamentals/run-card.ts` builds `.run-open`: a real anchor, so
 *  middle-click and copy-link work, with a click handler that lets the app's own
 *  routing own a plain click and steps aside for a modified one.
 *
 *  NO `#turn-{n}` permalink, and the reason CHANGED: `Turn.n` is session-absolute now
 *  (`turns.ts` reads it off `turn_open.n`), so a computed anchor names the right turn but still
 *  resolves nowhere — `route-path.ts`'s private `parseHashLine` matches only
 *  `/^#L(\d+)/`. */
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
      // The reveal is the REFINEMENT and the open is the affordance, which is why it
      // is best-effort and lazily imported: `messages.ts` is the transcript stack,
      // and a run tab must not pull it in to draw a link.
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

/** Paint the view from a store value. `undefined` means the first fetch has not
 *  resolved, which is the ONLY case that shows a loading row: a refetch driven by an
 *  invalidation must not blank a run the reader is looking at, several times a
 *  minute on a busy one. */
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
  // Nudge the tab dot. A run launched from the Workflows tab is painted by the
  // `run_started` frame that follows, but a tab restored on boot or opened from
  // History lands on a run already going, and a PAUSED run emits no frames at all —
  // so without this its dot would sit blank for as long as the pause lasts. Both
  // calls are needed: `trackRun` admits a run this client has seen no event for, and
  // `refreshRunDots` repaints one it already knows.
  trackRun(workflowID);
  refreshRunDots();

  // Chat-parentedness comes from the RUN, not from client memory or from the door.
  // `parentSessionId` is `inspect`'s own answer and is empty for a manual and a
  // scheduled launch alike; the second term can only ADD chat-parented verdicts, and
  // every one it adds offers a DOOR into the conversation rather than withholding
  // anything, which is the safe direction. No verb hangs off it: the control row is
  // the server's answer.
  const launchingChat = launchingChatOf(workflowID);
  shownRunChatParented = (state.parentSessionId ?? "") !== "" || launchingChat !== "";

  // Reused only while the page this module built is STILL MOUNTED in this container.
  // The run id alone is not enough: `#run-body` is one shared element whose children
  // any caller may have replaced, and a page cached against a detached container
  // would leave the live one blank while every render went to DOM nobody can see.
  const mounted = pageRun === workflowID && page?.root.parentElement === container;
  const view = mounted && page !== undefined ? page : mountPage(container, workflowID);
  // Two inputs on different clocks, the same pair the transcript's card takes:
  // `inspect` says what the nodes are doing, and the dock says which of them is
  // blocked on a person. The run's own status cannot carry the second — KAS blocks
  // the asking step's turn and leaves the run `running` — and both reads are
  // signal-backed, so the one effect this module installs repaints on either.
  const focus = focusRequest?.workflowID === workflowID ? focusRequest.path : "";
  const run = runToExec(workflowID, state, runPlan(workflowID), runPendingAsks(workflowID), focus);
  view.render(run);
  if (!mounted) {
    pageScroll.restore(workflowID);
  }
  // Spent only once the page could actually honour it — the plan has to CONTAIN the
  // path, since `page.ts` ignores a focus naming an absent node. Clearing on the
  // render alone would drop a pick made before `inspect` arrived; leaving it forever
  // would re-assert it on every invalidation and fight the reader's next click.
  if (focus !== "" && flatten(run.nodes).some((n) => n.path === focus)) {
    focusRequest = undefined;
  }
  projectStepTranscripts(workflowID, run.nodes, turns);
  // After the projection, so the repair is asked over the entries this frame rendered.
  armShownStepHole(workflowID, turns);
}

/** Project this run's step transcripts into the detail pane, from the run's own log.
 *
 *  ONE source, so there is no preference rule left to state: the entries this pane
 *  renders are the run's, appended to `runs/<workflowId>/entries.jsonl` by the same
 *  store code that writes a chat's log, for a chat-parented and a parentless run
 *  alike. What this replaced was a per-step choice between the launching chat's `wf:`
 *  lane and a settled on-demand read, and neither exists any more: a run's steps are
 *  not in any chat's log, and the step GET now ADOPTS into this same store rather than
 *  holding a second copy of the content.
 *
 *  It performs NO chat fetch and now has nothing to decline: `store-load.ts`
 *  `loadMessages` used to be the way to make the chat route reachable more often, and
 *  was refused because a run tab fetching a chat's window makes a SECOND owner of it
 *  (paging, the eviction sweep). With the route gone the question does not arise. */
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
      // The tab may have retargeted during the load, in which case this stream would
      // write into the previous page's detached hosts. `page` rather than a captured
      // view for the same reason: the current page is the mounted one.
      if (pageRun !== forRun || page === undefined) {
        return;
      }
      const host = page;
      const stream = createRunStepStream((nodePath) => host.bodyFor(nodePath));
      stepStream = stream;
      // RE-PROJECTED rather than replayed: the load is async, so the log may have
      // grown past what was captured and the stale map would leave the tail to the next
      // repaint. Outside the view effect, so `runTurns`' own touch registers nothing.
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

/** What to paint for every LEAF of this plan the log holds a turn for.
 *
 *  A turn whose path names no node is dropped so no orphan host is minted, and a
 *  CONTAINER is out by construction (it carries `transcript !== true` and hosts
 *  nothing). Every leaf WITH a turn is painted rather than only the selected one,
 *  because the hosts persist for the pane's life and a step's entries stream — the
 *  same reason `detail.ts` keeps them.
 *
 *  `live` is each turn's OWN `turn_close` absence, so one node's caret says nothing
 *  about its siblings': a `parallel` node holds several open turns in one log. */
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

/** The run's control row, rendered from the SERVER's answer and rebuilt only when
 *  that answer moved (`controlRow` owns why).
 *
 *  Nothing is decided here. The exec view's own state word is not even consulted:
 *  the affordance is computed against a status the server read one round trip ago,
 *  and re-deriving it from the state this page happens to hold would put the
 *  drifting copy back — which is what the status-keyed table plus a parentlessness
 *  gate used to be. Parentlessness gates no verb here either: `hostBridge`
 *  (run_host.go) resolves an agent-parented run's carrier from the launching chat's
 *  live bridge, so which verbs that run offers is the server's answer too.
 *
 *  Three outcomes. Verbs render as buttons. No verbs but a REFUSAL renders the
 *  server's sentences where the buttons would have been — before this the row
 *  returned null and a reader was never told why a run offered nothing. Neither
 *  renders nothing, the honest answer for a completed run (its state word says it)
 *  and for the moment before the first fetch resolves. */
function buildRunControls(workflowID: string): HTMLElement | null {
  const answer = runControls(workflowID);
  const sig = answer === undefined ? "" : controlSignature(workflowID, answer);
  if (sig === controlSig) {
    return controlRow;
  }
  // The answer moved, so the row on screen is on its way out and its bindings go
  // with it — the caller replaces the host's children with whatever is returned here.
  dropControlRow();
  controlSig = sig;
  controlRow = answer === undefined ? null : renderControls(workflowID, answer);
  return controlRow;
}

/** What the row is a function of, and nothing else: the run it acts on, the verbs it
 *  draws and the sentences it draws instead. The parent chat travels on the same
 *  answer and changes nothing here, so it is left out rather than churning the row. */
function controlSignature(workflowID: string, answer: RunControlsResponse): string {
  // Keyed, not positional: `refused` is a map on the wire, so two answers that differ
  // only in the order the server happened to serialize them are the same row.
  const refused = Object.entries(answer.refused ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([verb, text]) => `${verb}\u0001${text}`)
    .join("\u0002");
  return `${workflowID}\u0000${answer.verbs.join("\u0001")}\u0000${refused}`;
}

/** The row itself. Split from the decision above so the guard reads as one thing. */
function renderControls(workflowID: string, answer: RunControlsResponse): HTMLElement | null {
  const verbs = offeredVerbs(answer.verbs);
  if (verbs.length === 0) {
    return refusalRow(refusalSentences(answer.refused));
  }
  const row = el("div", { className: "run-controls" });
  for (const verb of verbs) {
    const action = RUN_ACTION[verb];
    const btn = el(
      "button",
      {
        type: "button",
        className: verb === "cancel" ? "btn btn-sm btn-danger" : "btn btn-sm",
        onclick: () => {
          // No optimistic state flip. Every one of these verbs settles at a NODE
          // boundary, so the run is still `running` when the reply arrives and a
          // flipped label would be a lie for as long as the node takes. The
          // run_progress invalidation is what repaints this row.
          void action.dispatch(workflowID);
        },
      },
      CONTROL_LABEL[verb],
      // `el` answers HTMLElement; the pending binding needs the `disabled`
      // property, which only the concrete button type declares.
    ) as HTMLButtonElement;
    // A retry starts a process and can legitimately take tens of seconds, so an
    // unbound button looks dead for the whole handshake and can be clicked again
    // meanwhile. Its disposer is held rather than left to the binding's own
    // detach detection, which cannot arm on a button bound before it is attached.
    controlBindings.push(bindLoadingState(action.name, btn));
    row.appendChild(btn);
  }
  return row;
}

/** The row a run with no verbs gets: the server's own sentences, in the place a
 *  reader is already looking for the control. Null when there is nothing to say,
 *  so a completed run keeps its clean header. */
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
