// `.subagent-block`'s intrinsic-size estimate must cover the RESTING shape on every pointer tier.
// A never-rendered `content-visibility: auto` box contributes `<len>` plus padding, border and
// `min-height`, so a wrong `<len>` moves `scrollHeight` per unrendered box; only layout answers it.

import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The reserve moves on the POINTER tier, so the coarse cases prove nothing keys on width
// (`01-tokens.css` has a width-keyed fallback). `@vitest/browser/context` throws; use `vitest/browser`.
import { page } from "vitest/browser";

// scroll.ts is a singleton over a real `#messages`; nothing folds here, so the mock only has to exist.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildSubagentCard, buildSubagentContainer } from "./fundamentals/subagent-block.js";

/** Long enough that a per-card error is a four-figure number in the list total, so
 *  a failure is unmistakable rather than a rounding argument. Also the divisor the
 *  real box height is reported through, so it has to match the card count. */
const CARDS = 200;

/** The scrollport's height. Every box past it is off-viewport and therefore
 *  skipped, which is what gives the first reading its estimate-driven total. */
const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with
 *  every box genuinely rendered. */
const FORCE = "subagent-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness
 *  can prove the estimate is what the first reading is made of. */
const PROBE = "subagent-metrics-probe-estimate";

/** Far from every real box height, so the shift it produces cannot be confused with a rounding
 *  difference. */
const PROBE_PX = 400;

/** Per-case timeout: six rAF turns, which a loaded full run prices at hundreds of ms each. Nothing
 *  here is timing-dependent; the assertion compares two synchronous `scrollHeight` reads. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.subagent-block`'s own (0,1,0), so both win wherever they are
  // inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .subagent-block { content-visibility: visible }`,
    `.${PROBE} .subagent-block { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  // Out of the way of anything else the page holds, and a fixed inline size so a
  // box's own width never depends on the body's flow.
  host.style.cssText = "position:fixed;top:0;left:0;inline-size:48rem;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  force.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** The settled shapes' ledger; `elapsedMs` earns the footer. Its tallest child is the
 *  `.turn-ledger-summary` button at `--hit-floor`; the info `i` and `.sr-only` name do not move it. */
const LEDGER = { elapsedMs: 3_000 } as const;

/** What the transcript builds for a LEAF delegate: an identity row that is itself
 *  the anchor to the delegate's own page, and nothing under it. A settled card's tail
 *  is removed by the builder, so the resting shape is header + foot. */
function settledCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "completed", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  sa.setSummary(LEDGER);
  return sa.root;
}

/** The same card mid-flight: the tail is present and EMPTY (a card is built before
 *  its delegate has said anything) and the foot is withheld by `:empty`, so this is
 *  the identity row alone. */
function runningCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "in_progress", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  return sa.root;
}

/** A COLLAPSED pipeline container over two stage cards: the second resting shape
 *  the ONE reserve has to serve. Its body is driven to zero by the disclosure, so
 *  what is left is the identity row and the foot -- the settled card's shape. */
function collapsedContainer(i: number): HTMLElement {
  const c = buildSubagentContainer(`Subagent pipeline \u00b7 2 stages`, "completed", {
    startOpen: false,
  });
  c.setSummary(LEDGER);
  c.body.append(settledCard(i * 2), settledCard(i * 2 + 1));
  return c.root;
}

type Build = (i: number) => HTMLElement;

/** The production nesting: a fixed-height scroller around a `.turn-body` flex column, whose gap
 *  cancels out of the drift. */
function mountList(build: Build): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;

  const list = document.createElement("div");
  list.className = "turn-body";
  for (let i = 0; i < CARDS; i++) {
    list.appendChild(build(i));
  }

  wrap.appendChild(list);
  host.replaceChildren(wrap);
  return { wrap, list };
}

/** Yield until the renderer has run, which is what updates
 *  `content-visibility: auto` relevance. */
async function frame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** Jump every entry animation to its end: `vk-slide-up`'s `translateY(6px)` extends scrollable
 *  overflow while it runs. INFINITE animations (a running card's `vk-spin`) are skipped:
 *  `finish()` throws on one, and a glyph spin inside `overflow: hidden` cannot reach the list. */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    if (Number.isFinite(anim.effect?.getComputedTiming().endTime ?? Infinity)) {
      anim.finish();
    }
  }
}

interface Metrics {
  readonly listSkipped: number;
  /** The same, with the estimate replaced by `PROBE_PX`. The harness's own
   *  sensitivity check -- see `expectPremise`. */
  readonly listProbe: number;
  readonly listRendered: number;
}

/** Read the same 200-box list three times: shipped estimate, a wrong one, and no skip. The list
 *  TOTAL is the instrument: `getBoundingClientRect()` inside a skipped subtree reports the REAL box. */
async function measure(build: Build): Promise<Metrics> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Still skipped, so this reads the fallback rather than a remembered size. Done
  // BEFORE the force-render, because a box that has been rendered once remembers
  // its real size and stops consulting the fallback at all.
  list.classList.add(PROBE);
  await frame();
  const listProbe = list.scrollHeight;
  list.classList.remove(PROBE);

  list.classList.add(FORCE);
  await frame();
  finishAnimations(wrap);
  await frame();

  return { listSkipped, listProbe, listRendered: list.scrollHeight };
}

function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** THE PREMISE: inflating the estimate must move the total by a lot, or the other readings agree
 *  trivially. */
function expectPremise(m: Metrics): void {
  expect(
    m.listProbe - m.listSkipped,
    "boxes are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);
}

/** For a RESTING box the reserve IS the shape, so skipped and rendered agree: a drift of exactly 0.
 *  The per-box height rides the MESSAGE, since it is font-dependent. */
function expectNoDrift(m: Metrics): void {
  expectPremise(m);
  expect(
    m.listSkipped - m.listRendered,
    `drift at ${String(m.listRendered / CARDS)}px per box`,
  ).toBe(0);
}

/** A RUNNING card is deliberately over-stated: the POPULATION is settled, and the fallback applies
 *  only before first render. The SIGN is pinned; the magnitude rides the message. */
function expectOverStates(m: Metrics): void {
  expectPremise(m);
  const drift = m.listSkipped - m.listRendered;
  expect(
    drift > 0,
    `the reserve is the SETTLED shape, so a running card is over-stated: ${String(Math.round(drift / CARDS))}px per card at ${String(m.listRendered / CARDS)}px per box`,
  ).toBe(true);
}

// Red-check probes: `contain-intrinsic-size: auto 4rem` reddens the settled and container cases;
// `--subagent-content: 30px` reddens the running arm.
describe("the fine-pointer tier", () => {
  it(
    "reports one height whether its settled cards are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "reports one height for a COLLAPSED pipeline container too, on the same reserve",
    async () => {
      tier("fine");
      expectNoDrift(await measure(collapsedContainer));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "over-states a RUNNING card, which is the accepted half of the trade",
    async () => {
      tier("fine");
      expectOverStates(await measure(runningCard));
    },
    LOADED_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // Coarse narrow (where `01-tokens.css`'s fallback also binds) and wide measure identically. LAST in
  // the file, restoring the size it found: `page.viewport` has no getter.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  it(
    "holds for a settled card on a NARROW viewport",
    async () => {
      // 48rem is the fallback's own boundary and `<=` includes it, so this is the
      // widest viewport that still takes the coarse control heights with no
      // attribute set.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a settled card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a COLLAPSED pipeline container",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(collapsedContainer));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "still over-states a RUNNING card",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectOverStates(await measure(runningCard));
    },
    LOADED_BUDGET_MS,
  );
});
