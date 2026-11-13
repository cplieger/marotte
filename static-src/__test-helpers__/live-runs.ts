// The lease store's side of the live-runs inventory: its `live_run` frames, stamped from one `runs`
// counter per test file. The inventory is module state that outlives a case, so every frame and
// every stamped read in a file takes its version from `nextRunsVersion`, or the mirror refuses it
// as older than what it holds.

import { applyLiveRunFrame } from "../run-store.js";
import type { SubjectStamp } from "../wire/types.gen.js";

let runsVersion = 0;

/** The next `runs` version the server would mint. */
export function nextRunsVersion(): string {
  runsVersion++;
  return String(runsVersion);
}

/** A `runs` stamp at the next version, for an inventory read's reply. */
export function runsStamp(): SubjectStamp {
  return { kind: "runs", ref: "", version: nextRunsVersion() };
}

/** The server announced this run live (or moved its row). */
export function frameRunLive(workflowID: string, chatID: string, executing = true): void {
  applyLiveRunFrame(
    { workflow_id: workflowID, chat_id: chatID, live: true, executing },
    runsStamp(),
  );
}

/** The server released this run's lease. */
export function frameRunGone(workflowID: string): void {
  applyLiveRunFrame(
    { workflow_id: workflowID, chat_id: "", live: false, executing: false },
    runsStamp(),
  );
}
