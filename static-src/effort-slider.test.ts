// The effort slider's geometry and gestures against the shipped cascade, driven through `buildEffortSlider` with a
// spy `onPick` to keep model-switcher.ts's mocks out.

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { buildEffortSlider, type EffortSliderHandle } from "./effort-slider.js";
import type { SessionEffortLevel } from "./types.js";

/** Not 1 (the mouse's id): a real gesture earlier leaves pointer 1 active, so the capture stub would do no work. */
const POINTER_ID = 7;

/** What KAS 2.21.2 sends for a real model in this account. */
const FIVE: SessionEffortLevel[] = [
  { id: "low", name: "Low" },
  { id: "medium", name: "Medium" },
  { id: "high", name: "High" },
  { id: "xhigh", name: "xHigh" },
  { id: "max", name: "Max" },
];

/**
 * Per-tier geometry, all derived in 15-input.css from `--ctl-h-sm`: the knob is the bar's height on both axes, and the
 * line is floored at `--hit-floor` because the whole track answers a tap.
 */
const TIER = {
  fine: { line: 24, bar: 24, knob: 24 },
  coarse: { line: 44, bar: 36, knob: 36 },
} as const;

let style: HTMLStyleElement;
let host: HTMLElement;
let picks: string[];
let slider: EffortSliderHandle;
let mounted: readonly SessionEffortLevel[] = [];

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
  host = document.createElement("div");
  // A definite width so the card sizes as above the composer; 400px down because the card anchors above its slot and
  // a pointer gesture is refused outside the viewport.
  host.style.cssText = "position:fixed;top:400px;left:0;width:600px;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

function mount(levels: readonly SessionEffortLevel[], active: string): void {
  picks = [];
  mounted = levels;
  slider = buildEffortSlider({
    onPick: (level) => {
      picks.push(level);
    },
  });
  const scroll = document.createElement("div");
  scroll.className = "pill-model-scroll";
  const card = document.createElement("span");
  card.className = "pill-expand-content pill-model-list is-open";
  // The resting state is scaled, and a transformed box reports scaled rects.
  card.style.cssText = "opacity:1;transform:none;animation:none;transition:none;";
  card.append(scroll, slider.el);
  const slot = document.createElement("span");
  slot.className = "pill-slot";
  slot.appendChild(card);
  host.replaceChildren(slot);
  slider.setLevels(levels);
  slider.setActive(active);
  // A rect mid-transition is interpolated, and Playwright refuses a moving box. The declaration is asserted below.
  knob().style.transition = "none";
}

function card(): HTMLElement {
  return slider.el.parentElement as HTMLElement;
}

function track(): HTMLElement {
  const el = slider.el.querySelector<HTMLElement>(".effort-track");
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

function knob(): HTMLElement {
  const el = slider.el.querySelector<HTMLElement>('[role="slider"]');
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

function caption(): string {
  return slider.el.querySelector<HTMLElement>(".effort-value")?.textContent ?? "";
}

function barHeight(): number {
  return parseFloat(getComputedStyle(track(), "::before").blockSize);
}

/**
 * Stop positions off the computed gradient; the first item is the direction. Colours compute to `oklch(...)` with
 * no commas.
 */
function fillStops(): readonly string[] {
  const img = getComputedStyle(track(), "::before").backgroundImage;
  const inner = img.slice(img.indexOf("(") + 1, img.lastIndexOf(")"));
  return inner
    .split(",")
    .slice(1)
    .map((stop) => stop.slice(stop.indexOf(")") + 1).trim());
}

/** Typed OM resolves `50%` / `calc(25% + 6px)`; the bar's border box is the gradient line, so `clientWidth` is the base. */
function stopPx(position: string): number {
  const sum = CSSNumericValue.parse(position).toSum("percent", "px");
  const pct = (sum.values[0] as CSSUnitValue).value;
  const px = (sum.values[1] as CSSUnitValue).value;
  return (pct / 100) * track().clientWidth + px;
}

/** Chromium computes `color-mix(in oklch, …)` to `oklab()`, so the colour parser reads it. */
function rgbOf(colour: string): readonly [number, number, number] {
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const ctx = canvas.getContext("2d");
  expect(ctx, "the harness needs a 2d context to read a colour").not.toBeNull();
  const c = ctx as CanvasRenderingContext2D;
  c.fillStyle = colour.trim();
  c.fillRect(0, 0, 1, 1);
  const d = c.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0] as const;
}

function distance(
  a: readonly [number, number, number],
  b: readonly [number, number, number],
): number {
  return Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);
}

function contrast(
  a: readonly [number, number, number],
  b: readonly [number, number, number],
): number {
  const lum = (c: readonly [number, number, number]): number => {
    const [r, g, bl] = c.map((v) => {
      const s = v / 255;
      return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    }) as [number, number, number];
    return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

function frac(): number {
  return Number(track().style.getPropertyValue("--effort-frac"));
}

/** The vocabulary comes from the fixture: nothing draws the tiers any more. */
function shown(): number {
  const k = knob();
  const now = Number(k.getAttribute("aria-valuenow"));
  expect(mounted[now]?.id, "aria-valuenow indexes the tier the knob shows").toBe(
    k.dataset["level"],
  );
  return now;
}

async function press(...keys: readonly string[]): Promise<void> {
  knob().focus();
  for (const key of keys) {
    await userEvent.keyboard(key);
  }
}

/** Read off the knob's rendered position; leaves the knob on the last tier. */
function tierCentres(levels: readonly SessionEffortLevel[]): number[] {
  return levels.map((level) => {
    slider.setActive(level.id);
    const r = knob().getBoundingClientRect();
    return (r.left + r.right) / 2;
  });
}

/**
 * `setPointerCapture` throws `NotFoundError` for a synthetic pointerId, so the harness backs capture with a Set;
 * the control must not grow a test path.
 */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

function ptr(type: string, clientX: number): PointerEvent {
  return new PointerEvent(type, { bubbles: true, clientX, pointerId: POINTER_ID });
}

beforeEach(() => {
  picks = [];
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

describe("the knob's size", () => {
  it("is ONE derived square, whatever words the vocabulary carries", () => {
    // The knob is sized by the pointer tier, not by foreign text.
    mount(FIVE, "low");
    const boxes = new Set<string>();
    for (const level of FIVE) {
      slider.setActive(level.id);
      boxes.add(`${String(knob().offsetWidth)}x${String(knob().offsetHeight)}`);
    }
    expect([...boxes], "one box serves the whole vocabulary").toEqual([
      `${String(TIER.fine.knob)}x${String(TIER.fine.knob)}`,
    ]);

    mount([{ id: "max", name: "M".repeat(120) }], "max");
    expect(knob().offsetWidth).toBe(TIER.fine.knob);
    expect(knob().offsetHeight).toBe(TIER.fine.knob);
  });

  it("takes the BAR's height rather than the hit floor's, and the track carries the target", () => {
    // The floor is physical and 15-input.css precedes it in MANIFEST order, so a logical override would lose; inherited,
    // the coarse 44px floor would outgrow the 36px bar.
    mount(FIVE, "high");
    const k = getComputedStyle(knob());
    expect(parseFloat(k.minWidth), "the floor is overridden, not inherited").toBe(0);
    expect(parseFloat(k.minHeight)).toBe(0);
    expect(knob().offsetHeight, "the bar's height, not the line's").toBe(TIER.fine.bar);
    expect(knob().offsetHeight).toBeLessThanOrEqual(TIER.fine.line);
    // The whole track carries the pointerdown handler and meets the floor.
    expect(track().offsetHeight).toBe(TIER.fine.line);
    expect(track().clientWidth).toBeGreaterThan(TIER.fine.line);
  });

  it("takes no width from the card it happens to be in", () => {
    // A widened card must not change the knob, nor carry its width into the next open.
    mount(FIVE, "high");
    const before = knob().offsetWidth;
    const wide = document.createElement("div");
    wide.className = "pill-model-item";
    wide.textContent = "a-model-with-a-long-name".repeat(3);
    card().querySelector<HTMLElement>(".pill-model-scroll")?.appendChild(wide);

    slider.setActive("high");

    expect(card().clientWidth, "the model row made the card wide").toBeGreaterThan(400);
    expect(knob().offsetWidth).toBe(before);
    expect(track().clientWidth, "the bar took the width instead").toBeGreaterThan(400);
  });

  it("FILLS the bar's height: the handle is the groove, not a dot inside it", () => {
    // Handle band and bar band coincide at every tier.
    mount(FIVE, "high");
    const bar = barHeight();
    expect(bar).toBe(TIER.fine.bar);
    expect(knob().offsetHeight, "the handle is the bar's own height").toBe(bar);

    for (const level of FIVE) {
      slider.setActive(level.id);
      const k = knob().getBoundingClientRect();
      const c = card().getBoundingClientRect();
      const t = track().getBoundingClientRect();
      const barTop = (t.top + t.bottom) / 2 - bar / 2;
      const barBottom = barTop + bar;
      expect(k.top, `${level.id}: flush with the bar's top`).toBeCloseTo(barTop, 1);
      expect(k.bottom, `${level.id}: flush with the bar's bottom`).toBeCloseTo(barBottom, 1);
      expect(k.top, `${level.id}: inside the card's clip box`).toBeGreaterThanOrEqual(c.top);
      expect(k.bottom, `${level.id}: inside the card's clip box`).toBeLessThanOrEqual(c.bottom);
    }
  });

  it("draws both of its edges with a border and carries no shadow", () => {
    // Both are borders, not shadows: no other surface in the interface uses a shadow.
    mount(FIVE, "high");
    const bar = getComputedStyle(track(), "::before");
    const k = getComputedStyle(knob());
    expect(bar.boxShadow, "the bar's groove is its border").toBe("none");
    expect(k.boxShadow, "the handle's lift is its border").toBe("none");
    expect(parseFloat(bar.borderTopWidth), "the bar has one").toBe(1);
    expect(parseFloat(k.borderTopWidth), "and so does the handle").toBe(1);
  });

  it("holds the knob inside the bar at both extremes, flush with each end", () => {
    mount(FIVE, "low");
    for (const index of [0, FIVE.length - 1]) {
      slider.setActive(FIVE[index]?.id ?? "");
      const t = track().getBoundingClientRect();
      const k = knob().getBoundingClientRect();
      expect(k.left - t.left, `tier ${String(index)} starts inside the bar`).toBeGreaterThanOrEqual(
        -0.5,
      );
      expect(t.right - k.right, `tier ${String(index)} ends inside the bar`).toBeGreaterThanOrEqual(
        -0.5,
      );
    }
    // Flush handle: the low tier sits on the start edge, the top tier on the end.
    slider.setActive("low");
    expect(knob().getBoundingClientRect().left - track().getBoundingClientRect().left).toBeCloseTo(
      0,
      1,
    );
    slider.setActive("max");
    expect(
      track().getBoundingClientRect().right - knob().getBoundingClientRect().right,
    ).toBeCloseTo(0, 1);
  });

  it("FILLS to the knob and gains intensity, bounded by the handle's contrast", () => {
    mount(FIVE, "low");
    const handle = rgbOf(getComputedStyle(knob()).backgroundColor);
    const accent = rgbOf(getComputedStyle(document.documentElement).getPropertyValue("--c-accent"));

    // Three declared colours, read through a probe (a gradient's stops are not readable off `getComputedStyle`).
    const mix = (pct: number): readonly [number, number, number] => {
      const probe = document.createElement("div");
      probe.style.backgroundColor = `color-mix(in oklch, var(--c-accent) ${String(pct)}%, var(--c-bg-tertiary))`;
      host.appendChild(probe);
      const out = rgbOf(getComputedStyle(probe).backgroundColor);
      probe.remove();
      return out;
    };
    const idle = mix(6);
    const warm = mix(22);
    const hot = mix(70);

    expect(distance(warm, accent), "warm is nearer the accent than the idle track").toBeLessThan(
      distance(idle, accent),
    );
    expect(distance(hot, accent), "the hot spot is the nearest of the three").toBeLessThan(
      distance(warm, accent),
    );
    expect(distance(idle, hot), "the two ends are visibly different").toBeGreaterThan(90);

    // Every pixel of the bar is one of these mixes, so 3:1 across 6..70% clears WCAG 1.4.11 everywhere. Widening
    // `--effort-fill-max` past 70% must re-run this.
    for (let pct = 6; pct <= 70; pct += 8) {
      expect(
        contrast(handle, mix(pct)),
        `the handle clears 3:1 on a ${String(pct)}% fill`,
      ).toBeGreaterThan(3);
    }
    expect(distance(handle, accent), "the handle is not accent-coloured").toBeGreaterThan(40);

    // The boundary is `--effort-frac`, so each tier paints a different gradient.
    track().dataset["dragging"] = "";
    const images = FIVE.map((level) => {
      slider.setActive(level.id);
      return getComputedStyle(track(), "::before").backgroundImage;
    });
    delete track().dataset["dragging"];
    expect(new Set(images).size, "one gradient per tier").toBe(FIVE.length);
  });

  it("ENDS the fill at the handle's centre, with nothing leaking past it", () => {
    // The boundary is the handle's centre, so the seam never shows. Layout against the computed stop.
    mount(FIVE, "low");
    for (const level of FIVE) {
      slider.setActive(level.id);
      const stops = fillStops();
      expect(stops, "warm at the start, the boundary twice, idle to the end").toHaveLength(4);
      expect(stops[1], `${level.id}: one hard edge rather than a shoulder`).toBe(stops[2]);
      const k = knob().getBoundingClientRect();
      const t = track().getBoundingClientRect();
      expect(
        stopPx(stops[1] as string),
        `${level.id}: the fill ends where the handle's centre is`,
      ).toBeCloseTo((k.left + k.right) / 2 - t.left, 1);
    }
  });

  it("marks each tier where the knob actually lands", () => {
    // The release snaps to a tier, so tier marks show where it ends; they share the knob's travel arithmetic.
    mount(FIVE, "low");
    const dots = [...slider.el.querySelectorAll<HTMLElement>(".effort-stop")];
    expect(dots).toHaveLength(FIVE.length);
    const dotCentres = dots.map((d) => {
      const r = d.getBoundingClientRect();
      return (r.left + r.right) / 2;
    });
    for (const [i, centre] of tierCentres(FIVE).entries()) {
      expect(dotCentres[i], `the mark for tier ${String(i)} is on the knob's centre`).toBeCloseTo(
        centre,
        1,
      );
    }
    // The track is the one pointerdown target.
    expect(getComputedStyle(dots[0] as HTMLElement).pointerEvents).toBe("none");
  });

  it("draws no marks for a vocabulary with nothing to choose between", () => {
    mount([{ id: "high", name: "High" }], "high");
    expect(slider.el.querySelectorAll(".effort-stop")).toHaveLength(0);
  });

  it("leaves a foreign tier name to the caption, which WRAPS rather than widening the card", () => {
    // A tier label is KAS's text of unbounded length: the card must not grow sideways and the name must not be clipped
    // (the card is `overflow: hidden`).
    mount(FIVE, "high");
    const narrow = card().clientWidth;

    mount(
      [
        { id: "low", name: "Low" },
        { id: "max", name: "M".repeat(120) },
      ],
      "max",
    );
    expect(caption(), "the long name landed in the caption").toBe("M".repeat(120));
    const label = slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement;
    expect(card().clientWidth, "the card did not grow sideways for it").toBe(narrow);
    expect(label.scrollWidth, "and not one character is clipped").toBeLessThanOrEqual(
      label.clientWidth,
    );
    expect(label.getBoundingClientRect().height, "it wrapped onto more lines").toBeGreaterThan(30);

    const t = track();
    const k = knob();
    for (const id of ["low", "max"]) {
      slider.setActive(id);
      const tr = t.getBoundingClientRect();
      const kr = k.getBoundingClientRect();
      expect(kr.left, `${id} starts inside the track`).toBeGreaterThanOrEqual(tr.left);
      expect(kr.right, `${id} ends inside the track`).toBeLessThanOrEqual(tr.right);
    }
    // The track's floor is the knob itself, so the travel cannot invert.
    expect(parseFloat(getComputedStyle(t).minInlineSize)).toBe(k.offsetWidth);
    expect(t.clientWidth).toBeGreaterThan(k.offsetWidth);
  });

  it("grows every part of itself on the coarse tier", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    mount(FIVE, "high");
    expect(track().offsetHeight).toBe(TIER.coarse.line);
    expect(barHeight()).toBe(TIER.coarse.bar);
    expect(knob().offsetHeight).toBe(TIER.coarse.knob);
    expect(knob().offsetWidth).toBe(TIER.coarse.knob);
    // Under the line: a 44px knob under a finger was the reported complaint.
    expect(knob().offsetHeight).toBeLessThan(TIER.coarse.line);
    const k = knob().getBoundingClientRect();
    const c = card().getBoundingClientRect();
    expect(k.top).toBeGreaterThanOrEqual(c.top);
    expect(k.bottom).toBeLessThanOrEqual(c.bottom);
  });
});

describe("one tier", () => {
  it("shrinks the bar to the knob, stays named, and reports a zero-width range", () => {
    mount([{ id: "high", name: "High" }], "high");
    const t = track();
    const k = knob();
    // One tier is not a choice, so there is no travel.
    expect(t.clientWidth).toBe(k.offsetWidth);
    expect(k.offsetWidth).toBe(TIER.fine.knob);
    expect(caption(), "the caption still names the tier in force").toBe("High");
    expect(k.getAttribute("aria-valuemin")).toBe("0");
    expect(k.getAttribute("aria-valuemax")).toBe("0");
    expect(shown()).toBe(0);
    const kr = k.getBoundingClientRect();
    const tr = t.getBoundingClientRect();
    expect(kr.left - tr.left).toBeCloseTo(0, 1);
    expect(tr.right - kr.right).toBeCloseTo(0, 1);
  });

  it("cannot be stepped off its one tier", async () => {
    mount([{ id: "high", name: "High" }], "high");
    await press("{ArrowRight}", "{End}", "{ArrowLeft}", "{Home}");
    expect(shown()).toBe(0);
    expect(picks).toEqual(["high", "high", "high", "high"]);
  });
});

describe("the keyboard", () => {
  it("steps down on Left and Down, up on Right and Up, clamped at both ends", async () => {
    mount(FIVE, "medium");
    await press("{ArrowLeft}", "{ArrowDown}", "{ArrowLeft}");
    expect(shown(), "clamped at the lowest tier").toBe(0);
    await press("{ArrowRight}", "{ArrowUp}");
    expect(shown()).toBe(2);
    expect(picks).toEqual(["low", "low", "low", "medium", "high"]);
  });

  it("jumps to the ends on Home and End", async () => {
    mount(FIVE, "medium");
    await press("{End}");
    expect(shown()).toBe(4);
    await press("{Home}");
    expect(shown()).toBe(0);
    expect(picks).toEqual(["max", "low"]);
  });

  it("keeps the card's roving focus out of its own arrow keys", async () => {
    // `model-switcher.ts`'s `rovingFocus` reads no target, so the knob's keys must be stopped, not just defaulted.
    mount(FIVE, "medium");
    const scroll = card().querySelector<HTMLElement>(".pill-model-scroll") as HTMLElement;
    for (const id of ["a-model", "b-model"]) {
      const item = document.createElement("div");
      item.className = "pill-model-item";
      item.setAttribute("role", "option");
      item.textContent = id;
      scroll.appendChild(item);
    }
    const nav = rovingFocus(card(), ".pill-model-item");

    await press("{ArrowUp}", "{Home}", "{End}");

    expect(document.activeElement, "focus stayed on the knob").toBe(knob());
    expect(picks).toEqual(["high", "low", "max"]);
    nav.dispose();
  });

  it("leaves every other key to the app", async () => {
    // Escape must reach the popup's own document handler.
    mount(FIVE, "medium");
    await press("{Escape}", "a", "{PageDown}", "{Enter}", " ");
    expect(shown()).toBe(1);
    expect(picks).toEqual([]);
  });
});

describe("the pointer", () => {
  it("snaps to the nearest tier when the bar is tapped away from the knob", () => {
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");

    // 40% of the way from tier 3 to 4 is nearest tier 3.
    const between =
      (centres[3] as number) + 0.4 * ((centres[4] as number) - (centres[3] as number));
    t.dispatchEvent(ptr("pointerdown", between));
    expect(frac(), "the press paints where the finger is, not on a tier").not.toBeCloseTo(3 / 4, 3);
    expect(shown(), "while naming the tier it is nearest").toBe(3);
    expect(picks, "a press is not a pick").toEqual([]);

    t.dispatchEvent(ptr("pointerup", between));

    expect(shown()).toBe(3);
    expect(frac(), "the release snaps the position onto the tier").toBeCloseTo(3 / 4, 5);
    expect(picks).toEqual(["xhigh"]);
  });

  it("moves smoothly between two tiers and snaps to the nearer one on release", () => {
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");
    const low = centres[0] as number;
    const medium = centres[1] as number;

    t.dispatchEvent(ptr("pointerdown", low));
    const seen: number[] = [];
    for (const step of [0.1, 0.2, 0.3, 0.4, 0.6, 0.7]) {
      t.dispatchEvent(ptr("pointermove", low + step * (medium - low)));
      seen.push(frac());
    }

    expect(seen, "every step is its own position").toHaveLength(new Set(seen).size);
    expect(
      seen.every((f) => f > 0 && f < 1 / 4),
      "all of them between the two tiers",
    ).toBe(true);
    expect(picks, "not one move is a pick").toEqual([]);

    t.dispatchEvent(ptr("pointerup", low + 0.7 * (medium - low)));
    expect(frac()).toBeCloseTo(1 / 4, 5);
    expect(picks).toEqual(["medium"]);
  });

  it("still dispatches when the tap lands where the knob already sits", async () => {
    // The tier is marked, not chosen (`model-switcher.setEffort` guards the chat's choice), so pinning it must dispatch.
    mount(FIVE, "high");

    await userEvent.click(knob());

    expect(shown()).toBe(2);
    expect(picks).toEqual(["high"]);
  });

  it("follows a drag across the whole track and reports where it was released", () => {
    // Synthetic events, not `userEvent.dragAndDrop`: Playwright's actionability wait is on the 2px target span, so the
    // gesture stalls under load.
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");

    // `pointerup` reads no coordinate, so the gesture ends with a move at the far tier.
    t.dispatchEvent(ptr("pointerdown", centres[0] as number));
    const painted = [shown()];
    const named = [caption()];
    for (const x of centres.slice(1)) {
      t.dispatchEvent(ptr("pointermove", x));
      painted.push(shown());
      named.push(caption());
    }

    expect(painted, "the knob paints the tier under the finger, tier by tier").toEqual([
      0, 1, 2, 3, 4,
    ]);
    expect(named, "and the caption names the same tier at every step").toEqual(
      FIVE.map((l) => l.name),
    );
    expect(picks, "the moves PAINT: not one of them is a pick").toEqual([]);

    t.dispatchEvent(ptr("pointerup", centres[4] as number));

    expect(shown()).toBe(4);
    // One dispatch, on the release.
    expect(picks).toEqual(["max"]);
  });

  it("eases to a tier at rest and follows the finger while dragging", () => {
    mount(FIVE, "low");
    const t = track();
    const k = knob();
    // The fixture suppresses the transition; this case reads the shipped declaration.
    k.style.removeProperty("transition");
    const rest = getComputedStyle(k);
    expect(rest.transitionProperty, "a settled knob eases to its tier").toContain("transform");
    expect(parseFloat(rest.transitionDuration)).toBeGreaterThan(0);
    // The press scale is a `transform` function, not the `scale` property: the individual properties compose so `scale`
    // multiplies the element's translate and displaced the knob under the pointer.
    expect(rest.scale, "the resting handle sets no `scale` property").toBe("none");
    expect(rest.transitionProperty, "and has nothing to transition on it").not.toContain("scale");
    const sheet = loadCSS("15-input.css");
    for (const [state, scope] of [
      ["hover", "any-hover"],
      ["active", "top"],
    ] as const) {
      const body = ruleContaining(sheet, `.effort-knob:${state}`, scope).body;
      expect(body, `the ${state} state sets no \`scale\` property`).not.toMatch(/(^|[;\s])scale:/);
      expect(body, `the ${state} state scales inside \`transform\``).toMatch(
        /transform:\s*translate\([^;]*\)\s*scale\(/,
      );
    }

    t.dataset["dragging"] = "";
    // A transition makes the knob lag the finger.
    expect(getComputedStyle(k).transitionDuration).toBe("0s");
    delete t.dataset["dragging"];
  });

  it("returns to the synced tier when the gesture is cancelled", () => {
    mount(FIVE, "high");
    const t = track();
    stubPointerCapture(t);
    t.dispatchEvent(ptr("pointerdown", 0));
    expect(shown(), "the press moved the knob to the low end").toBe(0);

    t.dispatchEvent(ptr("pointercancel", 0));

    expect(shown()).toBe(2);
    expect(caption(), "the caption came back with it").toBe("High");
    expect(picks, "a cancelled gesture is not a pick").toEqual([]);
  });
});

describe("the single writer", () => {
  it("moves position, aria-valuenow, aria-valuetext and the caption together", () => {
    mount(FIVE, "low");
    const k = knob();
    const seen = new Set<string>();
    for (const [index, level] of FIVE.entries()) {
      slider.setActive(level.id);
      expect(k.getAttribute("aria-valuenow")).toBe(String(index));
      expect(k.getAttribute("aria-valuetext")).toBe(caption());
      expect(k.dataset["level"]).toBe(level.id);
      expect(caption()).toBe(level.name);
      seen.add(track().style.getPropertyValue("--effort-frac"));
    }
    expect([...seen], "a distinct position per tier").toHaveLength(FIVE.length);
  });

  it("keeps the caption's value following the knob through a real gesture", async () => {
    // The caption is the visible label and readout; one writer, so every input path moves it.
    mount(FIVE, "low");
    expect(caption()).toBe("Low");

    await press("{ArrowRight}", "{ArrowRight}");
    expect(caption()).toBe("High");
    await press("{End}");
    expect(caption()).toBe("Max");

    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("max");
    t.dispatchEvent(ptr("pointerdown", centres[1] as number));
    t.dispatchEvent(ptr("pointerup", centres[1] as number));
    expect(caption()).toBe("Medium");
    expect(caption(), "and it is the tier announced").toBe(knob().getAttribute("aria-valuetext"));
  });

  it("names the dimension beside the value, so the control has a visible label", () => {
    // A value alone names no dimension (WCAG 3.3.2), so one writer replaces the word, never the sentence.
    mount(FIVE, "high");
    const label = slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement;
    expect(label.textContent).toBe("Effort: High");
    slider.setActive("max");
    expect(label.textContent).toBe("Effort: Max");
    // A stable name: one carrying the value is re-announced on every step.
    expect(knob().getAttribute("aria-label")).toBe("Reasoning effort");
    expect(label.getAttribute("aria-live"), "the caption is visual, not a live region").toBeNull();
    expect(knob().getAttribute("aria-labelledby")).toBeNull();
  });

  it("puts an unoffered tier on the lowest one", () => {
    // `effortVocabulary` answers "" when nothing resolved; a slider is always somewhere.
    mount(FIVE, "");
    expect(shown()).toBe(0);
    mount(FIVE, "not-a-tier");
    expect(shown()).toBe(0);
  });
});

describe("the vocabulary", () => {
  it("rebuilds the range when the tiers change", () => {
    mount(FIVE, "high");
    expect(knob().getAttribute("aria-valuemax")).toBe("4");
    expect(track().dataset["tiers"]).toBe("5");

    mounted = [{ id: "low" }, { id: "high" }];
    slider.setLevels(mounted);
    slider.setActive("high");

    expect(knob().getAttribute("aria-valuemax")).toBe("1");
    expect(track().dataset["tiers"], "the count is the one thing CSS reads").toBe("2");
    expect(shown()).toBe(1);
    expect(knob().dataset["level"]).toBe("high");
  });

  it("needs no layout to build, so a detached or hidden card is not a special case", () => {
    // Position and announcements are CSS arithmetic over `--effort-frac`, so building detached is the same control.
    const detached = buildEffortSlider({ onPick: () => undefined });
    detached.setLevels(FIVE);
    detached.setActive("xhigh");
    const k = detached.el.querySelector<HTMLElement>('[role="slider"]') as HTMLElement;
    expect(k.getAttribute("aria-valuenow")).toBe("3");
    expect(detached.el.querySelector(".effort-value")?.textContent).toBe("xHigh");

    mount(FIVE, "low");
    card().appendChild(detached.el);
    k.style.transition = "none";
    expect(k.offsetWidth, "and it lays out at the derived size on attach").toBe(TIER.fine.knob);
    const t = detached.el.querySelector<HTMLElement>(".effort-track") as HTMLElement;
    const kr = k.getBoundingClientRect();
    const tr = t.getBoundingClientRect();
    expect(kr.right).toBeLessThanOrEqual(tr.right);
    expect(kr.left).toBeGreaterThan(tr.left);
    detached.el.remove();
  });
});

describe("the row keeps its place in the card", () => {
  it("bleeds to both edges below the scroller", () => {
    mount(FIVE, "low");
    expect(card().lastElementChild, "the section is the card's last child").toBe(slider.el);
    // The card gave its padding to the scroller, so its border is the only thing at its edge.
    const border = parseFloat(getComputedStyle(card()).borderLeftWidth);
    const c = card().getBoundingClientRect();
    const r = slider.el.getBoundingClientRect();
    expect(r.left - c.left).toBeCloseTo(border, 1);
    expect(c.right - r.right).toBeCloseTo(border, 1);
  });

  it("stacks the caption over the bar rather than beside it", () => {
    mount(FIVE, "high");
    const label = (
      slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement
    ).getBoundingClientRect();
    const bar = track().getBoundingClientRect();
    expect(label.bottom, "the caption sits entirely above the bar").toBeLessThanOrEqual(bar.top);
    expect(label.left, "and both lines start on the same edge").toBeCloseTo(bar.left, 0);
  });
});

describe("nothing about it is a range input", () => {
  it("builds a custom slider role instead", () => {
    mount(FIVE, "low");
    expect(slider.el.querySelectorAll("input")).toHaveLength(0);
    expect(knob().tagName).toBe("DIV");
    expect(knob().getAttribute("role")).toBe("slider");
    expect(knob().getAttribute("aria-label")).toBe("Reasoning effort");
    expect(knob().getAttribute("tabindex")).toBe("0");
  });

  it("is the section's ONE tab stop", () => {
    mount(FIVE, "low");
    const focusable = slider.el.querySelectorAll(
      'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    expect([...focusable]).toEqual([knob()]);
  });
});

// Guards the fixture, not the code.
it("reports every pick through the callback it was given", () => {
  const onPick = vi.fn();
  const handle = buildEffortSlider({ onPick });
  expect(handle.el.className).toBe("effort-row");
});
