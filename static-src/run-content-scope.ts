// Which run's step content this page keeps: the run its visible run tab draws, and that tab's shown
// step. `run-view.ts` writes it; the SSE adapter drops every other run's content frames, and the run
// handlers route the scoped run's frames between the shown step's log and the dock's tail cells.

let scope: { readonly run: string; readonly step: string } = { run: "", step: "" };

/** Point the scope at `run` ("" for none) and its shown step ("" for none). Answers the run scoped
 *  before, so the caller can tell a run entering or leaving the scope from a step change. */
export function setRunContentScope(run: string, step: string): string {
  const before = scope.run;
  scope = { run, step: run === "" ? "" : step };
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
