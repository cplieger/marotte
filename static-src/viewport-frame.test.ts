// The visual-viewport frame reader: its two numbers, its tolerance, and the three subscriptions its
// remover detaches. The fake `visualViewport` is installed AFTER import: the module reads the
// global per call, and a cached read would pass against Chromium's own viewport.
import { afterEach, describe, expect, it, vi } from "vitest";

import { onViewportChange, viewportBox, viewportMoved } from "./viewport-frame.js";

type Listener = () => void;

interface FakeViewport {
  offsetLeft: number;
  offsetTop: number;
  width: number;
  height: number;
  addEventListener(type: string, fn: Listener): void;
  removeEventListener(type: string, fn: Listener): void;
  fire(type: string): void;
  listeners(): number;
}

function fakeViewport(height: number, offsetTop = 0, width = 0, offsetLeft = 0): FakeViewport {
  const map = new Map<string, Set<Listener>>();
  return {
    offsetLeft,
    offsetTop,
    width,
    height,
    addEventListener(type, fn): void {
      let set = map.get(type);
      if (set === undefined) {
        set = new Set();
        map.set(type, set);
      }
      set.add(fn);
    },
    removeEventListener(type, fn): void {
      map.get(type)?.delete(fn);
    },
    fire(type): void {
      for (const fn of [...(map.get(type) ?? [])]) {
        fn();
      }
    },
    listeners(): number {
      let n = 0;
      for (const set of map.values()) {
        n += set.size;
      }
      return n;
    },
  };
}

let release: (() => void) | undefined;

afterEach(() => {
  release?.();
  release = undefined;
  vi.unstubAllGlobals();
});

describe("viewportBox", () => {
  it("reads the visible viewport's four numbers", () => {
    vi.stubGlobal("visualViewport", fakeViewport(400, 300, 320, 24));
    expect(viewportBox()).toEqual({ offsetLeft: 24, offsetTop: 300, width: 320, height: 400 });
  });

  it("falls back to the layout viewport where the API is absent", () => {
    vi.stubGlobal("visualViewport", undefined);
    expect(viewportBox()).toEqual({
      offsetLeft: 0,
      offsetTop: 0,
      width: window.innerWidth,
      height: window.innerHeight,
    });
  });
});

describe("viewportMoved", () => {
  const box = (
    height: number,
    offsetTop = 0,
    width = 320,
    offsetLeft = 0,
  ): { offsetLeft: number; offsetTop: number; width: number; height: number } => ({
    offsetLeft,
    offsetTop,
    width,
    height,
  });

  it("is false for an identical box", () => {
    expect(viewportMoved(box(700), box(700))).toBe(false);
    expect(viewportMoved(box(400, 300, 320, 24), box(400, 300, 320, 24))).toBe(false);
  });

  // Sub-pixel jitter is not a move: `visualViewport.height` is fractional and iOS
  // reports it moving while nothing has happened.
  it("is false for a sub-pixel difference on any field", () => {
    expect(viewportMoved(box(700), box(700.5))).toBe(false);
    expect(viewportMoved(box(700, 0), box(700, 0.5))).toBe(false);
    expect(viewportMoved(box(700, 0, 320), box(700, 0, 320.5))).toBe(false);
    expect(viewportMoved(box(700, 0, 320, 0), box(700, 0, 320, 0.5))).toBe(false);
  });

  it("is true at one whole pixel on any field", () => {
    expect(viewportMoved(box(700), box(701))).toBe(true);
    expect(viewportMoved(box(700, 0), box(700, 1))).toBe(true);
    expect(viewportMoved(box(700, 0, 320), box(700, 0, 321))).toBe(true);
    expect(viewportMoved(box(700, 0, 320, 0), box(700, 0, 320, 1))).toBe(true);
  });

  // The keyboard dismissal this exists to catch: the offset closes and the height
  // grows back, both far past the tolerance.
  it("is true for a keyboard-shaped box against an open one", () => {
    expect(viewportMoved(box(400, 300), box(700, 0))).toBe(true);
  });

  // A horizontal pan while pinch-zoomed moves NEITHER block-axis field, so a gate
  // watching only those leaves an inline clamp frozen and reads a shifted `clientX`
  // as travel the reader did not make.
  it("is true for an inline-only pan the block axis cannot see", () => {
    const before = box(700, 0, 320, 0);
    const after = box(700, 0, 320, 90);
    expect(after.height).toBe(before.height);
    expect(after.offsetTop).toBe(before.offsetTop);
    expect(viewportMoved(after, before)).toBe(true);
  });
});

describe("onViewportChange", () => {
  it("fires for a viewport resize, a viewport scroll and a window resize", () => {
    const fake = fakeViewport(700);
    vi.stubGlobal("visualViewport", fake);
    const seen: string[] = [];
    release = onViewportChange(() => {
      seen.push("hit");
    });
    fake.fire("resize");
    fake.fire("scroll");
    window.dispatchEvent(new Event("resize"));
    expect(seen).toHaveLength(3);
  });

  it("detaches all three", () => {
    const fake = fakeViewport(700);
    vi.stubGlobal("visualViewport", fake);
    const seen: string[] = [];
    const stop = onViewportChange(() => {
      seen.push("hit");
    });
    expect(fake.listeners()).toBe(2);
    stop();
    expect(fake.listeners()).toBe(0);
    fake.fire("resize");
    fake.fire("scroll");
    window.dispatchEvent(new Event("resize"));
    expect(seen).toHaveLength(0);
  });

  it("subscribes to the window alone where the API is absent", () => {
    vi.stubGlobal("visualViewport", undefined);
    const seen: string[] = [];
    release = onViewportChange(() => {
      seen.push("hit");
    });
    window.dispatchEvent(new Event("resize"));
    expect(seen).toHaveLength(1);
  });
});
