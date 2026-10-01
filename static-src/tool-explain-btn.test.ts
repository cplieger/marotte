// ---------------------------------------------------------------------------
// The failed card's "Explain this error" trigger: its contract and its geometry.
//
// One file because both halves need the same real path (`updateToolCall` against a
// real `buildToolCard` card) and the same harness (the shipped stylesheet mounted,
// the card IN the viewport so `content-visibility: auto` has rendered it).
//
// EVERY CLAIM HERE IS NUMERIC OR A HIT TEST, because none of them is visible in
// source. The control is an icon-only button, so its accessible name is the only
// thing identifying it; it is a CIRCLE, so `width === height` plus a 50% radius is
// the shape rather than a class name; it floats in a strip the card reserves, so
// "does not overlay the output" is a rect comparison; and its target is grown by an
// `::after` expander, which reads identically in the cascade whether or not some
// ancestor `overflow` has clipped it away — so only `elementFromPoint` can answer.
//
// `messages-tools-status.test.ts` keeps the GATING (a failure with output offers it,
// a blank output and a declined tool do not). This file owns the shape.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";
import type { ToolCall } from "./types.js";

// The signal layer only, so a card is reachable without dragging the chat store in.
// It hands back the snapshot it was given, so a mount's effect sees
// `next === lastApplied` and does not re-enter the update path.
vi.mock("./store-signals.js", () => ({
  toolCallSigKey: vi.fn((chatID: string, toolID: string) => `${chatID}\u0000${toolID}`),
  ensureToolCallSig: vi.fn((_chat: string, _id: string, tc: unknown) => ({ value: tc })),
  // Present-but-undefined so real-ESM linking succeeds: other modules in this graph
  // import these names and no path here calls them.
  bumpLane: undefined,
  laneSig: undefined,
  toolCallSigs: undefined,
  peekToolCallSig: undefined,
  ensureEntryTextSig: undefined,
  writeEntryText: undefined,
  clearEntryTextSig: undefined,
  clearTurnSigs: undefined,
}));

// The real chrome reaches `scroll.ts`, which resolves the transcript scroller at
// module load and throws on a missing id — so these exist before the dynamic
// imports below are evaluated.
for (const id of ["messages", "messages-wrap", "messages-wrap-outer", "chat-view"]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}
const scrollBottom = document.createElement("div");
scrollBottom.id = "scroll-bottom";
scrollBottom.appendChild(document.createElement("span"));
document.body.appendChild(scrollBottom);

const { mountAppCSS, loadCSS, allRules } = await import("./__test-helpers__/css-rules.js");
const { buildToolCard } = await import("./tool-card.js");
const { updateToolCall, initToolCallbacks } = await import("./messages-tools.js");

const LABEL = "Explain this error";
const EXPLANATION = "The linker could not find libfoo.";

/** A transcript-width column, IN the viewport: a `.tool-call` parked off-screen is
 *  SKIPPED by `content-visibility: auto`, so its own box reports the containment
 *  estimate rather than its contents. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  initToolCallbacks({
    pushBind: () => {
      /* the bind's disposer is the transcript's business, not this file's */
    },
    refreshGroupHeader: () => {
      /* no group here */
    },
    // The module default resolves "", which never swaps — so the result case is
    // unreachable without a stub.
    explainError: () => Promise.resolve(EXPLANATION),
  });
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.removeAttribute("data-touched");
});

afterEach(() => {
  host.replaceChildren();
  // Browser Mode isolates per FILE, so a `:root` attribute set by one case leaks
  // into every later one.
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.removeAttribute("data-touched");
});

/** Wait for Chromium to decide the mounted card is near the viewport. `.tool-call`
 *  carries `content-visibility: auto`, and until that lands the card's OWN box is
 *  the `contain-intrinsic-size` fallback while its descendants still report real
 *  geometry — the asymmetry that makes an outer-box assertion quietly read a
 *  constant. Measured in this Chromium at the second frame after the mount; three
 *  for margin, counted in lifecycle passes so load does not move it. */
async function rendered(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}

function frame(id: string, tc: Partial<ToolCall>): ToolCall {
  return { id, title: "Execute", kind: "execute", ts: 0, ...tc } as ToolCall;
}

/** A real shell card that ran LIVE and then failed with output — the one state that
 *  offers this trigger, reached through the production update path so the button is
 *  built by `applyStatusUpdate` rather than by the fixture. */
async function failedCard(id: string): Promise<HTMLDivElement> {
  const card = buildToolCard({
    id,
    title: "Execute",
    kind: "execute",
    status: "in_progress",
    live: true,
    input: { command: "go build ./..." },
  });
  host.appendChild(card);
  updateToolCall(
    card,
    frame(id, {
      status: "failed",
      output: "# github.com/cplieger/marotte\nld: cannot find -lfoo\nexit status 2\n",
    }),
    "c1",
  );
  await rendered();
  return card;
}

function trigger(card: HTMLElement): HTMLButtonElement {
  const btn = card.querySelector<HTMLButtonElement>(".tool-explain-btn");
  expect(btn, "the fixture has to actually offer the trigger").not.toBeNull();
  return btn!;
}

/** A token's length in pixels for the tier currently set. A custom property reads
 *  back as its raw text, so only the engine can turn `max(var(--sp-2), calc(…))`
 *  into a number — which is what makes a token retune move the assertion instead of
 *  breaking it. Resolved INSIDE the card, because `--explain-inset` is declared
 *  there and inherited. */
function tokenPx(within: HTMLElement, value: string): number {
  const probe = document.createElement("div");
  probe.style.blockSize = value;
  probe.style.inlineSize = value;
  within.appendChild(probe);
  const v = probe.getBoundingClientRect().width;
  probe.remove();
  return v;
}

describe("the trigger says what it is", () => {
  it("carries the accessible name AND the app's styled tooltip, from one string", async () => {
    const card = await failedCard("ex-name");
    const btn = trigger(card);
    // ICON-ONLY, so the name is the whole of what a screen reader has. A locator by
    // NAME has to resolve, not just one by class: a class-only assertion passes over
    // a button no reader can identify.
    expect(card.querySelector(`.tool-explain-btn[aria-label="${LABEL}"]`)).toBe(btn);
    // `data-tooltip`, never a native `title`: it is the app's delegated styled
    // tooltip and the only form that publishes `aria-describedby`.
    expect(btn.getAttribute("data-tooltip")).toBe(LABEL);
    expect(btn.hasAttribute("title")).toBe(false);
    expect(btn.type).toBe("button");
  });

  it("draws the registry's SPARKLE, not a text character", async () => {
    const card = await failedCard("ex-glyph");
    const btn = trigger(card);
    const glyph = btn.querySelector("svg");
    expect(glyph, "the glyph is an SVG the icon registry owns").not.toBeNull();
    // The TIER class is what sizes it (`--icon-ui`, 03-base.css), so a per-path size
    // override would be a defect rather than a tuning.
    expect(glyph?.classList.contains("ic-ui")).toBe(true);
    expect(btn.textContent).toBe("");
    // An emoji would satisfy "there is a mark" and fail this: it renders from a
    // colour font at its own weight and baseline, which is why `ICON_SPARKLE` exists.
    expect(btn.innerHTML).not.toContain("\u2728");
  });

  it("is reachable by keyboard", async () => {
    const card = await failedCard("ex-focus");
    const btn = trigger(card);
    btn.focus();
    expect(document.activeElement).toBe(btn);
    // The ring is 40-a11y.css's zero-specificity floor, and the corner inset clears
    // its reach inside the card's clip. The sweep below asserts nothing here
    // suppresses it; a rendered `:focus-visible` cannot be forced in this sidecar.
    expect(getComputedStyle(btn).outlineStyle).not.toBe("none");
  });
});

describe("the result replaces the trigger in place", () => {
  it("drops the trigger's name and tooltip with it", async () => {
    const card = await failedCard("ex-swap");
    const btn = trigger(card);
    btn.click();
    // One microtask for the stubbed promise, then a frame for the layout the swap
    // changes.
    await rendered();

    expect(card.querySelector(".tool-explain-btn")).toBeNull();
    const result = card.querySelector(".tool-explain-result");
    expect(result).toBe(btn);
    expect(result?.textContent).toBe(EXPLANATION);
    // The trigger's name must not outlive the trigger: `aria-label` wins over
    // content, so one left standing would announce "Explain this error" INSTEAD of
    // the explanation the element now holds.
    expect(result?.hasAttribute("aria-label")).toBe(false);
    expect(result?.hasAttribute("data-tooltip")).toBe(false);
  });

  it("hands its reserved strip back, because the result is in flow", async () => {
    const card = await failedCard("ex-strip-gone");
    const before = getComputedStyle(card).paddingBlockEnd;
    expect(parseFloat(before), "a card offering the trigger reserves a strip").toBeGreaterThan(0);

    trigger(card).click();
    await rendered();

    // `:has(> .tool-explain-btn)` stops matching, so the strip disappears rather
    // than sitting under an in-flow result as dead space.
    expect(parseFloat(getComputedStyle(card).paddingBlockEnd)).toBe(0);
    expect(getComputedStyle(card.querySelector(".tool-explain-result")!).position).toBe("static");
  });
});

describe("the disc is a circle at the small-control rung", () => {
  it.each(["fine", "coarse"] as const)("on %s", async (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const card = await failedCard(`ex-circle-${tier}`);
    const btn = trigger(card);
    const box = btn.getBoundingClientRect();

    expect(box.width, `${tier}: square is what makes a 50% radius a circle`).toBeCloseTo(
      box.height,
      1,
    );
    expect(getComputedStyle(btn).borderRadius).toBe("50%");
    // The PAINTED box is the tier's small-control rung, probed rather than restated
    // as 24/36 so a control-height retune moves the assertion.
    expect(box.width).toBeCloseTo(tokenPx(card, "var(--ctl-h-sm)"), 1);

    // The glyph is centred in it, which a floor-widened icon button has to declare.
    const glyph = btn.querySelector("svg")!.getBoundingClientRect();
    expect(glyph.left + glyph.width / 2, `${tier}: glyph centre x`).toBeCloseTo(
      box.left + box.width / 2,
      0,
    );
    expect(glyph.top + glyph.height / 2, `${tier}: glyph centre y`).toBeCloseTo(
      box.top + box.height / 2,
      0,
    );
  });
});

describe("it is ALWAYS visible, not hover-only", () => {
  it("paints at rest and answers a pointer at its own centre", async () => {
    const card = await failedCard("ex-visible");
    const btn = trigger(card);
    const cs = getComputedStyle(btn);
    // This app is touch-first: a coarse pointer has no hover to reveal an
    // affordance with, so a hidden-at-rest trigger is unreachable there.
    expect(cs.opacity).toBe("1");
    expect(cs.visibility).toBe("visible");
    expect(cs.display).not.toBe("none");
    const box = btn.getBoundingClientRect();
    expect(box.width).toBeGreaterThan(0);
    expect(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)).toBe(btn);
  });
});

describe("it floats in the card's bottom-right and overlays nothing", () => {
  it("sits one inset in from the card's own edges", async () => {
    const card = await failedCard("ex-corner");
    const btn = trigger(card);
    const cardBox = card.getBoundingClientRect();
    const box = btn.getBoundingClientRect();
    // An absolutely positioned box is offset from its containing block's PADDING
    // box, so the card's 1px border is the second term. Probed, not restated.
    const inset = tokenPx(card, "var(--explain-inset)");
    const border = parseFloat(getComputedStyle(card).borderRightWidth);

    expect(cardBox.right - box.right, "inline-end inset").toBeCloseTo(inset + border, 1);
    expect(cardBox.bottom - box.bottom, "block-end inset").toBeCloseTo(inset + border, 1);
    // Bottom-RIGHT rather than any corner: both distances are positive and the disc
    // is past the card's own midpoints.
    expect(box.left).toBeGreaterThan(cardBox.left + cardBox.width / 2);
    expect(box.top).toBeGreaterThan(cardBox.top + cardBox.height / 2);
  });

  it("clears the output region and the claim row, on an OPEN failed card", async () => {
    const card = await failedCard("ex-clears-open");
    const btn = trigger(card);
    const box = btn.getBoundingClientRect();
    const details = card.querySelector(".tool-details")!.getBoundingClientRect();
    const summary = card.querySelector(".tool-summary")!.getBoundingClientRect();

    expect(
      details.height,
      "the failure force-opens its region, or there is nothing to clear",
    ).toBeGreaterThan(0);
    // Reservation, not an overlay: the strip the card reserves is what puts the disc
    // BELOW every flow child, so it can obscure neither the output text nor the
    // region's own scrollbar.
    expect(box.top).toBeGreaterThanOrEqual(details.bottom);
    expect(box.top).toBeGreaterThanOrEqual(summary.bottom);
  });

  it("clears the claim row once the reader COLLAPSES the region", async () => {
    // The other reachable shape: with the region shut, the card's bottom-right is
    // the claim row's trailing edge, where `.tool-file-link` and the MCP badge live.
    const card = await failedCard("ex-clears-shut");
    // The region animates its height over `--dur-standard`; zeroing it on the card
    // makes the collapse land in the frames below instead of mid-flight.
    card.style.setProperty("--dur-standard", "0s");
    card.querySelector<HTMLElement>(".tool-disclosure")!.click();
    await rendered();

    const btn = trigger(card);
    const box = btn.getBoundingClientRect();
    const summary = card.querySelector(".tool-summary")!.getBoundingClientRect();
    expect(card.querySelector(".tool-details")!.getBoundingClientRect().height).toBeCloseTo(0, 0);
    expect(box.top).toBeGreaterThanOrEqual(summary.bottom);
  });
});

describe("the TARGET reaches the hit floor, past the box it paints", () => {
  it.each(["fine", "coarse"] as const)("on %s", async (tier) => {
    // A real hit test rather than a style read: a declared `::after` that some
    // ancestor `overflow` clips away reads identically in the cascade and hits
    // nothing — and `.tool-call` carries `overflow: hidden`, which is exactly why
    // the corner inset is derived from this reach rather than fixed at `--sp-2`.
    document.documentElement.dataset["pointer"] = tier;
    const card = await failedCard(`ex-target-${tier}`);
    const btn = trigger(card);
    const box = btn.getBoundingClientRect();
    const floor = tokenPx(card, "var(--hit-floor)");
    // Centred on the disc, so it is half the shortfall per edge.
    const reach = (floor - box.height) / 2;
    const cx = box.left + box.width / 2;

    if (reach <= 1) {
      // The fine tier paints AT the floor, so there is no expander to test — the
      // box itself is the target, which the visibility case above already hit.
      expect(box.height).toBeGreaterThanOrEqual(floor - 0.5);
      return;
    }
    expect(
      document.elementFromPoint(cx, box.top - reach + 1),
      `${tier}: a point just inside the target's top edge activates the trigger`,
    ).toBe(btn);
    expect(
      document.elementFromPoint(cx, box.bottom + reach - 1),
      `${tier}: and one just inside its bottom edge`,
    ).toBe(btn);
    // The control. Without it an expander of any size would pass, including one
    // overhanging into the card's own content.
    expect(
      document.elementFromPoint(cx, box.top - reach - 2),
      `${tier}: and the target stops there — it may not reach past the floor`,
    ).not.toBe(btn);
  });

  it("survives the THIRD tier, where the floor moves and the control rung does not", async () => {
    // `:root[data-touched]` raises `--hit-floor` to 44px ALONE (01-tokens.css), so a
    // hybrid device driven by its mouse keeps 24px controls and a 44px target — a
    // 10px expander against what would be an 8px corner inset. That is the ONE tier
    // where a flat `--sp-2` puts the expander past the card's padding box and
    // `.tool-call`'s `overflow: hidden` clips the target away, which is why
    // `--explain-inset` is derived from the reach rather than fixed.
    document.documentElement.dataset["pointer"] = "fine";
    document.documentElement.dataset["touched"] = "";
    const card = await failedCard("ex-target-touched");
    const btn = trigger(card);
    const box = btn.getBoundingClientRect();
    const reach = (tokenPx(card, "var(--hit-floor)") - box.height) / 2;
    expect(reach, "the third tier has to actually widen the target").toBeGreaterThan(1);
    const cx = box.left + box.width / 2;

    expect(
      document.elementFromPoint(cx, box.bottom + reach - 1),
      "the expander's bottom edge is inside the card's clip, so it still hits",
    ).toBe(btn);
    expect(document.elementFromPoint(cx, box.top - reach + 1)).toBe(btn);
  });
});

describe("the pending face replaces the glyph rather than crowding it", () => {
  it("hides the sparkle and drops the spinner's label margin", async () => {
    const card = await failedCard("ex-pending");
    const btn = trigger(card);
    // `bindLoadingState` writes this class; the point is the geometry, which is why
    // it is set by hand rather than driven through an action registry.
    btn.classList.add("btn-loading");
    await rendered();

    // `.btn-loading::before` is a 16px mark with a trailing margin authored for a
    // label beside it; both together overflow the disc.
    expect(getComputedStyle(btn.querySelector("svg")!).display).toBe("none");
    expect(parseFloat(getComputedStyle(btn, "::before").marginInlineEnd)).toBe(0);
    const box = btn.getBoundingClientRect();
    const spinner = parseFloat(getComputedStyle(btn, "::before").width);
    expect(spinner, "the spinner fits the disc it paints inside").toBeLessThanOrEqual(box.width);
  });
});

describe("nothing here suppresses the app-wide focus ring", () => {
  it("declares no outline at any state of this selector", () => {
    // 40-a11y.css draws every `:focus-visible` ring at ZERO specificity, so it is a
    // floor rather than a mandate — any rule here declaring `outline` would win and
    // could silently take the ring away. `CSS.forcePseudoState` and synthetic mouse
    // events both fail to drive `:focus-visible` in this sidecar
    // (`chromium-sidecar.md`), so a source sweep is the honest instrument.
    const rules = allRules(loadCSS("61-mcp-tools.css")).filter((r) =>
      r.selector.includes(".tool-explain-btn"),
    );
    expect(rules.length, "the sweep has to find the rules it is sweeping").toBeGreaterThan(0);
    for (const r of rules) {
      expect(r.body, `${r.selector} must not declare an outline`).not.toMatch(/\boutline\b/);
    }
  });
});
