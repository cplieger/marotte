// The editor pane is ONE set of elements serving every editor tab, so a file's
// reading position has to live on its own state. Measured in a real browser over
// the shipped stylesheet: activating a file again (the tab's onShow) must land
// where the reader left it, whether they went to another view or another file.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { mountEditorView } from "./__test-helpers__/editor-dom.js";
import { initEditor } from "./editor-core.js";
import { activateFile, closeEditorFile } from "./editor-openers.js";
import { viewer } from "./editor-pane.js";
import { scrollToEditorLine } from "./editor-scroll.js";
import { fileStates, freshState, setActiveFilePath, type FileMode } from "./editor-types.js";

let sheet: HTMLStyleElement;
let sizing: HTMLStyleElement;
let host: HTMLElement;
let other: HTMLElement;

beforeAll(() => {
  sheet = mountAppCSS();
  sizing = document.createElement("style");
  sizing.textContent = `[id="editor-view"] { width: 480px; height: 240px; display: flex; flex-direction: column; }
    [id="editor-view"] > .editor-page { flex: 1; min-height: 0; }`;
  document.head.appendChild(sizing);
  host = mountEditorView();
  other = document.createElement("div");
  other.id = "other-view";
  other.className = "hidden";
  other.textContent = "chat";
  host.prepend(other);
  initEditor();
});

afterAll(() => {
  sheet.remove();
  sizing.remove();
  host.remove();
});

const A = "/workspace/a.go";
const B = "/workspace/b.go";

function lines(tag: string, n: number, width = 10): string {
  return Array.from({ length: n }, (_, i) => `${tag} line ${String(i)} ${"x".repeat(width)}`).join(
    "\n",
  );
}

function seed(path: string, content: string, mode: FileMode = { kind: "text", editing: false }) {
  const state = freshState(path);
  state.original.value = content;
  state.current.value = content;
  state.loaded = true;
  state.facts.value = {
    kind: "small",
    binary: false,
    utf8: true,
    conflict: false,
    readOnly: false,
    size: content.length,
  };
  state.mode.value = mode;
  fileStates.set(path, state);
}

function body(): HTMLElement {
  return document.querySelector<HTMLElement>(".editor-body")!;
}

/** One line box: a read-state position is restored by its logical line. */
function lineBox(): number {
  return parseFloat(getComputedStyle(document.getElementById("editor-viewer")!).lineHeight);
}

function textarea(): HTMLTextAreaElement {
  return document.getElementById("editor-content") as HTMLTextAreaElement;
}

/** A scroll event is dispatched at the next frame, which is when the capture runs. */
async function frames(n = 2): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
  }
}

/** A short line, then one cut into ten rows (rows 1 to 10), then short lines. */
function longLineFile(): string {
  return `first\n${"y".repeat(4096 * 10)}\n${lines("a", 200)}`;
}

function seatedRow(row: number): HTMLElement {
  const el = document.querySelector<HTMLElement>(`.viewer-row[data-row="${String(row)}"]`);
  if (el === null) {
    throw new Error(`row ${String(row)} is not seated`);
  }
  return el;
}

/** Scroll the read surface so `row` sits at its top, as a reader's scroll would. */
function scrollRowToTop(row: number): void {
  body().scrollTop +=
    seatedRow(row).getBoundingClientRect().top - body().getBoundingClientRect().top;
}

function rowOffsetFromTop(row: number): number {
  return Math.abs(seatedRow(row).getBoundingClientRect().top - body().getBoundingClientRect().top);
}

function leaveEditorView(): void {
  document.getElementById("editor-view")!.classList.add("hidden");
  other.classList.remove("hidden");
}

function returnToEditorView(path: string): void {
  other.classList.add("hidden");
  document.getElementById("editor-view")!.classList.remove("hidden");
  activateFile(path);
}

beforeEach(() => {
  document.getElementById("editor-view")!.classList.remove("hidden");
  other.classList.add("hidden");
});

afterEach(() => {
  closeEditorFile(A);
  closeEditorFile(B);
  setActiveFilePath("");
});

describe("the editor keeps each file's reading position", () => {
  it("survives a round trip through another view", async () => {
    seed(A, lines("a", 400));
    activateFile(A);
    body().scrollTop = 1200;
    await frames();
    leaveEditorView();
    await frames();
    returnToEditorView(A);
    expect(Math.abs(body().scrollTop - 1200)).toBeLessThan(lineBox());
  });

  it("is per file: A, then B, then A lands each where it was left", async () => {
    seed(A, lines("a", 400));
    seed(B, lines("b", 400));
    activateFile(A);
    body().scrollTop = 1200;
    await frames();
    activateFile(B);
    // A first open lands at the top rather than at the previous file's offset.
    expect(body().scrollTop).toBe(0);
    body().scrollTop = 300;
    await frames();
    activateFile(A);
    expect(Math.abs(body().scrollTop - 1200)).toBeLessThan(lineBox());
    await frames();
    activateFile(B);
    expect(Math.abs(body().scrollTop - 300)).toBeLessThan(lineBox());
  });

  it("reopens a file taller than the height cap at its first line after a jump past the cap", async () => {
    const text = "\n".repeat((2 << 20) - 1);
    seed(A, text);
    activateFile(A);
    scrollToEditorLine(1_500_000);
    await frames();
    expect(viewer().rows?.lines[viewer().topRow()], "the jump moved the view").toBe(1_500_000);
    closeEditorFile(A);
    seed(A, text);
    activateFile(A);
    await frames();
    expect(viewer().rows?.lines[viewer().topRow()]).toBe(1);
    expect(rowOffsetFromTop(0)).toBeLessThan(lineBox());
  });

  it("comes back on the cut row of a long line it was left at, across a tab switch", async () => {
    seed(A, longLineFile());
    seed(B, lines("b", 400));
    activateFile(A);
    await frames();
    scrollRowToTop(7);
    await frames();
    activateFile(B);
    await frames();
    activateFile(A);
    await frames();
    expect(rowOffsetFromTop(7)).toBeLessThan(2);
  });

  it("comes back on the cut row of a long line after an edit that kept the line", async () => {
    seed(A, longLineFile());
    activateFile(A);
    await frames();
    scrollRowToTop(7);
    await frames();
    document.getElementById("editor-edit-btn")!.click();
    // Edit opens on the same line, the caret at its start rather than at the end of the file.
    const ta = textarea();
    const style = getComputedStyle(ta);
    expect(
      Math.abs(ta.scrollTop - parseFloat(style.paddingBlockStart) - parseFloat(style.lineHeight)),
    ).toBeLessThan(1);
    expect(ta.selectionStart).toBe("first\n".length);
    await frames();
    document.getElementById("editor-cancel-btn")!.click();
    await frames();
    expect(rowOffsetFromTop(7)).toBeLessThan(2);
  });

  it("opens Edit on the line in view when the kept selection is off screen", async () => {
    const text = lines("a", 400);
    seed(A, text);
    seed(B, lines("b", 400));
    activateFile(A);
    document.getElementById("editor-edit-btn")!.click();
    const ta = textarea();
    const lineFive = text.split("\n").slice(0, 4).join("\n").length + 1;
    ta.setSelectionRange(lineFive, lineFive);
    const style = getComputedStyle(ta);
    const line150 = parseFloat(style.paddingBlockStart) + 149 * parseFloat(style.lineHeight);
    ta.scrollTop = line150;
    await frames();
    activateFile(B);
    activateFile(A);
    document.getElementById("editor-cancel-btn")!.click();
    await frames();
    document.getElementById("editor-edit-btn")!.click();
    expect(ta.selectionStart).toBe(lineFive);
    expect(Math.abs(ta.scrollTop - line150)).toBeLessThan(parseFloat(style.lineHeight));
    ta.blur();
  });

  it("keeps the selection and the textarea's own pan in edit mode", async () => {
    seed(A, lines("a", 40, 300), { kind: "text", editing: true });
    seed(B, lines("b", 40, 300), { kind: "text", editing: true });
    activateFile(A);
    const ta = textarea();
    ta.setSelectionRange(10, 25, "backward");
    ta.scrollLeft = 200;
    ta.scrollTop = 150;
    await frames();
    activateFile(B);
    activateFile(A);
    expect(ta.selectionStart).toBe(10);
    expect(ta.selectionEnd).toBe(25);
    expect(ta.selectionDirection).toBe("backward");
    expect(ta.scrollLeft).toBe(200);
    // Restored by line, so within one line box of where it was left.
    expect(Math.abs(ta.scrollTop - 150)).toBeLessThan(parseFloat(getComputedStyle(ta).lineHeight));
    expect(document.activeElement).not.toBe(ta);
  });

  // Distinct selections, because the textarea is SHARED: an activation that read it
  // back would file the outgoing file's selection under the incoming one, and two
  // equal selections cannot show that.
  it("restores each file's own selection when two files differ", async () => {
    seed(A, lines("a", 40, 300), { kind: "text", editing: true });
    seed(B, lines("b", 40, 300), { kind: "text", editing: true });
    const ta = textarea();
    activateFile(A);
    ta.setSelectionRange(10, 25, "backward");
    await frames();
    activateFile(B);
    ta.setSelectionRange(300, 340, "forward");
    await frames();

    activateFile(A);
    expect([ta.selectionStart, ta.selectionEnd, ta.selectionDirection]).toEqual([
      10,
      25,
      "backward",
    ]);
    activateFile(B);
    expect([ta.selectionStart, ta.selectionEnd, ta.selectionDirection]).toEqual([
      300,
      340,
      "forward",
    ]);
  });

  it("keeps a diff's scroll position, on both axes, across a rebuild of the pane", async () => {
    const old = lines("a", 300, 200);
    const next = old.replace("a line 150", "A LINE 150").replace("a line 7 ", "A LINE 7 ");
    seed(A, old, {
      kind: "diff",
      diffSource: {
        oldText: old,
        newText: next,
        oldLabel: "saved",
        newLabel: "unsaved",
        kind: "pair",
      },
    });
    seed(B, lines("b", 400));
    activateFile(A);
    await frames(3);
    const split = (): HTMLElement => document.querySelector<HTMLElement>(".diff-pane-split")!;
    const col = (): HTMLElement => document.querySelector<HTMLElement>(".diff-col-old")!;
    const bar = (): HTMLElement => document.querySelector<HTMLElement>(".diff-pane-hbar")!;
    expect(split().scrollHeight).toBeGreaterThan(split().clientHeight);
    split().scrollTop = 800;
    col().scrollLeft = 120;
    await frames();
    const pane = split();
    activateFile(B);
    activateFile(A);
    expect(split()).not.toBe(pane);
    expect(split().scrollTop).toBe(800);
    expect(col().scrollLeft).toBe(120);
    await frames(4);
    expect(bar().scrollLeft).toBe(120);
  });
});

describe("the edit gutter", () => {
  it("numbers the lines typed into the textarea", () => {
    seed(A, "one\ntwo", { kind: "text", editing: true });
    activateFile(A);
    const ta = textarea();
    ta.value = "one\ntwo\nthree\nfour";
    ta.dispatchEvent(new Event("input"));
    const numbers = [
      ...document.querySelectorAll<HTMLElement>('[id="editor-edit-gutter"] .viewer-ln'),
    ].map((c) => c.textContent);
    expect(numbers).toEqual(["1", "2", "3", "4"]);
  });
});
