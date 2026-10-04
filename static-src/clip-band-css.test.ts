// ---------------------------------------------------------------------------
// NO CLIP REGION MAY CONTAIN BLOCK-END PADDING.
//
// `overflow: hidden` clips at the PADDING box, so block-end padding INSIDE a clip
// region leaves a band the first overflowing line paints its ascenders and
// cap-tops into: the box says "N lines" and renders N plus a sliver. This app
// already knew the rule — 27-run-card.css carries a dated write-up of it for
// `.run-step-capture`, which fixed it by moving the trailing space from padding
// to a MARGIN — and two sites had missed it. Both were reproduced in real
// Chromium, Firefox and WebKit before being fixed (2026-09-18): `.rolling-output`
// in 14-tools.css and `.uip-tooltip` in 04-uip-skin.css.
//
// THE PER-ENGINE CUT FRACTIONS AND INK COUNTS ARE AT THOSE TWO RULES, not here.
// Each number justifies the declaration it sits beside, so it is maintained where
// a reader changing the shape will see it; restating it in this header would be a
// second copy with no way to notice the two disagreeing. What matters HERE is one
// consequence of the tooltip's reading: WebKit is the one engine that keeps
// `display: -webkit-box` (the other two compute `flow-root`) and clips the third
// line's ink entirely, so it cut 0.250 of a line at ZERO ink — a clean pixel read
// on WebKit is not a pass anywhere, which is why the BAND oracle below is what
// these cases assert.
//
// The two took DIFFERENT remedies, and the difference is the reusable part. The
// tooltip element is @cplieger/ui-primitives' while the rule is marotte's own
// skin, so it takes a transparent block-end BORDER: a border is outside the clip
// region and `background-clip` defaults to `border-box`, so the space survives
// and the border box does not move. The rolling bar owns its own markup, so it
// SPLITS the two jobs — chrome on the outer, the clamp on an inner element with
// no padding at all — which also removes the second half of its defect, a cap
// that summed the padding but not the border.
//
// TWO ORACLES PER SITE, because neither answers the other's question:
//
//   BAND — no line band may straddle the clip edge, where a band is the merged
//     vertical extent of `Range.getClientRects()` per text node. This is the
//     defect as a reader sees it, over real layout. It cannot say WHY, and it
//     OVER-REPORTS: `.entry-sub-clamp` (11-page-lists.css) reports a 1px
//     straddle in Chromium from `2lh` resolving to 31.2px against a
//     `clientHeight` of 31, with zero ink in all three engines, so a sub-1px
//     straddle needs a pixel read before it is believed. Both fixes here land
//     the clip on a line boundary exactly, so a straddle of ANY size is real.
//   MECHANISM — the element that clips declares no block-end padding. This is
//     the root cause as a declaration, so it holds for content this file does
//     not think to render, and it fails on a shape the band walk would pass
//     (padding restored while the content happens not to overflow).
//
// The PIXEL oracle is not here: a `Locator.screenshot` writes a PNG into the
// repo tree, and with the clip region's padding at zero the band has no area to
// scan. It lives in the read-only probe that produced the ink counts recorded at
// `.rolling-output` (14-tools.css) and `.uip-tooltip` (04-uip-skin.css), which
// screenshots all three engines outside the working tree.
//
// `.run-step-capture` is the NEGATIVE CONTROL and is asserted here too: it is the
// site that got this right, so a case that passes for every clamp in the app
// would be proving nothing about these two.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import modalsSrc from "./modals.ts?raw";
import { RollingOutput } from "./modals.js";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // Both subjects wrap, so a definite width is what gives a line box somewhere
  // to break. 560px is the install banner's measured width in Settings -> Git.
  host.style.inlineSize = "560px";
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

interface Band {
  top: number;
  bottom: number;
}

/** Line bands under `box`: one per rendered line, merged over the fragments a
 *  line is split into. `white-space: pre-wrap` plus `overflow-wrap: anywhere`
 *  yields a rect per fragment rather than per line, and
 *  `selectNodeContents(box)` collapses the lot into one bounding rect — so the
 *  walk is per TEXT NODE and the merge is on overlapping vertical extent. */
function textBands(box: HTMLElement): Band[] {
  const bands: Band[] = [];
  const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
  const range = document.createRange();
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    if ((node.textContent ?? "").trim() === "") {
      continue;
    }
    range.selectNodeContents(node);
    for (const rect of range.getClientRects()) {
      if (rect.height <= 0) {
        continue;
      }
      const open = bands[bands.length - 1];
      if (open !== undefined && rect.top < open.bottom - 0.5) {
        bands[bands.length - 1] = {
          top: Math.min(open.top, rect.top),
          bottom: Math.max(open.bottom, rect.bottom),
        };
        continue;
      }
      bands.push({ top: rect.top, bottom: rect.bottom });
    }
  }
  return bands;
}

/** Where `overflow: hidden` cuts: the element's PADDING-box bottom, which is its
 *  border-box bottom less its own block-end border. */
function clipEdge(el: HTMLElement): number {
  return el.getBoundingClientRect().bottom - parseFloat(getComputedStyle(el).borderBottomWidth);
}

/** The bands that are cut through by `el`'s clip edge, as `top..bottom` offsets
 *  from it, plus how many landed wholly above it. "No band BELOW the edge" is
 *  the wrong claim and fails on a correct fix: leaving whole lines below the cut
 *  is a clamp's entire job. */
function cut(el: HTMLElement, textHost: HTMLElement = el): { straddling: string[]; whole: number } {
  const edge = clipEdge(el);
  const bands = textBands(textHost);
  return {
    straddling: bands
      .filter((b) => b.top < edge - 0.5 && b.bottom > edge + 0.5)
      .map((b) => `${String(b.top - edge)}..${String(b.bottom - edge)}`),
    whole: bands.filter((b) => b.bottom <= edge + 0.5).length,
  };
}

function mount(node: HTMLElement): HTMLElement {
  host.replaceChildren(node);
  return node;
}

// ---------------------------------------------------------------------------
// SITE 1 — the tools-engine install-progress bar.
// ---------------------------------------------------------------------------

/** The bar as `mcp-panels.ts` builds it, inside the column flex container it
 *  puts it in (`.inline-install-banner`, 18-pages.css). */
function installBanner(): { banner: HTMLElement; bar: HTMLDivElement } {
  const bar = document.createElement("div");
  bar.className = "rolling-output hidden";
  bar.setAttribute("role", "log");
  const banner = document.createElement("div");
  banner.className = "inline-install-banner";
  banner.appendChild(bar);
  return { banner, bar };
}

/** Four SOURCE lines of real install output, the second of which wraps at this
 *  width: a release-download URL is 96 characters against the ~81 that fit, and
 *  `installToolAndWait` streams exactly these. The cut is CONDITIONAL on a wrap,
 *  because `RollingOutput.append` only ever puts `lines.slice(-4)` in the DOM,
 *  so a fifth SOURCE line never exists and four short lines are clean. */
const WRAPPING_OUTPUT = [
  "Resolving gh 2.101.0 from the aqua catalog",
  "Downloading https://github.com/cli/cli/releases/download/v2.101.0/gh_2.101.0_linux_amd64.tar.gz",
  "Verifying checksum",
  "Linking /config/tools/bin/gh",
];

/** Drives the REAL producer, so the wrapper the cap lives on is pinned to the
 *  builder rather than restated here: a bare text node caps nothing, and that is
 *  the one way to break this fix from the TypeScript side. */
function rollTo(bar: HTMLDivElement, lines: readonly string[]): HTMLElement {
  const roll = new RollingOutput(bar, "git-output-modal");
  for (const line of lines) {
    roll.append(line);
  }
  bar.classList.remove("hidden");
  const inner = bar.querySelector<HTMLElement>(".rolling-output-text");
  expect(inner, "RollingOutput.append emits the .rolling-output-text wrapper").not.toBeNull();
  if (inner === null) {
    throw new Error("unreachable: asserted above");
  }
  return inner;
}

describe(".rolling-output cuts no line of a job's output", () => {
  it("shows four whole lines and cuts none of them when a source line wraps", () => {
    const { banner, bar } = installBanner();
    mount(banner);
    const inner = rollTo(bar, WRAPPING_OUTPUT);

    // The premise, or this asserts nothing: four SOURCE lines that wrap to more
    // than four VISUAL lines is the shape the defect needs.
    expect(textBands(inner).length, "the output must overflow four lines").toBeGreaterThan(4);

    const { straddling, whole } = cut(inner);
    expect(straddling).toEqual([]);
    expect(whole, "the cap delivers the four lines it reserves").toBe(4);
  });

  it("puts the clip on an element with no block-end padding inside it", () => {
    const { banner, bar } = installBanner();
    mount(banner);
    const inner = rollTo(bar, WRAPPING_OUTPUT);

    // The mechanism, stated where it is decided. The inner element clips and has
    // no padding; the bar keeps the padding and does NOT clip, so that padding is
    // outside every clip region rather than merely balanced against it.
    expect(getComputedStyle(inner).paddingBlockEnd).toBe("0px");
    expect(getComputedStyle(inner).overflow).not.toBe("visible");
    expect(getComputedStyle(bar).overflow).toBe("visible");
    expect(parseFloat(getComputedStyle(bar).paddingBlockEnd)).toBeGreaterThan(0);
  });

  it("reserves the four lines before the output fills them", () => {
    // The bar must not grow as lines arrive, which is what the retired
    // `min-height` bought and what a bare line clamp does not. One line in, four
    // lines of box.
    const { banner, bar } = installBanner();
    mount(banner);
    const inner = rollTo(bar, ["Resolving gh 2.101.0"]);
    const bands = textBands(inner);
    expect(bands.length).toBe(1);
    const line = bands[0]!.bottom - bands[0]!.top;
    expect(inner.getBoundingClientRect().height).toBeGreaterThanOrEqual(4 * line - 0.5);
  });

  it("keeps the visible trailing gap the old padding gave it", () => {
    // The fix must not tighten the bar. The oracle is the distance from the last
    // SHOWN line to the bar's own bottom edge, against a second bar carrying the
    // PRE-FIX declarations inline — arithmetic over the box model would only
    // restate the fix. The clip element is a PARAMETER because it is what the fix
    // moved: before, the padded bar clipped; after, the inner text element does,
    // and filtering the legacy arm against the inner's own edge would count the
    // half-cut fifth line as shown.
    const gap = (bar: HTMLDivElement, inner: HTMLElement, clipEl: HTMLElement): number => {
      const edge = clipEdge(clipEl);
      const shown = textBands(inner)
        .map((b) => b.bottom)
        .filter((b) => b <= edge + 0.5);
      expect(shown.length).toBeGreaterThan(0);
      return bar.getBoundingClientRect().bottom - Math.max(...shown);
    };

    const fixed = installBanner();
    mount(fixed.banner);
    const fixedInner = rollTo(fixed.bar, WRAPPING_OUTPUT);
    const fixedGap = gap(fixed.bar, fixedInner, fixedInner);
    const line = textBands(fixedInner).map((b) => b.bottom - b.top)[0]!;

    const legacy = installBanner();
    mount(legacy.banner);
    const legacyInner = rollTo(legacy.bar, WRAPPING_OUTPUT);
    legacy.bar.style.maxHeight = "calc(4 * 1.5em + 2 * var(--sp-2))";
    legacy.bar.style.overflow = "hidden";
    legacyInner.style.minBlockSize = "0";
    legacyInner.style.display = "block";
    legacyInner.style.overflow = "visible";

    // WITHIN A LINE BOX, not identical, and the residual is the fix: the pre-fix
    // cap summed the padding but NOT the two 1px borders, so the bar was 2px
    // short of the four lines it claimed to reserve. Asserting equality would
    // fail on the correct fix; asserting nothing would let the bar grow by a
    // whole line unnoticed.
    const drift = Math.abs(gap(legacy.bar, legacyInner, legacy.bar) - fixedGap);
    expect(drift, `trailing gap moved ${String(drift)}px`).toBeLessThan(line);
  });

  it("clamps to the same count the producer slices to", () => {
    // The bar's four is ONE number in two languages, and the two halves count
    // different things: `append` keeps the last four SOURCE lines, the stylesheet
    // shows four VISUAL lines. They agree by design, and a drift is silent — a
    // slice of six against a clamp of four reserves four lines and hides two of
    // the six it kept, with no straddle for the cases above to see.
    //
    // NOT a row in clamp-line-count.test.ts: that file's contract is the
    // `[data-clamped]` clamps, whose count pairs a CSS rule with an `attachClamp`
    // constant, and this pairs a rule with an array slice. Its exhaustiveness
    // sweep keys on `[data-clamped]`, which this rule deliberately does not carry
    // (there is no opener — the whole bar clicks through to a modal).
    const sliced = /lines\.slice\(-(\d+)\)/.exec(
      modalsSrc.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " "),
    );
    expect(sliced, "modals.ts: no lines.slice(-N) found in RollingOutput").not.toBeNull();

    const body = ruleBody(loadCSS("14-tools.css"), ".rolling-output-text").replace(
      /\/\*[\s\S]*?\*\//g,
      " ",
    );
    // Every spelling in the rule, so the prefixed and standard twins cannot
    // half-move, and the reserve is checked against the same number as the cap.
    const declared = [
      ...body.matchAll(/((?:-webkit-)?line-clamp)\s*:\s*(\d+)/g),
      ...body.matchAll(/(min-block-size)\s*:\s*(\d+)lh/g),
    ].map((m) => ({ prop: m[1] ?? "", n: Number.parseInt(m[2] ?? "", 10) }));
    // The expected SET rather than a count, because the count that actually matters is
    // two of three — a half-moved clamp. This suite runs in Chromium only, where the
    // standard property wins, so dropping the PREFIXED twin breaks Firefox and WebKit
    // with every band case above still green; dropping the reserve lets the bar grow as
    // output arrives. Naming which spelling is missing is what makes such a failure
    // diagnosable.
    expect(
      declared.map((d) => d.prop).sort(),
      "css/14-tools.css .rolling-output-text must declare all three: both line-clamp " +
        "twins and the min-block-size reserve",
    ).toEqual(["-webkit-line-clamp", "line-clamp", "min-block-size"]);

    for (const found of declared) {
      expect(
        found.n,
        `modals.ts slices to ${sliced?.[1] ?? "?"} lines but ` +
          `css/14-tools.css .rolling-output-text declares ${found.prop}: ${String(found.n)}`,
      ).toBe(Number.parseInt(sliced?.[1] ?? "", 10));
    }
  });
});

// ---------------------------------------------------------------------------
// SITE 2 — every tooltip in the app. ONE size class (the marotte-ui ruling), so
// these cases drive `--tooltip-lines` rather than a second class.
// ---------------------------------------------------------------------------

/** Longer than the ~102 characters two lines hold. Real content: a mode-picker
 *  row's agent description is a paragraph and a tool-input file path was
 *  measured to 121 characters. */
const LONG_TIP =
  "Open the diff for internal/agent/bridge_coord.go and check whether the projection " +
  "still reads the same field after the cutover lands and the tail run reconciles it.";

function tooltip(lines?: number): HTMLElement {
  const tip = document.createElement("div");
  tip.className = "uip-tooltip";
  tip.setAttribute("role", "tooltip");
  tip.textContent = LONG_TIP;
  if (lines !== undefined) {
    tip.style.setProperty("--tooltip-lines", String(lines));
  }
  // The library positions it `fixed`; a definite corner keeps it in view so its
  // rects are the ones a reader would see.
  tip.style.insetBlockStart = "40px";
  tip.style.insetInlineStart = "40px";
  return tip;
}

describe(".uip-tooltip cuts no line of its own text", () => {
  it("cuts nothing at the shipped two-line clamp", () => {
    const tip = mount(tooltip());
    expect(textBands(tip).length, "the text must overflow two lines").toBeGreaterThan(2);

    const { straddling, whole } = cut(tip);
    expect(straddling).toEqual([]);
    expect(whole).toBe(2);
  });

  it("leaves the third line whole when three lines are allowed", () => {
    // The control the two-line case needs: it proves the band the clamp used to
    // cut is EMPTY because the clip moved, not because the text stopped
    // producing a third line at this width.
    const tip = mount(tooltip(3));
    expect(textBands(tip).length).toBeGreaterThan(2);

    const { straddling, whole } = cut(tip);
    expect(straddling).toEqual([]);
    expect(whole).toBe(3);
  });

  it("carries the trailing space as a border, outside the clip", () => {
    const tip = mount(tooltip());
    const cs = getComputedStyle(tip);
    expect(cs.paddingBlockEnd).toBe("0px");
    expect(parseFloat(cs.borderBlockEndWidth)).toBeGreaterThan(0);
    // The clip edge IS the content-box bottom, so no band can be half-painted;
    // the visible space lives between it and the border box.
    expect(clipEdge(tip)).toBeCloseTo(tip.getBoundingClientRect().bottom - 4, 1);
  });

  it("does not move the border box, so anchored placement is unaffected", () => {
    // `placeAnchored` measures the border box, so the fix is only free if that
    // box is unchanged. The oracle is a second tooltip carrying the pre-fix
    // declarations inline.
    const fixed = mount(tooltip()).getBoundingClientRect().height;

    const legacy = mount(tooltip());
    legacy.style.paddingBlockEnd = "4px";
    legacy.style.borderBlockEnd = "0";
    expect(legacy.getBoundingClientRect().height).toBeCloseTo(fixed, 1);
  });
});

// The border trick's own cost, and the one mode where it is not free. A source
// read cannot answer this: the defect is the UA's forcing of `border-color`, which
// no stylesheet read reproduces.
describe("the tooltip's border-as-spacing under forced colours", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ forcedColors: "active" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("keeps the trailing space invisible instead of painting a rule", () => {
    const tip = mount(tooltip());
    const cs = getComputedStyle(tip);
    // A COMPARISON, not a literal: the arm names `Canvas`, and what matters is
    // that it resolves to the same colour the UA forced the background to. Without
    // the arm this reads rgb(0, 0, 0) against rgb(255, 255, 255) in both engines.
    expect(cs.borderBlockEndColor).toBe(cs.backgroundColor);
    // And the clip has not moved back onto padding to get there, which is the
    // remedy that would put the half-line cut back in this one mode.
    expect(cs.paddingBlockEnd).toBe("0px");
    expect(parseFloat(cs.borderBlockEndWidth)).toBeGreaterThan(0);
  });

  it("cuts no line here either", () => {
    const tip = mount(tooltip());
    expect(textBands(tip).length).toBeGreaterThan(2);
    expect(cut(tip).straddling).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// The site that got it right, so the two above are not passing for free.
// ---------------------------------------------------------------------------

describe(".run-step-capture, the negative control", () => {
  it("still carries its trailing space as a margin and cuts nothing", () => {
    const cap = document.createElement("div");
    cap.className = "run-step-capture";
    cap.textContent = WRAPPING_OUTPUT.join("\n");
    const row = document.createElement("div");
    row.className = "run-step";
    row.appendChild(cap);
    mount(row);

    expect(getComputedStyle(cap).paddingBlockEnd).toBe("0px");
    expect(parseFloat(getComputedStyle(cap).marginBlockEnd)).toBeGreaterThan(0);
    expect(textBands(cap).length).toBeGreaterThan(2);
    expect(cut(cap).straddling).toEqual([]);
  });
});
