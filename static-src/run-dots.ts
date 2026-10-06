// The activity dot and the NAME of a workflow run's tab row: one effect writes both, because a
// `tabs_changed` frame carries no label. The dot is the harder half.

import { effect, signal, touch } from "@cplieger/reactive";
import { openRunRefs, renameTab, setTabStatus, tabIdFor, tabSetVersion } from "./tabs.js";
import { runStatusFor, type RunPauseClass } from "./store.js";
import { runPendingAsks } from "./decision-dock.js";
import {
  invalidateRun,
  isNeedInputPark,
  registerLiveRunObserver,
  runLabelOf,
  runState,
} from "./run-store.js";

/** The runs this client has seen an event for, and the version counter that makes the effect
 *  depend on the set. */
const tracked = new Set<string>();
const version = signal(0);

/** Start painting a run's tab. Called on every run event, for every run: the origin no longer
 *  decides, because a chat's own dot cannot cover a run that outlives its turn (see the header). */
export function trackRun(workflowID: string): void {
  if (workflowID === "" || tracked.has(workflowID)) {
    return;
  }
  tracked.add(workflowID);
  bump();
}

/** Nudge the dot without a new run event. Called by the run view after it reads `GET
 *  /api/runs/{id}`, which is what paints a tab restored on boot or opened from History onto a
 *  run already going — a PAUSED run emits no frames at all, so nothing else would. */
export function refreshRunDots(): void {
  bump();
}

/** Advance the counter without READING it as a dependency. `peek` is load-bearing: both writers
 *  above are reachable from inside an effect (`run-view.ts`'s paint calls them), and a plain
 *  `version.value + 1` subscribes the CALLING effect to the signal it is about to write — a
 *  self-cycle the reactive layer refuses with `Cycle detected` on every paint of a run view. */
function bump(): void {
  version.value = version.peek() + 1;
}

/** Fetch run state for every OPEN run tab this client has heard nothing about — the door a cold
 *  load reaches, since a restored subject names the run and says nothing about it while
 *  `/api/runs/live` names only the runs still going. */
function seedOpenRunTabs(): void {
  for (const ref of openRunRefs()) {
    if (ref === "" || tracked.has(ref)) {
      continue;
    }
    // Neither call alone: repaint never visits an untracked run, and a tracked run with no fetch
    // behind it never gets state.
    trackRun(ref);
    invalidateRun(ref);
  }
}

function repaint(): void {
  for (const workflowID of tracked) {
    // The TAB id, resolved from the subject, and the DOCK key `run:<workflowId>` below are two
    // different strings now. The dock's is a synthetic chat id it files a parentless run's asks
    // under; the tab's is opaque and server-minted.
    const id = tabIdFor("run", workflowID);
    if (id === "") {
      // KEEP the id: this effect depends on the tab set's version, so the dot paints the moment the
      // row lands, and there is deliberately no sweep to race it.
      continue;
    }
    // The SAME join the other two run surfaces make (the transcript's card and the exec page both
    // take `runPendingAsks`), rather than two `hasPendingDecision` reads.
    const asking = runPendingAsks(workflowID).count > 0;
    // TRACKED read, deliberately: the run's cell resolving (the fetch an invalidation coalesces
    // into) is exactly the moment the dot must repaint, and `run_progress` for an already-tracked
    // run bumps nothing else here.
    const state = runState(workflowID);
    // A park on a PERSON is the yellow dot even with no card in the dock: a client that connected
    // after the ask was raised holds nothing to join, and the park is the only thing left that says
    // a human is owed an answer.
    const pause: RunPauseClass = isNeedInputPark(state) ? "need_input" : "";
    setTabStatus(id, runStatusFor(state?.status, asking, pause));
    // The name, from the same read. `""` means nothing has been fetched for the run yet, so the row
    // keeps the factory's placeholder.
    const label = runLabelOf(workflowID);
    if (label !== "") {
      renameTab(id, label);
    }
  }
}

/** Wire the effect. Called from the composition root, not at import: an effect running at module
 *  load would paint against a tab strip that has not been restored yet, and `setTabStatus` parks
 *  its state on a spec that does not exist then. The tab-set dependency is what picks a tab up
 *  once it does. */
export function installRunDotSubscriber(): void {
  // Registered from here rather than from the composition root because this module imports the
  // store, and the store may not import this one.
  registerLiveRunObserver(trackRun);
  // A SECOND effect, because the seed writes the counter the paint effect below reads: one effect
  // doing both would write a signal it has subscribed to.
  effect(() => {
    seedOpenRunTabs();
  });
  effect(() => {
    touch(version);
    // Subscribes to the tab SET as well, so a run tab arriving after its run's frames (the open_tab
    // round trip) or restored on boot paints without a fresh run event.
    void tabSetVersion();
    // Subscribes to the dock queue too: runPendingAsks reads queueVersion, so an ask arriving for a
    // background run repaints its dot with no run event. And to every tracked run's own cell,
    // through repaint's runState reads.
    repaint();
  });
}
