// The transcript's LAYOUT declarations: what is contained, what is outside its layout, and what the
// live-edge marker costs. Containment is a source fact, read from the CSS (`contain` computes to
// itself whether or not it does anything); the marker's cost is measured. One open turn measured
// 353 tool cards, and the composer's `field-sizing: content` textarea shares a layout pass with the
// transcript unless contained.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { loadCSS, mountAppCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";
import scrollSource from "./scroll.ts?raw";

/** The selectors that carry the bulk, and the file each is authored in. */
const BULK: readonly { selector: string; file: string }[] = [
  { selector: ".msg-row", file: "13-messages.css" },
  { selector: ".tool-call", file: "14-tools.css" },
  { selector: ".subagent-block", file: "14-tools.css" },
  { selector: ".plan-message", file: "14-tools.css" },
  { selector: ".run-card", file: "27-run-card.css" },
];

describe("transcript containment", () => {
  for (const { selector, file } of BULK) {
    it(`${selector} skips its subtree off-screen and reserves a size`, () => {
      const body = ruleBody(loadCSS(file), selector);
      expect(body, `${selector} declares content-visibility: auto`).toMatch(
        /content-visibility:\s*auto\s*;/u,
      );
      // `auto` first makes the fallback a pre-render estimate. A token size (`var(--btn-h)`) stays right
      // on coarse; `tool-box-height.test.ts` checks the resolved value.
      expect(body, `${selector} declares contain-intrinsic-size: auto <size>`).toMatch(
        /contain-intrinsic-size:\s*auto\s+(?:[\d.]+rem|var\(--[\w-]+\))\s*;/u,
      );
    });
  }

  it("leaves the collapsed subagent body on `hidden`, keyed on its own state", () => {
    // The block's `auto` keys on viewport relevancy, the BODY's on the disclosure; 14-tools.css says why
    // they must not be "aligned".
    const body = ruleBody(loadCSS("14-tools.css"), ".subagent-block.collapsed > .subagent-body");
    expect(body).toMatch(/content-visibility:\s*hidden\s*;/u);
  });

  it("is the set scroll.ts lays out ahead of the scrollport", () => {
    // A box missing there first takes its real size on screen in WebKit, where anchoring cannot hide it.
    const declared = /const SKIPPABLE = "([^"]+)";/u.exec(scrollSource)?.[1]?.split(", ") ?? [];
    expect(declared.toSorted()).toEqual(BULK.map((b) => b.selector).toSorted());
  });
});

describe("composer layout independence", () => {
  const form = (): string => ruleBody(loadCSS("15-input.css"), '[id="prompt-form"]');

  it("declares LAYOUT containment, and only that", () => {
    // Not `paint`/`content`/`strict`: pill cards open UPWARD out of this bar and paint containment
    // would clip their tops. Not `size`: the bar's height IS its content's.
    expect(form()).toMatch(/contain:\s*layout\s*;/u);
    expect(form()).not.toMatch(/contain:[^;]*\b(?:paint|content|strict|size)\b/u);
  });

  it("carries the z-index its own stacking context makes necessary", () => {
    // Layout containment makes the bar a stacking context, so `position: relative` plus a positive
    // z-index lifts it (and its upward pill cards) above the content layer. The toolbar is static,
    // in-flow and unstacked, so any positive z-index clears it; a re-floated toolbar must fail here.
    const body = form();
    expect(body).toMatch(/position:\s*relative\s*;/u);
    expect(body).toMatch(/z-index:\s*10\s*;/u);
    // `ruleContaining` rather than `ruleBody`: a negative assertion has to run
    // against a comment-free body, and this one also pins that there is exactly
    // one top-level `.chat-toolbar` rule to read.
    const toolbar = ruleContaining(loadCSS("12-chat.css"), ".chat-toolbar", "top").body;
    expect(toolbar, "the toolbar claims no stacking level of its own").not.toMatch(/z-index:/u);
    expect(toolbar, "and stays in flow, under the positioned composer").not.toMatch(
      /position:\s*(?:absolute|fixed|sticky)/u,
    );
  });

  it("does not put the containment on the bottom bar every view shares", () => {
    // `.bottom-bar` is also the editor, files and run toolbars; none of them
    // sits beside a live transcript, and containing them would be a claim about
    // three surfaces this finding never measured.
    expect(ruleBody(loadCSS("19-files.css"), ".bottom-bar")).not.toMatch(/contain:/u);
  });
});

// Real layout, because both of these are claims about pixels rather than about
// declarations.
describe("against real layout", () => {
  let sheet: HTMLStyleElement;
  let stage: HTMLElement;

  beforeAll(() => {
    sheet = mountAppCSS();
    stage = document.createElement("div");
    document.body.appendChild(stage);
  });

  afterAll(() => {
    sheet.remove();
    stage.remove();
  });

  /** A transcript view of `n` 100px turns, optionally with scroll.ts's live-edge marker placed FIRST
   *  (where reconcile leaves unkeyed furniture), so flex order must put it last. */
  function view(withEdge: boolean, n = 2): HTMLElement {
    const v = document.createElement("div");
    v.className = "transcript-view is-active";
    if (withEdge) {
      const edge = document.createElement("div");
      edge.className = "transcript-edge";
      v.appendChild(edge);
    }
    for (let i = 0; i < n; i++) {
      const turn = document.createElement("div");
      turn.className = "turn";
      turn.style.blockSize = "100px";
      v.appendChild(turn);
    }
    return v;
  }

  /** The multiplexer inside index.html's REAL ancestor chain, overflowing: `#messages-wrap` is what
   *  scroll.ts scrolls and observes. `parked` adds a switched-away view. The outer wrap gets an
   *  explicit height in place of production's `flex: 1`. */
  function scroller(n: number, parked = 0): { scrollEl: HTMLElement; edge: HTMLElement } {
    const outer = document.createElement("div");
    outer.id = "messages-wrap-outer";
    outer.style.blockSize = "200px";
    const wrap = document.createElement("div");
    wrap.id = "messages-wrap";
    const messages = document.createElement("div");
    messages.id = "messages";
    const v = view(true, n);
    messages.appendChild(v);
    for (let i = 0; i < parked; i++) {
      const p = view(true, n);
      p.classList.remove("is-active");
      messages.appendChild(p);
    }
    wrap.appendChild(messages);
    outer.appendChild(wrap);
    stage.appendChild(outer);
    const edge = v.querySelector<HTMLElement>(".transcript-edge");
    expect(edge, "the fixture mounted the marker").not.toBeNull();
    expect(wrap.scrollHeight, "the fixture overflows its scroller").toBeGreaterThan(
      wrap.clientHeight,
    );
    return { scrollEl: wrap, edge: edge as HTMLElement };
  }

  it("lays a skippable box out once it is marked near, with the containment `auto` applies", () => {
    const wrap = document.createElement("div");
    wrap.id = "messages-wrap";
    stage.appendChild(wrap);
    const seen = BULK.map(({ selector }) => {
      const box = document.createElement("div");
      box.className = selector.slice(1);
      wrap.appendChild(box);
      const far = getComputedStyle(box).contentVisibility;
      box.toggleAttribute("data-near", true);
      const near = getComputedStyle(box);
      return { selector, far, near: near.contentVisibility, contain: near.contain };
    });
    wrap.remove();

    expect(seen).toEqual(
      BULK.map(({ selector }) => ({ selector, far: "auto", near: "visible", contain: "content" })),
    );
  });

  /** How far the marker's bottom sits above the scroller's content bottom, which is
   *  the figure `isAtBottom()` and the edge observer have to agree on. */
  function offsetFromContentBottom(scrollEl: HTMLElement, edge: HTMLElement): number {
    const markerBottom =
      edge.getBoundingClientRect().bottom -
      scrollEl.getBoundingClientRect().top +
      scrollEl.scrollTop;
    return scrollEl.scrollHeight - markerBottom;
  }

  it("the live-edge marker sits ON the SCROLLER's content bottom", () => {
    // The marker's top margin: scroll.ts's observer (rooted on `#messages-wrap`, BOTTOM_TOLERANCE_PX)
    // and `isAtBottom` agree only at zero offset from its content bottom. Measured, only
    // `.transcript-view`'s `padding-block-end` reopens the 16px; ancestors move both together.
    const { scrollEl, edge } = scroller(3);
    expect(offsetFromContentBottom(scrollEl, edge)).toBe(0);
  });

  it("a parked view adds nothing below the marker", () => {
    // A parked view sharing the scroll box stacks height under the active marker; `content-visibility:
    // hidden` skips only contents, so zeroed `min-height` and `padding-block` keep the marker honest.
    const { scrollEl, edge } = scroller(3, 1);
    expect(offsetFromContentBottom(scrollEl, edge)).toBe(0);
  });

  it("the marker's margin carries the block-end air and adds nothing of its own", () => {
    // A zero-height flex item still earns the `gap`, so the margin is `--sp-4 - --sp-3`; any other value
    // lifts the `flex-end` column. 244 = two 100px turns, two 12px gaps, 4px margin, 16px of air.
    const v = view(true);
    stage.appendChild(v);
    const edge = v.querySelector<HTMLElement>(".transcript-edge");
    const turns = v.querySelectorAll<HTMLElement>(".turn");
    const last = turns[turns.length - 1];
    expect(v.getBoundingClientRect().height).toBe(244);
    expect(v.getBoundingClientRect().bottom - last!.getBoundingClientRect().bottom).toBe(16);
    // `order: 1` is why it lands last: in the DOM this marker is the FIRST child.
    expect(edge?.previousElementSibling).toBeNull();
    expect(edge!.getBoundingClientRect().top).toBeGreaterThanOrEqual(
      last!.getBoundingClientRect().bottom,
    );
  });

  // No rect case for containment CLIPPING a pill card: paint containment leaves geometry reads
  // unchanged, so it would pass under `contain: paint` too (measured). Pinned as a declaration above.

  /** A turn body holding the entries `build` returns, inside the card that owns the
   *  gap. Real layout, because the question is what the ENTRIES measure apart. */
  function body(...entries: readonly HTMLElement[]): HTMLElement {
    const turn = document.createElement("div");
    turn.className = "turn";
    const b = document.createElement("div");
    b.className = "turn-body";
    b.style.inlineSize = "640px";
    for (const e of entries) {
      b.appendChild(e);
    }
    turn.appendChild(b);
    stage.appendChild(turn);
    return b;
  }

  /** One entry of `cls`, given a height so an intrinsic-size reserve cannot stand in
   *  for a measurement. */
  function entry(cls: string): HTMLElement {
    const el = document.createElement("div");
    el.className = cls;
    el.style.blockSize = "40px";
    return el;
  }

  /** The vertical distance between two rendered siblings. */
  function between(a: HTMLElement, b: HTMLElement): number {
    return b.getBoundingClientRect().top - a.getBoundingClientRect().bottom;
  }

  it("spaces two entries by the turn body's own gap and nothing else", () => {
    // `.turn-body` (29-turns.css) spaces a turn's blocks, with no level between it and an entry.
    // MEASURED rather than read off `row-gap`: an entry margin stacks on the gap.
    const a = entry("msg-row");
    const b = entry("msg-row");
    const host = body(a, b);
    const gap = Number.parseFloat(getComputedStyle(host).rowGap);
    expect(gap, "the body declares a gap at all").toBeGreaterThan(0);
    expect(between(a, b)).toBeCloseTo(gap, 1);
  });

  it("adds no margin of its own on a reasoning trace or a code-references row", () => {
    // Both carry `margin: 0` (13-messages.css); a margin here reads as a 20px boundary beside 12px.
    const row = entry("msg-row");
    const reasoning = entry("reasoning-block");
    const refs = entry("code-refs");
    const host = body(row, reasoning, refs);
    const gap = Number.parseFloat(getComputedStyle(host).rowGap);
    expect(between(row, reasoning)).toBeCloseTo(gap, 1);
    expect(between(reasoning, refs)).toBeCloseTo(gap, 1);
  });
});
