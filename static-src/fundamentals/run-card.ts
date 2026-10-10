// The WORKFLOW RUN card: HEAD / ALERT (ask, pause, stop or failure) / BODY (a GROUP per container,
// a repeat's latest pass folded into the repeat's group; a ROW per work node, a DOOR into the run
// tab) / FOOT (outcome + link). The body is the run tab's own model (`runToExec`), so the two
// cannot disagree on what is work or where it sits. No step content: hosting it built every card.
// Expanded while it is the newest top-level element, folded once superseded, unless one of FOUR
// REFUSALS holds: a failure, a still-live run (incl. paused or unknown), an unanswered ask, or a
// reader decision. An aborted or cancelled run folds. A later failure re-opens it. A pure view of
// `inspect` (`run-store.ts` fetches).

import { join as joinKey } from "@cplieger/keyenc";
import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { STATE_WORD, paintStateMark, stateOf } from "../exec-view/status.js";
import {
  counters,
  currentMembers,
  elapsed,
  failureOwner,
  isWork,
  latestPass,
  window as execWindow,
  type ExecKind,
  type ExecNode,
} from "../exec-view/model.js";
import { reconcile } from "../reconcile.js";
import { kindGlyph } from "../exec-view/tree.js";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "../icons.js";
import { buildPath } from "../route-path.js";
import { runToExec, type RunAsks, type StepEnds } from "../run-exec-source.js";
import { formatElapsed, truncate } from "../strings.js";
import type { ToolStatus } from "../types.js";
import {
  isNeedInputPark,
  pauseDetailPhrase,
  pausePendingSentence,
  runIsLive,
  type RunState,
  userStopSentence,
} from "../run-store.js";

const NO_ASKS: RunAsks = { count: 0, nodes: new Set<string>(), label: "" };

/** The parts of a run's read the store keeps beside its state (`runPlan`, `runStepEnds`): the plan
 *  is what a container KAS has not expanded yet will hold. */
interface RunDetail {
  readonly plan?: unknown;
  readonly ends?: StepEnds;
}

/** The pause as a sentence a reader can act on. KAS's two `need_input` literals name a tool and a
 *  mechanism, so they are replaced (the arm above renders the question when the dock has it). Takes
 *  the STATE: for a park inside a parallel branch the run's reason names only a branch. */
function pauseSentence(state: RunState | undefined): string {
  if (isNeedInputPark(state)) {
    return "A step is waiting for your answer";
  }
  const reason = state?.pauseReason;
  if (reason === undefined || reason === "") {
    return "Waiting";
  }
  return `Waiting: ${reason}`;
}

/** A run's status as one word. `paused` reads "waiting": KAS pauses for a watch, a retry budget
 *  and a loop policy alike. */
function runWord(status: RunState["status"]): string {
  switch (status) {
    case "running":
      return "running";
    case "paused":
      return "waiting";
    case "completed":
      return "completed";
    case "failed":
      return "failed";
    case "aborted":
      return "stopped";
    case "cancelled":
      return "cancelled";
    case "unknown":
      return "unknown";
    case undefined:
      return "starting";
  }
}

/** A mounted run card plus its imperative handle. */
export interface RunCardView {
  readonly root: HTMLDivElement;
  /** Re-render every region from a fresh state; idempotent, rows reconciled in place. `asks` and
   *  `detail` default to the last ones given. */
  render(state: RunState | undefined, asks?: RunAsks, detail?: RunDetail): void;
  /** Advance the clocks only, on a 1s tick while the run is live. */
  tick(): void;
  /** Fold in the LAUNCH tool call's status and output: a failed launch created no run, so the tool
   *  call is the only witness. Silent on success. */
  setLaunch(status: ToolStatus, output: string | undefined): void;
  /** Whether another element has been posted after this card in the store. Pushed in
   *  because only the dispatcher holds the block index that answers it. True FOLDS
   *  the card subject to the four refusals; false never opens one. */
  setSuperseded(superseded: boolean): void;
}

interface StepRow {
  root: HTMLElement;
  head: HTMLElement;
  glyph: HTMLElement;
  name: HTMLElement;
  meta: HTMLElement;
  dur: HTMLElement;
  /** Live while the step runs, so the clock ticks; cleared once it settles. */
  start?: string;
  end?: string;
}

/** A container: a label over its members, with no state mark and no clock of its own. */
interface GroupRow {
  root: HTMLElement;
  glyph: HTMLElement;
  name: HTMLElement;
  meta: HTMLElement;
  body: HTMLElement;
  kind?: ExecKind;
}

/** What a container shows, its `currentMembers`. A repeat's pass gets no group of its own: its
 *  members hang off the repeat, whose group carries the pass number. The run tab keeps every pass. */
function membersOf(n: ExecNode): readonly ExecNode[] {
  const members = currentMembers(n);
  return n.kind === "repeat" ? members.flatMap((p) => (isWork(p) ? [p] : p.children)) : members;
}

/** A container's one-line fact: a repeat's pass against its bound, else the adapter's summary. */
function groupMeta(n: ExecNode): string {
  const pass = n.kind === "repeat" ? latestPass(n)?.pass : undefined;
  if (pass === undefined) {
    return n.subtitle ?? "";
  }
  return n.maxPasses === undefined
    ? `pass ${String(pass)}`
    : `pass ${String(pass)} of ${String(n.maxPasses)}`;
}

/** The card's disclosure bookkeeping, delegated because the registry's keys are not known here.
 *  `wasOpen` is the READER's state (`undefined` = undecided; a decision ends the auto path);
 *  `defaultOpen` is the newest-element verdict. */
export interface RunDisclosure {
  readonly wasOpen: () => boolean | undefined;
  readonly defaultOpen: boolean;
  readonly onOpenChange: (open: boolean) => void;
}

/** Build a run card. `name` is the creation-time label; later renders prefer `runLabel`. `onOpen`
 *  and `disclosure` are injected to avoid importing the run-tab feature module; `onOpen`'s third
 *  argument is a STEP ROW's node (absent = the run). With no `disclosure` the card mounts OPEN. */
export function buildRunCard(
  workflowID: string,
  name: string,
  onOpen: (id: string, label: string, focusNode?: string) => void,
  disclosure?: RunDisclosure,
): RunCardView {
  const root = el("div", {
    className: "run-card",
    "data-run": workflowID,
    "data-status": "starting",
  }) as HTMLDivElement;
  /** The run's own route (the FOOT link, no node), through `buildPath` so it cannot be spelled
   *  differently from the parser; `route-path.ts` has no imports. */
  const runHref = buildPath({ kind: "run", id: workflowID });
  // The reader's own state outranks the verdict, and having one at all is what turns
  // the auto path off for this card's whole life.
  const readerState = disclosure?.wasOpen();
  const cardOpen = readerState ?? disclosure?.defaultOpen ?? true;
  let userToggled = readerState !== undefined;
  root.classList.toggle("collapsed", !cardOpen);

  // --- head -----------------------------------------------------------------
  const icon = el("span", { className: "run-icon", "aria-hidden": "true" }, iconEl(ICON_TAB_RUN));
  const nameEl = el("span", { className: "run-name" }, name);
  // The step counter is the only fact this row carries besides the name: the status
  // word and the elapsed clock are `.run-foot`'s, which states both 44px below and
  // outside the disclosure, so a second rendering here was one fact with two owners.
  const countEl = el("span", { className: "run-count" });
  // A span, not a button: the head is `role="button"`, so a nested `<button>`
  // is axe's `nested-interactive` regardless of aria-hidden/tabindex="-1".
  const chevron = el("span", { className: "run-toggle", "aria-hidden": "true" }, chevronEl());
  const head = el(
    "div",
    { className: "run-head", role: "button", tabindex: "0" },
    // The chevron LEADS because it discloses the rows below (chevron.ts: a disclosure chevron leads
    // and rotates, a navigation one trails), telling this head from a step row at rest.
    chevron,
    icon,
    nameEl,
    countEl,
  );

  // --- alert ------------------------------------------------------------
  // A `status` live region: a run parked waiting for a PR review must not
  // require polling to notice.
  const alert = el("div", {
    className: "run-alert hidden",
    role: "status",
    "aria-live": "polite",
  });

  // --- body -----------------------------------------------------------------
  const steps = el("div", { className: "run-steps" });
  const outputs = el("dl", { className: "run-outputs hidden" });
  const body = el("div", { className: "run-body" }, steps, outputs);

  // --- foot ---------------------------------------------------------------
  const ledger = el("span", { className: "run-ledger" });
  // A real anchor (middle-click, copy-link work) with a click handler that
  // opens the tab instead of navigating. `onOpen` is injected since this
  // `fundamentals/` view must not import `run-view.ts`'s feature module.
  const open = el(
    "a",
    { className: "run-open", href: runHref },
    "Open run",
    el("span", { className: "run-open-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  );
  open.addEventListener("click", (e) => {
    // A modified click is a deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    onOpen(workflowID, nameEl.textContent);
  });
  const foot = el("div", { className: "run-foot" }, ledger, open);

  root.append(head, alert, body, foot);

  const ctl = createDisclosure(head, body, {
    open: cardOpen,
    onToggle: (isOpen, source) => {
      // The class write is for BOTH sources — it is what the skin keys on, and an
      // auto collapse has to reach it. The LATCH and the registry write are the
      // reader's alone, or the card would record its own folds as their choice.
      root.classList.toggle("collapsed", !isOpen);
      if (source !== "user") {
        return;
      }
      userToggled = true;
      disclosure?.onOpenChange(isOpen);
    },
  });

  /** Each mounted row's and group's parts, by its root; a removed one is collected with it. */
  const stepParts = new WeakMap<HTMLElement, StepRow>();
  const groupParts = new WeakMap<HTMLElement, GroupRow>();
  let liveClock = false;
  /** Whether the store holds another element after this card. */
  let superseded = false;
  /** Set when the LAUNCH itself failed, so the alert stays on the tool call's
   *  reason instead of being overwritten by a state that will never arrive. */
  let launchError = "";
  /** The last state, asks and detail rendered, so `setLaunch` can re-render
   *  without re-fetching and without restating what it does not know. */
  let lastState: RunState | undefined;
  let lastAsks: RunAsks = NO_ASKS;
  let lastDetail: RunDetail = {};
  /** The run tab's model of `lastState`, which every region below reads. */
  let lastNodes: readonly ExecNode[] = [];

  /** One step's row: a real anchor into the run tab with that node selected, yielding to a modified
   *  click. Spans only, no `role` or `tabindex` (an anchor in a `role="button"` host is axe's
   *  `nested-interactive`). `href` carries the node as a `#node=` fragment; the plain click goes
   *  through `onOpen` to activate in place. */
  function stepRow(path: string): StepRow {
    const glyph = el("span", { className: "run-step-glyph", "aria-hidden": "true" });
    const nameEl2 = el("span", { className: "run-step-name" });
    const meta = el("span", { className: "run-step-meta" });
    const dur = el("span", { className: "run-step-dur" });
    const rowHead = el(
      "a",
      { className: "run-step-head", href: buildPath({ kind: "run", id: workflowID, node: path }) },
      glyph,
      nameEl2,
      meta,
      dur,
    );
    rowHead.addEventListener("click", (e) => {
      // A modified click is a deliberate escape from the app's own routing.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
        return;
      }
      e.preventDefault();
      onOpen(workflowID, nameEl.textContent, path);
    });
    const rowRoot = el("div", { className: "run-step", "data-node": path }, rowHead);
    const row: StepRow = { root: rowRoot, head: rowHead, glyph, name: nameEl2, meta, dur };
    stepParts.set(rowRoot, row);
    return row;
  }

  /** One container's group: a plain label over its members, not a control, so it adds no tab stop. */
  function groupRow(path: string): GroupRow {
    const glyph = el("span", { className: "run-group-glyph", "aria-hidden": "true" });
    const name = el("span", { className: "run-group-name" });
    const meta = el("span", { className: "run-group-meta" });
    const groupBody = el("div", { className: "run-group-body" });
    const groupRoot = el(
      "div",
      { className: "run-group", role: "group", "data-node": path },
      el("div", { className: "run-group-head" }, glyph, name, meta),
      groupBody,
    );
    const g: GroupRow = { root: groupRoot, glyph, name, meta, body: groupBody };
    groupParts.set(groupRoot, g);
    return g;
  }

  function paintGroup(g: GroupRow, node: ExecNode): void {
    if (g.kind !== node.kind) {
      g.glyph.replaceChildren(kindGlyph(node.kind));
      g.kind = node.kind;
    }
    g.root.dataset["kind"] = node.kind;
    g.name.textContent = node.label;
    const meta = groupMeta(node);
    g.meta.textContent = meta;
    g.root.setAttribute("aria-label", meta === "" ? node.label : `${node.label}, ${meta}`);
  }

  /** Paint one row from its node. */
  function paintRow(row: StepRow, node: ExecNode): void {
    row.root.dataset["status"] = node.state;
    row.root.dataset["nodeType"] = node.kind;
    paintStateMark(row.glyph, node.state);
    row.name.textContent = node.label;
    // Via a local, not directly: `undefined` is not a valid value for an
    // optional property under exactOptionalPropertyTypes.
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

    // Who ran it and on what (the adapter's subtitle), then what went wrong.
    const bits: string[] = [];
    if (node.subtitle !== undefined) {
      bits.push(node.subtitle);
    }
    if (node.failure !== undefined) {
      bits.push(truncate(node.failure, 80));
    }
    row.meta.textContent = bits.join(" \u00b7 ");

    const ms = elapsed(node.start, node.end);
    row.dur.textContent = ms > 0 ? formatElapsed(ms) : "";
    row.head.setAttribute("aria-label", `${node.label}, ${STATE_WORD[node.state]}`);

    // Visible on the collapsed row, since a capture is a RESULT. Guarded on the text changing, since
    // render() runs on every invalidation.
    const cap = row.root.querySelector<HTMLElement>(":scope > .run-step-capture");
    const text = node.output ?? "";
    if (text === "") {
      cap?.remove();
    } else if (cap?.dataset["text"] !== text) {
      const next = el("div", { className: "run-step-capture", "data-text": text }, text);
      if (cap === null) {
        row.root.appendChild(next);
      } else {
        cap.replaceWith(next);
      }
    }
  }

  /** Paint a mounted row or group from its node; a group reconciles its members in turn. */
  function paintNode(nodeRoot: HTMLElement, node: ExecNode): void {
    const row = stepParts.get(nodeRoot);
    if (row !== undefined) {
      paintRow(row, node);
      return;
    }
    const g = groupParts.get(nodeRoot);
    if (g !== undefined) {
      paintGroup(g, node);
      seat(g.body, membersOf(node));
    }
  }

  /** One sibling list, reconciled in document order. The key carries the kind, so a replanned run
   *  that puts a step where a container was remounts rather than painting one shape as the other. */
  function seat(into: HTMLElement, nodes: readonly ExecNode[]): void {
    reconcile(into, nodes, {
      key: (n) => joinKey(isWork(n) ? "step" : "group", n.path),
      mount: (n) => {
        const nodeRoot = isWork(n) ? stepRow(n.path).root : groupRow(n.path).root;
        paintNode(nodeRoot, n);
        return nodeRoot;
      },
      update: paintNode,
    });
  }

  /** The alert: five things that can put a run in front of a person, ordered
   *  by what the reader can do about it. Only one shows. It truncates tighter
   *  than the run tab's alert, which carries the detail this glance leaves out. */
  function renderAlert(state: RunState | undefined, asks: RunAsks): void {
    const parts: string[] = [];
    let kind = "";
    if (launchError !== "") {
      // Outranks every state: the launch never created a run, so `inspect`
      // has nothing and only this reason exists.
      kind = "failed";
      parts.push(launchError);
    } else if (asks.count > 0) {
      // Ahead of the run's own status: it still reads `running` while a
      // step's ask blocks it.
      kind = "input";
      parts.push(
        asks.label === ""
          ? "Waiting for your answer"
          : `Waiting for your answer: ${truncate(asks.label, 120)}`,
      );
      if (asks.count > 1) {
        parts.push(`${String(asks.count)} asks waiting`);
      }
    } else if (state !== undefined && userStopSentence(state) !== undefined) {
      kind = "stopped";
      parts.push(userStopSentence(state) ?? "");
    } else if (state !== undefined && pausePendingSentence(state) !== undefined) {
      kind = "paused";
      parts.push(pausePendingSentence(state) ?? "");
    } else if (state?.status === "paused") {
      kind = "paused";
      parts.push(pauseSentence(state));
      const detail = pauseDetailPhrase(state.pauseDetail);
      if (detail !== undefined) {
        parts.push(detail);
      }
    } else if (state?.status === "failed") {
      kind = "failed";
      const failed = failureOwner(lastNodes);
      parts.push(
        failed === undefined
          ? "The run failed"
          : `${failed.label} failed: ${truncate(failed.failure ?? "", 160)}`,
      );
    }
    if (parts.length === 0) {
      alert.classList.add("hidden");
      alert.replaceChildren();
      return;
    }
    alert.dataset["kind"] = kind;
    alert.classList.remove("hidden");
    alert.textContent = parts.join(" \u00b7 ");
  }

  /** Artifacts and captured outputs as one result list (one thing to a reader); artifacts win a key
   *  collision. */
  function renderOutputs(state: RunState | undefined): void {
    const merged = new Map<string, string>();
    for (const [k, v] of Object.entries(state?.capturedOutputs ?? {})) {
      merged.set(k, v);
    }
    for (const [k, v] of Object.entries(state?.artifacts ?? {})) {
      merged.set(k, v);
    }
    if (merged.size === 0) {
      outputs.classList.add("hidden");
      outputs.replaceChildren();
      delete outputs.dataset["sig"];
      return;
    }
    // A signature over the whole list, for paintRow's reason: this runs on every
    // invalidation, and rebuilding N markdown renders per frame would both cost
    // real work and reset the reader's scroll inside a long report.
    const sig = [...merged].map(([k, v]) => `${k}\u0000${v}`).join("\u0001");
    if (outputs.dataset["sig"] === sig) {
      return;
    }
    outputs.dataset["sig"] = sig;
    outputs.classList.remove("hidden");
    outputs.replaceChildren(
      ...[...merged.entries()].flatMap(([k, v]) => [
        el("dt", { className: "run-output-key" }, k),
        // Rendered when EMPTY: KAS writes a key only for a step that captured, so empty means it said
        // nothing, which differs from "never ran".
        el(
          "dd",
          { className: v.trim() === "" ? "run-output-val run-output-val-empty" : "run-output-val" },
          v.trim() === "" ? "This step's last message carried no text." : truncate(v, 300),
        ),
      ]),
    );
  }

  function renderFoot(state: RunState | undefined): void {
    const c = counters(lastNodes);
    const bits: string[] = [];
    if (c.total > 0) {
      bits.push(`${String(c.total)} ${c.total === 1 ? "step" : "steps"}`);
    }
    if (c.failed > 0) {
      bits.push(`${String(c.failed)} failed`);
    }
    bits.push(runWord(state?.status));
    const ms = execWindow(lastNodes, runIsLive(state))?.span ?? 0;
    if (ms > 0) {
      bits.push(formatElapsed(ms));
    }
    ledger.textContent = bits.join(" \u00b7 ");
  }

  /** Whether this run holds a FAILURE a reader has to see: a failed launch, a failed run, or a
   *  failed work node the card shows (the latest pass's, as the foot counts them). The one
   *  carve-out that works in both directions. */
  function holdsFailure(): boolean {
    if (launchError !== "") {
      return true;
    }
    if (lastState === undefined) {
      return false;
    }
    return stateOf(lastState.status) === "fail" || counters(lastNodes).failed > 0;
  }

  /** The newest-element fold, and the only place the four refusals are spelled. A failure RE-OPENS a
   *  folded card and blocks a fold thereafter; idempotent and monotone in `superseded`. */
  function applyAutoCollapse(): void {
    if (userToggled) {
      return;
    }
    if (holdsFailure()) {
      if (!ctl.isOpen) {
        ctl.open();
      }
      return;
    }
    if (
      !superseded ||
      // Not knowing is not the same as finished: the card is built before its first
      // `inspect` lands.
      lastState === undefined ||
      // An ask outranks the run's own status, and it is the one refusal a status
      // cannot express — the head reads "needs input" whatever the run says.
      lastAsks.count > 0 ||
      // STILL LIVE covers `paused` and `unknown` as well as `running`. Not "settled clean": that would
      // keep a stopped (`warn`) run from ever folding. `failed` is `holdsFailure` above.
      runIsLive(lastState) ||
      !ctl.isOpen
    ) {
      return;
    }
    ctl.close();
  }

  function render(
    state: RunState | undefined,
    asks: RunAsks = lastAsks,
    detail: RunDetail = lastDetail,
  ): void {
    lastState = state;
    lastAsks = asks;
    lastDetail = detail;
    lastNodes =
      state === undefined
        ? []
        : runToExec(workflowID, state, detail.plan, asks, "", detail.ends).nodes;
    const label = state?.runLabel ?? state?.workflowName ?? "";
    if (label !== "") {
      nameEl.textContent = label;
    }
    root.dataset["status"] = state?.status ?? "starting";
    // Second axis rather than a sixth `data-status` value: the run genuinely
    // IS running while a step's ask blocks it.
    if (asks.count > 0) {
      root.dataset["asking"] = "true";
    } else {
      delete root.dataset["asking"];
    }
    // An ask outranks the run's own word: the run genuinely IS running while a step's
    // ask blocks it, so `data-status` keeps the status and this says what is wanted.
    const word = asks.count > 0 ? "needs input" : runWord(state?.status);

    const c = counters(lastNodes);
    countEl.textContent =
      c.total === 0
        ? ""
        : c.current > 0
          ? `step ${String(c.current)} of ${String(c.total)}`
          : `${String(c.done)} of ${String(c.total)}`;

    liveClock = runIsLive(state);

    renderAlert(state, asks);
    seat(steps, lastNodes);
    renderOutputs(state);
    renderFoot(state);
    // The head's one statement of the run's state, since the row itself shows the
    // name and the counter; the foot carries the word visibly.
    head.setAttribute("aria-label", `Workflow run ${nameEl.textContent}, ${word}`);
    // Every state change re-asks the fold: a run that settled clean is now foldable,
    // and one that failed re-opens.
    applyAutoCollapse();
  }

  function tick(): void {
    if (!liveClock) {
      return;
    }
    // Re-derive from the rows' own timestamps rather than re-fetch — the one
    // thing the client can advance honestly on its own.
    for (const rowRoot of steps.querySelectorAll<HTMLElement>(".run-step")) {
      const row = stepParts.get(rowRoot);
      if (row === undefined) {
        continue;
      }
      const rowMs = elapsed(row.start, row.end);
      if (rowMs > 0) {
        row.dur.textContent = formatElapsed(rowMs);
      }
    }
    // The run's own elapsed is the FOOT's, and `window` reads `Date.now()` while
    // the run is live, so re-rendering the ledger IS the tick.
    renderFoot(lastState);
  }

  render(undefined);

  return {
    root,
    render,
    tick,
    setLaunch(status: ToolStatus, output: string | undefined): void {
      if (status !== "failed") {
        return;
      }
      const text = (output ?? "").trim();
      launchError = text === "" ? "The workflow could not be started" : truncate(text, 200);
      liveClock = false;
      // `render` ends in `applyAutoCollapse`, so a launch failing after a fold
      // re-opens the card through the same door a failed state does.
      render(lastState);
    },
    setSuperseded(next: boolean): void {
      superseded = next;
      applyAutoCollapse();
    },
  };
}
