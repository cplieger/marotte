// The turn header dot exists only where the TAB STRIP cannot be reached: above the drawer breakpoint
// the strip's dot says it better, below it the strip is off-canvas (50-mobile.css). Two claims, two
// tests: the hide APPLIES against the assembled cascade (computed), and its breakpoint EQUALS the
// drawer's (source; the drawer is a pure media query). The mobile side runs in an IFRAME, since the
// page viewport is pinned at 1280x720.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

const BREAKPOINT = "width <= 48rem";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** A running turn's header dot under the real card's ancestry (selectors are descendant-scoped).
 *  BOTH attributes `updateTurnHeader` writes: `data-severity` (hue, `display`) and `data-outcome`. */
function mountDot(doc: Document, outcome: TurnOutcome): HTMLElement {
  const header = doc.createElement("div");
  header.className = "turn-header";
  header.dataset["outcome"] = outcome;
  header.dataset["severity"] = severityOf(outcome);
  const row = doc.createElement("div");
  row.className = "turn-head-row";
  const dot = doc.createElement("span");
  dot.className = "turn-dot";
  row.appendChild(dot);
  header.appendChild(row);
  doc.body.replaceChildren(header);
  return dot;
}

describe("the turn header dot at desktop width", () => {
  it("does not render, at the real viewport against the real cascade", () => {
    const dot = mountDot(document, "running");
    expect(window.innerWidth).toBeGreaterThan(768);
    expect(getComputedStyle(dot).display).toBe("none");
  });

  it("does not render for any outcome, running included", () => {
    const outcomes: TurnOutcome[] = ["running", "completed", "interrupted", "failed", "unknown"];
    for (const outcome of outcomes) {
      const dot = mountDot(document, outcome);
      expect(getComputedStyle(dot).display, outcome).toBe("none");
    }
  });
});

describe("the turn header dot below the drawer breakpoint", () => {
  let frame: HTMLIFrameElement;
  let doc: Document;

  beforeAll(() => {
    frame = document.createElement("iframe");
    // 700px is inside 48rem (768px at the 16px root size the app never changes),
    // and the height is irrelevant to a width query.
    frame.width = "700";
    frame.height = "400";
    document.body.appendChild(frame);
    const inner = frame.contentDocument;
    if (inner === null) {
      throw new Error("iframe has no contentDocument");
    }
    doc = inner;
    const sheet = doc.createElement("style");
    sheet.textContent = style.textContent;
    doc.head.appendChild(sheet);
  });

  afterAll(() => {
    frame.remove();
  });

  it("renders for a running turn", () => {
    expect(doc.defaultView?.innerWidth).toBeLessThanOrEqual(768);
    const dot = mountDot(doc, "running");
    expect(getComputedStyle(dot).display).not.toBe("none");
  });

  it("still hides on a clean outcome", () => {
    // `.turn-header[data-outcome="completed"] .turn-dot` (0,3,0) beats the media rule's (0,1,0), so a
    // completed turn stays dotless inside the query too.
    const dot = mountDot(doc, "completed");
    expect(getComputedStyle(dot).display).toBe("none");
  });
});

describe("the gate's breakpoint", () => {
  it("is the same width the sidebar drawer uses", () => {
    // A media query cannot read a custom property, so the coupling is the number in both PRELUDES.
    const turns = ruleContaining(loadCSS("29-turns.css"), ".turn-dot", BREAKPOINT);
    expect(turns.body).toMatch(/display:\s*block/u);

    const drawer = ruleContaining(loadCSS("50-mobile.css"), '[id="sidebar"]', BREAKPOINT);
    expect(drawer.body).toContain("translateX(-100%)");
  });

  it("hides the dot at top level, so the media block is the whole reveal", () => {
    // The scope argument is mandatory: `.turn-dot` now names a rule in both
    // places, and the helper requires exactly one match.
    const base = ruleContaining(loadCSS("29-turns.css"), ".turn-dot", "top");
    expect(base.body).toMatch(/display:\s*none/u);
  });
});
