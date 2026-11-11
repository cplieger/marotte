import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { skeletonTiming } from "./skeleton.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("skeletonTiming", () => {
  it("a fast load never paints the skeleton; commit renders immediately", () => {
    const show = vi.fn();
    const render = vi.fn();
    const t = skeletonTiming(show, { showDelayMs: 150 });
    vi.advanceTimersByTime(100);
    t.commit(render);
    expect(show).not.toHaveBeenCalled();
    expect(render).toHaveBeenCalledTimes(1);
    // The pending show timer is cleared: it must not paint later.
    vi.advanceTimersByTime(1000);
    expect(show).not.toHaveBeenCalled();
  });

  it("paints the skeleton after the show delay; commit past min-visible renders at once", () => {
    const show = vi.fn();
    const render = vi.fn();
    const t = skeletonTiming(show, { showDelayMs: 150, minVisibleMs: 300 });
    vi.advanceTimersByTime(150);
    expect(show).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(400); // shown for 400ms >= 300ms
    t.commit(render);
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("defers commit until min-visible has elapsed", () => {
    const render = vi.fn();
    const t = skeletonTiming(vi.fn(), { showDelayMs: 150, minVisibleMs: 300 });
    vi.advanceTimersByTime(200); // skeleton visible for 50ms
    t.commit(render);
    expect(render).not.toHaveBeenCalled();
    vi.advanceTimersByTime(249);
    expect(render).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("min-visible defaults to 0: commit renders as soon as the load settles", () => {
    const render = vi.fn();
    const t = skeletonTiming(vi.fn());
    vi.advanceTimersByTime(150);
    t.commit(render);
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("defaults the show delay to 150ms", () => {
    // The anti-flicker premise: a load settling inside this window never paints.
    const show = vi.fn();
    skeletonTiming(show);
    vi.advanceTimersByTime(149);
    expect(show).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("runs the show teardown before the committed render", () => {
    const order: string[] = [];
    const t = skeletonTiming(() => {
      order.push("show");
      return () => order.push("teardown");
    });
    vi.advanceTimersByTime(150);
    t.commit(() => order.push("render"));
    expect(order).toEqual(["show", "teardown", "render"]);
  });

  it("cancel before the delay suppresses the skeleton entirely", () => {
    const show = vi.fn();
    const t = skeletonTiming(show);
    t.cancel();
    vi.advanceTimersByTime(1000);
    expect(show).not.toHaveBeenCalled();
  });

  it("cancel after paint runs the teardown exactly once (idempotent)", () => {
    const teardown = vi.fn();
    const t = skeletonTiming(() => teardown);
    vi.advanceTimersByTime(150);
    t.cancel();
    t.cancel();
    expect(teardown).toHaveBeenCalledTimes(1);
  });

  it("cancel drops a commit render still deferred by min-visible", () => {
    const teardown = vi.fn();
    const render = vi.fn();
    const t = skeletonTiming(() => teardown, { minVisibleMs: 300 });
    vi.advanceTimersByTime(150);
    t.commit(render); // deferred ~300ms
    t.cancel();
    vi.advanceTimersByTime(1000);
    expect(render).not.toHaveBeenCalled();
    expect(teardown).toHaveBeenCalledTimes(1);
  });

  it("commit after cancel is a no-op", () => {
    const render = vi.fn();
    const t = skeletonTiming(vi.fn());
    t.cancel();
    t.commit(render);
    vi.advanceTimersByTime(1000);
    expect(render).not.toHaveBeenCalled();
  });

  it("a second commit is a no-op", () => {
    const first = vi.fn();
    const second = vi.fn();
    const t = skeletonTiming(vi.fn());
    t.commit(first);
    t.commit(second);
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).not.toHaveBeenCalled();
  });

  it("an aborted signal suppresses an un-painted skeleton; commit still renders", () => {
    const ctrl = new AbortController();
    const show = vi.fn();
    const render = vi.fn();
    const t = skeletonTiming(show, { signal: ctrl.signal });
    ctrl.abort();
    vi.advanceTimersByTime(150);
    expect(show).not.toHaveBeenCalled();
    t.commit(render);
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("the signal does not retract an already-painted skeleton", () => {
    const ctrl = new AbortController();
    const teardown = vi.fn();
    skeletonTiming(() => teardown, { signal: ctrl.signal });
    vi.advanceTimersByTime(150);
    ctrl.abort();
    expect(teardown).not.toHaveBeenCalled();
  });
});
