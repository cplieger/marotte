import { describe, it, expect, vi, afterEach } from "vitest";
import fc from "fast-check";

import {
  ToastEngine,
  type ToastCallbacks,
  type ToastRenderData,
  type ToastView,
} from "./engine.js";

interface FakeToast {
  data: ToastRenderData;
  ctx: ToastCallbacks;
  left: boolean;
  removed: boolean;
  paused: boolean;
  /** Off screen: its leave finished or the engine removed it outright. */
  gone: boolean;
  /** How many times the engine asked the view to run this toast's leave. */
  leaves: number;
  /** How many times the engine asked the view to resume this toast's progress. */
  resumes: number;
  done: (() => void) | null;
}

function makeFakeView(autoLeave = true): { view: ToastView<FakeToast>; mounts: FakeToast[] } {
  const mounts: FakeToast[] = [];
  const view: ToastView<FakeToast> = {
    mount(data, ctx) {
      const handle: FakeToast = {
        data,
        ctx,
        left: false,
        removed: false,
        paused: false,
        gone: false,
        leaves: 0,
        resumes: 0,
        done: null,
      };
      mounts.push(handle);
      return handle;
    },
    scheduleLeave(handle, done) {
      handle.left = true;
      handle.leaves++;
      const finish = (): void => {
        handle.gone = true;
        done();
      };
      if (autoLeave) {
        finish();
      } else {
        handle.done = finish;
      }
    },
    remove(handle) {
      handle.removed = true;
      handle.gone = true;
    },
    pauseProgress(handle) {
      handle.paused = true;
    },
    resumeProgress(handle) {
      handle.paused = false;
      handle.resumes++;
    },
    dispose() {
      /* no-op */
    },
  };
  return { view, mounts };
}

function shown(mounts: readonly FakeToast[]): number {
  return mounts.filter((m) => !m.gone).length;
}

/** Dismisses every toast on screen, newest first, finishing each leave, and
 *  returns the messages of the queued toasts promoted along the way. The queue
 *  is observable only through promotion, so this is how a test reads it. */
function drain(mounts: FakeToast[]): string[] {
  const before = mounts.length;
  for (
    let m = mounts.findLast((t) => !t.gone);
    m !== undefined;
    m = mounts.findLast((t) => !t.gone)
  ) {
    m.ctx.dismiss();
    m.done?.();
    // A dismiss that takes nothing off screen would otherwise loop forever.
    expect(m.gone).toBe(true);
  }
  return mounts.slice(before).map((t) => t.data.message);
}

describe("ToastEngine", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("uses per-level default durations (info/success = default, error = sticky 0)", () => {
    const { view, mounts } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({ view, defaultDuration: 1234 });
    engine.show("a", { level: "info" });
    engine.show("b", { level: "success" });
    engine.show("c", { level: "error" });
    expect(mounts[0]!.data.duration).toBe(1234);
    expect(mounts[1]!.data.duration).toBe(1234);
    expect(mounts[2]!.data.duration).toBe(0);
  });

  it("honors an explicit duration override", () => {
    const { view, mounts } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({ view, defaultDuration: 4000 });
    engine.show("x", { level: "info", duration: 500 });
    expect(mounts[0]!.data.duration).toBe(500);
  });

  it("promotes from the queue when a visible slot frees up", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 2, defaultDuration: 0 });
    const dismiss1 = engine.show("1");
    engine.show("2");
    engine.show("3");
    expect(shown(mounts)).toBe(2);
    expect(mounts).toHaveLength(2);

    dismiss1();
    expect(shown(mounts)).toBe(2);
    expect(mounts).toHaveLength(3);
    expect(mounts[2]!.data.message).toBe("3");
  });

  it("caps the queue at maxQueue, dropping the oldest queued toast", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({
      view,
      maxVisible: 1,
      maxQueue: 2,
      defaultDuration: 0,
    });
    engine.show("visible");
    engine.show("q1");
    engine.show("q2");
    engine.show("q3");
    expect(shown(mounts)).toBe(1);
    expect(drain(mounts)).toEqual(["q2", "q3"]);
  });

  it("a dropped queued toast's dismiss function is a no-op", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({
      view,
      maxVisible: 1,
      maxQueue: 1,
      defaultDuration: 0,
    });
    engine.show("visible");
    const dropped = engine.show("will-queue");
    engine.show("evicts-the-previous");
    expect(() => {
      dropped();
    }).not.toThrow();
    expect(drain(mounts)).toEqual(["evicts-the-previous"]);
  });

  it("pauses and resumes the dismiss timer with correct remaining-time math", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 1000 });
    engine.show("t", { level: "info", duration: 1000 });
    expect(shown(mounts)).toBe(1);

    vi.advanceTimersByTime(400);
    mounts[0]!.ctx.pause();
    expect(mounts[0]!.paused).toBe(true);

    vi.advanceTimersByTime(5000); // paused: must not dismiss
    expect(shown(mounts)).toBe(1);

    mounts[0]!.ctx.resume();
    expect(mounts[0]!.paused).toBe(false);

    vi.advanceTimersByTime(599);
    expect(shown(mounts)).toBe(1);
    vi.advanceTimersByTime(2);
    expect(shown(mounts)).toBe(0);
  });

  it("auto-dismisses a timed toast after its duration", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 3, defaultDuration: 4000 });
    engine.show("hi", { level: "info" });
    expect(shown(mounts)).toBe(1);
    vi.advanceTimersByTime(3999);
    expect(shown(mounts)).toBe(1);
    vi.advanceTimersByTime(1);
    expect(shown(mounts)).toBe(0);
  });

  it("sticky toasts (duration 0) never auto-dismiss", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 3 });
    engine.show("e", { level: "error" });
    vi.advanceTimersByTime(1_000_000);
    expect(shown(mounts)).toBe(1);
  });

  it("clear() removes all visible toasts and empties the queue", () => {
    const { view, mounts } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 2, defaultDuration: 0 });
    engine.show("1");
    engine.show("2");
    engine.show("3");
    engine.clear();
    expect(mounts[0]!.removed).toBe(true);
    expect(mounts[1]!.removed).toBe(true);

    engine.show("after");
    expect(mounts).toHaveLength(3);
    // A toast left in the queue would be promoted once this one leaves.
    expect(drain(mounts)).toEqual([]);
  });

  it("dismissNewest() dismisses the most recently shown visible toast", () => {
    const { view, mounts } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 3, defaultDuration: 0 });
    engine.show("old");
    engine.show("new");
    engine.dismissNewest();
    expect(mounts[0]!.left).toBe(false);
    expect(mounts[1]!.left).toBe(true);
  });

  it("property: visible never exceeds maxVisible and queue never exceeds maxQueue", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 5 }),
        fc.integer({ min: 0, max: 5 }),
        fc.array(fc.boolean(), { minLength: 0, maxLength: 60 }),
        (maxVisible, maxQueue, ops) => {
          const { view, mounts } = makeFakeView(true);
          const engine = new ToastEngine<FakeToast>({
            view,
            maxVisible,
            maxQueue,
            defaultDuration: 0,
          });
          const dismissers: (() => void)[] = [];
          for (const isShow of ops) {
            if (isShow) {
              dismissers.push(engine.show("m", { level: "error" }));
            } else {
              const dismiss = dismissers.shift();
              if (dismiss) {
                dismiss();
              }
            }
            expect(shown(mounts)).toBeLessThanOrEqual(maxVisible);
          }
          expect(drain(mounts).length).toBeLessThanOrEqual(maxQueue);
        },
      ),
    );
  });
});

describe("ToastEngine: mode replace (single-slot latest-wins)", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("a new toast instantly removes the visible one — remove, not a leave; nothing queues", () => {
    const { view, mounts } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({ view, mode: "replace", defaultDuration: 0 });

    engine.show("first");
    engine.show("second");
    expect(mounts).toHaveLength(2);
    expect(mounts[0]?.removed).toBe(true);
    expect(mounts[0]?.left).toBe(false);
    expect(mounts[1]?.removed).toBe(false);
    expect(drain(mounts)).toEqual([]);
  });

  it("ignores maxVisible (single slot) and cancels the replaced toast's timer", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({
      view,
      mode: "replace",
      maxVisible: 5,
      defaultDuration: 1000,
    });

    engine.show("a");
    engine.show("b");
    expect(shown(mounts)).toBe(1);

    // Only b's timer may fire; a's was cancelled with its removal.
    vi.advanceTimersByTime(1000);
    expect(mounts[0]?.left).toBe(false);
    expect(mounts[1]?.left).toBe(true);
  });

  it("a replaced toast's dismiss fn is a safe no-op", () => {
    const { view, mounts } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({ view, mode: "replace", defaultDuration: 0 });
    const dismissFirst = engine.show("first");
    engine.show("second");
    dismissFirst();
    expect(shown(mounts)).toBe(1);
    expect(mounts[1]?.left).toBe(false);
    expect(mounts[1]?.removed).toBe(false);
  });
});

describe("ToastEngine: dismissing a queued toast", () => {
  it("a queued toast keeps its retry action when it is promoted", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 0 });
    const onClick = vi.fn();
    const dismissVisible = engine.show("visible");
    engine.show("queued", { level: "error", retry: { label: "Try again", onClick } });
    expect(mounts).toHaveLength(1);

    dismissVisible();
    expect(mounts[1]!.data.message).toBe("queued");
    expect(mounts[1]!.data.retry?.label).toBe("Try again");
  });

  it("dismissing a still-queued toast drops it from the queue so it never mounts", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 0 });
    const dismissVisible = engine.show("visible");
    const dismissQueued = engine.show("queued");

    dismissQueued();

    // The freed slot must stay empty: the queued toast is gone, not deferred.
    dismissVisible();
    expect(mounts).toHaveLength(1);
  });

  it("the dismiss fn of a queued toast still dismisses it after promotion", () => {
    const { view, mounts } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 0 });
    const dismissVisible = engine.show("visible");
    const dismissQueued = engine.show("queued");

    dismissVisible();
    mounts[0]!.done?.();
    expect(mounts[1]!.data.message).toBe("queued");

    dismissQueued();
    expect(mounts[1]!.left).toBe(true);
  });
});

describe("ToastEngine: per-toast targeting", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("dismissing a middle toast removes that toast and frees exactly one slot", () => {
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 3, defaultDuration: 0 });
    engine.show("a");
    const dismissB = engine.show("b");
    engine.show("c");

    dismissB();
    expect(shown(mounts)).toBe(2);
    expect(mounts[1]!.left).toBe(true);
    expect(mounts[2]!.left).toBe(false);
  });

  it("pause acts on the toast whose callback ran, not the first visible one", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 2, defaultDuration: 1000 });
    engine.show("first");
    engine.show("second");

    mounts[1]!.ctx.pause();
    expect(mounts[1]!.paused).toBe(true);
    expect(mounts[0]!.paused).toBe(false);

    // Only the first toast's timer is still running.
    vi.advanceTimersByTime(1000);
    expect(mounts[0]!.left).toBe(true);
    expect(mounts[1]!.left).toBe(false);
  });

  it("a resume without a preceding pause leaves the progress animation alone", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 1000 });
    engine.show("t");

    vi.advanceTimersByTime(600);
    mounts[0]!.ctx.resume(); // never paused: nothing to resume
    expect(mounts[0]!.resumes).toBe(0);

    vi.advanceTimersByTime(400);
    expect(shown(mounts)).toBe(0);
  });

  it("dismissing the same toast twice runs the leave lifecycle once", () => {
    const { view, mounts } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 0 });
    const dismiss = engine.show("t");

    dismiss();
    dismiss();
    expect(mounts[0]!.leaves).toBe(1);
  });

  it("a view that finishes a leave twice does not evict another visible toast", () => {
    const { view, mounts } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 2, defaultDuration: 0 });
    const dismissA = engine.show("a");
    engine.show("b");

    dismissA();
    const finish = mounts[0]!.done;
    expect(finish).not.toBeNull();
    finish?.();
    finish?.();

    // "b" must still be the engine's newest visible toast.
    engine.dismissNewest();
    expect(mounts[1]!.left).toBe(true);
  });
});

describe("ToastEngine: timer hygiene", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("leaves no timer armed for a toast replaced in replace mode", () => {
    vi.useFakeTimers();
    const { view } = makeFakeView();
    const engine = new ToastEngine<FakeToast>({ view, mode: "replace", defaultDuration: 1000 });

    engine.show("first");
    expect(vi.getTimerCount()).toBe(1);
    engine.show("second");
    // The replaced toast's countdown dies with it: one live toast, one timer.
    expect(vi.getTimerCount()).toBe(1);
  });

  it("leaves no timer armed after clear()", () => {
    vi.useFakeTimers();
    const { view } = makeFakeView(false);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 3, defaultDuration: 1000 });

    engine.show("a");
    engine.show("b");
    expect(vi.getTimerCount()).toBe(2);
    engine.clear();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("leaves no timer armed after a toast is dismissed", () => {
    vi.useFakeTimers();
    const { view } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 1000 });

    const dismiss = engine.show("a");
    expect(vi.getTimerCount()).toBe(1);
    dismiss();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not resume the progress animation when no time is left on the clock", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 1000 });
    engine.show("t", { level: "info", duration: 1000 });

    // The wall clock runs past the deadline without the timer firing — what a
    // backgrounded tab does to a throttled timeout. Pausing there drains the
    // remaining time to zero.
    vi.setSystemTime(Date.now() + 1500);
    mounts[0]!.ctx.pause();
    expect(mounts[0]!.paused).toBe(true);

    // Nothing is left to count down, so there is nothing to resume: the view
    // must not be told to restart a progress animation that has no time in it.
    mounts[0]!.ctx.resume();
    expect(mounts[0]!.resumes).toBe(0);
  });
});

describe("ToastEngine: a dismiss that lands mid-promotion", () => {
  it("dismisses a queued toast that is dismissed from inside the view's mount", () => {
    // The queue shift and dismiss-fn rebind straddle view.mount(), so a dismiss
    // arriving from inside mount() finds neither the queued entry nor a bound fn.
    const { view, mounts } = makeFakeView(true);
    let dismissQueued: (() => void) | null = null;
    const dismissingView: ToastView<FakeToast> = {
      ...view,
      mount(data, ctx) {
        const handle = view.mount(data, ctx);
        if (data.message === "queued") {
          dismissQueued?.();
        }
        return handle;
      },
    };
    const engine = new ToastEngine<FakeToast>({
      view: dismissingView,
      maxVisible: 1,
      defaultDuration: 0,
    });
    const dismissVisible = engine.show("visible");
    dismissQueued = engine.show("queued");

    dismissVisible();

    expect(mounts[1]!.data.message).toBe("queued");
    expect(mounts[1]!.left).toBe(true);

    // The single slot is free again.
    engine.show("next");
    expect(mounts).toHaveLength(3);
  });
});

describe("ToastEngine: a countdown that drained while the toast was held", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  // A backgrounded tab can let the wall clock pass a toast's deadline before its
  // setTimeout runs; hovering then drains `remaining` to 0 with no timer armed.
  function expireWhileHeld(): FakeToast[] {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1, defaultDuration: 1000 });
    engine.show("t", { level: "info", duration: 1000 });
    vi.setSystemTime(Date.now() + 1500);
    mounts[0]!.ctx.pause();
    return mounts;
  }

  it("keeps a toast whose countdown drained while it is still hovered or focused", () => {
    const mounts = expireWhileHeld();

    // Hover/focus pauses the countdown; an expired one is no exception.
    vi.advanceTimersByTime(10_000);
    expect(mounts[0]!.left).toBe(false);
    expect(shown(mounts)).toBe(1);
  });

  it("dismisses it when the hover/focus is released, instead of stranding it on screen", () => {
    const mounts = expireWhileHeld();

    mounts[0]!.ctx.resume();

    // No timer is left to notice; release is the last chance to auto-dismiss.
    expect(mounts[0]!.left).toBe(true);
    expect(shown(mounts)).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("leaves a sticky toast alone: remaining 0 there is no countdown, not a drained one", () => {
    vi.useFakeTimers();
    const { view, mounts } = makeFakeView(true);
    const engine = new ToastEngine<FakeToast>({ view, maxVisible: 1 });
    engine.show("e", { level: "error" });

    // remaining=0 from birth must not be read as an expired countdown.
    mounts[0]!.ctx.pause();
    mounts[0]!.ctx.resume();

    expect(mounts[0]!.left).toBe(false);
    expect(shown(mounts)).toBe(1);
  });
});
