// A TOOLTIP POINTS AT INK, NOT AT A HIT BOX. Three triggers are rows whose box is the layout's (the
// turn footer's ledger button and changed-file rows, the git Changes rows), so a centred tip landed
// ~250px from the ink. `data-tooltip-anchor` is DERIVED from the `attribute` option `tooltip.ts`
// configures, so this measures through `./tooltip.js`. Requires a ui-primitives build whose
// tooltip reads the mark; a failure with marks present means that pin is behind.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { FileChange } from "./types.js";

vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: `navigate.js` imports
  // these names and Browser Mode links for real rather than reading properties off
  // a namespace object.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: undefined,
}));

const { buildTurnFooter, updateTurnFooter } = await import("./fundamentals/turn-footer.js");
const { initTooltips } = await import("./tooltip.js");
const { _resetForTest } = await import("@cplieger/ui-primitives/tooltip");

let style: HTMLStyleElement;
let card: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  card.remove();
});

/** `--content-max-w` rather than the viewport, because the row's slack is what the column's width
 *  decides. */
function mountFooter(files: Record<string, FileChange>): HTMLElement {
  card = document.createElement("div");
  card.className = "turn";
  card.style.inlineSize = "800px";
  // The centred column's real position: at x=0 the viewport clamp would hide the property.
  card.style.marginInlineStart = "240px";
  document.body.appendChild(card);
  const d = {
    outcome: "completed" as const,
    elapsedMs: 12_000,
    credits: 0.5,
    changedFiles: files,
  };
  const footer = buildTurnFooter(d);
  card.appendChild(footer);
  updateTurnFooter(footer, d);
  footer.dataset["info"] = "open";
  return footer;
}

/** Hover a trigger the way a pointer does — over a child, so the delegated
 *  controller has to resolve the trigger itself — and return the tip it shows. */
function hover(trigger: HTMLElement, over: Element = trigger): HTMLElement {
  vi.useFakeTimers();
  over.dispatchEvent(new Event("pointerover", { bubbles: true }));
  vi.advanceTimersByTime(600);
  const tip = document.querySelector<HTMLElement>(".uip-tooltip:not(.is-leaving)");
  if (tip === null) {
    throw new Error("no tooltip shown");
  }
  return tip;
}

/** The tip's centre on the inline axis, from the position the controller wrote. */
function tipCenterX(tip: HTMLElement): number {
  return parseFloat(tip.style.left) + tip.getBoundingClientRect().width / 2;
}

function centerX(el: Element): number {
  const r = el.getBoundingClientRect();
  return r.left + r.width / 2;
}

/** The tip is centred ON `mark`, not on `trigger`. EQUALITY, not containment: the ledger
 *  button's width is the footer grid's, so overlap could pass by accident. The trigger is asserted
 *  bigger than the ink, or the property would be vacuous. */
function expectAnchoredTo(tip: HTMLElement, mark: Element, trigger: HTMLElement): void {
  const m = mark.getBoundingClientRect();
  const t = trigger.getBoundingClientRect();
  const cx = tipCenterX(tip);
  expect(t.width, "the trigger is wider than its ink, which is the premise").toBeGreaterThan(
    m.width + 4,
  );
  expect(
    cx,
    `tip centre ${cx.toFixed(1)} against ink centre ${centerX(mark).toFixed(1)} ` +
      `and the row's own ${centerX(trigger).toFixed(1)}`,
  ).toBeCloseTo(centerX(mark), 0);
}

describe("the turn footer's ledger row", () => {
  it("points its tooltip at the `i`, not at the middle of the band", () => {
    const footer = mountFooter({});
    const ledger = footer.querySelector<HTMLElement>(".turn-ledger-summary");
    const info = footer.querySelector<HTMLElement>(".turn-ledger-info");
    expect(ledger).not.toBeNull();
    expect(info).not.toBeNull();
    if (ledger === null || info === null) {
      return;
    }
    initTooltips();
    expect(ledger.getAttribute("data-tooltip")).toBe("Show turn details");
    // Entering over the glyph, which is what a pointer reaching for the `i` does.
    expectAnchoredTo(hover(ledger, info), info, ledger);
  });
});

describe("the turn footer's changed-file rows", () => {
  it("points each row's tooltip at the path it opens", () => {
    const footer = mountFooter({
      "marotte/internal/translate/streaming_tools.go": { lines_added: 6, lines_removed: 2 },
    });
    const row = footer.querySelector<HTMLElement>(".turn-file-row");
    expect(row).not.toBeNull();
    if (row === null) {
      return;
    }
    const path = row.querySelector<HTMLElement>(".turn-file-path");
    expect(path).not.toBeNull();
    if (path === null) {
      return;
    }
    initTooltips();
    expect(row.getAttribute("data-tooltip")).toContain("Open the diff for");
    expectAnchoredTo(hover(row, path), path, row);
  });
});
