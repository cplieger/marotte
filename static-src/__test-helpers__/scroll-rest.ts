import { FRAME_BUDGET_MS } from "./frame-budget.js";

/** Consecutive frames `scrollTop` must hold: a correction lands at most one frame after the layout it answers. */
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

/** Frames until `el` sits on its live edge with `scrollTop` held for STILL_FRAMES. */
async function restOnEdge(el: HTMLElement, start: number): Promise<void> {
  let last = Number.NaN;
  let still = 0;
  while (still < STILL_FRAMES || el.scrollHeight - el.clientHeight - el.scrollTop > 1) {
    if (performance.now() - start > FRAME_BUDGET_MS) {
      throw new Error(`the scroller never came to rest on its live edge (${edge(el)})`);
    }
    await frame(el, start + FRAME_BUDGET_MS);
    const top = el.scrollTop;
    still = top === last ? still + 1 : 0;
    last = top;
  }
}

/** Runs `toEnd`, which sends `el` to its live edge, then resolves once `el` rests there while rows still taking their
 *  real height settle. Throws past FRAME_BUDGET_MS. */
export async function restAtLiveEdge(el: HTMLElement, toEnd: () => void): Promise<void> {
  toEnd();
  await restOnEdge(el, performance.now());
}
