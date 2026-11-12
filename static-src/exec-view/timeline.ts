// The exec view's timeline: where the time went and what ran concurrently (a column shows only
// order). A lane per WORK NODE (steps and watches), since a container's span is its children's, and
// every one of them from the plan, so a step not started yet is an empty lane; a loop pass heads
// its lanes with a row of its own. A readout: no axes, ticks or zoom; selectable with the tree's
// `onSelect` and selected path.

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { reconcile } from "../reconcile.js";
import {
  elapsed,
  flatten,
  isWork,
  passText,
  window as execWindow,
  windowAt,
  windowBounds,
  type ExecNode,
  type WindowBounds,
} from "./model.js";
import { STATE_WORD } from "./status.js";
import { paintLabel } from "./label.js";
import { setAttr, setText } from "../dom.js";
import {
  bandOf,
  gapEl,
  offsetsOf,
  pitchOf,
  rangeOf,
  sizeGap,
  watchScroller,
  type ItemRange,
} from "./row-window.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running: a live run's bar grows and the
   *  window stretches with it. */
  tick(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Draw the lanes `scroller` shows, plus an overscan, and follow its scroll and size; null
   *  unmounts every lane. Until then `render` and `tick` only record. */
  attach(scroller: HTMLElement | null): void;
}

/** A lane's and a pass header's heights before one is measured, at the default type scale. */
const LANE_PX = 21;
const PASS_PX = 30;

/** A loop pass container's number; undefined for anything else, a step a repeat holds directly
 *  included, which is a lane. */
function passOf(n: ExecNode): number | undefined {
  return isWork(n) ? undefined : n.pass;
}

/** Each lane's innermost pass by path, so lanes of the same step in different passes are told
 *  apart by name; a lane in no loop is absent. */
function passesOf(nodes: readonly ExecNode[]): Map<string, number> {
  const out = new Map<string, number>();
  const walk = (n: ExecNode, around: number | undefined): void => {
    const here = n.pass ?? around;
    if (isWork(n) && here !== undefined) {
      out.set(n.path, here);
    }
    for (const k of n.children) {
      walk(k, here);
    }
  };
  for (const n of nodes) {
    walk(n, undefined);
  }
  return out;
}

export function buildExecTimeline(onSelect: (path: string) => void): ExecTimelineView {
  const lanes = el("div", { className: "ev-tl-lanes" });
  const root = el("div", { className: "ev-tl" }, lanes);

  /** One lane per node path, reused across passes. */
  interface Lane {
    readonly root: HTMLElement;
    readonly name: HTMLElement;
    readonly bar: HTMLElement;
    readonly dur: HTMLElement;
    /** The bar's last written offset and width: the style serializes them, so it cannot be read
     *  back to compare. */
    geom: string;
  }
  /** Each mounted lane's parts, by its root; a removed lane is collected with it. */
  const laneParts = new WeakMap<HTMLElement, Lane>();

  function buildLane(path: string): Lane {
    const bar = el("span", { className: "ev-tl-bar" });
    const name = el("span", { className: "ev-tl-name" });
    const dur = el("span", { className: "ev-tl-dur" });
    const root = el(
      "div",
      {
        className: "ev-tl-lane",
        "data-path": path,
        role: "button",
        tabindex: "0",
      },
      name,
      el("span", { className: "ev-tl-track" }, bar),
      dur,
    );
    root.addEventListener("click", () => {
      onSelect(path);
    });
    root.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onSelect(path);
      }
    });
    return { root, name, bar, dur, geom: "" };
  }

  /** A loop pass's header: its number and its span, read off its steps because a resume restamps
   *  the pass container itself. */
  function buildPassHead(): HTMLElement {
    return el(
      "div",
      { className: "ev-tl-pass" },
      el("span", { className: "ev-tl-pass-name" }),
      el("span", { className: "ev-tl-dur" }),
    );
  }

  function paintPassHead(head: HTMLElement, n: ExecNode, pass: number): void {
    const [name, dur] = [head.firstElementChild, head.lastElementChild];
    setText(name, passText(pass));
    const ms = execWindow([n], false)?.span ?? 0;
    setText(dur, ms > 0 ? formatElapsed(ms) : "");
  }

  /** One seated item: a lane or pass header, or the spacer for a run the window skips. */
  type Slot =
    | { readonly node: ExecNode; readonly pass: number | undefined }
    | { readonly gap: "lead" | "tail"; readonly px: number };

  let shown: { nodes: readonly ExecNode[]; selected: string; live: boolean } = {
    nodes: [],
    selected: "",
    live: false,
  };
  let box: HTMLElement | null = null;
  let release: (() => void) | undefined;
  let lanePx = LANE_PX;
  let passPx = PASS_PX;
  let measured = false;
  let range: ItemRange = { from: 0, to: 0 };

  /** What a model gives the timeline before any clock reads it, kept per model so a tick or a
   *  scroll walks only the window's lanes. */
  interface Layout {
    readonly nodes: readonly ExecNode[];
    readonly live: boolean;
    readonly lanePx: number;
    readonly passPx: number;
    readonly items: { node: ExecNode; pass: number | undefined }[];
    readonly offsets: Float64Array;
    readonly bounds: WindowBounds | undefined;
    readonly lanePass: ReadonlyMap<string, number>;
  }
  let layout: Layout | undefined;

  /** Every lane and pass header the run has, in order, with the tops a window places them at. */
  function layoutOf(nodes: readonly ExecNode[], live: boolean): Layout {
    if (
      layout?.nodes === nodes &&
      layout.live === live &&
      layout.lanePx === lanePx &&
      layout.passPx === passPx
    ) {
      return layout;
    }
    const items = flatten(nodes)
      .map((node) => ({ node, pass: passOf(node) }))
      .filter((it) => isWork(it.node) || it.pass !== undefined);
    layout = {
      nodes,
      live,
      lanePx,
      passPx,
      items,
      offsets: offsetsOf(items.map((it) => (it.pass === undefined ? lanePx : passPx))),
      bounds: windowBounds(nodes, live),
      lanePass: passesOf(nodes),
    };
    return layout;
  }

  /** RECONCILED by path, not rebuilt: each lane is a focusable `role="button"`, and replacing it on
   *  the 1s tick drops focus and `:hover`. The geometry is recomputed every pass and written onto
   *  the REUSED element. */
  function paint(): void {
    if (box === null) {
      return;
    }
    const { nodes, selected, live } = shown;
    const { items, offsets, bounds, lanePass } = layoutOf(nodes, live);
    const band = bandOf(lanes, box);
    range = rangeOf(offsets, band.top, band.bottom);
    const win = bounds === undefined ? undefined : windowAt(bounds, Date.now());

    const paintLane = (lane: Lane, n: ExecNode): void => {
      const from = Date.parse(n.start ?? "");
      setAttr(lane.bar, "hidden", win === undefined || Number.isNaN(from) ? "" : null);
      if (win === undefined || Number.isNaN(from)) {
        paintText(lane, n);
        return;
      }
      const to = n.end === undefined ? win.to : Date.parse(n.end);
      const left = ((from - win.from) / win.span) * 100;
      // Floor of 0.75%: a sub-second step in an hour-long run rounds to zero
      // width otherwise, reporting the step as absent rather than fast.
      const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
      // Clamped so clock skew between the server's stamps and this browser
      // cannot widen the row past the track.
      const start = `${left.toFixed(3)}%`;
      const size = `${Math.min(100 - left, width).toFixed(3)}%`;
      if (lane.geom !== `${start} ${size}`) {
        lane.geom = `${start} ${size}`;
        lane.bar.style.insetInlineStart = start;
        lane.bar.style.inlineSize = size;
      }
      paintText(lane, n);
    };

    const paintText = (lane: Lane, n: ExecNode): void => {
      const ms = elapsed(n.start, n.end);
      const durText = ms > 0 ? formatElapsed(ms) : "";
      lane.root.classList.toggle("ev-selected", n.path === selected);
      setAttr(lane.root, "data-state", n.state);
      const pass = lanePass.get(n.path);
      setAttr(
        lane.root,
        "aria-label",
        [n.label, pass === undefined ? "" : passText(pass), STATE_WORD[n.state], durText]
          .filter((s) => s !== "")
          .join(", "),
      );
      paintLabel(lane.name, n);
      setText(lane.dur, durText);
    };

    const slots: Slot[] = items.slice(range.from, range.to);
    if (range.from > 0) {
      slots.unshift({ gap: "lead", px: offsets[range.from] ?? 0 });
    }
    if (range.to < items.length) {
      slots.push({ gap: "tail", px: (offsets[items.length] ?? 0) - (offsets[range.to] ?? 0) });
    }
    reconcile(lanes, slots, {
      key: (s) => ("gap" in s ? `\u0001${s.gap}` : s.node.path),
      mount: (s) => {
        if ("gap" in s) {
          const gap = gapEl();
          sizeGap(gap, s.px);
          return gap;
        }
        if (s.pass !== undefined) {
          const head = buildPassHead();
          paintPassHead(head, s.node, s.pass);
          return head;
        }
        const lane = buildLane(s.node.path);
        laneParts.set(lane.root, lane);
        paintLane(lane, s.node);
        return lane.root;
      },
      update: (row, s) => {
        if ("gap" in s) {
          sizeGap(row, s.px);
          return;
        }
        const lane = laneParts.get(row);
        if (lane !== undefined) {
          paintLane(lane, s.node);
        } else if (s.pass !== undefined) {
          paintPassHead(row, s.node, s.pass);
        }
      },
    });
  }

  /** Read a lane's and a header's pitch off the mounted ones; true when they moved the estimate. */
  function measure(): boolean {
    const gap = Number.parseFloat(getComputedStyle(lanes).rowGap) || 0;
    const lane = pitchOf(lanes.querySelector(".ev-tl-lane"), lanePx - gap) + gap;
    const pass = pitchOf(lanes.querySelector(".ev-tl-pass:not(:first-child)"), passPx - gap) + gap;
    measured = lanes.querySelector(".ev-tl-lane") !== null;
    if (Math.abs(lane - lanePx) < 0.5 && Math.abs(pass - passPx) < 0.5) {
      return false;
    }
    lanePx = lane;
    passPx = pass;
    return true;
  }

  /** A scroll or a resize: redraw when the window moved or the measure did. */
  function onMove(): void {
    if (box === null) {
      return;
    }
    const was = range;
    const band = bandOf(lanes, box);
    const next = rangeOf(layoutOf(shown.nodes, shown.live).offsets, band.top, band.bottom);
    if (measure() || !measured || next.from !== was.from || next.to !== was.to) {
      paint();
    }
  }

  function draw(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    shown = { nodes, selected, live };
    paint();
    if (box !== null && !measured && measure()) {
      paint();
    }
  }

  return {
    root,
    render: draw,
    tick: draw,
    attach(scroller) {
      if (scroller === box) {
        return;
      }
      release?.();
      release = undefined;
      box = scroller;
      if (scroller === null) {
        lanes.replaceChildren();
        return;
      }
      measured = false;
      paint();
      if (measure()) {
        paint();
      }
      release = watchScroller(scroller, onMove);
    },
  };
}
