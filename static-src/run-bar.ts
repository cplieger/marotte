// The run bar: one line per LIVE workflow run the active chat launched, in the bottom bar between
// the interaction dock and the steer stack.

import { el, computed, effect, touch, untracked } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { watchActiveId } from "./store.js";
import {
  liveRunIDsForChat,
  runIsLive,
  runLabelOf,
  runPlan,
  runState,
  runStepEnds,
  invalidateRun,
} from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { runToExec } from "./run-exec-source.js";
import { counters, type ExecRun } from "./exec-view/model.js";
import { buildExecSummary, type ExecSummaryView } from "./exec-view/summary.js";
import { wantsReader } from "./exec-view/alert.js";
import { chevronEl } from "./chevron.js";
import { runBarOpen, setRunBarOpen } from "./device-view.js";
import { holdRunClock, releaseRunClock, type RunClockHolder } from "./messages-blocks.js";
import { openRunView } from "./run-view.js";
import type { ExecState } from "./exec-view/status.js";
import type { TabRunDotStatus } from "./tabs.js";

/** The name a run with no fetched state carries: `runLabelOf` answers `""` there, and a row with
 *  no name at all is unclickable in practice. */
const FALLBACK_NAME = "Workflow run";

/** From this many live runs the bar folds them behind one summary row, so a fan-out cannot push
 *  the transcript and the composer off a phone. */
const FOLD_AT = 3;

/** The summary row's reconcile key; a run id is never empty, so it cannot collide. */
const SUMMARY_KEY = "";

/** One bar row's painted parts, kept so a repaint and the clock patch them in place. */
interface RowView {
  readonly btn: HTMLButtonElement;
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

/** Everything the bar's CONTENT depends on, as one string so the computed dedupes by value. */
function key(): string {
  const chatID = watchActiveId();
  const parts: string[] = [chatID];
  for (const id of liveRunIDsForChat(chatID)) {
    const run = execOf(id);
    const c = counters(run?.nodes ?? []);
    parts.push(
      [
        id,
        run?.state ?? "",
        run?.label ?? "",
        run?.alert?.kind ?? "",
        run?.alert?.text ?? "",
        String(c.total),
        String(c.done),
        String(c.current),
      ].join("\u0002"),
    );
  }
  return parts.join("\u0001");
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
  // Runs that need the reader first, each group in inventory order.
  const live = rows(chatID);
  const waiting = live.filter(needsYou);
  const ids = [...waiting, ...live.filter((id) => !waiting.includes(id))];
  const folds = ids.length >= FOLD_AT;
  const open = !folds || runBarOpen();

  bar.classList.toggle("hidden", ids.length === 0);
  // BEFORE the rows, not after: `buildRow` points this run's hold at its row, so a hold created
  // afterwards would leave the FIRST render's row unaddressable, and the tick would never paint it.
  reconcileHolds(ids);
  // The waiting-first order moves a row whenever another run starts needing the reader, and a
  // moved node loses focus, so the focused row button is handed it back.
  const focused = document.activeElement;
  const held = focused instanceof HTMLElement && bar.contains(focused) ? focused : null;
  // KEYED BY RUN ID, and each surviving row is PATCHED rather than rebuilt. Both halves are needed.
  reconcile(bar, folds ? [SUMMARY_KEY, ...ids] : ids, {
    key: (id: string) => id,
    mount: (id: string) => (id === SUMMARY_KEY ? buildSummary(bar) : buildRow(id, chatID)),
    update: (row: HTMLElement, id: string) => {
      if (id !== SUMMARY_KEY) {
        paintRow(row, id);
      }
    },
  });
  for (const row of bar.querySelectorAll<HTMLElement>(":scope > .run-bar-row")) {
    row.hidden = !open;
  }
  if (held !== null && held.isConnected && document.activeElement !== held) {
    held.focus({ preventScroll: true });
  }
  if (folds) {
    paintSummary(bar, ids.length, waiting.length, open);
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

/** The folded bar's one row: how many runs and how many need the reader, opening the list in place. */
function buildSummary(bar: HTMLUListElement): HTMLElement {
  const glyph = el("span", { className: "run-bar-glyph", "aria-hidden": "true" });
  const btn = el(
    "button",
    { type: "button", className: "run-bar-open run-bar-toggle", "aria-expanded": "false" },
    el("span", { className: "run-bar-chev", "aria-hidden": "true" }, chevronEl()),
    glyph,
    el("span", { className: "run-bar-count" }),
  );
  btn.addEventListener("click", () => {
    setRunBarOpen(!runBarOpen());
    render(bar);
  });
  return el("li", { className: "run-bar-summary" }, btn);
}

function paintSummary(bar: HTMLUListElement, total: number, waiting: number, open: boolean): void {
  const btn = bar.querySelector<HTMLButtonElement>(":scope > .run-bar-summary > .run-bar-toggle");
  if (btn === null) {
    return;
  }
  const text =
    waiting === 0
      ? `${String(total)} workflows running`
      : `${String(total)} workflows, ${String(waiting)} ${waiting === 1 ? "needs" : "need"} you`;
  const count = btn.querySelector<HTMLElement>(".run-bar-count");
  if (count !== null && count.textContent !== text) {
    count.textContent = text;
  }
  btn.setAttribute("aria-expanded", String(open));
  btn.toggleAttribute("data-alert", waiting > 0);
  if (waiting > 0) {
    btn.dataset["alert"] = "input";
  }
  const glyph = btn.querySelector<HTMLElement>(".run-bar-glyph");
  if (glyph !== null) {
    glyph.dataset["status"] = waiting > 0 ? "input" : "working";
  }
}

/** One row: an `<li>` carrying the state, holding the `<button>` that opens the run's own tab. */
function buildRow(id: string, chatID: string): HTMLElement {
  // No attribute means reserved box, nothing to show.
  const glyph = el("span", { className: "run-bar-glyph", "aria-hidden": "true" });
  const btn = el("button", { type: "button", className: "run-bar-open" }) as HTMLButtonElement;
  const summary = buildExecSummary({ washHost: btn, live: false });
  btn.append(glyph, summary.root);
  // The NAME is resolved at click time rather than captured here, because this row outlives its
  // first paint: a run whose label arrives with its first fetch would otherwise open a tab called
  // "Workflow run" for the rest of the session.
  btn.addEventListener("click", () => {
    void openRunView(id, runLabelOf(id) || FALLBACK_NAME, chatID);
  });

  const row = el("li", { className: "run-bar-row" }, btn);
  const view = { btn, glyph, summary };
  rowViews.set(row, view);
  // Point this run's hold at the row ONCE, so the shared tick keeps painting the row on screen for
  // as long as the bar shows that run.
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
  row.dataset["state"] = state;
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
  if (mark === "") {
    delete view.glyph.dataset["status"];
  } else {
    view.glyph.dataset["status"] = mark;
  }
  view.btn.setAttribute("aria-label", `Open workflow run ${view.summary.describe()}`);
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

/** The run tab's model of a run, which the card and the page read as well. */
function execOf(id: string): ExecRun | undefined {
  const st = runState(id);
  return st === undefined
    ? undefined
    : runToExec(id, st, runPlan(id), runPendingAsks(id), "", runStepEnds(id));
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
          // A tracked read is inert here: a setInterval callback runs outside any effect's eval
          // context, so nothing subscribes.
          if (hold.row !== null) {
            paintView(hold.row, id);
          }
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
// deadset:ignore DS1004 -- test seam: resets the render effect, run clock holds and previous counts
export function _resetRunBarForTest(): void {
  stopRender?.();
  stopRender = null;
  for (const [id, hold] of holds) {
    releaseRunClock(id, hold);
  }
  holds.clear();
  bound = false;
  prevCount = 0;
  prevChatID = "";
}
