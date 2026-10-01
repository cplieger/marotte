// `--shell-shortfall`: its arithmetic, its gate, and its lifecycle.
//
// The standalone gate is the function's own parameter (defaulting to the
// platform's answer), so the tab case needs no module re-import. Chromium's own
// `visualViewport` is replaced by a fake that carries the three numbers the module
// reads and really adds and removes its listeners.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  computeShortfall,
  initShellViewport,
  SHORTFALL_CAP_PX,
  SHORTFALL_PROP,
} from "./shell-viewport.js";

type Listener = () => void;

interface FakeViewport {
  height: number;
  offsetTop: number;
  scale: number;
  addEventListener(type: string, fn: Listener): void;
  removeEventListener(type: string, fn: Listener): void;
  fire(type: string): void;
  listeners(): number;
}

function fakeViewport(height: number, offsetTop = 0, scale = 1): FakeViewport {
  const map = new Map<string, Set<Listener>>();
  return {
    height,
    offsetTop,
    scale,
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

const nextFrame = (): Promise<void> =>
  new Promise((r) => {
    requestAnimationFrame(() => r());
  });

const icb = (): number => document.documentElement.clientHeight;
const shortfall = (): string => document.documentElement.style.getPropertyValue(SHORTFALL_PROP);

let dispose: (() => void) | undefined;

afterEach(() => {
  dispose?.();
  dispose = undefined;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.documentElement.style.removeProperty(SHORTFALL_PROP);
  document.getElementById("vpdebug")?.remove();
  if (location.hash === "#vpdebug") {
    history.replaceState(null, "", location.pathname + location.search);
  }
});

describe("computeShortfall", () => {
  const vv = (h: number, top = 0, scale = 1): VisualViewport =>
    ({ height: h, offsetTop: top, scale }) as unknown as VisualViewport;

  it("is the visual viewport's overhang below the layout viewport", () => {
    expect(computeShortfall(800, vv(840))).toBe(40);
    expect(computeShortfall(800, vv(830, 10))).toBe(40);
  });

  it("is 0 when the two agree", () => {
    expect(computeShortfall(800, vv(800))).toBe(0);
  });

  it("ignores a SHORTER visual viewport, which is the keyboard", () => {
    expect(computeShortfall(800, vv(500))).toBe(0);
  });

  it("treats a reading over the cap as broken, not as the cap", () => {
    expect(computeShortfall(800, vv(800 + SHORTFALL_CAP_PX))).toBe(SHORTFALL_CAP_PX);
    expect(computeShortfall(800, vv(800 + SHORTFALL_CAP_PX + 1))).toBe(0);
    expect(computeShortfall(800, vv(1300))).toBe(0);
  });

  it("answers 0 while pinch-zoomed or with no visualViewport", () => {
    expect(computeShortfall(800, vv(840, 0, 2))).toBe(0);
    expect(computeShortfall(800, null)).toBe(0);
  });
});

describe("initShellViewport", () => {
  it("installs nothing in a browser tab", () => {
    const fake = fakeViewport(icb() + 40);
    vi.stubGlobal("visualViewport", fake);
    const add = vi.spyOn(window, "addEventListener");
    dispose = initShellViewport(false);
    expect(fake.listeners()).toBe(0);
    expect(add).not.toHaveBeenCalled();
    expect(shortfall()).toBe("");
  });

  it("publishes a visual viewport 40px taller than the layout viewport as 40px", () => {
    vi.stubGlobal("visualViewport", fakeViewport(icb() + 40));
    dispose = initShellViewport(true);
    expect(shortfall()).toBe("40px");
  });

  it("leaves the property ABSENT rather than writing 0px when the two agree", () => {
    vi.stubGlobal("visualViewport", fakeViewport(icb()));
    dispose = initShellViewport(true);
    expect(shortfall()).toBe("");
  });

  it("leaves it absent for a keyboard-shaped shrink, an over-cap reading and a zoom", () => {
    for (const fake of [
      fakeViewport(icb() - 300),
      fakeViewport(icb() + 500),
      fakeViewport(icb() + 40, 0, 2),
    ]) {
      vi.stubGlobal("visualViewport", fake);
      dispose = initShellViewport(true);
      expect(shortfall()).toBe("");
      dispose();
    }
    dispose = undefined;
  });

  it("recomputes on the viewport's events, once per frame for a burst", async () => {
    const fake = fakeViewport(icb());
    vi.stubGlobal("visualViewport", fake);
    dispose = initShellViewport(true);
    expect(shortfall()).toBe("");
    const raf = vi.spyOn(window, "requestAnimationFrame");
    fake.height = icb() + 24;
    fake.fire("resize");
    fake.fire("scroll");
    window.dispatchEvent(new Event("resize"));
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    expect(raf).toHaveBeenCalledTimes(1);
    expect(shortfall(), "coalesced: nothing written before the frame").toBe("");
    await nextFrame();
    expect(shortfall()).toBe("24px");
  });

  it("removes the property again once the shortfall closes", async () => {
    const fake = fakeViewport(icb() + 40);
    vi.stubGlobal("visualViewport", fake);
    dispose = initShellViewport(true);
    expect(shortfall()).toBe("40px");
    fake.height = icb();
    window.dispatchEvent(new Event("pageshow"));
    await nextFrame();
    expect(shortfall()).toBe("");
  });

  it("dispose removes every listener and the property", () => {
    const fake = fakeViewport(icb() + 40);
    vi.stubGlobal("visualViewport", fake);
    const remove = vi.spyOn(window, "removeEventListener");
    dispose = initShellViewport(true);
    expect(fake.listeners()).toBe(2);
    dispose();
    dispose = undefined;
    expect(fake.listeners()).toBe(0);
    expect(remove.mock.calls.map((c) => c[0]).sort()).toEqual([
      "focus",
      "orientationchange",
      "pageshow",
      "resize",
    ]);
    expect(shortfall()).toBe("");
    fake.height = icb() + 60;
    fake.fire("resize");
    expect(shortfall(), "a disposed module writes nothing").toBe("");
  });

  it("mounts the readout only under #vpdebug", () => {
    vi.stubGlobal("visualViewport", fakeViewport(icb() + 40));
    dispose = initShellViewport(true);
    expect(document.getElementById("vpdebug")).toBeNull();
    dispose();

    location.hash = "#vpdebug";
    dispose = initShellViewport(true);
    const out = document.getElementById("vpdebug");
    expect(out).not.toBeNull();
    expect(out?.textContent).toContain("standalone yes");
    expect(out?.textContent).toContain(`clientHeight ${icb()}`);
    expect(out?.textContent).toContain(`vv.height ${icb() + 40}`);
    expect(out?.textContent).toContain("shortfall 40");
    expect(out?.textContent).toContain("safe-area t/r/b/l 0px 0px 0px 0px");
    dispose();
    dispose = undefined;
    expect(document.getElementById("vpdebug"), "dispose removes the readout").toBeNull();
  });
});
