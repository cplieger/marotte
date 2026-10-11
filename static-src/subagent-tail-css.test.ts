// A DELEGATE CARD RESERVES NOTHING FOR OUTPUT IT HAS NOT RECEIVED, and the box it grows
// into is animated rather than snapped: 14-tools.css rests the tail at `:empty` zero size
// and shows it at `:not(:empty)` `auto`. Measured on real boxes against the assembled
// stylesheet, because neither half is visible in the markup.
import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

// scroll.ts is a self-initialising singleton over a real `#messages`; nothing here folds,
// so the canonical mock only has to exist.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { buildSubagentCard, type SubagentCard } from "./fundamentals/subagent-block.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { ToolStatus } from "./types.js";

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  stage.className = "turn-body";
  stage.style.inlineSize = "760px";
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

afterEach(() => {
  stage.replaceChildren();
});

/** A card as the transcript builds one, mounted. */
function card(status: ToolStatus): SubagentCard {
  const sa = buildSubagentCard("wf-workflow-creator", status);
  stage.appendChild(sa.root);
  return sa;
}

function tailOf(sa: SubagentCard): HTMLElement {
  const tail = sa.root.querySelector<HTMLElement>(".subagent-tail");
  if (tail === null) {
    throw new Error("the card has no tail element");
  }
  return tail;
}

const height = (el: Element): number => el.getBoundingClientRect().height;

/** Commit the resting style, or the growth has no before-change value to transition
 *  FROM and the assertions pass for the wrong reason. */
const frame = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

describe("a delegate's tail before its first line", () => {
  it("occupies nothing on a card that has just been dispatched", () => {
    const sa = card("in_progress");
    expect(height(tailOf(sa))).toBe(0);
  });

  // `bindSubagentTail` paints on bind, so an empty projection is most cards' first paint.
  it("occupies nothing when the projection paints no lines", () => {
    const sa = card("in_progress");
    sa.setTail([]);
    expect(height(tailOf(sa))).toBe(0);
  });

  // An empty tail is the one region between the identity row and the foot, so any cost
  // to the card IS the band. Measured after two frames: before them `content-visibility:
  // auto` renders the card at its `contain-intrinsic-size` reserve.
  it("leaves the card at its identity row, with nothing reserved under it", async () => {
    const sa = card("in_progress");
    await frame();
    await frame();
    const box = height(sa.root);
    const cs = getComputedStyle(sa.root);
    const border =
      Number.parseFloat(cs.borderBlockStartWidth) + Number.parseFloat(cs.borderBlockEndWidth);
    expect(border, "the card is bordered, so the relation below has to carry it").toBeGreaterThan(
      0,
    );
    // `auto <length>`, so the keyword comes off before the number does.
    const reserve = Number.parseFloat(cs.containIntrinsicBlockSize.replace(/^auto\s+/u, ""));
    expect(
      Number.isNaN(reserve),
      `containIntrinsicBlockSize was ${cs.containIntrinsicBlockSize}`,
    ).toBe(false);
    expect(box, "the card is laid out rather than rendering at its reserve").toBeLessThan(reserve);
    const header = sa.root.querySelector(".subagent-header");
    if (header === null) {
      throw new Error("the card has no identity row");
    }
    expect(box, "header plus borders, and nothing else").toBeCloseTo(height(header) + border, 1);
  });
});

describe("a delegate's first line", () => {
  it("grows the box over --dur-enter instead of snapping it open", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    expect(height(tail), "resting at zero before the line lands").toBe(0);

    sa.setTail(["go build ./..."]);
    // getAnimations flushes style, so the read below is the animation's first value.
    const running = tail.getAnimations();
    expect(running.length, "the growth is transitioned").toBeGreaterThan(0);
    expect(height(tail), "still at the resting height on the frame it lands").toBeLessThan(2);

    await Promise.all(running.map((a) => a.finished));
    const line = tail.firstElementChild;
    if (line === null) {
      throw new Error("the tail painted no line");
    }
    const cs = getComputedStyle(tail);
    const pad = Number.parseFloat(cs.paddingBlockStart) + Number.parseFloat(cs.paddingBlockEnd);
    expect(pad, "the shown state carries block padding").toBeGreaterThan(0);
    expect(height(tail), "settles at its one line plus that padding").toBeCloseTo(
      height(line) + pad,
      1,
    );
  });

  // Anti-thrash: `auto` is the computed value in every shown state, so a per-delta
  // rewrite that keeps the line count must transition nothing.
  it("does not re-run the growth for a delta that rewrites its last line", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    sa.setTail(["go build ./..."]);
    await Promise.all(tail.getAnimations().map((a) => a.finished));
    const settled = height(tail);

    // Wrapping is `subagent-tail-wrap.test.ts`'s subject.
    sa.setTail(["go test ./..."]);
    expect(tail.getAnimations(), "a rewrite starts no transition").toEqual([]);
    expect(height(tail), "and moves the box not at all").toBeCloseTo(settled, 1);
  });

  it("is what the block padding arrives with", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    const resting = getComputedStyle(tail);
    expect(Number.parseFloat(resting.paddingBlockStart)).toBe(0);
    expect(Number.parseFloat(resting.paddingBlockEnd)).toBe(0);
    // Inline padding is constant, aligning with the header's name from the first frame.
    expect(Number.parseFloat(resting.paddingInlineStart)).toBeGreaterThan(0);

    sa.setTail(["one"]);
    await Promise.all(tail.getAnimations().map((a) => a.finished));
    const shown = getComputedStyle(tail);
    expect(Number.parseFloat(shown.paddingBlockStart)).toBeGreaterThan(0);
    expect(Number.parseFloat(shown.paddingBlockEnd)).toBeGreaterThan(0);
    expect(shown.paddingInlineStart).toBe(resting.paddingInlineStart);
  });
});
