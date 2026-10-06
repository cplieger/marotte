// Every transcript box seam is a FILL step or a 1px rule, never both. A TABLE with both halves
// asserted, read as SOURCE facts (`.tool-call`'s `content-visibility: auto` makes layout
// measure fallbacks).
import { describe, expect, it } from "vitest";

import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";

/**
 * A seam and its side of the split. `owner` draws (or would draw) the rule, spelled as the
 * source spells it so a reworded selector fails loudly. `fillOwner` is the region below:
 * `step: true` declares its own fill, `step: false` shows its card's.
 */
interface Seam {
  readonly name: string;
  readonly sheet: string;
  readonly owner: string;
  readonly prop: string;
  readonly fillOwner: string;
  readonly step: boolean;
  readonly why: string;
}

const SEAMS: readonly Seam[] = [
  {
    name: "turn card: header → body",
    sheet: "29-turns.css",
    owner: ".turn-header",
    prop: "border-bottom",
    fillOwner: ".turn-header",
    step: true,
    why: "the band is --c-bg-tertiary over a --c-turn-body card",
  },
  {
    name: "turn card: body → foot",
    sheet: "29-turns.css",
    owner: ".turn-footer",
    prop: "border-top",
    fillOwner: ".turn-footer",
    step: true,
    why: "the same band at the other end",
  },
  {
    name: "code block: head → pre",
    sheet: "13-messages.css",
    owner: ".code-head",
    prop: "border-block-end",
    fillOwner: ".code-head",
    step: true,
    why: "the box-rung head sits over the pre's well",
  },
  {
    name: "tool card: claim row → details",
    sheet: "14-tools.css",
    owner: ".tool-details",
    prop: "border-block-start",
    fillOwner: ".tool-details",
    step: true,
    why: "the machine-text wash is a step against the card's own fill",
  },
  {
    name: "tool group: header → row, and row → row",
    sheet: "14-tools.css",
    owner: ".tool-group-body > .tool-call",
    prop: "border-top",
    fillOwner: ".tool-group-body > .tool-call",
    step: false,
    why: "a member flattens to `background: none`, so every row shares the group's fill",
  },
  {
    name: "delegate card: header → body",
    sheet: "14-tools.css",
    owner: ".subagent-body",
    prop: "border-block-start",
    fillOwner: ".subagent-body",
    step: false,
    why: "the body declares no fill and shows the card's",
  },
  {
    name: "delegate card: body → foot",
    sheet: "14-tools.css",
    owner: ".subagent-foot",
    prop: "border-block-start",
    fillOwner: ".subagent-foot",
    step: false,
    why: "the one card foot of the three with no band of its own",
  },
  {
    name: "run card: head → body",
    sheet: "27-run-card.css",
    owner: ".run-body",
    prop: "border-block-start",
    fillOwner: ".run-body",
    step: false,
    why: "a nested header takes no band, so head and body share the box fill",
  },
  {
    name: "run card: body → foot",
    sheet: "27-run-card.css",
    owner: ".run-foot",
    prop: "border-block-start",
    fillOwner: ".run-foot",
    step: false,
    why: "the nested feet — this and the delegate's — are box-toned, only the turn's is a band",
  },
  {
    name: "run card: body → outputs",
    sheet: "27-run-card.css",
    owner: ".run-outputs",
    prop: "border-block-start",
    fillOwner: ".run-outputs",
    step: false,
    why: "a list inside the body, separating two regions of one fill",
  },
];

/**
 * A rule's declarations, COMMENTS STRIPPED: several rules' comments name the very property
 * they omit, which a raw scan would read as a declaration.
 */
function declarations(sheet: string, selector: string): string {
  return ruleBody(loadCSS(sheet), selector).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/**
 * Whether a rule's body (nested `&` blocks included) declares the property with a painting
 * value; `0` or `none` cancels.
 */
function draws(body: string, prop: string): boolean {
  const at = new RegExp(`(^|[;{\\s])${prop}\\s*:\\s*([^;]+)`, "g");
  for (const m of body.matchAll(at)) {
    const value = (m[2] ?? "").trim();
    if (value !== "" && value !== "0" && value !== "none") {
      return true;
    }
  }
  return false;
}

/** Whether a rule declares a fill that paints. `background: none` is the tool
 *  group's way of saying "show the box behind me", so it counts as no fill. */
function declaresFill(body: string): boolean {
  const m = /(^|[;{\s])background\s*:\s*([^;]+)/.exec(body);
  if (m === null) {
    return false;
  }
  const value = (m[2] ?? "").trim();
  return value !== "" && value !== "none" && value !== "transparent";
}

describe("a box seam draws a rule only where no fill change marks it", () => {
  const stepped = SEAMS.filter((s) => s.step);
  const flat = SEAMS.filter((s) => !s.step);

  it("has both halves of the split populated, or the table proves nothing", () => {
    // The differential for the whole file: a table that drifted to one side would let
    // every row below pass while saying nothing about the rule being tested.
    expect(stepped.length, "seams a fill change marks").toBeGreaterThan(1);
    expect(flat.length, "seams where the rule is the only boundary").toBeGreaterThan(1);
  });

  it.each(stepped)("draws no rule at $name, because $why", ({ sheet, owner, prop, fillOwner }) => {
    expect(
      draws(declarations(sheet, owner), prop),
      `${owner} in ${sheet} must not draw ${prop}`,
    ).toBe(false);
    expect(
      declaresFill(declarations(sheet, fillOwner)),
      `${fillOwner} must paint its own fill, or removing the rule lost the boundary`,
    ).toBe(true);
  });

  it.each(flat)("keeps its rule at $name, because $why", ({ sheet, owner, prop, fillOwner }) => {
    expect(draws(declarations(sheet, owner), prop), `${owner} in ${sheet} must draw ${prop}`).toBe(
      true,
    );
    expect(
      declaresFill(declarations(sheet, fillOwner)),
      `${fillOwner} must NOT paint a fill, or its rule is redundant too`,
    ).toBe(false);
  });

  it("leaves the two dashed FRAMES alone, which are not seams", () => {
    // `.turn-notice` and `.boundary` draw a dashed rule opening a framed banner against a
    // filled region: by the same test the rule is the only boundary, outside the population.
    expect(draws(declarations("29-turns.css", ".turn-notice"), "border-block-start")).toBe(true);
    expect(draws(declarations("13-messages.css", ".boundary"), "border-block-start")).toBe(true);
  });
});
