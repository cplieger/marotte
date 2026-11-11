// The windowed text renderer: the read state draws only the rows around the viewport, and the
// edit state's gutter draws only the visible line numbers beside the native textarea. Scrolling
// stays native: `base` moves only when a jump targets a row past the browser's height cap, so
// the rendered region follows it. Each scroller ends the file with a tail one viewport less one
// line tall, so a jump can put the last line at the top.

import { el } from "@cplieger/reactive";
import { RUN_CLASSES, type HighlightRuns } from "./highlight.js";
import type { LineLocation, WholeRows } from "./viewer-rows.js";

/** The tallest spacer drawn, under the lowest engine height cap (16,777,216 px). */
export const MAX_SCROLL_PX = 16_000_000;
/** Rows kept seated above and below the viewport. */
const OVERSCAN = 40;
const CONTINUATION = "\u21b3";
/** How far a probe grows a scroller's content to learn whether its height follows it. */
const PROBE_PX = 100;

interface ViewerContent {
  readonly rows: WholeRows;
  readonly runs: HighlightRuns | null;
  readonly agentLines: ReadonlySet<number>;
}

/** A selection endpoint as a text offset within row `row`. */
interface RowPoint {
  readonly row: number;
  readonly offset: number;
}

/** The read state's renderer over `.editor-body`, the one scroller in read state. */
export class TextViewer {
  private content: ViewerContent | null = null;
  private readonly spacer: HTMLElement;
  private readonly host: HTMLElement;
  private readonly seated = new Map<number, HTMLElement>();
  /** Seated row indices in ascending order, which is also their DOM order. */
  private order: number[] = [];
  /** Rows holding the selection's endpoints, kept seated so a drag survives a scroll. */
  private pinned = new Set<number>();
  private base = 0;
  /** The record `base` belongs to, compared by identity: a reopened path is a new record. */
  private owner: object | undefined;
  private lh = 0;
  private padTop = 0;
  private tail = 0;
  private flashEl: HTMLElement | null = null;
  private flashRowAt = 0;
  private flashTimer: ReturnType<typeof setTimeout> | undefined;
  private marks: HTMLElement[] = [];
  private markSpec: { readonly from: number; readonly to: number } | null = null;
  private allSelected = false;

  constructor(
    private readonly scroller: HTMLElement,
    private readonly root: HTMLElement,
  ) {
    this.spacer = el("div", { className: "viewer-spacer" });
    this.host = el("div", { className: "viewer-rows" });
    root.replaceChildren(this.spacer, this.host);
    scroller.addEventListener(
      "scroll",
      () => {
        if (this.content !== null && !root.classList.contains("hidden")) {
          this.render();
        }
      },
      { passive: true },
    );
    // The tail is a viewport tall, so in-flow chrome and window resizes move it.
    new ResizeObserver(() => {
      this.refit();
    }).observe(scroller, { box: "border-box" });
  }

  get rows(): WholeRows | null {
    return this.content?.rows ?? null;
  }

  /** Paint `content`, keeping no row of the previous one. Showing the same `owner` object again
   *  keeps the rendered region, so a re-read of a capped file lands where the reader was. Call
   *  while the root is shown. */
  show(content: ViewerContent, owner?: object): void {
    this.clearRows();
    this.clearMark();
    this.setAllSelected(false);
    this.content = content;
    if (owner === undefined || owner !== this.owner) {
      this.base = 0;
    }
    this.owner = owner;
    this.measure();
    const digits = String(content.rows.totalLines).length;
    this.root.style.setProperty("--ln-digits", String(digits));
    this.root.style.setProperty("--viewer-cols", String(content.rows.maxColumns));
    this.layout();
    this.render();
  }

  /** Replace the highlight or the agent marks without re-seating a row; a selection over the
   *  rows survives, direction included. */
  update(content: ViewerContent): void {
    const prev = this.content;
    if (prev?.rows !== content.rows) {
      this.show(content, this.owner);
      return;
    }
    this.content = content;
    if (prev.runs === content.runs) {
      for (const [i, row] of this.seated) {
        this.markRow(row, i);
      }
      return;
    }
    // A new highlight replaces each row's text nodes, which collapses a selection anchored in
    // them, so it is put back by text offset.
    const sel = this.selectionPoints();
    for (const [i, row] of this.seated) {
      this.paintRow(row, i);
    }
    if (sel !== null) {
      this.restoreSelection(sel);
    }
  }

  /** The row at the top of the viewport. */
  topRow(): number {
    return this.content === null ? 0 : this.firstVisibleRow();
  }

  /** Whether the file's last row is within half a line of the viewport's bottom: the end of the
   *  file, never just the end of a height-capped spacer. */
  atBottom(): boolean {
    const rows = this.content?.rows;
    if (rows === undefined) {
      return false;
    }
    const s = this.scroller;
    return s.scrollTop + s.clientHeight >= this.rowTop(rows.total) - this.lh / 2;
  }

  /** Bring the last row into view without moving the view up, when it is in reach of the rendered
   *  region; past the height cap only a jump moves `base`, so a last row out of reach leaves the
   *  view where it is. */
  pinBottom(): void {
    const rows = this.content?.rows;
    if (
      rows === undefined ||
      (this.capped() && rows.total * this.lh - this.base > this.span() + this.lh / 2)
    ) {
      return;
    }
    const s = this.scroller;
    s.scrollTop = Math.max(s.scrollTop, this.rowTop(rows.total) - s.clientHeight);
    this.render();
  }

  /** Bring `row` to the top of the viewport or a third of the way down. `base` moves only when
   *  that viewport would leave the rendered region. */
  scrollToRow(row: number, align: "top" | "third"): void {
    const rows = this.content?.rows;
    if (rows === undefined) {
      return;
    }
    const before = this.base;
    this.layout();
    const target = Math.min(Math.max(0, row), rows.total - 1);
    const y = target * this.lh;
    const view = this.scroller.clientHeight;
    const offset = align === "top" ? 0 : view / 3;
    const from = Math.max(0, y - offset);
    const to = Math.min(from + view, rows.total * this.lh);
    if (from < this.base || to > this.base + this.span()) {
      this.base = y - this.span() / 2;
      this.layout();
    }
    if (this.base !== before) {
      this.repositionAll();
    }
    this.scroller.scrollTop = Math.max(0, this.padTop + y - this.base - offset);
    this.render();
  }

  /** Flash `row` for 1.2 s. */
  flashRow(row: number): void {
    const box = this.flashEl ?? el("div", { className: "editor-line-flash" });
    this.flashEl = box;
    box.remove();
    this.flashRowAt = row;
    this.placeFlash(box);
    this.root.appendChild(box);
    clearTimeout(this.flashTimer);
    this.flashTimer = setTimeout(() => {
      box.remove();
    }, 1200);
  }

  /** Mark text offsets `[from, to)`, across every row they span, and pan to the first piece. */
  markRange(from: number, to: number): void {
    this.markSpec = { from, to };
    this.placeMarks();
    const first = this.marks[0];
    if (first !== undefined) {
      const left = first.offsetLeft;
      const s = this.scroller;
      if (left < s.scrollLeft || left + first.offsetWidth > s.scrollLeft + s.clientWidth) {
        s.scrollLeft = Math.max(0, left - s.clientWidth / 3);
      }
    }
  }

  clearMark(): void {
    this.markSpec = null;
    for (const m of this.marks) {
      m.remove();
    }
    this.marks = [];
  }

  /** The text offset a DOM selection endpoint names, or null when it sits in no seated row. */
  offsetAt(node: Node, offset: number): number | null {
    const rows = this.content?.rows;
    const elOf = node instanceof Element ? node : node.parentElement;
    const rowEl = elOf?.closest<HTMLElement>(".viewer-row");
    if (rows === undefined || rowEl === null || rowEl === undefined || !this.host.contains(rowEl)) {
      return null;
    }
    const i = Number(rowEl.dataset["row"]);
    const start = rows.starts[i] ?? 0;
    const textEl = rowEl.querySelector(".viewer-text");
    if (!textEl?.contains(node)) {
      // The line cell, or the row box itself: the row's edge.
      return node === rowEl && offset > 0 ? (rows.ends[i] ?? start) : start;
    }
    const range = document.createRange();
    range.setStart(textEl, 0);
    range.setEnd(node, offset);
    return Math.min(start + range.toString().length, rows.ends[i] ?? start);
  }

  /** Keep the rows holding these offsets seated while a selection spans them. */
  pinOffsets(offsets: readonly number[]): void {
    const rows = this.content?.rows;
    this.pinned = new Set(rows === undefined ? [] : offsets.map((o) => rows.rowOfOffset(o)));
  }

  get selectsAll(): boolean {
    return this.allSelected;
  }

  setAllSelected(on: boolean): void {
    this.allSelected = on;
    this.root.classList.toggle("is-all-selected", on);
  }

  private measure(): void {
    const style = getComputedStyle(this.root);
    const lh = parseFloat(style.lineHeight);
    const fs = parseFloat(style.fontSize);
    this.lh = Number.isFinite(lh) && lh > 0 ? lh : Number.isFinite(fs) && fs > 0 ? fs * 1.6 : 20;
    const pad = parseFloat(style.paddingBlockStart);
    this.padTop = Number.isFinite(pad) && pad >= 0 ? pad : 0;
  }

  /** Fit the geometry to the scroller's current box and line pitch, keeping the row at the top. */
  private refit(): void {
    if (
      this.content === null ||
      this.root.classList.contains("hidden") ||
      this.scroller.getClientRects().length === 0
    ) {
      return;
    }
    const top = this.firstVisibleRow();
    const { lh, padTop, base } = this;
    this.measure();
    this.layout();
    if (this.lh !== lh || this.padTop !== padTop || this.base !== base) {
      this.repositionAll();
      this.scroller.scrollTop = Math.max(0, this.rowTop(top));
    }
    this.render();
  }

  /** Clamp `base` and size the spacer: the rendered region, plus the tail when that region ends
   *  the file. The tail is measured here because it is a viewport tall. */
  private layout(): void {
    this.tail = this.boxed() ? Math.max(0, this.scroller.clientHeight - this.lh) : 0;
    const virtualH = (this.content?.rows.total ?? 0) * this.lh;
    this.base = Math.min(Math.max(0, this.base), Math.max(0, virtualH - this.span()));
    const tail = this.base + this.span() >= virtualH ? this.tail : 0;
    this.spacer.style.blockSize = `${String(this.span() + tail)}px`;
  }

  /** Whether the scroller's height is its own, probed by growing the spacer. A box its content
   *  sizes has no viewport, and a tail measured off it would grow it on every resize. */
  private boxed(): boolean {
    const style = this.spacer.style;
    const was = style.blockSize;
    const height = this.scroller.clientHeight;
    style.blockSize = `${String((parseFloat(was) || 0) + PROBE_PX)}px`;
    const own = this.scroller.clientHeight === height;
    style.blockSize = was;
    return own;
  }

  /** The rendered region's height: every row, unless they and the tail pass the height cap. */
  private span(): number {
    return Math.min((this.content?.rows.total ?? 0) * this.lh, MAX_SCROLL_PX - this.tail);
  }

  /** Whether the rows are taller than the rendered region. */
  private capped(): boolean {
    return (this.content?.rows.total ?? 0) * this.lh > this.span();
  }

  private rowTop(row: number): number {
    return this.padTop + row * this.lh - this.base;
  }

  private firstVisibleRow(): number {
    const rows = this.content?.rows;
    if (rows === undefined || this.lh <= 0) {
      return 0;
    }
    // One pixel of slack: the engine rounds a written scrollTop, so a row aligned to the top can
    // read back a fraction above it and would name the row before.
    const y = this.scroller.scrollTop - this.padTop + this.base + 1;
    return Math.min(Math.max(0, Math.floor(y / this.lh)), rows.total - 1);
  }

  private render(): void {
    const rows = this.content?.rows;
    if (rows === undefined || this.lh <= 0) {
      return;
    }
    const first = this.firstVisibleRow();
    const visible = Math.ceil(this.scroller.clientHeight / this.lh) + 1;
    // A row seated outside the spacer would extend the native scroll range past the height cap.
    const lo = Math.floor(this.base / this.lh);
    const hi = this.capped() ? Math.floor((this.base + this.span()) / this.lh) : rows.total;
    const end = Math.min(rows.total, hi);
    const from = Math.max(lo, first - OVERSCAN);
    const to = Math.min(end, first + visible + OVERSCAN);
    const inWindow = (i: number): boolean => i >= from && i < to;
    const pinnedHere = (i: number): boolean => this.pinned.has(i) && i >= lo && i < end;
    const keep = (i: number): boolean => inWindow(i) || pinnedHere(i);
    for (const i of this.order) {
      if (!keep(i)) {
        this.seated.get(i)?.remove();
        this.seated.delete(i);
      }
    }
    this.order = this.order.filter(keep);
    const wanted = [...this.pinned].filter((i) => pinnedHere(i) && !inWindow(i));
    for (let i = from; i < to; i++) {
      wanted.push(i);
    }
    for (const i of wanted) {
      if (!this.seated.has(i)) {
        this.seat(i);
      }
    }
    if (this.markSpec !== null) {
      this.placeMarks();
    }
  }

  /** Insert row i at its index position; a seated row is never moved. */
  private seat(i: number): void {
    const row = el("div", { className: "viewer-row" });
    row.dataset["row"] = String(i);
    row.style.top = `${String(this.rowTop(i))}px`;
    this.paintRow(row, i);
    let lo = 0;
    let hi = this.order.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((this.order[mid] ?? 0) < i) {
        lo = mid + 1;
      } else {
        hi = mid;
      }
    }
    const next = this.order[lo];
    this.host.insertBefore(row, next === undefined ? null : (this.seated.get(next) ?? null));
    this.order.splice(lo, 0, i);
    this.seated.set(i, row);
  }

  private paintRow(row: HTMLElement, i: number): void {
    const c = this.content;
    if (c === null) {
      return;
    }
    const line = c.rows.lines[i] ?? 1;
    const cont = c.rows.continues(i);
    const ln = el(
      "span",
      { className: "viewer-ln", "aria-hidden": "true" },
      cont ? CONTINUATION : String(line),
    );
    ln.classList.toggle("gutter-agent-modified", !cont && c.agentLines.has(line));
    const text = el("span", { className: "viewer-text" });
    appendRowText(text, c.rows.text, c.rows.starts[i] ?? 0, c.rows.ends[i] ?? 0, c.runs);
    row.replaceChildren(ln, text);
  }

  /** Re-mark a seated row's line number in place, leaving its text nodes alone. */
  private markRow(row: HTMLElement, i: number): void {
    const c = this.content;
    const ln = row.querySelector(".viewer-ln");
    if (c === null || ln === null) {
      return;
    }
    const line = c.rows.lines[i] ?? 1;
    ln.classList.toggle("gutter-agent-modified", !c.rows.continues(i) && c.agentLines.has(line));
  }

  /** The document selection as row-relative text offsets, when both endpoints sit in seated
   *  rows. */
  private selectionPoints(): { anchor: RowPoint; focus: RowPoint } | null {
    const sel = document.getSelection();
    if (!sel?.anchorNode || !sel.focusNode) {
      return null;
    }
    const anchor = this.rowPoint(sel.anchorNode, sel.anchorOffset);
    const focus = this.rowPoint(sel.focusNode, sel.focusOffset);
    return anchor === null || focus === null ? null : { anchor, focus };
  }

  private rowPoint(node: Node, offset: number): RowPoint | null {
    const at = this.offsetAt(node, offset);
    const rowEl = (node instanceof Element ? node : node.parentElement)?.closest<HTMLElement>(
      ".viewer-row",
    );
    if (at === null || rowEl === null || rowEl === undefined) {
      return null;
    }
    const row = Number(rowEl.dataset["row"]);
    return { row, offset: at - (this.content?.rows.starts[row] ?? 0) };
  }

  private restoreSelection(sel: { anchor: RowPoint; focus: RowPoint }): void {
    const anchor = this.domPoint(sel.anchor);
    const focus = this.domPoint(sel.focus);
    if (anchor !== null && focus !== null) {
      document
        .getSelection()
        ?.setBaseAndExtent(anchor.node, anchor.offset, focus.node, focus.offset);
    }
  }

  private domPoint(p: RowPoint): { node: Node; offset: number } | null {
    const textEl = this.seated.get(p.row)?.querySelector(".viewer-text");
    return textEl === null || textEl === undefined ? null : positionAt(textEl, p.offset);
  }

  private repositionAll(): void {
    for (const [i, row] of this.seated) {
      row.style.top = `${String(this.rowTop(i))}px`;
    }
    if (this.flashEl?.isConnected === true) {
      this.placeFlash(this.flashEl);
    }
  }

  private placeFlash(box: HTMLElement): void {
    box.style.top = `${String(this.rowTop(this.flashRowAt))}px`;
    box.style.height = `${String(this.lh)}px`;
  }

  private clearRows(): void {
    this.host.replaceChildren();
    this.seated.clear();
    this.order = [];
    this.pinned = new Set();
  }

  private placeMarks(): void {
    for (const m of this.marks) {
      m.remove();
    }
    this.marks = [];
    const rows = this.content?.rows;
    const spec = this.markSpec;
    if (rows === undefined || spec === null) {
      return;
    }
    const rootRect = this.root.getBoundingClientRect();
    for (let r = rows.rowOfOffset(spec.from); r < rows.total; r++) {
      const s = rows.starts[r] ?? 0;
      if (s >= spec.to && r !== rows.rowOfOffset(spec.from)) {
        break;
      }
      const row = this.seated.get(r);
      const textEl = row?.querySelector(".viewer-text");
      if (textEl === null || textEl === undefined) {
        continue;
      }
      const a = Math.max(spec.from, s) - s;
      const b = Math.min(spec.to, rows.ends[r] ?? s) - s;
      const box = measureTextSpan(textEl, a, Math.max(a, b));
      const mark = el("div", { className: "editor-find-mark" });
      mark.style.top = `${String(this.rowTop(r))}px`;
      mark.style.left = `${String(box.left - rootRect.left)}px`;
      mark.style.width = `${String(Math.max(box.width, 2))}px`;
      mark.style.height = `${String(this.lh)}px`;
      this.root.appendChild(mark);
      this.marks.push(mark);
    }
  }
}

/** Fill a row's text span from the runs overlapping `[start, end)`: text nodes and classed
 *  spans, never parsed markup. */
function appendRowText(
  host: HTMLElement,
  text: string,
  start: number,
  end: number,
  runs: HighlightRuns | null,
): void {
  if (runs === null || runs.starts.length === 0) {
    host.textContent = text.slice(start, end);
    return;
  }
  let lo = 0;
  let hi = runs.ends.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((runs.ends[mid] ?? 0) <= start) {
      lo = mid + 1;
    } else {
      hi = mid;
    }
  }
  let at = start;
  for (let k = lo; k < runs.starts.length; k++) {
    const rs = runs.starts[k] ?? 0;
    if (rs >= end) {
      break;
    }
    const s = Math.max(rs, start);
    const e = Math.min(runs.ends[k] ?? 0, end);
    if (s > at) {
      host.append(text.slice(at, s));
    }
    const cls = RUN_CLASSES[runs.kinds[k] ?? 0] ?? "keyword";
    host.append(el("span", { className: `hl-${cls}` }, text.slice(s, e)));
    at = e;
  }
  if (at < end) {
    host.append(text.slice(at, end));
  }
}

/** The client box of characters `[a, b)` of a text span's content. */
function measureTextSpan(textEl: Element, a: number, b: number): { left: number; width: number } {
  const range = document.createRange();
  const startPos = positionAt(textEl, a);
  const endPos = positionAt(textEl, b);
  range.setStart(startPos.node, startPos.offset);
  range.setEnd(endPos.node, endPos.offset);
  const rect = range.getBoundingClientRect();
  if (a === b) {
    return { left: rect.left, width: 0 };
  }
  return { left: rect.left, width: rect.width };
}

function positionAt(root: Element, offset: number): { node: Node; offset: number } {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let left = offset;
  let last: Text | null = null;
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    const t = n as Text;
    if (left <= t.length) {
      return { node: t, offset: left };
    }
    left -= t.length;
    last = t;
  }
  return last === null ? { node: root, offset: 0 } : { node: last, offset: last.length };
}

/** The edit state's geometry over the textarea, which is the scroller: the visible line numbers
 *  beside it, translated by its `scrollTop`, and the line arithmetic that scrolls it. */
export class EditSurface {
  private lines = 1;
  private marks: ReadonlySet<number> = new Set();
  /** Whether the frame's height is its own; null until probed. */
  private boxed: boolean | null = null;
  private readonly inner: HTMLElement;

  /** `frame` is the box the textarea fills (20-editor.css `.is-editing`). */
  constructor(
    private readonly host: HTMLElement,
    private readonly area: HTMLTextAreaElement,
    private readonly frame: HTMLElement,
  ) {
    this.inner = el("div", { className: "viewer-edit-lines" });
    host.replaceChildren(this.inner);
    area.addEventListener(
      "scroll",
      () => {
        this.render();
      },
      { passive: true },
    );
    new ResizeObserver(() => {
      this.boxed = null;
      this.layout();
    }).observe(frame, { box: "border-box" });
  }

  /** Call whenever the textarea's text or the agent marks change, while it is shown. */
  setLines(count: number, marks: ReadonlySet<number>): void {
    this.lines = Math.max(1, count);
    this.marks = marks;
    this.host.style.setProperty("--ln-digits", String(String(this.lines).length));
    this.layout();
  }

  /** The 1-based line at the textarea's top. One pixel of slack: the engine rounds a written
   *  scrollTop down, so a line restored to the top would otherwise read as the one above. */
  topLine(): number {
    const { lh, pad } = this.metrics();
    return Math.max(1, Math.floor((this.area.scrollTop - pad + 1) / lh) + 1);
  }

  /** Scroll 1-based `line` to the top or a third of the way down; a line past the end lands on the
   *  last one. */
  scrollToLine(line: number, align: "top" | "third"): LineLocation {
    const shown = Math.min(Math.max(1, Math.floor(line)), this.lines);
    // A jump issued in the task that resized the frame lands before the observer runs.
    this.layout();
    const { lh, pad } = this.metrics();
    const offset = align === "top" ? 0 : this.area.clientHeight / 3;
    this.area.scrollTop = Math.max(0, pad + (shown - 1) * lh - offset);
    return { row: shown - 1, line: shown, clamped: shown !== line };
  }

  render(): void {
    const { lh, pad } = this.metrics();
    const top = this.area.scrollTop;
    const first = Math.max(0, Math.floor((top - pad) / lh) - 2);
    const count = Math.ceil(this.area.clientHeight / lh) + 4;
    const last = Math.min(this.lines, first + count);
    const cells: HTMLElement[] = [];
    for (let i = first; i < last; i++) {
      const cell = el("div", { className: "viewer-ln" }, String(i + 1));
      cell.classList.toggle("gutter-agent-modified", this.marks.has(i + 1));
      cells.push(cell);
    }
    this.inner.replaceChildren(...cells);
    this.inner.style.transform = `translateY(${String(pad + first * lh - top)}px)`;
  }

  /** Where 1-based `line`'s box sits in the textarea's scrolled content. */
  lineBox(line: number): { readonly top: number; readonly height: number } {
    const { lh, pad } = this.metrics();
    return { top: pad + (line - 1) * lh, height: lh };
  }

  /** The line pitch is measured off the scroll extent: the engine snaps each line box (19.2px lays
   *  out at 19.1875 in Chromium), so the computed line-height drifts a line every 1,500. An extent
   *  no taller than the box is the box, not the text, and falls back to the computed value. */
  private metrics(): { readonly lh: number; readonly pad: number } {
    const style = getComputedStyle(this.area);
    const pad = parseFloat(style.paddingBlockStart) || 0;
    const extent = this.area.scrollHeight;
    const lh =
      extent > this.area.clientHeight
        ? (extent - pad - (parseFloat(style.paddingBlockEnd) || 0)) / this.lines
        : parseFloat(style.lineHeight) || 20;
    return { lh, pad };
  }

  /** Size the textarea's `--edit-tail` (20-editor.css), the blank extent that lets the last line
   *  scroll to the top, and draw the gutter. The tail is measured off the frame: its padding
   *  holds the textarea's own box open, so a textarea-sized tail never lets it shrink. */
  private layout(): void {
    if (this.area.getClientRects().length === 0) {
      return;
    }
    this.boxed ??= this.probeBoxed();
    const tail = this.boxed ? Math.max(0, this.frame.clientHeight - this.metrics().lh) : 0;
    this.area.style.setProperty("--edit-tail", `${String(tail)}px`);
    this.render();
  }

  /** Whether the frame's height is its own, probed by growing the tail. Cached until the frame
   *  resizes, because `setLines` runs on every keystroke. */
  private probeBoxed(): boolean {
    const style = this.area.style;
    const was = style.getPropertyValue("--edit-tail");
    const height = this.frame.clientHeight;
    style.setProperty("--edit-tail", `${String((parseFloat(was) || 0) + PROBE_PX)}px`);
    const own = this.frame.clientHeight === height;
    style.setProperty("--edit-tail", was);
    return own;
  }
}

/** The edit state's find mark and line flash, placed over the textarea from its surface's line
 *  boxes and re-placed as it scrolls. */
export class EditDecor {
  private flashEl: HTMLElement | null = null;
  private flashLine = 0;
  private flashTimer: ReturnType<typeof setTimeout> | undefined;
  private markEl: HTMLElement | null = null;
  private markSpec: {
    readonly line: number;
    readonly start: number;
    readonly width: number;
  } | null = null;

  constructor(
    private readonly body: HTMLElement,
    private readonly area: HTMLTextAreaElement,
    private readonly surface: EditSurface,
  ) {
    area.addEventListener(
      "scroll",
      () => {
        this.place();
      },
      { passive: true },
    );
  }

  flash(line: number): void {
    const box = this.flashEl ?? el("div", { className: "editor-line-flash" });
    this.flashEl = box;
    this.flashLine = line;
    box.remove();
    this.body.appendChild(box);
    this.place();
    clearTimeout(this.flashTimer);
    this.flashTimer = setTimeout(() => {
      box.remove();
    }, 1200);
  }

  /** Mark `match` on `line`, `prefix` being the line's text before it, measured in the
   *  textarea's own font so tabs and wide glyphs land where they render. */
  mark(line: number, prefix: string, match: string): void {
    const probe = el("pre", {
      className: "editor-content editor-find-probe",
      "aria-hidden": "true",
    });
    const before = el("span", {}, prefix);
    const hit = el("span", {}, match);
    probe.append(before, hit);
    this.body.appendChild(probe);
    const probeRect = probe.getBoundingClientRect();
    const hitRect = hit.getBoundingClientRect();
    probe.remove();
    this.markSpec = { line, start: hitRect.left - probeRect.left, width: hitRect.width };
    const from = this.area.scrollLeft;
    if (
      this.markSpec.start < from ||
      this.markSpec.start + this.markSpec.width > from + this.area.clientWidth
    ) {
      this.area.scrollLeft = Math.max(0, this.markSpec.start - this.area.clientWidth / 3);
    }
    this.markEl ??= el("div", { className: "editor-find-mark" });
    if (this.markEl.parentNode !== this.body) {
      this.body.appendChild(this.markEl);
    }
    this.place();
  }

  clearMark(): void {
    this.markSpec = null;
    this.markEl?.remove();
  }

  private place(): void {
    const top0 = this.area.offsetTop - this.area.scrollTop;
    if (this.flashEl?.isConnected === true) {
      const box = this.surface.lineBox(this.flashLine);
      this.flashEl.style.top = `${String(top0 + box.top)}px`;
      this.flashEl.style.height = `${String(box.height)}px`;
    }
    const spec = this.markSpec;
    if (spec !== null && this.markEl !== null) {
      const box = this.surface.lineBox(spec.line);
      this.markEl.style.top = `${String(top0 + box.top)}px`;
      // The probe carries the textarea's class, so `start` already includes its border and padding.
      this.markEl.style.left = `${String(this.area.offsetLeft + spec.start - this.area.scrollLeft)}px`;
      this.markEl.style.width = `${String(spec.width)}px`;
      this.markEl.style.height = `${String(box.height)}px`;
    }
  }
}
