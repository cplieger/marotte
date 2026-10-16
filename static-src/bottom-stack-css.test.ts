// The bottom bar's stack: every box above the composer, and the composer, is one card shape
// (01-tokens.css `--stack-r`, one border, one gap), measured on the rendered cascade.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let bars: HTMLElement[] = [];

function box(tag: string, className: string, parent: HTMLElement, text = "x"): HTMLElement {
  const el = document.createElement(tag);
  el.className = className;
  el.textContent = text;
  parent.append(el);
  return el;
}

function probe(el: HTMLElement, name: string): HTMLElement {
  el.dataset["probe"] = name;
  return el;
}

/** The chat's bar: a permission card, the run bar, two queued steers, the open slash menu, the
 *  composer holding an upload chip over its pill row. */
function chatBar(): HTMLElement {
  const form = document.createElement("form");
  // The id carries the composer's tokens (`--composer-pill-pad`, 15-input.css).
  form.id = "prompt-form";
  form.className = "bottom-bar";
  const dock = box("div", "decision-dock", form, "");
  probe(box("div", "dock-card dock-permission", dock, "Allow git push?"), "permission card");
  const runHost = probe(box("div", "run-bar-host", form, ""), "run bar");
  box("li", "run-bar-row", box("ul", "run-bar", runHost, ""), "");
  const steers = box("ul", "steer-stack", form, "");
  for (const n of [1, 2]) {
    const row = probe(box("li", "steer-row", steers, `queued ${String(n)}`), `steer ${String(n)}`);
    row.dataset["state"] = "sent";
    // At rest: the entry rises 4px from below (`@starting-style`), which is motion, not a gap.
    row.style.transition = "none";
  }
  box("li", "slash-item", probe(box("ul", "slash-menu", form, ""), "slash menu"), "/compact");
  const prompt = probe(box("div", "prompt-box", form, ""), "composer");
  box("li", "attachment-pill", box("ul", "attachment-row", prompt, ""), "notes.md").dataset[
    "chip"
  ] = "";
  box("button", "pill", box("div", "prompt-pills", prompt, ""), "auto").dataset["pill"] = "";
  return form;
}

/** The run tab's bar: the run dock, a permission card, two queued steers, the composer. */
function runTabBar(): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "run-bottom-bar bottom-bar";
  probe(box("div", "ev-dock", box("div", "ev-dock-host", bar, ""), ""), "run dock");
  const dock = box("div", "decision-dock", bar, "");
  probe(box("div", "dock-card dock-permission", dock, "Allow git push?"), "permission card");
  const steers = box("ul", "steer-stack", bar, "");
  for (const n of [1, 2]) {
    const row = probe(box("li", "steer-row", steers, `queued ${String(n)}`), `steer ${String(n)}`);
    row.dataset["state"] = "sent";
    // At rest: the entry rises 4px from below (`@starting-style`), which is motion, not a gap.
    row.style.transition = "none";
  }
  const composer = box("form", "run-composer", bar, "");
  probe(box("div", "prompt-box run-composer-box", composer, ""), "composer");
  return bar;
}

beforeAll(() => {
  style = mountAppCSS();
  bars = [chatBar(), runTabBar()];
  for (const b of bars) {
    b.style.inlineSize = "900px";
    document.body.append(b);
  }
});

afterAll(() => {
  for (const b of bars) {
    b.remove();
  }
  style.remove();
});

const probes = (bar: HTMLElement): HTMLElement[] => [
  ...bar.querySelectorAll<HTMLElement>("[data-probe]"),
];

describe("the bottom stack", () => {
  it("draws every box at the app's card radius", () => {
    const ref = document.createElement("div");
    ref.style.borderRadius = "var(--r)";
    document.body.append(ref);
    const want = getComputedStyle(ref).borderTopLeftRadius;
    ref.remove();
    expect(want).toBe("6px");
    for (const el of bars.flatMap(probes)) {
      expect(getComputedStyle(el).borderTopLeftRadius, el.dataset["probe"]).toBe(want);
    }
  });

  it("draws one border, one width and one colour, on every box", () => {
    const [first, ...rest] = bars.flatMap(probes).map((el) => {
      const cs = getComputedStyle(el);
      return `${cs.borderTopWidth} ${cs.borderTopStyle} ${cs.borderTopColor}`;
    });
    expect(first).toMatch(/^1px solid /);
    for (const b of rest) {
      expect(b).toBe(first);
    }
  });

  it("gives an upload chip the corner of the composer's pills under it", () => {
    const chip = bars[0]!.querySelector<HTMLElement>("[data-chip]")!;
    const pill = bars[0]!.querySelector<HTMLElement>("[data-pill]")!;
    expect(getComputedStyle(chip).borderTopLeftRadius).toBe(
      getComputedStyle(pill).borderTopLeftRadius,
    );
  });

  for (const [which, at] of [
    ["the chat's", 0],
    ["the run tab's", 1],
  ] as const) {
    it(`keeps one gap between neighbouring boxes in ${which} bar`, () => {
      const rects = probes(bars[at]!).map(
        (el) => [el.dataset["probe"], el.getBoundingClientRect()] as const,
      );
      for (let i = 1; i < rects.length; i++) {
        const gap = Math.round((rects[i]?.[1].top ?? 0) - (rects[i - 1]?.[1].bottom ?? 0));
        expect(gap, `above ${rects[i]?.[0] ?? ""}`).toBe(8);
      }
    });
  }
});
