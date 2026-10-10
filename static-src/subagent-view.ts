// One SUBAGENT execution on its own page (/chat/{id}/subagent/{taskId}), with its whole pipeline
// readable in place: the second surface over the card's blocks. It IS the shared exec view
// (`subagent-exec-source.ts` folds into its model), so no layout or status vocabulary lives here.
// View-only (no composer, dock or controls row). The close stops nothing but RELEASES: the demand
// effect drops the page once no open subagent tab names a member of its group. Nothing is fetched:
// blocks persist with `agent_subtask_id`. The page projects the GROUP (`sliceSubagentGroup`); a body
// mounts on first `onShowNode` and is kept. Paged-out turns say so; unopened chats fill in later.

import { el, effect, signal, touch } from "@cplieger/reactive";
import { hasTab, openSubagentRefs, openSubagentTab } from "./tabs.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight } from "./exec-view/status.js";
import type { ExecNode } from "./exec-view/model.js";
import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
import { refreshChatView } from "./chat.js";
import { chatTurnLive, get, isThinking, messagesVersionOf } from "./store.js";
import { laneSig } from "./store-signals.js";
import { ICON_TAB_AGENT } from "./icons.js";
import {
  blockShape,
  shapeExtends,
  sliceSubagentGroup,
  type SubagentGroup,
  type SubagentProjection,
  type SubagentSlice,
} from "./subagent-slice.js";
import { subagentToExec } from "./subagent-exec-source.js";
import { parseSubagentRef, subagentRef } from "./tab-materialize.js";
import type { TurnState } from "./types.js";
import { SharedScroll } from "./view-scroll.js";

/** A detached render is addressed `(turn, lane)`, `messages-blocks.ts`'s render key; the
 *  transcript holds the same turn under the EMPTY lane, so the two cannot clobber each other. */

/** One PAGE INSTANCE's identity: the GROUP, so switching stage tabs reuses the page and its
 *  bodies (re-pointing through `ExecRun.focus`). Prefixed so it cannot collide with a `renderID`. */
function pageID(chatID: string, group: SubagentGroup, subtaskID: string): string {
  return group.pipeline === ""
    ? `page:sub:${chatID}:${subtaskID}`
    : `page:sub:${chatID}:pipeline:${group.pipeline}`;
}

/** The delegate on screen, a SIGNAL so the view effect re-runs on a tab switch; ONE
 *  `#subagent-view` serves every subagent tab. Also the page's LIFETIME input: the demand effect
 *  empties it and the paint effect's empty-subject guard drops the page. */
const shown = signal<{ chatID: string; subtaskID: string }>({ chatID: "", subtaskID: "" });

/** One mounted member's render lifecycle, per MEMBER: the repaint gate is a UNION over registered
 *  renders, so each must be disposed (as `run-chat-steps.ts`'s `StepRender`). */
interface BodyRender {
  host: HTMLElement;
  /** The turn this member's lane was mounted from, which is half its render key. Held
   *  because the DISPOSE has to name it after the projection has moved on. */
  turnID: string;
  /** The mounted entry shape: the incremental update is correct only while the mounted prefix is
   *  unchanged (tail growth keeps it; a rewind or refetch does not). */
  shape: readonly string[];
  /** Guarded because every later repaint of a finished delegate lands here too. */
  sealed: boolean;
}

/** ONE PAGE, ONE LIFETIME: dropping the record IS the disposal, so a half-disposed page cannot be
 *  spelled. `key` is `pageID`'s GROUP key; `chatID` is immutable per page; `projection` is the
 *  latest paint's inputs and the membership demand reads; `bodies` are released together;
 *  `shownNodePath` is a subtask id for a delegate and anything else for a node with no transcript. */
interface MountedPage {
  readonly key: string;
  readonly chatID: string;
  readonly view: ExecPageView;
  projection: SubagentProjection;
  readonly bodies: Map<string, BodyRender>;
  shownNodePath: string;
}
let mounted: MountedPage | undefined;

/** `.page-content` is shared by every subagent tab, so each TAB keeps its own offset.
 *  Keyed by the tab's ref rather than the page: sibling stages of one pipeline share
 *  a page, and each shows a different transcript. */
const pageScroll = new SharedScroll(
  () => document.querySelector<HTMLElement>("[id='subagent-view'] > .page-content"),
  () => (mounted === undefined ? "" : shownRef()),
);

function shownRef(): string {
  const { chatID, subtaskID } = shown.peek();
  return subagentRef(chatID, subtaskID);
}

/** A re-entrancy flag for one synchronous `view.render()`: only a click (outside a render) must
 *  sync the bodies itself. */
let inPaint = false;

/** Point the shared subagent view at one delegate (a tab's `onShow`). Writing the subject is the
 *  whole call; the paint effect re-points and releases a different group's page. */
export function showSubagent(chatID: string, subtaskID: string): void {
  shown.value = { chatID, subtaskID };
  installEffects();
  // A re-show of the page already mounted mounts nothing, so the paint restores nothing.
  if (mounted !== undefined) {
    pageScroll.restore(shownRef());
  }
}

/** A subagent tab's `refresh`. The page is a projection of the launching chat's
 *  blocks, so its window is the only thing that can make it current. */
export function refreshSubagent(chatID: string): void {
  refreshChatView(chatID);
}

/** Whether an open subagent tab still names a member of `m`'s group: a `Map.has` plus a chat
 *  compare (two chats can share a subtask id), which also rejects a malformed ref. */
function demandHolds(m: MountedPage, refs: readonly string[]): boolean {
  for (const ref of refs) {
    const { chatID, subtaskID } = parseSubagentRef(ref);
    if (chatID === m.chatID && m.projection.slices.has(subtaskID)) {
      return true;
    }
  }
  return false;
}

/** The view's two subscriptions, installed once. INSTALL ORDER IS LOAD-BEARING: demand first, so
 *  its install-time pass finds nothing mounted rather than dropping the page paint just mounted. */
let effectsInstalled = false;
function installEffects(): void {
  if (effectsInstalled) {
    return;
  }
  effectsInstalled = true;

  // DEMAND is a SECOND effect whose ONLY tracked read is `openSubagentRefs()`; in the paint effect
  // it would repaint on every tab mutation (as `run-dots.ts` separates seed and paint).
  effect(() => {
    const refs = openSubagentRefs();
    // UNTRACKED: every way to reach "mounted and unwanted" is a tab-set change, and a mount always
    // follows an activation of a row in the projection.
    const m = mounted;
    if (m === undefined || demandHolds(m, refs)) {
      // Nothing mounted. Accepted residual: a non-resident delegate leaves `shown` set with nothing
      // mounted; bounded and self-healing.
      return;
    }
    // Write the INPUT, never `mounted` (the paint effect is its single writer). Clearing `shown` stops
    // the chat's next delta re-mounting a page for a closed tab.
    for (const id of m.projection.slices.keys()) {
      pageScroll.forget(subagentRef(m.chatID, id));
    }
    shown.value = { chatID: "", subtaskID: "" };
  });

  effect(() => {
    const { chatID, subtaskID } = shown.value;
    if (chatID === "" || subtaskID === "") {
      // TOTAL over its input: an empty subject means no page. This branch is what
      // makes the demand effect's `shown` write the drop.
      unmount();
      return;
    }
    // Per-chat structural growth gives live background updates. Below the guard, or the empty subject
    // would mint a `messagesVersionSigs` entry under `""`.
    touch(messagesVersionOf(chatID));
    const session = get(chatID);
    const projection =
      session === undefined
        ? { group: { pipeline: "", driver: undefined, members: [] }, slices: new Map() }
        : sliceSubagentGroup(session, subtaskID, isThinking(chatID));
    subscribeToDeltas(projection);
    paint(chatID, subtaskID, projection);
  });
}

/** Subscribe to EVERY member's LANE signal: a delta bumps the lane, not the chat's version, and a
 *  sibling's body can be mounted at any selection. */
function subscribeToDeltas(projection: SubagentProjection): void {
  for (const [lane, slice] of projection.slices) {
    const turnID = slice.turn?.id;
    if (turnID !== undefined) {
      touch(laneSig(turnID, lane));
    }
  }
}

function paint(chatID: string, subtaskID: string, projection: SubagentProjection): void {
  const host = document.getElementById("subagent-body");
  if (host === null) {
    return;
  }

  // Nothing resident for this delegate: no blocks AND no invocation. One honest
  // sentence per situation, because a blank page reads as a broken one in both.
  const own = projection.slices.get(subtaskID);
  if (own === undefined || (!holdsOutput(own) && own.invocation === undefined)) {
    unmount();
    host.replaceChildren(el("div", { className: "list-empty" }, notResidentNote(chatID)));
    return;
  }

  const key = pageID(chatID, projection.group, subtaskID);
  // `#subagent-body` is shared, so a page cached against a detached container renders into nothing;
  // re-resolved per pass (as `run-view.ts` does).
  const kept =
    mounted?.key === key && mounted.view.root.parentElement === host ? mounted : undefined;
  const m = kept ?? mountPage(host, key, chatID, projection);
  // AFTER `mountPage`, which drops the previous page and with it its own projection.
  m.projection = projection;
  inPaint = true;
  try {
    m.view.render(subagentToExec(subtaskID, projection, chatTurnLive(chatID)));
  } finally {
    inPaint = false;
  }
  if (kept === undefined) {
    pageScroll.restore(subagentRef(chatID, subtaskID));
  }
  syncBodies(m);
}

/** Bring every mounted transcript current, mounting the SHOWN node's first. Lazy: a hidden body
 *  still costs construction. Once mounted it stays; the scroll offset is shared (the page scrolls). */
function syncBodies(m: MountedPage): void {
  const wanted = m.shownNodePath;
  const slice = wanted === "" ? undefined : m.projection.slices.get(wanted);
  // An EMPTY slice is left unmounted deliberately: `exec-view/detail.ts` shows the
  // empty note only while its host has no children, and that note is the honest answer
  // for a delegate that has produced nothing yet.
  const turnID = slice?.turn?.id;
  if (slice !== undefined && turnID !== undefined && holdsOutput(slice) && !m.bodies.has(wanted)) {
    m.bodies.set(wanted, {
      // Through the transcript's OWN dispatcher with this lane as render root: prose runs, fold rules
      // and grandchild cards come free.
      host: m.view.bodyFor(wanted),
      turnID,
      shape: [],
      sealed: false,
    });
  }
  for (const [id, rec] of m.bodies) {
    const s = m.projection.slices.get(id);
    if (s !== undefined) {
      renderBody(rec, m.chatID, id, s);
    }
  }
}

/** Paint one member's transcript into its own host, deciding per record whether the
 *  mounted prefix can be appended to or has to be rebuilt. */
function renderBody(
  rec: BodyRender,
  chatID: string,
  subtaskID: string,
  slice: SubagentSlice,
): void {
  const turn = slice.turn;
  if (turn === undefined) {
    return;
  }
  const shape = blockShape(slice.entries);
  // A turn the window no longer holds under the same id is a REBUILD whatever the
  // shape says: the render is keyed `(turn, lane)`, so the mounted one belongs to a
  // turn this pass is not rendering.
  const sameTurn = rec.turnID === turn.id;
  if (sameTurn && shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
    updateDetachedBody(rec.host, turn, chatID, subtaskID, slice.live);
  } else {
    disposeDetachedBody(rec.turnID, subtaskID);
    rec.host.replaceChildren();
    rec.turnID = turn.id;
    rec.sealed = false;
    buildDetachedBody(rec.host, turn, chatID, subtaskID, slice.live);
  }
  rec.shape = shape;

  // Settled: flush the markdown streams and collapse the reasoning traces, so a
  // finished delegate does not sit under a caret. Read off the SLICE, so liveness is
  // per member: a stage can finish while its siblings carry on.
  if (!slice.live && !rec.sealed) {
    rec.sealed = true;
    finalizeDetachedBody(turn.id, subtaskID);
  }
}

/** Build the page into `#subagent-body`, replacing whatever was there. */
function mountPage(
  host: HTMLElement,
  key: string,
  chatID: string,
  projection: SubagentProjection,
): MountedPage {
  unmount();
  const built = buildExecPage({
    emptyNote: emptyNote,
    // The agent hexagon rather than the workflow glyph. No `controls`: a delegate has
    // no pause, resume or cancel verb, so the row does not render at all.
    icon: ICON_TAB_AGENT,
    // A selection arrives from a repaint or a click; only the click syncs here. Resolved through the
    // module slot, so a dropped page is never reached; `exec-view/page.ts` fires this only from
    // `repaint()`, never during `buildExecPage`.
    onShowNode: (node) => {
      const m = mounted;
      if (m === undefined) {
        return;
      }
      m.shownNodePath = node?.path ?? "";
      if (!inPaint) {
        syncBodies(m);
      }
    },
  });
  mounted = {
    key,
    chatID,
    view: built,
    projection,
    bodies: new Map(),
    shownNodePath: "",
  };
  host.replaceChildren(built.root);
  return mounted;
}

/** Release the page and its renders; idempotent. `mounted = undefined` comes FIRST, and the node is
 *  removed as well as disposed. */
function unmount(): void {
  const m = mounted;
  if (m === undefined) {
    return;
  }
  mounted = undefined;
  for (const [subtaskID, rec] of m.bodies) {
    disposeDetachedBody(rec.turnID, subtaskID);
  }
  m.view.dispose();
  m.view.root.remove();
}

/** What a node with an empty transcript host says. Two cases (blocks persist in the chat file).
 *  Reached for a REAL delegate only: `toLeaf` alone sets `node.transcript`. */
function emptyNote(node: ExecNode): string {
  return inFlight(node.state)
    ? "Waiting for this delegate to produce output\u2026"
    : "This delegate finished without producing any transcript.";
}

/** Why this page has nothing to show, in the reader's terms. */
function notResidentNote(chatID: string): string {
  return get(chatID) === undefined
    ? "This conversation is not open here yet. Open it and this delegate's output appears."
    : "This delegate's turn is not in the loaded history. Scroll up in the conversation to load it.";
}

/** Whether the lane holds a sealed or open entry: an open entry never reaches the log, so a
 *  delegate's first streamed words need the open half. */
function holdsOutput(slice: SubagentSlice): boolean {
  return slice.entries.length > 0 || slice.open;
}

/** Open (or focus) a delegate's page. No name: the tab factory derives it from the chat store. */
/** RETURNS the open, so a DEEP LINK can await it: `router.ts` releases its claim on the location
 *  when `applyRoute` settles, so voiding it let the next projection emit overwrite the opened URL.
 *  Click callers void it. */
export function openSubagentView(chatID: string, subtaskID: string): Promise<void> {
  return openSubagentTab(chatID, subtaskID);
}

/** Whether an open subagent tab projects `chatID`'s transcript: the eviction exemption app.ts
 *  registers (store.ts cannot import tabs.ts). Answered from the RESIDENT blocks. */
export function subagentTabProjectsChat(chatID: string): boolean {
  const session = get(chatID);
  if (session === undefined) {
    return false;
  }
  for (const state of session.turns.values()) {
    for (const lane of laneNames(state)) {
      if (hasTab("subagent", subagentRef(chatID, lane))) {
        return true;
      }
    }
  }
  return false;
}

/** Every DELEGATE lane one resident turn holds. The empty lane is the log's own agent
 *  and names no delegate; an open entry counts, because a delegate whose first words
 *  are still arriving is exactly one whose window must not be evicted. */
function laneNames(state: TurnState): Set<string> {
  const out = new Set<string>();
  for (const e of state.entries) {
    const lane = e.lane ?? "";
    if (lane !== "") {
      out.add(lane);
    }
  }
  for (const lane of state.openEntries.keys()) {
    if (lane !== "") {
      out.add(lane);
    }
  }
  return out;
}
