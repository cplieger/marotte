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
  runPlan,
  runState,
  runStepEnds,
  invalidateRun,
  type RunState,
} from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { runToExec } from "./run-exec-source.js";
import { counters, window as execWindow, type ExecNode } from "./exec-view/model.js";
import { holdRunClock, releaseRunClock, type RunClockHolder } from "./messages-blocks.js";
import { openRunView } from "./run-view.js";
import { STATE_WORD, stateOf, withAsk, type ExecState } from "./exec-view/status.js";
import type { TabRunDotStatus } from "./tabs.js";
import { formatElapsed } from "./strings.js";

/** NOT `stateOf(undefined)`, which is `pending` and reads "not started" — a run in the live
 *  inventory has demonstrably started, so that word would be false. */
const UNKNOWN_STATE = "unknown";

/** The label a run with no fetched state carries. The store's `runLabelOf` answers `""` there,
 *  and a row with no name at all is unclickable in practice. */
const FALLBACK_NAME = "Workflow run";

/** The element is re-pointed on every render rather than re-registered, so the refcount in
 *  `messages-blocks.ts` sees one holder per run for as long as the bar shows it. */
interface Hold extends RunClockHolder {
  clock: HTMLElement | null;
}

let bound = false;
let prevCount = 0;
let prevChatID = "";
const holds = new Map<string, Hold>();
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
    const st = runState(id);
    const asks = runPendingAsks(id);
    const c = counters(execNodes(id, st));
    parts.push(
      [
        id,
        st?.status ?? "",
        st?.runLabel ?? "",
        st?.workflowName ?? "",
        String(asks.count),
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

function render(bar: HTMLUListElement): void {
  const chatID = watchActiveId();
  const ids = rows(chatID);

  bar.classList.toggle("hidden", ids.length === 0);
  // BEFORE the rows, not after: `buildRow` points its clock span at this run's hold, so a hold
  // created afterwards would leave the FIRST render's clock unaddressable — which is every render
  // for a run whose state was already in the store when the bar started showing it, and the tick
  // would then never write to it.
  reconcileHolds(ids);
  // KEYED BY RUN ID, and each surviving row is PATCHED rather than rebuilt. Both halves are needed.
  reconcile(bar, ids, {
    key: (id: string) => id,
    mount: (id: string) => buildRow(id, chatID),
    update: (row: HTMLElement, id: string) => {
      paintRow(row, id);
    },
  });
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

/** One row: an `<li>` carrying the state, holding the `<button>` that opens the run's own tab. */
function buildRow(id: string, chatID: string): HTMLElement {
  // No attribute means reserved box, nothing to show.
  const glyph = el("span", { className: "run-bar-glyph", "aria-hidden": "true" });
  const clock = el("span", { className: "run-bar-clock" });

  const btn = el(
    "button",
    { type: "button", className: "run-bar-open" },
    glyph,
    el("span", { className: "run-bar-name" }),
    el("span", { className: "run-bar-state" }),
    el("span", { className: "run-bar-steps" }),
    clock,
  );
  // The NAME is resolved at click time rather than captured here, because this row outlives its
  // first paint now: a run whose label arrives with its first fetch would otherwise open a tab
  // called "Workflow run" for the rest of the session.
  btn.addEventListener("click", () => {
    void openRunView(id, runName(runState(id)), chatID);
  });

  const row = el("li", { className: "run-bar-row" }, btn);
  // Point this run's hold at the clock span ONCE. `paintRow` never replaces it, so the shared tick
  // keeps writing into the row on screen for as long as the bar shows that run.
  const hold = holds.get(id);
  if (hold !== undefined) {
    hold.clock = clock;
  }
  paintRow(row, id);
  return row;
}

/** Write one row's state into the shell `buildRow` made, IN PLACE. */
function paintRow(row: HTMLElement, id: string): void {
  const btn = row.querySelector<HTMLButtonElement>(":scope > .run-bar-open");
  if (btn === null) {
    return;
  }
  const st = runState(id);
  const state = execStateOf(st, runPendingAsks(id).count > 0);
  const name = runName(st);
  const word = state === UNKNOWN_STATE ? "" : STATE_WORD[state];
  const steps = stepText(id, st);

  row.dataset["state"] = state;
  const mark = runMarkStatus(state);
  const glyph = btn.querySelector<HTMLElement>(":scope > .run-bar-glyph");
  if (glyph !== null) {
    // A run with no fetched state writes NO attribute, which is how the shared rule says "reserved
    // box, nothing to show" — so leaving a stale one behind would claim a status this row cannot
    // vouch for.
    if (mark === "") {
      delete glyph.dataset["status"];
    } else {
      glyph.dataset["status"] = mark;
    }
  }
  setText(btn, ".run-bar-name", name);
  setText(btn, ".run-bar-state", word);
  setText(btn, ".run-bar-steps", steps);
  setText(btn, ".run-bar-clock", elapsedText(id, st));
  // The visible state is a glyph plus a word, both visual, so the name has to carry it too. The
  // step counter rides along because it is the row's other non-textual claim about progress.
  btn.setAttribute("aria-label", accessibleName(name, word, steps));
}

function setText(host: HTMLElement, selector: string, text: string): void {
  const span = host.querySelector<HTMLElement>(`:scope > ${selector}`);
  if (span !== null) {
    span.textContent = text;
  }
}

/** The workflow mark's own status for a row's state, or `""` for a row that has nothing to show
 *  yet. */
function runMarkStatus(state: ExecState | typeof UNKNOWN_STATE): TabRunDotStatus | "" {
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

/** Fold a run's status and its ask onto ONE axis, the way the card's STEP rows and the exec tree
 *  do. The card's ROOT keeps two axes because its rail and its ask paint at the same time; a
 *  single line has one slot. */
function execStateOf(st: RunState | undefined, asking: boolean): ExecState | typeof UNKNOWN_STATE {
  if (st === undefined) {
    return UNKNOWN_STATE;
  }
  return withAsk(stateOf(st.status), asking);
}

/** The run's name, RAW. It is also the run TAB's name and the button's accessible name, and
 *  `.run-bar-name` ellipsizes the visible span responsively — so a length cut here would travel
 *  into the tab strip and into what a screen reader reads. */
function runName(st: RunState | undefined): string {
  const label = st?.runLabel ?? "";
  const name = label === "" ? (st?.workflowName ?? "") : label;
  return name === "" ? FALLBACK_NAME : name;
}

/** The run tab's model of a run, which the card and the page count from as well. */
function execNodes(id: string, st: RunState | undefined): readonly ExecNode[] {
  return st === undefined
    ? []
    : runToExec(id, st, runPlan(id), runPendingAsks(id), "", runStepEnds(id)).nodes;
}

/** The step counter, in the card's own wording so the two surfaces agree. */
function stepText(id: string, st: RunState | undefined): string {
  const c = counters(execNodes(id, st));
  if (c.total === 0) {
    return "";
  }
  return c.current > 0
    ? `step ${String(c.current)} of ${String(c.total)}`
    : `${String(c.done)} of ${String(c.total)}`;
}

function elapsedText(id: string, st: RunState | undefined): string {
  const ms = execWindow(execNodes(id, st), runIsLive(st))?.span ?? 0;
  return ms > 0 ? formatElapsed(ms) : "";
}

function accessibleName(name: string, word: string, steps: string): string {
  const parts = [name];
  if (word !== "") {
    parts.push(word);
  }
  if (steps !== "") {
    parts.push(steps);
  }
  return `Open workflow run ${parts.join(", ")}`;
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
        clock: null,
        tick(): void {
          // A tracked read is inert here: a setInterval callback runs outside any effect's eval
          // context, so nothing subscribes.
          if (hold.clock !== null) {
            hold.clock.textContent = elapsedText(id, runState(id));
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
