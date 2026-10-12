// The exec view page: one full tab for one delegated execution — header, alert,
// timeline, tree, detail, results — owning the selection between them and one clock
// for all of them. It must name no source: a consumer hands it an `ExecRun`.

import { el } from "@cplieger/reactive";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { attachClamp, releaseClampsIn } from "../clamp-text.js";
import { chevronEl } from "../chevron.js";
import { wireRowToggle } from "../disclosure-row.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN } from "../icons.js";
import { reconcile } from "../reconcile.js";
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
import { STATE_WORD, inFlight, paintStateMark, settled } from "./status.js";
import { alertGlyph, buildAlertLine, wantsReader } from "./alert.js";
import { paintLabel } from "./label.js";
import { buildExecTree, kindGlyph, nodeAt, attentionRank, type ExecTreeView } from "./tree.js";
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
  /** The same verbs as compact icons for the dock's timeline row, under the same contract. Read
   *  only with a `dock`. */
  dockControls?: (run: ExecRun) => HTMLElement | null;
  /** What the dock's alert row offers beside the run's verbs (an ask's Answer), under the same
   *  contract. Asked on every repaint, alert or not. Read only with a `dock`. */
  alertAction?: (run: ExecRun) => HTMLElement | null;
  /** The inline control for the step the detail pane shows, or null, under the same contract;
   *  the consumer decides which states earn one. Asked with no node too, so it can let go. */
  stepControls?: (run: ExecRun, node: ExecNode | undefined) => HTMLElement | null;
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
  const alertLine = buildAlertLine("div", "ev-alert ev-alert-line");
  const alert = alertLine.root;
  alert.setAttribute("role", "status");
  alert.setAttribute("aria-live", "polite");
  alert.hidden = true;
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
  // Docked, the timeline box is always there, so one planned step is chart enough; on the page a
  // single bar would say nothing the header's clock does not.
  const timeline: ExecTimelineView = buildExecTimeline(select, opts.dock === undefined ? 2 : 1);
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
    dock.timeline.body.append(timeline.root);
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
      dock.setControls(opts.dockControls?.(run) ?? null);
      dock.setAlertAction(opts.alertAction?.(run) ?? null);
    }
    const node = nodeAt(run.nodes, selected);
    detail.render(node);
    detail.setControls(opts.stepControls?.(run, node) ?? null);
    // In `repaint` rather than in `render`, because the region is the SELECTED node's:
    // a selection change is a change of subject, and only this path runs on one.
    renderResults(node);
    if (dock !== undefined) {
      dock.results.root.hidden = run.live;
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
      progress.textContent = c.total <= 1 ? "" : progressText(c);
      const win = execWindow(run.nodes, run.live);
      clock.textContent = win === undefined ? "" : formatElapsed(win.span);

      renderInputs(run);

      alert.hidden = run.alert === undefined;
      if (run.alert !== undefined) {
        alertLine.paint(run.alert);
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

/** One dock box: a head row that opens it from anywhere but its own controls, over a region the
 *  shared disclosure slides, as every chat disclosure does. */
interface DockBox {
  readonly root: HTMLElement;
  readonly body: HTMLElement;
  readonly ctl: DisclosureController;
}

interface Dock {
  readonly timeline: DockBox;
  readonly steps: DockBox;
  readonly results: DockBox;
  readonly resultsEmpty: HTMLElement;
  paint(run: ExecRun): void;
  /** Seat the run's compact controls on the timeline row; the same element again moves nothing. */
  setControls(row: HTMLElement | null): void;
  /** Seat the alert row's own action, under `setControls`' contract. */
  setAlertAction(action: HTMLElement | null): void;
  dispose(): void;
}

let dockIds = 0;

/** One in-flight (or newest) step's line on the folded Steps row. */
interface DockRow {
  readonly root: HTMLElement;
  readonly mark: HTMLElement;
  readonly kind: HTMLElement;
  readonly label: HTMLElement;
  readonly tail: HTMLElement;
  readonly dur: HTMLElement;
}

function buildDock(host: HTMLElement): Dock {
  const boxes: DockBox[] = [];
  const mk = (key: string, name: string, info: HTMLElement, ...tail: HTMLElement[]): DockBox => {
    info.id = `ev-dock-${key}-${String(++dockIds)}`;
    const toggle = el(
      "button",
      {
        type: "button",
        className: "ev-box-toggle",
        "aria-label": name,
        "aria-describedby": info.id,
      },
      chevronEl(),
    );
    const head = el("div", { className: "ev-box-head" }, toggle, info, ...tail);
    const body = el("div", { className: "ev-box-body" });
    const region = el("div", { className: "ev-box-region" }, body);
    const root = el("div", { className: "ev-box", "data-box": key }, head, region);
    const ctl = createDisclosure(toggle, region, {
      onToggle: (open) => {
        root.classList.toggle("ev-box-open", open);
        if (!open) {
          return;
        }
        // One box open at a time, the one leaving sliding shut as the other opens.
        for (const other of boxes) {
          if (other.root !== root && other.ctl.isOpen) {
            other.ctl.close();
          }
        }
      },
    });
    wireRowToggle(head, toggle);
    const box = { root, body, ctl };
    boxes.push(box);
    return box;
  };

  // The timeline row: the plan as one serial bar, where the run stands, its clock and its controls,
  // so none of them scrolls out of reach. An alert that wants the reader takes the summary's place,
  // with its own action beside the verbs; a `stopped` one is a mark beside the clock.
  const mini = el("span", { className: "ev-mini", "aria-hidden": "true" });
  const progress = el("span", { className: "ev-box-sum" });
  const clock = el("span", { className: "ev-box-dur" });
  const alertMark = el("span", { className: "ev-box-alert", role: "img", hidden: true });
  alertMark.replaceChildren(iconEl(alertGlyph("stopped")));
  const summary = el("span", { className: "ev-box-summary" }, mini, progress, clock, alertMark);
  const dockAlert = buildAlertLine("span", "ev-box-alertline");
  dockAlert.root.hidden = true;
  const alertActs = el("span", { className: "ev-box-alert-act" });
  const tlInfo = el("span", { className: "ev-box-info" }, summary, dockAlert.root);
  const actions = el("span", { className: "ev-box-actions" });
  const timeline = mk("timeline", "Timeline", tlInfo, alertActs, actions);

  // The steps row: every step in flight, each with its state, agent mark, name and newest line.
  // The list scrolls inside itself past a few rows (18-pages.css), so a wide fan-out keeps the
  // composer on screen.
  const rows = el("span", { className: "ev-box-rows" });
  const steps = mk("steps", "Steps", el("span", { className: "ev-box-info" }, rows));

  const latest = el("span", { className: "ev-box-latest" });
  const latestDur = el("span", { className: "ev-box-dur" });
  const results = mk(
    "results",
    "Results",
    el(
      "span",
      { className: "ev-box-info" },
      el("span", { className: "ev-box-title" }, "Results"),
      latest,
      latestDur,
    ),
  );
  results.root.hidden = true;
  const resultsEmpty = el("p", { className: "ev-box-empty" });
  host.replaceChildren(timeline.root, steps.root, results.root);

  const rowParts = new WeakMap<HTMLElement, DockRow>();
  const buildRow = (): DockRow => {
    const mark = el("span", { className: "ev-state", "aria-hidden": "true" });
    const kind = el("span", { className: "ev-kind", "aria-hidden": "true" });
    const label = el("span", { className: "ev-dock-label" });
    const tailEl = el("span", { className: "ev-dock-tail" });
    const dur = el("span", { className: "ev-dock-dur" });
    const root = el("span", { className: "ev-dock-row" }, mark, kind, label, tailEl, dur);
    const row = { root, mark, kind, label, tail: tailEl, dur };
    rowParts.set(root, row);
    return row;
  };
  const paintRow = (row: DockRow, n: ExecNode): void => {
    if (row.root.dataset["state"] !== n.state) {
      paintStateMark(row.mark, n.state);
      row.root.dataset["state"] = n.state;
    }
    if (row.root.dataset["kind"] !== n.kind) {
      row.kind.replaceChildren(kindGlyph(n.kind));
      row.root.dataset["kind"] = n.kind;
    }
    paintLabel(row.label, n);
    setText(row.tail, n.tail ?? "");
    const ms = elapsed(n.start, n.end);
    setText(row.dur, ms > 0 ? formatElapsed(ms) : "");
  };

  return {
    timeline,
    steps,
    results,
    resultsEmpty,
    paint(run) {
      const work = currentWork(run.nodes);
      const paintSeg = (seg: HTMLElement, n: ExecNode): void => {
        seg.dataset["state"] = n.state;
        seg.setAttribute("data-tooltip", `${n.label}, ${STATE_WORD[n.state]}`);
      };
      reconcile(mini, work, {
        key: (n) => n.path,
        mount: (n) => {
          const seg = el("span", { className: "ev-mini-seg" });
          paintSeg(seg, n);
          return seg;
        },
        update: paintSeg,
      });
      setText(progress, progressText(counters(run.nodes)));
      const win = execWindow(run.nodes, run.live);
      setText(clock, win === undefined ? "" : formatElapsed(win.span));
      const wanting = wantsReader(run.alert) ? run.alert : undefined;
      summary.hidden = wanting !== undefined;
      dockAlert.root.hidden = wanting === undefined;
      timeline.root.classList.toggle("ev-box-alerting", wanting !== undefined);
      if (wanting !== undefined) {
        timeline.root.dataset["kind"] = wanting.kind;
        dockAlert.paint(wanting);
        setTip(dockAlert.root, wanting.text);
      } else {
        delete timeline.root.dataset["kind"];
      }
      alertMark.hidden = run.alert?.kind !== "stopped";
      if (run.alert?.kind === "stopped") {
        setTip(alertMark, run.alert.text);
        if (alertMark.getAttribute("aria-label") !== run.alert.text) {
          alertMark.setAttribute("aria-label", run.alert.text);
        }
      }

      const flying = work.filter((n) => inFlight(n.state));
      const newest = newestStarted(run.nodes);
      const shown = flying.length > 0 ? flying : [newest ?? work[0]].flatMap((n) => n ?? []);
      reconcile(rows, shown, {
        key: (n) => n.path,
        mount: (n) => {
          const row = buildRow();
          paintRow(row, n);
          return row.root;
        },
        update: (rowRoot, n) => {
          const row = rowParts.get(rowRoot);
          if (row !== undefined) {
            paintRow(row, n);
          }
        },
      });

      paintLabel(latest, newest ?? { label: "" });
      const ms = newest === undefined ? 0 : elapsed(newest.start, newest.end);
      setText(latestDur, ms > 0 ? formatElapsed(ms) : "");
    },
    setControls(row) {
      seat(actions, row);
    },
    setAlertAction(action) {
      seat(alertActs, action);
    },
    dispose() {
      for (const b of boxes) {
        b.ctl.dispose();
      }
      host.replaceChildren();
    },
  };
}

/** Make `child` the host's only child, or empty it; the same element again moves nothing, since a
 *  re-insertion blurs whatever is focused inside and this runs once per progress frame. */
function seat(host: HTMLElement, child: HTMLElement | null): void {
  if (child === null) {
    host.replaceChildren();
  } else if (host.firstChild !== child || host.childNodes.length !== 1) {
    host.replaceChildren(child);
  }
}

function setTip(target: HTMLElement, text: string): void {
  if (target.getAttribute("data-tooltip") !== text) {
    target.setAttribute("data-tooltip", text);
  }
}

/** A text write that skips an unchanged value, since the 1s tick repaints every row. */
function setText(target: HTMLElement, text: string): void {
  if (target.textContent !== text) {
    target.textContent = text;
  }
}

/** Where the run stands: the in-flight step's position, else how many are done. */
function progressText(c: ReturnType<typeof counters>): string {
  if (c.total === 0) {
    return "";
  }
  return c.current > 0
    ? `step ${String(c.current)} of ${String(c.total)}`
    : `${String(c.done)} of ${String(c.total)}`;
}
