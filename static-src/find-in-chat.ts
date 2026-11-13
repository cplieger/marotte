// Find in Chat (Ctrl-F / Cmd-F): a search overlay scoped to the active chat. A hit is one item (a message, a tool card,
// a header, ...) holding every word of the query. A local DOM pass marks what is rendered; the server's answer is the
// honest count and the step list, and landing on a hit opens only that hit's containers (`search-reveal.ts`).

import { foldedHookBadge, hookRowOf } from "./hook-badge.js";
import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { createPopup } from "./legacy-ui/popup.js";
import type { PopupController } from "./legacy-ui/popup.js";
import { $, byId, maybeEl } from "./dom.js";
import { jumpInFlight, jumpTo, onTranscriptMutate, shiftScroll } from "./scroll.js";
import {
  CHAT_SEARCH_DEBOUNCE_MS,
  runServerSearch,
  resetServerSearch,
  revealHitTurn,
} from "./chat-search.js";
import { parseChatQuery } from "./chat-query.js";
import { FIND_UNIT_SELECTOR } from "./find-unit.js";
import { releaseChain, revealChain, revealTurnOnly } from "./search-reveal.js";
import { prepareMatcher, type Matcher } from "./textsearch/query.js";
import { getPainted, getPaintedId, getActiveId } from "./store.js";
import { loadOlderTurns } from "./store-load.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import { FindEngine, type UnitScope } from "./find-engine.js";
import { classify, cursorCount, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns, Tally } from "./textsearch/copy.js";
import type { SearchShell } from "./search-shell.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

// Coalesces a burst of streamed chunks. The typing pause is chat-search.ts's CHAT_SEARCH_DEBOUNCE_MS.
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
/** The identity `stampEntry` (messages-blocks.ts) writes on every entry element: a re-rendered entry keeps its hit's character position. */
const ENTRY_HOLDER = ["data-entry-turn", "data-entry-id"] as const;
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
/** Adopted with `serverHits`: no item held every word, so each hit is a turn holding them between its items. */
let serverSpread = false;
/** The query whose answer stands, written only in `render` and read by `step`, `updateCounter` and the note. */
let serverHitsQuery = "";
/** The box value the shell last ran, the search that stands until the next run: a re-run refreshes it, and Enter on
 *  any other value runs the shell, whatever a walk marked meanwhile. */
let searchedQuery = "";
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
/** What the landing on `hitCursor` said about it (`paintHitPosition`), kept through every repaint until the cursor
 *  moves, so a re-run cannot erase why the step has no mark. */
let stepNotice = "";
/** The current step landed on an element with no mark (`selectContainer`), so a re-walk makes no mark current. */
let marklessStep = false;
/** One navigation at a time: paging spans awaits, and a second Enter would race the first's reveal. */
let navBusy = false;
/** The marks in the badge list row of the folded hook the current step landed on (`markHookRow`). */
let hookRowEngine: FindEngine | null = null;
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

/** The hit's identity, for the cross-tab resume: one item is one hit. */
function hitKey(hit: Hit): string {
  return join(hit.turn_id, hit.entry_id, hit.kind);
}

/** Open state lives on the popup and nowhere else, so every close path tears down fully. */
function isOpen(): boolean {
  return popup?.isOpen === true;
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
  engine = new FindEngine(findRoot(), ENTRY_HOLDER);

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
    debounceMs: CHAT_SEARCH_DEBOUNCE_MS,
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
      searchedQuery = query;
      // The local pass marks what is rendered at once; the server's answer, which names every hit, picks where to land.
      searchNow(query, ctx.caseSensitive);
      updateCounter(query);
      return runServerSearch(getActiveId(), query, ctx.caseSensitive);
    },
    render: (result, query) => {
      // The one writer of the standing answer: eight values move together or none does.
      let land = false;
      if (result !== null) {
        serverHits = result.matches;
        serverTally = result;
        serverSpread = result.spread;
        stepOrder = buildStepOrder(result.matches);
        hitCursor = resumeIndex();
        stepNotice = "";
        land = landOnResume;
        landOnResume = false;
        serverHitsQuery = query;
        announcedBoundary = false;
      }
      const owned = serverHitsQuery === query;
      // Above the early return: a query refined to zero hits must clear the cut sentence.
      shell?.setNote(owned ? answerNote() : "");
      if (result === null || result.matches.length === 0) {
        // Repaint even with nothing to walk: ownership moved, and the session figure and miss skin are gated on it.
        updateCounter(query);
        // No answer to land by (a failed fetch): the local pass's first mark is all there is.
        if (result === null) {
          revealCurrent();
        }
        return;
      }
      // Re-walk under the answer's scope: a spread answer's units are its turns.
      searchNow(query, shell?.caseSensitive ?? false);
      // A cursor restored from the walk the reader left (a return from another tab) stays put until they step.
      if (hitCursor >= 0 && !land) {
        updateCounter(query);
        return;
      }
      // A handoff lands on its hit; any other answer, or a handoff whose hit is gone, on the one nearest the reader.
      if (hitCursor < 0) {
        hitCursor = nearestHitIndex();
      }
      updateCounter(query);
      const target = stepOrder[hitCursor];
      if (target !== undefined) {
        landOnHit(target);
      }
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
    trigger: maybeEl("find-btn"),
    group: "app-search",
    isolateEscape: false,
    haspopup: "dialog",
    onOpen: () => {
      // Re-root on the active view: parked sibling views hold other chats' text.
      if (engine !== null && engine.root !== findRoot()) {
        applyEngine(() => {
          engine?.clear();
        });
        engine = new FindEngine(findRoot(), ENTRY_HOLDER);
      }
      startObserving();
      // aria-pressed: find is a toggle, and `.active` means "this singleton tab is active" (tabs.ts).
      maybeEl("find-btn")?.setAttribute("aria-pressed", "true");
      built.focus();
      built.run();
    },
    onClose: () => {
      teardown();
      maybeEl("find-btn")?.setAttribute("aria-pressed", "false");
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
  serverSpread = false;
  stepOrder = [];
  localCount = 0;
  announcedBoundary = false;
  serverHitsQuery = "";
  searchedQuery = "";
  hitCursor = -1;
  stepNotice = "";
  marklessStep = false;
  // Cleared here: a handoff arms it after the switch's own close, so this cannot delete it early.
  landOnResume = false;
  // The containers first, while their turns still render; then every turn search opened folds again.
  clearHookRow();
  releaseChain();
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
  if (searchedQuery !== shell.value) {
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
    countEl.textContent = [steppedCounter(), stepNotice].filter((s) => s !== "").join(" \u00b7 ");
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
    behavior: "smooth",
  });
}

// Server-hit navigation: page the item's turn in, open its chain, re-walk, make its unit current.

/** Generous (10000 messages at 50 a page); the cap only bounds a server that never says `has_more: false`. */
const HIT_PAGE_CAP = 200;

/** Past the CSS animation, so the class strip is cleanup. */
const FLASH_FALLBACK_MS = 2000;

/** The notice for a hit on a folded hook, whose name the hooks badge lists. */
const HOOK_BADGE_NOTICE = "listed under the hooks badge";

/** Explains the partition once per answer, on the press that first crosses into phase 2. */
const BOUNDARY_NOTICE = "the rest are in delegate pages and run tabs";

/** The note under a spread answer: no hit is one message. */
const SPREAD_NOTE =
  "No single message holds every word; these turns hold them between their messages.";

/** Every item root the transcript stamps (`find-unit.ts`). */
const UNIT_SCOPE: UnitScope = { selector: FIND_UNIT_SELECTOR };

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

/**
 * Where an answer lands: the last in-place hit at or above the scroller's bottom edge, else the first, so the reader
 * stays near where they were. A hit outside the resident window is older, so above; one whose item is not mounted is
 * placed by its turn card. -1 when every hit opens another view.
 */
function nearestHitIndex(): number {
  if (localCount === 0) {
    return -1;
  }
  const bottom = maybeEl("messages-wrap")?.getBoundingClientRect().bottom ?? window.innerHeight;
  let nearest = 0;
  for (let i = 0; i < localCount; i++) {
    const hit = stepOrder[i];
    if (hit === undefined) {
      continue;
    }
    const at = itemElement(hit) ?? turnCardEl(hit.turn_id);
    if (at === null || at.getBoundingClientRect().top <= bottom) {
      nearest = i;
    }
  }
  return nearest;
}

/** Synchronous landing first: `navBusy` drops Enters while the async pipeline runs. */
function landOnHit(hit: Hit): void {
  clearHookRow();
  stepNotice = "";
  if (landInPlace(hit)) {
    return;
  }
  navBusy = true;
  void navigateToHit(hit).finally(() => {
    navBusy = false;
  });
}

/** Land on `hit` with no await, or answer false for the async pipeline: only an item in an open, mounted turn whose
 *  unit holds a mark once its chain is open. */
function landInPlace(hit: Hit): boolean {
  if (engine === null || shell === null || (hit.lane ?? "") !== "" || hit.kind === "turn") {
    return false;
  }
  const chatID = getPaintedId();
  const card = turnCardEl(hit.turn_id);
  const item = itemElement(hit);
  if (card === null || card.hasAttribute("data-folded") || item === null || diffOwed(item, hit)) {
    return false;
  }
  if (item.classList.contains("tool-group-hooks")) {
    return false;
  }
  revealChain(chatID, hit, item);
  if (!selectUnit(item)) {
    return false;
  }
  // Through `updateCounter`, the one writer, so a landing cannot disagree with the next repaint.
  updateCounter(shell.value);
  revealCurrent();
  holdWhereLanded(item, hit);
  return true;
}

/** How long a landing holds the hit where it put it on screen: past the longest open or fold animation the
 *  stylesheet runs, and the frames a box near the scrollport takes to lay out (`[data-near]`). */
const HOLD_MS = 800;

/** Frames with no layout change that end a hold early, once no animation runs in the transcript. */
const HOLD_STILL_FRAMES = 3;

/** The landing whose hit is being held, stopped by the next landing. */
let holdStop: (() => void) | null = null;

/**
 * Keep the hit where the landing aimed it, the middle of the scroller (`revealCurrent`, `selectContainer`), while the
 * layout around it settles: the chain's containers and the previous hit's turn animate, and boxes that just came near
 * the scrollport take their real size a frame later. Corrected through `shiftScroll`, and only between scrolls, since a
 * flight or the controller's own hold is still moving the view; a flight aimed before the layout moved is corrected
 * once it lands. The reader's wheel, touch or press ends the hold.
 */
function holdWhereLanded(unit: HTMLElement, hit: Hit): void {
  holdStop?.();
  const scroller = maybeEl("messages-wrap");
  if (scroller === null) {
    return;
  }
  const target = (): HTMLElement => engine?.currentMark() ?? unit;
  const middle = (r: DOMRect): number => r.top + r.height / 2;
  let lastScroll = scroller.scrollTop;
  let still = 0;
  let frame = 0;
  const deadline = performance.now() + HOLD_MS;
  const stop = (): void => {
    cancelAnimationFrame(frame);
    for (const type of ["wheel", "touchstart", "pointerdown"]) {
      scroller.removeEventListener(type, stop);
    }
    if (holdStop === stop) {
      holdStop = null;
    }
  };
  const tick = (): void => {
    const now = target();
    if (!owns(hit) || !now.isConnected) {
      stop();
      return;
    }
    if (performance.now() > deadline) {
      stop();
      recentreIfOutOfView(scroller, now);
      return;
    }
    // A flight's first frame has not moved it yet, and a write now would abort it.
    const scrolling = jumpInFlight() || Math.abs(scroller.scrollTop - lastScroll) >= 1;
    let corrected = false;
    if (!scrolling) {
      const off = middle(now.getBoundingClientRect()) - middle(scroller.getBoundingClientRect());
      const at = scroller.scrollTop;
      if (Math.abs(off) >= 1) {
        shiftScroll(off);
      }
      // A content edge clamps the write: what it cannot move is where the landing could put the hit.
      corrected = Math.abs(scroller.scrollTop - at) >= 1;
    }
    still = scrolling || corrected ? 0 : still + 1;
    lastScroll = scroller.scrollTop;
    if (still >= HOLD_STILL_FRAMES && scroller.getAnimations({ subtree: true }).length === 0) {
      stop();
      return;
    }
    frame = requestAnimationFrame(tick);
  };
  for (const type of ["wheel", "touchstart", "pointerdown"]) {
    scroller.addEventListener(type, stop, { passive: true });
  }
  holdStop = stop;
  frame = requestAnimationFrame(tick);
}

/** A held hit the layout still carried out of the scroller (a flight aimed before it settled) is jumped to again. */
function recentreIfOutOfView(scroller: HTMLElement, target: HTMLElement): void {
  const view = scroller.getBoundingClientRect();
  const at = target.getBoundingClientRect();
  if (at.top >= view.top && at.bottom <= view.bottom) {
    return;
  }
  jumpTo(target, {
    block: "center",
    inline: "nearest",
    behavior: "smooth",
  });
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

/** The notes under the standing answer: the spread sentence, then the cut one; "" when neither applies. */
function answerNote(): string {
  const cut =
    serverTally.matched > serverHits.length ? scanNote(serverTally, serverHits.length, NOUNS) : "";
  return [serverSpread ? SPREAD_NOTE : "", cut].filter((s) => s !== "").join(" ");
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
  stepNotice = parts.join(" \u00b7 ");
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
  // Closed (or switched away, or the list moved on) while paging in: the surface this would select on is gone,
  // and teardown already released the reveal.
  if (!owns(hit)) {
    return;
  }
  const item = itemElement(hit);
  if (item === null) {
    revealTurnOnly(chatID, hit);
    showHitNotice("could not be shown");
    return;
  }
  revealChain(chatID, hit, item);
  if (item.classList.contains("tool-group-hooks")) {
    selectContainer(item);
    markHookRow(item, hit);
    showHitNotice(HOOK_BADGE_NOTICE);
    holdWhereLanded(item, hit);
    return;
  }
  // A preview-less diff card loads its diff on open, so the text does not exist until the bulk lands.
  const arriving = awaitDeferredDiff(item, hit);
  if (arriving !== null) {
    await arriving;
    // Closed, retyped, a newer answer, or the transcript repainted this element away, while the bulk was in flight.
    if (!owns(hit) || !item.isConnected) {
      return;
    }
  }
  await landOrNotice(item, hit);
}

/** Whether `hit` is still the one to land on: find open, the list the box's text owns, the cursor on it. */
function owns(hit: Hit): boolean {
  return isOpen() && shell !== null && stepsOwnedList(shell.value) && stepOrder[hitCursor] === hit;
}

/** Make `unit` current, or select it and say why it cannot be; shared by every resolution path. */
async function landOrNotice(unit: HTMLElement, hit: Hit): Promise<void> {
  // A landing may not move the reader once this press lost the list: every effect below is a visible write.
  if (shell === null || !owns(hit)) {
    return;
  }
  // A filter-only query marks nothing: the item is the answer.
  if (parseChatQuery(shell.value).terms.length === 0) {
    selectContainer(unit);
    updateCounter(shell.value);
    return;
  }
  let landed = selectUnit(unit);
  // Whether the second walk had a rendered frame to walk. A first-walk landing never asks.
  let rendered = true;
  if (!landed) {
    // A miss may mean not rendered: the transcript's heavy cards are `content-visibility: auto` (css/14-tools.css), so
    // jump and wait one rendered frame.
    jumpTo(unit, { block: "center", inline: "nearest", behavior: "auto" });
    rendered = await nextRender();
    // Closed, repainted away, or no longer owning the answer during the frame.
    if (!unit.isConnected || !owns(hit)) {
      return;
    }
    landed = selectUnit(unit);
  }
  if (!landed) {
    // The words are in the raw text and not the rendered one (markdown syntax), or not rendered at all.
    selectContainer(unit);
    showHitNotice(missNotice(unit, hit, rendered));
    holdWhereLanded(unit, hit);
    return;
  }
  updateCounter(shell.value);
  revealCurrent();
  holdWhereLanded(unit, hit);
}

/** Re-walk, so marks exist inside content that only just opened, and make `unit`'s step current. */
function selectUnit(unit: HTMLElement): boolean {
  marklessStep = false;
  searchNow(shell?.value ?? "", shell?.caseSensitive ?? false);
  let found = false;
  applyEngine(() => {
    found = engine?.setCurrentUnit(unit) === true;
  });
  return found;
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
  const resident = (): boolean => getPainted()?.turns.has(hit.turn_id) === true;
  for (let pages = 0; !resident(); pages++) {
    const session = getPainted();
    if (session?.has_more !== true || session.id !== chatID || pages >= HIT_PAGE_CAP) {
      return false;
    }
    // False covers a failed fetch and a page that moved the window's edge nowhere, where more requests would loop on
    // the same answer.
    if (!(await loadOlderTurns(chatID))) {
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

/** The element a hit's item is drawn as, its search unit; null when not mounted. A spread hit is its turn card, and
 *  a turn-level item the card does not draw (a cancelled turn's reason) is the card, where the miss is stated. */
function itemElement(hit: Hit): HTMLElement | null {
  const card = turnCardEl(hit.turn_id);
  if (card === null) {
    return null;
  }
  switch (hit.kind) {
    case "header":
      return card.querySelector<HTMLElement>(":scope > .turn-header > .turn-req") ?? card;
    case "turn_failure":
      return card.querySelector<HTMLElement>(":scope > .turn-notice") ?? card;
    case "turn":
      return card;
    case "message":
    case "reasoning":
    case "tool":
    case "steer":
    case "plan":
    case "notice":
      return entryElement(card, hit.entry_id);
  }
}

/** An entry's unit in its own turn card: the element stamped for it, or the run holding it. */
function entryElement(card: HTMLElement, entryID: string): HTMLElement | null {
  const id = CSS.escape(entryID);
  const stamped =
    card.querySelector<HTMLElement>(`[data-entry-id="${id}"]`) ??
    card.querySelector<HTMLElement>(`[data-entries~="${id}"]`);
  if (stamped === null) {
    return null;
  }
  // A folded hook's card is hidden; the hooks badge counting it stands for it.
  return foldedHookBadge(stamped) ?? stamped.closest<HTMLElement>(FIND_UNIT_SELECTOR) ?? stamped;
}

const TOOL_DIFF_PREVIEW = ".tool-diff-preview";

/** Three states: no frame delivered (not rendered), a diff-specific sentence, or rendered but not found. */
function missNotice(unit: HTMLElement, hit: Hit, rendered: boolean): string {
  if (!rendered) {
    return "not rendered yet";
  }
  if (hit.segments.length > 0 && hit.segments.every((k) => k === "tool_diff")) {
    return unit.querySelector(TOOL_DIFF_PREVIEW) !== null
      ? "not in the shown hunks, so open the diff"
      : "only in this call's diff, which did not load";
  }
  return "not in rendered text";
}

/** `stepServerHit` drops Enters while `navBusy` is latched, so this bounds a swallowed keystroke. */
const DEFERRED_DIFF_WAIT_MS = 1500;

/** Bounds a dead diff hit to one ceiling per card rather than per visit. */
const openBroughtNoDiff = new WeakSet<HTMLElement>();

/** Whether `hit`'s words are in a diff its card has not loaded: only the async path waits for it. */
function diffOwed(item: HTMLElement, hit: Hit): boolean {
  if (!hit.segments.includes("tool_diff")) {
    return false;
  }
  const card = item.closest<HTMLElement>(".tool-call");
  return (
    card !== null &&
    card.querySelector(TOOL_DIFF_PREVIEW) === null &&
    !openBroughtNoDiff.has(card) &&
    card.querySelector(".tool-disclosure") !== null
  );
}

/** Wait, bounded, for the diff the chain's open of a preview-less card fetches; null when nothing is coming. */
function awaitDeferredDiff(item: HTMLElement, hit: Hit): Promise<void> | null {
  const card = item.closest<HTMLElement>(".tool-call");
  if (card === null || !diffOwed(item, hit)) {
    return null;
  }
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

/** Mark the query's words in the badge list row standing for the folded hook `hit` is on, as the current hit: the
 *  row names the hook in its own words, so a unit holding any term is marked. */
function markHookRow(badge: HTMLElement, hit: Hit): void {
  const card = turnCardEl(hit.turn_id)?.querySelector<HTMLElement>(
    `[data-entry-id="${CSS.escape(hit.entry_id)}"]`,
  );
  const row = card === null || card === undefined ? null : hookRowOf(badge, card);
  if (row === null) {
    return;
  }
  // Rooted at the list: a unit is found below the engine's root, never as it.
  hookRowEngine = new FindEngine(row.parentElement ?? row);
  hookRowEngine.searchAnyTerm(walkOf(serverHitsQuery, shell?.caseSensitive ?? false).matcher, {
    units: [row],
  });
}

function clearHookRow(): void {
  hookRowEngine?.clear();
  hookRowEngine = null;
}

/** The class comes off on animationend, with a timer as the reduced-motion backstop. */
function selectContainer(target: HTMLElement): void {
  // The step is this element, not a mark: no other mark may read as current, a re-walk's included.
  marklessStep = true;
  applyEngine(() => {
    engine?.clearCurrent();
  });
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
    behavior: "smooth",
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

/** The walk `query` asks for: its words, and the units they must share, a spread answer's turns or every item. */
function walkOf(query: string, caseSensitive: boolean): { matcher: Matcher; scope: UnitScope } {
  const matcher = prepareMatcher(parseChatQuery(query).terms, caseSensitive);
  if (!serverSpread || serverHitsQuery !== query) {
    return { matcher, scope: UNIT_SCOPE };
  }
  const units = serverHits
    .map((h) => turnCardEl(h.turn_id))
    .filter((c): c is HTMLElement => c !== null);
  return { matcher, scope: { units } };
}

/** Walk the transcript now, superseding a pending re-run: this walk already sees the mutations that scheduled it. */
function searchNow(query: string, caseSensitive: boolean): void {
  cancelRerun();
  const { matcher, scope } = walkOf(query, caseSensitive);
  applyEngine(() => {
    engine?.searchUnits(matcher, scope);
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
  // The standing search, not the box: words typed since are walked when the pause or Enter runs them.
  const query = searchedQuery;
  const { matcher, scope } = walkOf(query, shell.caseSensitive);
  applyEngine(() => {
    engine?.refreshUnits(matcher, scope);
    if (marklessStep && stepsOwnedList(query)) {
      engine?.clearCurrent();
    }
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
  const chatView = maybeEl("chat-view");
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

/** @internal Test seam: whether the search is open. */
// deadset:ignore DS1102 -- test seam: observes whether the chat find bar is open
export function _isChatFindOpen(): boolean {
  return isOpen();
}
