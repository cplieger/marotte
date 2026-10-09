// The WORKFLOW RUN card: a run's home in the transcript (the run TAB, `exec-view/`, shares only
// the status vocabulary). HEAD / ALERT (ask, pause, stop or failure) / BODY (one row per step,
// each a DOOR into the run tab) / FOOT (outcome + link). It renders NO step content, because
// hosting it built every step's cards (`content-visibility` skips paint, not construction).
// Expanded while it is the newest top-level element, folded once superseded, unless one of FOUR
// REFUSALS holds: a failure, a still-live run (incl. paused or unknown), an unanswered ask, or a
// reader decision. An aborted or cancelled run folds. A later failure re-opens it. A pure view of
// `inspect` (`run-store.ts` fetches).

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { STATE_WORD, paintStateMark, stateOf, withAsk } from "../exec-view/status.js";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "../icons.js";
import { buildPath } from "../route-path.js";
import { nodePathKey } from "../run-node-key.js";
import { formatElapsed, truncate } from "../strings.js";
import type { ToolStatus } from "../types.js";
import {
  isNeedInputPark,
  leafNodes,
  nodeAddressOf,
  pauseDetailPhrase,
  pausePendingSentence,
  runCounters,
  runElapsedMs,
  runIsLive,
  elapsedMs,
  type NodeAddress,
  type RunNode,
  type RunState,
  userStopSentence,
} from "../run-store.js";

/** The state vocabulary is `exec-view/status.ts`, shared with the `/run/{id}`
 *  page's tree, timeline and detail pane — one module rather than a private
 *  copy per surface. */

/** What a run is waiting on a PERSON for. KAS leaves an asking run `running`, so `inspect` cannot
 *  show it; injected from `decision-dock.ts` (a feature module) and declared here, by the consumer. */
export interface RunAsks {
  /** How many of this run's asks are unanswered. */
  count: number;
  /** The node ids those asks name. Separate from `count` because the wire cannot
   *  always attribute one (the step-session registry has to have seen the
   *  sub-session), and a run with an unattributable ask is still blocked. */
  nodes: ReadonlySet<string>;
  /** The head ask as one line, for the alert. "" when there is none to name. */
  label: string;
}

const NO_ASKS: RunAsks = { count: 0, nodes: new Set<string>(), label: "" };

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
  /** Re-render every region from a fresh state; idempotent, rows reconciled in place. `asks`
   *  defaults to the last one given. */
  render(state: RunState | undefined, asks?: RunAsks): void;
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
  startedAt?: string;
  endedAt?: string;
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

  const rows = new Map<string, StepRow>();
  let liveClock = false;
  /** Whether the store holds another element after this card. */
  let superseded = false;
  /** Set when the LAUNCH itself failed, so the alert stays on the tool call's
   *  reason instead of being overwritten by a state that will never arrive. */
  let launchError = "";
  /** The last state and the last asks rendered, so `setLaunch` can re-render
   *  without re-fetching and without restating what it does not know. */
  let lastState: RunState | undefined;
  let lastAsks: RunAsks = NO_ASKS;

  /** One step's row: a real anchor into the run tab with that node selected, yielding to a modified
   *  click. Spans only, no `role` or `tabindex` (an anchor in a `role="button"` host is axe's
   *  `nested-interactive`). `href` carries the node as a `#node=` fragment; the plain click goes
   *  through `onOpen` to activate in place. An UNPLACED address carries no node on either channel,
   *  or the page holds a pending focus forever. */
  function stepRow(addr: NodeAddress): StepRow {
    const nodePath = nodePathKey(addr.path);
    let row = rows.get(nodePath);
    if (row !== undefined) {
      return row;
    }
    const glyph = el("span", { className: "run-step-glyph", "aria-hidden": "true" });
    // The last path segment is the step; the segments above it are the loop or
    // branch containing it, and they are what tell two iterations apart.
    const label = addr.path[addr.path.length - 1] ?? nodePath;
    const nameEl2 = el("span", { className: "run-step-name" }, label);
    const meta = el("span", { className: "run-step-meta" });
    const dur = el("span", { className: "run-step-dur" });
    const rowHead = el(
      "a",
      {
        className: "run-step-head",
        href: addr.placed ? buildPath({ kind: "run", id: workflowID, node: nodePath }) : runHref,
      },
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
      onOpen(workflowID, nameEl.textContent, addr.placed ? nodePath : undefined);
    });
    const rowRoot = el("div", { className: "run-step", "data-node": nodePath }, rowHead);
    row = { root: rowRoot, head: rowHead, glyph, name: nameEl2, meta, dur };
    rows.set(nodePath, row);
    steps.appendChild(rowRoot);
    return row;
  }

  /** Paint one row from its node. Split out so the reconcile below reads as an
   *  ordering pass rather than an ordering pass with a renderer inside it. */
  function paintRow(row: StepRow, node: RunNode, asks: RunAsks): void {
    const state = stateOf(node.status);
    // An ask reclassifies a step only while it is IN FLIGHT: `node_id` is a
    // node ID not a path, so a repeat's iterations share it.
    const shown = withAsk(state, asks.nodes.has(node.nodeId));
    row.root.dataset["status"] = shown;
    row.root.dataset["nodeType"] = node.type;
    paintStateMark(row.glyph, shown);
    // Via a local, not directly: `undefined` is not a valid value for an
    // optional property under exactOptionalPropertyTypes.
    if (node.startedAt === undefined) {
      delete row.startedAt;
    } else {
      row.startedAt = node.startedAt;
    }
    if (node.endedAt === undefined) {
      delete row.endedAt;
    } else {
      row.endedAt = node.endedAt;
    }

    // Who ran it, on what, what went wrong, in that order. A watch names its
    // own kind since nothing runs a watch — it polls.
    const bits: string[] = [];
    if (node.type === "watch") {
      bits.push("watch");
    } else if (node.agentName !== undefined && node.agentName !== "") {
      bits.push(node.agentName);
    }
    if (node.modelId !== undefined && node.modelId !== "" && node.modelId !== "auto") {
      bits.push(node.modelId);
    }
    if (node.iteration !== undefined) {
      // 1-based for a reader; KAS counts from zero.
      bits.push(`pass ${String(node.iteration + 1)}`);
    }
    if (node.branchId !== undefined && node.branchId !== "") {
      bits.push(node.branchId);
    }
    if (node.continuationAttempts !== undefined && node.continuationAttempts > 0) {
      bits.push(`${String(node.continuationAttempts)} retries`);
    }
    if (node.failureReason !== undefined && node.failureReason !== "") {
      bits.push(truncate(node.failureReason, 80));
    }
    row.meta.textContent = bits.join(" \u00b7 ");

    const ms = elapsedMs(node.startedAt, node.endedAt);
    row.dur.textContent = ms > 0 ? formatElapsed(ms) : "";
    row.head.setAttribute("aria-label", `${row.name.textContent}, ${STATE_WORD[shown]}`);

    // Visible on the collapsed row, since a capture is a RESULT. Guarded on the text changing, since
    // render() runs on every invalidation.
    const cap = row.root.querySelector<HTMLElement>(":scope > .run-step-capture");
    const text = node.capturedOutput ?? "";
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

  function renderSteps(state: RunState | undefined, asks: RunAsks): void {
    const painted: StepRow[] = [];
    for (const node of leafNodes(state?.root)) {
      const row = stepRow(nodeAddressOf(state?.root, node));
      paintRow(row, node, asks);
      painted.push(row);
    }
    // Order to match the plan: a pure ordering pass over what this render just
    // painted. `stepRow` always inserts, so every leaf has a row here and the
    // second walk needs no lookup and no absent case.
    let anchor: Element | null = null;
    for (const row of painted) {
      const want: Element | null =
        anchor === null ? steps.firstElementChild : anchor.nextElementSibling;
      if (row.root !== want) {
        if (anchor === null) {
          steps.prepend(row.root);
        } else {
          anchor.after(row.root);
        }
      }
      anchor = row.root;
    }
  }

  /** The alert: five things that can put a run in front of a person, ordered
   *  by what the reader can do about it. Only one shows. */
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
      const failed = leafNodes(state.root).find(
        (n) => n.status === "failed" && n.failureReason !== undefined && n.failureReason !== "",
      );
      parts.push(
        failed === undefined
          ? "The run failed"
          : `${failed.nodeId} failed: ${truncate(failed.failureReason ?? "", 160)}`,
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
    const c = runCounters(state);
    const bits: string[] = [];
    if (c.total > 0) {
      bits.push(`${String(c.total)} ${c.total === 1 ? "step" : "steps"}`);
    }
    if (c.failed > 0) {
      bits.push(`${String(c.failed)} failed`);
    }
    bits.push(runWord(state?.status));
    const ms = runElapsedMs(state);
    if (ms > 0) {
      bits.push(formatElapsed(ms));
    }
    ledger.textContent = bits.join(" \u00b7 ");
  }

  /** Whether this run holds a FAILURE a reader has to see: a failed launch, a failed
   *  run, or any failed step. The one carve-out that works in both directions. */
  function holdsFailure(): boolean {
    if (launchError !== "") {
      return true;
    }
    if (lastState === undefined) {
      return false;
    }
    return stateOf(lastState.status) === "fail" || runCounters(lastState).failed > 0;
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

  function render(state: RunState | undefined, asks: RunAsks = lastAsks): void {
    lastState = state;
    lastAsks = asks;
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

    const c = runCounters(state);
    countEl.textContent =
      c.total === 0
        ? ""
        : c.current > 0
          ? `step ${String(c.current)} of ${String(c.total)}`
          : `${String(c.done)} of ${String(c.total)}`;

    liveClock = runIsLive(state);

    renderAlert(state, asks);
    renderSteps(state, asks);
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
    for (const row of rows.values()) {
      const rowMs = elapsedMs(row.startedAt, row.endedAt);
      if (rowMs > 0) {
        row.dur.textContent = formatElapsed(rowMs);
      }
    }
    // The run's own elapsed is the FOOT's, and `runElapsedMs` reads `Date.now()`
    // while a leaf is running, so re-rendering the ledger IS the tick.
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
