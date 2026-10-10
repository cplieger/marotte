// An action inside a dense list row takes the dense tier, not `--btn-h`: a `.btn-small` (36px) would otherwise drive a
// `.list-row` (floor 24px) and read as its subject. A standalone `.btn-small` keeps `--btn-h`; pinning only the first
// would pass for a rule shrinking every `.btn-small`. `data-pointer="fine"` is stated as a premise.
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** `--ctl-h-dense` and `--btn-h` on the fine tier, stated so a token retune fails here. */
const DENSE_PX = 32;
const BTN_PX = 36;
/** `--hit-floor` and `--ctl-h` on the coarse tier, which share 44px. */
const COARSE_PX = 44;

let style: HTMLStyleElement;
let pointerWas: string | null;
const hosts: HTMLElement[] = [];

function track<T extends HTMLElement>(el: T): T {
  document.body.append(el);
  hosts.push(el);
  return el;
}

function rowWithAction(): { row: HTMLElement; btn: HTMLElement } {
  const row = document.createElement("div");
  row.className = "list-row tool-hit";
  const text = document.createElement("div");
  text.className = "tool-hit-text";
  const name = document.createElement("span");
  name.className = "list-row-name";
  name.textContent = "ripgrep";
  text.append(name);
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small list-row-enable";
  btn.textContent = "Install";
  row.append(text, btn);
  track(row);
  return { row, btn };
}

/** The Add modal's close button, the height every control on that surface agrees on. */
function modalIconButton(): HTMLElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "icon-btn";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  btn.append(svg);
  return track(btn);
}

/** The negative control: nothing scopes it to a row, so it keeps the full tier. */
function standaloneButton(): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "modal-actions";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small";
  btn.textContent = "Cancel";
  bar.append(btn);
  track(bar);
  return btn;
}

const h = (el: HTMLElement): number => el.getBoundingClientRect().height;

beforeAll(() => {
  style = mountAppCSS();
  pointerWas = document.documentElement.getAttribute("data-pointer");
  document.documentElement.setAttribute("data-pointer", "fine");
});

afterAll(() => {
  style.remove();
  if (pointerWas === null) {
    document.documentElement.removeAttribute("data-pointer");
  } else {
    document.documentElement.setAttribute("data-pointer", pointerWas);
  }
  for (const el of hosts.splice(0)) {
    el.remove();
  }
});

describe("a .btn-small inside a .list-row", () => {
  it("takes the dense tier, agreeing with the modal's own icon button", () => {
    const { btn } = rowWithAction();
    expect(h(btn), "an action inside a dense row is --ctl-h-dense").toBeCloseTo(DENSE_PX, 0);
    expect(h(btn), "and so it agrees with every other control on that surface").toBeCloseTo(
      h(modalIconButton()),
      0,
    );
  });

  it("keeps its row off the coarse tier", () => {
    // The reported symptom: a 36px button in a 24px-floor row rendered a 44px row.
    const { row } = rowWithAction();
    expect(h(row), "a fine-pointer row must not measure the touch tier").toBeLessThan(COARSE_PX);
  });

  it("leaves a standing .btn-small at the full-size tier", () => {
    // The control: without it the first case passes for a rule that shrank every .btn-small.
    expect(h(standaloneButton()), "a button that stands alone keeps --btn-h").toBeCloseTo(
      BTN_PX,
      0,
    );
  });

  it("still clears the touch floor on a coarse pointer", () => {
    // Why the rule reads `max()`: it outranks the zero-specificity hit-target floor, and a bare `--ctl-h-dense` would put
    // a 40px button beside the 44px `.list-row-btn`. Fine-tier only.
    document.documentElement.setAttribute("data-pointer", "coarse");
    try {
      const { btn } = rowWithAction();
      expect(h(btn), "a finger's action keeps the 44px target").toBeCloseTo(COARSE_PX, 0);
    } finally {
      document.documentElement.setAttribute("data-pointer", "fine");
    }
  });
});
