import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page, userEvent } from "vitest/browser";

import { allRules, manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let area: HTMLElement | undefined;
/** Far from the map, so a real pointer left by a hover case rests over no row of the next fixture. */
let park: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
  park = document.createElement("div");
  park.style.cssText = "position:fixed;left:0;bottom:0;width:4px;height:4px;";
  document.body.appendChild(park);
});

afterAll(() => {
  style.remove();
  park.remove();
});

afterEach(async () => {
  await userEvent.hover(park);
  area?.remove();
  area = undefined;
});

/** A token's computed value for `prop`, so a comparison is computed against computed. */
function probe(prop: string, token: string): string {
  const el = document.createElement("span");
  el.style.setProperty(prop, `var(${token})`);
  document.body.appendChild(el);
  const value = getComputedStyle(el).getPropertyValue(prop);
  el.remove();
  return value;
}

type Attrs = Record<string, string>;

interface Fixture {
  nav: HTMLElement;
  track: HTMLElement;
  stack: HTMLElement;
  row: (name: string) => HTMLElement;
  steps: HTMLElement[];
}

const ROWS: [string, Attrs][] = [
  ["plain", { "data-severity": "clean" }],
  ["inView", { "data-severity": "clean", "data-in-view": "" }],
  ["current", { "data-severity": "clean", "data-current": "" }],
  ["currentAgent", { "data-severity": "clean", "data-current": "", "data-trigger": "system" }],
  ["running", { "data-severity": "running" }],
  ["currentRunning", { "data-severity": "running", "data-current": "" }],
  ["broken", { "data-severity": "broken" }],
  ["stopped", { "data-severity": "stopped" }],
  ["agent", { "data-severity": "clean", "data-trigger": "system" }],
  ["hit", { "data-severity": "clean", "data-hit": "" }],
];

function buildMap(rows: [string, Attrs][] = ROWS): Fixture {
  area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = "position:fixed;top:0;left:0;width:1120px;height:600px;";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  outer.style.cssText = "position:absolute;inset:0;";
  const nav = document.createElement("nav");
  nav.className = "turn-map";
  nav.toggleAttribute("data-shown", true);
  const prev = document.createElement("button");
  prev.className = "turn-map-step";
  const stack = document.createElement("ol");
  stack.className = "turn-map-stack";
  stack.style.setProperty("--turn-map-slots", String(rows.length));
  stack.style.setProperty("--turn-map-total", String(rows.length));
  const track = document.createElement("div");
  track.className = "turn-map-track";
  track.appendChild(stack);
  const next = document.createElement("button");
  next.className = "turn-map-step";
  nav.append(prev, track, next);
  const byName = new Map<string, HTMLElement>();
  rows.forEach(([name, attrs], i) => {
    const li = document.createElement("li");
    li.className = "turn-pill";
    li.style.setProperty("--rail-at", String(i / (rows.length - 1)));
    for (const [k, v] of Object.entries(attrs)) {
      li.setAttribute(k, v);
    }
    const a = document.createElement("a");
    a.className = "turn-pill-link";
    a.href = `#turn-${String(i + 1)}`;
    const mark = document.createElement("span");
    mark.className = "turn-pill-mark";
    const hit = document.createElement("span");
    hit.className = "turn-pill-hit";
    a.append(mark, hit);
    li.appendChild(a);
    stack.appendChild(li);
    byName.set(name, li);
  });
  outer.appendChild(nav);
  area.appendChild(outer);
  document.body.appendChild(area);
  return {
    nav,
    track,
    stack,
    row: (name) => {
      const li = byName.get(name);
      if (li === undefined) {
        throw new Error(`no row ${name}`);
      }
      return li;
    },
    steps: [prev, next],
  };
}

function link(li: HTMLElement): HTMLElement {
  const a = li.querySelector<HTMLElement>(".turn-pill-link");
  if (a === null) {
    throw new Error("row without a link");
  }
  return a;
}

function bar(li: HTMLElement): CSSStyleDeclaration {
  return getComputedStyle(link(li), "::before");
}

function part(li: HTMLElement, cls: string): CSSStyleDeclaration {
  const el = li.querySelector<HTMLElement>(`.${cls}`);
  if (el === null) {
    throw new Error(`row without .${cls}`);
  }
  return getComputedStyle(el);
}

function settle(): void {
  for (const a of document.getAnimations()) {
    a.finish();
  }
}

/** The painted pixels of `el`, read at CSS-px offsets from its box. A `::before` mask is visible
 *  to no style or geometry API, so the paint is the only instrument for a hollow middle. */
async function pixels(el: HTMLElement): Promise<(x: number, y: number) => number[]> {
  const png = await page.screenshot({ element: el, save: false });
  const blob = await (await fetch(`data:image/png;base64,${png}`)).blob();
  const bitmap = await createImageBitmap(blob);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;
  const ctx = canvas.getContext("2d");
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.drawImage(bitmap, 0, 0);
  const scale = bitmap.width / el.getBoundingClientRect().width;
  return (x, y) => [...ctx.getImageData(Math.floor(x * scale), Math.floor(y * scale), 1, 1).data];
}

function distance(a: readonly number[], b: readonly number[]): number {
  return Math.hypot(...a.slice(0, 3).map((v, i) => v - (b[i] ?? 0)));
}

function scaleX(li: HTMLElement): number {
  return new DOMMatrixReadOnly(bar(li).transform).a;
}

/** The bar's painted span in viewport px: a `::before` has no rect of its own. */
function barSpan(li: HTMLElement): { left: number; right: number } {
  const box = link(li).getBoundingClientRect();
  const right = box.right - parseFloat(getComputedStyle(link(li)).paddingInlineEnd);
  return { left: right - parseFloat(bar(li).width) * scaleX(li), right };
}

const FULL = 1;
const NEAR = 16 / 24;
const NEXT = 12 / 24;
const REST = 10 / 24;

/** A stack of clean rows named r0, r1, … at the 8px pitch. */
function ladderMap(count: number, extra: Record<number, Attrs> = {}): Fixture {
  return buildMap(
    Array.from({ length: count }, (_, i): [string, Attrs] => [
      `r${String(i)}`,
      { "data-severity": "clean", ...extra[i] },
    ]),
  );
}

function ladder(f: Fixture, count: number): number[] {
  settle();
  return Array.from({ length: count }, (_, i) => scaleX(f.row(`r${String(i)}`)));
}

function expectLadder(got: number[], want: number[]): void {
  expect(got).toHaveLength(want.length);
  want.forEach((w, i) => {
    expect(got[i], `row ${String(i)}`).toBeCloseTo(w, 4);
  });
}

describe("the position marks are ink steps and width, never an accent fill", () => {
  it("draws a resting bar in tertiary ink", () => {
    const f = buildMap();
    expect(bar(f.row("plain")).backgroundColor).toBe(probe("color", "--c-text-tertiary"));
  });

  it("brightens the turns on screen to secondary ink", () => {
    const f = buildMap();
    expect(bar(f.row("inView")).backgroundColor).toBe(probe("color", "--c-text-secondary"));
  });

  it("draws the current turn full width in primary ink", () => {
    const f = buildMap();
    expect(bar(f.row("current")).backgroundColor).toBe(probe("color", "--c-text-primary"));
    expect(bar(f.row("current")).width).toBe("24px");
    expect(scaleX(f.row("current"))).toBe(FULL);
    expect(scaleX(f.row("plain"))).toBeCloseTo(REST, 4);
  });

  it("draws a current agent-initiated turn as wide as any current turn", () => {
    const f = buildMap();
    expect(scaleX(f.row("currentAgent"))).toBe(scaleX(f.row("current")));
  });

  it("gives no mark the accent and the current row no fill box", () => {
    const f = buildMap();
    const accent = probe("color", "--c-accent");
    for (const name of ["plain", "inView", "current"]) {
      expect(bar(f.row(name)).backgroundColor, name).not.toBe(accent);
    }
    expect(getComputedStyle(link(f.row("current"))).backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("keeps one position attribute: no rule in the bundle names data-selected", () => {
    const offenders = manifestSheets().flatMap(({ name, css }) =>
      allRules(css)
        .filter((r) => r.selector.includes("data-selected"))
        .map((r) => `${name}: ${r.selector}`),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps a running current turn's bar full width in primary ink and says running in the lane", () => {
    const f = buildMap();
    const b = bar(f.row("currentRunning"));
    expect(b.backgroundColor).toBe(probe("color", "--c-text-primary"));
    expect(b.boxShadow).toBe("none");
    expect(b.maskImage).toBe("none");
    expect(scaleX(f.row("currentRunning"))).toBe(scaleX(f.row("current")));

    const mark = part(f.row("currentRunning"), "turn-pill-mark");
    expect(mark.boxShadow).toContain(probe("color", "--c-dot-working"));
    expect(parseFloat(mark.height)).toBeLessThan(parseFloat(mark.width));
    expect(part(f.row("current"), "turn-pill-mark").boxShadow).toBe("none");
  });

  it("draws an agent-initiated turn at half width", () => {
    const f = buildMap();
    expect(scaleX(f.row("agent"))).toBeCloseTo(scaleX(f.row("plain")) / 2, 5);
  });
});

describe("severity is a shape", () => {
  it("fills a disc for a broken turn", () => {
    const f = buildMap();
    const mark = part(f.row("broken"), "turn-pill-mark");
    expect(mark.backgroundColor).toBe(probe("color", "--c-red"));
    expect(mark.borderRadius).toBe("50%");
    expect(mark.boxShadow).toBe("none");
  });

  it("draws a ring with a transparent middle for a stopped turn", () => {
    const f = buildMap();
    const mark = part(f.row("stopped"), "turn-pill-mark");
    expect(mark.backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(mark.boxShadow).toContain("inset");
    expect(mark.boxShadow).toContain(probe("color", "--c-yellow"));
  });

  it("draws no mark for a clean turn", () => {
    const f = buildMap();
    const mark = part(f.row("plain"), "turn-pill-mark");
    expect(mark.backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(mark.boxShadow).toBe("none");
  });

  it("outlines a running bar in the in-flight violet, with no beat", () => {
    const f = buildMap();
    const b = bar(f.row("running"));
    expect(b.backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(b.boxShadow).toContain(probe("color", "--c-dot-working"));
    expect(b.animationName).toBe("none");
    expect(getComputedStyle(f.row("running")).animationName).toBe("none");
  });

  it("keeps consecutive running rows hollow at the 2px floor, where clean rows are solid", async () => {
    const names = ["run1", "run2", "run3", "clean1", "clean2", "clean3"];
    const f = buildMap(
      names.map((n): [string, Attrs] => [
        n,
        { "data-severity": n.startsWith("run") ? "running" : "clean" },
      ]),
    );
    // The densest layout binning allows: one row per 2px of track, consecutive in mid-stack.
    const slots = Math.floor(f.track.getBoundingClientRect().height / 2);
    f.stack.style.setProperty("--turn-map-slots", String(slots));
    f.stack.style.setProperty("--turn-map-total", String(slots * 2));
    const mid = Math.floor(slots / 2);
    names.forEach((n, i) => {
      f.row(n).style.setProperty("--rail-at", String((mid + i) / (slots - 1)));
    });
    const shot = await pixels(f.stack);
    const box = f.stack.getBoundingClientRect();

    for (const n of names) {
      const r = f.row(n).getBoundingClientRect();
      expect(r.height, n).toBeCloseTo(2, 1);
      const b = barSpan(f.row(n));
      const y = r.top + r.height / 2 - box.top;
      const cx = (b.left + b.right) / 2 - box.left;
      const ground = shot(b.left - box.left - 4, y);
      const inked = (x: number): boolean => distance(shot(x, y), ground) > 40;
      if (n.startsWith("run")) {
        expect(inked(cx - 3.5), `${n} left end`).toBe(true);
        expect(inked(cx), `${n} middle`).toBe(false);
        expect(inked(cx + 3.5), `${n} right end`).toBe(true);
      } else {
        expect(inked(cx), `${n} middle`).toBe(true);
      }
    }
  });

  for (const pitch of [2, 3]) {
    it(`leaves ground between neighbouring bars at a ${String(pitch)}px pitch`, async () => {
      const names = ["a", "b", "c"];
      const f = buildMap(names.map((n): [string, Attrs] => [n, { "data-severity": "clean" }]));
      f.track.style.flex = "none";
      f.track.style.blockSize = "600px";
      const slots = 600 / pitch;
      f.stack.style.setProperty("--turn-map-slots", String(slots));
      f.stack.style.setProperty("--turn-map-total", String(slots));
      const mid = Math.floor(slots / 2);
      names.forEach((n, i) => {
        f.row(n).style.setProperty("--rail-at", String((mid + i) / (slots - 1)));
      });
      const shot = await pixels(f.stack);
      const box = f.stack.getBoundingClientRect();
      const centre = (n: string): number => {
        const r = f.row(n).getBoundingClientRect();
        expect(r.height, n).toBeCloseTo(pitch, 1);
        return r.top + r.height / 2 - box.top;
      };
      const b = barSpan(f.row("a"));
      const cx = (b.left + b.right) / 2 - box.left;
      const ground = shot(b.left - box.left - 4, centre("a"));

      for (const [upper, lower] of [
        ["a", "b"],
        ["b", "c"],
      ] as const) {
        const from = centre(upper);
        const to = centre(lower);
        expect(distance(shot(cx, from), ground), `${upper} inked`).toBeGreaterThan(40);
        const between: number[] = [];
        for (let y = from; y <= to; y += 0.25) {
          between.push(distance(shot(cx, y), ground));
        }
        expect(Math.min(...between), `ground between ${upper} and ${lower}`).toBeLessThan(12);
      }
    });
  }

  it("marks a search hit with a square in the find highlight's ink", () => {
    const f = buildMap();
    const hit = part(f.row("hit"), "turn-pill-hit");
    expect(hit.backgroundColor).toBe(probe("color", "--c-accent"));
    expect(hit.borderRadius).toBe("0px");
    expect(hit.width).toBe(hit.height);
    expect(part(f.row("plain"), "turn-pill-hit").backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("keeps the lanes out of the pointer's way", () => {
    const f = buildMap();
    expect(part(f.row("broken"), "turn-pill-mark").pointerEvents).toBe("none");
    expect(part(f.row("hit"), "turn-pill-hit").pointerEvents).toBe("none");
  });

  it("keeps both lanes clear of a full-width bar", async () => {
    const f = buildMap([
      ["full", { "data-severity": "broken", "data-current": "", "data-hit": "" }],
      ["spare", { "data-severity": "clean" }],
    ]);
    const row = f.row("full");
    const shot = await pixels(f.stack);
    const box = f.stack.getBoundingClientRect();
    const r = row.getBoundingClientRect();
    const y = r.top + r.height / 2 - box.top;
    const ground = shot(box.width / 2, 0.5);
    const runs: [number, number][] = [];
    for (let x = 0.25; x < box.width; x += 0.5) {
      if (distance(shot(x, y), ground) <= 40) {
        continue;
      }
      const last = runs.at(-1);
      if (last !== undefined && x - last[1] <= 0.5) {
        last[1] = x;
      } else {
        runs.push([x, x]);
      }
    }
    expect(runs, "disc, bar and hit as three separate runs").toHaveLength(3);
    const [, painted] = runs;
    const width = (painted?.[1] ?? 0) - (painted?.[0] ?? 0) + 0.5;
    expect(Math.abs(width - 24), `painted bar ${String(width)}px`).toBeLessThanOrEqual(1.5);
  });
});

describe("the fisheye moves width by transform alone, behind a hover gate", () => {
  it("transitions the bar's transform only", () => {
    const f = buildMap();
    expect(bar(f.row("plain")).transitionProperty).toBe("transform");
  });

  it("grows the bar from its inline end", () => {
    const f = buildMap();
    const [x] = bar(f.row("plain")).transformOrigin.split(" ");
    expect(x).toBe(bar(f.row("plain")).width);
  });

  it("draws the pointed row full width and its neighbours at 2/3 and 1/2", async () => {
    const f = ladderMap(9);
    await userEvent.hover(link(f.row("r4")));
    expectLadder(ladder(f, 9), [REST, REST, NEXT, NEAR, FULL, NEAR, NEXT, REST, REST]);
  });

  it("follows a swipe, so the rows the pointer left settle back to rest", async () => {
    const f = ladderMap(9);
    for (const i of [1, 2, 3, 4, 5, 6]) {
      await userEvent.hover(link(f.row(`r${String(i)}`)));
    }
    expectLadder(ladder(f, 9), [REST, REST, REST, REST, NEXT, NEAR, FULL, NEAR, NEXT]);
  });

  it("holds a hovered current row at full width, not past it", async () => {
    const f = ladderMap(5, { 2: { "data-current": "" } });
    await userEvent.hover(link(f.row("r2")));
    expectLadder(ladder(f, 5), [NEXT, NEAR, FULL, NEAR, NEXT]);
  });

  it("keeps an agent-initiated neighbour on the ladder's tier", async () => {
    const f = ladderMap(5, { 3: { "data-trigger": "system" } });
    await userEvent.hover(link(f.row("r2")));
    expectLadder(ladder(f, 5), [NEXT, NEAR, FULL, NEAR, NEXT]);
  });

  it("drives the same ladder from a keyboard focus", async () => {
    const f = ladderMap(9);
    link(f.row("r2")).focus();
    await userEvent.keyboard("{Tab}");
    expect(link(f.row("r3")).matches(":focus-visible")).toBe(true);
    expectLadder(ladder(f, 9), [REST, NEXT, NEAR, FULL, NEAR, NEXT, REST, REST, REST]);
  });

  it("takes the larger tier where the pointer's ladder meets the keyboard's", async () => {
    const f = ladderMap(9);
    link(f.row("r4")).focus();
    await userEvent.keyboard("{Tab}");
    await userEvent.hover(link(f.row("r2")));
    expectLadder(ladder(f, 9), [NEXT, NEAR, FULL, NEAR, NEAR, FULL, NEAR, NEXT, REST]);
  });

  it("leaves no ladder behind a pointer's focus", async () => {
    const f = ladderMap(9);
    f.stack.addEventListener("click", (e) => {
      e.preventDefault();
    });
    await userEvent.click(link(f.row("r3")));
    expect(document.activeElement).toBe(link(f.row("r3")));
    await userEvent.hover(park);
    expectLadder(ladder(f, 9), [REST, REST, REST, REST, REST, REST, REST, REST, REST]);
  });

  it("gates every hover rule on any-hover, never hover", () => {
    const sheet = manifestSheets().find((s) => s.name === "29-turns.css")?.css ?? "";
    const turnMapRules = sheet
      .split("@media")
      .filter((chunk) => chunk.includes(".turn-pill:hover"));
    expect(turnMapRules.length).toBeGreaterThan(0);
    for (const chunk of turnMapRules) {
      expect(chunk.trimStart().startsWith("(any-hover: hover)")).toBe(true);
    }
  });
});

describe("the step buttons rest invisible with their boxes reserved", () => {
  function glyph(b: HTMLElement): SVGElement {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    b.appendChild(svg);
    return svg;
  }

  it("hides the glyph at rest and keeps the button's box", () => {
    const f = buildMap();
    for (const b of f.steps) {
      expect(getComputedStyle(glyph(b)).opacity).toBe("0");
      expect(getComputedStyle(b).visibility).toBe("visible");
      expect(b.getBoundingClientRect().height).toBeGreaterThan(0);
    }
  });

  it("shows the glyph while focus is inside the map", () => {
    const f = buildMap();
    const glyphs = f.steps.map(glyph);
    link(f.row("plain")).focus();
    settle();
    for (const g of glyphs) {
      expect(getComputedStyle(g).opacity).toBe("1");
    }
  });

  it("leaves a disabled step to the shared disabled face", () => {
    const f = buildMap();
    const [prev, next] = f.steps;
    if (!(prev instanceof HTMLButtonElement) || next === undefined) {
      throw new Error("no step buttons");
    }
    prev.disabled = true;
    link(f.row("plain")).focus();
    settle();
    expect(getComputedStyle(prev).opacity).not.toBe(getComputedStyle(next).opacity);
    expect(getComputedStyle(prev).cursor).toBe("not-allowed");
  });
});
