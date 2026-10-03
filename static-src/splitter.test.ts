import { describe, it, expect, afterEach, vi } from "vitest";

import { attachSplitter, type SplitterOptions } from "./splitter.js";

/** Back the three pointer-capture methods with a Set, as shell.test.ts does: a
 *  synthetic event carries no real pointer for the browser to capture. */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

function ptr(
  type: string,
  at: { x?: number; y?: number },
  opts: { pointerId?: number; isPrimary?: boolean } = {},
): Event {
  return Object.assign(new Event(type), {
    pointerId: opts.pointerId ?? 1,
    isPrimary: opts.isPrimary ?? true,
    clientX: at.x ?? 0,
    clientY: at.y ?? 0,
  });
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

interface Rig {
  handle: HTMLDivElement;
  size: { value: number };
  apply: ReturnType<typeof vi.fn<(n: number) => number>>;
  commit: ReturnType<typeof vi.fn<(n: number) => void>>;
  calls: string[];
}

function rig(overrides: Partial<SplitterOptions> = {}): Rig {
  const handle = document.createElement("div");
  stubPointerCapture(handle);
  document.body.appendChild(handle);
  const size = { value: 300 };
  const calls: string[] = [];
  const apply = vi.fn((n: number) => {
    const v = Math.min(Math.max(Math.round(n), 100), 500);
    size.value = v;
    calls.push(`apply:${String(v)}`);
    return v;
  });
  const commit = vi.fn((n: number) => {
    calls.push(`commit:${String(n)}`);
  });
  attachSplitter({
    handle,
    axis: "x",
    direction: 1,
    orientation: "vertical",
    label: "Resize",
    keys: { grow: "ArrowRight", shrink: "ArrowLeft" },
    step: () => 10,
    measure: () => size.value,
    limits: () => ({ min: 100, max: 500 }),
    apply,
    commit,
    frame: "immediate",
    ...overrides,
  });
  return { handle, size, apply, commit, calls };
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("splitter: drag", () => {
  it("lets direction and axis decide the sign", () => {
    const r = rig();
    r.handle.dispatchEvent(ptr("pointerdown", { x: 50 }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 90 }));
    expect(r.size.value).toBe(340);

    const up = rig({ axis: "y", direction: -1 });
    up.handle.dispatchEvent(ptr("pointerdown", { y: 400 }));
    up.handle.dispatchEvent(ptr("pointermove", { y: 330 }));
    expect(up.size.value).toBe(370);
  });

  it("coalesces moves into one apply per frame in animation mode", async () => {
    const r = rig({ frame: "animation" });
    r.handle.dispatchEvent(ptr("pointerdown", { x: 0 }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 10 }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 20 }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 30 }));
    expect(r.apply).not.toHaveBeenCalled();

    await nextFrame();
    expect(r.apply).toHaveBeenCalledTimes(1);
    expect(r.apply).toHaveBeenLastCalledWith(330);
  });

  it("flushes a pending frame before it commits a release", () => {
    const r = rig({ frame: "animation" });
    r.handle.dispatchEvent(ptr("pointerdown", { x: 0 }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 45 }));
    r.handle.dispatchEvent(ptr("pointerup", { x: 45 }));
    expect(r.calls).toEqual(["apply:345", "commit:345"]);
  });

  it("ignores a non-primary press and a move it did not capture", () => {
    const r = rig();
    r.handle.dispatchEvent(ptr("pointerdown", { x: 0 }, { isPrimary: false }));
    r.handle.dispatchEvent(ptr("pointermove", { x: 80 }));
    r.handle.dispatchEvent(ptr("pointerup", { x: 80 }));
    expect(r.apply).not.toHaveBeenCalled();
    expect(r.commit).not.toHaveBeenCalled();
  });
});

describe("splitter: keys and reset", () => {
  it("leaves Home and End inert unless they are enabled", () => {
    const r = rig();
    for (const key of ["Home", "End"]) {
      const e = new KeyboardEvent("keydown", { key, cancelable: true });
      r.handle.dispatchEvent(e);
      expect(e.defaultPrevented).toBe(false);
    }
    expect(r.apply).not.toHaveBeenCalled();
    expect(r.commit).not.toHaveBeenCalled();

    const on = rig({ homeEnd: true });
    on.handle.dispatchEvent(new KeyboardEvent("keydown", { key: "End" }));
    expect(on.calls).toEqual(["apply:500", "commit:500"]);
  });

  it("does nothing on a double-click without onReset", () => {
    const r = rig();
    r.handle.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(r.apply).not.toHaveBeenCalled();
    expect(r.commit).not.toHaveBeenCalled();

    const onReset = vi.fn();
    const withReset = rig({ onReset });
    withReset.handle.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(onReset).toHaveBeenCalledTimes(1);
  });
});

describe("splitter: ARIA", () => {
  it("writes valuetext only when asked", () => {
    const plain = rig();
    plain.handle.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    expect(plain.handle.getAttribute("aria-valuenow")).toBe("310");
    expect(plain.handle.hasAttribute("aria-valuetext")).toBe(false);

    const texted = rig({ valueText: (n) => `${String(n)} pixels` });
    texted.handle.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    expect(texted.handle.getAttribute("aria-valuetext")).toBe("310 pixels");
  });
});
