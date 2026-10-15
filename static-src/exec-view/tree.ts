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
import { STATE_WORD, inFlight, paintStateMark, type ExecState } from "./status.js";

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
  collapsed: boolean;
  /** Top-level only — see the ONE FOLD PER BOX note at the top of this file. */
  collapsible: boolean;
}

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
      collapsed: false,
      collapsible: depth === 0,
    };

    head.addEventListener("click", (e) => {
      // The twist is a zone of the row, not a nested control; the handler tells
      // them apart by target, keeping the row one activation target.
      if (row.collapsible && chevron.contains(e.target as Node) && row.kids !== null) {
        row.collapsed = !row.collapsed;
        applyCollapse(row);
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
  function applyCollapse(row: Row): void {
    const collapsed = row.collapsible && row.collapsed;
    row.root.classList.toggle("ev-collapsed", collapsed);
    if (row.collapsible) {
      row.root.setAttribute("aria-expanded", String(!collapsed));
    } else {
      row.root.removeAttribute("aria-expanded");
    }
    if (row.kids !== null) {
      row.kids.hidden = collapsed;
    }
  }

  /** One sibling list, keyed by path rather than kind: `paint` repaints a kind change in place,
   *  so the row element, with its fold and focus, survives it. */
  function seatRows(
    into: HTMLElement,
    nodes: readonly ExecNode[],
    depth: number,
    selected: string,
  ): void {
    reconcile(into, nodes, {
      key: (n) => n.path,
      mount: (n) => {
        const row = buildRow(n, depth);
        rowParts.set(row.root, row);
        paint(row, n, depth, selected);
        return row.root;
      },
      update: (rowRoot, n) => {
        const row = rowParts.get(rowRoot);
        if (row !== undefined) {
          paint(row, n, depth, selected);
        }
      },
    });
  }

  function paint(row: Row, node: ExecNode, depth: number, selected: string): void {
    paintLabel(row.label, node);
    row.sub.textContent = node.subtitle ?? "";
    row.sub.hidden = (node.subtitle ?? "") === "";
    row.root.dataset["state"] = node.state;
    // Rows are keyed by PATH, and a replanned run can put a different kind at one path.
    if (row.root.dataset["kind"] !== node.kind) {
      row.kindSlot.replaceChildren(kindGlyph(node.kind));
    }
    row.root.dataset["kind"] = node.kind;
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
    row.dur.textContent = ms > 0 ? formatElapsed(ms) : "";
    row.root.classList.toggle("ev-selected", node.path === selected);
    row.root.setAttribute("aria-selected", String(node.path === selected));
    // Read back off the rendered text, so it cannot disagree with the screen.
    row.root.setAttribute(
      "aria-label",
      `${node.label}, ${STATE_WORD[node.state]}${node.subtitle === undefined ? "" : `, ${node.subtitle}`}`,
    );

    if (node.children.length === 0) {
      row.kids?.remove();
      row.kids = null;
      // THIS is what keeps a STEP non-collapsible: a childless row has no
      // disclosure at all, so there is nothing to fold. Nothing here may change it.
      row.chevron.hidden = true;
      row.root.removeAttribute("aria-expanded");
      return;
    }
    row.chevron.hidden = !row.collapsible;
    row.kids ??= el("div", { className: "ev-kids", role: "group" });
    // Only when detached: `.ev-row-main` is the row's one other child, so the append seats it
    // second; re-appending an attached box restarts every animation in it and drops focus.
    if (row.kids.parentNode !== row.root) {
      row.root.appendChild(row.kids);
    }
    seatRows(row.kids, node.children, depth + 1, selected);
    applyCollapse(row);
  }

  return {
    root,
    render(nodes, selected) {
      seatRows(root, nodes, 0, selected);
    },
    tick() {
      for (const rowRoot of root.querySelectorAll<HTMLElement>(".ev-row")) {
        const row = rowParts.get(rowRoot);
        if (row === undefined || row.end !== undefined || row.start === undefined) {
          continue;
        }
        const ms = elapsed(row.start, undefined);
        if (ms > 0) {
          row.dur.textContent = formatElapsed(ms);
        }
      }
    },
    reveal(path, scroller) {
      const target = [...root.querySelectorAll<HTMLElement>(".ev-row")].find(
        (r) => r.dataset["path"] === path,
      );
      if (target === undefined) {
        return;
      }
      let up = target.parentElement?.closest<HTMLElement>(".ev-row") ?? null;
      while (up !== null) {
        const row = rowParts.get(up);
        if (row?.collapsed === true) {
          row.collapsed = false;
          applyCollapse(row);
        }
        up = up.parentElement?.closest<HTMLElement>(".ev-row") ?? null;
      }
      // The row's own line, not the row: a container's row box holds its children too.
      const line = (target.firstElementChild ?? target).getBoundingClientRect();
      const box = scroller.getBoundingClientRect();
      const offset = line.top - box.top - scroller.clientTop;
      scroller.scrollTo({
        top: scroller.scrollTop + offset - (scroller.clientHeight - line.height) / 2,
        behavior: "instant",
      });
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
