// `.fb-row`'s `contain-intrinsic-size` must equal the row's real height on every tier: a never-rendered row
// contributes the estimate, so a wrong one moves `#fb-list`'s `scrollHeight` as the reader scrolls (and scroll
// anchoring corrects `scrollTop` each frame). `contain-intrinsic-size` states the content box, so one value covers
// every tier. Covers listing rows, search hits and dirty-file rows; only a layout measurement can see it.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The padding override is a width media rule, so the narrow tier needs a real resize. `vitest/browser` is the Vitest 5
// spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Long enough that a per-row error is a four-figure list total. */
const ROWS = 600;

/** Rows past the scrollport are skipped, so the first reading is estimate-driven. */
const WRAP_H = 500;

const FORCE = "fb-metrics-force-render";

const PROBE = "fb-metrics-probe-estimate";

const PROBE_PX = 100;

/** Per-case timeout: six rAF turns, each hundreds of ms in a loaded full run. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.fb-row`'s (0,1,0), so both win wherever inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .fb-row { content-visibility: visible }`,
    `.${PROBE} .fb-row { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;inline-size:40rem;";
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

/** With the 1.25rem icon box, the tallest flex child, which the estimate names. */
function row(i: number): HTMLElement {
  const el = document.createElement("div");
  el.className = "fb-row";
  el.setAttribute("role", "listitem");

  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  icon.appendChild(svg);

  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = `entry-${String(i)}.ts`;

  const meta = document.createElement("span");
  meta.className = "fb-meta";
  meta.textContent = "1.2 KB   ·   2026-09-01   ·   -rw-r--r--";

  el.append(check, icon, name, meta);
  return el;
}

function letteredRow(i: number): HTMLElement {
  const el = row(i);

  const badge = document.createElement("span");
  badge.className = "fb-git-letter git-st-m fb-git-clickable";
  badge.setAttribute("role", "button");
  badge.setAttribute("aria-label", "Git status: modified");
  badge.textContent = "M";

  el.insertBefore(badge, el.querySelector(".fb-meta"));
  return el;
}

/** A search hit carries both classes, so it takes `.fb-row`'s estimate. */
function hitRow(i: number): HTMLElement {
  const el = document.createElement("div");
  el.className = "fb-row fb-search-hit";
  el.setAttribute("role", "listitem");
  el.setAttribute("tabindex", "0");

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  icon.appendChild(svg);

  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = `src/entry-${String(i)}.ts`;

  const lineno = document.createElement("span");
  lineno.className = "fb-search-lineno";
  lineno.textContent = `:${String(i + 1)}`;

  const excerpt = document.createElement("span");
  excerpt.className = "fb-search-excerpt";
  excerpt.textContent = "const answer = compute(input, options, fallback);";

  el.append(icon, name, lineno, excerpt);
  return el;
}

type RowBuilder = (i: number) => HTMLElement;

/** `.fb-list-wrap`'s `flex: 1 1 0` is inert outside a flex container, so an inline height sizes it. */
function mountList(build: RowBuilder = row): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.className = "fb-list-wrap";
  wrap.style.cssText = `height:${String(WRAP_H)}px;`;

  const list = document.createElement("div");
  list.id = "fb-list";
  list.setAttribute("role", "list");
  for (let i = 0; i < ROWS; i++) {
    list.appendChild(build(i));
  }

  wrap.appendChild(list);
  host.replaceChildren(wrap);
  return { wrap, list };
}

async function frame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/**
 * `vk-slide-up` starts at `translateY(6px)`, and a transformed descendant extends scrollable overflow, so animations
 * are finished first.
 */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    anim.finish();
  }
}

interface Metrics {
  readonly listSkipped: number;
  readonly listProbe: number;
  readonly listRendered: number;
}

/** Three readings of one list: shipped estimate, a wrong one, skip off. Only the list total is an honest instrument. */
async function measure(build: RowBuilder = row): Promise<Metrics> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Before the force-render: a rendered row remembers its size and stops consulting the fallback.
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

function expectNoDrift(m: Metrics): void {
  // Premise: if nothing were skipped, readings one and three would agree trivially.
  expect(
    m.listProbe - m.listSkipped,
    "rows are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(ROWS * 10);

  // The drift, with the real row height beside it, so a failure names the px.
  expect({
    drift: m.listSkipped - m.listRendered,
    realRowHeight: m.listRendered / ROWS,
  }).toEqual({ drift: 0, realRowHeight: m.listRendered / ROWS });
}

describe("the fine-pointer tier", () => {
  it(
    "reports one list height whether its rows are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit, which takes .fb-row's estimate on the same element",
    async () => {
      tier("fine");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row, which carries the git letter production emits",
    async () => {
      tier("fine");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );
});

/** The walk's budget: ~60 steps of two rAF turns each. */
const WALK_BUDGET_MS = 90_000;

/** Bounded so a list that never reaches its end fails rather than hangs. */
const MAX_WALK_STEPS = 200;

interface Walk {
  readonly heights: readonly number[];
  readonly steps: number;
  readonly reachedEnd: boolean;
}

/** Read `scrollHeight` after every screenful; finishing animations and the frame wait are both required. */
async function walkScrollHeights(build: RowBuilder): Promise<Walk> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();

  const heights: number[] = [list.scrollHeight];
  let steps = 0;
  let reachedEnd = false;
  while (steps < MAX_WALK_STEPS) {
    // Re-read per step: the maximum moves as bands render.
    const max = wrap.scrollHeight - wrap.clientHeight;
    if (max - wrap.scrollTop < 1) {
      reachedEnd = true;
      break;
    }
    wrap.scrollTop = Math.min(wrap.scrollTop + WRAP_H, max);
    steps++;
    finishAnimations(wrap);
    await frame();
    heights.push(list.scrollHeight);
  }
  return { heights, steps, reachedEnd };
}

describe("scrolling a lettered listing end to end", () => {
  it(
    "never moves #fb-list's scrollHeight",
    async () => {
      tier("fine");
      const walk = await walkScrollHeights(letteredRow);

      // Premise: one step, or never reaching the end, leaves most bands on the estimate.
      expect(walk.steps, "the walk actually traversed the list").toBeGreaterThan(1);
      expect(walk.reachedEnd, "the walk reached the scroller's end").toBe(true);

      // [min, max, distinct], so a regression names the climb.
      const distinct = [...new Set(walk.heights)];
      expect({
        min: Math.min(...walk.heights),
        max: Math.max(...walk.heights),
        distinctHeights: distinct.length,
      }).toEqual({
        min: walk.heights[0],
        max: walk.heights[0],
        distinctHeights: 1,
      });
    },
    WALK_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // The padding override keys on width (`50-mobile.css`) while `--btn-h` moves on pointer tier (`01-tokens.css`), so
  // coarse has two cases. Sits last and restores the size.
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
    "holds on a NARROW viewport, where the padding override binds",
    async () => {
      // 48rem is the override's boundary and `<=` includes it.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds on a WIDE viewport, where min-height binds instead",
    async () => {
      // Desktop padding with the coarse `--btn-h`: the row is `min-height` tall, which a per-tier literal cannot cover.
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );
});
