import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { cachePointerTier, markCoarseSeen } from "./device-view.js";
import { initPointerTier } from "./pointer-tier.js";

type Tier = "fine" | "coarse";

function markupAt(openTag: string, tag: string): string {
  const start = indexHtml.indexOf(openTag);
  expect(start, `static/index.html has no ${openTag}`).toBeGreaterThan(-1);
  const tags = new RegExp(`<${tag}\\b|</${tag}\\s*>`, "g");
  tags.lastIndex = start;
  let depth = 0;
  for (let m = tags.exec(indexHtml); m !== null; m = tags.exec(indexHtml)) {
    depth += m[0].startsWith("</") ? -1 : 1;
    if (depth === 0) {
      return indexHtml.slice(start, m.index + m[0].length);
    }
  }
  throw new Error(`unbalanced ${tag} after ${openTag}`);
}

let style: HTMLStyleElement;
let host: HTMLElement;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(() => {
  style = mountAppCSS();
  entry = { width: window.innerWidth, height: window.innerHeight };
});

afterAll(async () => {
  style.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

afterEach(() => {
  host.remove();
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
});

async function settled(): Promise<void> {
  for (let i = 0; i < 2; i++) {
    await new Promise((r) => requestAnimationFrame(r));
  }
}

interface Controls {
  readonly [name: string]: HTMLElement;
  readonly segBar: HTMLElement;
  readonly iconBtn: HTMLElement;
  readonly shellBtn: HTMLElement;
  readonly steerAct: HTMLElement;
  readonly tabClose: HTMLElement;
}

function mount(): Controls {
  host = document.createElement("div");
  host.innerHTML = `
    <div class="page-shell"><div class="page-header">${markupAt('<nav id="docs-tab-bar"', "nav")}</div></div>
    ${markupAt('<button type="button" id="theme-btn"', "button")}
    <div class="shell-panel">${markupAt('<div class="shell-header">', "div")}</div>
    <ul class="steer-stack"><li class="steer-row" data-state="sent">
      <span class="steer-state"></span><span class="steer-text">queued</span>
      <span class="steer-actions"><button type="button" class="steer-act" aria-label="Discard">
        <svg class="ic-ui" viewBox="0 0 24 24"></svg></button></span>
    </li></ul>
    <div class="tab" role="tab"><span class="tab-name">A chat</span>
      <span class="tab-close" aria-hidden="true"><svg class="ic-inline" viewBox="0 0 24 24"></svg></span></div>`;
  document.body.appendChild(host);
  const pick = (sel: string): HTMLElement => {
    const el = host.querySelector<HTMLElement>(sel);
    expect(el, sel).not.toBeNull();
    return el!;
  };
  return {
    segBar: pick("#docs-tab-bar"),
    iconBtn: pick("#theme-btn"),
    shellBtn: pick("#shell-restart-btn"),
    steerAct: pick(".steer-act"),
    tabClose: pick(".tab-close"),
  };
}

/** Rounded to a tenth: a narrow viewport snaps a box a few millionths off its rem value. */
const px = (v: number): number => Math.round(v * 10) / 10;
const h = (el: HTMLElement): number => px(el.getBoundingClientRect().height);
const w = (el: HTMLElement): number => px(el.getBoundingClientRect().width);

const PAINTED: Record<Tier, Record<string, [number, number]>> = {
  fine: {
    segBar: [-1, 36],
    iconBtn: [32, 32],
    shellBtn: [24, 24],
    steerAct: [24, 24],
    tabClose: [24, 24],
  },
  coarse: {
    segBar: [-1, 44],
    iconBtn: [44, 44],
    shellBtn: [44, 44],
    steerAct: [44, 44],
    tabClose: [24, 24],
  },
};

function painted(els: Controls): Record<string, [number, number]> {
  return Object.fromEntries(
    Object.entries(els).map(([k, el]) => [k, [k === "segBar" ? -1 : w(el), h(el)]]),
  );
}

describe("a control's painted box follows the tier", () => {
  it.each(["fine", "coarse"] as const)("at the %s tier", async (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const els = mount();
    await settled();
    expect(painted(els)).toEqual(PAINTED[tier]);
  });

  it.each(["fine", "coarse"] as const)("grows the ×'s target to the floor at %s", async (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const { tabClose } = mount();
    await settled();
    const target = getComputedStyle(tabClose, "::after");
    expect(parseFloat(target.height)).toBe(tier === "fine" ? 24 : 44);
  });

  it.each(["fine", "coarse"] as const)(
    "grows a segment's target to the bar's edges at %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const { segBar } = mount();
      await settled();
      const seg = segBar.querySelector<HTMLElement>(".seg")!;
      const bar = segBar.getBoundingClientRect();
      const s = seg.getBoundingClientRect();
      const cx = s.left + s.width / 2;
      expect(document.elementFromPoint(cx, bar.top + 0.5)?.closest(".seg")).toBe(seg);
      expect(document.elementFromPoint(cx, bar.bottom - 0.5)?.closest(".seg")).toBe(seg);
    },
  );

  it("keeps the mouse sizes on a touched device driven by its mouse", async () => {
    markCoarseSeen();
    cachePointerTier("fine");
    initPointerTier();
    const els = mount();
    await settled();
    expect(document.documentElement.dataset["pointer"]).toBe("fine");
    expect(painted(els)).toEqual(PAINTED.fine);
  });

  it.each([
    [1180, 820],
    [390, 844],
  ] as const)("is the same box at %ix%i at each tier", async (width, height) => {
    await page.viewport(width, height);
    for (const tier of ["fine", "coarse"] as const) {
      document.documentElement.dataset["pointer"] = tier;
      const els = mount();
      await settled();
      expect(painted(els), `${tier} at ${String(width)}x${String(height)}`).toEqual(PAINTED[tier]);
      host.remove();
    }
  });
});

describe("a wrapped segment bar", () => {
  it.each(["fine", "coarse"] as const)(
    "gives each row the half of the gap nearest it, and the bar's edge, at %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      host = document.createElement("div");
      host.style.inlineSize = "18rem";
      host.innerHTML = markupAt('<nav id="mcp-modal-tabs"', "nav");
      document.body.appendChild(host);
      await settled();
      const segs = [...host.querySelectorAll<HTMLElement>(".seg")];
      const [top, , bottom] = segs.map((s) => s.getBoundingClientRect());
      expect(bottom!.top, "the bar wraps into two rows").toBeGreaterThan(top!.bottom);
      const bar = host.querySelector(".seg-bar")!.getBoundingClientRect();
      const cx = top!.left + top!.width / 2;
      const owner = (y: number): HTMLElement | null =>
        document.elementFromPoint(cx, y)?.closest<HTMLElement>(".seg") ?? null;

      expect(owner(bar.top + 0.5), "above the top row").toBe(segs[0]);
      expect(owner(top!.bottom + 0.5), "just under the top row").toBe(segs[0]);
      expect(owner(bottom!.top - 0.5), "just over the bottom row").toBe(segs[2]);
      expect(owner(bar.bottom - 0.5), "below the bottom row").toBe(segs[2]);
    },
  );
});

describe("a size keyed on the tier, not the width", () => {
  function mountRows(): { row: HTMLElement; check: HTMLElement; pill: HTMLElement } {
    host = document.createElement("div");
    host.innerHTML = `
      <div class="fb-row"><input type="checkbox" class="fb-check"><span class="fb-icon"></span>
        <span class="fb-name">file.ts</span></div>
      <form id="prompt-form"><div class="prompt-pills"><button type="button" class="pill">Auto</button></div></form>`;
    document.body.appendChild(host);
    return {
      row: host.querySelector<HTMLElement>(".fb-row")!,
      check: host.querySelector<HTMLElement>(".fb-check")!,
      pill: host.querySelector<HTMLElement>(".pill")!,
    };
  }

  function sizes(els: { row: HTMLElement; check: HTMLElement; pill: HTMLElement }): number[] {
    return [
      h(els.row),
      w(els.check),
      parseFloat(getComputedStyle(els.pill).fontSize),
      parseFloat(getComputedStyle(els.pill).paddingInlineStart),
    ];
  }

  it.each(["fine", "coarse"] as const)(
    "reads one set of sizes at 1180 and 390, %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      await page.viewport(1180, 820);
      const wide = sizes(mountRows());
      host.remove();
      await page.viewport(390, 844);
      const narrow = sizes(mountRows());
      expect(narrow).toEqual(wide);
    },
  );

  it("gives the coarse tier the finger's sizes on a wide screen", async () => {
    await page.viewport(1180, 820);
    document.documentElement.dataset["pointer"] = "fine";
    const fine = sizes(mountRows());
    host.remove();
    document.documentElement.dataset["pointer"] = "coarse";
    const coarse = sizes(mountRows());
    expect(coarse.map((v, i) => v > (fine[i] ?? Infinity))).toEqual([true, true, true, true]);
  });
});
