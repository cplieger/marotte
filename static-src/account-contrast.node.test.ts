// The sidebar footer's measured floors over two backdrops: the trigger (`.account-btn`)
// on `--c-bg-secondary`, and the credits row (`.pill-account`) inside the status card,
// whose surface is tinted by `--status-color` (composed here per hue). Interaction
// backdrops use css-contrast.py's `over()`, alpha compositing in sRGB as a browser
// paints, never a `color-mix()`. The maths is shelled out, not reimplemented, and every
// expression is read from the stylesheet so a CSS change cannot pass silently. Node env.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { ruleBody } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/**
 * Read with `node:fs`: in vitest's node project a `*.css?raw` import resolves to "", so
 * every sweep would pass over nothing.
 */
const shell = readFileSync(join(here, "css", "10-shell-app.css"), "utf8");
const input = readFileSync(join(here, "css", "15-input.css"), "utf8");

interface Measurement {
  theme: string;
  ratio: number;
}

function pair(fg: string, bg: string): Measurement[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", ratio: Number(cols[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

const ratio = (theme: string, fg: string, bg: string): number => {
  const row = pair(fg, bg).find((r) => r.theme === theme);
  expect(row, `${theme} row for ${fg} on ${bg}`).toBeDefined();
  return row?.ratio ?? 0;
};

function decl(body: string, prop: string): string {
  const m = new RegExp(`(?:^|[;{\\s])${prop}:\\s*([^;]+);`).exec(
    body.replace(/\/\*[\s\S]*?\*\//g, " "),
  );
  expect(m, `${prop} is declared`).not.toBeNull();
  return (m?.[1] ?? "").trim();
}

function nested(body: string, selector: string): string {
  const clean = body.replace(/\/\*[\s\S]*?\*\//g, " ");
  const at = clean.indexOf(selector);
  expect(at, `${selector} is declared`).toBeGreaterThan(-1);
  const open = clean.indexOf("{", at);
  const close = clean.indexOf("}", open);
  return clean.slice(open + 1, close);
}

/** The composite a browser paints when `wash` sits on `surface`. */
const over = (wash: string, surface: string): string => `over(${wash}, ${surface})`;

/**
 * The card's own backdrop, read from `.pill-status-content` with one connection hue
 * substituted for `--status-color`.
 */
function cardBackdrop(hue: string): string {
  // `ruleBody` keys on the exact selector line (`ruleContaining` matches substrings of
  // selector lists). Whitespace is collapsed because prettier wraps this declaration.
  const bg = decl(ruleBody(input, ".pill-status-content"), "background")
    .replace(/\s+/gu, " ")
    .replace(/\(\s+/gu, "(")
    .replace(/\s+\)/gu, ")")
    .trim();
  // The inner `var()` carries its own fallback, so the substitution takes the WHOLE
  // `var(--status-color, …)` call.
  const substituted = bg.replace(/var\(--status-color,\s*var\([^)]*\)\s*\)/u, `var(--c-${hue})`);
  expect(substituted, `the ${hue} tint substitutes cleanly`).not.toContain("--status-color");
  expect(substituted, "and it is still the card's own mix").toContain("color-mix(in srgb");
  // The parens have to balance, or the script resolves a different expression than the
  // card paints and every figure below is about a colour nothing renders.
  const opens = (substituted.match(/\(/gu) ?? []).length;
  const closes = (substituted.match(/\)/gu) ?? []).length;
  expect(opens, `the ${hue} expression's parens balance`).toBe(closes);
  return substituted;
}

/** The three connection hues, read off `.status-dot`'s own rules rather than named
 *  here — the base rule plus its two settled states. */
function markInks(): { state: string; ink: string }[] {
  const body = ruleBody(shell, ".status-dot");
  return [
    { state: "connecting", ink: decl(body, "background") },
    { state: "connected", ink: decl(nested(body, "&.connected"), "background") },
    { state: "error", ink: decl(nested(body, "&.error"), "background") },
  ];
}

const FOOTER = "var(--c-bg-secondary)";
const HOVER_TOKEN = "var(--c-hover)";
const PRESS_TOKEN = "var(--c-press)";
const HUES = ["green", "yellow", "red"] as const;

// The script lives outside static-src, and Stryker's sandbox copies static-src
// alone — so its absence is a skip, the same rule the sibling floors use.
describe.skipIf(!existsSync(script))("the trigger's floors", () => {
  it("holds all three connection marks to 3:1 at rest and hovered", () => {
    // WCAG 1.4.11: the mark is a graphical object a reader has to pick out of the row.
    // The hover fill is read out of the rule, so changing that token moves this.
    const btn = ruleBody(shell, ".account-btn");
    const hoverFill = decl(nested(btn, "&:hover"), "background");
    for (const { state, ink } of markInks()) {
      for (const [label, bg] of [
        ["rest", FOOTER],
        ["hovered", hoverFill],
      ] as const) {
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the ${state} mark ${label} (${ink} on ${bg})`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    }
  });

  it("holds the address to 4.5:1 in every state, pressed included", () => {
    // WCAG 1.4.3: `--fs-sm` regular-weight text, so the large-text exception does not apply.
    const btn = ruleBody(shell, ".account-btn");
    const hoverFill = decl(nested(btn, "&:hover"), "background");
    const ink = decl(ruleBody(shell, ".sidebar-email"), "color");
    for (const bg of [FOOTER, hoverFill, over(PRESS_TOKEN, FOOTER), over(PRESS_TOKEN, hoverFill)]) {
      for (const m of pair(ink, bg)) {
        expect(m.ratio, `${m.theme}: the address on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe.skipIf(!existsSync(script))("the pressed mark, a residual the ink ramp closed", () => {
  // Every status ink is authored against the hovered box (01-tokens.css "SEEDS: ink"), so
  // the pressed mark meets the 1.4.11 floor. `over()` is the instrument.
  it.each([
    ["green", "dark"],
    ["green", "light"],
    ["yellow", "dark"],
    ["yellow", "light"],
    ["red", "dark"],
    ["red", "light"],
  ] as const)("holds the %s mark to 3:1 pressed, in %s", (hue, theme) => {
    const bg = over(PRESS_TOKEN, FOOTER);
    expect(ratio(theme, `var(--c-${hue})`, bg), `${theme} ${hue} on ${bg}`).toBeGreaterThanOrEqual(
      3,
    );
  });

  it("takes the app-wide press rather than a bespoke one, which is WHY it was a residual", () => {
    // The absence of a bespoke `:active` IS the decision: the footer's two controls answer the
    // pointer alike under 03-base.css's universal press. This fails on a local "fix".
    const btn = ruleBody(shell, ".account-btn").replace(/\/\*[\s\S]*?\*\//g, " ");
    expect(btn, "no bespoke press on the trigger").not.toMatch(/&:active/u);
  });
});

describe.skipIf(!existsSync(script))("the credits row's floors, over the TINTED card", () => {
  it("holds the plan ink to 4.5:1 hovered and pressed, on every tint", () => {
    // 4.5:1, not 3:1: the plan line is a 13px regular-weight label. The LIFTED ink is read
    // from the rule's state blocks, so deleting the lift fails here.
    const row = ruleBody(input, ".pill-account");
    const hoverInk = decl(nested(row, "&:hover"), "color");
    const pressInk = decl(nested(row, "&:active"), "color");
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, ink, wash] of [
        ["hovered", hoverInk, HOVER_TOKEN],
        ["pressed", pressInk, PRESS_TOKEN],
      ] as const) {
        const bg = over(wash, card);
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the plan ink ${label} over the ${hue} card (${ink})`,
          ).toBeGreaterThanOrEqual(4.5);
        }
      }
    }
  });

  it("would FAIL that floor unlifted, which is why the ladder moves the ink", () => {
    // The measurement the lift rests on, so the lift is not a preference. The resting
    // ink is what the row INHERITS from the card, read off `.pill-expand-content`.
    const resting = decl(ruleBody(input, ".pill-expand-content"), "color");
    const failures: string[] = [];
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, wash] of [
        ["hovered", HOVER_TOKEN],
        ["pressed", PRESS_TOKEN],
      ] as const) {
        for (const m of pair(resting, over(wash, card))) {
          if (m.ratio < 4.5) {
            failures.push(`${m.theme} ${hue} ${label}`);
          }
        }
      }
    }
    // The unlifted ink fails only dark's three pressed tints, so the press in dark is what
    // requires the lift.
    expect(failures.sort(), "the unlifted ink's failures").toEqual(
      ["dark green pressed", "dark red pressed", "dark yellow pressed"].sort(),
    );
  });

  it("holds the external mark to 3:1 WHILE INHERITING, in every state", () => {
    // The mark declares no `color` and inherits the row's, the only value clearing 3:1 in all
    // four states.
    const row = ruleBody(input, ".pill-account");
    const resting = decl(ruleBody(input, ".pill-expand-content"), "color");
    const hoverInk = decl(nested(row, "&:hover"), "color");
    const pressInk = decl(nested(row, "&:active"), "color");
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      for (const [label, ink, bg] of [
        ["rest", resting, card],
        ["hovered", hoverInk, over(HOVER_TOKEN, card)],
        ["pressed", pressInk, over(PRESS_TOKEN, card)],
      ] as const) {
        for (const m of pair(ink, bg)) {
          expect(
            m.ratio,
            `${m.theme}: the external mark ${label} over the ${hue} card`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    }
  });

  it("declares no colour for that mark, so the value above cannot drift", () => {
    // A named ink would break the floor: `--c-text-tertiary` reads 2.71:1 pressed over the
    // dark tinted card.
    const tertiary = "var(--c-text-tertiary)";
    const card = cardBackdrop("yellow");
    expect(
      ratio("dark", tertiary, over(PRESS_TOKEN, card)),
      "the rejected ink, pressed in dark",
    ).toBeLessThan(3);
    // And the rule genuinely declares nothing for it.
    expect(input.replace(/\/\*[\s\S]*?\*\//g, " "), "no .pill-account-out rule").not.toMatch(
      /\.pill-account-out[^{]*\{/u,
    );
  });

  it("keeps the wash VISIBLE over the tinted card, which is why the opaque rung lost", () => {
    // The ladder half a contrast floor cannot see: the opaque `.pill` rung steps only
    // ~1.07-1.16 over this card (an invisible hover); the gated wash steps ~1.59 / ~1.35.
    for (const hue of HUES) {
      const card = cardBackdrop(hue);
      const washStep = ratio("dark", over(HOVER_TOKEN, card), card);
      const opaqueStep = ratio("dark", "var(--c-bg-tertiary)", card);
      expect(washStep, `${hue}: the wash steps visibly`).toBeGreaterThan(1.4);
      expect(opaqueStep, `${hue}: the opaque rung would not`).toBeLessThan(1.2);
      expect(washStep, `${hue}: and the wash is the louder of the two`).toBeGreaterThan(opaqueStep);
    }
  });
});
