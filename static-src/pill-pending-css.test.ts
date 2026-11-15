// A pill holding a pick for after the turn (the model's and the security profile's) pulses its mark, and its name
// stays readable through the pulse: WCAG 1.4.3 holds text to its contrast floor at every frame, not on average.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { iconEl } from "./icon-el.js";
import { ICON_MODEL, ICON_SHIELD_LINES_TWO } from "./icons.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

function mountRow(): void {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const app = doc.getElementById("app");
  if (app === null) {
    throw new Error("index.html has no #app");
  }
  document.body.replaceChildren(document.importNode(app, true));
  const model = document.getElementById("switch-model-btn")!;
  model.prepend(iconEl(ICON_MODEL));
  model.classList.add("pending");
  const profile = document.getElementById("profile-pill")!;
  profile.classList.remove("hidden");
  document.getElementById("profile-pill-icon")!.replaceChildren(iconEl(ICON_SHIELD_LINES_TWO));
  document.getElementById("profile-pill-label")!.textContent = "Trusted";
  profile.classList.add("pending");
}

/** The opacity `node` paints at within `host`, the pill's own and every box between them folded in. */
function foldedOpacity(node: Element, host: Element): number {
  let o = 1;
  for (let e: Element | null = node; e !== null; e = e.parentElement) {
    o *= Number(getComputedStyle(e).opacity);
    if (e === host) {
      break;
    }
  }
  return o;
}

/** Seek every animation under `host` to `ms` and hold it there. */
function holdAt(host: Element, ms: number): Animation[] {
  const all = host.getAnimations({ subtree: true });
  for (const a of all) {
    a.pause();
    a.currentTime = ms;
  }
  return all;
}

const PILLS = [
  { what: "the model pill", pill: "switch-model-btn", name: "ctx-model-pill" },
  { what: "the security profile pill", pill: "profile-pill", name: "profile-pill-label" },
];

describe("a pending pill's pulse", () => {
  for (const { what, pill, name } of PILLS) {
    it(`keeps ${what}'s name at full opacity in every frame, and pulses its icon`, () => {
      mountRow();
      const host = document.getElementById(pill)!;
      const label = document.getElementById(name)!;
      const icon = host.querySelector("svg")!;
      const running = holdAt(host, 0);
      expect(running.length, `${what} pulses`).toBeGreaterThan(0);
      const period = Math.max(
        ...running.map((a) => Number(a.effect?.getComputedTiming().duration ?? 0)),
      );
      let dimmest = 1;
      for (let step = 0; step <= 20; step++) {
        holdAt(host, (period * step) / 20);
        expect(foldedOpacity(label, host), `${what}'s name at ${step}/20 of the pulse`).toBe(1);
        dimmest = Math.min(dimmest, foldedOpacity(icon, host));
      }
      expect(dimmest, `${what}'s icon pulses`).toBeLessThan(1);
    });
  }
});
