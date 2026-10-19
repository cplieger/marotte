import { FRAME_BUDGET_MS } from "./frame-budget.js";

/** The next frame, or the next task where the runtime has no frames (the node project). Rejects
 *  a second past `deadline` (a `performance.now()` value), so a frame that never comes still names
 *  `what` while a late one leaves the verdict to the caller's own deadline check. */
function nextPoll(deadline: number, what: () => string): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const scope = globalThis as { readonly requestAnimationFrame?: typeof requestAnimationFrame };
    if (scope.requestAnimationFrame === undefined) {
      setTimeout(resolve, 0);
      return;
    }
    let handle = 0;
    const bound = setTimeout(
      () => {
        cancelAnimationFrame(handle);
        reject(new Error(`no frame arrived while waiting for ${what()}`));
      },
      Math.max(0, deadline - performance.now()) + 1_000,
    );
    handle = scope.requestAnimationFrame(() => {
      clearTimeout(bound);
      resolve();
    });
  });
}

/** Resolves once `cond` holds, checked now and then once a frame. Throws naming `what` (and
 *  `detail()`, when given) once `timeout` ms have passed without it. */
export async function until(
  cond: () => boolean,
  what: string,
  { timeout = FRAME_BUDGET_MS, detail }: { timeout?: number; detail?: () => string } = {},
): Promise<void> {
  const deadline = performance.now() + timeout;
  const describe = (): string => (detail === undefined ? what : `${what} (${detail()})`);
  while (!cond()) {
    if (performance.now() >= deadline) {
      throw new Error(`gave up after ${String(timeout)}ms waiting for ${describe()}`);
    }
    await nextPoll(deadline, describe);
  }
}
