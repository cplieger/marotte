// The exec view's tree pane: the execution's structure (a loop ran twice, three ran at once).
// A container's rolled-up state draws no in-flight ring: only a step or watch is work
// (`31-exec-view.css`). SELECTION, not disclosure, so live rows never move; a work node never
// collapses. ONE FOLD PER BOX: only a top-level container discloses, and `applyCollapse` is the one
// writer that honours it.

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import {
  ICON_EXEC_GROUP,
  ICON_EXEC_PARALLEL,
  ICON_EXEC_SEQUENCE,
  ICON_EXEC_WATCH,
  ICON_REFRESH,
  ICON_TAB_AGENT,
} from "../icons.js";
import { formatElapsed } from "../strings.js";
import { reconcile } from "../reconcile.js";
import {
  currentWork,
  elapsed,
  isWork,
  newestStarted,
  workNodes,
  type ExecKind,
  type ExecNode,
  type ExecRun,
} from "./model.js";
import { paintLabel } from "./label.js";
import { setAttr, setText } from "../dom.js";
import { STATE_WORD, inFlight, paintStateMark, type ExecState } from "./status.js";
import {
  bandOf,
  gapEl,
  offsetsOf,
  pitchOf,
  rangeOf,
  sizeGap,
  watchScroller,
} from "./row-window.js";

/** The kind glyph: a step gets the agent hexagon; a container gets a glyph
 *  naming what it does, since "sequence"/"parallel" shouldn't require reading.
 *  Exhaustive with no default, so a new `ExecKind` fails the type check. */
export function kindGlyph(kind: ExecKind): Element {
  switch (kind) {
    case "step":
      return iconEl(ICON_TAB_AGENT);
    case "sequence":
      return iconEl(ICON_EXEC_SEQUENCE);
    case "repeat":
      return iconEl(ICON_REFRESH);
    case "parallel":
      return iconEl(ICON_EXEC_PARALLEL);
    case "watch":
      return iconEl(ICON_EXEC_WATCH);
    case "group":
      return iconEl(ICON_EXEC_GROUP);
  }
}

export interface ExecTreeView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. Rows are reconciled by PATH, so a collapsed
   *  container and the selected row survive the refetches a live run drives. */
  render(nodes: readonly ExecNode[], selected: string): void;
  /** Draw the rows `scroller` shows, plus an overscan, and follow its scroll and size; null
   *  unmounts every row, so a tree nobody sees holds nothing. Until then `render` only records. */
  attach(scroller: HTMLElement | null): void;
  /** Advance the durations of the rows still running. */
  tick(): void;
  /** Unfold every container above `path`'s row and centre the row in `scroller`, the box the tree
   *  scrolls in, where the tree is long enough. Moves no other scroller and never animates. */
  reveal(path: string, scroller: HTMLElement): void;
}

interface Row {
  root: HTMLElement;
  label: HTMLElement;
  sub: HTMLElement;
  dur: HTMLElement;
  glyph: HTMLElement;
  kindSlot: HTMLElement;
  chevron: HTMLElement;
  kids: HTMLElement | null;
  start?: string;
  end?: string;
  /** Top-level only — see the ONE FOLD PER BOX note at the top of this file. */
  collapsible: boolean;
}

/** One seated child of a sibling list: a row, or the spacer for the rows the window skips. */
type Slot =
  | { readonly node: ExecNode; readonly pos: number }
  | { readonly gap: "lead" | "tail"; readonly px: number };

/** The rows the tree would draw unwindowed, in document order: each path's index, the index past
 *  its subtree, and every row's top. */
interface Flat {
  readonly at: ReadonlyMap<string, number>;
  readonly end: ReadonlyMap<string, number>;
  readonly offsets: Float64Array;
}

/** A row's height before one is measured: the `--row-tap` floor at the default type scale. */
const ROW_PX = 36;

/** Build the tree pane. `onSelect` is injected so this file points only
 *  downward; the page owns what selection means. */
export function buildExecTree(onSelect: (path: string) => void): ExecTreeView {
  const root = el("div", {
    className: "ev-tree",
    role: "tree",
    "aria-label": "Execution steps",
  });
  /** Each mounted row's parts, by its root; a removed row is collected with it. */
  const rowParts = new WeakMap<HTMLElement, Row>();
  /** The top-level containers the reader folded, by path: a fold outlives its row's unmount. */
  const folded = new Set<string>();

  let nodes: readonly ExecNode[] = [];
  let selected = "";
  let box: HTMLElement | null = null;
  let release: (() => void) | undefined;
  let rowPx = ROW_PX;
  /** `.ev-tree`'s gap, which only top-level entries take, and a group box's two border lines. */
  let topGap = 0;
  let boxPx = 0;
  let measured = false;
  let flat: Flat = { at: new Map(), end: new Map(), offsets: new Float64Array(1) };
  let range = { from: 0, to: 0 };

  function flatOf(): Flat {
    const at = new Map<string, number>();
    const end = new Map<string, number>();
    const heights: number[] = [];
    const walk = (n: ExecNode, depth: number): void => {
      at.set(n.path, heights.length);
      const top = depth === 0 && heights.length > 0 ? topGap : 0;
      heights.push(rowPx + top + (depth === 0 && n.children.length > 0 ? boxPx : 0));
      if (!(depth === 0 && folded.has(n.path))) {
        for (const k of n.children) {
          walk(k, depth + 1);
        }
      }
      end.set(n.path, heights.length);
    };
    for (const n of nodes) {
      walk(n, 0);
    }
    return { at, end, offsets: offsetsOf(heights) };
  }

  function buildRow(node: ExecNode, depth: number): Row {
    const glyph = el("span", { className: "ev-state", "aria-hidden": "true" });
    const kindSlot = el("span", { className: "ev-kind", "aria-hidden": "true" });
    const label = el("span", { className: "ev-label" });
    const sub = el("span", { className: "ev-sub" });
    const dur = el("span", { className: "ev-dur" });
    // A span, not a button: the row itself is the control. A `<button>` inside
    // an element carrying `role="treeitem"` plus a click handler is axe's
    // `nested-interactive`, and `aria-hidden` does not clear it.
    const chevron = el("span", { className: "ev-twist", "aria-hidden": "true" }, chevronEl());

    const head = el(
      "div",
      { className: "ev-row-main" },
      chevron,
      glyph,
      kindSlot,
      el("span", { className: "ev-text" }, label, sub),
      dur,
    );
    const rowRoot = el("div", {
      className: "ev-row",
      role: "treeitem",
      tabindex: "-1",
      "data-path": node.path,
    });
    // One indent step per level: a top-level group's twist is one step wide, so a direct child's
    // dot lands in its parent's dot column, and each deeper level moves one more step.
    rowRoot.style.setProperty("--ev-depth", String(depth));
    if (depth >= 1) {
      // A direct child's dot sits in its parent's column, so its one guide runs through that dot.
      rowRoot.dataset["guides"] = depth === 1 ? "through-dot" : "";
    }
    rowRoot.appendChild(head);

    const row: Row = {
      root: rowRoot,
      label,
      sub,
      dur,
      glyph,
      kindSlot,
      chevron,
      kids: null,
      collapsible: depth === 0,
    };

    head.addEventListener("click", (e) => {
      // The twist is a zone of the row, not a nested control; the handler tells
      // them apart by target, keeping the row one activation target.
      if (row.collapsible && chevron.contains(e.target as Node) && row.kids !== null) {
        if (!folded.delete(node.path)) {
          folded.add(node.path);
        }
        flat = flatOf();
        paintWindow();
        return;
      }
      onSelect(node.path);
    });
    head.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onSelect(node.path);
      }
    });
    return row;
  }

  /** The one writer of a row's fold state, in all three channels. Reads `collapsible`, so a nested
   *  container is held open and carries no `aria-expanded`. */
  function applyCollapse(row: Row, path: string): void {
    const collapsed = row.collapsible && folded.has(path);
    row.root.classList.toggle("ev-collapsed", collapsed);
    setAttr(row.root, "aria-expanded", row.collapsible ? String(!collapsed) : null);
    if (row.kids !== null) {
      setAttr(row.kids, "hidden", collapsed ? "" : null);
    }
  }

  /** The slots of one sibling list under the current window: the rows it touches, and a spacer for
   *  each run of rows it skips. A folded box's list is empty. */
  function slotsOf(siblings: readonly ExecNode[]): Slot[] {
    const first = siblings[0];
    const last = siblings.at(-1);
    if (first === undefined || last === undefined) {
      return [];
    }
    const top = flat.at.get(first.path);
    const bottom = flat.end.get(last.path);
    if (top === undefined || bottom === undefined) {
      return [];
    }
    const out: Slot[] = [];
    let seatedEnd = top;
    siblings.forEach((node, pos) => {
      const at = flat.at.get(node.path) ?? 0;
      const end = flat.end.get(node.path) ?? at;
      if (end > range.from && at < range.to) {
        if (out.length === 0 && at > top) {
          out.push({ gap: "lead", px: px(top, at) });
        }
        out.push({ node, pos });
        seatedEnd = end;
      }
    });
    if (out.length === 0) {
      return [{ gap: "lead", px: px(top, bottom) }];
    }
    if (bottom > seatedEnd) {
      out.push({ gap: "tail", px: px(seatedEnd, bottom) });
    }
    return out;
  }

  function px(from: number, to: number): number {
    return (flat.offsets[to] ?? 0) - (flat.offsets[from] ?? 0);
  }

  /** One sibling list, keyed by path rather than kind: `paint` repaints a kind change in place,
   *  so the row element, with its fold and focus, survives it. */
  function seatRows(into: HTMLElement, siblings: readonly ExecNode[], depth: number): void {
    reconcile(into, slotsOf(siblings), {
      key: (s) => ("gap" in s ? `\u0001${s.gap}` : s.node.path),
      mount: (s) => {
        if ("gap" in s) {
          const gap = gapEl();
          sizeGap(gap, s.px);
          return gap;
        }
        const row = buildRow(s.node, depth);
        rowParts.set(row.root, row);
        paint(row, s.node, depth, s.pos, siblings.length);
        return row.root;
      },
      update: (slotEl, s) => {
        if ("gap" in s) {
          sizeGap(slotEl, s.px);
          return;
        }
        const row = rowParts.get(slotEl);
        if (row !== undefined) {
          paint(row, s.node, depth, s.pos, siblings.length);
        }
      },
    });
  }

  function paint(row: Row, node: ExecNode, depth: number, pos: number, size: number): void {
    paintLabel(row.label, node);
    setText(row.sub, node.subtitle ?? "");
    setAttr(row.sub, "hidden", (node.subtitle ?? "") === "" ? "" : null);
    setAttr(row.root, "data-state", node.state);
    // A windowed tree mounts only part of each list, so a row states its own place in it.
    setAttr(row.root, "aria-posinset", String(pos + 1));
    setAttr(row.root, "aria-setsize", String(size));
    // Rows are keyed by PATH, and a replanned run can put a different kind at one path.
    if (row.root.dataset["kind"] !== node.kind) {
      row.kindSlot.replaceChildren(kindGlyph(node.kind));
      row.root.dataset["kind"] = node.kind;
    }
    // The GROUP BOX. Toggled rather than added, because `paint` re-runs for the same
    // reconciled row and a node gains children as the run progresses.
    row.root.classList.toggle("ev-group", depth === 0 && node.children.length > 0);
    paintStateMark(row.glyph, node.state);
    // Assigned through locals: the fields are optional under
    // exactOptionalPropertyTypes, so `undefined` is not a value they accept.
    if (node.start === undefined) {
      delete row.start;
    } else {
      row.start = node.start;
    }
    if (node.end === undefined) {
      delete row.end;
    } else {
      row.end = node.end;
    }
    const ms = elapsed(node.start, node.end);
    setText(row.dur, ms > 0 ? formatElapsed(ms) : "");
    row.root.classList.toggle("ev-selected", node.path === selected);
    setAttr(row.root, "aria-selected", String(node.path === selected));
    // Read back off the rendered text, so it cannot disagree with the screen.
    setAttr(
      row.root,
      "aria-label",
      `${node.label}, ${STATE_WORD[node.state]}${node.subtitle === undefined ? "" : `, ${node.subtitle}`}`,
    );

    if (node.children.length === 0) {
      row.kids?.remove();
      row.kids = null;
      // THIS is what keeps a STEP non-collapsible: a childless row has no
      // disclosure at all, so there is nothing to fold. Nothing here may change it.
      setAttr(row.chevron, "hidden", "");
      setAttr(row.root, "aria-expanded", null);
      return;
    }
    setAttr(row.chevron, "hidden", row.collapsible ? null : "");
    row.kids ??= el("div", { className: "ev-kids", role: "group" });
    // Only when detached: `.ev-row-main` is the row's one other child, so the append seats it
    // second; re-appending an attached box restarts every animation in it and drops focus.
    if (row.kids.parentNode !== row.root) {
      row.root.appendChild(row.kids);
    }
    seatRows(row.kids, row.collapsible && folded.has(node.path) ? [] : node.children, depth + 1);
    applyCollapse(row, node.path);
  }

  function paintWindow(): void {
    if (box === null) {
      return;
    }
    const band = bandOf(root, box);
    range = rangeOf(flat.offsets, band.top, band.bottom);
    seatRows(root, nodes, 0);
  }

  /** Read the row height, the gap and a box's border off the mounted rows; true when they moved
   *  the estimate. */
  function measure(): boolean {
    const px = pitchOf(root.querySelector(".ev-row-main"), rowPx);
    const gap = Number.parseFloat(getComputedStyle(root).rowGap) || 0;
    const group = root.querySelector(".ev-row.ev-group");
    const border =
      group === null
        ? boxPx
        : (Number.parseFloat(getComputedStyle(group).borderTopWidth) || 0) +
          (Number.parseFloat(getComputedStyle(group).borderBottomWidth) || 0);
    measured = root.querySelector(".ev-row-main") !== null;
    if ([px - rowPx, gap - topGap, border - boxPx].every((d) => Math.abs(d) < 0.5)) {
      return false;
    }
    rowPx = px;
    topGap = gap;
    boxPx = border;
    flat = flatOf();
    return true;
  }

  /** A scroll or a resize: re-measure after a resize or before the first measure, then redraw
   *  only when the window moved. */
  function onMove(): void {
    if (box === null) {
      return;
    }
    const was = range;
    const band = bandOf(root, box);
    const next = rangeOf(flat.offsets, band.top, band.bottom);
    if (measure() || !measured || next.from !== was.from || next.to !== was.to) {
      paintWindow();
    }
  }

  return {
    root,
    render(next, pick) {
      nodes = next;
      selected = pick;
      if (box === null) {
        return;
      }
      flat = flatOf();
      paintWindow();
      if (!measured && measure()) {
        paintWindow();
      }
    },
    attach(scroller) {
      if (scroller === box) {
        return;
      }
      release?.();
      release = undefined;
      box = scroller;
      if (scroller === null) {
        root.replaceChildren();
        return;
      }
      measured = false;
      flat = flatOf();
      paintWindow();
      if (measure()) {
        paintWindow();
      }
      release = watchScroller(scroller, onMove);
    },
    tick() {
      for (const rowRoot of root.querySelectorAll<HTMLElement>(".ev-row")) {
        const row = rowParts.get(rowRoot);
        if (row === undefined || row.end !== undefined || row.start === undefined) {
          continue;
        }
        const ms = elapsed(row.start, undefined);
        if (ms > 0) {
          setText(row.dur, formatElapsed(ms));
        }
      }
    },
    reveal(path, scroller) {
      const top = nodes.find((n) => n.path === path || nodeAt(n.children, path) !== undefined);
      if (top !== undefined && folded.delete(top.path)) {
        flat = flatOf();
      }
      const at = flat.at.get(path);
      if (at === undefined || box !== scroller) {
        return;
      }
      // The row may be outside the window: scroll to its estimated place so the window seats it,
      // then centre it on its measured line. The first pass seats an unfolded box's spacers, or
      // the scroll range is still the folded one's and clamps the scroll.
      paintWindow();
      const viewTop =
        root.getBoundingClientRect().top -
        scroller.getBoundingClientRect().top -
        scroller.clientTop +
        scroller.scrollTop;
      scroller.scrollTo({
        top: viewTop + (flat.offsets[at] ?? 0) - (scroller.clientHeight - rowPx) / 2,
        behavior: "instant",
      });
      paintWindow();
      const target = [...root.querySelectorAll<HTMLElement>(".ev-row")].find(
        (r) => r.dataset["path"] === path,
      );
      if (target === undefined) {
        return;
      }
      // The row's own line, not the row: a container's row box holds its children too.
      const line = (target.firstElementChild ?? target).getBoundingClientRect();
      const rect = scroller.getBoundingClientRect();
      const offset = line.top - rect.top - scroller.clientTop;
      scroller.scrollTo({
        top: scroller.scrollTop + offset - (scroller.clientHeight - line.height) / 2,
        behavior: "instant",
      });
      paintWindow();
    },
  };
}

/** Find a node by path. Kept here rather than in the model because it is a
 *  VIEW concern: a selection that no longer resolves falls back to the page's
 *  `autoSelect` pick. */
export function nodeAt(nodes: readonly ExecNode[], path: string): ExecNode | undefined {
  for (const n of nodes) {
    if (n.path === path) {
      return n;
    }
    const hit = nodeAt(n.children, path);
    if (hit !== undefined) {
      return hit;
    }
  }
  return undefined;
}

/** The work node of the current pass that most wants the reader (`attentionRank`), else its first:
 *  the page's default selection and the first choice of `focusStep`. */
export function attentionStep(nodes: readonly ExecNode[]): ExecNode | undefined {
  const ls = currentWork(nodes);
  let best = ls[0];
  for (const n of ls) {
    if (best !== undefined && attentionRank(n.state) < attentionRank(best.state)) {
      best = n;
    }
  }
  return best;
}

/** The step the Steps tree lands on when it opens: the one waiting on the reader (an ask, a
 *  failure, a paused run's parked step), else the shown step while in flight, else the newest in
 *  flight, else the shown step, else the last to end. "" when the run has no step. */
export function focusStep(run: ExecRun, selected: string): string {
  const wants = attentionStep(run.nodes);
  const work = workNodes(run.nodes);
  const waitingOnReader =
    wants !== undefined && attentionRank(wants.state) <= attentionRank("fail")
      ? wants
      : run.state === "waiting"
        ? currentWork(run.nodes).find((n) => n.state === "waiting")
        : undefined;
  const picked = nodeAt(run.nodes, selected);
  const shown = picked !== undefined && isWork(picked) ? picked : undefined;
  const flying = work.filter((n) => inFlight(n.state));
  let lastEnded: ExecNode | undefined;
  for (const n of work) {
    const end = Date.parse(n.end ?? "");
    if (!Number.isNaN(end) && (lastEnded === undefined || end >= Date.parse(lastEnded.end ?? ""))) {
      lastEnded = n;
    }
  }
  const pick =
    waitingOnReader ??
    (shown !== undefined && inFlight(shown.state) ? shown : undefined) ??
    newestStarted(flying) ??
    flying[0] ??
    shown ??
    lastEnded;
  return pick?.path ?? "";
}

/** Whether a state deserves a reader's attention, which is what the page uses to
 *  pick a default selection: the node that wants something, else the one working,
 *  else the first. */
function attentionRank(state: ExecState): number {
  switch (state) {
    case "input":
      return 0;
    case "fail":
      return 1;
    case "running":
    case "unknown":
      return 2;
    case "waiting":
      return 3;
    case "warn":
      return 4;
    case "pending":
    case "ok":
    case "skipped":
      return 5;
  }
}
