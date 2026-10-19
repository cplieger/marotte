// The run bar box's vertical balance, measured on rendered ink: the gap from the border to the first
// ink equals the gap from the last ink to the border, the fold row sits as far from the first run as
// two runs sit from each other, and the fold row's touch target reaches the hit floor without
// reaching the first run's.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { until } from "./__test-helpers__/until.js";
import { buildExecSummary } from "./exec-view/summary.js";
import { chevronEl } from "./chevron.js";
import type { ExecNode, ExecRun } from "./exec-view/model.js";

/** What counts as equal: a pixel of rounding in the ink scan or in a font's line box. */
const TOLERANCE_PX = 1;

const step = (id: string, state: ExecNode["state"]): ExecNode => ({
  path: id,
  label: id,
  kind: "step",
  state,
  children: [],
});

const runOf = (label: string, done: number, total: number): ExecRun => ({
  label,
  state: "running",
  live: true,
  nodes: Array.from({ length: total }, (_, i) =>
    step(`${label}-${String(i)}`, i < done ? "ok" : i === done ? "running" : "pending"),
  ),
});

let style: HTMLStyleElement | null = null;
let host: HTMLElement | null = null;

afterEach(async () => {
  host?.remove();
  host = null;
  style?.remove();
  style = null;
  document.documentElement.removeAttribute("data-pointer");
  await browser.viewport(1280, 720);
});

interface Mounted {
  box: HTMLElement;
  toggle: HTMLButtonElement | null;
  rows: HTMLElement[];
}

/** The chat's band as the page builds it: the fold row above the list from three runs, the rows in
 *  the list, every row hidden while the fold is closed. */
function mount(count: number, open: boolean, width: number): Mounted {
  host = document.createElement("div");
  host.style.cssText = `inline-size:${String(width)}px;padding-block:48px`;
  host.style.background = "var(--c-bg-primary)";
  const form = document.createElement("form");
  form.className = "bottom-bar";
  const box = document.createElement("div");
  box.className = "run-bar-host";
  const fold = document.createElement("div");
  fold.className = "run-bar-fold";
  const bar = document.createElement("ul");
  bar.className = "run-bar";

  const folded = count >= 3;
  fold.hidden = !folded;
  let toggle: HTMLButtonElement | null = null;
  if (folded) {
    toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "run-bar-open run-bar-toggle";
    toggle.setAttribute("aria-expanded", String(open));
    const chev = document.createElement("span");
    chev.className = "run-bar-chev";
    chev.append(chevronEl());
    const glyph = document.createElement("span");
    glyph.className = "run-bar-glyph";
    glyph.dataset["status"] = "working";
    const text = document.createElement("span");
    text.className = "run-bar-count";
    text.textContent = `${String(count)} workflows running`;
    toggle.append(chev, glyph, text);
    fold.append(toggle);
  }

  const rows = Array.from({ length: count }, (_, i) => {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    li.hidden = folded && !open;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "run-bar-open";
    const glyph = document.createElement("span");
    glyph.className = "run-bar-glyph";
    glyph.dataset["status"] = "working";
    const summary = buildExecSummary({ alertHost: btn });
    btn.append(glyph, summary.root);
    summary.paint(runOf(`nightly-sweep-${String(i)}`, 1 + (i % 3), 4 + 2 * (i % 4)));
    li.append(btn);
    bar.append(li);
    return li;
  });

  box.append(fold, bar);
  form.append(box);
  host.append(form);
  document.body.append(host);
  return { box, toggle, rows };
}

/** The entry rise must be over before pixels are read, so each finite animation is jumped to its
 *  end, re-checked a frame later until a style flush finds none running; the screenshot then
 *  captures a fresh frame.
 *  The working mark's infinite beat only fills the glyph's hole, inside its box, so its phase
 *  moves no ink row. */
async function settle(): Promise<void> {
  await until(() => {
    // A layout read flushes pending style, which is where a CSS animation starts.
    void document.body.offsetHeight;
    const running = document
      .getAnimations()
      .filter(
        (a) =>
          Number.isFinite(a.effect?.getComputedTiming().endTime ?? Infinity) &&
          a.playState !== "finished" &&
          a.playState !== "idle",
      );
    for (const a of running) {
      a.finish();
    }
    return running.length === 0;
  }, "the finite animations to end");
}

interface Ink {
  /** Empty rows between the border and the first ink. */
  top: number;
  /** Empty rows between the last ink and the border. */
  bottom: number;
  /** Ink rows grouped into bands, as [first, last] y offsets from the box's top edge. */
  bands: [number, number][];
}

/** Read the box's painted rows. A row is EMPTY when every pixel inside the border, clear of the
 *  corner arcs, matches the box surface; INK when the pixels differ from the surface and from each
 *  other; and neither otherwise: the border row, which a fractional box height blends across one or
 *  two pixel rows of the clip, is uniform and so is never read as ink or as empty. */
async function readInk(box: HTMLElement): Promise<Ink> {
  const png = await browser.elementLocator(box).screenshot({ base64: true, save: false });
  const bytes = Uint8Array.from(atob(png as unknown as string), (c) => c.charCodeAt(0));
  const img = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
  const canvas = document.createElement("canvas");
  canvas.width = img.width;
  canvas.height = img.height;
  const g = canvas.getContext("2d");
  expect(g, "a 2d canvas").not.toBeNull();
  g!.drawImage(img, 0, 0);
  const px = g!.getImageData(0, 0, img.width, img.height).data;

  const at = (x: number, y: number): number[] => {
    const i = (y * img.width + x) * 4;
    return [px[i] ?? 0, px[i + 1] ?? 0, px[i + 2] ?? 0];
  };
  const apart = (p: number[], q: number[]): boolean =>
    Math.abs((p[0] ?? 0) - (q[0] ?? 0)) +
      Math.abs((p[1] ?? 0) - (q[1] ?? 0)) +
      Math.abs((p[2] ?? 0) - (q[2] ?? 0)) >
    24;
  const surface = at(3, 3);
  const arc = 16;
  const kinds: ("empty" | "ink" | "edge")[] = [];
  for (let y = 0; y < img.height; y++) {
    const lead = at(arc, y);
    let differs = false;
    let varies = false;
    for (let x = arc; x < img.width - arc; x++) {
      const p = at(x, y);
      differs ||= apart(p, surface);
      varies ||= apart(p, lead);
    }
    kinds.push(!differs ? "empty" : varies ? "ink" : "edge");
  }
  const inked = kinds.map((k) => k === "ink");
  const bands: [number, number][] = [];
  let from = -1;
  inked.forEach((v, y) => {
    if (v && from < 0) {
      from = y;
    }
    if (!v && from >= 0) {
      bands.push([from, y - 1]);
      from = -1;
    }
  });
  expect(bands.length, "the box paints some ink").toBeGreaterThan(0);
  const first = bands[0]![0];
  const last = bands[bands.length - 1]![1];
  return {
    top: kinds.slice(0, first).filter((k) => k === "empty").length,
    bottom: kinds.slice(last + 1).filter((k) => k === "empty").length,
    bands,
  };
}

/** The ink band holding an element's vertical middle. */
function bandOf(ink: Ink, box: HTMLElement, el: Element | null | undefined): [number, number] {
  expect(el, "an element to place in a band").toBeTruthy();
  const r = el!.getBoundingClientRect();
  const y = (r.top + r.bottom) / 2 - box.getBoundingClientRect().top;
  const hit = ink.bands.find(([a, b]) => y >= a - 1 && y <= b + 1);
  expect(hit, `an ink band at y=${String(y)}`).toBeDefined();
  return hit!;
}

const PHONES = [
  [390, 844],
  [360, 740],
] as const;

describe("the run bar box's vertical balance", () => {
  for (const [w, h] of PHONES) {
    describe(`on a ${String(w)}px phone`, () => {
      it("keeps the gap above the fold row equal to the gap below the last bar", async () => {
        await browser.viewport(w, h);
        style = mountAppCSS();
        document.documentElement.setAttribute("data-pointer", "coarse");
        const { box } = mount(4, true, w - 16);
        await settle();
        const ink = await readInk(box);
        expect(Math.abs(ink.top - ink.bottom)).toBeLessThanOrEqual(TOLERANCE_PX);
      });

      it("keeps the fold row as far from the first run as two runs are from each other", async () => {
        await browser.viewport(w, h);
        style = mountAppCSS();
        document.documentElement.setAttribute("data-pointer", "coarse");
        const { box, toggle } = mount(4, true, w - 16);
        await settle();
        const ink = await readInk(box);
        const head = bandOf(ink, box, toggle?.querySelector(".run-bar-count"));
        const titles = [...box.querySelectorAll(".ev-sum-label")];
        const firstTitle = bandOf(ink, box, titles[0]);
        const secondTitle = bandOf(ink, box, titles[1]);
        const firstBar = bandOf(ink, box, box.querySelector(".ev-mini"));
        const headToRow = firstTitle[0] - head[1] - 1;
        const rowToRow = secondTitle[0] - firstBar[1] - 1;
        expect(Math.abs(headToRow - rowToRow)).toBeLessThanOrEqual(TOLERANCE_PX);
      });

      it("centres the fold row when the list is closed", async () => {
        await browser.viewport(w, h);
        style = mountAppCSS();
        document.documentElement.setAttribute("data-pointer", "coarse");
        const { box } = mount(4, false, w - 16);
        await settle();
        const ink = await readInk(box);
        expect(Math.abs(ink.top - ink.bottom)).toBeLessThanOrEqual(TOLERANCE_PX);
      });

      for (const runs of [1, 2]) {
        it(`balances the box of ${String(runs)} run${runs === 1 ? "" : "s"}, which has no fold row`, async () => {
          await browser.viewport(w, h);
          style = mountAppCSS();
          document.documentElement.setAttribute("data-pointer", "coarse");
          const { box } = mount(runs, true, w - 16);
          await settle();
          const ink = await readInk(box);
          expect(Math.abs(ink.top - ink.bottom)).toBeLessThanOrEqual(TOLERANCE_PX);
        });
      }
    });
  }

  it("balances the desktop box as well", async () => {
    await browser.viewport(1280, 800);
    style = mountAppCSS();
    const { box } = mount(4, true, 1000);
    await settle();
    const ink = await readInk(box);
    expect(Math.abs(ink.top - ink.bottom)).toBeLessThanOrEqual(TOLERANCE_PX);
  });
});

/** The vertical span of points whose hit test lands inside `owner`, scanned down the box's middle. */
function hitSpan(
  owner: Element,
  box: HTMLElement,
  within?: Element,
): { top: number; bottom: number } {
  const b = box.getBoundingClientRect();
  const x = Math.round(b.left + b.width / 2);
  const w = within?.getBoundingClientRect();
  const from = Math.round(w?.top ?? b.top - 30);
  const to = Math.round(w?.bottom ?? b.bottom);
  let top = Number.POSITIVE_INFINITY;
  let bottom = Number.NEGATIVE_INFINITY;
  for (let y = from; y < to; y++) {
    const hit = document.elementFromPoint(x, y);
    if (hit !== null && owner.contains(hit)) {
      top = Math.min(top, y);
      bottom = Math.max(bottom, y + 1);
    }
  }
  return { top, bottom };
}

describe("the fold row's touch target", () => {
  for (const [w, h] of PHONES) {
    it(`reaches the 44px hit floor at ${String(w)}px on a coarse pointer, inside the page`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      document.documentElement.setAttribute("data-pointer", "coarse");
      const { box, toggle } = mount(4, true, w - 16);
      await settle();
      const probe = document.createElement("div");
      probe.style.blockSize = "var(--hit-floor)";
      host!.append(probe);
      const hitFloor = probe.getBoundingClientRect().height;
      probe.remove();
      expect(hitFloor, "the coarse floor").toBeCloseTo(44, 0);

      const span = hitSpan(toggle!, box);
      expect(span.bottom - span.top).toBeGreaterThanOrEqual(hitFloor - 0.5);
    });

    it(`stops short of the first run's own target at ${String(w)}px`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      document.documentElement.setAttribute("data-pointer", "coarse");
      const { box, toggle, rows } = mount(4, true, w - 16);
      await settle();
      const first = rows[0]!.querySelector<HTMLElement>(".run-bar-open")!;
      expect(hitSpan(toggle!, box, first).top, "no toggle hit inside the first run").toBe(
        Number.POSITIVE_INFINITY,
      );
      const own = hitSpan(first, box, first);
      const rect = first.getBoundingClientRect();
      expect(own.bottom - own.top).toBeGreaterThanOrEqual(Math.floor(rect.height) - 1);
    });
  }
});

/** The box's padding and borders, which sit on top of the cap. */
function chromeOf(box: HTMLElement): number {
  const cs = getComputedStyle(box);
  return (
    parseFloat(cs.paddingTop) +
    parseFloat(cs.paddingBottom) +
    parseFloat(cs.borderTopWidth) +
    parseFloat(cs.borderBottomWidth)
  );
}

describe("the run bar box's cap", () => {
  it("shows all seven runs at once on a 360x740 phone, inside half the viewport", async () => {
    await browser.viewport(360, 740);
    style = mountAppCSS();
    document.documentElement.setAttribute("data-pointer", "coarse");
    const { box, rows } = mount(7, true, 344);
    await settle();
    const list = box.querySelector<HTMLElement>(".run-bar")!;
    expect(box.getBoundingClientRect().height - chromeOf(box)).toBeLessThanOrEqual(0.5 * 740 + 0.5);
    expect(list.scrollHeight, "nothing to scroll").toBeLessThanOrEqual(list.clientHeight);
    const last = rows[rows.length - 1]!.getBoundingClientRect();
    expect(last.bottom, "the seventh row is fully visible").toBeLessThanOrEqual(
      list.getBoundingClientRect().bottom + 0.5,
    );
    const ink = await readInk(box);
    expect(Math.abs(ink.top - ink.bottom)).toBeLessThanOrEqual(TOLERANCE_PX);
  });

  it("caps a long list at half the viewport on a phone and scrolls to its last row", async () => {
    await browser.viewport(360, 740);
    style = mountAppCSS();
    document.documentElement.setAttribute("data-pointer", "coarse");
    const { box, rows } = mount(14, true, 344);
    await settle();
    const list = box.querySelector<HTMLElement>(".run-bar")!;
    expect(box.getBoundingClientRect().height - chromeOf(box)).toBeLessThanOrEqual(0.5 * 740 + 0.5);
    expect(list.scrollHeight, "fourteen runs overflow the cap").toBeGreaterThan(list.clientHeight);
    list.scrollTop = list.scrollHeight;
    const last = rows[rows.length - 1]!.getBoundingClientRect();
    const view = list.getBoundingClientRect();
    expect(last.bottom, "the last row scrolls fully into view").toBeLessThanOrEqual(
      view.bottom + 0.5,
    );
    expect(last.top).toBeGreaterThanOrEqual(view.top - 0.5);
  });

  it("fits seven runs inside the cap on a 390px phone and reaches the last row", async () => {
    await browser.viewport(390, 844);
    style = mountAppCSS();
    document.documentElement.setAttribute("data-pointer", "coarse");
    const { box, rows } = mount(7, true, 374);
    await settle();
    const list = box.querySelector<HTMLElement>(".run-bar")!;
    expect(box.getBoundingClientRect().height - chromeOf(box)).toBeLessThanOrEqual(0.5 * 844 + 0.5);
    list.scrollTop = list.scrollHeight;
    const last = rows[rows.length - 1]!.getBoundingClientRect();
    expect(last.bottom).toBeLessThanOrEqual(list.getBoundingClientRect().bottom + 0.5);
  });

  it("keeps the whole box inside a quarter of the viewport on a desktop", async () => {
    await browser.viewport(1280, 800);
    style = mountAppCSS();
    const { box, rows } = mount(7, true, 1000);
    await settle();
    const list = box.querySelector<HTMLElement>(".run-bar")!;
    expect(box.getBoundingClientRect().height - chromeOf(box)).toBeLessThanOrEqual(
      0.25 * 800 + 0.5,
    );
    list.scrollTop = list.scrollHeight;
    expect(rows[rows.length - 1]!.getBoundingClientRect().bottom).toBeLessThanOrEqual(
      list.getBoundingClientRect().bottom + 0.5,
    );
  });

  it("keeps the fold row in view while a long list scrolls", async () => {
    await browser.viewport(360, 740);
    style = mountAppCSS();
    document.documentElement.setAttribute("data-pointer", "coarse");
    const { box, toggle } = mount(14, true, 344);
    await settle();
    const list = box.querySelector<HTMLElement>(".run-bar")!;
    const before = toggle!.getBoundingClientRect().top;
    list.scrollTop = list.scrollHeight / 2;
    expect(list.scrollTop, "the list scrolled").toBeGreaterThan(0);
    const after = toggle!.getBoundingClientRect();
    expect(after.top).toBe(before);
    const hit = document.elementFromPoint(
      after.left + after.width / 2,
      after.top + after.height / 2,
    );
    expect(hit !== null && toggle!.contains(hit), "the toggle is still what a tap reaches").toBe(
      true,
    );
  });

  it("squares the list's top corners under the fold row and rounds them without one", async () => {
    await browser.viewport(1280, 800);
    style = mountAppCSS();
    const folded = mount(4, true, 1000);
    const top = getComputedStyle(folded.box.querySelector(".run-bar")!);
    expect(top.borderTopLeftRadius).toBe("0px");
    expect(top.borderTopRightRadius).toBe("0px");
    expect(
      parseFloat(top.borderBottomLeftRadius),
      "the bottom corners stay concentric",
    ).toBeGreaterThan(0);
    host?.remove();
    const lone = mount(2, true, 1000);
    expect(
      parseFloat(getComputedStyle(lone.box.querySelector(".run-bar")!).borderTopLeftRadius),
    ).toBeGreaterThan(0);
  });
});

describe("the fold row's reach", () => {
  for (const [w, h, tier] of [
    [390, 844, "coarse"],
    [1280, 800, "fine"],
  ] as const) {
    it(`spans its whole line at ${String(w)}px, a tap near the inline end included`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      document.documentElement.setAttribute("data-pointer", tier);
      const { box, toggle } = mount(4, true, w <= 768 ? w - 16 : 1000);
      await settle();
      const line = box.querySelector<HTMLElement>(".run-bar-fold")!.getBoundingClientRect();
      const t = toggle!.getBoundingClientRect();
      expect(t.width).toBeCloseTo(line.width, 0);
      const y = t.top + t.height / 2;
      for (const x of [t.left + 4, t.right - 4]) {
        const hit = document.elementFromPoint(x, y);
        expect(
          hit !== null && toggle!.contains(hit),
          `the toggle at x=${String(Math.round(x))}`,
        ).toBe(true);
      }
    });
  }
});
