// Both find bars' match counter against the shipped stylesheet: it once sat between the field and buttons and painted
// over them; now it is its own row.
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { emptyNote, cursorCount } from "./textsearch/copy.js";
import type * as ModFindInChat from "./find-in-chat.js";
import type * as ModEditorFind from "./editor-find.js";

// find-in-chat.ts's graph: scroll.ts self-initialises against DOM this file does not build.
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
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: vi.fn(),
  flashEditorLine: vi.fn(),
  markEditorSpan: vi.fn(),
  clearEditorMark: vi.fn(),
  // Present for real-ESM linking (editor-diff and editor-openers import them).
  trackEditorView: () => undefined,
  captureSelection: () => undefined,
  restoreEditorView: () => undefined,
  bindDiffView: () => undefined,
}));

const NOUNS = {
  match: { one: "match", many: "matches" },
  scanned: { one: "message", many: "messages" },
};

/** Derived from the real producers, so a copy change moves the fixture. */
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

/** Both axes: the counter is on its own row, so a horizontal-only test passes the broken layout. */
function intersects(a: DOMRect, b: DOMRect): boolean {
  return (
    a.right > b.left + 0.5 &&
    a.left < b.right - 0.5 &&
    a.bottom > b.top + 0.5 &&
    a.top < b.bottom - 0.5
  );
}

/** The ink, not the box: a wrapped line's box is the column. */
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

/** A phone width: at 1280 the field's shrink frees room for every string, so the case could not fail. */
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
    // `--btn-h` is 2.25rem fine, 2.75rem coarse, so the buttons squeeze harder under a finger.
    for (const tier of ["fine", "coarse"] as const) {
      it(`keeps every counter string clear of ${name}'s buttons on a ${tier} pointer`, async () => {
        document.documentElement.setAttribute("data-pointer", tier);
        const { box, count, buttons } = await boot();
        expect(buttons.length).toBeGreaterThan(0);
        const readings = counterStrings().map((text) => {
          count.textContent = text;
          // `block-size` is animated, so rects are taken against its target.
          void box.offsetWidth;
          const ink = inkOf(count);
          return {
            text,
            hitButtons: buttons.filter((b) => intersects(ink, b.getBoundingClientRect())).length,
            // The mechanism: the box shrank under its text.
            textPastOwnBox: count.scrollWidth - count.clientWidth,
            // And out through the popup's border.
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
      // `domCounter` answers "" for an empty query, the just-opened state, which must cost no height.
      count.textContent = "";
      await settle();
      // Read as strings first: `getComputedStyle` is live, so it answers "" after `remove()`.
      const empty = getComputedStyle(count);
      const emptyBlockSize = empty.blockSize;
      const emptyOverflowY = empty.overflowY;
      const counterHeight = count.getBoundingClientRect().height;
      const withCounter = box.getBoundingClientRect().height;
      count.remove();
      expect({
        counterHeight,
        // Measured: the bar is the height it would be without the counter.
        barUnchanged: box.getBoundingClientRect().height === withCounter,
        // The declarations the growth animates through, which a zero height does not imply.
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
    // The two counters share one rule; a re-added local rule shows only in a computed comparison.
    const chat = await bootChatBar();
    const chatStyle = shapeOf(chat.count);
    const editor = await bootEditorBar();
    expect(shapeOf(editor.count)).toEqual(chatStyle);
  });
});

/** `whiteSpace` and `minInlineSize` are the two the defect was made of. */
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

/** Past the longest transition: `block-size` is animated. */
function settle(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        setTimeout(resolve, 320);
      });
    });
  });
}
