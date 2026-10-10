// The exec view page: one full tab for one delegated execution — header, alert,
// timeline, tree, detail, results — owning the selection between them and one clock
// for all of them. It must name no source: a consumer hands it an `ExecRun`.

import { el } from "@cplieger/reactive";
import { attachClamp, releaseClampsIn } from "../clamp-text.js";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN } from "../icons.js";
import { buildAssistantBubble } from "../fundamentals/text-bubble.js";
import { formatElapsed } from "../strings.js";
import {
  counters,
  currentWork,
  elapsed,
  isWork,
  window as execWindow,
  workNodes,
  type ExecNode,
  type ExecRun,
} from "./model.js";
import { STATE_WORD, settled } from "./status.js";
import { buildExecTree, nodeAt, attentionRank, type ExecTreeView } from "./tree.js";
import { buildExecTimeline, type ExecTimelineView } from "./timeline.js";
import {
  buildExecDetail,
  type ExecDetailView,
  type EmptyAction,
  type EmptyNote,
} from "./detail.js";

interface ExecPageOpts {
  /** What a node with a transcript host but no content yet should say; the
   *  consumer's call (a workflow run has three reasons, a subagent tab its own). */
  emptyNote: EmptyNote;
  /** An affordance to render beside that note. Optional: a consumer with nowhere
   *  to send anyone passes none and the slot stays empty. */
  emptyAction?: EmptyAction;
  /** The execution's controls. Returns null for a state with no verbs, so the row
   *  disappears rather than rendering empty, and may return the SAME element it
   *  returned last time to say nothing changed — the page then leaves it in place
   *  rather than re-inserting it. */
  controls?: (run: ExecRun) => HTMLElement | null;
  /** The header's identity glyph. Defaults to the workflow one; a subagent page
   *  passes the agent hexagon. */
  icon?: string;
  /** Fired when the node the DETAIL PANE is showing changes, or when that node's
   *  state moves — the seam a consumer arms an on-demand fetch on (`emptyNote`/`emptyAction`
   *  run on every render and must stay pure). The STATE half is required because `select()`
   *  PINS the selection: a path-only guard never re-fires for a step clicked while running. */
  onShowNode?: (node: ExecNode | undefined) => void;
  /** Fired after every repaint with the selected node as that render built it, so a consumer keeps no
   *  copy whose fields (`verb` among them) can go stale between `onShowNode` calls. */
  onSelect?: (node: ExecNode | undefined) => void;
  /** Where the timeline, the step tree and (once the execution ends) the results go, as one-row
   *  accordion boxes, leaving the page to the selected node. Absent keeps them on the page. */
  dock?: HTMLElement;
  /** How the page picks a node without a click. `attention` (the default) opens the one that wants
   *  something. `newest` keeps the most recently started step selected, switching when a newer one
   *  starts; a click holds until then, and a container click picks its newest step, so a step is
   *  always selected. */
  follow?: "attention" | "newest";
}

/** The instructions clamp: the line count the STYLESHEET clamps to, plus the
 *  character threshold used only while the element is detached and cannot be
 *  measured. `clamp-line-count.test.ts` holds the first to the stylesheet's own. */
const CLAMP = { lines: 3, fallbackChars: 220 } as const;

/** The results clamp, same shape: `.ev-r-text`'s stylesheet line count plus the
 *  pre-layout character guess, plus the line SNAP. This is the app's only clamp
 *  over a markdown bubble's block children, so it is the only one whose stylesheet
 *  cap cannot land on a line boundary by itself — `31-exec-view.css`
 *  `.ev-r-text[data-clamped]` carries the measurement. */
const RESULT_CLAMP = { lines: 12, fallbackChars: 900, snapToLine: true } as const;

export interface ExecPageView {
  readonly root: HTMLElement;
  /** Draw an execution. Idempotent and safe on every invalidation. */
  render(run: ExecRun | undefined): void;
  /** The host a node's live transcript renders into. */
  bodyFor(path: string): HTMLElement;
  /** Release the clock. Called when the page stops being shown. */
  dispose(): void;
}

export function buildExecPage(opts: ExecPageOpts): ExecPageView {
  const name = el("span", { className: "ev-name" });
  const stateEl = el("span", { className: "ev-h-state" });
  const progress = el("span", { className: "ev-h-progress" });
  const clock = el("span", { className: "ev-h-clock" });
  const controlsHost = el("div", { className: "ev-h-actions" });
  const inputs = el("dl", { className: "ev-inputs" });
  const alert = el("div", {
    className: "ev-alert",
    role: "status",
    "aria-live": "polite",
    hidden: true,
  });
  const notice = el("p", {
    className: "ev-alert ev-notice",
    role: "status",
    "aria-live": "polite",
    hidden: true,
  });
  const head = el(
    "header",
    { className: "ev-head" },
    el(
      "div",
      { className: "ev-h-main" },
      el(
        "span",
        { className: "ev-h-icon", "aria-hidden": "true" },
        iconEl(opts.icon ?? ICON_TAB_RUN),
      ),
      name,
      el("span", { className: "ev-h-meta" }, stateEl, progress, clock),
      controlsHost,
    ),
    inputs,
  );

  let selected = "";
  /** Whether the reader has chosen a node. Until they have, the page follows the
   *  work (a running or failed node opens without a click); after a click it
   *  stops moving. */
  let userPicked = false;
  /** The path the detail pane is currently showing, so `onShowNode` fires on a
   *  change of ATTENTION rather than on every repaint. "" means none. */
  let shownPath = "";
  /** That node's state at the last notification, the second half of the same guard:
   *  a node settling under the reader's cursor is a change of attention too, and it
   *  is the only one a pinned selection can produce. "" means none. */
  let shownState = "";
  /** The last `ExecRun.focus` honoured. Recorded rather than consumed once,
   *  because one page instance serves every tab of its kind: a subagent tab
   *  switching delegates arrives as a render with a different `focus`, which
   *  must re-focus without fighting a later click. */
  let focused = "";

  const tree: ExecTreeView = buildExecTree(select);
  const timeline: ExecTimelineView = buildExecTimeline(select);
  const detail: ExecDetailView = buildExecDetail(opts.emptyNote, opts.emptyAction);

  const treePane = el("div", { className: "ev-pane ev-pane-tree" }, tree.root);
  const panes = el(
    "div",
    { className: "ev-panes" },
    treePane,
    el("div", { className: "ev-pane ev-pane-detail" }, detail.root),
  );

  const resultsBody = el("div", { className: "ev-r-body" });
  const resultsHead = el(
    "div",
    { className: "ev-r-head" },
    el("span", { className: "ev-r-title" }, "Step results"),
  );
  const results = el("div", { className: "ev-results", hidden: true }, resultsHead, resultsBody);

  const dock = opts.dock === undefined ? undefined : buildDock(opts.dock);
  // Results sit BELOW the panes: the region toggles as the reader clicks between steps, and above
  // them its `hidden` flip moved the tree and detail under the pointer. Docked, the page keeps the
  // detail alone and the dock boxes hold the rest.
  const root =
    dock === undefined
      ? el("div", { className: "ev-page" }, head, alert, notice, timeline.root, panes, results)
      : el("div", { className: "ev-page ev-page-docked" }, head, alert, notice, panes);
  if (dock !== undefined) {
    dock.steps.body.append(treePane);
    dock.timeline.body.append(timeline.root, dock.timelineEmpty);
    dock.results.body.append(results, dock.resultsEmpty);
  }

  let current: ExecRun | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  const follow = opts.follow ?? "attention";
  let seenGen = 0;

  function advance(node: ExecNode | undefined): boolean {
    if (node?.startGen === undefined || node.startGen <= seenGen) {
      return false;
    }
    seenGen = node.startGen;
    return true;
  }

  function select(path: string): void {
    userPicked = true;
    selected = follow === "newest" && current !== undefined ? stepFor(current, path) : path;
    repaint();
  }

  function stepFor(run: ExecRun, path: string): string {
    const node = nodeAt(run.nodes, path);
    if (node === undefined || isWork(node)) {
      return path;
    }
    return (newestStarted(node.children) ?? currentWork([node])[0])?.path ?? path;
  }

  /** The node the page opens on: the work node of the current pass (`currentWork`) that wants
   *  attention, else its first. Re-derived on every render while the reader has not chosen. */
  function autoSelect(run: ExecRun): string {
    const ls = currentWork(run.nodes);
    if (ls.length === 0) {
      return "";
    }
    let best = ls[0];
    if (best === undefined) {
      return "";
    }
    for (const n of ls) {
      if (attentionRank(n.state) < attentionRank(best.state)) {
        best = n;
      }
    }
    return best.path;
  }

  /** The newest-follow choice: a newer start moves the selection and ends a pick; otherwise a pick
   *  holds, and with none the newest started step (else the first) is shown. */
  function followNewest(run: ExecRun): string {
    const newest = newestStarted(run.nodes);
    if (newest !== undefined && advance(newest)) {
      userPicked = false;
      return newest.path;
    }
    if (userPicked && nodeAt(run.nodes, selected) !== undefined) {
      return stepFor(run, selected);
    }
    return newest?.path ?? currentWork(run.nodes)[0]?.path ?? "";
  }

  function repaint(): void {
    const run = current;
    if (run === undefined) {
      return;
    }
    // Degrades by content: a tree of one row is not navigation, and a timeline
    // of one bar says nothing the header's elapsed does not. Structural regions
    // appear only when there is structure (more than one root, or any node with
    // children). Docked, the tree is the run's step list and always shows.
    const structural =
      dock !== undefined || run.nodes.length > 1 || run.nodes.some((n) => n.children.length > 0);
    panes.classList.toggle("ev-panes-flat", !structural);
    // Docked, the tree is in a box: one column, and the detail keeps its name row, because the
    // Steps box is navigation and the row says which step it picked.
    panes.classList.toggle("ev-panes-docked", dock !== undefined);
    treePane.hidden = !structural;
    if (structural) {
      tree.render(run.nodes, selected);
    }
    timeline.render(run.nodes, selected, run.live);
    if (dock !== undefined) {
      dock.paint(run);
    }
    const node = nodeAt(run.nodes, selected);
    detail.render(node);
    // In `repaint` rather than in `render`, because the region is the SELECTED node's:
    // a selection change is a change of subject, and only this path runs on one.
    renderResults(node);
    if (dock !== undefined) {
      dock.results.box.hidden = run.live;
      dock.resultsEmpty.hidden = !results.hidden;
      dock.resultsEmpty.textContent = node === undefined ? "" : `No results from ${node.label}.`;
    }
    // AFTER the pane, so a synchronous consumer finds its host created. Guarded on PATH and STATE:
    // `repaint` runs per invalidation, and a path-only guard misses a node settling in place.
    const path = node?.path ?? "";
    const state = node?.state ?? "";
    if (path !== shownPath || state !== shownState) {
      shownPath = path;
      shownState = state;
      opts.onShowNode?.(node);
    }
    opts.onSelect?.(node);
  }

  /** The header's instructions list, rebuilt only when the SET changed. The signature
   *  guard is REQUIRED: `render` runs on every store invalidation, so an unguarded
   *  `replaceChildren` would discard the reader's show-more expansion. */
  function renderInputs(run: ExecRun): void {
    const entries = Object.entries(run.inputs ?? {});
    if (entries.length === 0) {
      inputs.hidden = true;
      delete inputs.dataset["sig"];
      releaseClampsIn(inputs);
      inputs.replaceChildren();
      return;
    }
    inputs.hidden = false;
    const sig = entries.map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    if (inputs.dataset["sig"] === sig) {
      return;
    }
    inputs.dataset["sig"] = sig;
    // Before the discard, or the rows leaving take their clamp observations with
    // them and the shared observer keeps watching detached elements.
    releaseClampsIn(inputs);
    inputs.replaceChildren(
      ...entries.flatMap(([k, v]) => {
        const text = el("div", { className: "ev-in-text" }, v);
        const more = el("button", {
          type: "button",
          className: "ev-in-more",
        }) as HTMLButtonElement;
        // `clamp-text.ts` MEASURES overflow, so a short instruction is never offered
        // an opener that opens nothing.
        attachClamp(text, more, CLAMP);
        return [
          el("dt", { className: "ev-in-k" }, k),
          el("dd", { className: "ev-in-v" }, text, more),
        ];
      }),
    );
  }

  /** The SELECTED step's own results, signature-guarded like `renderInputs`, and gated
   *  on `settled` because a step still in flight has produced nothing yet. */
  function renderResults(node: ExecNode | undefined): void {
    const merged = new Map<string, string>();
    if (node !== undefined && settled(node.state)) {
      if (node.output !== undefined) {
        merged.set("", node.output);
      }
      // Artifacts win a key collision, being the value a step CHOSE to publish.
      for (const [k, v] of Object.entries(node.artifacts ?? {})) {
        merged.set(k, v);
      }
    }
    if (merged.size === 0) {
      results.hidden = true;
      delete resultsBody.dataset["sig"];
      releaseClampsIn(resultsBody);
      resultsBody.replaceChildren();
      return;
    }
    results.hidden = false;
    const body = [...merged].map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    const sig = `${node?.path ?? ""}\u0000${body}`;
    if (resultsBody.dataset["sig"] === sig) {
      return;
    }
    resultsBody.dataset["sig"] = sig;
    releaseClampsIn(resultsBody);
    resultsBody.replaceChildren(...[...merged].map(([key, value]) => resultItem(key, value)));
  }

  /** One capture as its own box: a key row over the report, clamped. The opener is a
   *  SIBLING of the clipped box, never a child — a clamped box is `overflow: hidden`,
   *  so a button inside it is clipped away exactly when it becomes needed. */
  function resultItem(key: string, value: string): HTMLElement {
    const keyRow = el("span", { className: "ev-r-item-key" }, key === "" ? "Output" : key);
    // An empty value is a fact: a source writes a key only for a node that captured,
    // so empty distinguishes "finished silently" from "never ran". Left unclamped.
    if (value.trim() === "") {
      return el(
        "div",
        { className: "ev-r-item" },
        keyRow,
        el(
          "div",
          { className: "ev-r-item-body" },
          el(
            "div",
            { className: "ev-r-item-empty" },
            "This step finished without producing any text.",
          ),
        ),
      );
    }
    const text = el("div", { className: "ev-r-text" }, buildAssistantBubble(value, false).root);
    const more = el("button", { type: "button", className: "ev-r-more" }) as HTMLButtonElement;
    attachClamp(text, more, RESULT_CLAMP);
    return el(
      "div",
      { className: "ev-r-item" },
      keyRow,
      el("div", { className: "ev-r-item-body" }, text, more),
    );
  }

  function setClock(live: boolean): void {
    if (live && timer === undefined) {
      timer = setInterval(() => {
        const run = current;
        if (run === undefined) {
          return;
        }
        tree.tick();
        detail.tick();
        timeline.tick(run.nodes, selected, run.live);
        dock?.paint(run);
        const win = execWindow(run.nodes, run.live);
        clock.textContent = win === undefined ? "" : formatElapsed(win.span);
      }, 1000);
      return;
    }
    if (!live && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  }

  return {
    root,
    render(run) {
      current = run;
      if (run === undefined) {
        return;
      }
      root.dataset["state"] = run.state;
      name.textContent = run.label;
      // An unanswered ask outranks the status word: the run genuinely still
      // reads `running` while it blocks on a node's ask.
      stateEl.textContent = run.alert?.kind === "input" ? "needs input" : STATE_WORD[run.state];

      const c = counters(run.nodes);
      // Withheld for a one-node execution: "step 1 of 1" cannot progress, and the
      // state word beside it already says how the node ended.
      progress.textContent =
        c.total <= 1
          ? ""
          : c.current > 0
            ? `step ${String(c.current)} of ${String(c.total)}`
            : `${String(c.done)} of ${String(c.total)}`;
      const win = execWindow(run.nodes, run.live);
      clock.textContent = win === undefined ? "" : formatElapsed(win.span);

      renderInputs(run);

      alert.hidden = run.alert === undefined;
      if (run.alert !== undefined) {
        alert.dataset["kind"] = run.alert.kind;
        alert.textContent = run.alert.text;
      }
      notice.hidden = run.notice === undefined;
      if (run.notice !== undefined) {
        notice.dataset["kind"] = run.notice.failed ? "failed" : "stopped";
        notice.textContent = run.notice.text;
      }

      // Re-inserted only when it is a DIFFERENT row. `replaceChildren` with a node
      // that is already the host's only child still removes and re-adds it, which
      // blurs whatever is focused inside — and this runs once per progress frame.
      const row = opts.controls?.(run) ?? null;
      if (row === null) {
        controlsHost.replaceChildren();
      } else if (controlsHost.firstChild !== row || controlsHost.childNodes.length !== 1) {
        controlsHost.replaceChildren(row);
      }

      // A focus-free render clears the watermark, or a door could name a node only once per page;
      // safe, since such a render asserts nothing (`userPicked` keeps the reader's choice).
      if (run.focus === undefined) {
        focused = "";
      }
      // A door that NAMES a node is a choice: honoured like a click, and it stops
      // the auto-follow. Guarded on the value having changed, or re-asserting the
      // same focus on every invalidation would fight a reader who clicked
      // elsewhere. A focus naming an absent node falls through to auto-follow.
      if (
        run.focus !== undefined &&
        run.focus !== focused &&
        nodeAt(run.nodes, run.focus) !== undefined
      ) {
        focused = run.focus;
        selected = run.focus;
        userPicked = true;
        // A door's pick is the reader's, so the start already under way does not undo it.
        advance(newestStarted(run.nodes));
      }
      if (follow === "newest") {
        selected = followNewest(run);
      } else if (!userPicked || nodeAt(run.nodes, selected) === undefined) {
        selected = autoSelect(run);
      }
      repaint();
      setClock(run.live);
    },
    bodyFor(path) {
      return detail.bodyFor(path);
    },
    dispose() {
      setClock(false);
      dock?.dispose();
    },
  };
}

/** The work node with the largest `startGen`; one carrying none has not started. */
function newestStarted(nodes: readonly ExecNode[]): ExecNode | undefined {
  let best: ExecNode | undefined;
  for (const n of workNodes(nodes)) {
    if (n.startGen !== undefined && (best?.startGen === undefined || n.startGen > best.startGen)) {
      best = n;
    }
  }
  return best;
}

interface DockBox {
  readonly box: HTMLDetailsElement;
  readonly body: HTMLElement;
  readonly latest: HTMLElement;
  readonly dur: HTMLElement;
}

interface Dock {
  readonly timeline: DockBox;
  readonly steps: DockBox;
  readonly results: DockBox;
  readonly timelineEmpty: HTMLElement;
  readonly resultsEmpty: HTMLElement;
  paint(run: ExecRun): void;
  dispose(): void;
}

function buildDock(host: HTMLElement): Dock {
  const mk = (key: string, title: string): DockBox => {
    const latest = el("span", { className: "ev-box-latest" });
    const dur = el("span", { className: "ev-box-dur" });
    const summary = el(
      "summary",
      { className: "ev-box-head" },
      el("span", { className: "ev-box-twist", "aria-hidden": "true" }, chevronEl()),
      el("span", { className: "ev-box-title" }, title),
      latest,
      dur,
    );
    const body = el("div", { className: "ev-box-body" });
    const box = el(
      "details",
      { className: "ev-box", "data-box": key },
      summary,
      body,
    ) as HTMLDetailsElement;
    box.name = "ev-dock";
    return { box, body, latest, dur };
  };
  const timeline = mk("timeline", "Timeline");
  const steps = mk("steps", "Steps");
  const results = mk("results", "Results");
  results.box.hidden = true;
  const timelineEmpty = el(
    "p",
    { className: "ev-box-empty" },
    "The timeline appears once two steps have run.",
  );
  const resultsEmpty = el("p", { className: "ev-box-empty" });
  host.replaceChildren(timeline.box, steps.box, results.box);
  return {
    timeline,
    steps,
    results,
    timelineEmpty,
    resultsEmpty,
    paint(run) {
      const newest = newestStarted(run.nodes);
      const ms = newest === undefined ? 0 : elapsed(newest.start, newest.end);
      const label = newest?.label ?? "";
      const time = ms > 0 ? formatElapsed(ms) : "";
      for (const b of [timeline, steps, results]) {
        if (b.latest.textContent !== label) {
          b.latest.textContent = label;
        }
        if (b.dur.textContent !== time) {
          b.dur.textContent = time;
        }
      }
      const charted = workNodes(run.nodes).filter((n) => n.start !== undefined).length >= 2;
      timelineEmpty.hidden = charted;
    },
    dispose() {
      host.replaceChildren();
    },
  };
}
