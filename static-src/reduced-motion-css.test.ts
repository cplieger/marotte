// A reader who asks for reduced motion gets it everywhere: under
// `prefers-reduced-motion: reduce` every animation runs once for a near-zero
// duration and every transition is near-instant (40-a11y.css's universal sweep).
// Measured RENDERED, with the preference emulated in the engine, against elements
// that animate and transition at rest; the no-preference case is the control that
// proves those elements move at all.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLDivElement;

function mount(): { spinner: HTMLElement; subagent: HTMLElement; button: HTMLElement } {
  host.innerHTML = `
    <span class="tool-spinner"></span>
    <span class="subagent-spinner"></span>
    <button type="button" class="btn-small">Go</button>`;
  return {
    spinner: host.querySelector<HTMLElement>(".tool-spinner") as HTMLElement,
    subagent: host.querySelector<HTMLElement>(".subagent-spinner") as HTMLElement,
    button: host.querySelector<HTMLElement>(".btn-small") as HTMLElement,
  };
}

function longest(times: string): number {
  return Math.max(
    ...times.split(",").map((t) => {
      const v = t.trim();
      return v.endsWith("ms") ? Number.parseFloat(v) / 1000 : Number.parseFloat(v);
    }),
  );
}

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(async () => {
  await resetA11yMedia();
});

afterAll(() => {
  host.remove();
  style.remove();
});

describe("reduced motion", () => {
  it("leaves the spinners spinning and the button transitioning with no preference", async () => {
    expect.assertions(4);
    await resetA11yMedia();
    const { spinner, subagent, button } = mount();
    expect(getComputedStyle(spinner).animationIterationCount).toBe("infinite");
    expect(getComputedStyle(subagent).animationIterationCount).toBe("infinite");
    expect(longest(getComputedStyle(spinner).animationDuration)).toBeGreaterThan(0.1);
    expect(longest(getComputedStyle(button).transitionDuration)).toBeGreaterThan(0.01);
  });

  it("runs every animation once for a near-zero duration and makes transitions instant", async () => {
    expect.assertions(5);
    await emulateA11yMedia({ reducedMotion: "reduce" });
    const { spinner, subagent, button } = mount();
    expect(getComputedStyle(spinner).animationIterationCount).toBe("1");
    expect(getComputedStyle(subagent).animationIterationCount).toBe("1");
    expect(longest(getComputedStyle(spinner).animationDuration)).toBeLessThan(0.001);
    expect(longest(getComputedStyle(subagent).animationDuration)).toBeLessThan(0.001);
    expect(longest(getComputedStyle(button).transitionDuration)).toBeLessThan(0.001);
  });
});
