import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { releaseMarkdownIn, renderMarkdownInto } from "./markdown.js";

// Pinned because the arithmetic lives in a `calc()` on a custom property and a missing declaration
// fails SILENTLY: `var(--menu-radius)` with nothing behind it is an invalid value and the corner
// renders SQUARE.

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;";
document.body.appendChild(host);

// These assertions read COMPUTED radii, so the app's own stylesheet has to be in the document — a
// text-level check could not catch the silent failure this file exists for, because
// `var(--menu-radius)` with no declaration is valid CSS text and only resolves to nothing at
// computed-value time.
let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

function radiusOf(el: Element): number {
  return parseFloat(getComputedStyle(el).borderRadius);
}

describe("the two menu surfaces derive one corner from one shape", () => {
  // Both are a 1px border with a --sp-1 list inset, so one derivation serves them.
  it.each([
    ["tab-context-menu", "tab-context-item"],
    ["chip-menu", "chip-menu-item"],
  ])("%s holds a concentric %s", (outerCls, innerCls) => {
    const outer = document.createElement("div");
    outer.className = outerCls;
    const inner = document.createElement("button");
    inner.className = innerCls;
    outer.appendChild(inner);
    host.appendChild(outer);

    const o = radiusOf(outer);
    const i = radiusOf(inner);
    expect(o, "the container takes the 12px container rung").toBeCloseTo(12, 1);
    // 12 outer - 1px border - 4px inset. A missing custom property renders 0.
    expect(i, `${innerCls} must be concentric, and non-zero`).toBeCloseTo(7, 1);
  });
});

describe("a 24px icon button takes the ladder's small rung", () => {
  // The ladder reserves --r-sm for a 16-24px box. Each of these shipped --r (6px), and for
  // `.tab-close` that was its own container's radius, so the button's arc was identical to the tab
  // holding it.
  it.each([
    "tab-close",
    "turn-action-btn",
    "native-rule-remove",
    "attachment-close",
    "list-row-btn",
  ])("%s is 4px, not 6px", (cls) => {
    const b = document.createElement("button");
    b.className = cls;
    host.appendChild(b);
    expect(radiusOf(b)).toBeCloseTo(4, 1);
  });

  // The SHAPE half of the same claim, which the 4px assertion alone cannot make: a percentage
  // radius resolves against the box, so `parseFloat` on "50%" yields 50 and every pixel comparison
  // above passes for a circle too.
  it("keeps the attachment pill's remove control a rounded square, never a circle", () => {
    const pill = document.createElement("li");
    pill.className = "attachment-pill";
    const b = document.createElement("button");
    b.className = "icon-btn attachment-close";
    pill.appendChild(b);
    host.appendChild(pill);

    const box = b.getBoundingClientRect();
    expect(box.width, "the 24px box every other close button paints").toBeCloseTo(24, 1);

    const raw = getComputedStyle(b).borderTopLeftRadius.trim();
    const shorter = Math.min(box.width, box.height);
    const corner = raw.endsWith("%")
      ? (Number.parseFloat(raw) / 100) * shorter
      : Number.parseFloat(raw);
    expect(corner, "a radius at or past half the shorter side is a circle").toBeLessThan(
      shorter / 2,
    );
  });
});

describe("an inner box is never rounder than the box holding it", () => {
  it("makes the pill's count badge a stadium rather than a rounder rung", () => {
    // It declared --r-md (8px) inside a 7px --pill-radius pill. At 14px tall, 8px already exceeds
    // half the height, so the browser clamped it to a stadium and the declared rung never rendered
    // — the fix is to say what it renders as.
    const badge = document.createElement("span");
    badge.className = "pill-badge";
    badge.textContent = "3";
    host.appendChild(badge);
    const r = radiusOf(badge);
    const h = badge.getBoundingClientRect().height;
    expect(r, "a stadium's radius is at least half the box's height").toBeGreaterThanOrEqual(h / 2);
  });

  it("derives the chat-options input from the form it sits in", () => {
    // It shipped --r (6px) inside a --card-radius (3px) form: twice as round as its container.
    const form = document.createElement("form");
    form.className = "chat-opt-form";
    const input = document.createElement("input");
    input.className = "chat-opt-input";
    form.appendChild(input);
    host.appendChild(form);
    expect(radiusOf(input)).toBeLessThanOrEqual(radiusOf(form) + 0.5);
  });
});

describe("the footer's ledger keeps ONE radius on every corner", () => {
  // BOTH cards that mount this footer, because the one rule serves both and the delegate's ledger
  // is also a member of 40-a11y.css's inset focus-ring list.
  it.each([
    ["turn", (card: HTMLElement, footer: HTMLElement) => card.appendChild(footer)],
    [
      "subagent-block",
      (card: HTMLElement, footer: HTMLElement) => {
        const foot = document.createElement("div");
        foot.className = "subagent-foot";
        foot.appendChild(footer);
        card.appendChild(foot);
        footer.classList.add("subagent-footer");
      },
    ],
  ] as const)("gives the ledger the ladder's rung inside a %s", (cls, nest) => {
    const card = document.createElement("div");
    card.className = cls;
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    const ledger = document.createElement("button");
    ledger.type = "button";
    ledger.className = "turn-ledger-summary";
    footer.appendChild(ledger);
    nest(card, footer);
    host.appendChild(card);

    const s = getComputedStyle(ledger);
    for (const [corner, value] of Object.entries({
      "top-left": s.borderTopLeftRadius,
      "top-right": s.borderTopRightRadius,
      "bottom-right": s.borderBottomRightRadius,
      "bottom-left": s.borderBottomLeftRadius,
    })) {
      expect(parseFloat(value), `ledger ${corner} takes the small rung`).toBeCloseTo(4, 1);
    }
  });
});

describe("a box with a header at its top has ONE radius owner", () => {
  // A nested corner is concentric: inner radius = outer radius - border - inset.
  it("squares a code block's pre under its bar and rounds it concentric below", () => {
    const msg = document.createElement("div");
    msg.className = "message assistant";
    host.appendChild(msg);
    renderMarkdownInto(msg, "```go\npackage main\n```\n");
    const block = msg.querySelector(".cp-code") as HTMLElement;
    const pre = block.querySelector(":scope > pre") as HTMLElement;

    const outer = radiusOf(block);
    expect(outer, "the block draws the box").toBeGreaterThan(0);
    const inner = outer - parseFloat(getComputedStyle(block).borderBottomWidth);
    const s = getComputedStyle(pre);
    expect(parseFloat(s.borderTopLeftRadius), "under the bar").toBe(0);
    expect(parseFloat(s.borderTopRightRadius), "under the bar").toBe(0);
    expect(parseFloat(s.borderBottomLeftRadius), "concentric").toBeCloseTo(inner, 1);
    expect(parseFloat(s.borderBottomRightRadius), "concentric").toBeCloseTo(inner, 1);
    releaseMarkdownIn(msg);
  });
});
