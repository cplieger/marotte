// `forceReflow` pins a browser behaviour four call sites rely on: removing and re-adding a class in one task restarts
// an animation only with a layout read between. Real Chromium.
import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { forceReflow } from "./dom.js";

const ANIM = "reflow-probe";

beforeAll(() => {
  const style = document.createElement("style");
  style.textContent = `
    @keyframes ${ANIM} { from { opacity: 0 } to { opacity: 1 } }
    .${ANIM}-on { animation: ${ANIM} 5s linear }
  `;
  document.head.append(style);
});

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function mountProbe(): HTMLElement {
  const el = document.createElement("div");
  el.style.inlineSize = "40px";
  el.style.blockSize = "20px";
  document.body.append(el);
  hosts.push(el);
  return el;
}

// The element's one running animation. getAnimations() flushes style itself, so it is read only after the class is
// back; a restart replaces the CSSAnimation object (CSS Animations 1, "Animations"), which no frame wait can race.
function animationOf(el: HTMLElement): Animation {
  const all = el.getAnimations();
  expect(all).toHaveLength(1);
  return all[0] as Animation;
}

describe("forceReflow", () => {
  it("restarts an animation that a bare remove-then-add does not", () => {
    const withoutFlush = mountProbe();
    withoutFlush.classList.add(`${ANIM}-on`);
    const first = animationOf(withoutFlush);

    // Coalesced: the browser never sees the element without the class.
    withoutFlush.classList.remove(`${ANIM}-on`);
    withoutFlush.classList.add(`${ANIM}-on`);
    expect(animationOf(withoutFlush)).toBe(first);

    const withFlush = mountProbe();
    withFlush.classList.add(`${ANIM}-on`);
    const before = animationOf(withFlush);

    withFlush.classList.remove(`${ANIM}-on`);
    forceReflow(withFlush);
    withFlush.classList.add(`${ANIM}-on`);
    expect(animationOf(withFlush)).not.toBe(before);
    expect(before.playState).toBe("idle");
  });

  it("reads a real measurement, so the call cannot be elided", () => {
    expect(forceReflow(mountProbe())).toBe(20);
  });
});
