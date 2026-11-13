import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { cancelTransition, runTransition } from "./transition.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function mount(): HTMLElement {
  const el = document.createElement("div");
  document.body.appendChild(el);
  return el;
}

describe("runTransition: the start state is committed before the change", () => {
  it("reads a layout property before running the change, so the change has something to animate from", () => {
    const el = mount();
    // The commit leaves no observable state behind, so ordering (read before
    // change) is the only thing a test can pin.
    const order: string[] = [];
    vi.spyOn(el, "getBoundingClientRect").mockImplementation(() => {
      order.push("commit");
      return new DOMRect();
    });
    runTransition(el, {
      change: () => {
        order.push("change");
      },
    });
    expect(order).toEqual(["commit", "change"]);
  });

  it("commits unconditionally, so a caller has no placement decision to get wrong", () => {
    const el = mount();
    const read = vi.spyOn(el, "getBoundingClientRect");
    // No settled, nothing pending: the commit still happens.
    runTransition(el, { change: () => undefined });
    expect(read).toHaveBeenCalledOnce();
  });
});

describe("runTransition: settled", () => {
  it("runs once on a transitionend whose target is the element", () => {
    const el = mount();
    const settled = vi.fn();
    runTransition(el, { change: () => undefined, settled });
    el.dispatchEvent(new Event("transitionend"));
    el.dispatchEvent(new Event("transitionend"));
    expect(settled).toHaveBeenCalledOnce();
  });

  it("ignores a transitionend that bubbles up from a descendant", () => {
    const el = mount();
    const child = document.createElement("span");
    el.appendChild(child);
    const settled = vi.fn();
    runTransition(el, { change: () => undefined, settled });
    child.dispatchEvent(new Event("transitionend", { bubbles: true }));
    expect(settled).not.toHaveBeenCalled();
    el.dispatchEvent(new Event("transitionend"));
    expect(settled).toHaveBeenCalledOnce();
  });

  it("runs via the fallback ceiling when transitionend never fires", () => {
    const el = mount();
    const settled = vi.fn();
    runTransition(el, { change: () => undefined, settled });
    vi.advanceTimersByTime(399);
    expect(settled).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(settled).toHaveBeenCalledOnce();
  });

  it("arms nothing when settled is omitted", () => {
    const el = mount();
    const listen = vi.spyOn(el, "addEventListener");
    runTransition(el, { change: () => undefined });
    expect(listen).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("runTransition: supersession is per element", () => {
  it("a second run drops the first settle without running it", () => {
    const el = mount();
    const first = vi.fn();
    const second = vi.fn();
    runTransition(el, { change: () => undefined, settled: first });
    runTransition(el, { change: () => undefined, settled: second });
    el.dispatchEvent(new Event("transitionend"));
    vi.advanceTimersByTime(400);
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledOnce();
  });

  it("supersedes only the element it is given", () => {
    const a = mount();
    const b = mount();
    const settledA = vi.fn();
    const settledB = vi.fn();
    runTransition(a, { change: () => undefined, settled: settledA });
    runTransition(b, { change: () => undefined, settled: settledB });
    a.dispatchEvent(new Event("transitionend"));
    b.dispatchEvent(new Event("transitionend"));
    expect(settledA).toHaveBeenCalledOnce();
    expect(settledB).toHaveBeenCalledOnce();
  });

  it("cancelTransition prevents the settle; a later transitionend and the ceiling are no-ops", () => {
    const el = mount();
    const settled = vi.fn();
    runTransition(el, { change: () => undefined, settled });
    cancelTransition(el);
    el.dispatchEvent(new Event("transitionend"));
    vi.advanceTimersByTime(400);
    expect(settled).not.toHaveBeenCalled();
  });

  it("cancelTransition on an element with nothing pending is a no-op", () => {
    const el = mount();
    const detach = vi.spyOn(el, "removeEventListener");
    cancelTransition(el);
    expect(detach).not.toHaveBeenCalled();
  });
});

describe("runTransition: 'settled' means it stops touching the element", () => {
  // Overlay primitives hand this helper one long-lived, shared element, so
  // finishing must mean it stops touching that element, not just calling back.
  function arm(): { el: HTMLElement; detach: ReturnType<typeof vi.spyOn> } {
    const el = mount();
    const detach = vi.spyOn(el, "removeEventListener");
    runTransition(el, { change: () => undefined, settled: () => undefined });
    return { el, detach };
  }

  it("cancelTransition after the settle has already run detaches nothing a second time", () => {
    const { el, detach } = arm();
    el.dispatchEvent(new Event("transitionend"));
    expect(detach).toHaveBeenCalledTimes(1);
    cancelTransition(el);
    expect(detach).toHaveBeenCalledTimes(1);
  });

  it("a second cancelTransition detaches nothing a second time", () => {
    const { el, detach } = arm();
    cancelTransition(el);
    expect(detach).toHaveBeenCalledTimes(1);
    cancelTransition(el);
    expect(detach).toHaveBeenCalledTimes(1);
  });

  it("clears the fallback timer when the transition ends first", () => {
    const { el } = arm();
    el.dispatchEvent(new Event("transitionend"));
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("runTransition: re-entry from change and the spec's getters", () => {
  it("a cancelTransition from change takes the element: nothing is armed", () => {
    const el = mount();
    const settled = vi.fn();
    runTransition(el, {
      change: () => {
        cancelTransition(el);
      },
      settled,
    });
    vi.advanceTimersByTime(400);
    expect(settled).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a runTransition from change wins: only the nested settle runs", () => {
    const el = mount();
    const outer = vi.fn();
    const inner = vi.fn();
    runTransition(el, {
      change: () => {
        runTransition(el, { change: () => undefined, settled: inner });
      },
      settled: outer,
    });
    vi.advanceTimersByTime(400);
    expect(outer).not.toHaveBeenCalled();
    expect(inner).toHaveBeenCalledTimes(1);
  });

  it("a throw from change arms nothing, leaves nothing reserved, and propagates", () => {
    const el = mount();
    const settled = vi.fn();
    const boom = new Error("change");
    expect(() => {
      runTransition(el, {
        change: () => {
          throw boom;
        },
        settled,
      });
    }).toThrow(boom);
    vi.advanceTimersByTime(400);
    expect(settled).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a change-only transition whose change re-runs the element leaves only the nested one", () => {
    const el = mount();
    const inner = vi.fn();
    runTransition(el, {
      change: () => {
        runTransition(el, { change: () => undefined, settled: inner });
      },
    });
    vi.advanceTimersByTime(400);
    expect(inner).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("reads change and settled once, before anything: a settled getter's nested run is superseded", () => {
    const el = mount();
    const reads: string[] = [];
    const outer = vi.fn();
    const nested = vi.fn();
    const spec = {
      get change(): () => void {
        reads.push("change");
        return () => undefined;
      },
      get settled(): () => void {
        reads.push("settled");
        runTransition(el, { change: () => undefined, settled: nested });
        return outer;
      },
    };
    runTransition(el, spec);
    vi.advanceTimersByTime(400);
    expect(reads.sort()).toEqual(["change", "settled"]);
    expect(nested).not.toHaveBeenCalled();
    expect(outer).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a throwing change getter leaves a pending settle in place", () => {
    const el = mount();
    const first = vi.fn();
    runTransition(el, { change: () => undefined, settled: first });
    expect(() => {
      runTransition(el, {
        get change(): () => void {
          throw new Error("getter");
        },
      });
    }).toThrow("getter");
    vi.advanceTimersByTime(400);
    expect(first).toHaveBeenCalledTimes(1);
  });
});
