// The task `afterNextPaint` posts must not wait behind a `scheduler.yield` chain, which a cold body
// drain runs: Chromium runs every yield continuation ahead of an ordinary task.

import { describe, it, expect } from "vitest";
import { afterNextPaint } from "./next-paint.js";

const BATCHES = 20;
const BATCH_MS = 30;

function busy(ms: number): void {
  const end = performance.now() + ms;
  while (performance.now() < end) {
    // A build batch standing in for a drain's.
  }
}

describe("afterNextPaint", () => {
  it("runs before a scheduler.yield chain of busy batches ends", async () => {
    const sched = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
    expect(sched?.yield, "the premise: the engine has scheduler.yield").toBeTypeOf("function");
    const yieldNow = (): Promise<void> => sched?.yield?.call(sched) ?? Promise.resolve();
    let batches = 0;
    let ranAfter: number | undefined;
    const chain = (async () => {
      for (let i = 0; i < BATCHES; i++) {
        await yieldNow();
        busy(BATCH_MS);
        batches++;
      }
    })();
    afterNextPaint(() => {
      ranAfter = batches;
    });
    await chain;
    await new Promise((r) => setTimeout(r, 0));
    expect(ranAfter, "the task never ran").toBeDefined();
    expect(ranAfter, "the task waited for the whole chain").toBeLessThan(BATCHES);
  });

  it("runs on a page whose rendering is idle", async () => {
    let ran = false;
    afterNextPaint(() => {
      ran = true;
    });
    expect(ran, "ran in the caller's task").toBe(false);
    await new Promise((r) => requestAnimationFrame(r));
    await new Promise((r) => setTimeout(r, 0));
    expect(ran).toBe(true);
  });
});
