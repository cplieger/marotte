import { el, KEY_ATTR, reconcile } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { apiGet } from "./api-client.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  atLiveEdgeNow,
  beginSelfScroll,
  endSelfScroll,
  getScrollEl,
  onAttach,
  onContentResize,
  onReaderGesture,
  onTranscriptMutate,
  readingLineOffset,
  scrollableBy,
  scrollToOffset,
} from "./scroll.js";
import { binLabel, markerLabel } from "./rail-labels.js";
import { buildPath } from "./route-path.js";
import { projectTurns } from "./turns.js";
import { searchHitTurns } from "./chat-search.js";
import { get } from "./store.js";
import { mergeTurnSets, validateTurnIndex } from "./rail-merge.js";
import type { TurnSummary } from "./rail-merge.js";
import { binTurns } from "./rail-select.js";
import type { TurnBin, TurnMapLayout } from "./rail-select.js";
import { activeTurnAt, buildOffsets, markerSlotFor, turnsInView } from "./rail-activation.js";
import type { CardTop, TurnOffsets } from "./rail-activation.js";

/** One row of the session-wide turn index. Declared by the module that MERGES the
 *  set, which is a pure leaf, and re-exported here because this is where the map's
 *  consumers already read it from. */
export type { TurnSummary };

/** How far the transcript must scroll before the map appears: a threshold, not `> 0`, so a few
 *  pixels of settling overflow do not flicker it. Equal to `BOTTOM_TOLERANCE_PX` by coincidence;
 *  do not collapse the two. */
const MIN_SCROLL_PX = 100;

/** Wall-clock bound on one jump's paging loop, so a store that keeps reporting more
 *  history cannot spin. Deliberately not a page count: `loadUntilResident` is
 *  written for sessions of 8 pages and more, so a small iteration cap would make a
 *  click on an early turn scroll nowhere. */
const PAGE_BUDGET_MS = 4000;

/** How long one scroll is given to settle before the landing is re-measured.
 *  `scrollend` is not universally implemented, so this is the only release path on
 *  an engine without it rather than belt-and-braces. */
const PICK_SETTLE_MS = 1200;

/** At most this many `behavior: "auto"` corrections per jump — never a second smooth
 *  animation, so exactly one is ever in flight. */
const MAX_CORRECTIONS = 6;

/** How far the target's top may sit from the reading line and still count as landed. */
const LANDING_TOLERANCE_PX = 8;

/** Ceiling on the click's own intent, which suppresses offset-driven activation
 *  while the animation runs. DERIVED so it cannot fall inside the two budgets it
 *  brackets: a paged jump spends both in sequence. */
const PICK_BUDGET_MS = PAGE_BUDGET_MS + PICK_SETTLE_MS;

interface MapNodes {
  nav: HTMLElement;
  /** The box binning is measured against: its height never depends on the rows inside it. */
  track: HTMLElement;
  stack: HTMLOListElement;
  prev: HTMLButtonElement;
  next: HTMLButtonElement;
}

let root: MapNodes | undefined;
let chatID = "";
/** The pointed chat's index rows, as last fetched or replayed from its record. Held
 *  beside `records` rather than read out of it, because a chat the STORE does not
 *  hold records nothing and would otherwise lose its map. */
let indexed: TurnSummary[] = [];
let summaries: TurnSummary[] = [];
/** `summaries` indexed by the turn's opening-message id, rebuilt wherever the set is
 *  assigned, so the id-keyed lookups are never a linear scan. */
let summaryByID = new Map<string, TurnSummary>();
/** Each resident turn's number by its card key, so a held id resolves to a number
 *  even for the window's first turn when it is a fragment whose opening message paged
 *  out: `mergeTurnSets` keeps the INDEX's id for that row, so `summaryByID` never
 *  holds the card's key. */
let residentN = new Map<string, number>();
/** The last turn number a held id resolved to. An id the store window no longer holds
 *  must move the mark, never clear it, so the mark is latched here. */
let heldN: number | undefined;
/** The turn the scroll offset names. A turn ID rather than a number, because the id
 *  is the turn's identity and the number is a value the index can restate. */
let activeID = "";
/** The turn the READER picked, which outranks `activeID` until they say otherwise.
 *
 *  INVARIANT, enforced at `setTurns`: only ever a turn the merged set carries. The
 *  click reads it off a summary, and the merge drops it when the new set no longer
 *  names it — a rewind truncates the session from a turn footer two clicks away. */
let selectedID: string | undefined;
/** Turn IDs whose jump is waiting on a fetch, so the map can say so. */
const pending = new Set<string>();

/** One chat's fetched index plus the chat's turn count when the request went out,
 *  captured BEFORE the fetch (the same discipline as loadMessages' `knownBefore` — an
 *  answer that raced an append must not claim currency over it). */
interface RailRecord {
  summaries: TurnSummary[];
  atCount: number;
}

/** Session-wide indexes by chat, kept across switches so returning to a loaded chat
 *  paints its map from memory instead of refetching. `refreshTurnRail` is the one
 *  writer; re-pointing prunes rows the store no longer holds, and
 *  `invalidateTurnRails` drops them all. */
const records = new Map<string, RailRecord>();

/** Whether `id`'s record can stand in for a fetch: present and at the chat's current turn count,
 *  which witnesses a turn STARTED, never ended; `mergeTurnSets` covers a close by letting a resident
 *  row win. The GET has no digest stamp, so a whole-projection reconcile goes through
 *  `invalidateTurnRails`. */
function recordCurrent(id: string): boolean {
  const r = records.get(id);
  if (r === undefined) {
    return false;
  }
  return r.atCount === get(id)?.turn_count;
}

/** Forget every held index: the reconcile body's call, because nothing certifies a
 *  record across a lost stream. Each chat re-reads its index at its next activation;
 *  the one on screen is re-read by the refresh the reconcile ends on. */
export function invalidateTurnRails(): void {
  records.clear();
}

/** Whether there is enough transcript to navigate. */
function measureNavigable(): boolean {
  return scrollableBy() > MIN_SCROLL_PX;
}

/** `measureNavigable()`'s last answer, taken in `pick()` so `render()` reads no layout. */
let navigable = false;

/** Everything the map's rows were built FROM, so a render that would redraw the same map writes
 *  nothing: `render()` runs on every transcript paint. It must name EVERY input the map's nodes
 *  read (`renderSignature`, the one list), or the map goes stale. */
let renderedSig = "";

/** The track's height, from its ResizeObserver entry: `render()` reads no layout. 0 wherever CSS
 *  does not draw the map (narrow, coarse), so `binTurns` lays no rows there; an unshown map stays
 *  laid out (29-turns.css), so a chat switch bins against a live height. */
let trackPx = 0;
let inView: ReadonlySet<string> = new Set();
/** The row a keyboard reader moved the tab stop to while focus is in the stack; the current row
 *  holds it otherwise. */
let rovingKey: string | undefined;
let shown: readonly TurnBin[] = [];
let shownSlots = 0;

/** The one writer of the turn set: the resident window merged into the fetched
 *  index, so the newest turn appears the moment its card mounts and the index only
 *  extends the set backwards. Also the one place the reader's pick is reconciled
 *  against that set, because this is the moment the mapping moves. */
function setTurns(): void {
  const session = get(chatID);
  const resident = session === undefined ? [] : projectTurns(session);
  summaries = mergeTurnSets(resident, indexed);
  summaryByID = new Map(summaries.map((s) => [s.id, s]));
  residentN = new Map(resident.map((t) => [t.id, t.n]));
  if (selectedID !== undefined && !summaryByID.has(selectedID)) {
    selectedID = undefined;
  }
}

/** Mount the map into the transcript's positioned outer wrapper. Idempotent. */
export function mountTurnRail(host: HTMLElement): void {
  if (root !== undefined) {
    return;
  }
  root = buildMap();
  host.append(root.nav);
  bindMapInput(root);
  getScrollEl().addEventListener("scroll", schedulePick, { passive: true });
  // A READER GESTURE revokes the pick; nothing else does. That covers both ways the
  // reader states a position — a scroll, and a request for the live edge, which
  // scrolls through the controller and so fires no reader scroll event.
  onReaderGesture(clearSelection);
  // Mount, unmount, the pagination prepend and `content-visibility` re-estimates all move tops with no
  // scroll; an unpark restores against re-measured cards.
  onTranscriptMutate(repick);
  onContentResize(repick);
  onAttach(repick);
  if (typeof ResizeObserver === "function") {
    new ResizeObserver((entries) => {
      for (const e of entries) {
        trackPx = e.contentRect.height;
      }
      scheduleRailRender();
    }).observe(root.track);
  }
}

function stepButton(dir: "prev" | "next"): HTMLButtonElement {
  const label = dir === "prev" ? "Previous turn" : "Next turn";
  const btn = el("button", {
    type: "button",
    className: "turn-map-step",
    "data-dir": dir,
    "aria-label": label,
    "data-tooltip": label,
  }) as HTMLButtonElement;
  btn.appendChild(iconEl(dir === "prev" ? ICON_CHEVRON_UP : ICON_CHEVRON_DOWN));
  return btn;
}

function buildMap(): MapNodes {
  const prev = stepButton("prev");
  const next = stepButton("next");
  const stack = el("ol", { className: "turn-map-stack" }) as HTMLOListElement;
  const track = el("div", { className: "turn-map-track" }, stack);
  const nav = el("nav", { className: "turn-map", "aria-label": "Turns" }, prev, track, next);
  return { nav, track, stack, prev, next };
}

/** Hand the map to a chat, dropping the previous session's view state. Separate from the fetch: an
 *  EMPTY chat re-points too. The chat's record paints at once, so a switch back costs no fetch. */
export function pointTurnRail(id: string): void {
  if (id === chatID) {
    return;
  }
  selectedID = undefined;
  pending.clear();
  releaseIntent();
  invalidateJumps();
  // Records for chats the store no longer holds are dead weight (closed tabs,
  // deleted chats), and a re-point is the cheap moment to drop them — including the
  // target's own, so a purged chat renders empty rather than from memory.
  for (const key of records.keys()) {
    if (get(key) === undefined) {
      records.delete(key);
    }
  }
  chatID = id;
  indexed = records.get(id)?.summaries ?? [];
  activeID = "";
  heldN = undefined;
  inView = new Set();
  rovingKey = undefined;
  residentN = new Map();
  residentCards = [];
  setTurns();
  render();
  repick();
}

/** The activation entry: point the map at the chat, then fetch its index only when
 *  the chat's record cannot stand in for one. `force` skips that gate — the caller
 *  activating a stale transcript knows the map is implicated with it. */
export async function loadTurnRail(id: string, opts?: { force?: boolean }): Promise<void> {
  pointTurnRail(id);
  if (opts?.force !== true && recordCurrent(id)) {
    return;
  }
  await refreshTurnRail(id);
}

/** Re-fetch the session-wide index. */
export async function refreshTurnRail(id: string): Promise<void> {
  if (id === "") {
    return;
  }
  // Captured BEFORE the request — see RailRecord. A count the store does not know
  // records nothing: there is no session left to activate against.
  const countAtStart = get(id)?.turn_count;
  const d = await apiGet<{ turns?: unknown }>(`/api/chats/${encodeURIComponent(id)}/turns`);
  if (d === null) {
    // A failed fetch, already logged centrally. Keep what the map is showing and
    // keep the stale record, so the next activation retries instead of trusting it.
    return;
  }
  const { turns } = validateTurnIndex(d.turns ?? []);
  if (countAtStart !== undefined) {
    records.set(id, { summaries: turns, atCount: countAtStart });
  }
  if (id !== chatID) {
    return;
  }
  indexed = turns;
  setTurns();
  render();
  // The index is what turns an already-visible card into a placeable one, and a
  // scroll frame may never arrive on its own.
  repick();
}

export function resetTurnRail(): void {
  chatID = "";
  records.clear();
  indexed = [];
  summaries = [];
  summaryByID = new Map();
  residentN = new Map();
  activeID = "";
  heldN = undefined;
  selectedID = undefined;
  inView = new Set();
  rovingKey = undefined;
  pending.clear();
  residentCards = [];
  invalidateOffsets();
  releaseIntent();
  invalidateJumps();
  clearRailTarget();
  if (pickFrame !== 0) {
    cancelAnimationFrame(pickFrame);
    pickFrame = 0;
  }
  if (renderFrame !== 0) {
    cancelAnimationFrame(renderFrame);
    renderFrame = 0;
  }
  render();
}

/** Record which turn cards the transcript holds. Called after every full paint,
 *  which is what makes the offset table's rebuild the paint's own cost rather than a
 *  per-scroll-frame one. */
export function setResidentTurns(cards: Iterable<HTMLElement>): void {
  residentCards = [...cards];
  setTurns();
  render();
  repick();
}

// ---------------------------------------------------------------------------
// Activation
// ---------------------------------------------------------------------------

/** The transcript's turn cards, in paint order. */
let residentCards: HTMLElement[] = [];
/** The cached offset table, rebuilt lazily on the next read. */
let offsets: TurnOffsets | undefined;
/** The scroll-coalesced activation read. */
let pickFrame = 0;
/** The resize-coalesced render, deferred out of the map's own resize delivery. */
let renderFrame = 0;

function invalidateOffsets(): void {
  offsets = undefined;
}

/** Clear the cached geometry AND re-answer activation: nothing else re-derives the active turn on a
 *  non-scroll invalidation, so the mark would freeze. */
function repick(): void {
  invalidateOffsets();
  schedulePick();
}

/** Defer the resize-driven render one frame, behind a single slot. NEVER inside the track's own
 *  resize delivery: a render there that resized an observed box would make the engine report
 *  "ResizeObserver loop completed with undelivered notifications". Other renders stay sync. */
function scheduleRailRender(): void {
  if (renderFrame !== 0) {
    return;
  }
  renderFrame = requestAnimationFrame(() => {
    renderFrame = 0;
    render();
    repick();
  });
}

/** Coalesce scroll events into one activation read per frame. */
function schedulePick(): void {
  if (pickFrame !== 0) {
    return;
  }
  pickFrame = requestAnimationFrame(() => {
    pickFrame = 0;
    pick();
  });
}

function pick(): void {
  const scroller = getScrollEl();
  const table = readOffsets();
  const seen = turnsInView(scroller.scrollTop, table, scroller.clientHeight);
  const bandMoved = !sameSet(seen, inView);
  inView = seen;
  const gate = measureNavigable();
  const gateMoved = gate !== navigable;
  navigable = gate;
  // A held intent owns the position for the length of its own animation, which
  // crosses every intervening turn on the way. The band is a fact and follows anyway.
  const next = intentOpen
    ? ""
    : activeTurnAt(scroller.scrollTop, table, readingLineOffset(), {
        clientHeight: scroller.clientHeight,
        atLiveEdge: atLiveEdgeNow(),
      });
  const moved = next !== "" && next !== activeID;
  if (moved) {
    activeID = next;
  }
  if (moved || bandMoved || gateMoved) {
    render();
  }
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) {
    return false;
  }
  for (const v of a) {
    if (!b.has(v)) {
      return false;
    }
  }
  return true;
}

function readOffsets(): TurnOffsets {
  offsets ??= buildOffsets(cardTops());
  return offsets;
}

/** Measure the resident cards into the scroller's own frame. */
function cardTops(): CardTop[] {
  const out: CardTop[] = [];
  for (const card of residentCards) {
    const key = keyOf(card);
    if (key !== "") {
      out.push({ id: key, top: scrollFrameTop(card) });
    }
  }
  return out;
}

/** A card's top in the SCROLLER's frame, or null when it has no box. Rects, not `offsetTop`:
 *  `content-visibility: auto` makes `.msg-row` a containing block, so `offsetTop` read 0. */
function scrollFrameTop(card: HTMLElement): number | null {
  if (card.getClientRects().length === 0) {
    return null;
  }
  const scroller = getScrollEl();
  const frame = scroller.getBoundingClientRect();
  return scroller.scrollTop + card.getBoundingClientRect().top - frame.top - scroller.clientTop;
}

/** A card's stable identity: the id of the turn's OPENING MESSAGE, which is both the
 *  transcript's reconcile key and the server index's `TurnSummary.id`. "" for an
 *  element carrying no key, which is not a turn card. */
function keyOf(card: Element): string {
  return card.getAttribute("data-reconcile-key") ?? "";
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

interface PillView {
  readonly bin: TurnBin;
  readonly hit: boolean;
  readonly pending: boolean;
  readonly inView: boolean;
  readonly current: boolean;
  readonly roving: boolean;
}

function render(): void {
  if (root === undefined) {
    return;
  }
  const visible = summaries.length > 0 && navigable;
  const layout: TurnMapLayout = visible
    ? binTurns(summaries, trackPx)
    : { total: 0, slots: 0, bins: [] };
  const n = markedN();
  const current =
    n === undefined
      ? -1
      : markerSlotFor(
          layout.bins.map((b) => b.first),
          n,
        );
  const roving = rovingIndex(layout.bins, current);
  const hits = searchHitTurns();
  const rows: PillView[] = layout.bins.map((bin, i) => ({
    bin,
    hit: bin.members.some((m) => hits.has(m.n)),
    pending: bin.members.some((m) => pending.has(m.id)),
    inView: bin.members.some((m) => inView.has(m.id)),
    current: i === current,
    roving: i === roving,
  }));
  const sig = renderSignature(rows, layout, visible, n);
  if (sig === renderedSig) {
    return;
  }
  renderedSig = sig;
  shown = layout.bins;
  shownSlots = layout.slots;
  paintMap(root, rows, layout, visible, current);
}

/** The row holding the single tab stop: the keyboard reader's while they hold one, else the
 *  current row. */
function rovingIndex(bins: readonly TurnBin[], current: number): number {
  if (rovingKey !== undefined) {
    const i = bins.findIndex((b) => b.key === rovingKey);
    if (i >= 0) {
      return i;
    }
    rovingKey = undefined;
  }
  return bins.length === 0 ? -1 : Math.max(0, current);
}

/** Every value the rendered nodes read, in one string (see `renderedSig`). Joined with `keyenc`:
 *  a turn id is server text, and a collision freezes the map. */
function renderSignature(
  rows: readonly PillView[],
  layout: TurnMapLayout,
  visible: boolean,
  marked: number | undefined,
): string {
  const parts: string[] = [
    String(layout.total),
    String(layout.slots),
    flag(visible),
    String(marked ?? -1),
    chatID,
    flag(pending.size > 0),
  ];
  for (const r of rows) {
    parts.push(
      String(r.bin.members.length),
      r.bin.key,
      String(r.bin.at),
      r.bin.severity,
      r.bin.target.id,
      flag(r.hit),
      flag(r.pending),
      flag(r.inView),
      flag(r.current),
      flag(r.roving),
    );
    for (const m of r.bin.members) {
      parts.push(
        m.id,
        String(m.n),
        m.outcome,
        flag(m.agent_initiated === true),
        m.first_line ?? "",
        String(m.elapsed_ms ?? 0),
      );
    }
  }
  return join(...parts);
}

function flag(b: boolean): string {
  return b ? "1" : "0";
}

function paintMap(
  m: MapNodes,
  rows: readonly PillView[],
  layout: TurnMapLayout,
  visible: boolean,
  current: number,
): void {
  m.nav.toggleAttribute("data-shown", visible);
  setAttr(m.nav, "aria-busy", pending.size > 0 ? "true" : null);
  m.stack.style.setProperty("--turn-map-slots", String(Math.max(1, layout.slots)));
  m.stack.style.setProperty("--turn-map-total", String(Math.max(1, layout.total)));
  reconcile(m.stack, rows, { key: (r) => r.bin.key, mount: mountPill, update: paintPill });
  m.prev.disabled = current <= 0;
  m.next.disabled = current >= rows.length - 1;
}

function mountPill(r: PillView): HTMLElement {
  const link = el(
    "a",
    { className: "turn-pill-link" },
    el("span", { className: "turn-pill-mark" }),
    el("span", { className: "turn-pill-hit" }),
  );
  const li = el("li", { className: "turn-pill" }, link);
  paintPill(li, r);
  return li;
}

/** Writes in place, so a focused link and an open tooltip survive a repaint. */
function paintPill(li: HTMLElement, r: PillView): void {
  const { bin } = r;
  const single = bin.members.length === 1;
  li.style.setProperty("--rail-at", String(bin.at));
  setAttr(li, "data-severity", bin.severity);
  setAttr(li, "data-trigger", single && bin.first.agent_initiated === true ? "system" : null);
  setAttr(li, "data-in-view", r.inView ? "" : null);
  setAttr(li, "data-current", r.current ? "" : null);
  setAttr(li, "data-hit", r.hit ? "" : null);
  const link = li.firstElementChild;
  if (!(link instanceof HTMLAnchorElement)) {
    return;
  }
  const state = { pending: r.pending, hit: r.hit };
  const label = single
    ? markerLabel(bin.first, state)
    : binLabel({ first: bin.first.n, last: bin.last.n, worst: bin.target.outcome }, state);
  setAttr(link, "href", turnHref(bin.target));
  setAttr(link, "tabindex", r.roving ? "0" : "-1");
  setAttr(link, "aria-current", r.current ? "location" : null);
  setAttr(link, "aria-label", label.ariaLabel);
  // The styled tooltip, never a native `title`: a UA tooltip misses the styled treatment and
  // publishes no `aria-describedby`.
  setAttr(link, "data-tooltip", label.preview);
}

function turnHref(s: TurnSummary): string {
  return buildPath({ kind: "chat", id: chatID, turn: s.n });
}

function setAttr(node: Element, name: string, value: string | null): void {
  if (value === null) {
    if (node.hasAttribute(name)) {
      node.removeAttribute(name);
    }
  } else if (node.getAttribute(name) !== value) {
    node.setAttribute(name, value);
  }
}

// ---------------------------------------------------------------------------
// Input
// ---------------------------------------------------------------------------

const KEY_STEP = new Map<string, number>([
  ["ArrowUp", -1],
  ["ArrowDown", 1],
  ["PageUp", -10],
  ["PageDown", 10],
]);

/** A primary click with no modifier is the map's own jump; anything else is the link's, so
 *  copy-link, middle-click and open-in-new-tab keep working. */
function isPlainClick(e: MouseEvent): boolean {
  return (
    !e.defaultPrevented && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey
  );
}

function bindMapInput(m: MapNodes): void {
  // ONE listener for every row: the rows tile the stack, so native hit-testing maps pointer Y to a
  // turn, and a click landing in a gap left by an undrawn turn resolves to the nearest row.
  m.stack.addEventListener("click", (e) => {
    if (!isPlainClick(e)) {
      return;
    }
    const bin = binAt(m, e);
    if (bin === undefined) {
      return;
    }
    e.preventDefault();
    landTurn(bin.target);
  });
  m.stack.addEventListener("keydown", (e) => {
    onStackKey(m, e);
  });
  m.stack.addEventListener("focusout", (e) => {
    if (e.relatedTarget instanceof Node && m.stack.contains(e.relatedTarget)) {
      return;
    }
    if (rovingKey !== undefined) {
      rovingKey = undefined;
      render();
    }
  });
  m.prev.addEventListener("click", () => {
    step(-1);
  });
  m.next.addEventListener("click", () => {
    step(1);
  });
}

function binAt(m: MapNodes, e: MouseEvent): TurnBin | undefined {
  const row = e.target instanceof Element ? e.target.closest(".turn-pill") : null;
  if (row !== null) {
    const key = row.getAttribute(KEY_ATTR);
    return shown.find((b) => b.key === key);
  }
  if (e.target !== m.stack || shown.length === 0) {
    return undefined;
  }
  const pitch = m.stack.clientHeight / Math.max(1, shownSlots);
  const slot = pitch > 0 ? e.offsetY / pitch - 0.5 : 0;
  let best = shown[0];
  for (const b of shown) {
    const d = Math.abs(b.at * (shownSlots - 1) - slot);
    if (best === undefined || d < Math.abs(best.at * (shownSlots - 1) - slot)) {
      best = b;
    }
  }
  return best;
}

function onStackKey(m: MapNodes, e: KeyboardEvent): void {
  if (e.key === "Escape") {
    e.preventDefault();
    getScrollEl().focus({ preventScroll: true });
    return;
  }
  if (shown.length === 0) {
    return;
  }
  const last = shown.length - 1;
  const from = focusedRow();
  let to: number;
  if (e.key === "Home") {
    to = 0;
  } else if (e.key === "End") {
    to = last;
  } else {
    const delta = KEY_STEP.get(e.key);
    if (delta === undefined) {
      return;
    }
    to = Math.min(last, Math.max(0, from + delta));
  }
  e.preventDefault();
  const bin = shown[to];
  if (bin === undefined) {
    return;
  }
  rovingKey = bin.key;
  render();
  for (const li of m.stack.children) {
    if (li.getAttribute(KEY_ATTR) === bin.key && li.firstElementChild instanceof HTMLElement) {
      li.firstElementChild.focus();
    }
  }
}

function focusedRow(): number {
  const active = document.activeElement;
  const row = active instanceof Element ? active.closest(".turn-pill") : null;
  const key = row?.getAttribute(KEY_ATTR);
  const at = shown.findIndex((b) => b.key === key);
  if (at >= 0) {
    return at;
  }
  const n = markedN();
  return n === undefined
    ? 0
    : Math.max(
        0,
        markerSlotFor(
          shown.map((b) => b.first),
          n,
        ),
      );
}

function step(dir: -1 | 1): void {
  const n = markedN();
  const current =
    n === undefined
      ? -1
      : markerSlotFor(
          shown.map((b) => b.first),
          n,
        );
  const bin = shown[current + dir];
  if (bin !== undefined) {
    landTurn(bin.target);
  }
}

function landTurn(s: TurnSummary): void {
  // BEFORE the jump and unconditionally: the jump is allowed to do nothing and the pick still has
  // to produce a reaction. `s` comes off the merged set, so the pick's invariant holds here.
  selectedID = s.id;
  rovingKey = undefined;
  holdIntent();
  render();
  void navigateToTurn(s);
}

/** Land on turn `n` of `id`, the way a map click does: a `#turn-<n>` link's
 *  door. Points the map at the chat and fetches its index when it lacks `n`, so
 *  a cold link pages history in. Answers whether the turn was found. */
export async function jumpToTurn(id: string, n: number): Promise<boolean> {
  if (id !== chatID) {
    await loadTurnRail(id);
  }
  let s = summaries.find((x) => x.n === n);
  if (s === undefined) {
    await refreshTurnRail(id);
    s = summaries.find((x) => x.n === n);
  }
  if (s === undefined || id !== chatID) {
    return false;
  }
  selectedID = s.id;
  holdIntent();
  render();
  await navigateToTurn(s);
  return true;
}

/** The NUMBER of the turn the map claims the reader is at: their own pick while they
 *  hold one, the scroll-derived turn otherwise, resolved through the resident
 *  projection first and the index second, and latched. */
function markedN(): number | undefined {
  const id = selectedID ?? activeID;
  const n = residentN.get(id) ?? summaryByID.get(id)?.n;
  if (n !== undefined) {
    heldN = n;
  }
  return heldN;
}

/** Drop the reader's pick and repaint, if there was one to drop. */
function clearSelection(): void {
  releaseIntent();
  if (selectedID === undefined) {
    return;
  }
  selectedID = undefined;
  render();
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

/** The on-demand body build for a stub turn, injected by messages.ts at mount (a
 *  static import back would cycle). Inert until wired, so a map built in a test
 *  renders without the transcript. `activeView` scopes the card lookup to the ACTIVE
 *  transcript view: with parked views resident the same key exists once per view. */
let mountTurnBody: (chatID: string, turnID: string) => Promise<void> = () => Promise.resolve();
let activeView: () => HTMLElement | null = () => null;

export function initTurnRailCallbacks(cbs: {
  mountTurnBody: (chatID: string, turnID: string) => Promise<void>;
  activeView?: () => HTMLElement | null;
}): void {
  mountTurnBody = cbs.mountTurnBody;
  if (cbs.activeView !== undefined) {
    activeView = cbs.activeView;
  }
}

/** The jump that owns the scroller. A SUPERSEDED jump may not act: closing the epoch from its
 *  `finally` hands the reader to the live edge mid-flight. */
let jumpGeneration = 0;

function ownsJump(gen: number): boolean {
  return gen === jumpGeneration;
}

/** Supersede every jump in flight, so one cannot tear down state that has since
 *  become another chat's. */
function invalidateJumps(): void {
  jumpGeneration++;
}

/** Whether the click's intent is suppressing offset-driven activation. */
let intentOpen = false;
let intentTimer = 0;

function holdIntent(): void {
  releaseIntent();
  intentOpen = true;
  intentTimer = window.setTimeout(() => {
    intentTimer = 0;
    releaseIntent();
  }, PICK_BUDGET_MS);
}

function releaseIntent(): void {
  if (intentTimer !== 0) {
    clearTimeout(intentTimer);
    intentTimer = 0;
  }
  intentOpen = false;
}

/** Jump to a turn: page it in if needed, build its body, scroll once, then correct until its top
 *  sits on the reading line. ONE branch point (paging); the body build runs on both paths first, since
 *  a body landing mid-animation would move the target under the flight. */
async function navigateToTurn(s: TurnSummary, behavior = jumpBehavior()): Promise<void> {
  // A second click on a turn that is already paging IS that jump, not another one:
  // claiming a generation here would supersede the operation this click is waiting
  // on, so the page would land and nothing would scroll to it.
  if (pending.has(s.id)) {
    return;
  }
  const gen = ++jumpGeneration;
  try {
    if (turnCard(s.id) === null && !(await pageIn(s))) {
      return;
    }
    if (!ownsJump(gen)) {
      return;
    }
    // A rejected build still scrolls: the body arrives on a later paint.
    await mountTurnBody(chatID, s.id).catch(() => undefined);
    if (!ownsJump(gen)) {
      return;
    }
    await nextFrame();
    if (!ownsJump(gen)) {
      return;
    }
    const card = turnCard(s.id);
    if (card === null) {
      return;
    }
    // The epoch opens HERE rather than at the paging step, so its own backstop can
    // never expire inside `PAGE_BUDGET_MS` and leave the scroll, the corrections and
    // the release running with no epoch at all.
    beginSelfScroll();
    const landing = landingFor(card);
    if (landing === null) {
      return;
    }
    scrollToOffset(landing, behavior);
    markRailTarget(card);
    await correctLanding(card, gen);
  } finally {
    // Per id and unconditional: a superseded jump still owns the pending mark it
    // set. The epoch and the pick belong to whoever owns the scroller, and the
    // epoch closes from here because one left open suspends pagination and silences
    // every reader gesture indefinitely.
    pending.delete(s.id);
    if (ownsJump(gen)) {
      endSelfScroll();
      releaseIntent();
      render();
      schedulePick();
    }
  }
}

/** Where the scroller has to sit for `card`'s top to land on the reading line. */
function landingFor(card: HTMLElement): number | null {
  const top = scrollFrameTop(card);
  return top === null ? null : top - readingLineOffset();
}

/** Page history in until the turn is resident, marking the turn while it waits. */
async function pageIn(s: TurnSummary): Promise<boolean> {
  if (pending.has(s.id)) {
    return false;
  }
  pending.add(s.id);
  render();
  return loadUntilResident(s, Date.now() + PAGE_BUDGET_MS);
}

/** Page backwards until the target turn is in the store. Two termination conditions and
 *  a wall-clock bound: the store reports no more history, a page made no progress, or
 *  the budget is spent. `?before=<turn_id>` pages by whole turns, so the oldest resident
 *  turn id is both the residency test and the next page's cursor. */
async function loadUntilResident(s: TurnSummary, deadline: number): Promise<boolean> {
  const [{ getActive }, { loadMessages }] = await Promise.all([
    import("./store.js"),
    import("./store-load.js"),
  ]);
  for (;;) {
    const session = getActive();
    if (session?.id !== chatID) {
      return false;
    }
    if (session.turns.has(s.id)) {
      return true;
    }
    if (!session.has_more) {
      return false;
    }
    if (Date.now() > deadline) {
      // A budget spent on a store that still reports more history is a regression
      // signal rather than an ordinary end of the session.
      console.warn("turn map: paging budget spent before the turn became resident", s.n);
      return false;
    }
    const oldest = session.turn_order[0];
    if (oldest === undefined) {
      return false;
    }
    await loadMessages(chatID, oldest);
    const after = getActive();
    if (after === undefined || after.turn_order[0] === oldest) {
      return false;
    }
  }
}

/** Re-measure after each settle and jump the remainder. Never a second smooth
 *  animation: a correction closes the gap the first one could not see, because the
 *  page load and the body build moved the target after it was aimed at. */
async function correctLanding(card: HTMLElement, gen: number): Promise<void> {
  for (let i = 0; i < MAX_CORRECTIONS; i++) {
    await settled();
    if (!ownsJump(gen)) {
      return;
    }
    const landing = landingFor(card);
    if (landing === null) {
      return;
    }
    if (Math.abs(getScrollEl().scrollTop - landing) <= LANDING_TOLERANCE_PX) {
      return;
    }
    scrollToOffset(landing, "auto");
  }
  console.warn("turn map: the landing did not settle within the correction budget");
}

/** Resolve on `scrollend` or at `PICK_SETTLE_MS`, whichever comes first. */
function settled(): Promise<void> {
  return new Promise<void>((resolve) => {
    const scroller = getScrollEl();
    let timer = 0;
    const finish = (): void => {
      if (timer !== 0) {
        clearTimeout(timer);
        timer = 0;
      }
      scroller.removeEventListener("scrollend", finish);
      resolve();
    };
    timer = window.setTimeout(finish, PICK_SETTLE_MS);
    scroller.addEventListener("scrollend", finish, { once: true });
  });
}

function jumpBehavior(): ScrollBehavior {
  const reduced =
    typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  return reduced ? "auto" : "smooth";
}

/** The mounted card for the turn whose opening message is `id`, or null when it is
 *  not resident. Scoped to the ACTIVE transcript view, because the reconcile key
 *  repeats once per resident view and a document-wide query answers in document
 *  order. The document fallback keeps the map fixtures working unscoped. */
function turnCard(id: string): HTMLElement | null {
  if (id === "") {
    return null;
  }
  const selector = `[data-reconcile-key="${CSS.escape(id)}"]`;
  const view = activeView();
  return (view ?? document).querySelector<HTMLElement>(selector);
}

/** How long the landing card wears its ring. Long enough to be seen, short enough
 *  not to read as a persistent selected state — the current row carries that. */
const RAIL_TARGET_MS = 1000;

/** The card currently wearing `data-rail-target`, and the timer that removes it.
 *  Single-slot: a second click has to reset the first's timer, or the earlier
 *  deadline strips the ring off the card the reader just landed on. */
let railTarget: HTMLElement | undefined;
let railTargetTimer = 0;

/** Flash the ring on the landed card, so a jump to a card already on screen is visible. `outline`
 *  only: it must shift no layout. */
function markRailTarget(card: HTMLElement): void {
  clearRailTarget();
  railTarget = card;
  card.dataset["railTarget"] = "";
  railTargetTimer = window.setTimeout(() => {
    railTargetTimer = 0;
    clearRailTarget();
  }, RAIL_TARGET_MS);
}

function clearRailTarget(): void {
  if (railTargetTimer !== 0) {
    clearTimeout(railTargetTimer);
    railTargetTimer = 0;
  }
  if (railTarget !== undefined) {
    delete railTarget.dataset["railTarget"];
    railTarget = undefined;
  }
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => {
        resolve();
      });
      return;
    }
    setTimeout(resolve, 0);
  });
}
