import { FRAME_BUDGET_MS } from "./frame-budget.js";

// Installed on import and kept for the file's life: Browser Mode isolates per file. Under fake
// timers vitest swaps in its own `requestAnimationFrame`, so nothing queued there is counted.
const queued = new Set<number>();
const realRequest = window.requestAnimationFrame.bind(window);
const realCancel = window.cancelAnimationFrame.bind(window);
// Captured before any test fakes the clock, so the deadline holds under fake timers too.
const realSetTimeout = window.setTimeout.bind(window);
const realClearTimeout = window.clearTimeout.bind(window);
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

interface Step {
  readonly done: Promise<void>;
  cancel(): void;
}

/** A message task: a faked `setTimeout` never fires on its own. */
function task(): Step {
  const hop = new MessageChannel();
  const done = new Promise<void>((resolve) => {
    hop.port1.onmessage = (): void => {
      resolve();
    };
  });
  hop.port2.postMessage(null);
  return {
    done,
    cancel: (): void => {
      hop.port1.close();
      hop.port2.close();
    },
  };
}

/** A probe frame, not counted in `queued`. */
function frame(): Step {
  let handle = 0;
  const done = new Promise<void>((resolve) => {
    handle = realRequest(() => {
      resolve();
    });
  });
  return {
    done,
    cancel: (): void => {
      realCancel(handle);
    },
  };
}

/** Resolves on a task after a frame in which nothing queued a further frame callback, read after
 *  that frame's observer and scroll steps, so work a frame callback or a delivery in it queued is
 *  seen. The wait a negative reads after: every frame-scheduled write the page owed has run.
 *  Throws naming `what` past `timeout` ms, as a page that keeps queueing frames never drains,
 *  and a page whose frames are suspended never delivers one. */
export async function framesDrained(
  what: string,
  { timeout = FRAME_BUDGET_MS }: { timeout?: number } = {},
): Promise<void> {
  let late = false;
  let timer = 0;
  const expired = new Promise<false>((resolve) => {
    timer = realSetTimeout(() => {
      late = true;
      resolve(false);
    }, timeout);
  });
  const within = async (step: Step): Promise<boolean> => {
    try {
      return await Promise.race([step.done.then(() => true), expired]);
    } finally {
      step.cancel();
    }
  };
  try {
    if (await within(task())) {
      while ((await within(frame())) && (await within(task()))) {
        if (queued.size === 0 && !late) {
          return;
        }
      }
    }
  } finally {
    realClearTimeout(timer);
  }
  throw new Error(
    `gave up after ${String(timeout)}ms waiting for ${what}: ${String(queued.size)} frame callback(s) still queued`,
  );
}
