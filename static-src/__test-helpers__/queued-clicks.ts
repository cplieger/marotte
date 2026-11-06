// The one owner of the `queueClicks` command's `BrowserCommands` augmentation (declared in
// vitest.config.ts), for `*.pointer.test.ts` suites that need a real click to wait in the input
// queue behind a long task.
import { server } from "vitest/browser";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    queueClicks(
      points: readonly ClickPoint[],
      gapMs: number,
      holdMs: number,
      frameWidth: number,
    ): Promise<void>;
  }
}

export interface ClickPoint {
  readonly x: number;
  readonly y: number;
}

/** Schedule real clicks at `points` (viewport CSS px), each pressed `gapMs` after the previous one
 *  was released and held `holdMs`, as a hand holds a button. Resolves once scheduled, so the caller
 *  can block its main thread meanwhile. */
export async function queueClicks(
  points: readonly ClickPoint[],
  gapMs: number,
  holdMs: number,
): Promise<void> {
  await server.commands.queueClicks(points, gapMs, holdMs, window.innerWidth);
}

/** The centre of `el`'s box, in viewport CSS px. */
export function centreOf(el: Element): ClickPoint {
  const r = el.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
}
