import { FRAME_BUDGET_MS } from "./frame-budget.js";

// Installed on import and kept for the file's life: Browser Mode isolates per file. Under fake
// timers vitest swaps in its own `requestAnimationFrame`, so nothing queued there is counted.
const queued = new Set<number>();
const realRequest = window.requestAnimationFrame.bind(window);
const realCancel = window.cancelAnimationFrame.bind(window);
window.requestAnimationFrame = (cb: FrameRequestCallback): number => {
  const handle = realRequest((t) => {
    queued.delete(handle);
    cb(t);
  });
  queued.add(handle);
  return handle;
};
window.cancelAnimationFrame = (handle: number): void => {
  queued.delete(handle);
  realCancel(handle);
};

/** A message task: a faked `setTimeout` never fires on its own. */
function task(): Promise<void> {
  return new Promise<void>((resolve) => {
    const hop = new MessageChannel();
    hop.port1.onmessage = (): void => {
      hop.port1.close();
      resolve();
    };
    hop.port2.postMessage(null);
  });
}

/** Resolves on a task after a frame in which nothing queued a further frame callback, read after
 *  that frame's observer and scroll steps, so work a frame callback or a delivery in it queued is
 *  seen. The wait a negative reads after: every frame-scheduled write the page owed has run.
 *  Throws naming `what` past `timeout` ms, as a page that keeps queueing frames never drains. */
export async function framesDrained(
  what: string,
  { timeout = FRAME_BUDGET_MS }: { timeout?: number } = {},
): Promise<void> {
  const deadline = performance.now() + timeout;
  await task();
  for (;;) {
    await new Promise<void>((resolve) => {
      realRequest(() => {
        resolve();
      });
    });
    await task();
    if (queued.size === 0) {
      return;
    }
    if (performance.now() >= deadline) {
      throw new Error(
        `gave up after ${String(timeout)}ms waiting for ${what}: ${String(queued.size)} frame callback(s) still queued`,
      );
    }
  }
}
