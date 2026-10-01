import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// FORCED COLORS: a drawn box needs an EDGE, because its fill is not its own.
//
// `.skeleton`'s only paint is a background, and forced-colors mode forces an
// author background to `Canvas` — so without the arm every bar in the app paints
// invisibly against the canvas and the loading state renders as blank space.
// Measured on the identical construction one file over: 40-a11y.css's caret rule
// records "measured white-on-white" for a drawn box with no glyph and no border.
//
// Two halves, plus the two controls that make the rendered half mean anything.
// The SOURCE half also carries an OWNERSHIP assertion, which is the one case here
// that can fail on a legitimate future change: 30-utilities.css is MANIFEST
// position 33 and 40-a11y.css is 34, so a competing `.skeleton` rule there wins
// an equal-specificity tie. Its message carries the reason, because a bare grep
// count diagnoses nothing.
//
// The rendered halves are read as COMPARISONS rather than against literals: the
// two system colours' resolved values are the platform's and not this app's to
// hardcode.
// ---------------------------------------------------------------------------

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

function bar(): HTMLDivElement {
  const el = document.createElement("div");
  el.className = "skeleton skeleton-line";
  stage.appendChild(el);
  return el;
}

describe("the forced-colors arm, in source", () => {
  it("declares a border INSIDE the forced-colors query", () => {
    const arm = ruleContaining(loadCSS("30-utilities.css"), ".skeleton", "forced-colors");
    expect(arm.body).toMatch(/(^|[\s;])border\s*:/);
  });

  it("is the only forced-colors answer for this element", () => {
    // The WIDE form rather than `ruleContaining(loadCSS("40-a11y.css"),
    // ".skeleton", "forced-colors")` throwing. That narrower form is the actual
    // HAZARD — a competing arm at MANIFEST position 34 beating ours at 33 — and
    // is the shape to adopt if this turns out noisy, but the wide form also
    // catches a `.skeleton` rule added to that file's reduced-motion sweep or to
    // any other block in it, which is the same collision one query over.
    expect(
      loadCSS("40-a11y.css").toLowerCase(),
      "the forced-colors arm lives in 30-utilities.css beside the rule it modifies (design §2.5); a .skeleton rule in 40-a11y.css means the two files' owners have collided",
    ).not.toContain("skeleton");
  });
});

describe("no emulation, which is what makes the outlined reading mean anything", () => {
  it("draws no border on a real bar in normal mode", () => {
    // The control that fails a design declaring the border unconditionally,
    // which would put a 1px outline on every placeholder in the app.
    const el = bar();
    expect(getComputedStyle(el).borderTopWidth).toBe("0px");
  });
});

describe("the forced-colors arm, rendered under the emulated feature", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ forcedColors: "active" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("gives a real bar an edge distinguishable from its own fill", () => {
    const el = bar();
    const style = getComputedStyle(el);

    expect(style.borderTopWidth).toBe("1px");
    expect(style.borderTopStyle).toBe("solid");
    // A comparison, not a literal: the border resolves to the platform's
    // `CanvasText` and the background to its `Canvas`, and the fix is that the
    // two DIFFER. Under emulation this is a reading about the emulated palette
    // rather than about a real forced-colours OS session — see the run record.
    expect(style.borderTopColor).not.toBe(style.backgroundColor);
  });

  it("keeps the pulse running, so §2.2's arm is not read as covering this mode", () => {
    // The arm adds no `animation: none` and no `opacity: 1`: forced colours and
    // reduced motion are independent preferences, so a forced-colours reader has
    // not asked to lose the ambient loading signal.
    const el = bar();
    const [anim] = el.getAnimations();
    expect(anim?.effect?.getTiming().iterations).toBe(Infinity);
  });
});

describe("the border costs no layout", () => {
  afterAll(async () => {
    await resetA11yMedia();
  });

  it("leaves a real bar's box identical in both modes", async () => {
    const el = bar();

    const plain = el.getBoundingClientRect();
    await emulateA11yMedia({ forcedColors: "active" });
    const forced = el.getBoundingClientRect();

    // `box-sizing: border-box` is app-wide (02-reset.css), so the border comes
    // out of the content box and the outer box does not move. Asserted rather
    // than assumed, because a reader who saw the loading state reflow would be
    // watching the reserved slot change size.
    expect(forced.height).toBe(plain.height);
    expect(forced.width).toBe(plain.width);
    // The premise: the emulation really was in force for the second reading.
    expect(getComputedStyle(el).borderTopWidth).toBe("1px");
  });
});
