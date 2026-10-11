// The exec view's timeline: where the time went and what ran concurrently (a column shows only
// order). WORK NODES ONLY (steps and watches), since a container's span is its children's. A
// readout: no axes, ticks or zoom; selectable with the tree's `onSelect` and selected path.

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { reconcile } from "../reconcile.js";
import { elapsed, workNodes, window as execWindow, type ExecNode } from "./model.js";
import { STATE_WORD } from "./status.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running: a live run's bar grows and the
   *  window stretches with it. */
  tick(nodes: readonly ExecNode[], selected: string, live: boolean): void;
}

export function buildExecTimeline(onSelect: (path: string) => void): ExecTimelineView {
  const scale = el("span", { className: "ev-tl-scale" });
  const lanes = el("div", { className: "ev-tl-lanes" });
  const root = el(
    "div",
    { className: "ev-tl" },
    el(
      "div",
      { className: "ev-tl-head" },
      el("span", { className: "ev-tl-title" }, "Timeline"),
      scale,
    ),
    lanes,
  );

  /** One lane per node path, reused across passes. */
  interface Lane {
    readonly root: HTMLElement;
    readonly name: HTMLElement;
    readonly bar: HTMLElement;
    readonly dur: HTMLElement;
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
    return { root, name, bar, dur };
  }

  /** RECONCILED by path, not rebuilt: each lane is a focusable `role="button"`, and replacing it on
   *  the 1s tick drops focus and `:hover`. The geometry is recomputed every pass and written onto
   *  the REUSED element. */
  function paint(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    const ls = workNodes(nodes).filter((n) => n.start !== undefined);
    const win = execWindow(nodes, live);
    // TWO steps minimum: a single bar spanning its own window is always full
    // width and reports nothing the header's elapsed does not already say.
    if (win === undefined || ls.length < 2) {
      root.hidden = true;
      lanes.replaceChildren();
      return;
    }
    root.hidden = false;
    scale.textContent = formatElapsed(win.span);

    const paintLane = (lane: Lane, n: ExecNode): void => {
      const from = Date.parse(n.start ?? "");
      const to = n.end === undefined ? win.to : Date.parse(n.end);
      const left = ((from - win.from) / win.span) * 100;
      // Floor of 0.75%: a sub-second step in an hour-long run rounds to zero
      // width otherwise, reporting the step as absent rather than fast.
      const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
      lane.bar.style.insetInlineStart = `${left.toFixed(3)}%`;
      // Clamped so clock skew between the server's stamps and this browser
      // cannot widen the row past the track.
      lane.bar.style.inlineSize = `${Math.min(100 - left, width).toFixed(3)}%`;

      const ms = elapsed(n.start, n.end);
      const durText = ms > 0 ? formatElapsed(ms) : "";
      lane.root.className = n.path === selected ? "ev-tl-lane ev-selected" : "ev-tl-lane";
      lane.root.dataset["state"] = n.state;
      lane.root.setAttribute(
        "aria-label",
        `${n.label}, ${STATE_WORD[n.state]}${durText === "" ? "" : `, ${durText}`}`,
      );
      lane.name.textContent = n.label;
      lane.dur.textContent = durText;
    };

    reconcile(lanes, ls, {
      key: (n) => n.path,
      mount: (n) => {
        const lane = buildLane(n.path);
        laneParts.set(lane.root, lane);
        paintLane(lane, n);
        return lane.root;
      },
      update: (laneRoot, n) => {
        const lane = laneParts.get(laneRoot);
        if (lane !== undefined) {
          paintLane(lane, n);
        }
      },
    });
  }

  return {
    root,
    render: paint,
    tick: paint,
  };
}
