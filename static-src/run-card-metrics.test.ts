// `.run-card`'s intrinsic-size estimate has to cover the card's REAL floor, on every pointer tier.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control.
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { iconEl } from "./icon-el.js";
import { chevronEl } from "./chevron.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "./icons.js";
import { paintStateMark } from "./exec-view/status.js";

/** Long enough that a per-card error is a four-figure number in the list total, so a failure is
 *  unmistakable rather than a rounding argument. Also the divisor the real card height is
 *  reported through, so it has to match the card count. */
const CARDS = 200;

const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with every card
 *  genuinely rendered. */
const FORCE = "run-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness can prove the
 *  estimate is what the first reading is made of. */
const PROBE = "run-metrics-probe-estimate";

/** Far from every real card height, so the shift it produces cannot be confused with a rounding
 *  difference. */
const PROBE_PX = 400;

/** A case's cost here is SIX rAF turns, and a loaded `npm test` prices a turn at hundreds of ms
 *  whatever the list holds -- measured on `files-row-metrics.test.ts`, whose cases run 86-280ms cold
 *  in isolation and ~4.1s inside a full run. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.run-card`'s own (0,1,0), so both win wherever they are inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .run-card { content-visibility: visible }`,
    `.${PROBE} .run-card { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  // Out of the way of anything else the page holds, and a fixed inline size so a card's own width
  // never depends on the body's flow.
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

/** One step row, with the children `fundamentals/run-card.ts` `stepRow` gives it: the state
 *  mark, the node's own name, its meta line and its duration, on an anchor (the row is a DOOR
 *  into the run tab, not a disclosure). */
function stepRow(i: number): HTMLElement {
  const root = document.createElement("div");
  root.className = "run-step";
  root.dataset["node"] = `build-${String(i)}`;
  root.dataset["status"] = "ok";
  root.dataset["nodeType"] = "step";

  const glyph = document.createElement("span");
  glyph.className = "run-step-glyph";
  glyph.setAttribute("aria-hidden", "true");
  paintStateMark(glyph, "ok");

  const name = document.createElement("span");
  name.className = "run-step-name";
  name.textContent = `build-${String(i)}`;

  const meta = document.createElement("span");
  meta.className = "run-step-meta";
  meta.textContent = "general-task-execution \u00b7 claude-sonnet-5";

  const dur = document.createElement("span");
  dur.className = "run-step-dur";
  dur.textContent = "12s";

  const head = document.createElement("a");
  head.className = "run-step-head";
  head.href = `/run/r-${String(i)}#node=build-${String(i)}`;
  head.append(glyph, name, meta, dur);

  root.appendChild(head);
  return root;
}

/** One `.run-card`, with the four regions `fundamentals/run-card.ts` builds: head, alert (hidden
 *  until the run wants a person), the disclosure body holding the step rows, and the foot. Only
 *  the BODY is inside the disclosure, which is the whole point of the measurement: the reserve
 *  has to cover the regions that are present whatever the disclosure says. */
function runCard(i: number, open: boolean): HTMLElement {
  const root = document.createElement("div");
  root.className = open ? "run-card" : "run-card collapsed";
  root.dataset["run"] = `r-${String(i)}`;
  root.dataset["status"] = "completed";

  const icon = document.createElement("span");
  icon.className = "run-icon";
  icon.setAttribute("aria-hidden", "true");
  icon.appendChild(iconEl(ICON_TAB_RUN));

  const name = document.createElement("span");
  name.className = "run-name";
  name.textContent = `code-review-${String(i)}`;

  const count = document.createElement("span");
  count.className = "run-count";
  count.textContent = "1 step";

  const toggle = document.createElement("span");
  toggle.className = "run-toggle";
  toggle.setAttribute("aria-hidden", "true");
  toggle.appendChild(chevronEl());

  const head = document.createElement("div");
  head.className = "run-head";
  head.setAttribute("role", "button");
  head.setAttribute("tabindex", "0");
  head.setAttribute("aria-expanded", open ? "true" : "false");
  head.append(icon, name, count, toggle);

  const alert = document.createElement("div");
  alert.className = "run-alert hidden";
  alert.setAttribute("role", "status");
  alert.setAttribute("aria-live", "polite");

  const steps = document.createElement("div");
  steps.className = "run-steps";
  steps.appendChild(stepRow(i));

  const outputs = document.createElement("dl");
  outputs.className = "run-outputs hidden";

  const body = document.createElement("div");
  body.className = "run-body uip-disclosure-region";
  body.setAttribute("aria-hidden", open ? "false" : "true");
  body.inert = !open;
  if (!open) {
    body.style.height = "0px";
  }
  body.append(steps, outputs);

  const ledger = document.createElement("span");
  ledger.className = "run-ledger";
  ledger.textContent = "1 step \u00b7 1m 4s";

  const openLink = document.createElement("a");
  openLink.className = "run-open";
  openLink.href = `/run/r-${String(i)}`;
  openLink.append("Open run");
  const openIcon = document.createElement("span");
  openIcon.className = "run-open-icon";
  openIcon.setAttribute("aria-hidden", "true");
  openIcon.appendChild(iconEl(ICON_EXTERNAL));
  openLink.appendChild(openIcon);

  const foot = document.createElement("div");
  foot.className = "run-foot";
  foot.append(ledger, openLink);

  root.append(head, alert, body, foot);
  return root;
}

/** The production nesting: a fixed-height scroller holding the `.turn-body` block container a
 *  turn's cards live in. `.turn-body` is a flex column with a gap, and the gap is the same in
 *  all three readings, so it cancels out of the drift while staying faithful to what the
 *  transcript lays out. */
function mountList(open: boolean): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;

  const list = document.createElement("div");
  list.className = "turn-body";
  for (let i = 0; i < CARDS; i++) {
    list.appendChild(runCard(i, open));
  }

  wrap.appendChild(list);
  host.replaceChildren(wrap);
  return { wrap, list };
}

/** Yield until the renderer has run, which is what updates `content-visibility: auto` relevance. */
async function frame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    anim.finish();
  }
}

interface Metrics {
  readonly listSkipped: number;
  /** The same, with the estimate replaced by `PROBE_PX`. The harness's own sensitivity check --
   *  see `expectPremise`. */
  readonly listProbe: number;
  readonly listRendered: number;
}

/** Read the same 200-card list three times: on the shipped estimate, on a deliberately wrong
 *  one, and with the skip turned off. */
async function measure(open: boolean): Promise<Metrics> {
  const { wrap, list } = mountList(open);
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Still skipped, so this reads the fallback rather than a remembered size. Done BEFORE the
  // force-render, because a card that has been rendered once remembers its real size and stops
  // consulting the fallback at all.
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

/** THE PREMISE, without which every assertion below is vacuous: if Chromium were not skipping
 *  any card, the first and third readings would agree for the trivial reason that both measured
 *  rendered cards, and the case would pass against any estimate at all. */
function expectPremise(m: Metrics): void {
  expect(
    m.listProbe - m.listSkipped,
    "cards are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);
}

/** THE PROPERTY for a COLLAPSED card: the reserve IS the floor, so the container reports one
 *  height whether its cards are skipped or rendered. Stated as the DRIFT so a failure names the
 *  px rather than two five-figure totals, with the real card height beside it so the message
 *  says which tier it was measuring. */
function expectNoDrift(m: Metrics): void {
  expectPremise(m);
  expect({
    drift: m.listSkipped - m.listRendered,
    realCardHeight: m.listRendered / CARDS,
  }).toEqual({ drift: 0, realCardHeight: m.listRendered / CARDS });
}

/** THE PROPERTY for an OPEN card: one estimate cannot be exact for both disclosure states, so
 *  what is pinned here is the SIGN. */
function expectNeverOverStates(m: Metrics): void {
  expectPremise(m);
  const drift = m.listSkipped - m.listRendered;
  expect(
    { overStatesBy: Math.max(drift, 0), realCardHeight: m.listRendered / CARDS },
    "the reserve must not exceed an open card's real height",
  ).toEqual({ overStatesBy: 0, realCardHeight: m.listRendered / CARDS });
}

describe("the fine-pointer tier", () => {
  it(
    "reports one height whether its collapsed cards are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states a card open with one step",
    async () => {
      tier("fine");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // A coarse pointer has TWO cases -- narrow, where `01-tokens.css`'s width-keyed fallback also
  // binds, and wide, where only the attribute does -- and they measure identically here, which is
  // the point rather than a redundancy: it is what makes one expression exact on all three tiers.
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
    "holds for a collapsed card on a NARROW viewport",
    async () => {
      // 48rem is the fallback's own boundary and `<=` includes it, so this is the widest viewport
      // that still takes the coarse control heights with no attribute set.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a collapsed card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states an open card on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states an open card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );
});
