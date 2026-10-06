// The find walker against the shipped stylesheet: find-in-chat.test.ts uses no CSS, so it passes a walker that
// prunes every assistant reply.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FindEngine } from "./find-engine.js";
import type * as ModFindInChat from "./find-in-chat.js";

// Imports find-in-chat.ts with its own suite's mock set (scroll.ts self-initialises against DOM not built here).
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

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // In view at a real width: `.msg-row` is `content-visibility: auto`, so an off-screen fixture would be pruned.
  host.style.cssText = "inline-size:640px;";
  document.body.prepend(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

/** A turn body with a boxless (`display: contents`) wrapper, declared inline rather than by class. */
function assistantReply(text: string): HTMLElement {
  host.innerHTML =
    `<div class="turn-body">` +
    `<div style="display:contents">` +
    `<div class="msg-row"><div class="message assistant">${text}</div></div>` +
    `</div></div>`;
  return host.firstElementChild as HTMLElement;
}

/**
 * One rendered frame: `content-visibility: auto` relevance comes from the last lifecycle update (`navigateToHit` waits
 * the same frame).
 */
function rendered(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("the walker under the shipped stylesheet", () => {
  it("marks prose inside a boxless region", async () => {
    // `display: contents` answers checkVisibility false, so pruning there placed no mark in any reply.
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    expect(new FindEngine(wrap).search("TODO")).toBe(1);
  });

  it("puts the mark in the prose bubble itself", async () => {
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    new FindEngine(wrap).search("TODO");
    const mark = wrap.querySelector("mark");
    expect(mark?.parentElement?.className).toBe("message assistant");
  });

  // The fixture's premise, so a region that gains a box cannot leave the test vacuously green.
  it("is measuring a region Chromium reports as invisible", () => {
    const wrap = assistantReply("prose");
    const blocks = wrap.querySelector("[style]") as HTMLElement;
    expect({
      display: getComputedStyle(blocks).display,
      visible: blocks.checkVisibility({
        contentVisibilityAuto: true,
        visibilityProperty: true,
        opacityProperty: false,
      }),
    }).toEqual({ display: "contents", visible: false });
  });

  it("still prunes a subtree that is boxless because it is HIDDEN", async () => {
    // Neither has client rects, so a box-absence test passes the cases above and fails this one.
    host.innerHTML =
      `<div class="turn-body">` +
      `<div style="display:none"><div class="message">display TODO</div></div>` +
      `<div style="content-visibility:hidden"><div class="message">skipped TODO</div></div>` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });

  it("still prunes an off-screen row that content-visibility SKIPPED", async () => {
    // The `auto` variant: a skipped row is boxless to `checkVisibility` like the region above.
    host.innerHTML =
      `<div class="turn-body">` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `<div style="block-size:2400px"></div>` +
      `<div class="msg-row"><div class="message">skipped TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });
});

// The note is the bar's second line, mounted always as an aria-live region; its zero-height treatment keeps the bar
// one row.

let bootSeq = 0;

describe("the transcript find bar under the shipped stylesheet", () => {
  let fixture: HTMLElement;

  beforeEach(async () => {
    bootSeq++;
    fixture = document.createElement("div");
    fixture.innerHTML =
      `<div id="chat-view" data-tab-view>` +
      `<button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>` +
      `<div id="messages-wrap-outer">` +
      `<div id="messages-wrap"><div id="messages"></div></div>` +
      `</div>` +
      `<textarea id="prompt-input"></textarea>` +
      `</div>`;
    document.body.appendChild(fixture);
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?css-boot=${bootSeq}`
    )) as typeof ModFindInChat;
    mod.handleFindHotkey(
      new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }),
    );
    await rendered();
  });

  afterEach(() => {
    fixture.remove();
  });

  it("keeps the resting bar one row, with the empty note costing no height", () => {
    const region = document.getElementById("chat-find") as HTMLElement;
    const row = region.querySelector<HTMLElement>(".chat-find-row") as HTMLElement;
    const note = document.getElementById("chat-find-note") as HTMLElement;
    // Read before the removal: a detached element's computed style answers "".
    const noteStyle = getComputedStyle(note);
    const noteOpacity = noteStyle.opacity;
    const noteOverflow = noteStyle.overflowY;
    const noteHeight = note.getBoundingClientRect().height;
    const barHeight = region.getBoundingClientRect().height;
    const rowHeight = row.getBoundingClientRect().height;
    // Measured: the bar is the height it would be without the note.
    note.remove();
    const withoutNote = region.getBoundingClientRect().height;
    expect({
      noteHeight,
      barUnchanged: barHeight === withoutNote,
      rowFitsOnce: rowHeight > 0 && rowHeight < barHeight,
      // Mounted but silent, as a live region wants; both declarations belong to `.chat-find-note`.
      noteOpacity,
      noteOverflow,
    }).toEqual({
      noteHeight: 0,
      barUnchanged: true,
      rowFitsOnce: true,
      noteOpacity: "0",
      noteOverflow: "clip",
    });
  });
});
