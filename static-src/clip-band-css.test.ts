// No clip region may contain block-end padding: `overflow: hidden` clips at the padding box, so the next line paints
// its ascenders into the band. Sites: `.rolling-output` (14-tools.css), `.uip-tooltip` (04-uip-skin.css); the
// per-engine numbers live at those rules. Two oracles: BAND (no merged line band straddles the clip edge) and
// MECHANISM (the clipping element declares no block-end padding). `.run-step-capture` is the negative control.

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
  // 560px is the install banner's measured width in Settings -> Git; a definite width gives the lines a place to wrap.
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

/**
 * Per text node, merged on vertical overlap: `pre-wrap` + `overflow-wrap: anywhere` yields a rect per fragment, and
 * `selectNodeContents` collapses them to one.
 */
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

/** Bands cut by `el`'s clip edge, plus how many lie wholly above it. Whole lines below the cut are a clamp's job. */
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

function installBanner(): { banner: HTMLElement; bar: HTMLDivElement } {
  const bar = document.createElement("div");
  bar.className = "rolling-output hidden";
  bar.setAttribute("role", "log");
  const banner = document.createElement("div");
  banner.className = "inline-install-banner";
  banner.appendChild(bar);
  return { banner, bar };
}

/**
 * Four source lines, the second wrapping at this width (96 chars vs ~81). The cut needs a wrap, because
 * `RollingOutput.append` keeps only the last four.
 */
const WRAPPING_OUTPUT = [
  "Resolving gh 2.101.0 from the aqua catalog",
  "Downloading https://github.com/cli/cli/releases/download/v2.101.0/gh_2.101.0_linux_amd64.tar.gz",
  "Verifying checksum",
  "Linking /config/tools/bin/gh",
];

/** Drives the real producer: a bare text node caps nothing. */
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

    expect(getComputedStyle(inner).paddingBlockEnd).toBe("0px");
    expect(getComputedStyle(inner).overflow).not.toBe("visible");
    expect(getComputedStyle(bar).overflow).toBe("visible");
    expect(parseFloat(getComputedStyle(bar).paddingBlockEnd)).toBeGreaterThan(0);
  });

  it("reserves the four lines before the output fills them", () => {
    // The bar must not grow as lines arrive: one line in, four lines of box.
    const { banner, bar } = installBanner();
    mount(banner);
    const inner = rollTo(bar, ["Resolving gh 2.101.0"]);
    const bands = textBands(inner);
    expect(bands.length).toBe(1);
    const line = bands[0]!.bottom - bands[0]!.top;
    expect(inner.getBoundingClientRect().height).toBeGreaterThanOrEqual(4 * line - 0.5);
  });

  it("keeps the visible trailing gap the old padding gave it", () => {
    // Oracle: gap from the last shown line to the bar's bottom, against a bar carrying the naive declarations inline.
    // The clip element is a parameter because the two shapes clip on different elements.
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

    // Within a line box, not equal: the naive cap omits the two 1px borders, so it is 2px short.
    const drift = Math.abs(gap(legacy.bar, legacyInner, legacy.bar) - fixedGap);
    expect(drift, `trailing gap moved ${String(drift)}px`).toBeLessThan(line);
  });

  it("clamps to the same count the producer slices to", () => {
    // The bar's four is one number in two languages: `append`'s source-line slice and the stylesheet's visual-line
    // clamp; a drift is silent. Not in clamp-line-count.test.ts, whose contract is the `[data-clamped]` clamps.
    const sliced = /lines\.slice\(-(\d+)\)/.exec(
      modalsSrc.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " "),
    );
    expect(sliced, "modals.ts: no lines.slice(-N) found in RollingOutput").not.toBeNull();

    const body = ruleBody(loadCSS("14-tools.css"), ".rolling-output-text").replace(
      /\/\*[\s\S]*?\*\//g,
      " ",
    );
    const declared = [
      ...body.matchAll(/((?:-webkit-)?line-clamp)\s*:\s*(\d+)/g),
      ...body.matchAll(/(min-block-size)\s*:\s*(\d+)lh/g),
    ].map((m) => ({ prop: m[1] ?? "", n: Number.parseInt(m[2] ?? "", 10) }));
    // The expected set, not a count: this suite runs in Chromium only, so a dropped prefixed twin breaks Firefox and
    // WebKit unseen.
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

// Site 2: every tooltip. One size class, so these cases drive `--tooltip-lines`.

/** Longer than the ~102 characters two lines hold. */
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
    // Control: the band is empty because the clip moved, not because the text stopped wrapping.
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
    expect(clipEdge(tip)).toBeCloseTo(tip.getBoundingClientRect().bottom - 4, 1);
  });

  it("does not move the border box, so anchored placement is unaffected", () => {
    // `placeAnchored` measures the border box, so the clip must leave it unchanged.
    const fixed = mount(tooltip()).getBoundingClientRect().height;

    const legacy = mount(tooltip());
    legacy.style.paddingBlockEnd = "4px";
    legacy.style.borderBlockEnd = "0";
    expect(legacy.getBoundingClientRect().height).toBeCloseTo(fixed, 1);
  });
});

// Forced colours force `border-color`, which no source read reproduces.
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
    // A comparison, not a literal: the border must resolve to the colour the UA forced the background to.
    expect(cs.borderBlockEndColor).toBe(cs.backgroundColor);
    expect(cs.paddingBlockEnd).toBe("0px");
    expect(parseFloat(cs.borderBlockEndWidth)).toBeGreaterThan(0);
  });

  it("cuts no line here either", () => {
    const tip = mount(tooltip());
    expect(textBands(tip).length).toBeGreaterThan(2);
    expect(cut(tip).straddling).toEqual([]);
  });
});

// Negative control: the site that already got it right.

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
