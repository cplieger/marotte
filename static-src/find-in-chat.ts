// Find in Chat (Ctrl-F / Cmd-F): a search overlay scoped to the active chat. A local DOM pass marks what is rendered;
// the server's answer is the honest count and the step list, and stepping reveals hits the DOM cannot mark.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { createPopup } from "@cplieger/ui-primitives/popup";
import type { PopupController } from "@cplieger/ui-primitives/popup";
import { $, byId } from "./dom.js";
import { jumpTo, onTranscriptMutate } from "./scroll.js";
import { runServerSearch, resetServerSearch, revealHitTurn } from "./chat-search.js";
import { getActive, getActiveId } from "./store.js";
import { runOffsetOf } from "./messages-blocks.js";
import { loadMessages } from "./store-load.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import { FindEngine } from "./find-engine.js";
import { classify, cursorCount, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns, Tally } from "./textsearch/copy.js";
import type { SearchShell } from "./search-shell.js";
import type { Hit, SearchResult, SegmentKind } from "./wire/types.gen.js";

// Coalesces a burst of streamed chunks. The typing debounce is search-shell.ts's SEARCH_DEBOUNCE_MS.
const RERUN_DEBOUNCE_MS = 150;

/** The transcript's unit nouns: a hit is a match, and the scan reads messages. */
const NOUNS: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "message", many: "messages" },
};

/** The tally of no answer: nothing read, nothing matched, nothing cut. */
const NO_ANSWER: Tally = { scanned: 0, matched: 0, truncated: false };

let overlayEl: HTMLElement | null = null;
let shell: SearchShell | null = null;
let popup: PopupController | null = null;
let countEl: HTMLElement | null = null;
let engine: FindEngine | null = null;
let lastFocus: HTMLElement | null = null;
let rerunTimer: ReturnType<typeof setTimeout> | undefined;

/** The active transcript view, by the multiplexer's class contract rather than an import (messages.ts sits above this). */
function findRoot(): HTMLElement {
  return $.messages.querySelector<HTMLElement>(":scope > .transcript-view.is-active") ?? $.messages;
}
/** Unregister for the re-run's ride on scroll.ts's shared MutationObserver; null while closed. */
let unobserveTranscript: (() => void) | null = null;
/** Engine ops in flight whose own writes must not re-trigger the re-run; a counter so nested calls stay safe. */
let engineWrites = 0;
/** Unsubscribe for the tab teardown, so a rebuilt module cannot stack subscribers. */
let unsubTab: (() => void) | null = null;

// The step list is the server's answer whenever there is one for the text in the box.

/** The last successful answer, in wire order; the walk reads `stepOrder`. A failed re-fetch keeps it (chat-search.ts's rule). */
let serverHits: Hit[] = [];
/** Adopted with `serverHits` in one write; `matched` is the whole-chat count, cut iff it exceeds the list. */
let serverTally: Tally = NO_ANSWER;
/** The query whose answer stands, written only in `render` and read by `step`, `updateCounter` and the note. */
let serverHitsQuery = "";
/** `[...local, ...crossTab]`, each in wire order, so in-place hits come before those opening another view. */
let stepOrder: Hit[] = [];
/** `stepOrder[localCount]` is the first cross-tab hit, so the cursor reaching it
 *  IS the crossing. */
let localCount = 0;
/** The boundary sentence fires once per answer, not once per crossing: a reader
 *  who wraps round does not need telling twice. */
let announcedBoundary = false;
/** Position in `stepOrder` while stepping drives navigation; -1 = none. */
let hitCursor = -1;
/** One navigation at a time: paging spans awaits, and a second Enter would race the first's reveal. */
let navBusy = false;
/** This client caused the coming tab change; written just before the lazy import, since the switch tears the overlay down. */
let crossTabJump = false;
/** The hit the reader left on, as a keyenc identity (the chat may grow while away, so not an index). */
let resumeKey = "";
/** A handoff asked the next answer to land on `resumeKey`'s hit, not just restore the cursor. */
let landOnResume = false;

/** Partitioned once by the hit's `lane`, the predicate `navigateToHit` routes on. */
function buildStepOrder(hits: Hit[]): Hit[] {
  const isLocal = (h: Hit): boolean => (h.lane ?? "") === "";
  const local = hits.filter(isLocal);
  localCount = local.length;
  return [...local, ...hits.filter((h) => !isLocal(h))];
}

/** The resume position in the fresh `stepOrder`, or -1; spends the key either way so a stale identity cannot apply later. */
function resumeIndex(): number {
  const key = resumeKey;
  resumeKey = "";
  if (key === "") {
    return -1;
  }
  return stepOrder.findIndex((h) => hitKey(h) === key);
}

/** The hit's identity, for the cross-tab resume. */
function hitKey(hit: Hit): string {
  return join(hit.turn_id, hit.entry_id, hit.segment_kind, String(hit.offset));
}

/** Open state lives on the popup and nowhere else, so every close path tears down fully. */
function isOpen(): boolean {
  return popup?.isOpen === true;
}

function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** SVG glyphs, so `align-items: center` centres the ink (see search-shell.ts's searchIconButton). */
function navButton(
  label: string,
  hint: string,
  icon: string,
  onClick: () => void,
): HTMLButtonElement {
  return searchIconButton("chat-find-btn", label, hint, icon, onClick);
}

function ensureBuilt(): void {
  if (overlayEl !== null) {
    return;
  }
  engine = new FindEngine(findRoot());

  // A div with `search-status`: the counter is a line under the controls (24-find.css) that collapses while empty.
  const count = el("div", {
    id: "chat-find-count",
    className: "chat-find-count search-status",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
  countEl = count;

  const prevBtn = navButton("Previous match", "Previous (Shift+Enter)", ICON_CHEVRON_UP, () => {
    step(-1);
  });
  const nextBtn = navButton("Next match", "Next (Enter)", ICON_CHEVRON_DOWN, () => {
    step(1);
  });

  // The counter and prev/next are this surface's alone (a cursor has a document position), so they come through `compose`.
  const built = createSearchShell<SearchResult>({
    id: "chat-find",
    regionClass: "chat-find search-pop uip-popup",
    inputClass: "chat-find-input",
    buttonClass: "chat-find-btn",
    caseClass: "chat-find-case",
    label: "Find in conversation",
    placeholder: "Find in chat\u2026",
    inputTitle: "Find in chat. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    closeButton: true,
    // Required: the shell defaults to `search-note`, so without it no `.chat-find-note` rule matches.
    note: true,
    noteClass: "chat-find-note",
    // The counter is a sibling of the row: it holds sentences, which painted over the buttons beside it.
    compose: ({ input, caseButton, closeButton, note }) => [
      el(
        "div",
        { className: "chat-find-row" },
        input,
        el("div", { className: "chat-find-nav" }, caseButton, prevBtn, nextBtn, closeButton),
      ),
      count,
      note,
    ],
    query: async (query, ctx) => {
      if (engine === null) {
        return null;
      }
      // The local pass runs first and synchronously; the server pre-pass makes the count honest (the walker prunes hidden content).
      searchNow(query, ctx.caseSensitive);
      updateCounter(query);
      // Not while a handoff's landing is pending: that open is going elsewhere.
      if (!landOnResume) {
        revealCurrent();
      }
      return runServerSearch(getActiveId(), query, ctx.caseSensitive);
    },
    render: (result, query) => {
      // The one writer of the standing answer: seven values move together or none does.
      let land = false;
      if (result !== null) {
        serverHits = result.matches;
        serverTally = result;
        stepOrder = buildStepOrder(result.matches);
        hitCursor = resumeIndex();
        land = landOnResume;
        landOnResume = false;
        serverHitsQuery = query;
        announcedBoundary = false;
      }
      const owned = serverHitsQuery === query;
      // Above the early return: a query refined to zero hits must clear the cut sentence.
      shell?.setNote(owned ? cutNote() : "");
      if (result === null || result.matches.length === 0) {
        // Repaint even with nothing to walk: ownership moved, and the session figure and miss skin are gated on it.
        updateCounter(query);
        return;
      }
      // Re-run over the revealed DOM so marks and count cover the opened turns.
      searchNow(query, shell?.caseSensitive ?? false);
      updateCounter(query);
      const target = land ? stepOrder[hitCursor] : undefined;
      if (target !== undefined) {
        landOnHit(target);
        return;
      }
      revealCurrent();
    },
    onDismiss: () => {
      closeChatFind();
    },
    onSubmit: (shift) => {
      step(shift ? -1 : 1);
    },
  });
  shell = built;

  built.input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      step(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      step(-1);
    }
  });

  // Hidden before the first open: the popup writes `[hidden]` only at a leave's end, so a fresh region would be visible.
  built.region.hidden = true;
  byId("messages-wrap-outer").appendChild(built.region);
  overlayEl = built.region;

  // The popup owns dismissal, Escape, ARIA, single-open and the is-open/is-leaving animation pair.
  popup = createPopup(built.region, {
    trigger: document.getElementById("find-btn"),
    group: "app-search",
    isolateEscape: false,
    haspopup: "dialog",
    onOpen: () => {
      // Re-root on the active view: parked sibling views hold other chats' text.
      if (engine !== null && engine.root !== findRoot()) {
        applyEngine(() => {
          engine?.clear();
        });
        engine = new FindEngine(findRoot());
      }
      startObserving();
      // aria-pressed: find is a toggle, and `.active` means "this singleton tab is active" (tabs.ts).
      document.getElementById("find-btn")?.setAttribute("aria-pressed", "true");
      built.focus();
      built.run();
    },
    onClose: () => {
      teardown();
      document.getElementById("find-btn")?.setAttribute("aria-pressed", "false");
      const target = lastFocus;
      lastFocus = null;
      if (target?.isConnected === true) {
        target.focus();
      }
    },
  });

  // A tab switch closes the search and forgets the query, or the next chat's open searches with it.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    // Read one-shot before the close; teardown leaves it standing.
    const ours = crossTabJump;
    crossTabJump = false;
    closeChatFind();
    if (shell !== null && !ours) {
      shell.input.value = "";
    }
  });
}

/** Release everything the open state owns, from the popup's onClose so it runs on every close path. */
function teardown(): void {
  stopObserving();
  shell?.cancel();
  cancelRerun();
  applyEngine(() => {
    engine?.clear();
  });
  // `crossTabJump` and `resumeKey` stay standing: they describe the journey out, not the open.
  serverHits = [];
  serverTally = NO_ANSWER;
  stepOrder = [];
  localCount = 0;
  announcedBoundary = false;
  serverHitsQuery = "";
  hitCursor = -1;
  // Cleared here: a handoff arms it after the switch's own close, so this cannot delete it early.
  landOnResume = false;
  resetServerSearch();
  shell?.setNote("");
  updateCounter("");
}

/** Run an engine op without its own marks re-triggering the live re-run. */
function applyEngine(fn: () => void): void {
  engineWrites++;
  try {
    fn();
  } finally {
    queueMicrotask(() => {
      engineWrites--;
    });
  }
}

function step(dir: 1 | -1): void {
  if (engine === null || shell === null) {
    return;
  }
  // A changed query searches first, landing on the first match like native find.
  if (engine.query !== shell.value) {
    shell.run();
    return;
  }
  // The spine: an owned server answer is the step list, whatever the walker marked.
  if (serverHits.length > 0 && serverHitsQuery === shell.value) {
    stepServerHit(dir);
    return;
  }
  applyEngine(() => {
    if (dir === 1) {
      engine?.next();
    } else {
      engine?.prev();
    }
  });
  updateCounter(shell.value);
  revealCurrent();
}

/** The query is a parameter, so a count cannot describe another search. */
function updateCounter(query: string): void {
  if (countEl === null || engine === null) {
    return;
  }
  const owned = serverHitsQuery === query;
  // Two grammars, one owner: a session-list position while stepping, else the DOM position with the session figure.
  if (stepsOwnedList(query)) {
    countEl.textContent = steppedCounter();
    overlayEl?.classList.remove("chat-find-no-results");
    return;
  }
  // The session tally is ownership-gated, like the note.
  const session = owned ? serverTally : NO_ANSWER;
  countEl.textContent = domCounter(engine, query, session);
  // The miss skin claims the text is nowhere, so it needs an owned answer saying so.
  const noResults = query !== "" && engine.total === 0 && owned && serverTally.matched === 0;
  overlayEl?.classList.toggle("chat-find-no-results", noResults);
}

function revealCurrent(): void {
  if (engine === null) {
    return;
  }
  const mark = engine.currentMark();
  if (mark === null) {
    return;
  }
  // Freeze auto-scroll so streaming does not yank the view, only when the jump leaves the live edge (`jumpTo`'s rule).
  jumpTo(mark, {
    block: "center",
    inline: "nearest",
    behavior: prefersReducedMotion() ? "auto" : "smooth",
  });
}

// Server-hit navigation: page the message in, reveal its turn, open the disclosure chain, re-walk, pick the mark.

/** Generous (10000 messages at 50 a page); the cap only bounds a server that never says `has_more: false`. */
const HIT_PAGE_CAP = 200;

/** Below this similarity the candidate is the same text elsewhere, so the block is selected instead. */
const SIM_FLOOR = 0.3;

/** Past the CSS animation, so the class strip is cleanup. */
const FLASH_FALLBACK_MS = 2000;

/** Explains the partition once per answer, on the press that first crosses into phase 2. */
const BOUNDARY_NOTICE = "the rest are in delegate pages and run tabs";

function stepServerHit(dir: 1 | -1): void {
  if (navBusy || stepOrder.length === 0) {
    return;
  }
  hitCursor =
    hitCursor === -1
      ? dir === 1
        ? 0
        : stepOrder.length - 1
      : (hitCursor + dir + stepOrder.length) % stepOrder.length;
  const hit = stepOrder[hitCursor];
  if (hit === undefined) {
    return;
  }
  landOnHit(hit);
}

/** Synchronous landing first: `navBusy` drops Enters while the async pipeline runs. */
function landOnHit(hit: Hit): void {
  if (landInPlace(hit)) {
    return;
  }
  navBusy = true;
  void navigateToHit(hit).finally(() => {
    navBusy = false;
  });
}

/** Land on `hit` with no await, or answer false for the async pipeline; only where nothing must be fetched or opened. */
function landInPlace(hit: Hit): boolean {
  if (engine === null || shell === null) {
    return false;
  }
  if ((hit.lane ?? "") !== "" || hit.segment_kind === "entry") {
    return false;
  }
  const target = entryElement(hit.turn_id, hit.entry_id);
  if (target === null) {
    return false;
  }
  const walkTarget = narrowToolTarget(target, hit) ?? target;
  const chosen = pickNearestMark(walkTarget, hit);
  if (chosen === -1) {
    return false;
  }
  applyEngine(() => {
    engine?.setCurrent(chosen);
  });
  // Through `updateCounter`, the one writer, so a landing cannot disagree with the next repaint.
  updateCounter(shell.value);
  revealCurrent();
  return true;
}

/** The DOM-mark counter, with the whole-chat count beside it only when it exceeds the marks. */
function domCounter(eng: FindEngine, query: string, tally: Tally): string {
  if (query === "") {
    return "";
  }
  if (eng.total === 0) {
    return emptyNote(
      classify({ matched: tally.matched, shown: 0, truncated: tally.truncated }),
      NOUNS,
    );
  }
  return cursorCount(eng.currentIndex + 1, eng.total, exceeding(tally.matched, eng.total));
}

/** The counter while stepping the server's list, with the whole-chat count only when the list was cut. */
function steppedCounter(): string {
  return cursorCount(
    hitCursor + 1,
    serverHits.length,
    exceeding(serverTally.matched, serverHits.length),
  );
}

/** `matched` when it exceeds the navigable list's length, else nothing to add. */
function exceeding(matched: number, total: number): number | undefined {
  return matched > total ? matched : undefined;
}

/** The note under a cut answer; "" when the list holds it whole. */
function cutNote(): string {
  return serverTally.matched > serverHits.length
    ? scanNote(serverTally, serverHits.length, NOUNS)
    : "";
}

/** Whether the stepped grammar is honest for `query`: one owner for the counter and the landing gate. */
function stepsOwnedList(query: string): boolean {
  return hitCursor >= 0 && serverHits.length > 0 && serverHitsQuery === query;
}

/** Paint the stepped position with suffixes, gated here so a press that lost ownership paints nothing. */
function paintHitPosition(...suffixes: (string | (() => string))[]): void {
  if (countEl === null || !stepsOwnedList(shell?.value ?? "")) {
    return;
  }
  // Thunks are produced after the gate, so `takeBoundaryNotice()` does not spend its latch on a gated-off paint.
  const parts = suffixes.map((s) => (typeof s === "function" ? s() : s)).filter((s) => s !== "");
  countEl.textContent = [steppedCounter(), ...parts].join(" \u00b7 ");
}

/** Spends the once-per-answer latch. */
function takeBoundaryNotice(): string {
  // With no phase 1 there is nothing to cross from.
  if (announcedBoundary || localCount === 0 || hitCursor !== localCount) {
    return "";
  }
  announcedBoundary = true;
  return BOUNDARY_NOTICE;
}

/** Every caller is post-await, which is why it re-checks ownership. */
function showHitNotice(suffix: string): void {
  paintHitPosition(suffix);
}

async function navigateToHit(hit: Hit): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "" || engine === null) {
    return;
  }
  // A delegate's hit opens its tab, the only surface rendering its entries.
  const lane = hit.lane ?? "";
  if (lane !== "") {
    // Painted before the open: the tab switch tears the overlay down.
    paintHitPosition(takeBoundaryNotice, "opening the delegate's page");
    // Both resume values just before the lazy import; teardown exempts them because it runs in the same turn.
    crossTabJump = true;
    resumeKey = hitKey(hit);
    try {
      const { openSubagentView } = await import("./subagent-view.js");
      void openSubagentView(chatID, lane);
    } catch {
      if (isOpen()) {
        showHitNotice("could not be opened");
      }
    }
    return;
  }
  if (!(await ensureHitResident(chatID, hit))) {
    if (isOpen()) {
      showHitNotice("could not be loaded");
    }
    return;
  }
  await revealHitTurn(chatID, hit);
  // Closed (or switched away) while paging in: the surface this would select
  // on is gone, and teardown already reset the reveal.
  if (!isOpen() || shell === null) {
    return;
  }
  // The two turn-level kinds resolve from the turn card ahead of the row lookup: neither renders in the message row.
  if (hit.segment_kind === "turn_failure" || hit.segment_kind === "attachment") {
    const card = turnCardEl(hit.turn_id);
    if (card === null) {
      // The same sentence the row path answers, for the same reason: the surface
      // this hit names is not on screen and navigation cannot invent it.
      showHitNotice("could not be shown");
      return;
    }
    const region =
      hit.segment_kind === "turn_failure"
        ? card.querySelector<HTMLElement>(":scope > .turn-notice")
        : card.querySelector<HTMLElement>(":scope > .turn-header .turn-req-attachments");
    // Both regions sit in the card's resting state, so no disclosure chain.
    await landOrNotice(region ?? card, hit, region !== null);
    return;
  }
  const target = entryElement(hit.turn_id, hit.entry_id);
  if (target === null) {
    showHitNotice("could not be shown");
    return;
  }
  // An `entry` hit gets container navigation, which keeps the ranker's segment_len division unreachable.
  if (hit.segment_kind === "entry") {
    selectContainer(target);
    updateCounter(shell.value);
    return;
  }
  openDisclosureChain(target, hit);
  // A preview-less diff card loads its diff on open, so the text does not exist until the bulk lands.
  const arriving = awaitDeferredDiff(target, hit);
  if (arriving !== null) {
    await arriving;
    // Closed, or the transcript repainted this element away, while the bulk was in
    // flight: the same guard the residency and frame awaits already take.
    if (!isOpen() || !target.isConnected) {
      return;
    }
  }
  // Narrow after the chain opened: the input `<pre>` and denial block are built by `detailsBody` on first open.
  const narrowed = narrowToolTarget(target, hit);
  await landOrNotice(narrowed ?? target, hit, narrowed !== null);
}

/** Walk, select the credible mark, or select the region and say what happened; shared by every resolution path. */
async function landOrNotice(walkTarget: HTMLElement, hit: Hit, narrowed: boolean): Promise<void> {
  // A landing may not move the reader once this press lost the list: every effect below is a visible write.
  if (!stepsOwnedList(shell?.value ?? "")) {
    return;
  }
  // Re-walk now that the chain is open: the marks inside it exist only after
  // the walker can see the text.
  let chosen = walkAndPick(walkTarget, hit);
  // Whether the second walk had a rendered frame to walk. A first-walk hit never
  // asks, and never reaches the notice below either.
  let rendered = true;
  if (chosen === -1) {
    // A miss may mean not rendered: the transcript's heavy cards are `content-visibility: auto` (css/14-tools.css), so
    // jump and wait one rendered frame.
    jumpTo(walkTarget, { block: "center", inline: "nearest", behavior: "auto" });
    rendered = await nextRender();
    // Closed, repainted away, or no longer owning the answer during the frame.
    if (!isOpen() || !walkTarget.isConnected || !stepsOwnedList(shell?.value ?? "")) {
      return;
    }
    chosen = walkAndPick(walkTarget, hit);
  }
  if (chosen === -1) {
    // Syntax-only match, low similarity or elsewhere: select the narrowed region and say why.
    selectContainer(walkTarget);
    showHitNotice(missNotice(hit, rendered, narrowed));
    return;
  }
  applyEngine(() => {
    engine?.setCurrent(chosen);
  });
  updateCounter(shell?.value ?? "");
  revealCurrent();
}

/** The walk makes marks exist inside content that only just became visible. */
function walkAndPick(target: HTMLElement, hit: Hit): number {
  searchNow(shell?.value ?? "", shell?.caseSensitive ?? false);
  return pickNearestMark(target, hit);
}

/** Four 60Hz frames: a busy frame keeps its re-walk, and the fallback is no felt stall. */
const RENDER_WAIT_CEILING_MS = 64;

/** One rendered frame or the ceiling; `true` when a frame was delivered, which makes a following miss conclusive. */
function nextRender(): Promise<boolean> {
  return new Promise((resolve) => {
    const ceiling = setTimeout(() => {
      resolve(false);
    }, RENDER_WAIT_CEILING_MS);
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        clearTimeout(ceiling);
        resolve(true);
      });
    });
  });
}

/** Page history until the hit's turn is resident, bounded by `has_more`, a no-progress check and a page cap. */
async function ensureHitResident(chatID: string, hit: Hit): Promise<boolean> {
  const resident = (): boolean => getActive()?.turns.has(hit.turn_id) === true;
  for (let pages = 0; !resident(); pages++) {
    const session = getActive();
    if (session?.has_more !== true || session.id !== chatID || pages >= HIT_PAGE_CAP) {
      return false;
    }
    const oldest = session.turn_order[0];
    if (!(await loadMessages(chatID, oldest))) {
      return false;
    }
    // No progress: the server answered but the window's edge did not move, so
    // more requests would loop on the same answer.
    if (getActive()?.turn_order[0] === oldest) {
      return false;
    }
  }
  return true;
}

/** The rendered TURN CARD for a turn id, which every hit carries and which is the
 *  outer reconcile's own key. Null when that turn is not mounted in the active
 *  view. */
function turnCardEl(turnID: string): HTMLElement | null {
  return findRoot().querySelector<HTMLElement>(`.turn[data-reconcile-key="${CSS.escape(turnID)}"]`);
}

/** The hit's entry element in its own turn card; null when not mounted. Two stamps, for a prose run's members. */
function entryElement(turnID: string, entryID: string): HTMLElement | null {
  const card = turnCardEl(turnID);
  if (card === null) {
    return null;
  }
  const id = CSS.escape(entryID);
  const stamped =
    card.querySelector<HTMLElement>(`[data-entry-id="${id}"]`) ??
    card.querySelector<HTMLElement>(`[data-entries~="${id}"]`);
  if (stamped === null) {
    return null;
  }
  // Both shapes answer with the bubble, which every consumer flashes, walks and jumps to.
  return stamped.querySelector<HTMLElement>(":scope > .message") ?? stamped;
}

/** Which region inside a tool card each kind's text renders in; a kind with no row walks the whole card. */
const TOOL_DIFF_PREVIEW = ".tool-diff-preview";
const TOOL_TARGET: Partial<Record<SegmentKind, string>> = {
  tool_title: ".tool-title",
  tool_disclosed: ".tool-title",
  tool_diff: TOOL_DIFF_PREVIEW,
  tool_denial: ".tool-denial",
  tool_input: ".tool-input",
  tool_output: ".tool-output",
};

/** Without narrowing six kinds would share the whole `.tool-call` as one walk target. */
function narrowToolTarget(target: HTMLElement, hit: Hit): HTMLElement | null {
  const selector = TOOL_TARGET[hit.segment_kind];
  if (selector === undefined) {
    return null;
  }
  const card = target.closest<HTMLElement>(".tool-call") ?? target;
  return card.querySelector<HTMLElement>(selector);
}

/** Three states: no frame delivered (not rendered), rendered but not found, or a diff-specific sentence. */
function missNotice(hit: Hit, rendered: boolean, narrowed: boolean): string {
  if (!rendered) {
    return "not rendered yet";
  }
  if (hit.segment_kind === "tool_diff") {
    return narrowed
      ? "not in the shown hunks, so open the diff"
      : "only in this call's diff, which did not load";
  }
  return "not in rendered text";
}

/** Kinds rendered inside `.tool-details`, where `detailsBody` builds the input `<pre>` and denial block on first open. */
const OPENS_TOOL_DETAILS = new Set<SegmentKind>(["tool_output", "tool_input", "tool_denial"]);

/** The single owner of that click (the disclosure chain and `awaitDeferredDiff` both need it). */
function openToolCardDetails(card: HTMLElement): void {
  const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
  if (toggle?.getAttribute("aria-expanded") === "false") {
    activateQuietly(toggle);
  }
}

/** Activate without the click reaching the document: this popup's outside-dismissal would close the search. */
function activateQuietly(control: HTMLElement): void {
  const stop = (e: Event): void => {
    e.stopPropagation();
  };
  control.addEventListener("click", stop);
  try {
    control.click();
  } finally {
    control.removeEventListener("click", stop);
  }
}

/** Open every closed disclosure between the element and its turn body, through each control's real path. */
function openDisclosureChain(target: HTMLElement, hit: Hit): void {
  for (let cur: HTMLElement | null = target; cur !== null; cur = cur.parentElement) {
    if (cur instanceof HTMLDetailsElement && !cur.open) {
      cur.open = true;
    }
    if (cur.classList.contains("tool-group") && cur.classList.contains("tool-group-collapsed")) {
      const header = cur.querySelector<HTMLElement>(":scope > .tool-group-header");
      if (header !== null) {
        activateQuietly(header);
      }
    }
    if (cur.classList.contains("turn-body")) {
      break;
    }
  }
  const card = OPENS_TOOL_DETAILS.has(hit.segment_kind)
    ? target.closest<HTMLElement>(".tool-call")
    : null;
  if (card !== null) {
    openToolCardDetails(card);
  }
}

/** `stepServerHit` drops Enters while `navBusy` is latched, so this bounds a swallowed keystroke. */
const DEFERRED_DIFF_WAIT_MS = 1500;

/** Bounds a dead diff hit to one ceiling per card rather than per visit. */
const openBroughtNoDiff = new WeakSet<HTMLElement>();

/** Open a preview-less `tool_diff` card and wait, bounded, for its diff; null when nothing is coming. */
function awaitDeferredDiff(target: HTMLElement, hit: Hit): Promise<void> | null {
  if (hit.segment_kind !== "tool_diff") {
    return null;
  }
  const card = target.closest<HTMLElement>(".tool-call");
  if (card === null) {
    return null;
  }
  // A diff on screen is the resting path; a card that already came back empty answers at once.
  if (
    card.querySelector(TOOL_DIFF_PREVIEW) !== null ||
    openBroughtNoDiff.has(card) ||
    card.querySelector(".tool-disclosure") === null
  ) {
    return null;
  }
  openToolCardDetails(card);
  return waitForDiffPreview(card);
}

/** A MutationObserver, not a poll: the arrival is one `insertBefore`. */
function waitForDiffPreview(card: HTMLElement): Promise<void> {
  return new Promise((resolve) => {
    const obs = new MutationObserver((_records, observer) => {
      if (card.querySelector(TOOL_DIFF_PREVIEW) === null) {
        return;
      }
      clearTimeout(ceiling);
      observer.disconnect();
      resolve();
    });
    const ceiling = setTimeout(() => {
      obs.disconnect();
      openBroughtNoDiff.add(card);
      resolve();
    }, DEFERRED_DIFF_WAIT_MS);
    obs.observe(card, { childList: true, subtree: true });
  });
}

/** Excerpt similarity first (raw markdown vs rendered text), then position. -1 means no credible mark. */
function pickNearestMark(target: HTMLElement, hit: Hit): number {
  const excerptTokens = tokenSet(hit.excerpt);
  const offsets = markOffsets(target);
  const want = wantFraction(hit);
  let best = -1;
  let bestSim = -1;
  let bestDist = Infinity;
  // A multi-node hit shares one `data-hit`; its first piece is the candidate.
  const seen = new Set<number>();
  for (const mark of target.querySelectorAll<HTMLElement>("mark.find-hit")) {
    const index = Number(mark.getAttribute("data-hit"));
    if (seen.has(index)) {
      continue;
    }
    seen.add(index);
    const at = offsets.marks.get(mark) ?? 0;
    const sim = dice(excerptTokens, tokenSet(contextAround(offsets.text, at, mark)));
    const dist = Math.abs(want - at / Math.max(1, offsets.text.length));
    if (sim > bestSim || (sim === bestSim && dist < bestDist)) {
      best = index;
      bestSim = sim;
      bestDist = dist;
    }
  }
  return bestSim >= SIM_FLOOR ? best : -1;
}

/** A prose run is one element for several entries, so the offset is re-based over the run (`runOffsetOf`). */
function wantFraction(hit: Hit): number {
  const run = runOffsetOf(hit.turn_id, hit.entry_id);
  if (run !== undefined && run.total > 0) {
    return (run.offset + hit.offset) / run.total;
  }
  return hit.segment_len > 0 ? hit.offset / hit.segment_len : 0;
}

/** UTF-16 units, not runes: both sides of the comparison are fractions. */
function markOffsets(target: HTMLElement): { text: string; marks: Map<HTMLElement, number> } {
  const marks = new Map<HTMLElement, number>();
  let text = "";
  const walk = (node: Node): void => {
    if (node.nodeType === Node.TEXT_NODE) {
      text += node.nodeValue ?? "";
      return;
    }
    if (node instanceof HTMLElement && node.matches("mark.find-hit")) {
      marks.set(node, text.length);
    }
    for (const child of node.childNodes) {
      walk(child);
    }
  };
  walk(target);
  return { text, marks };
}

/** Twin of `chat.searchExcerptRadius` (internal/chat/search.go); the two must match. */
const EXCERPT_RADIUS = 60;

/** The rendered-side counterpart of the server's excerpt: the text around the
 *  mark, at the same radius. */
function contextAround(text: string, at: number, mark: HTMLElement): string {
  const len = mark.textContent.length;
  return text.slice(Math.max(0, at - EXCERPT_RADIUS), at + len + EXCERPT_RADIUS);
}

/** Splitting on non-alphanumerics strips markdown syntax from the comparison. */
function tokenSet(s: string): Set<string> {
  return new Set(
    s
      .toLowerCase()
      .split(/[^\p{L}\p{N}]+/u)
      .filter((t) => t !== ""),
  );
}

/** Dice coefficient over two token sets: 2·|A∩B| / (|A|+|B|). */
function dice(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 || b.size === 0) {
    return 0;
  }
  let inter = 0;
  for (const t of a) {
    if (b.has(t)) {
      inter++;
    }
  }
  return (2 * inter) / (a.size + b.size);
}

/** The class comes off on animationend, with a timer as the reduced-motion backstop. */
function selectContainer(target: HTMLElement): void {
  target.classList.add("find-target-flash");
  target.addEventListener(
    "animationend",
    () => {
      target.classList.remove("find-target-flash");
    },
    { once: true },
  );
  setTimeout(() => {
    target.classList.remove("find-target-flash");
  }, FLASH_FALLBACK_MS);
  jumpTo(target, {
    block: "center",
    inline: "nearest",
    behavior: prefersReducedMotion() ? "auto" : "smooth",
  });
}

function startObserving(): void {
  if (unobserveTranscript !== null) {
    return;
  }
  unobserveTranscript = onTranscriptMutate(() => {
    if (engineWrites > 0) {
      return; // our own mark writes; see applyEngine
    }
    scheduleRerun();
  });
}

function stopObserving(): void {
  unobserveTranscript?.();
  unobserveTranscript = null;
}

/** Walk the transcript now, superseding a pending re-run: this walk already sees the mutations that scheduled it. */
function searchNow(query: string, caseSensitive: boolean): void {
  cancelRerun();
  applyEngine(() => {
    engine?.search(query, caseSensitive);
  });
}

function cancelRerun(): void {
  if (rerunTimer !== undefined) {
    clearTimeout(rerunTimer);
    rerunTimer = undefined;
  }
}

/** Re-run so the counter stays honest, keeping the current hit and not scrolling. */
function scheduleRerun(): void {
  cancelRerun();
  rerunTimer = setTimeout(() => {
    rerunTimer = undefined;
    rerun();
  }, RERUN_DEBOUNCE_MS);
}

function rerun(): void {
  if (!isOpen() || engine === null || shell === null) {
    return;
  }
  const query = shell.value;
  applyEngine(() => {
    engine?.refresh(query, shell?.caseSensitive ?? false);
  });
  updateCounter(query);
}

function openFindInChat(): void {
  ensureBuilt();
  if (popup === null) {
    return;
  }
  if (!popup.isOpen) {
    lastFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  }
  // show() on an open popup is a no-op, so focus and re-run here.
  popup.show();
  shell?.focus();
  shell?.run();
}

/** Open the search with `query` and land on `hit`: the handoff from a surface that counted the conversation's matches. */
export function openChatFindAt(query: string, hit: Hit): void {
  ensureBuilt();
  if (shell === null) {
    return;
  }
  shell.input.value = query;
  resumeKey = hitKey(hit);
  landOnResume = true;
  openFindInChat();
}

/**
 * Close the search with the full teardown. Idempotent.
 *
 * @internal Test seam.
 * @knipignore The test loads this module through a cache-busting dynamic specifier.
 */
export function closeChatFind(): void {
  popup?.hide();
}

/** Toggle the search; what the toolbar button means. */
export function toggleChatFind(): void {
  if (!chatFindActiveContext()) {
    return;
  }
  if (isOpen()) {
    closeChatFind();
    return;
  }
  openFindInChat();
}

/** True when the chat transcript is the active context: the chat view is
 *  visible and focus is not inside the shell terminal panel. When false, the
 *  browser's native find is left untouched. */
function chatFindActiveContext(): boolean {
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return false;
  }
  const active = document.activeElement;
  if (active instanceof Element && active.closest("#shell-panel") !== null) {
    return false;
  }
  return true;
}

function findInputFocused(): boolean {
  return shell !== null && document.activeElement === shell.input;
}

/**
 * Global Ctrl-F / Cmd-F (via find-dispatch): opens or refocuses the widget when the chat view is active. A repeat press
 * while the field has focus goes to native find.
 */
export function handleFindHotkey(e: KeyboardEvent): void {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return;
  }
  // Escape hatch: let the browser's native find open on a repeat press.
  if (isOpen() && findInputFocused()) {
    return;
  }
  if (!chatFindActiveContext()) {
    return;
  }
  e.preventDefault();
  openFindInChat();
}

/**
 * @internal Test seam: whether the search is open.
 * @knipignore The test loads this module through a cache-busting dynamic specifier.
 */
export function _isChatFindOpen(): boolean {
  return isOpen();
}
