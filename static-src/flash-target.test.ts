// flash-target.ts: behaviour, plus computed reads of the flash keyframes (the only way to see them).

import { vi, describe, it, expect, beforeEach, afterEach, beforeAll, afterAll } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { cdp } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { flashTarget } from "./flash-target.js";

/** Drive the rAF retry loop the module uses to wait for a laid-out target. */
async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

let scrolled: string[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  scrolled = [];
  document.body.innerHTML = "";
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
});

function control(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  document.body.appendChild(e);
  return e;
}

function byId(id: string): () => HTMLElement | null {
  return () => document.getElementById(id);
}

describe("flashTarget", { timeout: testTimeoutFor(framesBudgetMs(25)) }, () => {
  it("scrolls the target into view and flashes a ring", async () => {
    const box = control("security-profile-list");
    flashTarget(byId("security-profile-list"));
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
  });

  it("drops the flash class when the animation ends", async () => {
    const box = control("flag-tool-search");
    flashTarget(byId("flag-tool-search"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("deep-link-flash")).toBe(false);
  });

  // Quiet degradation: a never-resolving target must not throw, scroll or leave the loop running.
  it("does nothing for a target that never resolves", async () => {
    control("real-control");
    expect(() => {
      flashTarget(byId("no-such-control"));
    }).not.toThrow();
    await frames(25);
    expect(scrolled).toEqual([]);
    expect(document.querySelectorAll(".deep-link-flash")).toHaveLength(0);
  });

  // Panels populate asynchronously, so a target can land frames after the request.
  it("waits for a target that does not exist yet", async () => {
    flashTarget(byId("late-control"));
    await frames(2);
    expect(scrolled).toEqual([]);

    control("late-control");
    await frames(2);
    expect(scrolled).toEqual(["late-control"]);
  });

  // MAX_FRAMES is 20: a target arriving later is not scrolled to.
  it("stops looking once the frame budget is spent", async () => {
    flashTarget(byId("very-late-control"));
    await frames(25);
    control("very-late-control");
    await frames(3);
    expect(scrolled).toEqual([]);
  });

  // An element without a box (hidden panel, collapsed disclosure) cannot be scrolled to.
  it("waits for a present target that has no layout box", async () => {
    const box = document.createElement("input");
    box.id = "boxless-control";
    box.style.display = "none";
    document.body.appendChild(box);

    flashTarget(byId("boxless-control"));
    await frames(2);
    expect(scrolled).toEqual([]);

    box.style.display = "";
    await frames(2);
    expect(scrolled).toEqual(["boxless-control"]);
  });

  it("restarts the flash when the same target is asked for twice", async () => {
    const box = control("diagnostics-run");
    flashTarget(byId("diagnostics-run"));
    await frames(1);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("deep-link-flash")).toBe(false);
    flashTarget(byId("diagnostics-run"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
  });
});

// Under `prefers-reduced-motion` the 2.5s timeout is the only cleanup.
describe("the reduced-motion backstop", () => {
  beforeEach(() => {
    // setTimeout only: the retry loop runs on rAF.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not let an older deadline clear a newer flash", async () => {
    const box = control("chat-retention-days");
    flashTarget(byId("chat-retention-days"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    vi.advanceTimersByTime(1000);
    flashTarget(byId("chat-retention-days"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    vi.advanceTimersByTime(1600);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    vi.advanceTimersByTime(1000);
    expect(box.classList.contains("deep-link-flash")).toBe(false);
  });
});

/** 15% of the 1.6s keyframes, where both animated properties reach the declared value; seeked, not slept. */
const MID_MS = 0.15 * 1600;

const canvas = document.createElement("canvas");
canvas.width = 1;
canvas.height = 1;
const ctx = canvas.getContext("2d", { willReadFrequently: true });

/** `getComputedStyle` returns the authored `oklch()` / `color-mix()` form, so paint it to get bytes. */
function bytes(colour: string): [number, number, number, number] {
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  // A rejected string leaves fillStyle unchanged, so a typo would measure the previous colour.
  expect(CSS.supports("color", colour), `Chromium parses ${colour}`).toBe(true);
  ctx.clearRect(0, 0, 1, 1);
  ctx.fillStyle = colour;
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0, d[3] ?? 0];
}

function expectSameColour(got: string, want: string, what: string): void {
  const a = bytes(got);
  const b = bytes(want);
  for (let i = 0; i < 4; i++) {
    expect(Math.abs((a[i] ?? 0) - (b[i] ?? 0)), `${what}: ${got} vs ${want}`).toBeLessThanOrEqual(
      2,
    );
  }
}

function probe(declaration: string, property: string): string {
  const p = document.createElement("div");
  p.setAttribute("style", declaration);
  document.body.appendChild(p);
  const value = getComputedStyle(p).getPropertyValue(property);
  p.remove();
  return value;
}

describe("the deep-link-flash rules as rendered", () => {
  let style: HTMLStyleElement;
  const host = document.createElement("div");

  beforeAll(() => {
    style = mountAppCSS();
    host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
    document.body.appendChild(host);
  });

  afterAll(() => {
    style.remove();
    host.remove();
    document.documentElement.removeAttribute("data-theme");
  });

  // The behavioural half empties `document.body`, detaching the host, and a detached element computes "".
  beforeEach(() => {
    if (!host.isConnected) {
      document.body.appendChild(host);
    }
  });

  afterEach(() => {
    host.replaceChildren();
    document.documentElement.removeAttribute("data-theme");
  });

  function setTheme(theme: "dark" | "light"): void {
    if (theme === "dark") {
      document.documentElement.removeAttribute("data-theme");
      return;
    }
    document.documentElement.dataset["theme"] = theme;
  }

  function mountRow(): HTMLElement {
    const list = document.createElement("ul");
    list.className = "git-pr-list";
    const row = document.createElement("li");
    row.className = "git-pr-row";
    row.setAttribute("data-pr", "github:github.com:cplieger/marotte#42");
    list.appendChild(row);
    host.replaceChildren(list);
    return row;
  }

  /** A text field: `02-reset.css` already rounds every checkbox. */
  function mountControl(): HTMLElement {
    const ctl = document.createElement("input");
    ctl.type = "text";
    host.replaceChildren(ctl);
    return ctl;
  }

  function midAnimation(el: HTMLElement): CSSStyleDeclaration {
    el.classList.add("deep-link-flash");
    const [anim] = el.getAnimations();
    expect(anim, "the flash class starts an animation").not.toBeUndefined();
    anim?.pause();
    if (anim !== undefined) {
      anim.currentTime = MID_MS;
    }
    return getComputedStyle(el);
  }

  /** One element per case: a second `classList.add` then `getComputedStyle` in one task reads the class rule stale. */
  const CASES = [
    { family: "a PR row", theme: "dark", mount: mountRow },
    { family: "a PR row", theme: "light", mount: mountRow },
    { family: "a settings control", theme: "dark", mount: mountControl },
    { family: "a settings control", theme: "light", mount: mountControl },
  ] as const;

  it("does not shrink a flashed PR row's corners", () => {
    const row = mountRow();
    const unflashed = getComputedStyle(row).borderTopLeftRadius;
    // The PREMISE: the row has a radius of its own, so there is something for the
    // utility to have overridden.
    expect(unflashed).toBe(probe("border-radius: var(--r)", "border-top-left-radius"));

    row.classList.add("deep-link-flash");
    expect(getComputedStyle(row).borderTopLeftRadius).toBe(unflashed);
  });

  it("gives a bare settings control the radius the declaration was written for", () => {
    const ctl = mountControl();
    // The other PREMISE: this control carries no radius of its own, so the utility
    // is the only thing that can supply one.
    expect(getComputedStyle(ctl).borderTopLeftRadius).toBe("0px");

    ctl.classList.add("deep-link-flash");
    expect(getComputedStyle(ctl).borderTopLeftRadius).toBe(
      probe("border-radius: var(--r-sm)", "border-top-left-radius"),
    );
  });

  it.each(CASES)("rings $family in the accent ($theme)", ({ theme, mount }) => {
    // The mid-animation reads mean nothing if the animation is suppressed.
    expect(matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);
    setTheme(theme);
    const accent = probe("color: var(--c-accent)", "color");
    const outline = midAnimation(mount()).outlineColor;
    expect(bytes(outline)[3], "the ring is not transparent").toBeGreaterThan(0);
    expectSameColour(outline, accent, "the ring");
  });

  it.each(CASES)("washes $family mid-flash ($theme)", ({ theme, mount }) => {
    setTheme(theme);
    const selected = probe("color: var(--c-selected-bg)", "color");
    // The animation origin outranks every normal author declaration, including the PR row's own `background`.
    expectSameColour(midAnimation(mount()).backgroundColor, selected, "the wash");
  });

  it.each(CASES)(
    "leaves $family a steady ring and no animation under reduced motion ($theme)",
    async ({ theme, mount }) => {
      // Emulated, so the override is judged from its real MANIFEST position under the real query.
      await cdp().send("Emulation.setEmulatedMedia", {
        features: [{ name: "prefers-reduced-motion", value: "reduce" }],
      });
      try {
        expect(matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(true);
        setTheme(theme);
        const accent = probe("color: var(--c-accent)", "color");
        const el = mount();
        el.classList.add("deep-link-flash");
        const cs = getComputedStyle(el);
        expect(cs.animationName, "nothing animates").toBe("none");
        expectSameColour(cs.outlineColor, accent, "the steady ring");
      } finally {
        await cdp().send("Emulation.setEmulatedMedia", { features: [] });
      }
    },
  );
});
