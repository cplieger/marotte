import { vi } from "vitest";

import { FRAME_BUDGET_MS } from "./frame-budget.js";

/** Consecutive frames `scrollTop` must hold: a pin's re-assert lands one frame after its write. */
const STILL_FRAMES = 3;

/** Each frame resumes on a task, where input arrives: a scroll written inside a frame callback lands
 *  before that frame's ResizeObserver delivery, which the controller reads as drift. Rejects at
 *  `deadline` (a `performance.now()` value) if no frame arrives. */
function frame(el: HTMLElement, deadline: number): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    let handle = 0;
    const bound = setTimeout(
      () => {
        cancelAnimationFrame(handle);
        reject(new Error(`no frame arrived within ${String(FRAME_BUDGET_MS)}ms (${edge(el)})`));
      },
      Math.max(0, deadline - performance.now()),
    );
    handle = requestAnimationFrame(() => {
      clearTimeout(bound);
      setTimeout(resolve, 0);
    });
  });
}

function edge(el: HTMLElement): string {
  return `scrollTop ${String(el.scrollTop)} of ${String(el.scrollHeight - el.clientHeight)}`;
}

/** Frames until `el` sits on its live edge with `scrollTop` held for STILL_FRAMES, all after `after`. */
async function restOnEdge(el: HTMLElement, after: number, start: number): Promise<void> {
  let last = Number.NaN;
  let still = 0;
  while (
    Date.now() <= after ||
    still < STILL_FRAMES ||
    el.scrollHeight - el.clientHeight - el.scrollTop > 1
  ) {
    if (performance.now() - start > FRAME_BUDGET_MS) {
      throw new Error(`the scroller never came to rest on its live edge (${edge(el)})`);
    }
    await frame(el, start + FRAME_BUDGET_MS);
    const top = el.scrollTop;
    still = top === last && Date.now() > after ? still + 1 : 0;
    last = top;
  }
}

/** Runs `pin`, which arms a pin window of `windowMs` and scrolls `el` to its live edge, then resolves
 *  once `el` has rested on that edge both inside the window and for STILL_FRAMES after it closed.
 *  The pin's clock is `Date.now()`, frozen until the first rest: the window cannot close while rows
 *  still take their real height, however slowly frames arrive. Throws past FRAME_BUDGET_MS, and
 *  at once under fake timers: it installs its own Date fake and removes every fake on return. */
export async function pinAtLiveEdge(
  el: HTMLElement,
  windowMs: number,
  pin: () => void,
): Promise<void> {
  if (vi.isFakeTimers()) {
    throw new Error("pinAtLiveEdge needs real timers: it installs and removes its own Date fake");
  }
  const start = performance.now();
  vi.useFakeTimers({ toFake: ["Date"] });
  let closes: number;
  try {
    closes = Date.now() + windowMs;
    pin();
    await restOnEdge(el, Number.NEGATIVE_INFINITY, start);
  } finally {
    vi.useRealTimers();
  }
  await restOnEdge(el, closes, start);
}
