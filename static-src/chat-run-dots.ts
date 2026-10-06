// The WORKFLOW mark on a chat's tab row: whether a run the chat launched is still going,
// from `run-store.ts`'s GLOBAL live-run inventory. N runs fold onto ONE mark (input >
// waiting > working); a settled run withdraws it.

import { effect } from "@cplieger/reactive";
import { hasTab, openChatRefs, setTabRunStatus, tabIdFor } from "./tabs.js";
import type { TabRunDotStatus, TabRunTally } from "./tabs.js";
import { runStatusFor, type RunPauseClass } from "./store.js";
import { runPendingAsks } from "./decision-dock.js";
import { isNeedInputPark, liveRunsForChat, peekLiveRun, runState } from "./run-store.js";

/** The fold's two halves: what the mark paints, and what its phrase says. */
interface RunFold {
  readonly status: TabRunDotStatus | "";
  readonly tally: TabRunTally;
}

/**
 * Fold this chat's live runs onto one mark with `run-dots.ts`'s per-run read; before a run's
 * cell resolves, `executing` answers (`false` contributes nothing).
 */
function foldRuns(chatID: string): RunFold {
  let working = 0;
  let waiting = 0;
  let input = 0;
  for (const row of liveRunsForChat(chatID)) {
    const asking = runPendingAsks(row.id).count > 0;
    const state = runState(row.id);
    const pause: RunPauseClass = isNeedInputPark(state) ? "need_input" : "";
    const rowStatus =
      state === undefined
        ? runStatusFor(row.executing ? "running" : undefined, asking, pause)
        : runStatusFor(state.status, asking, pause);
    switch (rowStatus) {
      case "input":
        input++;
        break;
      case "waiting":
        waiting++;
        break;
      case "working":
        working++;
        break;
      // Unreachable from a LIVE row: the mark withdraws when a run ends (`idle` keeps the switch
      // total).
      case "done":
      case "failed":
      case "idle":
      case "":
        break;
    }
  }
  const total = working + waiting + input;
  const status: TabRunDotStatus | "" =
    input > 0 ? "input" : waiting > 0 ? "waiting" : working > 0 ? "working" : "";
  return { status, tally: { total, working, waiting, input } };
}

function repaint(): void {
  for (const ref of openChatRefs()) {
    // Both walks share one synchronous pass, so the id resolves; unknown ids are no-ops anyway.
    const id = tabIdFor("chat", ref);
    const { status, tally } = foldRuns(ref);
    setTabRunStatus(id, status, tally);
  }
}

/**
 * Wire the effect from the composition root (at import the strip is not restored). ONE
 * effect: every input is a signal read inside its pass, and this module writes no signal.
 */
export function installChatRunDotSubscriber(): void {
  effect(() => {
    repaint();
  });
}

/**
 * Whether a chat row's fold still needs this run's state cell: the run is live and its
 * launching chat has an open tab. Registered as a run-state demand because the store cannot
 * know its readers. The row's OWN `chat`, since `runChatID` outlives a settle.
 */
export function chatTabFoldsRun(workflowID: string): boolean {
  const chat = peekLiveRun(workflowID)?.chat ?? "";
  return chat !== "" && hasTab("chat", chat);
}
