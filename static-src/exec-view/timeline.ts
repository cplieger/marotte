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
  type ExecNode,
} from "./model.js";
import { STATE_WORD } from "./status.js";
import { paintLabel } from "./label.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running: a live run's bar grows and the
   *  window stretches with it. */
  tick(nodes: readonly ExecNode[], selected: string, live: boolean): void;
}

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
    const text = passText(pass);
    if (name !== null && name.textContent !== text) {
      name.textContent = text;
    }
    const ms = execWindow([n], false)?.span ?? 0;
    const durText = ms > 0 ? formatElapsed(ms) : "";
    if (dur !== null && dur.textContent !== durText) {
      dur.textContent = durText;
    }
  }

  /** RECONCILED by path, not rebuilt: each lane is a focusable `role="button"`, and replacing it on
   *  the 1s tick drops focus and `:hover`. The geometry is recomputed every pass and written onto
   *  the REUSED element. */
  function paint(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    const ls = flatten(nodes).filter((n) => isWork(n) || passOf(n) !== undefined);
    const win = execWindow(nodes, live);
    const lanePass = passesOf(nodes);

    const paintLane = (lane: Lane, n: ExecNode): void => {
      const from = Date.parse(n.start ?? "");
      lane.bar.hidden = win === undefined || Number.isNaN(from);
      if (win === undefined || Number.isNaN(from)) {
        paintText(lane, n);
        return;
      }
      const to = n.end === undefined ? win.to : Date.parse(n.end);
      const left = ((from - win.from) / win.span) * 100;
      // Floor of 0.75%: a sub-second step in an hour-long run rounds to zero
      // width otherwise, reporting the step as absent rather than fast.
      const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
      lane.bar.style.insetInlineStart = `${left.toFixed(3)}%`;
      // Clamped so clock skew between the server's stamps and this browser
      // cannot widen the row past the track.
      lane.bar.style.inlineSize = `${Math.min(100 - left, width).toFixed(3)}%`;
      paintText(lane, n);
    };

    const paintText = (lane: Lane, n: ExecNode): void => {
      const ms = elapsed(n.start, n.end);
      const durText = ms > 0 ? formatElapsed(ms) : "";
      lane.root.className = n.path === selected ? "ev-tl-lane ev-selected" : "ev-tl-lane";
      lane.root.dataset["state"] = n.state;
      const pass = lanePass.get(n.path);
      lane.root.setAttribute(
        "aria-label",
        [n.label, pass === undefined ? "" : passText(pass), STATE_WORD[n.state], durText]
          .filter((s) => s !== "")
          .join(", "),
      );
      paintLabel(lane.name, n);
      lane.dur.textContent = durText;
    };

    reconcile(lanes, ls, {
      key: (n) => n.path,
      mount: (n) => {
        const pass = passOf(n);
        if (pass !== undefined) {
          const head = buildPassHead();
          paintPassHead(head, n, pass);
          return head;
        }
        const lane = buildLane(n.path);
        laneParts.set(lane.root, lane);
        paintLane(lane, n);
        return lane.root;
      },
      update: (row, n) => {
        const lane = laneParts.get(row);
        const pass = passOf(n);
        if (lane !== undefined) {
          paintLane(lane, n);
        } else if (pass !== undefined) {
          paintPassHead(row, n, pass);
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
