// The editor pane's position cues, measured against the shipped stylesheet in a real browser: the
// flash, the find's mark, and the jump. Geometry is asserted against the browser's own glyph
// boxes (a Range over the rendered text), never against the arithmetic under test.
import { describe, it, expect, beforeAll, afterAll, afterEach, beforeEach, vi } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { mountEditorView } from "./__test-helpers__/editor-dom.js";
import {
  clearEditorMark,
  flashEditorLine,
  revealBufferHit,
  scrollToEditorLine,
} from "./editor-scroll.js";
import { editSurface, viewer } from "./editor-pane.js";
import { WholeRows } from "./viewer-rows.js";

let sheet: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  sheet = mountAppCSS();
  host = mountEditorView();
});

afterAll(() => {
  host.remove();
  sheet.remove();
});

afterEach(() => {
  clearEditorMark();
  vi.useRealTimers();
  host.style.removeProperty("block-size");
  byId("editor-goto").classList.add("hidden");
});

const byId = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const body = (): HTMLElement => byId("editor-viewer").parentElement as HTMLElement;
const area = (): HTMLTextAreaElement => byId("editor-content");

function showRead(text: string): void {
  byId("editor-viewer").classList.remove("hidden");
  area().classList.add("hidden");
  byId("editor-edit-gutter").classList.add("hidden");
  body().classList.remove("is-editing");
  body().scrollTop = 0;
  body().scrollLeft = 0;
  viewer().show({ rows: new WholeRows(text), runs: null, agentLines: new Set() });
}

function showEdit(text: string): void {
  byId("editor-viewer").classList.add("hidden");
  area().classList.remove("hidden");
  byId("editor-edit-gutter").classList.remove("hidden");
  body().classList.add("is-editing");
  area().value = text;
  editSurface().setLines(text.split("\n").length, new Set());
  area().scrollTop = 0;
  area().scrollLeft = 0;
}

/** The browser's own box for characters `[from, to)` of a seated row's text. */
function glyphs(row: number, from: number, to: number): DOMRect {
  const text = document.querySelector(
    `.viewer-row[data-row="${String(row)}"] .viewer-text`,
  )?.firstChild;
  if (text === null || text === undefined) {
    throw new Error(`row ${String(row)} is not seated`);
  }
  const r = document.createRange();
  r.setStart(text, from);
  r.setEnd(text, to);
  return r.getBoundingClientRect();
}

const marks = (): HTMLElement[] =>
  [...document.querySelectorAll<HTMLElement>(".editor-find-mark")].filter((m) => m.isConnected);

describe("revealBufferHit", () => {
  it("covers the matched glyphs on the read surface, by the browser's own measure", () => {
    const text = "package main\n\nfunc target() {}\n// target again\n";
    showRead(text);
    revealBufferHit(text, 19, 6, 3);
    const want = glyphs(2, 5, 11);
    const got = marks()[0]?.getBoundingClientRect();
    expect(Math.abs((got?.left ?? 0) - want.left)).toBeLessThan(1);
    expect(Math.abs((got?.width ?? 0) - want.width)).toBeLessThan(1);
    expect(
      Math.abs(((got?.top ?? 0) + (got?.bottom ?? 0)) / 2 - (want.top + want.bottom) / 2),
    ).toBeLessThan(1);
  });

  it("expands tabs from the line start, so an indented match lands on its glyphs", () => {
    const text = "x\n\t\tfoo bar\n";
    showRead(text);
    revealBufferHit(text, 8, 3, 2);
    const want = glyphs(1, 6, 9);
    const got = marks()[0]?.getBoundingClientRect();
    expect(Math.abs((got?.left ?? 0) - want.left)).toBeLessThan(1);
    expect(Math.abs((got?.width ?? 0) - want.width)).toBeLessThan(1);
  });

  it("marks a hit straddling a cut on both rows it spans", () => {
    const line = "x".repeat(4094) + "needle";
    showRead(line);
    revealBufferHit(line, 4094, 6, 1);
    expect(marks()).toHaveLength(2);
  });

  it("sits over the textarea's glyphs in edit mode, and follows its scroller to a far column", () => {
    const line = `${"x".repeat(200)} needle`;
    showEdit(`${line}\n`);
    revealBufferHit(`${line}\n`, 201, 6, 1);
    expect(area().scrollLeft).toBeGreaterThan(0);
    const got = marks()[0]?.getBoundingClientRect();
    const view = area().getBoundingClientRect();
    // Sub-pixel: the mark is measured in a pre twin of the textarea, which rounds its own way.
    expect(got?.left ?? -1).toBeGreaterThanOrEqual(view.left - 1);
    expect(got?.right ?? 1e9).toBeLessThanOrEqual(view.right + 1);
    expect(area().selectionStart).toBe(201);
    expect(area().selectionEnd).toBe(207);
  });

  it("marks a hit deep in a long file over its line in edit mode", () => {
    const text = `${"a\n".repeat(2999)}needle\n${"a\n".repeat(1000)}`;
    showEdit(text);
    revealBufferHit(text, 2999 * 2, 6, 3000);
    const want = area().getBoundingClientRect().top + areaLineTop(3000) - area().scrollTop;
    expect(Math.abs((marks()[0]?.getBoundingClientRect().top ?? Infinity) - want)).toBeLessThan(1);
  });

  it("flashes a line of a file shorter than the textarea over its line in edit mode", () => {
    // As on a session's first edit: no tail yet, so the scroll extent is the box, not the text.
    area().style.removeProperty("--edit-tail");
    showEdit("a\nb\nc");
    flashEditorLine(3);
    const flash = document.querySelector<HTMLElement>(".editor-body > .editor-line-flash");
    const want = area().getBoundingClientRect().top + areaLineTop(3) - area().scrollTop;
    expect(Math.abs((flash?.getBoundingClientRect().top ?? Infinity) - want)).toBeLessThan(1);
  });

  it("pans the pane to a far column on the read surface", () => {
    const text = `${"x".repeat(200)} needle\n`;
    showRead(text);
    revealBufferHit(text, 201, 6, 1);
    expect(body().scrollLeft).toBeGreaterThan(0);
    const got = marks()[0]?.getBoundingClientRect();
    const view = body().getBoundingClientRect();
    expect(got?.left ?? -1).toBeGreaterThanOrEqual(view.left);
    expect(got?.right ?? 1e9).toBeLessThanOrEqual(view.right);
  });
});

describe("flashEditorLine", () => {
  it("re-positions ONE element across a burst of steps, each step getting its whole 1.2 s", () => {
    vi.useFakeTimers();
    showRead("a\n".repeat(50));
    const flashes = (): NodeListOf<Element> =>
      byId("editor-viewer").querySelectorAll(".editor-line-flash");
    for (let line = 1; line <= 41; line++) {
      flashEditorLine(line);
    }
    expect(flashes()).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    flashEditorLine(7);
    vi.advanceTimersByTime(250);
    expect(flashes()).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    expect(flashes()).toHaveLength(0);
  });
});

/** The textarea's scrollTop with 1-based `line` at its top, by the browser's own layout: measured
 *  in a `pre` twin carrying its class, whose line boxes the engine snaps the same way. */
function areaLineTop(line: number): number {
  const text = area().value;
  const twin = document.createElement("pre");
  twin.className = "editor-content editor-find-probe";
  twin.textContent = text;
  body().appendChild(twin);
  const glyphTop = (n: number): number => {
    const at =
      text
        .split("\n")
        .slice(0, n - 1)
        .join("\n").length + (n > 1 ? 1 : 0);
    const r = document.createRange();
    r.setStart(twin.firstChild as Text, at);
    r.setEnd(twin.firstChild as Text, at + 1);
    return r.getBoundingClientRect().top;
  };
  const top = parseFloat(getComputedStyle(area()).paddingBlockStart) + glyphTop(line) - glyphTop(1);
  twin.remove();
  return top;
}

describe("scrollToEditorLine", () => {
  it("lands the line's first row at the top at once", () => {
    showRead("a\n".repeat(400));
    const loc = scrollToEditorLine(300);
    expect(loc).toEqual({ row: 299, line: 300, clamped: false });
    const row = document.querySelector<HTMLElement>('.viewer-row[data-row="299"]');
    const view = body().getBoundingClientRect();
    expect(Math.abs((row?.getBoundingClientRect().top ?? 0) - view.top)).toBeLessThan(2);
  });

  it("lands a cut line on its first row", () => {
    showRead(`${"a\n".repeat(300)}${"x".repeat(4096 * 3)}\n${"a\n".repeat(300)}`);
    expect(scrollToEditorLine(301)).toEqual({ row: 300, line: 301, clamped: false });
    const row = document.querySelector<HTMLElement>('.viewer-row[data-row="300"]');
    const view = body().getBoundingClientRect();
    expect(Math.abs((row?.getBoundingClientRect().top ?? 0) - view.top)).toBeLessThan(2);
  });

  it("clamps a line past the end to the last one and says so", () => {
    showRead("a\nb\nc");
    expect(scrollToEditorLine(999)).toEqual({ row: 2, line: 3, clamped: true });
  });

  it("lands the last line at the top", () => {
    showRead(`${"a\n".repeat(399)}end`);
    expect(scrollToEditorLine(400)).toEqual({ row: 399, line: 400, clamped: false });
    const row = document.querySelector<HTMLElement>('.viewer-row[data-row="399"]');
    const view = body().getBoundingClientRect();
    expect(Math.abs((row?.getBoundingClientRect().top ?? 0) - view.top)).toBeLessThan(2);
  });

  it("puts the line at the textarea's top in edit state", () => {
    showEdit("a\n".repeat(400));
    expect(scrollToEditorLine(300)).toMatchObject({ line: 300, clamped: false });
    expect(Math.abs(area().scrollTop - areaLineTop(300))).toBeLessThan(1);
  });

  it("puts a line deep in a long file at the textarea's top in edit state", () => {
    showEdit("a\n".repeat(4000));
    scrollToEditorLine(3000);
    expect(Math.abs(area().scrollTop - areaLineTop(3000))).toBeLessThan(1);
  });

  it("numbers the line at the textarea's top deep in a long file", () => {
    showEdit("a\n".repeat(4000));
    area().scrollTop = areaLineTop(3000);
    area().dispatchEvent(new Event("scroll"));
    const cell = [...byId("editor-edit-gutter").querySelectorAll<HTMLElement>(".viewer-ln")].find(
      (c) => c.textContent === "3000",
    );
    const top = area().getBoundingClientRect().top;
    expect(Math.abs((cell?.getBoundingClientRect().top ?? Infinity) - top)).toBeLessThan(1);
  });

  it("puts the last line at the textarea's top in edit state", () => {
    showEdit(`${"a\n".repeat(399)}end`);
    expect(scrollToEditorLine(400)).toMatchObject({ line: 400, clamped: false });
    expect(Math.abs(area().scrollTop - areaLineTop(400))).toBeLessThan(1);
  });

  it("leaves a find hit in edit state a third of the way down", () => {
    const text = "a\n".repeat(400);
    showEdit(text);
    revealBufferHit(text, 598, 1, 300);
    expect(Math.abs(area().scrollTop - (areaLineTop(300) - area().clientHeight / 3))).toBeLessThan(
      1,
    );
  });
});

/** Two frames: a resize is delivered to its observers in the rendering step after layout. */
const settled = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });

function setPaneHeight(px: number): void {
  host.style.blockSize = `${String(px)}px`;
}

/** How far the last row sits below the viewport's top with the read scroller at its end. */
async function lastRowOffsetAtEnd(): Promise<number> {
  body().scrollTop = body().scrollHeight;
  await settled();
  const row = document.querySelector<HTMLElement>('.viewer-row[data-row="399"]');
  return (row?.getBoundingClientRect().top ?? Infinity) - body().getBoundingClientRect().top;
}

const LAST_LINE_FILE = `${"a\n".repeat(399)}end`;

describe("a pane that changes height", () => {
  // An observer is told of a size only once a frame delivers it, so each case starts from the
  // size the previous one restored.
  beforeEach(settled);

  it("ends the read scroll range on the last line at the top after the pane shrinks", async () => {
    showRead(LAST_LINE_FILE);
    setPaneHeight(host.clientHeight / 2);
    await settled();
    expect(Math.abs(await lastRowOffsetAtEnd())).toBeLessThan(2);
  });

  it("ends the read scroll range on the last line at the top after the pane grows", async () => {
    setPaneHeight(200);
    await settled();
    showRead(LAST_LINE_FILE);
    host.style.removeProperty("block-size");
    await settled();
    expect(Math.abs(await lastRowOffsetAtEnd())).toBeLessThan(2);
  });

  it("ends the read scroll range on the last line at the top under a bar that opens above it", async () => {
    showRead(LAST_LINE_FILE);
    byId("editor-goto").classList.remove("hidden");
    await settled();
    expect(Math.abs(await lastRowOffsetAtEnd())).toBeLessThan(2);
  });

  it("ends the textarea's scroll range on the last line at the top after the pane shrinks", async () => {
    showEdit(LAST_LINE_FILE);
    setPaneHeight(host.clientHeight / 2);
    await settled();
    area().scrollTop = area().scrollHeight;
    expect(Math.abs(area().scrollTop - areaLineTop(400))).toBeLessThan(1);
  });

  it("keeps the textarea inside the pane after the pane shrinks", async () => {
    showEdit(LAST_LINE_FILE);
    setPaneHeight(host.clientHeight / 2);
    await settled();
    expect(area().offsetHeight).toBeLessThanOrEqual(body().clientHeight);
  });

  it("numbers every line in the textarea's view after the pane grows", async () => {
    // At rest first, so no scroll event from an earlier case repaints the gutter on its own.
    area().scrollTop = 0;
    area().style.removeProperty("--edit-tail");
    setPaneHeight(200);
    await settled();
    showEdit(LAST_LINE_FILE);
    host.style.removeProperty("block-size");
    await settled();
    const cells = [...byId("editor-edit-gutter").querySelectorAll<HTMLElement>(".viewer-ln")];
    const lastCell = cells[cells.length - 1]?.getBoundingClientRect().bottom ?? 0;
    expect(lastCell).toBeGreaterThanOrEqual(area().getBoundingClientRect().bottom);
  });
});
