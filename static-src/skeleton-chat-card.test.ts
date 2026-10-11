// The transcript placeholder, against the REAL class vocabulary.

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach } from "vitest";

import { allRules, manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";
import { CHAT_SKELETON_ID, chatSkeleton, loadMoreSkeleton } from "./skeleton.js";

/** The five classes the bubble era owned, each deleted with its only consumer. */
const DELETED = [
  "skeleton-msg-group",
  "skeleton-row",
  "skeleton-avatar",
  "skeleton-bubble",
  "skeleton-tool",
] as const;

/** The state attributes a real card carries and a placeholder must not. `id` leads because it is
 *  the one that matters: the real card answers to `turnAnchorID(n)`, which the turn map's
 *  landing and find-in-chat's reveal both resolve. */
const FORBIDDEN_ATTRS = [
  "id",
  "data-outcome",
  "data-severity",
  "data-trigger",
  "data-running",
  "data-folded",
  "data-no-fold",
] as const;

/** Walk a CHAIN of direct-child selectors, failing at the first step that misses. A flat
 *  `querySelector(".turn-body")` passes for a body mounted in the wrong parent, and the parent
 *  is the whole point: every class in this DOM is worn so that the REAL owner's rule applies,
 *  and a rule like `.turn-body > .boundary` or `.turn:not([data-running]) > .turn-header`
 *  selects on the relationship rather than on the class. */
function chain(root: Element, selectors: readonly string[]): Element {
  let at: Element = root;
  const walked: string[] = [];
  for (const sel of selectors) {
    const next = at.querySelector(`:scope > ${sel}`);
    expect(next, `${walked.join(" > ")} > ${sel}`).not.toBeNull();
    at = next as Element;
    walked.push(sel);
  }
  return at;
}

function cards(wrap: Element): Element[] {
  return [...wrap.querySelectorAll(".turn")];
}

describe("the rebuilt transcript placeholder", () => {
  it("is a contents wrap carrying the id and the aria marker, over three cards", () => {
    const wrap = chatSkeleton();
    expect(wrap.id).toBe(CHAT_SKELETON_ID);
    expect(wrap.getAttribute("aria-hidden")).toBe("true");
    expect(wrap.classList.contains("skeleton-rows")).toBe(true);
    // Direct children, all of them cards: a wrap holding furniture beside the cards would be a
    // second thing for `display: contents` to dissolve into the view.
    expect([...wrap.children].map((c) => c.className)).toEqual([
      "turn turn-skel",
      "turn turn-skel",
      "turn turn-skel",
    ]);
  });

  it("builds each card's two parent chains, not just the classes", () => {
    for (const card of cards(chatSkeleton())) {
      // The head row's two chrome bars must state a width: neither `.turn-n` nor `.turn-ts`
      // declares a box, so a bar there with no width has none.
      const headRow = chain(card, [".turn-header", ".turn-head-row"]);
      expect(headRow.querySelector(":scope > .turn-fold-toggle")).not.toBeNull();
      // The values are MEASURED on the real cells (see `skeletonTurnCard`), and the two differ by
      // more than a character count because `.turn-n` is mono while `.turn-ts` inherits the head
      // row's `system-ui`.
      const chromeBars = [...headRow.querySelectorAll(":scope > .skeleton.skeleton-line")];
      expect(chromeBars.map((b) => (b as HTMLElement).style.width)).toEqual(["1rem", "1.875rem"]);

      // The prompt's own metrics, which `1lh` then reads.
      const promptText = chain(card, [".turn-header", ".turn-req", ".turn-req-text"]);
      expect(promptText.querySelector(":scope > .skeleton-text")).not.toBeNull();

      // The reply, three levels into the body. `.message.assistant` is what the bars' percentages
      // resolve against (`width: 100%` at 13-messages.css).
      const reply = chain(card, [".turn-body", ".msg-row", ".message.assistant"]);
      const replyBars = [
        ...reply.querySelectorAll(".skeleton-text > .skeleton.skeleton-line-text"),
      ];
      expect(replyBars.length).toBeGreaterThanOrEqual(2);
      // TEXT bars, never chrome ones: D4 splits the units by where a bar sits.
      expect(reply.querySelector(".skeleton-line")).toBeNull();
    }
  });

  it("carries no id and no state attribute on any card", () => {
    for (const card of cards(chatSkeleton())) {
      for (const attr of FORBIDDEN_ATTRS) {
        expect(card.hasAttribute(attr), `${card.className} must not carry ${attr}`).toBe(false);
      }
      // Nor anywhere below it: a `data-folded` on the body would be read by `messages-blocks.ts`'s
      // `.turn[data-folded] > .turn-body` walk.
      for (const attr of FORBIDDEN_ATTRS) {
        expect(card.querySelector(`[${attr}]`), `no descendant carries ${attr}`).toBeNull();
      }
    }
  });

  it("emits none of the five deleted classes", () => {
    for (const wrap of [chatSkeleton(), loadMoreSkeleton()]) {
      for (const cls of DELETED) {
        expect(wrap.querySelector(`.${cls}`), `${cls} is emitted`).toBeNull();
        expect(wrap.classList.contains(cls)).toBe(false);
      }
    }
  });

  it("leaves no rule declaring any of the five, in any stylesheet", () => {
    // Over every sheet the MANIFEST names rather than the one that carried them, so a rule re-added
    // anywhere fails here. Word-boundaried, because `.skeleton-rows` contains `.skeleton-row` and a
    // substring test would read the shape class as an offender.
    const offenders: string[] = [];
    for (const sheet of manifestSheets()) {
      for (const rule of allRules(sheet.css)) {
        for (const cls of DELETED) {
          if (new RegExp(`\\.${cls}(?![\\w-])`).test(rule.selector)) {
            offenders.push(`${sheet.name}: ${rule.selector}`);
          }
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("is invisible to turnCards, which is why the cards live inside a wrap", () => {
    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    view.appendChild(chatSkeleton());
    // `messages.ts:turnCards` exactly: it iterates root.children and collects every DIRECT child
    // carrying `.turn`, then feeds `setResidentTurns` and `applyFoldPass` on every full paint —
    // including the empty-transcript paint where this placeholder is mounted.
    const collected = [...view.children].filter((c) => c.classList.contains("turn"));
    expect(collected).toEqual([]);
    // Non-vacuity: the cards ARE there, one level in.
    expect(cards(view)).toHaveLength(3);
  });

  it("gives the load-more placeholder the same card shape and no inline padding", () => {
    const wrap = loadMoreSkeleton();
    expect(wrap.classList.contains("skeleton-rows")).toBe(true);
    expect(wrap.getAttribute("aria-hidden")).toBe("true");
    // scroll.ts stamps `load-more-skeleton` on the wrap and removes it by that id, so this painter
    // must not claim an id of its own.
    expect(wrap.id).toBe("");
    expect([...wrap.children].map((c) => c.className)).toEqual([
      "turn turn-skel",
      "turn turn-skel",
    ]);
    // A `display: contents` wrap generates no box, so padding on it is inert; the view's own gap
    // separates these cards from the first real turn.
    expect(wrap.style.paddingBlock).toBe("");
    expect(wrap.getAttribute("style")).toBeNull();
    for (const card of cards(wrap)) {
      chain(card, [".turn-header", ".turn-head-row"]);
      chain(card, [".turn-body", ".msg-row", ".message.assistant"]);
    }
  });
});

describe("the placeholder card is not a pointer target", () => {
  let sheet: HTMLStyleElement;
  let stage: HTMLDivElement;

  beforeAll(() => {
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(() => {
    // Fixed and viewport-filling, so `.transcript-view`'s `min-height: 100%` and its
    // `justify-content: flex-end` put the LAST card on screen whatever the prose runs to — a hit
    // test needs a real box inside the viewport.
    stage = document.createElement("div");
    stage.style.cssText = "position:fixed;inset:0;overflow:hidden";
    document.body.appendChild(stage);
  });

  afterEach(() => {
    // Browser Mode isolates per FILE, so a stage left behind is over the next case's hit test.
    stage.remove();
  });

  it("computes pointer-events: none on the card and inherits it into the header", () => {
    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    view.appendChild(chatSkeleton());
    stage.appendChild(view);

    const all = cards(view);
    const card = all[all.length - 1] as HTMLElement;
    const rect = card.getBoundingClientRect();
    // Without a box the reads below are about nothing.
    expect(rect.width).toBeGreaterThan(0);
    expect(rect.height).toBeGreaterThan(0);

    expect(getComputedStyle(card).pointerEvents).toBe("none");
    const header = card.querySelector(".turn-header") as HTMLElement;
    // Inherited rather than declared: `pointer-events` inherits, which is what makes one
    // declaration on the card cover every affordance inside it.
    expect(getComputedStyle(header).pointerEvents).toBe("none");

    // The CONSEQUENCE, which is what the declaration is for: 29-turns.css gives `cursor: pointer`
    // and a `--layer-hover` wash to `.turn:not([data-running], [data-no-fold]) > .turn-header`, and
    // a placeholder card carries neither attribute, so it would match both.
    const x = rect.left + rect.width / 2;
    const y = rect.top + rect.height / 2;
    const hit = document.elementFromPoint(x, y);
    expect(hit).not.toBeNull();
    expect(card.contains(hit), "the pointer resolves past the placeholder card").toBe(false);

    // NEGATIVE CONTROL, on the same element at the same point: without the modifier the identical
    // DOM IS hit, so the miss above is `.turn-skel`'s doing rather than a stage that swallows every
    // hit test.
    card.classList.remove("turn-skel");
    expect(getComputedStyle(card).pointerEvents).toBe("auto");
    const hitAgain = document.elementFromPoint(x, y);
    expect(card.contains(hitAgain)).toBe(true);
  });

  it("resolves each prompt bar against the card's own prompt column", () => {
    // The one thing no source read can see. `.turn-req` is a flex column with `align-items:
    // flex-start`, so `.turn-req-text` shrink-wraps to its content — and a run of PERCENTAGE-width
    // bars has no intrinsic width.
    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    view.appendChild(chatSkeleton());
    stage.appendChild(view);

    const card = cards(view)[0] as HTMLElement;
    const column = card.querySelector(".turn-req-text") as HTMLElement;
    const bar = column.querySelector(".skeleton-line-text") as HTMLElement;
    const columnWidth = column.getBoundingClientRect().width;
    const barWidth = bar.getBoundingClientRect().width;

    expect(columnWidth).toBeGreaterThan(0);
    // Against the COLUMN rather than a literal: the card's own width is the viewport's business,
    // and what the width table claims is a fraction of the prompt column. 68% is the first card's
    // prompt entry.
    expect(barWidth / columnWidth).toBeCloseTo(0.68, 2);
    // One line box of the PROMPT's own metrics, which is what `1lh` reads.
    const lineBox = column.getBoundingClientRect().height;
    expect(bar.getBoundingClientRect().height).toBeLessThan(lineBox);
    expect(bar.getBoundingClientRect().height).toBeGreaterThan(0);

    // NEGATIVE CONTROL: without the modifier the identical DOM collapses, so the measurement above
    // is the stretch rule's doing rather than a container that would have filled anyway.
    card.classList.remove("turn-skel");
    expect(column.getBoundingClientRect().width).toBe(0);
  });

  it("reserves the fold slot at the real control's box on BOTH pointer tiers", () => {
    // The real `.turn-fold-toggle` is a BUTTON, so 61-mcp-tools.css's zero- specificity floor lifts
    // it to `--hit-floor`; the placeholder is a div wearing the same class and matches none of that
    // rule's arms.
    const root = document.documentElement;
    const had = root.getAttribute("data-pointer");

    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    view.appendChild(chatSkeleton());
    stage.appendChild(view);

    const card = cards(view)[0] as HTMLElement;
    const slot = card.querySelector(".turn-fold-toggle") as HTMLElement;

    // The comparison subject: the control the slot stands in for, in the same header, so both read
    // one computed `--hit-floor` and one `--ctl-h-sm`.
    const real = document.createElement("button");
    real.className = "turn-fold-toggle";
    (slot.parentElement as HTMLElement).appendChild(real);

    try {
      for (const tier of ["fine", "coarse"] as const) {
        root.setAttribute("data-pointer", tier);
        const a = slot.getBoundingClientRect();
        const b = real.getBoundingClientRect();
        expect(a.width, `${tier}: slot width`).toBeCloseTo(b.width, 1);
        expect(a.height, `${tier}: slot height`).toBeCloseTo(b.height, 1);
        expect(a.width, `${tier}: slot is a real box`).toBeGreaterThan(0);
      }

      // NEGATIVE CONTROL, on the tier that separates the two tokens: strip the modifier and the
      // placeholder falls back to `--ctl-h-sm` while the button keeps the floor, so the pair
      // diverges.
      root.setAttribute("data-pointer", "coarse");
      slot.classList.remove("turn-fold-toggle");
      slot.className = `${slot.className} turn-fold-toggle`.trim();
      card.classList.remove("turn-skel");
      expect(slot.getBoundingClientRect().height).toBeLessThan(real.getBoundingClientRect().height);
    } finally {
      real.remove();
      if (had === null) {
        root.removeAttribute("data-pointer");
      } else {
        root.setAttribute("data-pointer", had);
      }
    }
  });
});
