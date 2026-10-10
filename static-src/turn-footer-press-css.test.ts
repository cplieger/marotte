// States are read off the CSSOM: synthetic hover drives no recalc and `CSS.forcePseudoState` is a
// devtools call. The walk descends nested rules: `CSSStyleRule` carries `cssRules` without being a
// `CSSGroupingRule`.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const mcp = loadCSS("61-mcp-tools.css");
const turns = loadCSS("29-turns.css");

let style: HTMLStyleElement;
let host: HTMLElement;

interface Pair {
  ledger: HTMLElement;
  action: HTMLElement;
}

/** A turn footer with a control at each end. The action button is the plain `Copy as
 *  text` rather than the `…` summary, which collapses the group behind itself below
 *  40rem; this page is 1280 wide. */
function mountPair(): Pair {
  const card = document.createElement("div");
  card.className = "turn";
  const footer = document.createElement("div");
  footer.className = "turn-footer";

  const ledger = document.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = document.createElement("span");
  info.className = "turn-ledger-info";
  info.appendChild(glyph());
  const mark = document.createElement("span");
  mark.className = "turn-ledger-glyph";
  const word = document.createElement("span");
  word.className = "turn-ledger-text";
  word.textContent = "Failed";
  const fact = document.createElement("span");
  fact.className = "turn-fact";
  fact.textContent = "20 commands";
  ledger.append(info, mark, word, fact);

  const slot = document.createElement("span");
  slot.className = "turn-actions-buttons";
  const more = document.createElement("details");
  more.className = "turn-actions-more";
  const summary = document.createElement("summary");
  summary.className = "turn-action-btn turn-action-more";
  summary.appendChild(glyph());
  const group = document.createElement("span");
  group.className = "turn-actions-group";
  const action = document.createElement("button");
  action.type = "button";
  action.className = "turn-action-btn";
  action.setAttribute("aria-label", "Copy as text");
  action.appendChild(glyph());
  const label = document.createElement("span");
  label.className = "turn-action-label";
  label.textContent = "Copy as text";
  action.appendChild(label);
  group.appendChild(action);
  more.append(summary, group);
  slot.appendChild(more);

  footer.append(ledger, slot);
  card.appendChild(footer);
  host.replaceChildren(card);
  return { ledger, action };
}

function glyph(): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  return svg;
}

interface Writer {
  selector: string;
  value: string;
}

/** Every rule in the document that writes `prop`, in document order, with a nested
 *  rule's `&` resolved against its parent list. */
function writersOf(prop: string): Writer[] {
  const out: Writer[] = [];
  const walk = (list: CSSRuleList, parent: string): void => {
    for (const rule of list) {
      const nested = (rule as CSSRule & { cssRules?: CSSRuleList }).cssRules;
      if (!(rule instanceof CSSStyleRule)) {
        if (nested !== undefined) {
          walk(nested, parent);
        }
        continue;
      }
      const selector =
        parent === "" ? rule.selectorText : rule.selectorText.replaceAll("&", `:is(${parent})`);
      const value = rule.style.getPropertyValue(prop);
      if (value !== "") {
        out.push({ selector, value });
      }
      if (nested !== undefined) {
        walk(nested, selector);
      }
    }
  };
  for (const sheet of document.styleSheets) {
    try {
      walk(sheet.cssRules, "");
    } catch {
      continue;
    }
  }
  return out;
}

/** Every rule writing `prop` FOR THIS STATE that applies to `el`, matched with the
 *  state's pseudo-class stripped. Scoped to the state's own writers: a rest rule matches
 *  the stripped selector too, and rest is compared through computed style instead. */
function stateWriters(el: Element, prop: string, state: string): Writer[] {
  return writersOf(prop).filter((w) => {
    if (!w.selector.includes(state)) {
      return false;
    }
    try {
      return el.matches(w.selector.replaceAll(state, ""));
    } catch {
      return false;
    }
  });
}

/** One rule paints `prop` in `state`, and it is the same rule for both controls. A COUNT
 *  rather than a comparison of the winning value: a copy in an earlier bundle slice is
 *  inert only until somebody raises its specificity. */
function expectSharedState(pair: Pair, prop: string, state: string): Writer {
  const forLedger = stateWriters(pair.ledger, prop, state);
  const forAction = stateWriters(pair.action, prop, state);
  const where = `${prop} in ${state}`;
  expect(
    forLedger.map((w) => w.selector),
    `${where}: exactly one rule paints the ledger`,
  ).toHaveLength(1);
  expect(
    forAction.map((w) => w.selector),
    `${where}: the action button takes that same rule`,
  ).toEqual(forLedger.map((w) => w.selector));
  const [only] = forLedger;
  if (only === undefined) {
    throw new Error(`no writer for ${where}`);
  }
  return only;
}

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  host.remove();
  style.remove();
});

describe("the two ends of the footer share one press box", () => {
  it("declares it once, as a selector list naming both controls", () => {
    // `ruleContaining` requires exactly one rule listing the ledger, so a second
    // declaration in this file fails here rather than being silently picked.
    const box = ruleContaining(mcp, ".turn-ledger-summary", "top");
    const hover = ruleContaining(mcp, ".turn-ledger-summary:hover", "top");
    expect(box.selector, "the resting box").toContain(".turn-action-btn");
    expect(box.selector, "Rewind takes the same box").toContain(".turn-rewind");
    expect(hover.selector, "the hover fill").toContain(".turn-action-btn:hover");
    expect(hover.selector, "and the same hover").toContain(".turn-rewind:hover");
    // `height` is in that list because the PAINTED box is what the two ends of the row
    // have to agree on: a per-control height is how the toggle came to paint a
    // full-height slab beside a 24px pill.
    for (const decl of ["height", "background", "border-radius", "color", "cursor", "transition"]) {
      expect(box.body, `the shared rule owns ${decl}`).toContain(`${decl}:`);
    }
    // The shared rule in 61-mcp-tools.css owns why `box-shadow` is in that list.
    expect(box.body, "the transition names box-shadow").toContain("box-shadow ");
  });

  it("leaves the ledger's and Rewind's own rules holding geometry only", () => {
    // 29-turns.css owns the band's height and the card's ink gutter; every fill, ink and
    // transition is the shared rule's, so a copy here would silently overlap it.
    for (const control of [".turn-ledger-summary", ".turn-rewind"]) {
      const geometry = ruleContaining(turns, control, "top");
      for (const decl of ["background", "border", "color", "cursor", "transition"]) {
        expect(geometry.body, `${control}: ${decl} belongs to the shared rule`).not.toContain(
          `${decl}:`,
        );
      }
      expect(turns, `${control}: no bespoke focus ring`).not.toContain(`${control}:focus-visible`);
      expect(turns, `${control}: no bespoke hover`).not.toMatch(
        new RegExp(`\\${control}:hover\\s*\\{`, "u"),
      );
    }
  });

  it("resolves one rest treatment on both controls", () => {
    const pair = mountPair();
    const l = getComputedStyle(pair.ledger);
    const a = getComputedStyle(pair.action);
    for (const prop of [
      "background-color",
      "border-top-width",
      "border-top-style",
      "color",
      "cursor",
      "border-top-left-radius",
      "border-bottom-left-radius",
      "height",
      "transition-property",
      "transition-duration",
      "transition-timing-function",
    ] as const) {
      expect(a.getPropertyValue(prop), prop).toBe(l.getPropertyValue(prop));
    }
    expect(l.transitionProperty, "the press wash animates").toContain("box-shadow");
    // WIDTH is the one property that differs, asserted so a pair agreeing above by both
    // being empty could not pass: the box spans the `i`, the outcome word and the fact,
    // and the height above is what says the wash is the same PILL at both ends.
    expect(pair.ledger.getBoundingClientRect().width).toBeGreaterThan(
      pair.action.getBoundingClientRect().width,
    );
    expect(pair.ledger.contains(pair.ledger.querySelector(".turn-fact"))).toBe(true);
  });

  it("resolves one hover, press and focus treatment on both controls", () => {
    const pair = mountPair();
    expect(expectSharedState(pair, "background", ":hover").value).toBe("var(--c-hover)");
    expectSharedState(pair, "color", ":hover");
    // 03-base.css's universal `:active` floor is what paints it.
    expect(expectSharedState(pair, "box-shadow", ":active").value).toContain("var(--c-press)");
    // The ring is 40-a11y.css's zero-specificity floor for both controls.
    expectSharedState(pair, "outline", ":focus-visible");
    expectSharedState(pair, "outline-offset", ":focus-visible");
  });
});
