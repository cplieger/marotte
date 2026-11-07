// The coarse tier's touch contract: a control paints at the mouse's height and its target is the
// 44px hit floor (Apple HIG's hit region) around it, a row is its own 44px target, and no target
// covers another. Only a hit test reads the target, since boxes and declarations are identical
// whichever element wins a point. The fixture is `__test-helpers__/app-surfaces.ts`'s surfaces.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page, server } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  ALL,
  SURFACES,
  TWO,
  VIEWPORTS,
  clearSurface,
  frames,
  mountSurface,
  q,
  turn,
  type Size,
  type Surface,
} from "./__test-helpers__/app-surfaces.js";

const { buildAssistantBubble } = await import("./fundamentals/text-bubble.js");

const FLOOR = 44;

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(async () => {
  style.remove();
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  clearSurface();
  delete document.documentElement.dataset["pointer"];
});

/** Targets the layout cannot give 44px on one axis, each with the reason it is accepted. */
const NARROW: readonly { sel: string; sizes: readonly Size[]; axis: "inline" | "block" }[] = [
  // Nine segments share the 390px bar: each paints 36 wide and its target meets its neighbour's at
  // the gap midpoint, 40px wide.
  { sel: "#docs-tab-bar .seg", sizes: [390], axis: "inline" },
  // A × beside a filename or the next chip's ×: 24px inline, WCAG 2.5.8's minimum, since a wider
  // reach would cross the neighbour's target.
  { sel: ".attachment-close", sizes: ALL, axis: "inline" },
  { sel: "#agent-dirs-chips .chip-remove", sizes: ALL, axis: "inline" },
];

/** Shaped expanders, whose target is not centred on the box: the padding box grown by the
 *  `::after`'s used insets, which the press cases then hit-test like any other target. */
const SHAPED = new Set([
  ".attachment-close",
  "#agent-dirs-chips .chip-remove",
  "#fb-list .fb-check",
  "#fb-list .fb-git-clickable",
  "#run-bar-fold .run-bar-toggle",
  "#run-bar-fold .run-bar-all",
  "#run-bar .run-bar-all",
  "a.git-pr-row-title",
  ".ev-box-toggle",
]);

function shaped(el: HTMLElement, b: Box): Box {
  const cs = getComputedStyle(el, "::after");
  const out = (v: string): number => Math.max(0, -(parseFloat(v) || 0));
  // An absolute `::after` is placed against its host's padding box, inside any border.
  const left = b.left + el.clientLeft;
  const top = b.top + el.clientTop;
  return {
    left: Math.max(0, left - out(cs.left)),
    right: Math.min(window.innerWidth, left + el.clientWidth + out(cs.right)),
    top: Math.max(0, top - out(cs.top)),
    bottom: Math.min(window.innerHeight, top + el.clientHeight + out(cs.bottom)),
  };
}

interface Box {
  readonly left: number;
  readonly right: number;
  readonly top: number;
  readonly bottom: number;
}

interface Probe {
  readonly el: HTMLElement;
  readonly sel: string;
  readonly label: string;
  readonly box: Box;
  /** The target the contract promises: the box where it clears the floor, else the floor centred
   *  on it, cut at the screen's edge and at the midpoint of the gap to a neighbour it would cross. */
  t: Box;
}

function boxOf(el: HTMLElement): Box {
  const r = el.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
}

function floored(b: Box): Box {
  const w = Math.max(b.right - b.left, FLOOR) / 2;
  const h = Math.max(b.bottom - b.top, FLOOR) / 2;
  const cx = (b.left + b.right) / 2;
  const cy = (b.top + b.bottom) / 2;
  return {
    left: Math.max(0, cx - w),
    right: Math.min(window.innerWidth, cx + w),
    top: Math.max(0, cy - h),
    bottom: Math.min(window.innerHeight, cy + h),
  };
}

const span = (a0: number, a1: number, b0: number, b1: number): number =>
  Math.min(a1, b1) - Math.max(a0, b0);

/** Where two facing targets meet across a gap: its midpoint, unless one side reaches less than
 *  half of it (a box that already clears the floor reaches nothing), which leaves the rest to the
 *  other. */
function cut(nearEdge: number, nearReach: number, farEdge: number, farReach: number): number {
  const half = (farEdge - nearEdge) / 2;
  if (nearReach < half) {
    return nearEdge + nearReach;
  }
  if (farReach < half) {
    return farEdge - farReach;
  }
  return nearEdge + half;
}

/** Two neighbours whose floors would cross meet without overlapping. */
function meet(a: Probe, b: Probe): void {
  const across = span(a.t.top, a.t.bottom, b.t.top, b.t.bottom) > 0.5;
  const along = span(a.t.left, a.t.right, b.t.left, b.t.right) > 0.5;
  if (!(across && along)) {
    return;
  }
  const [l, r] = a.box.left < b.box.left ? [a, b] : [b, a];
  const [u, d] = a.box.top < b.box.top ? [a, b] : [b, a];
  if (span(a.box.top, a.box.bottom, b.box.top, b.box.bottom) > 0) {
    const mid = cut(l.box.right, l.t.right - l.box.right, r.box.left, r.box.left - r.t.left);
    l.t = { ...l.t, right: Math.min(l.t.right, mid) };
    r.t = { ...r.t, left: Math.max(r.t.left, mid) };
  } else {
    const mid = cut(u.box.bottom, u.t.bottom - u.box.bottom, d.box.top, d.box.top - d.t.top);
    u.t = { ...u.t, bottom: Math.min(u.t.bottom, mid) };
    d.t = { ...d.t, top: Math.max(d.t.top, mid) };
  }
}

const nested = (a: Probe, b: Probe): boolean => a.el.contains(b.el) || b.el.contains(a.el);

function probesFor(s: Surface, which: "all" | "controls" = "all"): Probe[] {
  const sels =
    which === "all" ? [...s.controls, ...(s.rows ?? [])] : [...s.controls, ...(s.painted ?? [])];
  const probes = sels.flatMap((sel) => {
    const found = [...document.querySelectorAll<HTMLElement>(sel)].filter(
      (e) => e.checkVisibility() && e.getBoundingClientRect().width > 0,
    );
    expect(found.length, `${s.name}: ${sel} renders`).toBeGreaterThan(0);
    return found.map((el, i) => {
      const box = boxOf(el);
      const t = SHAPED.has(sel) ? shaped(el, box) : floored(box);
      return { el, sel, label: `${sel}[${String(i)}]`, box, t };
    });
  });
  for (const [i, a] of probes.entries()) {
    for (const b of probes.slice(i + 1)) {
      if (!nested(a, b)) {
        meet(a, b);
      }
    }
  }
  return probes;
}

function reaches(p: Probe, x: number, y: number): string | null {
  const hit = document.elementFromPoint(x, y);
  if (hit !== null && p.el.contains(hit)) {
    return null;
  }
  const owner = hit?.closest("[id], [class]");
  const name =
    owner === null || owner === undefined
      ? "nothing"
      : owner.id !== ""
        ? `#${owner.id}`
        : (owner.getAttribute("class") ?? "");
  const b = boxOf(p.el);
  return `${p.label} [${b.left.toFixed(1)}..${b.right.toFixed(1)} x ${b.top.toFixed(1)}..${b.bottom.toFixed(1)}] at ${x.toFixed(1)},${y.toFixed(1)} reached ${name}`;
}

/** Mount the surface, then for each control bring it on screen and measure everything afresh. */
async function eachControl(
  s: Surface,
  size: Size,
  check: (p: Probe, all: Probe[]) => void,
): Promise<void> {
  await mountSurface(s, size, "coarse");
  const count = probesFor(s).length;
  for (let i = 0; i < count; i++) {
    probesFor(s)[i]?.el.scrollIntoView({ block: "center", inline: "nearest" });
    await frames();
    const all = probesFor(s);
    check(all[i] as Probe, all);
  }
}

interface Findings {
  /** Presses 1px inside a target that reached something else. */
  readonly misses: string[];
  /** Targets under 44px on an axis the layout should give it. */
  readonly short: string[];
  /** Presses on either side of two meeting targets that reached the wrong one. */
  readonly faults: string[];
}

/** One mounting of a surface answers all three per-control cases, each asserting its own list. */
const findings = new Map<string, Promise<Findings>>();

function measured(s: Surface, size: Size): Promise<Findings> {
  const key = `${s.name}@${String(size)}`;
  let f = findings.get(key);
  if (f === undefined) {
    f = measure(s, size);
    findings.set(key, f);
  }
  return f;
}

async function measure(s: Surface, size: Size): Promise<Findings> {
  const out: Findings = { misses: [], short: [], faults: [] };
  const accepted = (p: Probe, axis: "inline" | "block"): boolean =>
    NARROW.some((n) => n.sel === p.sel && n.sizes.includes(size) && n.axis === axis);
  await eachControl(s, size, (a, all) => {
    const { left, right, top, bottom } = a.t;
    const cx = (left + right) / 2;
    const cy = (top + bottom) / 2;
    for (const [x, y] of [
      [left + 1, cy],
      [right - 1, cy],
      [cx, top + 1],
      [cx, bottom - 1],
    ] as const) {
      const miss = reaches(a, x, y);
      if (miss !== null) {
        out.misses.push(miss);
      }
    }
    const w = right - left;
    const h = bottom - top;
    if (w < FLOOR - 0.5 && !(left <= 0 || right >= window.innerWidth) && !accepted(a, "inline")) {
      out.short.push(`${a.label}: ${w.toFixed(1)}px wide`);
    }
    if (h < FLOOR - 0.5 && !(top <= 0 || bottom >= window.innerHeight) && !accepted(a, "block")) {
      out.short.push(`${a.label}: ${h.toFixed(1)}px tall`);
    }
    for (const b of all) {
      if (b === a || nested(a, b)) {
        continue;
      }
      const across = span(a.t.top, a.t.bottom, b.t.top, b.t.bottom);
      const along = span(a.t.left, a.t.right, b.t.left, b.t.right);
      if (across > 2 && Math.abs(a.t.right - b.t.left) < 2) {
        const y = (Math.max(a.t.top, b.t.top) + Math.min(a.t.bottom, b.t.bottom)) / 2;
        for (const miss of [reaches(a, a.t.right - 1, y), reaches(b, b.t.left + 1, y)]) {
          if (miss !== null) {
            out.faults.push(miss);
          }
        }
      }
      if (along > 2 && Math.abs(a.t.bottom - b.t.top) < 2) {
        const x = (Math.max(a.t.left, b.t.left) + Math.min(a.t.right, b.t.right)) / 2;
        for (const miss of [reaches(a, x, a.t.bottom - 1), reaches(b, x, b.t.top + 1)]) {
          if (miss !== null) {
            out.faults.push(miss);
          }
        }
      }
    }
  });
  return out;
}

describe.each(ALL)("the coarse tier at %ipx", (size) => {
  beforeAll(async () => {
    await page.viewport(...VIEWPORTS[size]);
  });

  const cases = SURFACES.filter((s) => s.sizes.includes(size));

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: a press 1px inside each control's 44px target reaches it",
    async (_name, s) => {
      const { misses } = await measured(s, size);
      expect(misses, `\n${misses.join("\n")}\n`).toEqual([]);
    },
  );

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: each target clears 44px on both axes once neighbours meet",
    async (_name, s) => {
      const { short } = await measured(s, size);
      expect(short, `\n${short.join("\n")}\n`).toEqual([]);
    },
  );

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: a press in a neighbour's target reaches the neighbour",
    async (_name, s) => {
      const { faults } = await measured(s, size);
      expect(faults, `\n${faults.join("\n")}\n`).toEqual([]);
    },
  );

  const painted = cases.filter(
    (s) => s.controls.length + (s.painted?.length ?? 0) > 0 && s.ownFineForm !== true,
  );
  it.each(painted.map((s) => [s.name, s] as const))(
    "%s: each control paints at its mouse-tier height, or grows with its column's rows",
    async (_name, s) => {
      const heights = async (
        tier: "fine" | "coarse",
      ): Promise<{ controls: Map<string, number>; row: number }> => {
        await mountSurface(s, size, tier);
        const row = s.rowStep === undefined ? 0 : q(s.rowStep.row).getBoundingClientRect().height;
        return {
          controls: new Map(
            probesFor(s, "controls").map((p) => [p.label, p.box.bottom - p.box.top] as const),
          ),
          row,
        };
      };
      const { controls: fine, row: rowFine } = await heights("fine");
      const { controls: coarse, row: rowCoarse } = await heights("coarse");
      const stepped = new Set(s.rowStep?.controls);
      const step = (label: string): number =>
        stepped.has(label.replace(/\[\d+\]$/, "")) ? rowCoarse - rowFine : 0;
      if (s.rowStep !== undefined) {
        expect(rowCoarse - rowFine, `premise: ${s.rowStep.row} grows on coarse`).toBeGreaterThan(0);
      }
      const drift = [...coarse]
        .filter(
          ([label, h]) =>
            Math.abs(h - (fine.get(label) ?? Number.NaN) - step(label)) > 0.5 || !fine.has(label),
        )
        .map(
          ([label, h]) =>
            `${label}: ${h.toFixed(1)}px coarse, ${String(fine.get(label)?.toFixed(1))}px fine`,
        );
      expect(drift, `\n${drift.join("\n")}\n`).toEqual([]);
    },
  );

  it.each(cases.filter((s) => s.ownFineForm !== true).map((s) => [s.name, s] as const))(
    "%s: each glyph inside a control or row draws at its mouse-tier size",
    async (_name, s) => {
      const glyphs = async (tier: "fine" | "coarse"): Promise<Map<string, string>> => {
        await mountSurface(s, size, tier);
        return new Map(
          probesFor(s).flatMap((p) =>
            [...p.el.querySelectorAll("svg")]
              .filter((g) => g.checkVisibility())
              .map((g, i) => {
                const r = g.getBoundingClientRect();
                return [
                  `${p.label} svg[${String(i)}]`,
                  `${r.width.toFixed(1)}x${r.height.toFixed(1)}`,
                ] as const;
              }),
          ),
        );
      };
      const fine = await glyphs("fine");
      const coarse = await glyphs("coarse");
      expect(coarse.size, "glyphs drawn on coarse").toBe(fine.size);
      const grown = [...coarse]
        .filter(([label, wh]) => fine.get(label) !== wh)
        .map(([label, wh]) => `${label}: ${wh} coarse, ${String(fine.get(label))} fine`);
      expect(grown, `\n${grown.join("\n")}\n`).toEqual([]);
    },
  );
});

describe("a run card's step pitch on the coarse tier", () => {
  // A step is its 44px head plus a 1px border on each side, and the gap between steps is the 2px
  // the 44px head leaves for, inside a loop as at the top level.
  it.each(TWO)("is 48px at the top level and inside a loop at %ipx", async (size) => {
    await page.viewport(...VIEWPORTS[size]);
    const loop = SURFACES.find((s) => s.name === "a run card's loop")!;
    await mountSurface(loop, size, "coarse");
    const pitches = (sel: string): number[] => {
      const tops = [...document.querySelectorAll<HTMLElement>(sel)].map(
        (e) => e.getBoundingClientRect().top,
      );
      return tops.slice(1).map((t, i) => t - (tops[i] ?? 0));
    };
    const top = pitches(".run-steps > .run-step");
    const inLoop = pitches(".run-group-body > .run-step");
    expect(top.length, "two top-level steps").toBeGreaterThan(0);
    expect(inLoop.length, "three steps in the loop").toBeGreaterThan(1);
    expect([...top, ...inLoop]).toEqual([...top, ...inLoop].map(() => 48));
  });
});

describe("a transcript's task list on the coarse tier", () => {
  // A task mark sits on its text line (WCAG 2.5.8's inline case), so its own box is its target
  // and no part of it reaches the next line's mark.
  it.each(TWO)("a press anywhere in a mark reaches that mark at %ipx", async (size) => {
    await page.viewport(...VIEWPORTS[size]);
    await mountSurface(
      {
        name: "a task list",
        sizes: TWO,
        setup: () => {
          turn(buildAssistantBubble("- [ ] one\n- [ ] two\n", false).root);
        },
        controls: [],
      },
      size,
      "coarse",
    );
    const marks = [
      ...document.querySelectorAll<HTMLInputElement>('.message input[type="checkbox"]'),
    ];
    expect(marks.length, "two task marks").toBe(2);
    const misses: string[] = [];
    for (const [i, mark] of marks.entries()) {
      const b = boxOf(mark);
      const cx = (b.left + b.right) / 2;
      for (let y = b.top + 1; y < b.bottom; y += 1) {
        if (document.elementFromPoint(cx, y) !== mark) {
          misses.push(`mark ${String(i)} at y=${y.toFixed(1)}`);
        }
      }
    }
    expect(misses, `\n${misses.join("\n")}\n`).toEqual([]);
  });
});

describe("the chat header's icons", () => {
  // `--btn-h` is 2.25rem on every tier: touch grows their targets, never their boxes.
  it.each(ALL)(
    "paint their 36px mouse box on the coarse and the fine tier at %ipx",
    async (size) => {
      await page.viewport(...VIEWPORTS[size]);
      const header = SURFACES.find((s) => s.name === "the chat header")!;
      for (const [tier, want] of [
        ["fine", 36],
        ["coarse", 36],
      ] as const) {
        await mountSurface(header, size, tier);
        const sizes = probesFor(header).map(
          (p) =>
            `${p.label} ${String(p.box.bottom - p.box.top)}x${String(p.box.right - p.box.left)}`,
        );
        expect(sizes, tier).toEqual(
          probesFor(header).map((p) => `${p.label} ${String(want)}x${String(want)}`),
        );
      }
    },
  );
});

/** Everything a press can land on: the ARIA widget roles a pointer operates, focusable elements,
 *  a label that carries its checkbox or radio, and a row `wireRowToggle` makes a target. */
const INTERACTIVE = [
  "button",
  "a[href]",
  'input:not([type="hidden"])',
  "select",
  "textarea",
  "summary",
  '[role="button"]',
  '[role="checkbox"]',
  '[role="radio"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="switch"]',
  '[role="slider"]',
  '[tabindex]:not([tabindex="-1"])',
  'label:has(input[type="checkbox"], input[type="radio"])',
  "[data-row-toggle]",
].join(", ");

interface Hit {
  readonly el: Element;
  readonly label: string;
  readonly layer: Element;
  readonly box: Box;
  /** The box joined with any pseudo-element that reaches outside it, cut by every clip. */
  readonly target: Box;
  /** The box is on screen and inside every clip, and no scroller's fold cuts the target, so a
   *  short target is the layout's. */
  readonly whole: boolean;
}

const EMPTY: Box = { left: 0, right: 0, top: 0, bottom: 0 };
const area = (b: Box): boolean => b.right - b.left > 0.5 && b.bottom - b.top > 0.5;
const join = (a: Box, b: Box): Box => ({
  left: Math.min(a.left, b.left),
  right: Math.max(a.right, b.right),
  top: Math.min(a.top, b.top),
  bottom: Math.max(a.bottom, b.bottom),
});
const crop = (a: Box, b: Box): Box => {
  const c = {
    left: Math.max(a.left, b.left),
    right: Math.min(a.right, b.right),
    top: Math.max(a.top, b.top),
    bottom: Math.min(a.bottom, b.bottom),
  };
  return area(c) ? c : EMPTY;
};
const px = (v: string): number => parseFloat(v) || 0;

function paddingBox(e: Element, cs: CSSStyleDeclaration): Box {
  const r = e.getBoundingClientRect();
  return {
    left: r.left + px(cs.borderLeftWidth),
    right: r.right - px(cs.borderRightWidth),
    top: r.top + px(cs.borderTopWidth),
    bottom: r.bottom - px(cs.borderBottomWidth),
  };
}

/** Cut `b` to what `e` lets through: its padding box on each axis it clips. */
function clipBy(e: Element, b: Box): Box {
  const cs = getComputedStyle(e);
  const paint = /paint|strict|content/.test(cs.contain) || cs.contentVisibility === "auto";
  const x = paint || cs.overflowX !== "visible";
  const y = paint || cs.overflowY !== "visible";
  if (!x && !y) {
    return b;
  }
  const p = paddingBox(e, cs);
  return crop(b, {
    left: x ? p.left : -Infinity,
    right: x ? p.right : Infinity,
    top: y ? p.top : -Infinity,
    bottom: y ? p.bottom : Infinity,
  });
}

/** The element an absolute box inside `e` is positioned against: `e` or its nearest positioned
 *  ancestor. */
function containingBlock(e: Element): Element {
  for (let a: Element | null = e; a !== null; a = a.parentElement) {
    const cs = getComputedStyle(a);
    if (
      cs.position !== "static" ||
      cs.transform !== "none" ||
      /paint|layout|strict|content/.test(cs.contain)
    ) {
      return a;
    }
  }
  return document.documentElement;
}

/** Where a pseudo-element's box reaches: a stretched link's reaches its containing block. */
function pseudoBox(e: Element, which: "::before" | "::after"): Box | null {
  const cs = getComputedStyle(e, which);
  if (
    cs.content === "none" ||
    cs.content === "normal" ||
    cs.display === "none" ||
    cs.pointerEvents === "none" ||
    cs.position !== "absolute"
  ) {
    return null;
  }
  const cb = containingBlock(e);
  const p = paddingBox(cb, getComputedStyle(cb));
  return {
    left: p.left + px(cs.left),
    right: p.right - px(cs.right),
    top: p.top + px(cs.top),
    bottom: p.bottom - px(cs.bottom),
  };
}

/** The surface a press on `e` is decided in: a modal or popup over the page, or the page. */
function layerOf(e: Element): Element {
  for (let a: Element | null = e; a !== null; a = a.parentElement) {
    const cs = getComputedStyle(a);
    const raised = cs.position === "absolute" && (parseInt(cs.zIndex, 10) || 0) > 0;
    if (a.matches("dialog[open], [popover]") || cs.position === "fixed" || raised) {
      return a;
    }
  }
  return document.body;
}

function describeEl(e: Element): string {
  const id = e.id === "" ? "" : `#${e.id}`;
  const cls = [...e.classList]
    .slice(0, 3)
    .map((c) => `.${c}`)
    .join("");
  const name = (e.getAttribute("aria-label") ?? e.textContent ?? "").trim().slice(0, 28);
  const type = e instanceof HTMLInputElement ? `[${e.type}]` : "";
  return `${e.tagName.toLowerCase()}${type}${id}${cls}${name === "" ? "" : ` "${name}"`}`;
}

/** Whether a press inside `b` can land on `e` at all, rather than on a surface over it. */
function onTop(e: Element, b: Box): boolean {
  const w = b.right - b.left;
  const h = b.bottom - b.top;
  return [
    [0.5, 0.5],
    [0.25, 0.25],
    [0.75, 0.25],
    [0.25, 0.75],
    [0.75, 0.75],
  ].some(([fx = 0, fy = 0]) => {
    const hit = document.elementFromPoint(b.left + w * fx, b.top + h * fy);
    return (
      hit !== null && (e.contains(hit) || (e instanceof HTMLLabelElement && e.control === hit))
    );
  });
}

function walk(): Hit[] {
  const modal = document.querySelector("dialog:modal");
  const screen: Box = { left: 0, right: window.innerWidth, top: 0, bottom: window.innerHeight };
  const seen = new Map<string, number>();
  const hits: Hit[] = [];
  for (const el of document.querySelectorAll(INTERACTIVE)) {
    if (
      !el.checkVisibility({ visibilityProperty: true }) ||
      getComputedStyle(el).pointerEvents === "none" ||
      el.closest("[inert]") !== null ||
      (modal !== null && !modal.contains(el))
    ) {
      continue;
    }
    const box = boxOf(el as HTMLElement);
    if (!area(box)) {
      continue;
    }
    let target = box;
    for (const which of ["::before", "::after"] as const) {
      const pb = pseudoBox(el, which);
      if (pb !== null) {
        target = join(target, clipBy(el, pb));
      }
    }
    let visible = crop(box, screen);
    let scrolled = false;
    for (let a = el.parentElement; a !== null && area(target); a = a.parentElement) {
      const cut = clipBy(a, target);
      const scrolls = a.scrollHeight > a.clientHeight + 1 || a.scrollWidth > a.clientWidth + 1;
      scrolled ||= scrolls && (cut.top > target.top || cut.bottom < target.bottom);
      target = cut;
      visible = clipBy(a, visible);
    }
    target = crop(target, screen);
    if (!area(visible) || !onTop(el, visible)) {
      continue;
    }
    const base = describeEl(el);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    const whole =
      !scrolled &&
      Math.abs(visible.right - visible.left - (box.right - box.left)) < 0.5 &&
      Math.abs(visible.bottom - visible.top - (box.bottom - box.top)) < 0.5;
    hits.push({ el, label: `${base}[${String(n)}]`, layer: layerOf(el), box, target, whole });
  }
  return hits;
}

const fmt = (b: Box): string =>
  `${b.left.toFixed(1)}..${b.right.toFixed(1)} x ${b.top.toFixed(1)}..${b.bottom.toFixed(1)}`;

/** Every pair of targets in one layer that share more than device-pixel snapping explains. */
function overlaps(hits: readonly Hit[]): string[] {
  const out: string[] = [];
  for (const [i, a] of hits.entries()) {
    for (const b of hits.slice(i + 1)) {
      if (a.layer !== b.layer || a.el.contains(b.el) || b.el.contains(a.el)) {
        continue;
      }
      const o = crop(a.target, b.target);
      const w = o.right - o.left;
      const h = o.bottom - o.top;
      if (w > 1 && h > 1) {
        out.push(
          `${a.label} [${fmt(a.target)}] and ${b.label} [${fmt(b.target)}] share ${w.toFixed(1)}x${h.toFixed(1)}`,
        );
      }
    }
  }
  return out;
}

/** Why a walked element may have a target under 44px on an axis, or null where it may not. */
function shortAllowed(h: Hit, size: Size, axis: "inline" | "block"): string | null {
  const e = h.el;
  const t = h.target;
  const edge =
    axis === "inline"
      ? t.left <= 0.5 || t.right >= window.innerWidth - 0.5
      : t.top <= 0.5 || t.bottom >= window.innerHeight - 0.5;
  if (!h.whole || edge) {
    return "scrolled or at the screen edge";
  }
  if (e.matches('input[type="checkbox"], input[type="radio"]') && e.closest("label") !== null) {
    return "rides its label";
  }
  if (e.matches("textarea")) {
    return "a text field keeps its typing height";
  }
  if (e.matches(".effort-knob")) {
    return "rides its track";
  }
  return NARROW.some((n) => e.matches(n.sel) && n.sizes.includes(size) && n.axis === axis)
    ? "NARROW"
    : null;
}

interface WalkRecord {
  readonly surface: string;
  readonly size: Size;
  readonly label: string;
  readonly coarse: readonly [number, number];
  readonly target: readonly [number, number];
  /** The coarse box's and the target's top-left corners, for reading a failure. */
  readonly at: readonly number[];
}

const dims = (b: Box): readonly [number, number] => [
  Math.round((b.right - b.left) * 10) / 10,
  Math.round((b.bottom - b.top) * 10) / 10,
];

describe("every interactive element on the coarse tier", () => {
  // Every walked target and its size, written out so a layout change can be read as a diff.
  const records: WalkRecord[] = [];

  afterAll(async () => {
    await server.commands.writeFile(
      "./.vitest/touch-targets-walk.json",
      JSON.stringify(records, null, 1),
    );
  });

  describe.each(ALL)("at %ipx", (size) => {
    beforeAll(async () => {
      await page.viewport(...VIEWPORTS[size]);
    });

    it.each(SURFACES.map((s) => [s.name, s] as const))(
      "%s: no target overlaps another, and each clears 44px",
      async (_name, s) => {
        await mountSurface(s, size, "coarse");
        const hits = walk();
        for (const h of hits) {
          records.push({
            surface: s.name,
            size,
            label: h.label,
            coarse: dims(h.box),
            target: dims(h.target),
            at: [h.box.left, h.box.top, h.target.left, h.target.top].map(
              (v) => Math.round(v * 10) / 10,
            ),
          });
        }
        expect(hits.length, `${s.name} has interactive elements`).toBeGreaterThan(0);
        const found = overlaps(hits);
        for (const h of hits) {
          const w = h.target.right - h.target.left;
          const tall = h.target.bottom - h.target.top;
          if (w < FLOOR - 0.5 && shortAllowed(h, size, "inline") === null) {
            found.push(`${h.label} [${fmt(h.target)}]: ${w.toFixed(1)}px wide`);
          }
          if (tall < FLOOR - 0.5 && shortAllowed(h, size, "block") === null) {
            found.push(`${h.label} [${fmt(h.target)}]: ${tall.toFixed(1)}px tall`);
          }
        }
        expect(found, `\n${found.join("\n")}\n`).toEqual([]);
      },
    );
  });
});
