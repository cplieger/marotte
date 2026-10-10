// The windowed renderer against real layout: scrollers, row geometry and the bounded window are
// arithmetic over boxes, so they are measured rather than read out of the CSS.
import { describe, it, expect, beforeAll, afterAll, beforeEach, onTestFinished } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { mountEditorView } from "./__test-helpers__/editor-dom.js";
import { EditSurface, MAX_SCROLL_PX, TextViewer } from "./viewer-render.js";
import { ROW_MAX, WholeRows } from "./viewer-rows.js";
import { highlightRuns } from "./highlight.js";

/** The logical line of the viewer's top row. */
function topLine(v: TextViewer): number {
  return v.rows?.lines[v.topRow()] ?? 1;
}

let sheet: HTMLStyleElement;
let host: HTMLElement;
let body: HTMLElement;
let root: HTMLElement;
let area: HTMLTextAreaElement;
let gutterEl: HTMLElement;
let viewer: TextViewer;
let gutter: EditSurface;

const q = <T extends Element>(sel: string): T => {
  const el = document.querySelector<T>(sel);
  if (el === null) {
    throw new Error(`missing ${sel}`);
  }
  return el;
};

beforeAll(() => {
  sheet = mountAppCSS();
  host = mountEditorView();
  root = q("#editor-viewer");
  body = root.parentElement as HTMLElement;
  area = q("#editor-content");
  gutterEl = q("#editor-edit-gutter");
  viewer = new TextViewer(body, root);
  gutter = new EditSurface(gutterEl, area, body);
});

afterAll(() => {
  host.remove();
  sheet.remove();
});

function readState(text: string, path = "/w/a.txt"): WholeRows {
  root.classList.remove("hidden");
  area.classList.add("hidden");
  gutterEl.classList.add("hidden");
  body.classList.remove("is-editing");
  const rows = new WholeRows(text);
  viewer.show({ rows, runs: highlightRuns(text, path), agentLines: new Set() });
  return rows;
}

function editState(text: string): void {
  root.classList.add("hidden");
  area.classList.remove("hidden");
  gutterEl.classList.remove("hidden");
  body.classList.add("is-editing");
  area.value = text;
  gutter.setLines(text.split("\n").length, new Set());
}

const seated = (): HTMLElement[] => [...root.querySelectorAll<HTMLElement>(".viewer-row")];

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(null)));
  }
}

function scrollers(): string[] {
  const out: string[] = [];
  for (const el of document.querySelectorAll("#editor-view, #editor-view *")) {
    const cs = getComputedStyle(el);
    if (/auto|scroll/u.test(cs.overflowY) && el.scrollHeight > el.clientHeight) {
      out.push(el.id !== "" ? `#${el.id}` : `.${el.className.split(" ")[0] ?? ""}`);
    }
  }
  return out;
}

const lines = (n: number): string =>
  Array.from({ length: n }, (_, i) => `\tline ${String(i + 1)}`).join("\n");

beforeEach(() => {
  body.scrollTop = 0;
  body.scrollLeft = 0;
});

describe("the read state", () => {
  it("has exactly one vertical scroller, the pane", () => {
    readState(lines(400));
    expect(scrollers()).toEqual([".editor-body"]);
  });

  it("holds line numbers in place while the text pans sideways", () => {
    readState(`${lines(50)}\nlong := ${"x".repeat(600)}`);
    const cell = q<HTMLElement>(".viewer-row .viewer-ln");
    const before = cell.getBoundingClientRect().left;
    body.scrollLeft = 300;
    expect(body.scrollLeft, "the pane panned").toBeGreaterThan(0);
    expect(cell.getBoundingClientRect().left).toBeCloseTo(before, 0);
  });

  it("places row i one line box below row i - 1", () => {
    readState(lines(30));
    const [a, b] = seated();
    expect(a && b).toBeTruthy();
    const lh = parseFloat(getComputedStyle(root).lineHeight);
    expect(
      (b?.getBoundingClientRect().top ?? 0) - (a?.getBoundingClientRect().top ?? 0),
    ).toBeCloseTo(lh, 1);
    expect(a?.getBoundingClientRect().height).toBeCloseTo(lh, 1);
  });

  it("seats a bounded window of a 2 MiB newline-only file", () => {
    readState("\n".repeat((2 << 20) - 1));
    const visible = Math.ceil(body.clientHeight / parseFloat(getComputedStyle(root).lineHeight));
    expect(seated().length).toBeLessThan(visible * 3 + 100);
    expect(q<HTMLElement>(".viewer-spacer").getBoundingClientRect().height).toBeLessThanOrEqual(
      MAX_SCROLL_PX,
    );
  });

  it("seats a bounded window of 2 MiB of highlighted source", () => {
    const line = "  const value = compute(alpha, beta); // a comment here\n";
    readState(line.repeat(Math.floor((2 << 20) / line.length)), "/w/a.ts");
    const visible = Math.ceil(body.clientHeight / parseFloat(getComputedStyle(root).lineHeight));
    expect(seated().length).toBeLessThan(visible * 3 + 100);
  });

  it("shows the last row of a file taller than the height cap after a jump to it", () => {
    const rows = readState("\n".repeat((2 << 20) - 1));
    viewer.scrollToRow(rows.total - 1, "third");
    const last = root.querySelector<HTMLElement>(
      `.viewer-row[data-row="${String(rows.total - 1)}"]`,
    );
    expect(last, "the last row is seated").not.toBeNull();
    const r = last?.getBoundingClientRect();
    const view = body.getBoundingClientRect();
    expect((r?.top ?? -1) >= view.top && (r?.bottom ?? 1e9) <= view.bottom).toBe(true);
    expect(topLine(viewer)).toBeGreaterThan(rows.totalLines - 100);
  });

  it("seats no row past the spacer of a file taller than the height cap", () => {
    readState("\n".repeat((2 << 20) - 1));
    for (let i = 0; i < 3; i++) {
      body.scrollTop = body.scrollHeight;
      body.dispatchEvent(new Event("scroll"));
    }
    const cs = getComputedStyle(root);
    const pads = parseFloat(cs.paddingBlockStart) + parseFloat(cs.paddingBlockEnd);
    expect(body.scrollHeight).toBeLessThanOrEqual(MAX_SCROLL_PX + pads);
  });

  it("is not at the bottom at the physical end of a file taller than the height cap", () => {
    readState("\n".repeat((2 << 20) - 1));
    body.scrollTop = body.scrollHeight;
    body.dispatchEvent(new Event("scroll"));
    expect(viewer.atBottom()).toBe(false);
  });

  it("is at the bottom at the end of a file under the height cap", () => {
    readState(lines(400));
    body.scrollTop = body.scrollHeight;
    body.dispatchEvent(new Event("scroll"));
    expect(viewer.atBottom()).toBe(true);
  });

  it("keeps the visible line when pinned to the bottom of a file taller than the height cap", () => {
    readState("\n".repeat((2 << 20) - 1));
    body.scrollTop = body.scrollHeight / 2;
    body.dispatchEvent(new Event("scroll"));
    const before = topLine(viewer);
    viewer.pinBottom();
    expect(topLine(viewer)).toBe(before);
  });

  it("does not move the view up when pinned with the file's end already in view", () => {
    readState(lines(400));
    viewer.scrollToRow(399, "top");
    const top = body.scrollTop;
    viewer.show({ rows: new WholeRows(lines(402)), runs: null, agentLines: new Set() });
    viewer.pinBottom();
    expect(body.scrollTop).toBe(top);
  });

  it.each([1, 6, 41])("names row %i the top line after scrolling it to the top", (row) => {
    const rows = readState(lines(400));
    viewer.scrollToRow(row, "top");
    expect(topLine(viewer)).toBe(rows.lines[row]);
  });

  it("reaches a row inside the height cap without moving the rendered region", () => {
    readState("\n".repeat((2 << 20) - 1));
    const lh = parseFloat(getComputedStyle(root).lineHeight);
    const row = Math.floor((MAX_SCROLL_PX - 3 * body.clientHeight) / lh);
    viewer.scrollToRow(row, "third");
    expect(topLine(viewer)).toBeLessThanOrEqual(row + 1);
    expect(body.scrollTop).toBeGreaterThan(MAX_SCROLL_PX - 4 * body.clientHeight);
  });

  /** How far row `row`'s box sits below the top of the pane, or null when it is not seated. */
  function offsetFromTop(row: number): number | null {
    const el = root.querySelector<HTMLElement>(`.viewer-row[data-row="${String(row)}"]`);
    return el === null ? null : el.getBoundingClientRect().top - body.getBoundingClientRect().top;
  }

  it("puts the last row of a file taller than the height cap at the top", () => {
    const rows = readState("\n".repeat((2 << 20) - 1));
    viewer.scrollToRow(rows.total - 1, "top");
    expect(Math.abs(offsetFromTop(rows.total - 1) ?? Infinity)).toBeLessThan(2);
  });

  it("keeps the spacer under the height cap with the last row of a capped file at the top", () => {
    const rows = readState("\n".repeat((2 << 20) - 1));
    viewer.scrollToRow(rows.total - 1, "top");
    const cs = getComputedStyle(root);
    const pads = parseFloat(cs.paddingBlockStart) + parseFloat(cs.paddingBlockEnd);
    expect(body.scrollHeight).toBeLessThanOrEqual(MAX_SCROLL_PX + pads);
  });

  it("keeps the last row of a capped file at the top after the pane shrinks", async () => {
    const rows = readState("\n".repeat((2 << 20) - 1));
    viewer.scrollToRow(rows.total - 1, "top");
    await frames(2);
    host.style.blockSize = `${String(host.clientHeight / 2)}px`;
    onTestFinished(() => {
      host.style.removeProperty("block-size");
    });
    await frames(2);
    expect(Math.abs(offsetFromTop(rows.total - 1) ?? Infinity)).toBeLessThan(2);
    // The row the reader is on is what a reopen restores, so it must agree with what is drawn.
    expect(viewer.topRow()).toBe(rows.total - 1);
  });

  it("ends the scroll range on a drawn row while the file continues past the height cap", () => {
    readState("\n".repeat((2 << 20) - 1));
    body.scrollTop = body.scrollHeight;
    body.dispatchEvent(new Event("scroll"));
    const lh = parseFloat(getComputedStyle(root).lineHeight);
    const bottom = Math.max(...seated().map((r) => r.getBoundingClientRect().bottom));
    expect(bottom).toBeGreaterThan(body.getBoundingClientRect().bottom - 2 * lh);
  });

  it("puts a row at the top when the viewport below it crosses the height cap", () => {
    readState("\n".repeat((2 << 20) - 1));
    const lh = parseFloat(getComputedStyle(root).lineHeight);
    // Inside the region drawn from the file's start, with less than a viewport of it below.
    const row = Math.floor((MAX_SCROLL_PX - body.clientHeight) / lh) - 3;
    viewer.scrollToRow(row, "top");
    expect(Math.abs(offsetFromTop(row) ?? Infinity)).toBeLessThan(2);
    expect(offsetFromTop(row + 8), "the rows below it are drawn").not.toBeNull();
  });

  it("keeps the rendered region when the same document is shown again past the cap", () => {
    const text = "\n".repeat((2 << 20) - 1);
    readState(text);
    const doc = {};
    const again = (): void => {
      viewer.show({ rows: new WholeRows(text), runs: null, agentLines: new Set() }, doc);
    };
    again();
    // A row whose region is not clamped to the file's end, so a reset region would differ.
    viewer.scrollToRow(1_200_000, "top");
    body.scrollTop -= 5000;
    body.dispatchEvent(new Event("scroll"));
    const line = topLine(viewer);
    const top = body.scrollTop;
    again();
    viewer.scrollToRow(viewer.rows?.lineToRow(line).row ?? 0, "top");
    expect(topLine(viewer)).toBe(line);
    const lh = parseFloat(getComputedStyle(root).lineHeight);
    expect(Math.abs(body.scrollTop - top)).toBeLessThan(lh);
  });

  it("numbers a long line's first row and marks each continuation ↳", () => {
    readState(`${"x".repeat(ROW_MAX * 2 + 5)}\nnext`);
    expect(seated().map((r) => r.querySelector(".viewer-ln")?.textContent)).toEqual([
      "1",
      "\u21b3",
      "\u21b3",
      "2",
    ]);
  });

  it("never re-seats a row that stays in the window", () => {
    readState(lines(2000));
    const row5 = root.querySelector('.viewer-row[data-row="5"]');
    body.scrollTop = 200;
    body.dispatchEvent(new Event("scroll"));
    expect(root.querySelector('.viewer-row[data-row="5"]')).toBe(row5);
  });

  it("carries a block comment's class on every row it spans", () => {
    readState("a\n/* one\ntwo\nthree */\nb", "/w/a.ts");
    const classes = seated()
      .slice(1, 4)
      .map((r) => r.querySelector(".viewer-text > span")?.className);
    expect(classes).toEqual(["hl-comment", "hl-comment", "hl-comment"]);
  });

  it("maps a selection endpoint in a seated row to its text offset", () => {
    readState("alpha\nbeta\ngamma");
    const text = q('.viewer-row[data-row="1"] .viewer-text').firstChild;
    expect(text).not.toBeNull();
    expect(viewer.offsetAt(text as Node, 2)).toBe("alpha\n".length + 2);
  });

  describe("a selection across rows", () => {
    const SRC = "const alpha = 1;\nlet beta = 2;\nreturn;";

    /** Select backwards from "be" in row 1 to "lpha" in row 0, over rows painted without
     *  highlight, and answer the selection's endpoints as text offsets. */
    function selectAcross(): { anchor: number | null; focus: number | null } {
      root.classList.remove("hidden");
      viewer.show({ rows: new WholeRows(SRC), runs: null, agentLines: new Set() });
      const row0 = q('.viewer-row[data-row="0"] .viewer-text').firstChild as Node;
      const row1 = q('.viewer-row[data-row="1"] .viewer-text').firstChild as Node;
      document.getSelection()?.setBaseAndExtent(row1, "let be".length, row0, "const a".length);
      return endpoints();
    }

    function endpoints(): { anchor: number | null; focus: number | null } {
      const sel = document.getSelection();
      if (sel === null || sel.anchorNode === null || sel.focusNode === null) {
        return { anchor: null, focus: null };
      }
      return {
        anchor: viewer.offsetAt(sel.anchorNode, sel.anchorOffset),
        focus: viewer.offsetAt(sel.focusNode, sel.focusOffset),
      };
    }

    it("survives the highlight arriving, direction included", () => {
      const before = selectAcross();
      const rows = viewer.rows as WholeRows;
      viewer.update({ rows, runs: highlightRuns(SRC, "/w/a.ts"), agentLines: new Set() });
      expect(q('.viewer-row[data-row="0"] .viewer-text > span').className).toMatch(/^hl-/);
      expect(endpoints()).toEqual(before);
      expect(before).toEqual({
        anchor: "const alpha = 1;\nlet be".length,
        focus: "const a".length,
      });
    });

    it("survives an agent mark arriving", () => {
      const before = selectAcross();
      const rows = viewer.rows as WholeRows;
      viewer.update({ rows, runs: null, agentLines: new Set([2]) });
      expect(q('.viewer-row[data-row="1"] .viewer-ln').classList).toContain(
        "gutter-agent-modified",
      );
      expect(endpoints()).toEqual(before);
    });

    it("survives an agent mark arriving while it reaches past the viewer", () => {
      selectAcross();
      const outside = document.createElement("p");
      outside.textContent = "after the viewer";
      document.body.appendChild(outside);
      const sel = document.getSelection();
      const anchor = sel?.anchorNode ?? null;
      sel?.extend(outside.firstChild as Node, 5);
      const rows = viewer.rows as WholeRows;
      viewer.update({ rows, runs: null, agentLines: new Set([1]) });
      outside.remove();
      expect(sel?.anchorNode).toBe(anchor);
      expect(sel?.anchorOffset).toBe("let be".length);
    });
  });

  it("keeps the row holding a pinned offset seated when it scrolls out of the window", () => {
    readState(lines(3000));
    viewer.pinOffsets([0]);
    const first = root.querySelector('.viewer-row[data-row="0"]');
    viewer.scrollToRow(2500, "top");
    expect(root.querySelector('.viewer-row[data-row="0"]')).toBe(first);
    expect(seated().map((r) => Number(r.dataset["row"]))).toEqual(
      [...seated().map((r) => Number(r.dataset["row"]))].sort((x, y) => x - y),
    );
  });
});

describe("the edit state", () => {
  it("makes the textarea the one vertical scroller", () => {
    editState(lines(400));
    expect(scrollers()).toEqual(["#editor-content"]);
  });

  it("numbers the line beside it after a scroll", () => {
    editState(lines(400));
    area.scrollTop = 1000;
    gutter.render();
    const lh = parseFloat(getComputedStyle(area).lineHeight);
    const pad = parseFloat(getComputedStyle(area).paddingBlockStart);
    const line = Math.floor((1000 - pad) / lh) + 3;
    const cell = [...gutterEl.querySelectorAll<HTMLElement>(".viewer-ln")].find(
      (c) => c.textContent === String(line),
    );
    expect(cell, `line ${String(line)} is numbered`).toBeDefined();
    const lineTop = area.getBoundingClientRect().top + pad + (line - 1) * lh - area.scrollTop;
    expect(Math.abs((cell?.getBoundingClientRect().top ?? 0) - lineTop)).toBeLessThan(1);
  });
});

// A box whose height its content decides has no viewport to size a tail from; one measured off it
// would grow the box it was measured from on every frame.
describe("a scroller its content sizes", () => {
  it("keeps its height in read state", async () => {
    const box = document.createElement("div");
    const view = document.createElement("div");
    view.className = "viewer";
    box.append(view);
    host.append(box);
    onTestFinished(() => {
      box.remove();
    });
    new TextViewer(box, view).show({
      rows: new WholeRows("a\nb\nc"),
      runs: null,
      agentLines: new Set(),
    });
    const height = box.clientHeight;
    await frames(3);
    expect(box.clientHeight).toBe(height);
  });

  it("keeps its height in edit state", async () => {
    const box = document.createElement("div");
    const lines = document.createElement("div");
    const field = document.createElement("textarea");
    field.className = "editor-content";
    field.value = "a\nb\nc";
    box.append(lines, field);
    host.append(box);
    onTestFinished(() => {
      box.remove();
    });
    new EditSurface(lines, field, box).setLines(3, new Set());
    const height = box.clientHeight;
    await frames(3);
    expect(box.clientHeight).toBe(height);
  });
});
