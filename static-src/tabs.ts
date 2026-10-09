// The tab strip as a PROJECTION of the server-owned tab set (internal/tabs): no membership, no
// derived order, no persistence; a gesture DISPATCHES and the `tabs_changed` frame paints.
// `TabSubject` is the shared half, `TabViewSpec` (tab-view.ts, via tab-materialize.ts) the local
// half, and `tabs-sync.ts` decides which frames arrive (this file implements its `TabsTarget`).
// Removal is STATED per id in `removed_ids`; `order` never implies closure. Ids are opaque.
// Local by design: the ACTIVE tab (device-view.ts), the live DOT, the NAME override, the
// pinned-first PARTITION, and the DOM.

import { pushRoute } from "./router.js";
import type { Route, SettingsTab, GitTab, DocsTab, HistoryTab } from "./route-path.js";
// The kinds' one definition is the Go const block (internal/marotte/domain_tabs.go), emitted as a
// registered enum, so per-kind handling is TOTAL.
import type { TabKind, TabSubject, TabsChangedPayload } from "./types.js";
// The DOM-free per-kind tables live in tab-view.ts; this file owns the STORE and the DOM.
import { TAB_ICONS, TAB_VIEWS, type TabDotStatus, type TabViewSpec } from "./tab-view.js";
import { materializeTab, subagentRef, subjectForRoute } from "./tab-materialize.js";
import { viewStale } from "./view-freshness.js";
// A PATH-SPACE normaliser rather than a feature behaviour. router.ts refuses the same
// import because a route table sits UNDER the feature line and this file does not.
// Acyclic: files-shared.ts imports only @cplieger/reactive.
import { normalizeDirPath } from "./files-shared.js";
import {
  registerTabsTarget,
  permute,
  listTabs,
  markLocalOp,
  beginAdopt,
  adoptCommitted,
  beginRemove,
  removeCommitted,
  opFailed,
  opTimedOut,
  removesPending,
  setOnRemovesSettled,
  beginReorder,
  reorderCommitted,
  overlayOrder,
  type TabsTarget,
} from "./tabs-sync.js";
import {
  openTabCommand,
  closeTabCommand,
  pinTabCommand,
  reparentTabCommand,
  reorderTabsCommand,
  REORDER_STALE,
} from "./actions/tabs.js";
import { newOpID } from "./transport.js";
import { join as joinKey } from "@cplieger/keyenc";
import { ICON_CLOSE, ICON_PIN_FILLED, ICON_TAB_SUBTAB } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { activeView, setActiveView } from "./device-view.js";
import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { signal, effect, el } from "@cplieger/reactive";
import {
  attachDrag,
  dragOwnsStrip,
  exceedsSlop,
  isDragHandled,
  pointerDragActivation,
  setReorderCallback,
  setReprojectCallback,
  setTapCallback,
} from "./tabs-drag.js";
import { announce } from "@cplieger/ui-primitives/announce";
import type { ViewportBox } from "./viewport-frame.js";
import { viewportBox, viewportMoved } from "./viewport-frame.js";
import { showContextMenu } from "./context-menu.js";
import type { ContextMenuItem } from "./context-menu.js";
import { downloadChatExport } from "./chat-export.js";
import { MAX_CHAT_NAME_UNITS, downloadKiroSession, renameChat } from "./actions/chat.js";
import { getActiveId, getSessions, setActive } from "./store.js";
import { relativeTime } from "./relative-time.js";
import { restoreFailedSend, retargetComposer } from "./composer-state.js";
import { info, error as toastError } from "./toast.js";
import { BUS_TAB_CHANGED, emitBus } from "./bus.js";
import { setPageTitle, clearPageTitle } from "./page-title.js";

// --- Types ---

/** Re-exported so the consumers that read a tab's kind keep importing it from
 *  the tab module, while the DEFINITION stays single and server-side. */
export type { TabKind };

/** Re-exported for the same reason: the definition lives in tab-view.ts with the
 *  rest of the view contract. */
export type { TabDotStatus };

/** One row: the shared half, the local half, and the device-local facts. `subject` is replaced
 *  wholesale by each frame. `spec` is a SNAPSHOT (`owns` is immutable; the parent and `pinned` are
 *  read from the subject). `name`, `dotStatus` and `runDot` are the mutable local fields; the dot is
 *  parked here so a rebuild repaints it. The tooltip IS `name`. */
interface TabRow {
  subject: TabSubject;
  spec: TabViewSpec;
  name: string;
  dotStatus?: TabDotStatus | undefined;
  /** When the dot's OUTCOME became true (`ChatHeader.updated_at`). A sibling field, because
   *  `recordDotStatus` compares states by value. */
  dotSince?: number | undefined;
  /** The WORKFLOW mark's state and the breakdown its phrase needs, parked as ONE
   *  field so a rebuilt row cannot repaint the state without the count that
   *  qualifies it. A third mutable local field, live like the dot beside it. */
  runDot?: { status: TabRunDotStatus; tally: TabRunTally } | undefined;
}

/** The workflow mark's states, NARROWED from the dot's with `Extract`. No `done`/`failed`: the live
 *  inventory deletes a terminal run, so the mark WITHDRAWS (as `run-bar.ts` does). No `idle`: no
 *  live run means nothing to report. */
export type TabRunDotStatus = Extract<TabDotStatus, "working" | "waiting" | "input">;

/** How many live runs the ONE mark stands for and their split (the fold is `chat-run-dots.ts`'s),
 *  shown in the tooltip and the announced phrase. */
export interface TabRunTally {
  readonly total: number;
  readonly working: number;
  readonly waiting: number;
  readonly input: number;
}

/** No live run. A named constant rather than an inline literal because it is the
 *  value a REBUILT row paints from before its producer next churns. */
const NO_RUNS: TabRunTally = { total: 0, working: 0, waiting: 0, input: 0 };

/** What `openTab` needs. `kind` plus `ref` names the subject; everything else is
 *  a choice about this particular open. */
export interface OpenTabArgs {
  kind: TabKind;
  /** A chat id, an absolute path, a run id. Empty (or absent) for a singleton,
   *  the one kind whose identity is its kind. */
  ref?: string;
  /** An open tab to nest under (a SUB-TAB). A parent that is not open promotes the tab to top level,
   *  as the server's rule does. */
  parent?: string;
  /** Whether closing this tab tears down what it shows (default true); `owns: false` makes a VIEW.
   *  Not derivable from the kind. */
  owns?: boolean;
  /** A label this caller knows and a subject cannot carry. */
  name?: string;
  /** Whether to activate once the tab exists. Default true; a bulk restore passes
   *  false, because the strip is the reader's. */
  activate?: boolean;
}

// --- Store ---

interface Callbacks {
  onActivate: ((id: string) => void) | null;
  onEmpty: (() => void) | null;
  /** Notified with the id of every tab that leaves the projection. A
   *  NOTIFICATION slot, like onEmpty: it must not mutate the store. */
  onClosed: ((id: string) => void) | null;
}

interface Internal {
  emptyTimer: ReturnType<typeof setTimeout> | null;
  /** Whether an empty-state respawn is waiting for the pending removes to
   *  settle. See scheduleEmpty: an empty strip whose close is still in flight
   *  must not respawn a chat the rollback may be about to bring back. */
  emptyDeferred: boolean;
  renderQueued: boolean;
  /** Whether a tab has ever entered the projection. The DOM subscriber keys its
   *  no-op on this rather than on an empty store, because the two differ on
   *  exactly one transition: the one INTO empty. See the render effect. */
  everOpened: boolean;
}

interface State {
  tabs: TabRow[];
  active: string;
}

const state: State = { tabs: [], active: "" };
const callbacks: Callbacks = { onActivate: null, onEmpty: null, onClosed: null };
const internal: Internal = {
  emptyTimer: null,
  emptyDeferred: false,
  renderQueued: false,
  everOpened: false,
};

/** Caller-supplied names keyed by `(kind, ref)`: a name arrives before the server mints an id, so
 *  the row is BUILT with it, and it survives a re-list. Bounded by `forgetRow`. */
const nameOverrides = new Map<string, string>();

/** Tab ids in ACTIVATION order, most recent first, for a close's hand-over. In memory only (a cold
 *  strip falls back to position 0) and non-reactive. Head is `state.active`; no closed id is held
 *  (`forgetRow`). */
const activationHistory: string[] = [];

function noteActivation(id: string): void {
  const at = activationHistory.indexOf(id);
  if (at >= 0) {
    activationHistory.splice(at, 1);
  }
  activationHistory.unshift(id);
}

function forgetActivation(id: string): void {
  const at = activationHistory.indexOf(id);
  if (at >= 0) {
    activationHistory.splice(at, 1);
  }
}

/** Put a captured entry back behind its ANCHOR (or at the head). A closed anchor falls to the TAIL:
 *  understating recency beats inverting it. */
function restoreActivation(id: string, anchor: string): void {
  if (anchor === "") {
    activationHistory.unshift(id);
    return;
  }
  const at = activationHistory.indexOf(anchor);
  activationHistory.splice(at < 0 ? activationHistory.length : at + 1, 0, id);
}

/** The most recently activated tab still open, or "" when the history holds
 *  none. `hasRow` is the second line of defence behind the prune: a stale entry
 *  can never be picked even if a future removal path forgets to forget. */
function mostRecentOpenTab(): string {
  return activationHistory.find((id) => hasRow(id)) ?? "";
}

/** Reactive version counter. Effects subscribed via `tabsEffect()` re-run on
 *  every emit(). State is mutated in place; this counter is the signal those
 *  mutations trip. */
const stateVersion = signal(0);

/** Reactive counter for DOT writes, which do not `emit()`; only `subscribeTabCues` reads it. */
const dotVersion = signal(0);

/** All registered module-level effects. Tracked so _resetForTest can dispose
 *  them and start fresh; production never disposes. */
const moduleEffects: (() => void)[] = [];

function emit(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** Register an effect that re-runs on every state mutation. */
function tabsEffect(fn: (s: State) => void): () => void {
  const cleanup = effect(() => {
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    stateVersion.value; // subscribe
    fn(state);
  });
  moduleEffects.push(cleanup);
  return cleanup;
}

/** Reactive read of the tab-set version: an effect calling this re-runs on every committed tab
 *  mutation. For writers painting rows from outside the strip (run-dots.ts), whose target row can
 *  arrive a round trip after the event that made it paintworthy. */
export function tabSetVersion(): number {
  return stateVersion.value;
}

function subjectKey(kind: TabKind, ref: string): string {
  return joinKey(kind, ref);
}

function rowOfID(id: string): TabRow | undefined {
  return state.tabs.find((t) => t.subject.id === id);
}

/** How many parents a row sits under: 0 for a top-level tab, 1 for a child, 2 for
 *  a grandchild. Walks the SUBJECT's parent chain through the current rows and
 *  stops at an orphan, so a hand-edited cycle cannot spin it. */
function depthOf(row: TabRow): number {
  let depth = 0;
  let parent = row.subject.parent;
  while (parent !== "" && depth < state.tabs.length) {
    const next = rowOfID(parent);
    if (next === undefined) {
      break;
    }
    depth++;
    parent = next.subject.parent;
  }
  return depth;
}

// --- The name a row renders ---

/** The label for a subject: the caller's override when one was supplied, else
 *  the factory's derived default. One expression, so a rebuilt row cannot lose an
 *  override and an override cannot outlive its tab. */
function nameFor(subject: TabSubject, spec: TabViewSpec): string {
  return nameOverrides.get(subjectKey(subject.kind, subject.ref)) ?? spec.name;
}

function buildRow(subject: TabSubject): TabRow {
  const spec = materializeTab(subject);
  const row: TabRow = { subject, spec, name: nameFor(subject, spec) };
  if (spec.dotStatus !== undefined) {
    row.dotStatus = spec.dotStatus;
  }
  return row;
}

/** Drop everything keyed off a departed tab, and tell the one listener. Called
 *  from both removal paths (an applied `removed_ids`, and a snapshot that no
 *  longer holds the tab) so neither can leak. */
function forgetRow(row: TabRow): void {
  nameOverrides.delete(subjectKey(row.subject.kind, row.subject.ref));
  // The one function every removal door already calls, which is what makes the MRU
  // prune structural and prunes a whole cascade in the loop that removes it.
  forgetActivation(row.subject.id);
  callbacks.onClosed?.(row.subject.id);
}

// --- Ordering ---

/** A row's sub-rows, in projection order. Reads the SUBJECT's parent, so the
 *  tab module needs nothing from the chat store to lay itself out. */
function childrenOf(id: string): TabRow[] {
  return state.tabs.filter((t) => t.subject.parent === id);
}

/** Insert a row at its canonical position: a sub-tab right after its parent's existing children,
 *  a top-level tab at the end. Keeping `state.tabs` parent-anchored means render and keyboard order
 *  need no grouping logic of their own. */
function insertRow(row: TabRow): void {
  // The one place a tab enters the projection, so the one place this is recorded.
  internal.everOpened = true;
  const parent = row.subject.parent;
  if (parent === "") {
    state.tabs.push(row);
    applyPinOrder();
    return;
  }
  const pIdx = state.tabs.findIndex((t) => t.subject.id === parent);
  if (pIdx < 0) {
    // An orphan (parent not open here) behaves as top-level rather than
    // vanishing: a tab nobody can see is worse than a tab in the wrong place.
    state.tabs.push(row);
    applyPinOrder();
    return;
  }
  let at = pIdx + 1;
  while (at < state.tabs.length && state.tabs[at]?.subject.parent === parent) {
    at++;
  }
  state.tabs.splice(at, 0, row);
}

/** Group `rows` into [parent, ...its whole descendant tree] runs, in array order, so the pin
 *  partition and Move up/down move a parent and its subtree as one unit. Membership is tested
 *  against every row already in a group, or a grandchild becomes an orphan top-level group. */
function groupsOf(rows: readonly TabRow[]): TabRow[][] {
  const groups: TabRow[][] = [];
  for (const t of rows) {
    const owner =
      t.subject.parent === ""
        ? undefined
        : groups.findLast((g) => g.some((m) => m.subject.id === t.subject.parent));
    if (owner === undefined) {
      groups.push([t]);
      continue;
    }
    owner.push(t);
  }
  return groups;
}

function tabGroups(): TabRow[][] {
  return groupsOf(state.tabs);
}

/** `rows` with every pinned group ahead of every unpinned one, stably and with
 *  each parent's children still behind it. */
function pinPartition(rows: readonly TabRow[]): readonly TabRow[] {
  const groups = groupsOf(rows);
  const pinned = groups.filter((g) => g[0]?.subject.pinned === true);
  if (pinned.length === 0 || pinned.length === groups.length) {
    return rows;
  }
  return [...pinned, ...groups.filter((g) => g[0]?.subject.pinned !== true)].flat();
}

/** Reorder `state.tabs` by the pin partition: a RENDERING rule over the stored
 *  order, so an unpin leaves the tab where it was. In the ARRAY rather than the
 *  render, because a drop and the keyboard arrows both read DOM order back as the
 *  truth, and a render-time sort would make them disagree with what is stored. */
function applyPinOrder(): void {
  state.tabs = [...pinPartition(state.tabs)];
}

/** Expand a top-level order into the exact set `reorder_tabs` demands: every open tab once, each
 *  parent followed by its descendant tree. A drag reads back top-level ids only, so without this
 *  every drop on a strip holding a sub-tab would be a 409. */
function expandOrder(order: readonly string[]): string[] {
  const remaining = new Map(state.tabs.map((t) => [t.subject.id, t]));
  const out: string[] = [];
  const take = (id: string): void => {
    if (!remaining.has(id)) {
      return;
    }
    remaining.delete(id);
    out.push(id);
    for (const c of childrenOf(id)) {
      take(c.subject.id);
    }
  };
  for (const id of order) {
    take(id);
  }
  // Anything the gesture did not name keeps its position at the end. Parents go
  // through take() so their children follow them rather than trailing the strip.
  for (const t of [...remaining.values()]) {
    take(t.subject.id);
  }
  return out;
}

// --- The sync target ---

/** Adopt a whole snapshot: the boot read, and the answer to a gap or a 409.
 *
 *  A snapshot is the COMPLETE set at a version tabs-sync already checked (`readList` discards
 *  anything older), so a tab absent from it really is closed; a DELTA's `order` never implies
 *  closure. Surviving rows are REUSED, so a name override, a dot and the spec ride through a
 *  re-list and the lazy singleton imports are not re-entered. */
function reset(subjects: readonly TabSubject[]): void {
  const before = new Map(state.tabs.map((t) => [t.subject.id, t]));
  const next: TabRow[] = [];
  for (const subject of subjects) {
    const existing = before.get(subject.id);
    if (existing === undefined) {
      next.push(buildRow(subject));
      continue;
    }
    before.delete(subject.id);
    existing.subject = subject;
    next.push(existing);
  }
  state.tabs = next;
  if (next.length > 0) {
    internal.everOpened = true;
  }
  // An authoritative list is a complete set, so a dropped row's local teardown runs here. A row this
  // device optimistically removed is no longer in the projection, so its machine op owns the teardown.
  for (const row of before.values()) {
    forgetRow(row);
    tearDown(row);
  }
  applyPinOrder();

  // BOOT (nothing active): point at this screen's last tab, or the first, WITHOUT running `onShow`;
  // app.ts performs the one boot activation, so a re-list never re-fetches a view's content. A LATER
  // re-list that dropped the active tab hands over properly; `local: false`, so a set this device did
  // not author never respawns a chat here.
  if (state.active === "") {
    const saved = activeView();
    state.active = saved !== "" && hasRow(saved) ? saved : (state.tabs[0]?.subject.id ?? "");
    if (state.active !== "") {
      // The one place a real id reaches `state.active` without activateTab, so the MRU invariant is
      // restored by hand.
      noteActivation(state.active);
    }
    emit();
    return;
  }
  if (!hasRow(state.active)) {
    emit();
    activateSuccessor(false);
    return;
  }
  emit();
}

/** Apply ONE committed mutation, already version-checked by tabs-sync.
 *
 *  `local` (this frame echoes our own dispatch) gates only the empty-strip respawn. A locally closed
 *  tab already left the projection, so its teardown runs from confirmClose; what lands here is a
 *  remote close, torn down as the row leaves. `changed` is an upsert by id, `removed_ids` the ONLY
 *  statement of closure, `order` a permutation. */
function apply(delta: TabsChangedPayload, local: boolean): void {
  const changed = delta.changed;
  if (changed !== undefined) {
    upsertSubject(changed);
  }

  const removed = delta.removed_ids ?? [];
  let lostActive = false;
  for (const id of removed) {
    const at = state.tabs.findIndex((t) => t.subject.id === id);
    if (at < 0) {
      continue;
    }
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
    const row = state.tabs[at]!;
    // REMOVE, then tear down: a teardown must observe a state the tab already left, or a hook that
    // closes its own tab recurses until the stack dies.
    state.tabs.splice(at, 1);
    if (state.active === id) {
      lostActive = true;
    }
    forgetRow(row);
    tearDown(row);
  }

  const order = delta.order;
  if (order !== undefined) {
    // A PERMUTATION: an id the order does not name sorts LAST and is never closed.
    state.tabs = permute(state.tabs, (t) => t.subject.id, order);
    applyPinOrder();
  }

  if (lostActive) {
    // An active view naming no open tab hands over to the most recently visited
    // tab still open, which is the same rule every other removal path applies.
    activateSuccessor(local);
  }
  emit();
}

/** Upsert one subject: the frame-apply path for `changed` and adoptSubject's paint. One code path,
 *  so the echo of an adopted open changes nothing.
 *
 *  An existing row's SUBJECT is replaced wholesale while the spec, name override and dot stay.
 *  Nothing here may re-materialize: `owns` is immutable after open and every reader of the parent
 *  takes it from the subject, so the spec cannot go stale. */
function upsertSubject(subject: TabSubject): void {
  const existing = rowOfID(subject.id);
  if (existing === undefined) {
    insertRow(buildRow(subject));
    return;
  }
  existing.subject = subject;
  existing.name = nameFor(subject, existing.spec);
  applyPinOrder();
}

/** Run a departed row's local teardown, if it has one and owns what it shows. `owns: false` tears
 *  down nothing: dismissing a view must not kill the work it watched. The teardown is client-local
 *  and identical whoever closed the tab. */
function tearDown(row: TabRow): void {
  if (!row.spec.owns) {
    return;
  }
  row.spec.onClose?.();
}

/** Whether the projection holds a tab with this id. */
function hasRow(id: string): boolean {
  return state.tabs.some((t) => t.subject.id === id);
}

const target: TabsTarget = { reset, apply };
registerTabsTarget(target);

// --- Activation (this device's alone) ---

/** Dismiss the phone drawer, because the reader asked for a DESTINATION. Belongs to a navigation
 *  gesture, not a view change: the view effect re-runs on every projection mutation, and a close
 *  must leave the drawer put. A state no-op on desktop. */
function revealActiveView(): void {
  $.sidebar.classList.remove("open");
}

/** Ask the freshness question for one row and spend the answer. THE one caller of
 *  `viewStale`, so all ten kinds are gated in one place and none can answer it by
 *  accident from its own `onShow`. */
function refreshRow(row: TabRow): void {
  if (viewStale(row.subject.kind, row.subject.ref)) {
    row.spec.refresh();
  }
}

/** The active row's refresh, for the two invalidation triggers. A no-op on an
 *  empty strip and mid-swap, where there is no view on screen to refresh and the
 *  successor activation runs its own gate a moment later. Emits nothing, pushes no
 *  route and touches no state. */
export function refreshActiveView(): void {
  const row = rowOfID(state.active);
  if (row !== undefined) {
    refreshRow(row);
  }
}

/** Activate an existing tab, and reveal it: this is the reader picking a
 *  destination. Every gesture door lands here — a row tap, Enter on a focused
 *  row, and `openTab`, which is what the five singleton buttons dispatch. */
export function activateTab(id: string): void {
  revealActiveView();
  activateTabQuietly(id);
}

/** Activate a tab WITHOUT dismissing the drawer: the app moved the view, the
 *  reader did not. Two callers, and both are consequences rather than gestures —
 *  a close handing the view to its successor, and a rollback restoring the tab a
 *  refused close had already taken away. Boot's one activation goes through it
 *  too, where the drawer is closed anyway and there is nothing to reveal. */
function activateTabQuietly(id: string): void {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  if (state.active === id) {
    return;
  }
  state.active = id;
  setActiveView(id);
  noteActivation(id);
  emit();
  row.spec.onShow?.();
  refreshRow(row);
  callbacks.onActivate?.(id);
}

/** Hand the active view to a departed tab's successor: the most recently visited open tab, else the
 *  first. Recency, not position: a pin or a sub-tab routinely puts a stranger first.
 *
 *  `local` gates the empty-strip respawn: a chat minted for ANOTHER device's close propagates back
 *  to every device, the loop that minted a chat every 1.5s. */
function activateSuccessor(local: boolean): void {
  const recent = mostRecentOpenTab();
  const next = recent !== "" ? recent : (state.tabs[0]?.subject.id ?? "");
  if (next !== "") {
    state.active = "";
    activateTabQuietly(next);
    return;
  }
  state.active = "";
  setActiveView("");
  if (local) {
    scheduleEmpty();
  }
}

/** Point the strip at the tab this SCREEN was last on, and run its `onShow`: boot's one activation,
 *  after `listTabs()`. Separate from `reset`, which also runs on every re-list where a re-fetch
 *  would be a round trip for nothing. */
export function activateRestoredTab(): void {
  const id = state.active;
  if (id === "") {
    return;
  }
  // Cleared so the already-active guard does not swallow the onShow that is the
  // whole point of this call.
  state.active = "";
  activateTabQuietly(id);
}

// --- Mutations: every one is a dispatch ---

/** Paint one subject NOW from a command response, through the frame path's upsert, so the echo
 *  frame re-animates nothing. The response carries what the server COMMITTED, so an open resolves
 *  with its row present whether or not the frame arrives; tabs-sync holds the same subject so a
 *  racing re-list cannot unpaint it. `name` labels a subject whose ref was server-minted. */
export function adoptSubject(subject: TabSubject, name?: string): void {
  if (name !== undefined && name !== "") {
    nameOverrides.set(subjectKey(subject.kind, subject.ref), name);
  }
  upsertSubject(subject);
  emit();
}

/** What an open resolved to. `not-found` means the subject is GONE (a retention-off close deleted
 *  the chat), a different fact from a network that failed to answer. */
export type OpenTabOutcome = "opened" | "not-found" | "failed";

/** Open a tab for something that already exists, and activate it. Resolves with the row in the
 *  projection, adopted from the RESPONSE, so no frame round trip sits in the gesture's path.
 *
 *  Never rejects: a refused open leaves the strip as it was and the action framework has raised its
 *  toast. A 404 answers `not-found`, any other failure `failed`. */
export async function openTab(args: OpenTabArgs): Promise<OpenTabOutcome> {
  const ref = args.ref ?? "";
  if (args.name !== undefined && args.name !== "") {
    // BEFORE the dispatch, so a frame that beats the response builds the row
    // with the right label rather than rendering the factory's placeholder for a
    // beat and then snapping.
    nameOverrides.set(subjectKey(args.kind, ref), args.name);
  }
  const opID = newOpID();
  markLocalOp(opID);
  beginAdopt(opID);
  const outcome = await openTabCommand.dispatch({
    kind: args.kind,
    ref,
    parent: args.parent ?? "",
    owns: args.owns ?? true,
    opID,
  }).outcome;
  if (outcome.status !== "success" || outcome.value === null) {
    // The server answered an error (nothing committed) or the dispatch gave up.
    // Nothing was painted, so the failure transition just retires the op.
    opFailed(opID);
    return outcome.status === "error" && outcome.error.status === 404 ? "not-found" : "failed";
  }
  const reply = outcome.value;
  adoptSubject(reply.subject);
  adoptCommitted(opID, reply.subject, reply.version, reply.created);
  if (args.activate !== false) {
    activateTab(reply.subject.id);
  }
  return "opened";
}

// --- The optimistic close ---

/** Everything a close gesture must be able to undo, captured before the strip
 *  changes. The rows are the LIVE TabRow objects — spec, name and dot ride
 *  along — and the projection retains nothing else about a departed tab. */
interface CapturedClose {
  /** The closed subtree in projection order, parent first. */
  rows: TabRow[];
  /** The parent's index in the projection at capture, so a rollback restores
   *  the subtree in place rather than at the end. */
  at: number;
  /** The name overrides the gesture's forgetRow dropped, keyed by subject. */
  overrides: Map<string, string>;
  /** The MRU entries the gesture's forgetRow dropped, each ANCHORED on the id ahead of it
   *  (`""` = the head) and ordered by original slot, so a removed anchor is back before its
   *  follower. Exact behind a SURVIVING anchor; a vanished one appends, understating recency. */
  history: { id: string; after: string }[];
  /** The active tab, when it was inside the subtree; "" otherwise. What a
   *  rollback re-activates. */
  activeTabID: string;
  /** The store's active chat at gesture time, when the gesture moved it; ""
   *  otherwise. The rollback's fallback when no tab re-activation happens. */
  storeActive: string;
  /** Set when the server ANSWERED an error: the framework's toast already ran,
   *  so the rollback stays quiet. A verify-settled restore has no toast of its
   *  own and says "not confirmed" instead. */
  refused: boolean;
}

/** Raises a notice about a tab's subject: a chat id, `run:<id>`, or "" for none.
 *  `name` is the row's name at the gesture. The composition root wires the
 *  subject-aware door; that module reads this one, so it cannot be imported. */
type TabNotice = (subject: string, name: string, message: string, level: "info" | "error") => void;

const bareTabNotice: TabNotice = (_subject, _name, message, level) => {
  if (level === "error") {
    toastError(message);
  } else {
    info(message);
  }
};

let tabNotice = bareTabNotice;

export function registerTabNotice(fn: TabNotice): void {
  tabNotice = fn;
}

function noticeSubjectOf(row: TabRow): string {
  switch (row.subject.kind) {
    case "chat":
      return row.subject.ref;
    case "run":
      return `run:${row.subject.ref}`;
    default:
      return "";
  }
}

function notifyAboutRow(row: TabRow | undefined, message: string, level: "info" | "error"): void {
  if (row === undefined) {
    tabNotice("", "", message, level);
    return;
  }
  tabNotice(noticeSubjectOf(row), row.name, message, level);
}

/** A row's whole subtree in projection order, the walk expandOrder's take() runs. The server
 *  refuses a parent cycle (ErrCycle). */
function subtreeRows(row: TabRow): TabRow[] {
  const out: TabRow[] = [];
  const take = (r: TabRow): void => {
    out.push(r);
    for (const c of childrenOf(r.subject.id)) {
      take(c);
    }
  };
  take(row);
  return out;
}

function captureClose(row: TabRow): CapturedClose {
  const rows = subtreeRows(row);
  const ids = new Set(rows.map((r) => r.subject.id));
  const overrides = new Map<string, string>();
  for (const r of rows) {
    const key = subjectKey(r.subject.kind, r.subject.ref);
    const name = nameOverrides.get(key);
    if (name !== undefined) {
      overrides.set(key, name);
    }
  }
  // Walked in HISTORY order, not projection order: that is what makes the array
  // ascending by slot, so an ascending restore puts a removed anchor back first.
  const history: { id: string; after: string }[] = [];
  for (const [at, id] of activationHistory.entries()) {
    if (ids.has(id)) {
      history.push({ id, after: activationHistory[at - 1] ?? "" });
    }
  }
  return {
    rows,
    at: state.tabs.findIndex((t) => t.subject.id === row.subject.id),
    overrides,
    history,
    activeTabID: ids.has(state.active) ? state.active : "",
    storeActive: "",
    refused: false,
  };
}

/** The REVERSIBLE half of a close, applied at gesture time: the subtree leaves the projection,
 *  row-scoped forgets run, activation falls back, and the store's active pointer moves with it.
 *  Nothing here destroys state a rollback cannot restore; that is confirmClose's and the server's. */
function applyGestureRemoval(c: CapturedClose): void {
  const ids = new Set(c.rows.map((r) => r.subject.id));
  state.tabs = state.tabs.filter((t) => !ids.has(t.subject.id));
  for (const r of c.rows) {
    forgetRow(r);
  }
  if (c.activeTabID !== "") {
    // Reversible: a chat successor's own activation moves the store pointer,
    // and the empty strip renders the empty-state surface (the view effect).
    // scheduleEmpty defers itself while this remove is pending.
    activateSuccessor(true);
  }
  syncStoreActive(c);
  emit();
}

/** Move the store's active chat off a chat this close made tabless, as removeChat would at
 *  teardown. Run EARLY because the store row survives to onConfirm, and getActiveId() readers must
 *  never see a closed chat in the window. */
function syncStoreActive(c: CapturedClose): void {
  const closedRefs = new Set(
    c.rows
      .filter((r) => r.subject.kind === "chat" && tabIdFor("chat", r.subject.ref) === "")
      .map((r) => r.subject.ref),
  );
  const active = getActiveId();
  if (active === "" || !closedRefs.has(active)) {
    return;
  }
  c.storeActive = active;
  // The next chat that still HAS a tab, in store order — the tab set is the
  // truth of "still around" now that closed rows linger until confirmation.
  const next =
    getSessions().find((s) => !closedRefs.has(s.id) && tabIdFor("chat", s.id) !== "")?.id ?? "";
  setActive(next);
  retargetComposer(next);
}

/** The deferred client-local teardown, run once per closed row when the close is CONFIRMED
 *  (tabs-sync's machine owns by what). An open row with the same (kind, ref) means the subject was
 *  reopened inside the window, so the subject-scoped teardown is SKIPPED. */
function confirmClose(c: CapturedClose): void {
  // Children BEFORE parents: c.rows is a pre-order walk, so the reverse never tears a child down
  // against a gone parent, matching the server's deepest-first removal list.
  for (const r of [...c.rows].reverse()) {
    if (tabIdFor(r.subject.kind, r.subject.ref) !== "") {
      continue;
    }
    tearDown(r);
  }
}

/** Restore a captured subtree: the definitive-failure rollback and the verify-settled restore. A
 *  row reopened under a NEW id keeps the reopened row; a restored `pinned` may lag one frame. */
function rollbackClose(c: CapturedClose): void {
  for (const [key, name] of c.overrides) {
    nameOverrides.set(key, name);
  }
  const restorable = c.rows.filter(
    (r) => !hasRow(r.subject.id) && tabIdFor(r.subject.kind, r.subject.ref) === "",
  );
  if (restorable.length > 0) {
    state.tabs.splice(Math.min(Math.max(c.at, 0), state.tabs.length), 0, ...restorable);
    internal.everOpened = true;
    applyPinOrder();
  }
  // `hasRow`, not the rows THIS call spliced: the verify-settled path's reset already
  // rebuilt the row, and it IS the no-closed-id invariant: a reopen under a new id is rowless.
  for (const { id, after } of c.history) {
    if (!hasRow(id) || activationHistory.includes(id)) {
      continue;
    }
    restoreActivation(id, after);
  }
  // Empty-state composer text lives only in the box, so it is filed as the restored chat's draft
  // BEFORE re-activation repaints the box: restoreComposerState would CLEAR a draftless chat's box.
  const restoredChat =
    c.rows.find((r) => r.subject.id === c.activeTabID && r.subject.kind === "chat")?.subject.ref ??
    c.storeActive;
  if (restoredChat !== "" && state.active === "" && getActiveId() === "") {
    const typed = $.promptInput.value;
    if (typed !== "") {
      restoreFailedSend(restoredChat, typed);
    }
  }
  emit();
  if (c.activeTabID !== "" && hasRow(c.activeTabID)) {
    activateTabQuietly(c.activeTabID);
  } else if (c.storeActive !== "" && getActiveId() === "") {
    setActive(c.storeActive);
    retargetComposer(c.storeActive);
  }
  if (!c.refused) {
    notifyAboutRow(c.rows[0], "Close not confirmed, so the tab was restored.", "info");
  }
}

/** Close a tab and its descendants as ONE server-side mutation, applied optimistically: reversible
 *  effects at the gesture, teardown on confirmation. A committed response feeds the pending-op
 *  machine, a DEFINITIVE error rolls back, a TIMEOUT moves to `verifying` with the removal applied.
 *  Closing an id that is not open is not an error: two devices can close one tab. */
export async function closeTab(id: string): Promise<void> {
  const row = rowOfID(id);
  const opID = newOpID();
  markLocalOp(opID);
  if (row === undefined) {
    // Not in this projection: no pending op, just the dispatch. An unknown id is SUCCESS server-side,
    // so a definitive error here is a real refusal.
    const lone = await closeTabCommand.dispatch({ id, opID }).outcome;
    if (lone.status === "error" && lone.error.code !== "timeout") {
      notifyAboutRow(undefined, "Could not close that tab", "error");
    }
    return;
  }
  const captured = captureClose(row);
  beginRemove(opID, {
    id,
    capturedTabIDs: captured.rows.map((r) => r.subject.id),
    onConfirm: () => {
      confirmClose(captured);
    },
    rollback: () => {
      rollbackClose(captured);
    },
  });
  applyGestureRemoval(captured);
  const outcome = await closeTabCommand.dispatch({ id, opID }).outcome;
  if (outcome.status === "success" && outcome.value !== null) {
    removeCommitted(opID, outcome.value.closed, outcome.value.version);
    return;
  }
  if (outcome.status === "error" && outcome.error.code !== "timeout") {
    // The server ANSWERED an error: nothing committed, so the restore is
    // honest — and the toast is this branch's, because the action itself stays
    // quiet (its other failure shape is inconclusive and must not claim one).
    captured.refused = true;
    notifyAboutRow(captured.rows[0], "Could not close that tab", "error");
    opFailed(opID);
    return;
  }
  // No answer — the definition-level CLOSE_CONFIRM_MS elapsed (or the dispatch
  // was aborted): the machine verifies rather than guessing in either
  // direction.
  opTimedOut(opID);
}

/** Pin or unpin a top-level tab.
 *
 *  Refused locally for a SUB-TAB before it costs a round trip: its position is
 *  its parent's, exactly as with drag. */
export async function setTabPinned(id: string, pinned: boolean): Promise<void> {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  if (row.subject.parent !== "" || row.subject.pinned === pinned) {
    return;
  }
  const opID = newOpID();
  markLocalOp(opID);
  await pinTabCommand.dispatch({ id, pinned, opID });
}

/** Hang an open tab under an open chat tab: the one mutation that reassigns `parent`. Resolves true
 *  when the tab now sits under `parent`. The returned subject is adopted as an open's is; on an
 *  unchanged parent no frame comes. */
export async function setTabParent(id: string, parent: string): Promise<boolean> {
  const row = rowOfID(id);
  if (row === undefined || parent === "") {
    return false;
  }
  if (row.subject.parent === parent) {
    return true;
  }
  const opID = newOpID();
  markLocalOp(opID);
  const outcome = await reparentTabCommand.dispatch({ id, parent, opID }).outcome;
  if (outcome.status !== "success") {
    return false;
  }
  upsertSubject(outcome.value);
  emit();
  return true;
}

const idOfRow = (t: TabRow): string => t.subject.id;

/** Publish a top-level order a drop or the menu committed. Expanded and pin-partitioned BEFORE
 *  shown or sent, so the server gets what every device will render. A 409 rolls back and re-lists,
 *  never re-sends: the set moved under the gesture. Answers the applied order, or null when the
 *  partition undid the move. */
function publishReorder(order: readonly string[]): readonly string[] | null {
  const prior = state.tabs.map(idOfRow);
  const next = pinPartition(permute(state.tabs, idOfRow, expandOrder(order))).map(idOfRow);
  if (next.length === prior.length && next.every((id, i) => id === prior[i])) {
    return null;
  }
  const opID = newOpID();
  markLocalOp(opID);
  beginReorder(opID, {
    order: next,
    // The captured order with any NEWER pending reorder re-applied on top, so
    // rolling back an older drop never undoes a newer one.
    rollback: () => {
      state.tabs = permute(state.tabs, idOfRow, overlayOrder(prior));
      applyPinOrder();
      emit();
    },
  });
  state.tabs = permute(state.tabs, idOfRow, next);
  emit();
  void (async () => {
    const outcome = await reorderTabsCommand.dispatch({ order: next, opID });
    if (outcome === REORDER_STALE) {
      opFailed(opID);
      await listTabs();
    } else if (outcome === null) {
      opFailed(opID);
    } else {
      reorderCommitted(opID, outcome.version);
    }
  })();
  return tabGroups().map((g) => g[0]?.subject.id ?? "");
}

setReorderCallback(publishReorder);
setReprojectCallback(renderDOM);
// A lifted hold's release goes to the list, which holds the pointer capture, so it
// never reaches the row's own activation listener.
setTapCallback(activateTab);

/** Where the menu's Move up / Move down would take a tab: its group swapped with the
 *  adjacent group in the SAME pin partition. Null for a sub-tab (its position is its
 *  parent's), at either end, and at the partition boundary. */
function moveTarget(
  id: string,
  delta: -1 | 1,
): { groups: TabRow[][]; at: number; to: number } | null {
  const groups = tabGroups();
  const at = groups.findIndex((g) => g[0]?.subject.id === id);
  const head = groups[at]?.[0];
  const neighbour = groups[at + delta]?.[0];
  if (head === undefined || neighbour === undefined) {
    return null;
  }
  if (neighbour.subject.pinned !== head.subject.pinned) {
    return null;
  }
  return { groups, at, to: at + delta };
}

function canMoveTab(id: string, delta: -1 | 1): boolean {
  return moveTarget(id, delta) !== null;
}

/** Move a tab and its whole subtree one group up or down, committing through the
 *  same path a drop takes. */
function moveTab(id: string, delta: -1 | 1): void {
  const move = moveTarget(id, delta);
  if (move === null) {
    return;
  }
  const groups = [...move.groups];
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- both indices checked by moveTarget
  [groups[move.at], groups[move.to]] = [groups[move.to]!, groups[move.at]!];
  const applied = publishReorder(groups.map((g) => g[0]?.subject.id ?? ""));
  const at = applied?.indexOf(id) ?? -1;
  const row = rowOfID(id);
  if (row !== undefined && at >= 0) {
    announce(`Moved ${row.name} to position ${String(at + 1)}`);
  }
}

// --- Local writers ---

/** Override a tab's label. Recorded against the SUBJECT as well as the row, so a re-list (which
 *  rebuilds rows from the factory's derived default) keeps it. */
export function renameTab(id: string, name: string): void {
  const row = rowOfID(id);
  if (row === undefined || row.name === name) {
    return;
  }
  nameOverrides.set(subjectKey(row.subject.kind, row.subject.ref), name);
  row.name = name;
  emit();
}

/** What a kind's OUTCOME states are about. The dot is `aria-hidden`, so the phrase is a
 *  screen-reader user's only channel, and three producers write outcomes about three subjects.
 *  The last six kinds have no producer; their odd nouns are a signal to whoever adds one. */
const DOT_SUBJECT: Readonly<Record<TabKind, string>> = {
  chat: "turn",
  run: "workflow run",
  subagent: "subagent",
  editor: "file",
  settings: "settings",
  git: "git",
  files: "file browser",
  history: "history",
  docs: "docs",
  spec: "spec",
  web: "preview",
};

/** The five states that say nothing about a subject, so one wording serves every
 *  kind. Keyed by the states that are NOT subject-bearing, so a new TabDotStatus
 *  member has to be classified here or in `dotPhrase` rather than shipping
 *  unnamed. */
const NEUTRAL_PHRASE: Readonly<Record<Exclude<TabDotStatus, "done" | "failed">, string>> = {
  idle: "idle",
  working: "working",
  waiting: "waiting for you",
  input: "needs a decision",
  dirty: "unsaved changes",
};

/** The ONE resolver, so the tooltip and the announced word cannot drift per kind. A phrase claims
 *  only what its producer supports (`store.ts` `outcomeLatch`). `since` reaches only the two
 *  OUTCOMES: the other states describe NOW. */
function dotPhrase(kind: TabKind, status: TabDotStatus, since?: number): string {
  if (status === "done") {
    return withAge(`${DOT_SUBJECT[kind]} finished`, since);
  }
  if (status === "failed") {
    return withAge(`${DOT_SUBJECT[kind]} failed`, since);
  }
  return NEUTRAL_PHRASE[status];
}

/** How long ago a finished thing finished. It does NOT tick: a 1s interval over a sidebar of
 *  finished chats is a wakeup cost, and both surfaces this feeds are read on demand. */
function withAge(phrase: string, since?: number): string {
  return since === undefined ? phrase : `${phrase} · ${relativeTime(since)}`;
}

const CLS_DOT = "tab-status-dot";
const CLS_DOT_SR = "tab-status-sr";
const CLS_RUN_DOT = "tab-run-dot";
const CLS_RUN_DOT_SR = "tab-run-sr";

function elementOf(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`);
}

/** Paint one row's hover tooltip: its full title. Guarded because renderDOM calls
 *  this per row per emit; an empty name REMOVES the attribute rather than leaving
 *  `[data-tooltip]` with nothing behind it. */
function paintTooltip(node: HTMLElement, name: string): void {
  if (name === "") {
    node.removeAttribute("data-tooltip");
    return;
  }
  if (node.dataset["tooltip"] !== name) {
    node.dataset["tooltip"] = name;
  }
}

/** Paint one tab's dot: the CSS attribute, the tooltip and the screen-reader word. The word is a
 *  SEPARATE element after `.tab-name`: the accessible name follows DOM order, so inside the leading
 *  dot it would announce before the title. An empty status removes the attribute, so
 *  `[data-status]` alone is the reveal condition. */
function paintDot(
  node: HTMLElement,
  kind: TabKind,
  status: TabDotStatus | "",
  since?: number,
): void {
  const dot = node.querySelector<HTMLElement>(`.${CLS_DOT}`);
  const sr = node.querySelector<HTMLElement>(`.${CLS_DOT_SR}`);
  if (dot === null || sr === null) {
    return;
  }
  if (status === "") {
    dot.removeAttribute("data-status");
    dot.removeAttribute("data-tooltip");
    sr.textContent = "";
    return;
  }
  const phrase = dotPhrase(kind, status, since);
  dot.dataset["status"] = status;
  dot.dataset["tooltip"] = phrase;
  sr.textContent = `, ${phrase}`;
}

/** Set a chat tab's activity dot, and optionally when its state became true; `tabStatusFor`
 *  (store.ts) owns the precedence. Recorded on the ROW first, so a rebuilt row starts from the real
 *  state. Deliberately no `emit()`: a dot is not structural and must not queue a re-render. */
export function setTabStatus(id: string, status: TabDotStatus | "", since?: number): void {
  // A row that left the projection returns early even while its element plays its exit: there is no
  // honest kind to phrase it with.
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  recordDotStatus(row, status, since);
  const node = elementOf(id);
  if (node === null) {
    return;
  }
  // From the ROW rather than the argument, so a since-less repaint of an unchanged
  // state keeps the age the row still holds instead of blanking the tooltip.
  paintDot(node, row.subject.kind, status, row.dotSince);
}

/** The workflow mark's phrase and tooltip, one string as `dotPhrase` is for the dot. Nouns from
 *  `DOT_SUBJECT`, state words from `NEUTRAL_PHRASE`; the COUNT is why it is not `dotPhrase`: N runs
 *  fold onto one mark. */
function runDotPhrase(status: TabRunDotStatus, tally: TabRunTally): string {
  const noun = tally.total === 1 ? DOT_SUBJECT.run : `${DOT_SUBJECT.run}s`;
  const word = NEUTRAL_PHRASE[status];
  // A single run needs no second number: "1 workflow run, 1 working" states the
  // same fact twice.
  return tally.total <= 1
    ? `${tally.total} ${noun}, ${word}`
    : `${tally.total} ${noun}, ${tally[status]} ${word}`;
}

/** Paint one chat row's workflow mark: the CSS attribute, tooltip and phrase, as `paintDot` does.
 *  The phrase span sits after the dot's own so both read out behind the title. An empty status
 *  REMOVES the attribute, and the mark then takes no space. A no-op on non-chat rows. */
function paintRunDot(node: HTMLElement, status: TabRunDotStatus | "", tally: TabRunTally): void {
  const mark = node.querySelector<HTMLElement>(`.${CLS_RUN_DOT}`);
  const sr = node.querySelector<HTMLElement>(`.${CLS_RUN_DOT_SR}`);
  if (mark === null || sr === null) {
    return;
  }
  if (status === "") {
    mark.removeAttribute("data-status");
    mark.removeAttribute("data-tooltip");
    sr.textContent = "";
    return;
  }
  const phrase = runDotPhrase(status, tally);
  mark.dataset["status"] = status;
  mark.dataset["tooltip"] = phrase;
  sr.textContent = `, ${phrase}`;
}

/** Set a chat tab's WORKFLOW mark; `chat-run-dots.ts` derives the fold. `tally` rides beside
 *  `status` because the status is what the mark PAINTS and the tally what its phrase SAYS.
 *
 *  Recorded on the ROW first, as `setTabStatus` does. No `emit()`, and no `dotVersion` bump: the
 *  attention fold reads `setTabStatus`'s dot, and whether it should read this chat-row mark is an
 *  open product decision. */
export function setTabRunStatus(
  id: string,
  status: TabRunDotStatus | "",
  tally: TabRunTally,
): void {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  if (status === "") {
    delete row.runDot;
  } else {
    row.runDot = { status, tally };
  }
  const node = elementOf(id);
  if (node === null) {
    return;
  }
  paintRunDot(node, status, tally);
}

/** Mark an editor tab as having unsaved changes. Reuses .tab-status-dot on the ONE attribute
 *  setTabStatus writes, so the two are mutually exclusive by construction. */
export function setTabDirty(id: string, dirty: boolean): void {
  setTabStatus(id, dirty ? "dirty" : "");
}

/** Park a dot state on its row. "" means no state, which is an ABSENT field
 *  rather than an empty string, so `createTabEl` can tell "nothing was ever
 *  painted" from "painted, then cleared" with one `?? default`. */
function recordDotStatus(row: TabRow, status: TabDotStatus | "", since?: number): void {
  const before = row.dotStatus;
  if (status === "") {
    delete row.dotStatus;
  } else {
    row.dotStatus = status;
  }
  // The age belongs to the STATE, so a new state drops the previous one's. A
  // since-less repaint of the SAME state keeps it: `turn-teardown.ts` repaints `done`
  // with no `since` and the chat row effect resupplies it a microtask later.
  if (status !== before) {
    delete row.dotSince;
  }
  if (since !== undefined) {
    row.dotSince = since;
  }
  // Only a CHANGED dot moves the attention surfaces, so the store effect's sweep over every open chat
  // wakes the fold once. The PAINT is unguarded: a rebuilt row still needs the unchanged state.
  if (before !== row.dotStatus) {
    dotVersion.value = dotVersion.peek() + 1;
  }
}

// --- Lookups ---

/** Whether a tab is open for this subject. Keyed by `(kind, ref)`: ids are opaque and
 *  server-minted. A singleton's ref is empty. */
export function hasTab(kind: TabKind, ref = ""): boolean {
  return tabIdFor(kind, ref) !== "";
}

/** The open tab's id for this subject, or "" when none is open: the one lookup from a chat id,
 *  path or run id to an id-keyed writer. */
export function tabIdFor(kind: TabKind, ref = ""): string {
  return state.tabs.find((t) => t.subject.kind === kind && t.subject.ref === ref)?.subject.id ?? "";
}

/** The open tab's id for the subject a route names, or "": what makes BACK redirect rather than
 *  re-open a tab the reader closed. Files routes resolve through `filesTabForRoute`. */
export function tabIdForRoute(route: Route): string {
  if (route.kind === "files") {
    return filesTabForRoute(route.path).id;
  }
  const { kind, ref } = subjectForRoute(route);
  return tabIdFor(kind, ref);
}

export function getActiveTabId(): string {
  return state.active;
}

/** Route of the currently active tab, or null when no tab is active. Used by
 *  app.ts to canonicalize the URL to a restored non-chat tab. */
export function getActiveTabRoute(): Route | null {
  return rowOfID(state.active)?.spec.route ?? null;
}

/** Kind of the active tab, or null. A key binding scoped BY VIEW reads this; the SUBJECT's kind,
 *  not the route's (an editor tab's route kind is "file"). */
export function getActiveTabKind(): TabKind | null {
  // Subscribe: the toolbar's find affordance derives from the tab SET inside an effect.
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  return rowOfID(state.active)?.subject.kind ?? null;
}

/** The ACTIVE tab's chat ref, or "". The projection's answer to create-vs-send: the store keeps a
 *  closed chat's row until confirmation, so this reads what the strip shows. */
export function activeChatRef(): string {
  const row = rowOfID(state.active);
  return row?.subject.kind === "chat" ? row.subject.ref : "";
}

/** The chat ref of the tab `tabID` hangs under, or "". The only client answer after a reload: a
 *  FINISHED run has no lease, so `runChatID` answers "", while `TabSubject.Parent` is persisted and
 *  was filled from the run's lease at open (`Membership.fillRunParent`). It changes only through
 *  `setTabParent`, which adopts the new subject, so the answer tracks it. */
export function parentChatRef(tabID: string): string {
  const parentID = rowOfID(tabID)?.subject.parent ?? "";
  if (parentID === "") {
    return "";
  }
  const parent = rowOfID(parentID);
  return parent?.subject.kind === "chat" ? parent.subject.ref : "";
}

/** The chat refs with an open tab, deduplicated (an owning and a view tab can project one chat),
 *  as a TRACKED read: re-runs on projection mutations, never on dot writes. Consumers resolve with
 *  `tabIdFor`, so a duplicated ref paints its first tab. */
export function openChatRefs(): string[] {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  const refs: string[] = [];
  for (const t of state.tabs) {
    if (t.subject.kind === "chat" && !refs.includes(t.subject.ref)) {
      refs.push(t.subject.ref);
    }
  }
  return refs;
}

/** The subagent tabs' refs, as a TRACKED read for `subagent-dots.ts`: a delegate's row can arrive
 *  long after its invocation is resident. Refs carry `<chatID>/<subtaskID>`; an id says nothing.
 *  No dedupe: a subagent tab is always a view of exactly one `(kind, ref)`. */
export function openSubagentRefs(): string[] {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  return state.tabs.filter((t) => t.subject.kind === "subagent").map((t) => t.subject.ref);
}

/** The run tabs' refs, as a TRACKED read: an effect calling this re-runs when a run
 *  tab lands or leaves. EVERY run tab, sub-tab and top-level alike. REFS, because a
 *  ref is the workflow id the run store is keyed by; `tabIdFor` recovers the id. */
export function openRunRefs(): string[] {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  return state.tabs.filter((t) => t.subject.kind === "run").map((t) => t.subject.ref);
}

/** The spec tabs' refs, as a TRACKED read like `openRunRefs`. A ref is the
 *  workspace-relative spec directory the page fetches by and the `spec_changed`
 *  frame names. */
export function openSpecRefs(): string[] {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  stateVersion.value;
  return state.tabs.filter((t) => t.subject.kind === "spec").map((t) => t.subject.ref);
}

/** The open SUBJECTS in projection order, for `boot-snapshot.ts`. Server-minted, so a restored one
 *  resolves against the server's collection; spec and dot are rebuilt from it. */
export function openTabSubjects(): TabSubject[] {
  return state.tabs.map((t) => t.subject);
}

/** Paint a PROVISIONAL tab set, `boot-snapshot.ts`'s paint-time hint. Runs the same reset an
 *  authoritative snapshot does but advances NO version, so the boot's `listTabs()` answer is
 *  adopted over it and every row it does not name is torn down. */
export function paintProvisionalTabs(subjects: readonly TabSubject[]): void {
  reset(subjects);
}

/** The cue-bearing tabs and their dot states, for attention.ts's out-of-page fold. A pure
 *  projection read: unbuilt rows still count.
 *
 *  Only `chat` and `run` bear a cue; every `runStatusFor` value is a `CueStatus` member or one
 *  `isCueStatus` rejects. `owns` excludes chat VIEW tabs (one chat counted twice) and is scoped to
 *  chats because tab-materialize.ts hard-codes `owns: false` for run and subagent. Subagent asks are
 *  filed under the launching chat. The id is the TAB id, that module's one key vocabulary. */
export function cueCandidates(): { id: string; status: string }[] {
  return state.tabs
    .filter((t) => (t.subject.kind === "chat" && t.spec.owns) || t.subject.kind === "run")
    .map((t) => ({ id: t.subject.id, status: foldedDotStatus(t) }));
}

/** The dot states meaning a chat's own work is OVER: the only ones the settle probe may blank.
 *  `input` and `waiting` are the reader's business and keep their cue. */
const SETTLED_CUES: ReadonlySet<string> = new Set<TabDotStatus>(["done", "failed"]);

/** Whether a CHAT still holds outstanding work, or null when unregistered. INJECTED: the answer
 *  lives in `chat-settled.ts`, whose dependencies all reach back here. */
let chatSettledProbe: ((chatRef: string) => boolean) | null = null;

/** Register the settle probe. Unregistered, nothing is ever suppressed, which is
 *  exactly the behaviour before the probe existed. */
export function setChatSettledProbe(fn: (chatRef: string) => boolean): void {
  chatSettledProbe = fn;
}

/** What the out-of-page fold sees for one row: its own dot, unless a CHAT whose turn settled while
 *  work it launched still runs. `""`, not `idle`: attention.ts reads `""` as no information, while
 *  `idle` FORGETS the reader's acknowledgement. The probe is consulted LAST, so a row that can never
 *  be suppressed subscribes to nothing extra. */
function foldedDotStatus(row: TabRow): string {
  const status = row.dotStatus ?? "";
  if (row.subject.kind !== "chat" || !SETTLED_CUES.has(status)) {
    return status;
  }
  return chatSettledProbe !== null && !chatSettledProbe(row.subject.ref) ? "" : status;
}

/** Subscribe to every input of the attention fold; returns the disposer. TWO signals for two write
 *  paths: `stateVersion` for the tab SET and `dotVersion` for dot writes, which do not emit. The
 *  settle probe's reads are tracked through `cueCandidates`. NOT in `moduleEffects`: the caller
 *  owns the lifetime, so `_resetForTest` must not unsubscribe it. */
export function subscribeTabCues(fn: () => void): () => void {
  return effect(() => {
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    stateVersion.value; // subscribe: the tab set
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    dotVersion.value; // subscribe: any tab's dot
    fn();
  });
}

/** Register the notification for a tab leaving the projection. One slot, one
 *  consumer: attention.ts, which drops the chat's acknowledgement. */
export function setOnTabClosed(fn: (id: string) => void): void {
  callbacks.onClosed = fn;
}

// --- Empty-state timer ---

export function setOnEmpty(fn: () => void): void {
  callbacks.onEmpty = fn;
}

function scheduleEmpty(): void {
  if (internal.emptyTimer !== null) {
    clearTimeout(internal.emptyTimer);
  }
  // DEFERRED while any remove is pending or verifying: a respawned chat would race the rollback that
  // could bring the closed one back. The removes-settled notification re-arms this.
  if (removesPending()) {
    internal.emptyDeferred = true;
    return;
  }
  internal.emptyDeferred = false;
  // Longer than the closed row's exit animation (10-shell-app.css), so the respawned row is built
  // into an EMPTY strip instead of animating in beside the departing one.
  internal.emptyTimer = setTimeout(() => {
    internal.emptyTimer = null;
    callbacks.onEmpty?.();
  }, 500);
}

/** Re-arm (or drop) a deferred empty-state respawn once the last pending remove
 *  settles. A confirmation leaves the strip empty and the respawn proceeds; a
 *  rollback restored rows, so there is nothing empty to answer. */
function settleDeferredEmpty(): void {
  if (!internal.emptyDeferred) {
    return;
  }
  internal.emptyDeferred = false;
  if (state.tabs.length === 0 && state.active === "") {
    scheduleEmpty();
  }
}

// --- Module subscribers ---

/** The id last announced on BUS_TAB_CHANGED. Module state rather than a closure
 *  so _resetForTest can clear it with the rest. */
let lastAnnouncedTab = "";

function registerModuleSubscribers(): void {
  // The removes-settled notification, which is what un-defers scheduleEmpty.
  setOnRemovesSettled(settleDeferredEmpty);

  // Clear the empty timer (and any deferred respawn) on any activation.
  tabsEffect(() => {
    if (state.active !== "") {
      internal.emptyDeferred = false;
      if (internal.emptyTimer !== null) {
        clearTimeout(internal.emptyTimer);
        internal.emptyTimer = null;
      }
    }
  });

  // View / route sync.
  tabsEffect((s) => {
    if (s.tabs.length === 0 && s.active === "") {
      // The empty-state surface, only once a tab has EVER opened: at boot the HTML already shows the chat
      // view, while after a last-tab close the closed tab's view must not linger.
      if (internal.everOpened) {
        showEmptySurface();
      }
      return;
    }
    const active = rowOfID(s.active);
    if (active !== undefined) {
      showView(active);
    } else {
      syncSidebarButtons(null);
    }
    // Announce a REAL switch only: this effect re-runs on every projection mutation. Emitted here, not
    // in showView, whose DOM swap runs inside a view transition.
    if (s.active !== lastAnnouncedTab) {
      lastAnnouncedTab = s.active;
      emitBus(BUS_TAB_CHANGED, { to: s.active, kind: active?.subject.kind ?? null });
    }
  });

  // DOM rendering, guarded on "no tab has ever opened" rather than "the store is empty": closing the
  // LAST tab restores both initial values, and an empty-store guard skipped the render that removes
  // the closed row.
  tabsEffect(() => {
    if (!internal.everOpened) {
      return;
    }
    if (internal.renderQueued) {
      return;
    }
    internal.renderQueued = true;
    requestAnimationFrame(() => {
      internal.renderQueued = false;
      renderDOM();
    });
  });
}

// --- View / route (subscriber) ---

const ALL_VIEWS_SELECTOR = "[data-tab-view]";

/** Icon buttons that should show `.active` when their singleton tab is active.
 *  Non-singleton kinds (chat, editor, run) are never in this map. */
const ACTIVE_BTN: Readonly<Partial<Record<TabKind, () => HTMLButtonElement>>> = {
  settings: () => $.settingsBtn,
  git: () => $.gitBtn,
  files: () => $.filesBtn,
  history: () => $.historyBtn,
};

function syncSidebarButtons(activeKind: TabKind | null): void {
  for (const [kind, getter] of Object.entries(ACTIVE_BTN)) {
    getter().classList.toggle("active", kind === activeKind);
  }
}

function showView(row: TabRow): void {
  // Swap the view ONLY when it is not already the right one: this re-runs on every projection
  // mutation, and a re-swap replays the entry fade. Read from the DOM, not a cached selector, so a
  // second writer of these classes can never leave a view hidden.
  const target = document.querySelector(row.spec.view);
  const shown = [...document.querySelectorAll(ALL_VIEWS_SELECTOR)].filter(
    (n) => !(n as HTMLElement).classList.contains("hidden"),
  );
  if (shown.length !== 1 || shown[0] !== target) {
    swapViews(() => {
      for (const node of document.querySelectorAll(ALL_VIEWS_SELECTOR)) {
        (node as HTMLElement).classList.add("hidden");
      }
      target?.classList.remove("hidden");
      return target as HTMLElement | null;
    });
  }

  // The tab's name IS the page title; the kind lets the heading recover a subtitle (page-title.ts).
  setPageTitle(row.name, row.subject.kind);

  // The phone drawer is NOT dismissed here: this runs on every projection mutation, a close included.
  // `revealActiveView` belongs to the navigation gesture.

  syncSidebarButtons(row.subject.kind);

  pushRoute(row.spec.route);
}

/** Show the EMPTY-STATE surface: the chat view with no active chat, whose Send creates a fresh one.
 *  HIDES the other views without disposing anything, so a rollback finds state intact. No
 *  pushRoute: the settlement's own activation corrects the URL. */
function showEmptySurface(): void {
  const target = document.querySelector(TAB_VIEWS.chat);
  const shown = [...document.querySelectorAll(ALL_VIEWS_SELECTOR)].filter(
    (n) => !(n as HTMLElement).classList.contains("hidden"),
  );
  // Same swap guard as showView: this effect re-runs on every projection
  // mutation, and re-animating a view already on screen would fade content
  // that never left.
  if (shown.length !== 1 || shown[0] !== target) {
    swapViews(() => {
      for (const node of document.querySelectorAll(ALL_VIEWS_SELECTOR)) {
        (node as HTMLElement).classList.add("hidden");
      }
      target?.classList.remove("hidden");
      return target as HTMLElement | null;
    });
  }
  clearPageTitle();
  syncSidebarButtons(null);
}

function renderDOM(): void {
  const list = $.tabList;
  if (!list.hasAttribute("role")) {
    list.setAttribute("role", "tablist");
  }

  const existing = new Map<string, HTMLElement>();
  for (const node of [...list.children]) {
    const id = (node as HTMLElement).dataset["tabId"];
    if (id !== undefined) {
      existing.set(id, node as HTMLElement);
    }
  }

  const activeIDs = new Set(state.tabs.map((t) => t.subject.id));

  // Orphans exit animated: a sub-tab whose parent survives merges UP (`.exiting-merge`), everything
  // else swipes out (`.exiting`), so a parent and its subtree leave as one block. The parent is read
  // off the DOM because the row already left the projection.
  for (const [id, node] of existing) {
    if (!activeIDs.has(id)) {
      const parent = node.dataset["parentId"];
      node.classList.add("exiting");
      if (parent !== undefined && activeIDs.has(parent)) {
        node.classList.add("exiting-merge");
      }
      node.addEventListener(
        "animationend",
        () => {
          node.remove();
        },
        { once: true },
      );
      existing.delete(id);
    }
  }

  // Exiting elements are skipped for ordering. While a drag holds the strip the pointer owns the
  // order, so nothing is re-seated.
  const reseat = !dragOwnsStrip();
  let prev: HTMLElement | null = null;
  for (const row of state.tabs) {
    let node = existing.get(row.subject.id);
    if (node === undefined) {
      node = createTabEl(row);
      if (prev !== null) {
        prev.after(node);
      } else {
        list.prepend(node);
      }
      node.classList.add("entering");
    } else {
      const nameEl = node.querySelector(".tab-name");
      if (nameEl !== null && nameEl.textContent !== row.name) {
        nameEl.textContent = row.name;
      }
      // Beside the label it restates, so a rename cannot move one and leave the
      // other: renameTab writes the row and emit()s rather than touching the DOM.
      paintTooltip(node, row.name);
      let expectedNext: ChildNode | null = prev !== null ? prev.nextSibling : list.firstChild;
      while (
        expectedNext !== null &&
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        (expectedNext as HTMLElement).classList?.contains("exiting")
      ) {
        expectedNext = expectedNext.nextSibling;
      }
      if (reseat && node !== expectedNext) {
        if (prev !== null) {
          prev.after(node);
        } else {
          list.prepend(node);
        }
      }
    }
    node.classList.toggle("active", row.subject.id === state.active);
    node.setAttribute("aria-selected", row.subject.id === state.active ? "true" : "false");
    node.tabIndex = row.subject.id === state.active ? 0 : -1;
    // Sub-tab indent and pin marker, both from the SUBJECT. Toggled here rather
    // than only at creation so a pin applied to an already-rendered tab shows as
    // soon as its frame lands.
    node.classList.toggle("tab-child", row.subject.parent !== "");
    // The indent is per generation, not per child: a grandchild (a spec under a
    // tangent) steps in once more than its parent. Written beside the class so the
    // two cannot disagree.
    node.style.setProperty("--tab-depth", String(depthOf(row)));
    node.classList.toggle("tab-pinned", row.subject.pinned);
    // The parent id rides the row for the exit above, which must know whether the parent is still open
    // once the row has left the projection.
    if (row.subject.parent === "") {
      delete node.dataset["parentId"];
    } else {
      node.dataset["parentId"] = row.subject.parent;
    }
    prev = node;
  }
}

function createTabEl(row: TabRow): HTMLElement {
  const kind = row.subject.kind;
  const id = row.subject.id;
  const node = el("div", {
    className: "tab",
    "data-tab-id": id,
    "data-kind": kind,
    role: "tab",
  });

  const name = el("span", { className: "tab-name" }, row.name);

  // A SPAN, not a button: `role="tab"` is Children Presentational in WAI-ARIA, so a button here is an
  // unnamed dead tab stop (axe `nested-interactive`). Keyboard close is the APG's Delete on the row.
  const close = el("span", { className: "tab-close", "aria-hidden": "true" }, iconEl(ICON_CLOSE));
  close.addEventListener("pointerup", (e) => {
    e.stopPropagation();
    // The row's own tap guard, read here for the same reason: a drag that happened
    // to start on the × is a scroll, and the × is the strip's one destructive
    // control, so releasing it after a pan closed a tab nobody aimed at.
    if (gestureDragged()) {
      return;
    }
    void closeTab(id);
  });

  // The dot is decoration; `statusSR` is what a screen reader hears. Both ride
  // every row and paintDot writes both, so no node is added or removed as a state
  // changes — the same reasoning as the pin marker below.
  const statusDot = el("span", { className: CLS_DOT, "aria-hidden": "true" });
  const statusSR = el("span", { className: `${CLS_DOT_SR} sr-only` });

  // The WORKFLOW mark and its phrase, chat rows only. A second element, not a second dot state: a
  // launching turn ends (green dot) while its run carries on, so compounding them either hides the
  // run or lies about the turn. NOT a `.tab-icon`: that class means "grab me to reorder".
  const runDot = el("span", { className: CLS_RUN_DOT, "aria-hidden": "true" });
  const runSR = el("span", { className: `${CLS_RUN_DOT_SR} sr-only` });

  // The pin marker rides every row and CSS reveals it under `.tab-pinned`. The glyph is decorative;
  // the .sr-only word is what a screen reader hears.
  const pin = el(
    "span",
    { className: "tab-pin" },
    iconEl(ICON_PIN_FILLED),
    el("span", { className: "sr-only" }, "Pinned"),
  );

  // The nesting marker a sub-tab carries INSTEAD of its kind glyph. Not a `.tab-icon` ("grab me to
  // reorder"): a sub-tab's position is its parent's.
  const nest = el(
    "span",
    { className: "tab-nest", "aria-hidden": "true" },
    iconEl(ICON_TAB_SUBTAB),
  );

  if (row.subject.parent !== "") {
    // A SUB-TAB is the parent chat row's layout with the nesting arrow in front: it says its kind by
    // sitting under its parent, so the glyph slot goes to the marker and the dot sits by the name.
    // Keyed on the SUBJECT's parent, as `renderDOM`'s `tab-child` is, but decided at creation only: a
    // later `setTabParent` does not rebuild the node. Chat sub-tabs carry the workflow mark too.
    if (kind === "chat") {
      node.append(nest, statusDot, runDot, name, statusSR, runSR, pin, close);
    } else {
      node.append(nest, statusDot, name, statusSR, pin, close);
    }
    // The `idle` floor is the CHAT rule only: a run with no frame yet and a non-resident delegate have
    // no state to show, so both start blank and 12-tabs.css reserves the slot so the name does not
    // move when `run-dots.ts` / `subagent-dots.ts` paint.
    paintDot(node, kind, row.dotStatus ?? (kind === "chat" ? "idle" : ""), row.dotSince);
  } else if (kind === "chat") {
    // A chat tab LEADS with its activity dot: the strip says what is happening in chats you are not
    // looking at.
    node.append(statusDot, runDot, name, statusSR, runSR, pin, close);
    // Seeded from the row, falling back to `idle`: the store effect paints a tick later, and an
    // unseeded dot would shift the name.
    paintDot(node, kind, row.dotStatus ?? "idle", row.dotSince);
  } else {
    // Other kinds keep their glyph and use the trailing slot for the editor's unsaved mark; no `idle`
    // floor.
    const icon = el("span", { className: "tab-icon" }, iconEl(TAB_ICONS[kind]));
    node.append(icon, name, statusSR, pin, statusDot, close);
    // No producer supplies an age here, and the argument is passed anyway so this is
    // not the one paint site a reader has to reason about.
    paintDot(node, kind, row.dotStatus ?? "", row.dotSince);
  }
  // Repainted from the row for the dot's reason: a DOM rebuild is not one of the producer's inputs.
  // A no-op on a row with no mark.
  paintRunDot(node, row.runDot?.status ?? "", row.runDot?.tally ?? NO_RUNS);
  // A sub-tab is not independently draggable: its position is its parent's.
  // attachTabInteraction wires click/keyboard AND drag, so the flag rides along.
  attachTabInteraction(node, id, row.subject.parent === "");

  paintTooltip(node, row.name);

  if (kind === "chat") {
    node.addEventListener("dblclick", (e) => {
      if ((e.target as HTMLElement).closest(".tab-close, .tab-name-input") !== null) {
        return;
      }
      beginTabRename(node, id);
    });
  }

  // Right-click context menu for chat tabs: pin/unpin, move up/down, rename, then the exports.
  // Non-chat tabs keep the native browser menu.
  node.addEventListener("contextmenu", (e) => {
    if (kind !== "chat") {
      return;
    }
    e.preventDefault();
    // Read the CURRENT row: an applied frame replaces the subject, so the
    // closure's copy can be a generation behind on exactly the field this menu
    // reports.
    const current = rowOfID(id);
    if (current === undefined) {
      return;
    }
    const items: ContextMenuItem[] = [];
    if (current.subject.parent === "") {
      const pinned = current.subject.pinned;
      items.push({
        label: pinned ? "Unpin" : "Pin",
        action: () => {
          void setTabPinned(id, !pinned);
        },
      });
    }
    // On every chat row, a sub-tab's included, so the items never shift position
    // between rows; a sub-tab shows both disabled.
    items.push(
      {
        label: "Move up",
        disabled: !canMoveTab(id, -1),
        action: () => {
          moveTab(id, -1);
        },
      },
      {
        label: "Move down",
        disabled: !canMoveTab(id, 1),
        action: () => {
          moveTab(id, 1);
        },
      },
    );
    items.push({
      label: "Rename\u2026",
      action: () => {
        beginTabRename(node, id);
      },
    });
    items.push(
      {
        label: "Export as Markdown",
        action: () => {
          downloadChatExport(current.subject.ref, current.name, "md");
        },
      },
      {
        label: "Export as JSON",
        action: () => {
          downloadChatExport(current.subject.ref, current.name, "json");
        },
      },
      {
        label: "Download Kiro session",
        action: () => {
          void downloadKiroSession.dispatch({ chatID: current.subject.ref, name: current.name });
        },
      },
    );
    showContextMenu(items, { x: e.clientX, y: e.clientY });
  });

  return node;
}

// --- In-place rename ---

/** Swap a chat row's name for a field. The name span stays in the row, hidden,
 *  so a `chat_updated` landing mid-edit still repaints it; the field is removed
 *  on Enter, blur or Escape. Pointer and key events stop at the field, or the
 *  row's own handlers would activate, drag or close the tab under the caret. */
function beginTabRename(node: HTMLElement, id: string): void {
  const row = rowOfID(id);
  const nameEl = node.querySelector<HTMLElement>(".tab-name");
  if (row?.subject.kind !== "chat" || nameEl === null) {
    return;
  }
  if (node.querySelector(".tab-name-input") !== null) {
    return;
  }
  const chatID = row.subject.ref;
  const before = nameEl.textContent;
  const input = el("input", {
    type: "text",
    className: "tab-name-input",
    maxLength: MAX_CHAT_NAME_UNITS,
    value: before,
    "aria-label": "Chat name",
  }) as HTMLInputElement;
  let done = false;
  const finish = (commit: boolean): void => {
    if (done) {
      return;
    }
    done = true;
    const name = input.value.trim();
    input.remove();
    nameEl.hidden = false;
    if (commit && name !== "" && name !== before) {
      void renameChat.dispatch({ chatID, name });
    }
  };
  for (const type of ["pointerdown", "pointerup", "click", "dblclick", "auxclick"] as const) {
    input.addEventListener(type, (e) => {
      e.stopPropagation();
    });
  }
  input.addEventListener("keydown", (e) => {
    e.stopPropagation();
    if (e.key === "Enter") {
      e.preventDefault();
      finish(true);
      node.focus();
    } else if (e.key === "Escape") {
      e.preventDefault();
      finish(false);
      node.focus();
    }
  });
  input.addEventListener("blur", () => {
    finish(true);
  });
  nameEl.hidden = true;
  nameEl.after(input);
  input.focus();
  input.select();
}

// --- Tap vs drag ---
//
// `#tab-list` scrolls, and activation is a `pointerup`; a drag along an axis with nothing to scroll
// starts no pan and so no cancel, and the release lands on the row. So the verdict is travelled
// DISTANCE. ONE module-scope gesture (`isPrimary`), read by both activation and the ×; no
// `pointercancel` reset, the next `pointerdown` clears it. The origin is RE-BASELINED when the
// visual viewport moves (`viewport-frame.ts`): declaring a drag would refuse a real click.
let gestureOriginX = 0;
let gestureOriginY = 0;
let gesturePointerType = "";
let gestureFrame: ViewportBox = { offsetLeft: 0, offsetTop: 0, width: 0, height: 0 };
let gestureIsDrag = false;

/** Whether the primary gesture in flight has travelled far enough to be a drag —
 *  and therefore may not activate or close the row it started on. */
function gestureDragged(): boolean {
  return gestureIsDrag;
}

/** Track the strip's tap-vs-drag gesture on one row. Every row gets this, unlike
 *  `attachDrag`: a sub-tab cannot be reordered and can still be scrolled past. */
function attachTapGuard(node: HTMLElement): void {
  node.addEventListener("pointerdown", (e) => {
    if (!e.isPrimary) {
      return;
    }
    gestureOriginX = e.clientX;
    gestureOriginY = e.clientY;
    gesturePointerType = e.pointerType;
    gestureFrame = viewportBox();
    gestureIsDrag = false;
  });
  node.addEventListener("pointermove", (e) => {
    if (!e.isPrimary || gestureIsDrag) {
      return;
    }
    const frame = viewportBox();
    if (viewportMoved(frame, gestureFrame)) {
      gestureOriginX = e.clientX;
      gestureOriginY = e.clientY;
      gestureFrame = frame;
      return;
    }
    if (
      exceedsSlop(
        e.clientX - gestureOriginX,
        e.clientY - gestureOriginY,
        pointerDragActivation(gesturePointerType),
      )
    ) {
      gestureIsDrag = true;
    }
  });
}

// --- Interaction (click, middle-click, drag, keyboard) ---

function attachTabInteraction(node: HTMLElement, id: string, draggable: boolean): void {
  attachTapGuard(node);

  // Click to activate (any target outside .tab-close).
  node.addEventListener("pointerup", (e) => {
    if (isDragHandled()) {
      return;
    }
    // A drag SCROLLS and never activates. Above the guard's own targets so a
    // gesture that started on the × and released on the row is refused too.
    if (gestureDragged()) {
      return;
    }
    if ((e.target as HTMLElement).closest(".tab-close") !== null) {
      return;
    }
    if (!e.isPrimary) {
      return;
    }
    activateTab(id);
  });

  // Middle-click to close.
  node.addEventListener("auxclick", (e) => {
    if (e.button === 1) {
      e.preventDefault();
      void closeTab(id);
    }
  });

  // Keyboard navigation: Enter/Space activates, Delete closes, arrows move focus.
  node.addEventListener("keydown", (e) => {
    switch (e.key) {
      case "Enter":
      case " ":
        e.preventDefault();
        activateTab(id);
        break;
      case "F2":
        if (rowOfID(id)?.subject.kind === "chat") {
          e.preventDefault();
          beginTabRename(node, id);
        }
        break;
      case "Delete":
      case "Backspace": {
        e.preventDefault();
        // Focus is moved by hand, or Delete drops the keyboard user onto <body>. The APG puts focus on the
        // tab that took the closed one's place: everything after me, then everything before in reverse.
        // Moved BEFORE the dispatch, because a close is a round trip.
        const siblings = [...(node.parentElement?.children ?? [])] as HTMLElement[];
        const self = siblings.indexOf(node);
        const successors = [...siblings.slice(self + 1), ...siblings.slice(0, self).reverse()];
        const successor = successors.find((n) => n.dataset["tabId"] !== id);
        if (successor !== undefined) {
          successor.focus();
        } else {
          // The LAST tab: no row survives for focus to land on, and losing it
          // to <body> strands the keyboard user. The empty-state surface's one
          // control is the composer, so focus goes there.
          $.promptInput.focus();
        }
        void closeTab(id);
        break;
      }
      case "ArrowDown":
      case "ArrowRight":
      case "ArrowUp":
      case "ArrowLeft":
      case "Home":
      case "End": {
        e.preventDefault();
        const tabs = [...(node.parentElement?.children ?? [])] as HTMLElement[];
        const i = tabs.indexOf(node);
        let targetEl: HTMLElement | undefined;
        if (e.key === "ArrowDown" || e.key === "ArrowRight") {
          targetEl = tabs[i + 1] ?? tabs[0];
        } else if (e.key === "ArrowUp" || e.key === "ArrowLeft") {
          targetEl = tabs[i - 1] ?? tabs[tabs.length - 1];
        } else if (e.key === "Home") {
          targetEl = tabs[0];
        } else {
          targetEl = tabs[tabs.length - 1];
        }
        targetEl?.focus();
        break;
      }
    }
  });

  if (draggable) {
    attachDrag(node);
  }
}

// --- Singleton tab helpers ---

/** Open or toggle a singleton tab: close it when active, else open/activate. Both halves are round
 *  trips, so the toggles return the promise; a toggle resolving before the frame lands would let a
 *  second click race the first. */
async function toggleSingleton(kind: TabKind): Promise<void> {
  const open = tabIdFor(kind);
  if (open !== "" && state.active === open) {
    await closeTab(open);
    return;
  }
  await openTab({ kind });
}

/** Toggle the Settings tab, landing on the given sub-tab (default: General). A singleton's ref is
 *  empty, so the sub-tab is applied AFTER the tab exists, through the router's setter. */
export async function toggleSettingsView(tab: SettingsTab = "general"): Promise<void> {
  await toggleSingleton("settings");
  setSettingsTab(tab);
}

/** Open Settings on the given sub-tab (default: General). NEVER closes it, which
 *  is why it exists beside the toggle: navigation, routing and a link out of a
 *  modal express an intent to OPEN, and a toggle would dismiss the panel it was
 *  pointing at. */
export async function openSettingsView(tab: SettingsTab = "general"): Promise<void> {
  await openTab({ kind: "settings" });
  setSettingsTab(tab);
}

/** Switch the Settings panel to a sub-tab; a no-op when Settings is not open. SYNCHRONOUS and not a
 *  collection mutation: the client's own correction channel over the factory's route. */
export function setSettingsTab(tab: SettingsTab): void {
  setTabRoute(tabIdFor("settings"), { kind: "settings", tab });
}

export async function toggleGitView(tab: GitTab = "changes"): Promise<void> {
  await toggleSingleton("git");
  setGitTab(tab);
}

/** Open the git view on the given sub-tab (default: Changes). NEVER closes it;
 *  the twin of openSettingsView, for the same reason. */
export async function openGitView(tab: GitTab = "changes"): Promise<void> {
  await openTab({ kind: "git" });
  setGitTab(tab);
}

/** Switch the git view's sub-tab route. No-op when the git view isn't open.
 *  Mirrors setSettingsTab for the same reason. */
export function setGitTab(tab: GitTab): void {
  setTabRoute(tabIdFor("git"), { kind: "git", tab });
}

/** Toggle the file browser, opening at `openAt` when none is open. Not `toggleSingleton`: files is
 *  multi-instance. Close the ACTIVE browser, else bring the most recent forward, else open one. */
export async function toggleFilesView(openAt: string): Promise<void> {
  const row = activeOrRecentFilesTab();
  if (row !== undefined) {
    if (state.active === row.subject.id) {
      await closeTab(row.subject.id);
    } else {
      activateTab(row.subject.id);
    }
    return;
  }
  await openTab({ kind: "files", ref: openAt });
}

/** Bring the file browser forward, opening at `openAt` when none is open. NEVER
 *  closes it, which is the whole reason this exists beside the toggle: "go to" and
 *  "toggle" are different verbs, as setSettingsTab/setGitTab/setDocsTab already are. */
export async function openFilesView(openAt: string): Promise<void> {
  const row = activeOrRecentFilesTab();
  if (row !== undefined) {
    if (state.active !== row.subject.id) {
      activateTab(row.subject.id);
    }
    return;
  }
  await openTab({ kind: "files", ref: openAt });
}

/** Switch the History page's pane route. No-op when it isn't open. */
export function setHistoryTab(tab: HistoryTab): void {
  setTabRoute(tabIdFor("history"), { kind: "history", tab });
}

/** Switch the docs browser's sub-tab route. No-op when it isn't open. */
export function setDocsTab(tab: DocsTab): void {
  setTabRoute(tabIdFor("docs"), { kind: "docs", tab });
}

/** Toggle the Kiro configuration browser, landing on the given sub-tab. */
export async function toggleDocsView(tab: DocsTab = "steering"): Promise<void> {
  await toggleSingleton("docs");
  setDocsTab(tab);
}

/** Point ONE row's LOCAL route somewhere, by id. The readonly spec is replaced, not mutated; emits
 *  only when the tab is active, since the route subscriber pushes the URL. */
function setTabRoute(id: string, route: Route): void {
  const row = rowOfID(id);
  if (row === undefined) {
    return;
  }
  row.spec = { ...row.spec, route };
  if (state.active === id) {
    emit();
  }
}

/** Point ONE file browser's route at the folder it shows, by ref. `showView` pushes the active
 *  route on every projection mutation, so an untracked route is overwritten; by REF because
 *  `applyRoute` points a tab it is about to activate. */
export function setFilesRoute(ref: string, path: string): void {
  setTabRoute(filesTabIdFor(ref), { kind: "files", path });
}

/** The files kind's `tabIdFor`: the tab whose ref names this FOLDER, or "". Both sides are
 *  normalised: a persisted `subject.ref` is bounded only by MaxRefBytes while everything downstream
 *  is `normalizeDirPath` output. */
export function filesTabIdFor(ref: string): string {
  return filesRowForRef(ref)?.subject.id ?? "";
}

/** The files row whose ref names this folder, in the normalised space. */
function filesRowForRef(ref: string): TabRow | undefined {
  const dir = normalizeDirPath(ref);
  return state.tabs.find(
    (t) => t.subject.kind === "files" && normalizeDirPath(t.subject.ref) === dir,
  );
}

/** The active files row, else the most recently activated, else the first, else "". Also
 *  `filesTabForRoute`'s rungs 2 and 3, so a route, the sidebar button and Ctrl-F agree. */
function activeOrRecentFilesTab(): TabRow | undefined {
  const active = rowOfID(state.active);
  if (active?.subject.kind === "files") {
    return active;
  }
  for (const id of activationHistory) {
    const row = rowOfID(id);
    if (row?.subject.kind === "files") {
      return row;
    }
  }
  return state.tabs.find((t) => t.subject.kind === "files");
}

/** The files tab a `/files/<path>` route resolves to, and its NORMALISED ref: the tab opened at that
 *  folder, else the active one, else the most recent; `{id:"", ref:""}` refuses a history entry for
 *  a browser nobody has. Not `subjectForRoute`, the factory's inverse for OPEN. */
export function filesTabForRoute(path: string): { id: string; ref: string } {
  const row = filesRowForRef(path) ?? activeOrRecentFilesTab();
  return row === undefined
    ? { id: "", ref: "" }
    : { id: row.subject.id, ref: normalizeDirPath(row.subject.ref) };
}

// --- Multi-instance openers ---

/** Open (or focus) a workflow run's tab. Not a singleton: several runs can be open
 *  side by side. `parent` makes it a SUB-TAB of the chat that launched it, and
 *  `owns: false` is what makes that safe — a VIEW tab's × removes a view and stops
 *  nothing, while a launcher-OWNED run's × means stop. */
export async function openRunTab(
  workflowID: string,
  name: string,
  opts?: { parent?: string; owns?: boolean; activate?: boolean },
): Promise<void> {
  await openTab({
    kind: "run",
    ref: workflowID,
    name,
    ...(opts?.parent === undefined ? {} : { parent: opts.parent }),
    ...(opts?.owns === undefined ? {} : { owns: opts.owns }),
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

/** Open (or focus) a SUBAGENT execution's page, only ever from a reader's gesture; no SSE handler
 *  may call it. `owns: false` always. `parent` nests it under the launching chat. No name override,
 *  so a restored tab and a card-opened one read the same. */
export async function openSubagentTab(chatID: string, subtaskID: string): Promise<void> {
  if (chatID === "" || subtaskID === "") {
    return;
  }
  const parent = tabIdFor("chat", chatID);
  await openTab({
    kind: "subagent",
    ref: subagentRef(chatID, subtaskID),
    ...(parent === "" ? {} : { parent }),
    owns: false,
  });
}

/** Open (or focus) an editor tab for a path: only the TAB half of the editor's `open()`, which
 *  keeps the opener's mode, repo and line. */
export async function openEditorView(
  filePath: string,
  opts?: { activate?: boolean },
): Promise<void> {
  await openTab({
    kind: "editor",
    ref: filePath,
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

// --- Test helpers (no-op in production; used by tabs.test.ts) ---

/** Reset all projection state. Exported for test isolation only. */
export function _resetForTest(): void {
  tabNotice = bareTabNotice;
  state.tabs = [];
  state.active = "";
  callbacks.onActivate = null;
  callbacks.onEmpty = null;
  callbacks.onClosed = null;
  chatSettledProbe = null;
  if (internal.emptyTimer !== null) {
    clearTimeout(internal.emptyTimer);
  }
  internal.emptyTimer = null;
  internal.emptyDeferred = false;
  internal.renderQueued = false;
  internal.everOpened = false;
  nameOverrides.clear();
  activationHistory.length = 0;
  lastAnnouncedTab = "";
  // Re-register the target: a test that reset tabs-sync dropped it.
  registerTabsTarget(target);
  // Dispose existing module effects and re-register so tests observe the same
  // side-effects (view sync, DOM render) as production.
  for (const c of moduleEffects) {
    c();
  }
  moduleEffects.length = 0;
  registerModuleSubscribers();
}

// Register module-level effects after all module declarations are initialized —
// effect() bodies run synchronously on subscribe, so they must run AFTER
// ACTIVE_BTN, syncSidebarButtons, renderDOM and showView are defined.
registerModuleSubscribers();
