// Diff pane: one `DiffLine[]` as two columns (versions) or one (unified, the cheaper path). Consumers: chat inline
// previews, the editor's diff mode, the conflict popup.

import { lineDiff, wordMarks, type CharRange, type DiffLine } from "./diff.js";
import { highlightMarked, resolveLangHint } from "./highlight.js";
import { el } from "@cplieger/reactive";
import { CHROME_ATTR } from "./chrome-attr.js";

interface DiffPaneOpts {
  /** Drop rows beyond this and append a "+N more" footer. */
  maxRows?: number;
  /** Label above the old (left) column. */
  oldLabel?: string;
  /** Label above the new (right) column. */
  newLabel?: string;
  /** Whether to show gutter line numbers. Default true. */
  lineNumbers?: boolean;
  /** Lock the columns' horizontal scroll (default true; ignored when `unified`). Vertical always shares the body. */
  syncScroll?: boolean;
  /**
   * One column instead of two (default false); `lang` highlights it. The change signal stays on the row background
   * and `+`/`-` marker, since text colour belongs to highlighting.
   */
  unified?: boolean;
  /** Highlighting hint (path, extension or language id; `resolveLangHint`), applied to both shapes, deletions included. */
  lang?: string;
  /** Source texts; when supplied the pane owns an "Ignore whitespace" toggle that re-diffs in place. */
  source?: { oldText: string; newText: string };
  /** Fires when the whitespace toggle flips; ignored when `source` is set. */
  onToggleWhitespace?: (ignoreWhitespace: boolean) => void;
  /** Draw the change map (default true for two-pane, ignored for `unified`: a mark's side carries its kind). */
  changeMap?: boolean;
}

/** What survives a whitespace re-diff; the label row is part of the body grid in two-pane. */
const CHROME_ROWS = ".diff-pane-toolbar";

/** Build a two-pane diff element. The caller appends it to the DOM. */
export function renderDiffPane(lines: DiffLine[], opts: DiffPaneOpts = {}): HTMLDivElement {
  const lineNumbers = opts.lineNumbers !== false;
  const syncScroll = opts.syncScroll !== false;
  const container = el("div", { className: "diff-pane" }) as HTMLDivElement;

  // A toolbar control, not a caption: sharing the label row made captions shrink off their columns.
  if (opts.source !== undefined || opts.onToggleWhitespace !== undefined) {
    container.appendChild(
      el("div", { className: "diff-pane-toolbar" }, buildWhitespaceToggle(container, opts)),
    );
  }
  // Inside the body in two-pane, so a caption and its column share one grid track.
  const header =
    opts.oldLabel !== undefined || opts.newLabel !== undefined
      ? (el(
          "div",
          { className: "diff-pane-header" },
          el("span", { className: "diff-pane-label diff-pane-label-old" }, opts.oldLabel ?? ""),
          el("span", { className: "diff-pane-label diff-pane-label-new" }, opts.newLabel ?? ""),
        ) as HTMLDivElement)
      : null;

  const unified = opts.unified === true;
  const limit = opts.maxRows ?? Number.POSITIVE_INFINITY;
  const lang = opts.lang !== undefined && opts.lang !== "" ? resolveLangHint(opts.lang) : "";
  let rowCount = 0;

  // An all-context diff renders a no-changes state; reachable once a chat's write is committed.
  if (!lines.some((l) => l.kind !== "ctx")) {
    if (header !== null) {
      container.appendChild(header);
    }
    container.appendChild(
      el(
        "div",
        { className: "diff-none" },
        lines.length === 0 ? "Empty file" : "No changes between these versions",
      ),
    );
    return container;
  }

  // Computed once for the whole diff, before windowing.
  const marks = wordMarks(lines);

  if (unified) {
    container.classList.add("diff-pane-unified");
    if (header !== null) {
      container.appendChild(header);
    }
    const col = el("div", { className: "diff-col diff-col-unified" }) as HTMLDivElement;
    container.appendChild(el("div", { className: "diff-pane-body" }, col));
    for (const line of lines) {
      if (rowCount >= limit) {
        break;
      }
      col.appendChild(makeUnifiedRow(line, lineNumbers, lang, marks.get(line)));
      rowCount++;
    }
    return finishPane(container, lines, rowCount);
  }

  // The body is the one vertical scroller (columns are its grid cells), so sides cannot shear. Each
  // column is a tab stop so both axes are keyboard-reachable (WCAG 2.1.1).
  const colAttrs = (side: string): Record<string, string> => ({
    className: `diff-col diff-col-${side}`,
    tabindex: "0",
  });
  const leftCol = el("div", colAttrs("old")) as HTMLDivElement;
  const rightCol = el("div", colAttrs("new")) as HTMLDivElement;
  const body = el("div", { className: "diff-pane-body diff-pane-split" }) as HTMLDivElement;
  if (header !== null) {
    body.appendChild(header);
  }
  body.appendChild(leftCol);
  body.appendChild(rightCol);

  // The map and bar are the scroller's siblings: a grid cell is as tall as the file.
  const viewport = el("div", { className: "diff-pane-viewport" }, body) as HTMLDivElement;
  container.appendChild(viewport);

  for (const line of lines) {
    if (rowCount >= limit) {
      break;
    }
    appendRow(leftCol, rightCol, line, lineNumbers, lang, marks.get(line));
    rowCount++;
  }
  finishPane(container, lines, rowCount);

  if (syncScroll) {
    wireHorizontalScroll(viewport, leftCol, rightCol);
  }

  if (opts.changeMap !== false && rowCount > 0) {
    container.classList.add("diff-pane-mapped");
    const map = buildChangeMap(lines, rowCount);
    viewport.appendChild(map);
    wireChangeMap(map, body);
  }

  return container;
}

function finishPane(
  container: HTMLDivElement,
  lines: DiffLine[],
  rowCount: number,
): HTMLDivElement {
  const extra = Math.max(0, lines.length - rowCount);
  if (extra > 0) {
    container.appendChild(
      el(
        "div",
        { className: "diff-more", [CHROME_ATTR]: "" },
        `+${String(extra)} more line${extra === 1 ? "" : "s"}`,
      ),
    );
  }
  return container;
}

/** Deleted lines are highlighted too: what was replaced is often the question. */
function makeUnifiedRow(
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): HTMLDivElement {
  const row = el("div", { className: `diff-row diff-row-${line.kind}` }) as HTMLDivElement;
  if (lineNumbers) {
    // The new number unless deleted.
    const no = line.kind === "del" ? line.oldNo : line.newNo;
    row.appendChild(
      el("span", { className: "diff-gutter", [CHROME_ATTR]: "" }, no > 0 ? String(no) : ""),
    );
  }
  const marker = line.kind === "add" ? "+" : line.kind === "del" ? "-" : " ";
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el("span", { className: "diff-marker", [CHROME_ATTR]: "" }, marker),
      lineText(line, lang, marks),
    ),
  );
  return row;
}

/** Shared by both shapes so a click-through never lands on a plainer rendering. */
function lineText(
  line: DiffLine,
  lang: string,
  marks: readonly CharRange[] | undefined,
): HTMLSpanElement {
  const text = el("span", { className: "diff-line-text" });
  const spans = marks ?? [];
  if (lang === "" && spans.length === 0) {
    text.textContent = line.text;
    return text;
  }
  const wordClass = line.kind === "del" ? "diff-word-del" : "diff-word-add";
  text.innerHTML = highlightMarked(line.text, lang, spans, wordClass);
  return text;
}

function appendRow(
  leftCol: HTMLDivElement,
  rightCol: HTMLDivElement,
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): void {
  // Each row holds the same slot on both sides, even when one is empty, for scroll sync.
  const [leftRow, rightRow] = makeRowPair(line, lineNumbers, lang, marks);
  leftCol.appendChild(leftRow);
  rightCol.appendChild(rightRow);
}

function makeRowPair(
  line: DiffLine,
  lineNumbers: boolean,
  lang: string,
  marks?: readonly CharRange[],
): [HTMLDivElement, HTMLDivElement] {
  const left = el("div", { className: "diff-row" }) as HTMLDivElement;
  const right = el("div", { className: "diff-row" }) as HTMLDivElement;

  if (line.kind === "ctx") {
    populateRow(left, line.oldNo, "ctx", lineNumbers, lineText(line, lang, undefined));
    populateRow(right, line.newNo, "ctx", lineNumbers, lineText(line, lang, undefined));
  } else if (line.kind === "del") {
    populateRow(left, line.oldNo, "del", lineNumbers, lineText(line, lang, marks));
    populateRow(right, 0, "empty", lineNumbers, null);
  } else {
    populateRow(left, 0, "empty", lineNumbers, null);
    populateRow(right, line.newNo, "add", lineNumbers, lineText(line, lang, marks));
  }
  return [left, right];
}

function populateRow(
  row: HTMLDivElement,
  lineNo: number,
  kind: "add" | "del" | "ctx" | "empty",
  lineNumbers: boolean,
  text: HTMLSpanElement | null,
): void {
  row.classList.add(`diff-row-${kind}`);
  if (lineNumbers) {
    row.appendChild(
      el("span", { className: "diff-gutter", [CHROME_ATTR]: "" }, lineNo > 0 ? String(lineNo) : ""),
    );
  }
  // Marker glyph so colour-blind users still read the row kind.
  row.appendChild(
    el(
      "span",
      { className: "diff-content" },
      el(
        "span",
        { className: "diff-marker", [CHROME_ATTR]: "" },
        kind === "add" ? "+" : kind === "del" ? "-" : " ",
      ),
      text ?? el("span", { className: "diff-line-text" }),
    ),
  );
}

/** One shared horizontal bar at the scrollport's bottom */
function wireHorizontalScroll(
  viewport: HTMLDivElement,
  left: HTMLDivElement,
  right: HTMLDivElement,
): void {
  const spacer = el("div", { className: "diff-pane-hbar-spacer" }) as HTMLDivElement;
  // A pointer duplicate of scrolling the focusable columns already provide.
  const bar = el(
    "div",
    { className: "diff-pane-hbar", "aria-hidden": "true" },
    spacer,
  ) as HTMLDivElement;
  viewport.appendChild(bar);

  // Guarded on the values differing, so a write's own scroll event writes nothing and no lock is needed.
  const drive =
    (from: HTMLElement, ...targets: HTMLElement[]) =>
    (): void => {
      for (const to of targets) {
        if (to.scrollLeft !== from.scrollLeft) {
          to.scrollLeft = from.scrollLeft;
        }
      }
    };
  bar.addEventListener("scroll", drive(bar, left, right));
  left.addEventListener("scroll", drive(left, right, bar));
  right.addEventListener("scroll", drive(right, left, bar));

  const measure = (): void => {
    const span = Math.max(left.scrollWidth, right.scrollWidth);
    const range = span - left.clientWidth;
    const idle = range <= 1;
    bar.classList.toggle("is-idle", idle);
    // The bar spans both columns but the range is one column's, so the spacer buys the range.
    spacer.style.inlineSize = `${String(bar.clientWidth + Math.max(0, range))}px`;
    // The span is published only while there is a range, and cleared otherwise: `.diff-col-old` spends 1px on the
    // divider, so a max over both columns overscrolls a fitting diff by 1px (60-mcp.css `.editor-diff-pane`).
    if (idle) {
      viewport.style.removeProperty("--diff-hspan");
    } else {
      viewport.style.setProperty("--diff-hspan", `${String(span)}px`);
      // The bar's range arrives only now, so a restored column position is re-applied from here.
      const at = Math.max(left.scrollLeft, right.scrollLeft);
      if (bar.scrollLeft !== at) {
        bar.scrollLeft = at;
      }
    }
  };
  // Deferred a frame: `--diff-hspan` is written on an ancestor of the observed column, so writing it inside the
  // delivery causes a ResizeObserver loop error. Same shape as `scroll.ts`'s `scheduleScrollbarWidth`.
  let measureFrame = 0;
  const scheduleMeasure = (): void => {
    if (measureFrame !== 0) {
      return;
    }
    measureFrame = requestAnimationFrame(() => {
      measureFrame = 0;
      measure();
    });
  };
  // Fires once on observe, the first real measurement (the pane is detached while built).
  new ResizeObserver(scheduleMeasure).observe(left);
}

/** The "Ignore whitespace" checkbox; with `opts.source` it re-diffs and re-renders the pane itself. */
function buildWhitespaceToggle(container: HTMLDivElement, opts: DiffPaneOpts): HTMLLabelElement {
  const input = el("input", { type: "checkbox" }) as HTMLInputElement;
  const wrap = el(
    "label",
    {
      className: "diff-pane-ws-toggle",
      "data-tooltip": "Treat a line that differs only in spacing or indentation as unchanged",
    },
    input,
    el("span", {}, "Ignore whitespace"),
  ) as HTMLLabelElement;
  input.addEventListener("change", () => {
    const ignore = input.checked;
    if (opts.source === undefined && opts.onToggleWhitespace !== undefined) {
      opts.onToggleWhitespace(ignore);
    }
    if (opts.source !== undefined) {
      // The re-rendered pane drops `source`, or it would attach a second toggle.
      const source = opts.source;
      const { source: _, ...freshOpts } = opts;
      const freshDiffOpts: DiffPaneOpts = freshOpts;
      const fresh = lineDiff(source.oldText, source.newText, { ignoreWhitespace: ignore });
      const rerendered = renderDiffPane(fresh, freshDiffOpts);
      // Swap derived rows, keep chrome by identity: the toolbar is the first row.
      for (const child of [...container.children]) {
        if (!child.matches(CHROME_ROWS)) {
          child.remove();
        }
      }
      container.classList.toggle(
        "diff-pane-mapped",
        rerendered.classList.contains("diff-pane-mapped"),
      );
      for (const child of [...rerendered.children]) {
        if (!child.matches(CHROME_ROWS)) {
          container.appendChild(child);
        }
      }
    }
  });
  return wrap;
}

interface ChangeRun {
  readonly start: number;
  readonly len: number;
  readonly kind: "add" | "del";
}

/** Runs break on a kind change too: the map may name only kinds the rows show. */
function changeRuns(lines: readonly DiffLine[], rowCount: number): ChangeRun[] {
  const runs: ChangeRun[] = [];
  const end = Math.min(lines.length, rowCount);
  let start = -1;
  let open: "add" | "del" | null = null;
  const flush = (at: number): void => {
    if (start >= 0 && open !== null) {
      runs.push({ start, len: at - start, kind: open });
    }
    start = -1;
    open = null;
  };
  for (let i = 0; i < end; i++) {
    const kind = lines[i]?.kind;
    if (kind === "add" || kind === "del") {
      if (kind !== open) {
        flush(i);
        start = i;
        open = kind;
      }
      continue;
    }
    flush(i);
  }
  flush(end);
  return runs;
}

/** One mark per run; `aria-hidden` and unfocusable, since the rows are the accessible statement. */
function buildChangeMap(lines: readonly DiffLine[], rowCount: number): HTMLDivElement {
  const map = el("div", {
    className: "diff-map",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  const pct = (rows: number): string => `${((rows / rowCount) * 100).toFixed(4)}%`;
  for (const run of changeRuns(lines, rowCount)) {
    const mark = el("div", {
      className: `diff-map-mark diff-map-mark-${run.kind}`,
    }) as HTMLDivElement;
    mark.style.top = pct(run.start);
    mark.style.height = pct(run.len);
    map.appendChild(mark);
  }
  return map;
}

/** Press or drag scrolls the body; the map reports nothing. */
function wireChangeMap(map: HTMLDivElement, body: HTMLDivElement): void {
  const jumpTo = (clientY: number): void => {
    const box = map.getBoundingClientRect();
    if (box.height <= 0) {
      return;
    }
    const frac = Math.min(1, Math.max(0, (clientY - box.top) / box.height));
    // Centre the landing so the mark's context above stays in view.
    body.scrollTop = Math.max(0, frac * body.scrollHeight - body.clientHeight / 2);
  };
  map.addEventListener("pointerdown", (e: PointerEvent) => {
    map.setPointerCapture(e.pointerId);
    jumpTo(e.clientY);
  });
  map.addEventListener("pointermove", (e: PointerEvent) => {
    if (map.hasPointerCapture(e.pointerId)) {
      jumpTo(e.clientY);
    }
  });
}
