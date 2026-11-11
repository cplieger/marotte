// The run bar: one box holding a line per LIVE workflow run the active chat launched, in the bottom
// bar between the interaction dock and the steer stack. From `HEADER_AT` runs a header row heads the
// list, outside the list's clip so its touch target can reach past the row, and ends with the batch
// button at the box's top right; one run has no header and carries the button at its row's end.

import { el, computed, effect, touch, untracked } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { reconcile } from "./reconcile.js";
import { announce } from "./legacy-ui/announce.js";
import { $, setAttr, setText } from "./dom.js";
import { watchActiveId } from "./store.js";
import {
  liveRunIDsForChat,
  runIsLive,
  runControls,
  runLabelOf,
  runPausedByPerson,
  runPausedByTransientError,
  runPlan,
  runState,
  runStepEnds,
  invalidateRun,
  type RunState,
} from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { sameRunAsks, type RunAsks } from "./run-asks.js";
import { runToExec, type ExecFoldMemo } from "./run-exec-source.js";
import type { ExecRun } from "./exec-view/model.js";
import { buildExecSummary, type ExecSummaryView } from "./exec-view/summary.js";
import { wantsReader } from "./exec-view/alert.js";
import { chevronEl } from "./chevron.js";
import { isSliding, selectableButton, slideBox, targetOpen } from "./disclosure-row.js";
import { runBarOpen, setRunBarOpen } from "./device-view.js";
import { holdRunClock, releaseRunClock, type RunClockHolder } from "./messages-blocks.js";
import { openRunView } from "./run-view.js";
import { bindLoadingState } from "./actions/index.js";
import { pauseRuns, resumeRuns, type RunRef } from "./actions/runs.js";
import { iconEl } from "./icon-el.js";
import { ICON_PAUSE, ICON_RESUME } from "./icons.js";
import { wholeCreditFigure } from "./strings.js";
import type { ExecState } from "./exec-view/status.js";
import type { TabRunDotStatus } from "./tabs.js";

/** The name a run with no fetched state carries: `runLabelOf` answers `""` there, and a row with
 *  no name at all is unclickable in practice. */
const FALLBACK_NAME = "Workflow run";

/** From this many live runs a header row heads the list with the recap, its fold toggle and the batch button. One run
 *  gets no header: the batch button, as Pause or Resume, ends that run's row. */
const HEADER_AT = 2;

/** From this many live runs the list starts folded behind the header until the reader chooses, so a fan-out cannot
 *  push the transcript and the composer off a phone. */
const FOLD_AT = 3;

/** One bar row's painted parts, kept so a repaint and the clock patch them in place. */
interface RowView {
  readonly btn: HTMLElement;
  readonly glyph: HTMLElement;
  readonly summary: ExecSummaryView;
}

/** The row is re-pointed on every render rather than re-registered, so the refcount in
 *  `messages-blocks.ts` sees one holder per run for as long as the bar shows it. */
interface Hold extends RunClockHolder {
  row: RowView | null;
}

let bound = false;
let prevCount = 0;
let prevChatID = "";
const holds = new Map<string, Hold>();
const rowViews = new WeakMap<HTMLElement, RowView>();
/** The batch button, built once by `initRunBar`: on the header row, or at the end of the one run's row. */
let batchBtn: HTMLButtonElement | null = null;
/** The run whose row holds the batch button, or `null` while it sits on the header row. */
let batchRun: string | null = null;
/** The render effect's disposer, held only so `_resetRunBarForTest` can stop it. Production
 *  never tears the bar down — it lives as long as the page. */
let stopRender: (() => void) | null = null;

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initRunBar(): void {
  if (bound) {
    return;
  }
  bound = true;
  const bar = $.runBar;
  batchBtn = buildBatchButton();
  // After the toggle, which `buildSummary` prepends, so the button ends the header row.
  $.runBarFold.append(batchBtn);
  const sig = computed(() => key());
  stopRender = effect(() => {
    touch(sig);
    // The computed IS the dedupe. A tracked read inside `render` would subscribe
    // this effect to every run cell directly, so a `run_progress` refetch that did
    // not move the key would still re-run `replaceChildren` and re-fire every row's
    // @starting-style entry animation.
    untracked(() => {
      render(bar);
    });
  });
}

/** Everything the bar's CONTENT depends on, as one string so the computed dedupes by value. A
 *  run's part is its model's generation, so every new model repaints its row and the 1s tick
 *  moves only the clocks. */
function key(): string {
  const chatID = watchActiveId();
  const parts: string[] = [chatID];
  for (const id of liveRunIDsForChat(chatID)) {
    execOf(id);
    parts.push(join(id, String(execs.get(id)?.gen ?? 0)));
  }
  const batch = batchOf(rows(chatID));
  parts.push(batch === null ? "" : join(batch.verb, ...batch.targets));
  return parts.join("\u0001");
}

/** What the batch button does now: pause every listed run that runs, else resume every one a person, the parent
 *  chat or a transient error parked. `targets` are the runs the server offers that verb on, so it is empty while every
 *  running run's pause is already landing, and it never holds a run waiting for an answer or at a loop's pass limit,
 *  which the server offers no resume on (a resume re-parks them). */
interface Batch {
  readonly verb: "pause" | "resume";
  readonly targets: readonly string[];
}

function batchOf(ids: readonly string[]): Batch | null {
  const offers = (id: string, verb: string): boolean =>
    runControls(id)?.verbs.includes(verb) === true;
  const running = ids.filter((id) => runState(id)?.status === "running");
  if (running.length > 0) {
    return { verb: "pause", targets: running.filter((id) => offers(id, "pause")) };
  }
  const parked = ids.filter((id) => {
    const st = runState(id);
    return (
      st !== undefined &&
      (runPausedByPerson(st) || runPausedByTransientError(st)) &&
      offers(id, "resume")
    );
  });
  return parked.length === 0 ? null : { verb: "resume", targets: parked };
}

interface BatchView {
  readonly label: string;
  readonly icon: string;
  readonly tip: string;
}

/** The button's face on the header row, addressing every listed run. */
const BATCH_VIEW: Record<Batch["verb"], BatchView> = {
  pause: {
    label: "Pause all",
    icon: ICON_PAUSE,
    tip: "Pause every running run here. Each one's current turn is cancelled, and the run picks up from there when resumed",
  },
  resume: {
    label: "Resume all",
    icon: ICON_RESUME,
    tip: "Resume every run here that was paused by hand, by an agent, by this chat or by a transient error such as a rate limit. A run waiting for an answer or at a loop's pass limit stays paused",
  },
};

const PAUSING_TIP = "Every running run here is already pausing";

/** The button's face on the one run's row. */
const SOLO_VIEW: Record<Batch["verb"], BatchView> = {
  pause: {
    label: "Pause",
    icon: ICON_PAUSE,
    tip: "Pause this run. Its current turn is cancelled, and the run picks up from there when resumed",
  },
  resume: {
    label: "Resume",
    icon: ICON_RESUME,
    tip: "Resume this run",
  },
};

const SOLO_PAUSING_TIP = "This run is already pausing";

/** The runs the batch addresses at click time, named as the reader sees them. */
function batchRefs(targets: readonly string[]): RunRef[] {
  return targets.map((id) => ({ id, name: runLabelOf(id) || FALLBACK_NAME }));
}

function buildBatchButton(): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn run-bar-all",
    hidden: true,
    onclick: () => {
      const batch = batchOf(rows(watchActiveId()));
      if (batch === null || batch.targets.length === 0) {
        return;
      }
      const action = batch.verb === "pause" ? pauseRuns : resumeRuns;
      void action.dispatch(batchRefs(batch.targets));
    },
  }) as HTMLButtonElement;
  // `disabledFn` is read when a batch settles, so the idle state is the runs' own, never a blanket enable.
  bindLoadingState([pauseRuns.name, resumeRuns.name], btn, {
    disabledFn: () => untracked(() => batchOf(rows(watchActiveId()))?.targets.length === 0),
  });
  return btn;
}

/** Put the batch button back on the header row before the row holding it leaves: the loading binding dies with a
 *  detached button. */
function parkBatch(ids: readonly string[]): void {
  if (batchBtn !== null && batchRun !== null && !(ids.length === 1 && ids[0] === batchRun)) {
    $.runBarFold.append(batchBtn);
    batchRun = null;
  }
}

/** Seat and paint the batch button: at the header row's end with a header, else at the end of the one run's row
 *  (`.run-bar > li > .run-bar-all`, 26-dock.css). Hidden, never detached, with nothing to do, so its loading binding
 *  lives. `focused` hands it the keyboard back after a move. */
function paintBatch(bar: HTMLUListElement, ids: readonly string[], focused: boolean): void {
  const btn = batchBtn;
  if (btn === null) {
    return;
  }
  const solo = ids.length === 1 ? ids[0] : undefined;
  const row = solo === undefined ? null : bar.querySelector<HTMLElement>(":scope > li.run-bar-row");
  const seat = row ?? $.runBarFold;
  if (btn.parentElement !== seat) {
    seat.append(btn);
    batchRun = row === null ? null : (solo ?? null);
  }
  const batch = ids.length === 0 ? null : batchOf(ids);
  btn.hidden = batch === null;
  if (batch === null) {
    return;
  }
  const view = (row === null ? BATCH_VIEW : SOLO_VIEW)[batch.verb];
  if (btn.getAttribute("aria-label") !== view.label) {
    btn.replaceChildren(iconEl(view.icon));
    btn.setAttribute("aria-label", view.label);
  }
  const idle = btn.getAttribute("aria-busy") !== "true";
  const none = batch.targets.length === 0;
  if (idle) {
    btn.disabled = none;
  }
  btn.dataset["tooltip"] = none ? (row === null ? PAUSING_TIP : SOLO_PAUSING_TIP) : view.tip;
  if (focused && document.activeElement !== btn && !btn.disabled) {
    btn.focus({ preventScroll: true });
  }
}

/** The runs this bar shows for a chat: the live inventory minus anything the store can PROVE has
 *  settled. */
function rows(chatID: string): string[] {
  return liveRunIDsForChat(chatID).filter((id) => {
    const st = runState(id);
    return st === undefined || runIsLive(st);
  });
}

/** Whether a run is waiting on the reader: an unanswered ask, a pause or a failure. */
function needsYou(id: string): boolean {
  const run = execOf(id);
  return run !== undefined && (run.state === "input" || wantsReader(run.alert));
}

function render(bar: HTMLUListElement): void {
  const chatID = watchActiveId();
  pruneExecs(chatID);
  // Runs that need the reader first, each group in inventory order.
  const live = rows(chatID);
  const waiting = live.filter(needsYou);
  const ids = [...waiting, ...live.filter((id) => !waiting.includes(id))];
  const headed = ids.length >= HEADER_AT;
  const open = listOpen(ids.length);

  bar.classList.toggle("hidden", ids.length === 0);
  // BEFORE the rows, not after: `buildRow` points this run's hold at its row, so a hold created
  // afterwards would leave the FIRST render's row unaddressable, and the tick would never paint it.
  reconcileHolds(ids);
  // The waiting-first order moves a row whenever another run starts needing the reader, and a
  // moved node loses focus, so the focused row button is handed it back.
  const focused = document.activeElement;
  const held = focused instanceof HTMLElement && bar.contains(focused) ? focused : null;
  const fold = $.runBarFold;
  const batchHadFocus = focused !== null && focused === batchBtn;
  const foldHadFocus = fold.contains(focused);
  parkBatch(ids);
  const before = bar.childElementCount;
  // KEYED BY RUN ID, and each surviving row is PATCHED rather than rebuilt. Both halves are needed.
  reconcile(bar, ids, {
    key: (id: string) => id,
    mount: (id: string) => buildRow(id, chatID),
    update: paintRow,
  });
  settleFold(bar, open, bar.childElementCount !== before);
  if (held !== null && held.isConnected && document.activeElement !== held) {
    held.focus({ preventScroll: true });
  }
  setAttr(fold, "hidden", headed ? null : "");
  if (headed) {
    if (fold.querySelector(".run-bar-toggle") === null) {
      fold.prepend(buildSummary(bar));
    }
    paintSummary(fold, ids.length, waiting.length, open);
  }
  paintBatch(bar, ids, batchHadFocus);
  // Hiding the header, or the button, under the keyboard would drop focus to the body, so it moves to the run left.
  const active = document.activeElement;
  if (
    (foldHadFocus || batchHadFocus) &&
    !(active instanceof HTMLElement && active !== document.body && active.checkVisibility())
  ) {
    bar.querySelector<HTMLElement>(".run-bar-open")?.focus({ preventScroll: true });
  }
  if (ids.length === 0) {
    // Reset the announce baseline so arriving at a chat that already has runs reads them out fresh,
    // while the empty case stays silent.
    prevCount = 0;
    prevChatID = chatID;
    return;
  }

  // Announce only on the same chat, and only when the COUNT moves — a chat switch is not news, and
  // a step advancing inside a run the reader already knows about is not either.
  if (chatID === prevChatID && ids.length !== prevCount) {
    announce(
      ids.length === 1
        ? "1 workflow run in progress"
        : `${String(ids.length)} workflow runs in progress`,
    );
  }
  prevCount = ids.length;
  prevChatID = chatID;
}

/** The fold row: how many runs and how many need the reader, opening the list in place. */
function buildSummary(bar: HTMLUListElement): HTMLElement {
  const btn = selectableButton("div", "run-bar-open run-bar-toggle", () => {
    const open = !listOpen(rows(watchActiveId()).length);
    setRunBarOpen(open);
    // 0 is `.run-bar[data-folded]`'s `block-size` (26-dock.css).
    slideBox(bar, open, applyOpen(bar), { fade: true, closedHeight: 0 });
    render(bar);
  });
  btn.setAttribute("aria-expanded", "false");
  btn.append(
    el("span", { className: "run-bar-chev", "aria-hidden": "true" }, chevronEl()),
    el("span", { className: "run-bar-glyph", "aria-hidden": "true" }),
    el("span", { className: "run-bar-count" }),
  );
  return btn;
}

/** Whether the list shows its rows for `count` runs: always with no header, else as the reader last chose, else open
 *  below `FOLD_AT`. */
function listOpen(count: number): boolean {
  return count < HEADER_AT || (runBarOpen() ?? count < FOLD_AT);
}

/** The `apply` of the list's `slideBox`: folded is `.run-bar[data-folded]` (26-dock.css). */
function applyOpen(bar: HTMLUListElement): (open: boolean) => void {
  return (open) => {
    bar.toggleAttribute("data-folded", !open);
  };
}

/** Bring the list to `open` at once, unless the reader's slide is already heading there: it owns the attribute until
 *  it ends, so a repaint mid-close keeps the rows shown. An open whose rows came or went (`resized`) slides on to the
 *  new height; a slide heading the other way (the fold row left) is cut. */
function settleFold(bar: HTMLUListElement, open: boolean, resized: boolean): void {
  if (!isSliding(bar)) {
    applyOpen(bar)(open);
  } else if (targetOpen(bar, open) !== open) {
    slideBox(bar, open, applyOpen(bar), { motion: false });
  } else if (open && resized) {
    slideBox(bar, true, applyOpen(bar), { fade: true });
  }
}

function paintSummary(fold: HTMLElement, total: number, waiting: number, open: boolean): void {
  const btn = fold.querySelector<HTMLElement>(".run-bar-toggle");
  if (btn === null) {
    return;
  }
  const text =
    waiting === 0
      ? `${String(total)} workflows running`
      : `${String(total)} workflows, ${String(waiting)} ${waiting === 1 ? "needs" : "need"} you`;
  setText(btn.querySelector(".run-bar-count"), text);
  setAttr(btn, "aria-expanded", String(open));
  const glyph = btn.querySelector(".run-bar-glyph");
  if (glyph !== null) {
    setAttr(glyph, "data-status", waiting > 0 ? "input" : "working");
  }
}

/** One row: an `<li>` carrying the state, holding the control that opens the run's own tab. */
function buildRow(id: string, chatID: string): HTMLElement {
  // No attribute means reserved box, nothing to show.
  const glyph = el("span", { className: "run-bar-glyph", "aria-hidden": "true" });
  // The NAME is resolved at activation rather than captured here, because this row outlives its
  // first paint: a run whose label arrives with its first fetch would otherwise open a tab called
  // "Workflow run" for the rest of the session.
  const btn = selectableButton("div", "run-bar-open", () => {
    void openRunView(id, runLabelOf(id) || FALLBACK_NAME, chatID);
  });
  const summary = buildExecSummary({ alertHost: btn, figure: wholeCreditFigure });
  btn.append(glyph, summary.root);

  const row = el("li", { className: "run-bar-row" }, btn);
  const view = { btn, glyph, summary };
  rowViews.set(row, view);
  // Point this run's hold at the row ONCE, so the shared tick keeps the row's clock moving for as
  // long as the bar shows that run.
  const hold = holds.get(id);
  if (hold !== undefined) {
    hold.row = view;
  }
  paintRow(row, id);
  return row;
}

function paintRow(row: HTMLElement, id: string): void {
  const view = rowViews.get(row);
  if (view === undefined) {
    return;
  }
  const state = paintView(view, id);
  setAttr(row, "data-state", state);
}

/** Paint a row's mark and summary from the run's model, IN PLACE; answers the row's state. */
function paintView(view: RowView, id: string): ExecState | "unknown" {
  const run = execOf(id);
  if (run === undefined) {
    view.summary.paintPending(runLabelOf(id) || FALLBACK_NAME);
  } else {
    view.summary.paint(run);
  }
  // NOT `pending` for a run with no state, which reads "not started": a run in the live inventory
  // has demonstrably started.
  const state = run?.state ?? "unknown";
  const mark = runMarkStatus(state);
  // A run with no fetched state writes NO attribute, which is how the shared rule says "reserved
  // box, nothing to show".
  setAttr(view.glyph, "data-status", mark === "" ? null : mark);
  setAttr(view.btn, "aria-label", `Open workflow run ${view.summary.describe()}`);
  return state;
}

/** The workflow mark's own status for a row's state, or `""` for a row that has nothing to show
 *  yet. */
function runMarkStatus(state: ExecState | "unknown"): TabRunDotStatus | "" {
  switch (state) {
    case "running":
      return "working";
    case "waiting":
      return "waiting";
    case "input":
      return "input";
    case "unknown":
    case "pending":
    case "ok":
    case "fail":
    case "warn":
    case "skipped":
      return "";
  }
}

/** A run's last model and what it was folded from. The plan and the step ends are not inputs:
 *  the store replaces them only together with the state (`run-store.ts` `fetchRun`). */
interface HeldExec {
  readonly state: RunState;
  readonly asks: RunAsks;
  readonly exec: ExecRun;
  readonly memo: ExecFoldMemo;
  readonly gen: number;
}

const execs = new Map<string, HeldExec>();

/** The run tab's model of a run, which the card and the page read as well, folded again only when
 *  the run's state or asks moved. */
function execOf(id: string): ExecRun | undefined {
  const st = runState(id);
  if (st === undefined) {
    return undefined;
  }
  const asks = runPendingAsks(id);
  const held = execs.get(id);
  if (held?.state === st && sameRunAsks(held.asks, asks)) {
    return held.exec;
  }
  const memo = held?.memo ?? {};
  const exec = runToExec(st, runPlan(id), asks, "", runStepEnds(id), memo);
  execs.set(id, { state: st, asks, exec, memo, gen: (held?.gen ?? 0) + 1 });
  return exec;
}

/** Forget the models of the runs `chatID` no longer lists. */
function pruneExecs(chatID: string): void {
  const listed = new Set(liveRunIDsForChat(chatID));
  for (const id of execs.keys()) {
    if (!listed.has(id)) {
      execs.delete(id);
    }
  }
}

/** Join the shared 1s clock for every run on screen and leave it for every run that left. ONE
 *  holder per run for as long as the bar shows it, so the refcount in `messages-blocks.ts` is
 *  not churned by a repaint. */
function reconcileHolds(ids: readonly string[]): void {
  const wanted = new Set(ids);
  for (const [id, hold] of holds) {
    if (!wanted.has(id)) {
      releaseRunClock(id, hold);
      holds.delete(id);
    }
  }
  for (const id of ids) {
    const held = holds.has(id);
    if (!held) {
      const hold: Hold = {
        row: null,
        tick(): void {
          hold.row?.summary.tick();
        },
      };
      holds.set(id, hold);
      holdRunClock(id, hold);
    }
    // The bar's one fetch, and its only non-projection act: when it starts showing a run, and again
    // whenever a rendered run has NO state to read.
    if (!held || runState(id) === undefined) {
      invalidateRun(id);
    }
  }
}

/** Stop the render effect, release every hold and forget the module state. For tests only: all
 *  of it is module state, and the browser project's module registry is URL-keyed, so
 *  `vi.resetModules()` does not re-evaluate this file. Stopping the effect is the load-bearing
 *  half. */
// deadset:ignore DS1102 -- test seam: resets the render effect, run clock holds and previous counts
export function _resetRunBarForTest(): void {
  stopRender?.();
  stopRender = null;
  for (const [id, hold] of holds) {
    releaseRunClock(id, hold);
  }
  holds.clear();
  execs.clear();
  batchBtn?.remove();
  batchBtn = null;
  batchRun = null;
  bound = false;
  prevCount = 0;
  prevChatID = "";
}
