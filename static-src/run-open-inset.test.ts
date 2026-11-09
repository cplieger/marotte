// The run card's Open run link takes the turn footer's control box: its hover fill is the footer
// button's height and corner, with the same air above and below it inside its row, on both tiers.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildRunCard } from "./fundamentals/run-card.js";

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});
afterAll(() => {
  style.remove();
  document.documentElement.removeAttribute("data-pointer");
});
afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function mount(node: HTMLElement): void {
  const host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;inline-size:48rem;";
  host.appendChild(node);
  document.body.appendChild(host);
  hosts.push(host);
}

interface Box {
  readonly height: number;
  readonly airAbove: number;
  readonly airBelow: number;
  readonly radius: string;
}

/** The control's box against its row's padding box (inside the row's own border). */
function boxIn(control: HTMLElement, row: HTMLElement): Box {
  const c = control.getBoundingClientRect();
  const r = row.getBoundingClientRect();
  const top = r.top + row.clientTop;
  return {
    height: c.height,
    airAbove: c.top - top,
    airBelow: top + row.clientHeight - c.bottom,
    radius: getComputedStyle(control).borderTopLeftRadius,
  };
}

/** A turn footer holding one action button, the reference box. */
function footerButton(): Box {
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const slot = document.createElement("span");
  slot.className = "turn-actions-buttons";
  const b = document.createElement("button");
  b.type = "button";
  b.className = "turn-action-btn";
  slot.appendChild(b);
  footer.appendChild(slot);
  mount(footer);
  return boxIn(b, footer);
}

function openRun(): Box {
  const view = buildRunCard("wf-1", "recipe", () => undefined);
  mount(view.root);
  const foot = view.root.querySelector<HTMLElement>(".run-foot");
  const open = view.root.querySelector<HTMLElement>(".run-open");
  expect(foot, "the card has its foot").not.toBeNull();
  expect(open, "the foot has its link").not.toBeNull();
  return boxIn(open!, foot!);
}

describe.each(["fine", "coarse"] as const)("the Open run link on the %s tier", (tier) => {
  it("is the turn footer button's box, inset in its row by the same air", () => {
    document.documentElement.dataset["pointer"] = tier;
    const ref = footerButton();
    expect(ref.airAbove, "the reference has air above").toBeGreaterThan(0);
    expect(openRun()).toEqual(ref);
  });
});
