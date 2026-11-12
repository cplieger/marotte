// Which run's step content this page keeps: the run its visible run tab draws, and that tab's shown
// step. `run-view.ts` writes it; the SSE adapter drops every other run's content frames and asks
// the stream for the run's topic, and the run handlers route the scoped run's frames between the
// shown step's log and the dock's tail cells.

/** What asking the stream for a run's topic came to: `owed` when the ask was lost and must be made
 *  again. */
export type ScopeOutcome = "covered" | "owed";

type Presenter = (run: string) => Promise<ScopeOutcome>;

/** A page with no stream receives no frame to miss, so its reads need not wait. */
let present: Presenter = () => Promise.resolve("covered");

/** One run's stay in the scope: `ready` settles true once the stream delivers its topic, false
 *  when the run leaves first. */
interface Cover {
  readonly run: string;
  readonly ready: Promise<boolean>;
  readonly settle: (covered: boolean) => void;
  owed: boolean;
}

let scope: { readonly run: string; readonly step: string } = { run: "", step: "" };
let cover: Cover = newCover("");
cover.settle(true);

function newCover(run: string): Cover {
  let settle: (covered: boolean) => void = () => undefined;
  const ready = new Promise<boolean>((resolve) => {
    settle = resolve;
  });
  return { run, ready, settle, owed: false };
}

function ask(c: Cover): void {
  void present(c.run).then((outcome) => {
    if (cover !== c) {
      return;
    }
    if (outcome === "owed") {
      c.owed = true;
      return;
    }
    c.settle(true);
  });
}

/** The SSE adapter's `setPageScope`, which asks the stream to deliver a run's topic. */
export function setScopePresenter(p: Presenter): void {
  present = p;
}

/** Point the scope at `run` ("" for none) and its shown step ("" for none), asking the stream for
 *  the run's topic when the run changes. Answers the run scoped before, so the caller can tell a
 *  run entering or leaving the scope from a step change. */
export function setRunContentScope(run: string, step: string): string {
  const before = scope.run;
  scope = { run, step: run === "" ? "" : step };
  if (before !== run) {
    cover.settle(false);
    cover = newCover(run);
    ask(cover);
  }
  return before;
}

/** Whether this page keeps `workflowID`'s step content. */
export function runContentScoped(workflowID: string): boolean {
  return workflowID !== "" && scope.run === workflowID;
}

/** The shown step's path while `workflowID` is scoped, "" otherwise. */
export function scopedStep(workflowID: string): string {
  return runContentScoped(workflowID) ? scope.step : "";
}

/** Settles true once the stream delivers `workflowID`'s topic, so a read started after it misses
 *  no frame; false when the run is not scoped or leaves before that. An ask lost with the link
 *  keeps it waiting until `rescopeOwed` asks again. */
export function scopeReady(workflowID: string): Promise<boolean> {
  return runContentScoped(workflowID) ? cover.ready : Promise.resolve(false);
}

/** Ask again for the scoped run's topic when the last ask was lost. The adapter calls it each time
 *  the stream opens, which is what keeps a lost ask from being retried in a loop. */
export function rescopeOwed(): void {
  if (cover.owed) {
    cover.owed = false;
    ask(cover);
  }
}
