// The match counter of both find bars, against the shipped stylesheet.
//
// THE DEFECT THIS PINS: the counter was a slot BETWEEN the field and the buttons
// (`min-inline-size: 4.5rem` + `white-space: nowrap`, one rule in 24-find.css and a
// byte-identical twin in 20-editor.css), while both of its writers put whole
// SENTENCES in it — `domCounter`'s `emptyNote` for an empty walk, and
// `paintHitPosition`'s `·`-joined per-press notices after the cursor. A DECLARED
// `min-inline-size` replaces a flex item's automatic minimum size, so the box
// shrank under its own text, `nowrap` refused to wrap it and the default
// `overflow: visible` painted it across all four buttons and out through the
// popup's border. Reported as the counter overlapping the controls.
//
// So the assertions are about INK against the buttons, not about a width: a
// declaration-level guard cannot see the case, because every declaration involved
// was individually reasonable.
//
// Both bars are booted for real, because the fix is ONE shared rule
// (`.search-status`) and a test over one bar would not notice the other's copy
// coming back.
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { emptyNote, cursorCount } from "./textsearch/copy.js";
import type * as ModFindInChat from "./find-in-chat.js";
import type * as ModEditorFind from "./editor-find.js";

// find-in-chat.ts's graph, with the mock set its own CSS suite uses and for the
// same reasons: scroll.ts self-initialises against DOM this file does not build,
// and the block dispatcher's graph reaches through it.
vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  onTranscriptMutate: vi.fn(() => () => undefined),
}));
vi.mock("./chat-search.js", { spy: true });
vi.mock("./store.js", { spy: true });
vi.mock("./store-load.js", { spy: true });
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
vi.mock("./subagent-view.js", () => ({ openSubagentView: vi.fn() }));
vi.mock("./messages-blocks.js", () => ({
  blockElement: vi.fn(),
  runOffsetOf: vi.fn(() => undefined),
}));
// editor-find.ts's one graph edge that reaches real editor scrolling.
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: vi.fn(),
  flashEditorLine: vi.fn(),
  markEditorSpan: vi.fn(),
  clearEditorMark: vi.fn(),
  // Present so real-ESM linking succeeds; editor-diff and editor-openers import them.
  trackEditorView: () => undefined,
  captureSelection: () => undefined,
  restoreEditorView: () => undefined,
  bindDiffView: () => undefined,
}));

const NOUNS = {
  match: { one: "match", many: "matches" },
  scanned: { one: "message", many: "messages" },
};

/** The strings the counter's own writers can produce, longest last. Derived from
 *  the real producers rather than transcribed, so a copy change moves the fixture:
 *  `emptyNote` is what `domCounter` prints for an empty walk, `cursorCount` the
 *  cursor, and the last row is the cursor with the two `·`-joined notices
 *  `paintHitPosition` appends on a cross-destination landing. */
function counterStrings(): string[] {
  const boundary = "the rest are in delegate pages and run tabs";
  const miss = "only in this call's diff, which did not load";
  return [
    cursorCount(3, 17),
    cursorCount(1, 200, 347),
    emptyNote({ kind: "partial", scanned: 1204 }, NOUNS),
    [cursorCount(1, 200, 347), boundary, miss].join(" \u00b7 "),
  ];
}

let styleEl: HTMLStyleElement;
let bootSeq = 0;

beforeAll(() => {
  styleEl = mountAppCSS();
});

afterAll(() => {
  styleEl.remove();
  document.documentElement.removeAttribute("data-pointer");
});

/** Rect intersection with a half-pixel tolerance, so a shared edge is not a hit.
 *  BOTH axes: the fix puts the counter on its own row, so a horizontal-only test
 *  reports every button as overlapped and passes for the broken layout too. */
function intersects(a: DOMRect, b: DOMRect): boolean {
  return (
    a.right > b.left + 0.5 &&
    a.left < b.right - 0.5 &&
    a.bottom > b.top + 0.5 &&
    a.top < b.bottom - 0.5
  );
}

/** The counter's INK, which is what paints over a button — its own box may be
 *  narrower (that was the defect) or wider (a wrapped line's box is the column). */
function inkOf(el: HTMLElement): DOMRect {
  const r = document.createRange();
  r.selectNodeContents(el);
  return r.getBoundingClientRect();
}

interface Bar {
  box: HTMLElement;
  count: HTMLElement;
  buttons: HTMLButtonElement[];
}

async function bootChatBar(): Promise<Bar> {
  bootSeq++;
  document.body.innerHTML =
    `<div id="chat-view" data-tab-view>` +
    `<button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>` +
    `<div id="messages-wrap-outer">` +
    `<div id="messages-wrap"><div id="messages"></div></div>` +
    `</div>` +
    `<textarea id="prompt-input"></textarea>` +
    `</div>`;
  const mod = (await import(
    /* @vite-ignore */ `./find-in-chat.ts?counter-css=${bootSeq}`
  )) as typeof ModFindInChat;
  mod.handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
  const box = document.getElementById("chat-find");
  const count = document.getElementById("chat-find-count");
  if (box === null || count === null) {
    throw new Error("the transcript find bar did not build its box or its counter");
  }
  return { box, count, buttons: [...box.querySelectorAll("button")] };
}

/** A phone's width, because the editor's bar is FULL WIDTH and the browser project's
 *  viewport is fixed at 1280: at that width the field's own shrink frees enough room
 *  for every string below, so the case could not fail and would be worth nothing.
 *  The bar overlapped from about 480px of bar width down, which is a phone in
 *  portrait and a narrow window, so this is the deployment rather than a squeeze
 *  invented for the test. */
const PHONE_W = "390px";

async function bootEditorBar(): Promise<Bar> {
  bootSeq++;
  document.body.innerHTML =
    `<div id="editor-view" data-tab-view style="inline-size:${PHONE_W}"><div class="editor-page">` +
    `<div id="editor-error" class="editor-error hidden"></div>` +
    `<div id="editor-conflict-overlay" class="editor-conflict-overlay hidden"></div>` +
    `<div class="editor-body">` +
    `<pre id="editor-gutter"></pre>` +
    `<pre id="editor-highlight"><code id="editor-code"></code></pre>` +
    `<textarea id="editor-content" class="hidden"></textarea>` +
    `<div id="editor-markdown" class="hidden"></div>` +
    `<div id="editor-image" class="hidden"></div>` +
    `<div id="editor-diff-pane" class="hidden"></div>` +
    `</div></div></div>`;
  const types = await import("./editor-types.js");
  const state = types.freshState("/workspace/a.go");
  state.loaded = true;
  state.current.value = "package main\n\nfunc target() {}\n";
  state.original.value = state.current.value;
  state.mode.value = { kind: "edit", editing: false };
  types.fileStates.set("/workspace/a.go", state);
  types.setActiveFilePath("/workspace/a.go");
  const mod = (await import(
    /* @vite-ignore */ `./editor-find.ts?counter-css=${bootSeq}`
  )) as typeof ModEditorFind;
  mod.openEditorFind();
  const box = document.querySelector<HTMLElement>(".editor-find");
  const count = document.getElementById("editor-find-count");
  if (box === null || count === null) {
    throw new Error("the in-file find bar did not build its box or its counter");
  }
  return { box, count, buttons: [...box.querySelectorAll("button")] };
}

const BARS = [
  ["the transcript find bar", bootChatBar],
  ["the in-file find bar", bootEditorBar],
] as const;

describe("the find bars' match counter under the shipped stylesheet", () => {
  beforeEach(() => {
    document.documentElement.setAttribute("data-pointer", "fine");
  });

  for (const [name, boot] of BARS) {
    // BOTH pointer tiers: `--btn-h` is 2.25rem on a fine pointer and 2.75rem on a
    // coarse one, so the button cluster is 32px wider under a finger and the row
    // that squeezed the counter squeezed it harder there.
    for (const tier of ["fine", "coarse"] as const) {
      it(`keeps every counter string clear of ${name}'s buttons on a ${tier} pointer`, async () => {
        document.documentElement.setAttribute("data-pointer", tier);
        const { box, count, buttons } = await boot();
        expect(buttons.length).toBeGreaterThan(0);
        const readings = counterStrings().map((text) => {
          count.textContent = text;
          // A layout read, so the paint is irrelevant and the transition on this
          // element's own height is not: `block-size` is animated, and its
          // TARGET is what the rects below have to be taken against. Reading the
          // width and the ink needs no settle (neither is transitioned), and the
          // vertical overlap question is answered by the row above it, which is
          // static.
          void box.offsetWidth;
          const ink = inkOf(count);
          return {
            text,
            hitButtons: buttons.filter((b) => intersects(ink, b.getBoundingClientRect())).length,
            // The MECHANISM, stated separately from the symptom: the box shrank
            // under its own text and the text painted out of it.
            textPastOwnBox: count.scrollWidth - count.clientWidth,
            // And out through the popup's border, which is how the empty-state
            // sentence left the box entirely.
            inkPastBoxRight: ink.right - box.getBoundingClientRect().right > 0.5,
          };
        });
        expect(readings.map((r) => r.hitButtons)).toEqual([0, 0, 0, 0]);
        expect(readings.map((r) => r.textPastOwnBox)).toEqual([0, 0, 0, 0]);
        expect(readings.map((r) => r.inkPastBoxRight)).toEqual([false, false, false, false]);
      });
    }

    it(`reserves ${name} no line while the counter says nothing`, async () => {
      const { box, count } = await boot();
      // `domCounter` answers "" for an empty query, so this IS the state a box
      // just opened is in, and keeping it costless is what leaves the resting bar
      // one row now that the counter is a line of its own. What it can fail
      // against: a reserved line (`min-block-size`) or an unconditional margin,
      // which is exactly what `.chat-find-note`'s own record says it used to have
      // — 16px of nothing with 4px of gap above it.
      count.textContent = "";
      await settle();
      // READ AS STRINGS before the removal: `getComputedStyle` hands back a LIVE
      // object, so a property accessed after `remove()` answers "" for everything.
      const empty = getComputedStyle(count);
      const emptyBlockSize = empty.blockSize;
      const emptyOverflowY = empty.overflowY;
      const counterHeight = count.getBoundingClientRect().height;
      const withCounter = box.getBoundingClientRect().height;
      count.remove();
      expect({
        counterHeight,
        // The direct statement, measured rather than derived from the padding: the
        // bar is the height it would be if the counter were not in the layout.
        barUnchanged: box.getBoundingClientRect().height === withCounter,
        // And the two declarations the GROWTH animates through, which the zero
        // height above does not imply: an empty block box is 0 tall on its own, so
        // without these the line still costs nothing and simply snaps open.
        emptyBlockSize,
        emptyOverflowY,
      }).toEqual({
        counterHeight: 0,
        barUnchanged: true,
        emptyBlockSize: "0px",
        emptyOverflowY: "clip",
      });
    });
  }

  it("resolves ONE shape for both bars rather than two rules that agree today", async () => {
    // The two counters carried byte-identical 6-declaration rules in two files,
    // which is the drift this file's subject removed. A computed comparison is
    // what notices a re-added local rule, because a re-added rule would still
    // pass every assertion above.
    const chat = await bootChatBar();
    const chatStyle = shapeOf(chat.count);
    const editor = await bootEditorBar();
    expect(shapeOf(editor.count)).toEqual(chatStyle);
  });
});

/** The declarations that decide whether a long counter can paint over a control.
 *  `whiteSpace` and `minInlineSize` are the two the defect was made of. */
function shapeOf(el: HTMLElement): Record<string, string> {
  const cs = getComputedStyle(el);
  return {
    whiteSpace: cs.whiteSpace,
    minInlineSize: cs.minInlineSize,
    overflowX: cs.overflowX,
    fontSize: cs.fontSize,
    fontVariantNumeric: cs.fontVariantNumeric,
    color: cs.color,
  };
}

/** Two frames plus longer than the longest transition in the sheet: this element's
 *  `block-size` is animated, so a height read taken in the same task as the text
 *  change is the PRE-transition value. */
function settle(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        setTimeout(resolve, 320);
      });
    });
  });
}
