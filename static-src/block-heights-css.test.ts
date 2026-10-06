// `block-heights.ts`'s estimate table is a SHADOW of the stylesheet, and this holds it
// there: custom-property reserves cannot be resolved to px without layout, so the module
// keeps literals. A skipped element's box is `max(min-height, contain-intrinsic-size +
// padding + border)`, a function of the CSS alone, read off the LAST instance's rect (the
// containment ROOT's rect is the rendered-skipped box). The premise: the last instance is
// skipped and the first is not. `thinking` and `steerNote` have no reserve and are
// measured as real heights on the opposite premise. The pipeline case reads container
// totals instead. 10 cases per tier, through one `tierCases` helper.

import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

// Two builders reach `scroll.ts`, a self-initialising singleton; the canonical mock suffices.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { ENTRY_ESTIMATE_PX, ROW_GAP_PX, type EntryEstimates } from "./block-heights.js";
import { buildSubagentCard, buildSubagentContainer } from "./fundamentals/subagent-block.js";
import { buildRunCard } from "./fundamentals/run-card.js";
import { buildReasoning } from "./fundamentals/reasoning.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { planElement } from "./messages-plan.js";

/** Enough instances that the last is far past the scrollport: 120 x 38px against 400px. */
const INSTANCES = 120;

/** The scrollport's height. Every instance past it is off-viewport and therefore
 *  skipped, which is what makes the LAST one's rect the rendered-skipped box. */
const WRAP_H = 400;

/** Per-case timeout, matching the metrics suites: a case's cost is four rAF turns, and
 *  a loaded `npm test` prices a turn at hundreds of ms. */
const LOADED_BUDGET_MS = 30_000;

/**
 * How far a font metric may move the ONE measured entry (`thinking`); every other entry
 * is pinned to the byte.
 */
const FONT_SLACK_PX = 1;

/** Class that turns the skip off, so the same list reads a second time with every box
 *  genuinely rendered. Only the PIPELINE case needs it — see `readTotals`. */
const FORCE = "block-heights-force-render";

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.subagent-block`'s own (0,1,0), so it wins wherever it is inserted.
  force = document.createElement("style");
  force.textContent = `.${FORCE} .subagent-block { content-visibility: visible }`;
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

/** A child for the premise probe, something `checkVisibility` can be asked about. */
function marker(): HTMLElement {
  const s = document.createElement("span");
  s.textContent = "x";
  return s;
}

/**
 * `.msg-row` as `messages.ts` `makeRow` builds it; hand-built because that builder's
 * module graph is the whole feature layer.
 */
function msgRow(): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  row.appendChild(marker());
  return row;
}

/** The empty PAD a `padBlocks` slot mounts: the same element plus `is-empty`, which
 *  `13-messages.css` takes out of flow entirely. */
function emptyRow(): HTMLElement {
  const row = msgRow();
  row.classList.add("is-empty");
  return row;
}

/**
 * A claim-only `.tool-call` in `buildToolCard`'s class list (`tool-depth1-none` for a
 * `read`); hand-built for `msgRow`'s reason.
 */
function toolCard(): HTMLElement {
  const card = document.createElement("div");
  card.className = "tool-call tool-depth1-none";
  card.appendChild(marker());
  return card;
}

/** A settled delegate card, from its real builder. */
function subagentCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "completed", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  sa.setSummary({ elapsedMs: 3_000 });
  return sa.root;
}

/** A collapsed run card from its real builder; collapsed is what the table's number MEANS. */
function runCard(i: number): HTMLElement {
  const view = buildRunCard(`wf-${String(i)}`, `recipe-${String(i)}`, () => undefined, {
    wasOpen: () => undefined,
    defaultOpen: false,
    onOpenChange: () => undefined,
  });
  return view.root;
}

/**
 * A COLLAPSED pipeline container over `stages` settled stage cards, from its real builder:
 * `subagentCard` prices a WHOLE pipeline, so the cards inside must contribute nothing.
 */
function pipelineBox(stages: number): (i: number) => HTMLElement {
  return (i: number): HTMLElement => {
    const c = buildSubagentContainer(
      `Subagent pipeline \u00b7 ${String(stages)} stages`,
      "completed",
      {
        startOpen: false,
      },
    );
    c.setSummary({ elapsedMs: 3_000 });
    for (let s = 0; s < stages; s++) {
      c.body.append(subagentCard(i * 100 + s));
    }
    return c.root;
  };
}

/** A plan card with ONE entry: the reserve counts one entry row. */
function planCard(i: number): HTMLElement {
  return planElement([{ content: `step ${String(i)}`, priority: "medium", status: "pending" }]);
}

/**
 * A one-line steer note from its real builder, the least it can be (`read`, user origin,
 * no clause): the shape the table prices.
 */
function steerNote(i: number): HTMLElement {
  return buildSteerNote({
    text: `keep going ${String(i)}`,
    origin: "user",
    dropped: false,
  });
}

/** A SEALED reasoning trace from its real builder; no reserve, so its contents matter. */
function sealedTrace(): HTMLElement {
  const r = buildReasoning("a few sentences of a settled trace", false, false);
  r.settle();
  return r.root;
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

/**
 * Jump every finite entry animation to its end; an infinite one is skipped because
 * `Animation.finish()` throws on it.
 */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    if (Number.isFinite(anim.effect?.getComputedTiming().endTime ?? Infinity)) {
      anim.finish();
    }
  }
}

interface Reading {
  /** The rendered-skipped box: the LAST instance's own rect. */
  readonly box: number;
  /** The FIRST instance's rect, which is real. Reported so a case that fails on the
   *  premise says what it was reading instead. */
  readonly realBox: number;
  /** The premise. Both halves, because "nothing is skipped" and "everything is
   *  skipped" are different failures. */
  readonly premise: { readonly firstRendered: boolean; readonly lastSkipped: boolean };
  /** Every term the box is made of, so a red case names which one moved. */
  readonly terms: {
    readonly contentVisibility: string;
    readonly containIntrinsicBlockSize: string;
    readonly paddingBlock: number;
    readonly borderBlock: number;
    readonly minBlockSize: string;
  };
}

async function read(build: (i: number) => HTMLElement): Promise<Reading> {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;
  const list = document.createElement("div");
  list.className = "turn-body";
  for (let i = 0; i < INSTANCES; i++) {
    list.appendChild(build(i));
  }
  wrap.appendChild(list);
  host.replaceChildren(wrap);

  await frame();
  finishAnimations(wrap);
  await frame();

  const first = list.firstElementChild as HTMLElement;
  const last = list.lastElementChild as HTMLElement;
  const cs = getComputedStyle(last);
  const probe = (el: Element | null): boolean =>
    el !== null && el.checkVisibility({ contentVisibilityAuto: true });

  return {
    box: Number(last.getBoundingClientRect().height.toFixed(2)),
    realBox: Number(first.getBoundingClientRect().height.toFixed(2)),
    premise: {
      firstRendered: probe(first.firstElementChild),
      lastSkipped: !probe(last.firstElementChild),
    },
    terms: {
      contentVisibility: cs.contentVisibility,
      containIntrinsicBlockSize: cs.containIntrinsicBlockSize,
      paddingBlock: Number.parseFloat(cs.paddingBlockStart) + Number.parseFloat(cs.paddingBlockEnd),
      borderBlock:
        Number.parseFloat(cs.borderBlockStartWidth) + Number.parseFloat(cs.borderBlockEndWidth),
      minBlockSize: cs.minBlockSize,
    },
  };
}

interface Totals {
  /** The box each instance resolves to when RENDERED, the list's row gaps taken back
   *  out. This is the number a stage card can move. */
  readonly perBox: number;
  /** Skipped total minus rendered total: 0 when the estimate IS the resting shape. */
  readonly drift: number;
  /** The collapsed body's own terms, echoed so a red case names what moved. */
  readonly bodyTerms: { readonly contentVisibility: string; readonly blockSize: string };
}

/**
 * How far an inflated estimate must move the total before the readings can be believed; a
 * floor, not an expectation.
 */
const PREMISE_FLOOR = INSTANCES * 10;

/** The estimate the premise probe substitutes. Far from every real box height here, so
 *  the shift it produces cannot be a rounding difference. */
const PROBE_PX = 400;

/**
 * The CONTAINER TOTAL over one list, read three times: on the estimate, on a wrong one,
 * and with the skip forced off. The only instrument that sees a stage card's contribution:
 * a skipped container's rect is its reserve, and an inner card's rect is its real box.
 * The probe reading comes before the force-render, after which a box remembers its size.
 */
async function readTotals(build: (i: number) => HTMLElement): Promise<Totals> {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;
  const list = document.createElement("div");
  list.className = "turn-body";
  for (let i = 0; i < INSTANCES; i++) {
    list.appendChild(build(i));
  }
  wrap.appendChild(list);
  host.replaceChildren(wrap);

  await frame();
  finishAnimations(wrap);
  await frame();
  const skipped = list.scrollHeight;
  const body = list.firstElementChild?.querySelector(".subagent-body") ?? null;
  const bodyCS = body === null ? null : getComputedStyle(body);

  const probeStyle = document.createElement("style");
  probeStyle.textContent = `.turn-body .subagent-block { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`;
  document.head.appendChild(probeStyle);
  await frame();
  const probed = list.scrollHeight;
  probeStyle.remove();

  list.classList.add(FORCE);
  await frame();
  finishAnimations(wrap);
  await frame();
  const rendered = list.scrollHeight;
  list.classList.remove(FORCE);

  expect(
    probed - skipped,
    "boxes are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(PREMISE_FLOOR);

  const pad = (() => {
    const cs = getComputedStyle(list);
    return Number.parseFloat(cs.paddingBlockStart) + Number.parseFloat(cs.paddingBlockEnd);
  })();

  return {
    // `.turn-body` puts N-1 gaps between N boxes and its own padding is in `scrollHeight`,
    // both read off the container rather than stated.
    perBox: (rendered - (INSTANCES - 1) * ROW_GAP_PX - pad) / INSTANCES,
    drift: skipped - rendered,
    bodyTerms: {
      contentVisibility: bodyCS?.contentVisibility ?? "(no body)",
      blockSize: bodyCS?.blockSize ?? "(no body)",
    },
  };
}

/** Assert one entry, premise first. The received `terms` are echoed into the expected
 *  object so a failure prints them beside the number without asserting them. */
function expectShadows(r: Reading, want: number, label: string): void {
  expect(r.premise, `${label}: the last instance's contents are being skipped`).toEqual({
    firstRendered: true,
    lastSkipped: true,
  });
  expect({ box: r.box, ...r.terms }, `${label}: the rendered-skipped box`).toEqual({
    box: want,
    ...r.terms,
  });
}

/** Set the pointer tier the way `pointer-tier.ts` does. */
function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** The cases every tier runs, so neither tier can be pinned and the other forgotten. */
function tierCases(name: "fine" | "coarse", enter: () => Promise<void>): void {
  const est = (): EntryEstimates => ENTRY_ESTIMATE_PX[name];

  it(
    "prices `text` and `row` at what .msg-row reserves",
    async () => {
      await enter();
      const r = await read(msgRow);
      expectShadows(r, est().text, "text");
      // A blockless message mounts ONE row, so the two entries answer for the same
      // element and cannot legitimately differ.
      expectShadows(r, est().row, "row");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `emptyText` at nothing, because the pad is out of flow",
    async () => {
      await enter();
      const r = await read(emptyRow);
      // `display: none` generates no box, so there is no premise: the rule is the assertion.
      expect(getComputedStyle(host.querySelector(".msg-row.is-empty")!).display).toBe("none");
      expect({ box: r.box, realBox: r.realBox }).toEqual({ box: 0, realBox: 0 });
      expect(est().emptyText).toBe(0);
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `toolCard` at .tool-call's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(toolCard), est().toolCard, "toolCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `runCard` at .run-card's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(runCard), est().runCard, "runCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `subagentCard` at .subagent-block's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(subagentCard), est().subagentCard, "subagentCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `planCard` at .plan-message's reserve plus its padding and border",
    async () => {
      await enter();
      // This reserve states the CONTENT height alone, so padding adds on top. Tier-invariant,
      // which running both tiers proves.
      expectShadows(await read(planCard), est().planCard, "planCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices a whole PIPELINE at `subagentCard`, whatever its stage count",
    async () => {
      await enter();
      // A stage card inside the collapsed body (`content-visibility: hidden` at height 0) adds
      // nothing, so one card prices the whole pipeline: read at ONE stage and at THREE.
      const one = await readTotals(pipelineBox(1));
      const three = await readTotals(pipelineBox(3));
      expect(
        {
          oneStage: one.perBox,
          threeStages: three.perBox,
          oneStageDrift: one.drift,
          threeStagesDrift: three.drift,
          ...three.bodyTerms,
        },
        "a pipeline is one card, and its stages are out of the box's layout",
      ).toEqual({
        oneStage: est().subagentCard,
        threeStages: est().subagentCard,
        oneStageDrift: 0,
        threeStagesDrift: 0,
        ...three.bodyTerms,
      });
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `thinking` at a sealed trace's REAL collapsed height, there being no reserve",
    async () => {
      await enter();
      const r = await read(sealedTrace);
      // The opposite premise: `.reasoning-block` declares no `content-visibility`, so its real
      // rect is legitimate.
      expect(
        {
          contentVisibility: r.terms.contentVisibility,
          reserve: r.terms.containIntrinsicBlockSize,
          bothReal: r.box === r.realBox,
        },
        "a sealed trace is never skipped, so its price is a measured height",
      ).toEqual({ contentVisibility: "visible", reserve: "none", bothReal: true });
      // The one entry not pinned to the byte: on the fine tier `max(--hit-floor, line box +
      // padding)` has its terms under a pixel apart, so the font stack decides; FONT_SLACK_PX is
      // that disagreement and no wider.
      expect(
        Math.abs(r.box - est().thinking),
        `thinking: the collapsed summary row measured ${String(r.box)}px against an estimate of ${String(est().thinking)}px`,
      ).toBeLessThanOrEqual(FONT_SLACK_PX);
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `steerNote` at a one-line note's REAL height, there being no reserve",
    async () => {
      await enter();
      const r = await read(steerNote);
      // `thinking`'s premise: `.steer-note` declares no `content-visibility`, so nothing is skipped.
      expect(
        {
          contentVisibility: r.terms.contentVisibility,
          reserve: r.terms.containIntrinsicBlockSize,
          bothReal: r.box === r.realBox,
        },
        "a steer note is never skipped, so its price is a measured height",
      ).toEqual({ contentVisibility: "visible", reserve: "none", bothReal: true });
      // Tier-dependent: the head's floor is `--btn-h` (36 -> 44) and nothing else tiers, so the
      // tiers differ by that step. FONT_SLACK_PX for `thinking`'s reason.
      expect(
        Math.abs(r.box - est().steerNote),
        `steerNote: a one-line note measured ${String(r.box)}px against an estimate of ${String(est().steerNote)}px`,
      ).toBeLessThanOrEqual(FONT_SLACK_PX);
    },
    LOADED_BUDGET_MS,
  );

  it(
    "puts ROW_GAP_PX on the gapped level and none on the spacer's own parent",
    async () => {
      await enter();
      // `spacerHeight` adds one more ROW_GAP_PX on the premise that `.turn` supplies no gap;
      // both halves are read off a mounted instance.
      const turn = document.createElement("div");
      turn.className = "turn";
      const body = document.createElement("div");
      body.className = "turn-body";
      body.appendChild(marker());
      turn.appendChild(body);
      host.replaceChildren(turn);
      await frame();
      const gapOf = (el: HTMLElement): number => {
        const raw = getComputedStyle(el).rowGap;
        return raw === "normal" ? 0 : Number.parseFloat(raw);
      };
      expect({ turnBody: gapOf(body), turn: gapOf(turn) }).toEqual({
        turnBody: ROW_GAP_PX,
        turn: 0,
      });
    },
    LOADED_BUDGET_MS,
  );
}

describe("the fine-pointer tier", () => {
  tierCases("fine", async () => {
    tier("fine");
    await Promise.resolve();
  });
});

describe("the coarse-pointer tier, measured at a real viewport size", () => {
  // Last in the file and restoring the size it found (`page.viewport` has no getter). 768px
  // also engages `01-tokens.css`'s width-keyed fallback, so attribute and fallback agree.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  tierCases("coarse", async () => {
    await page.viewport(768, 900);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      768, 900,
    ]);
    tier("coarse");
  });
});
