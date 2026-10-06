import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// Nested corners: a child against its container's corner is concentric only at
// `inner = outer - outer-border - inset`. Pinned because the arithmetic lives in a
// `calc()` on a custom property and a missing declaration fails SILENTLY:
// `var(--menu-radius)` with nothing behind it is an invalid value and the corner
// renders SQUARE.

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;";
document.body.appendChild(host);

// These assertions read COMPUTED radii, so the app's own stylesheet has to be in
// the document — a text-level check could not catch the silent failure this file
// exists for, because `var(--menu-radius)` with no declaration is valid CSS text
// and only resolves to nothing at computed-value time.
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

describe("the three menu surfaces derive one corner from one shape", () => {
  // All three are a 1px border with a --sp-1 list inset, so one derivation serves
  // them. The container is --r-lg (12px): at the --r (6px) it used to carry, the
  // concentric inner radius is 6 - 1 - 4 = 1px, which is square to the eye — and
  // the items shipped 6px (identical to their container) or a `calc(var(--r) / 2)`
  // halving that landed 2px over. It also closes the pair the user named, since
  // the composer is --r-lg and the top-right menu was --r.
  it.each([
    ["popup", "popup-list", "popup-item"],
    ["tab-context-menu", "", "tab-context-item"],
    ["chip-menu", "", "chip-menu-item"],
  ])("%s holds a concentric %s", (outerCls, midCls, innerCls) => {
    const outer = document.createElement("div");
    outer.className = outerCls;
    let mount: HTMLElement = outer;
    if (midCls !== "") {
      const mid = document.createElement("div");
      mid.className = midCls;
      outer.appendChild(mid);
      mount = mid;
    }
    const inner = document.createElement("button");
    inner.className = innerCls;
    mount.appendChild(inner);
    host.appendChild(outer);

    const o = radiusOf(outer);
    const i = radiusOf(inner);
    expect(o, "the container takes the 12px container rung").toBeCloseTo(12, 1);
    // 12 outer - 1px border - 4px inset. A missing custom property renders 0.
    expect(i, `${innerCls} must be concentric, and non-zero`).toBeCloseTo(7, 1);
  });
});

describe("a 24px icon button takes the ladder's small rung", () => {
  // The ladder reserves --r-sm for a 16-24px box. Each of these shipped --r
  // (6px), and for `.tab-close` that was its own container's radius, so the
  // button's arc was identical to the tab holding it.
  // A circle shipped on `.attachment-close` for one build, to dodge a nesting the
  // composer's row height makes unsatisfiable, and it was reported: it was the app's
  // only circular control, and one × that is not the shape of every other × is the
  // worse defect. The rung is the answer for all five (15-input.css records the
  // residual), so this list is a CONSISTENCY claim across the family rather than five
  // independent facts — a member leaving it is what the report was.
  it.each([
    "tab-close",
    "shell-header-btn",
    "turn-action-btn",
    "native-rule-remove",
    "attachment-close",
  ])("%s is 4px, not 6px", (cls) => {
    const b = document.createElement("button");
    b.className = cls;
    host.appendChild(b);
    expect(radiusOf(b)).toBeCloseTo(4, 1);
  });

  // The SHAPE half of the same claim, which the 4px assertion alone cannot make: a
  // percentage radius resolves against the box, so `parseFloat` on "50%" yields 50 and
  // every pixel comparison above passes for a circle too, so assert the corner is not
  // a circle by its own test.
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
    // It declared --r-md (8px) inside a 7px --pill-radius pill. At 14px tall, 8px
    // already exceeds half the height, so the browser clamped it to a stadium and
    // the declared rung never rendered — the fix is to say what it renders as.
    const badge = document.createElement("span");
    badge.className = "pill-badge";
    badge.textContent = "3";
    host.appendChild(badge);
    const r = radiusOf(badge);
    const h = badge.getBoundingClientRect().height;
    expect(r, "a stadium's radius is at least half the box's height").toBeGreaterThanOrEqual(h / 2);
  });

  it("derives the chat-options input from the form it sits in", () => {
    // It shipped --r (6px) inside a --card-radius (3px) form: twice as round as
    // its container.
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
  // BOTH cards that mount this footer, because the one rule serves both and the
  // delegate's ledger is also a member of 40-a11y.css's inset focus-ring list.
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
    // The control paints a box centred in the band rather than filling it
    // (29-turns.css), so the concentric rule has no single answer: an inset of 4px on
    // one edge and 0 on the other is unsatisfiable at any radius. One rung is the
    // whole vocabulary, the same 4px its siblings at the row's other end paint.
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
  it("leaves a code block's pre square so its wrapper draws every corner", () => {
    // The prose skin gave the pre `--r` on all four, and the wrapper's
    // `overflow: hidden` does not square a child's own corners — it removes only
    // what falls outside — so the top pair painted a notch of card background at
    // each end of the head's seam. An override on `.code-wrap > pre` cannot fix
    // it: `.message.assistant :where(pre)` scores (0,2,0) against its (0,1,1),
    // which is why the radius is deleted at the prose skin instead.
    const msg = document.createElement("div");
    msg.className = "message assistant";
    const wrap = document.createElement("div");
    wrap.className = "code-wrap";
    const head = document.createElement("div");
    head.className = "code-head";
    const pre = document.createElement("pre");
    wrap.append(head, pre);
    msg.appendChild(wrap);
    host.appendChild(msg);

    expect(radiusOf(wrap), "the wrapper draws the box").toBeCloseTo(6, 1);
    const s = getComputedStyle(pre);
    for (const [corner, value] of Object.entries({
      "top-left": s.borderTopLeftRadius,
      "top-right": s.borderTopRightRadius,
      "bottom-right": s.borderBottomRightRadius,
      "bottom-left": s.borderBottomLeftRadius,
    })) {
      expect(parseFloat(value), `pre ${corner} must be square`).toBe(0);
    }
  });
});
