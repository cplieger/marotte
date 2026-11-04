import { afterEach, describe, expect, it, vi } from "vitest";

import { clearDeviceScale, setDeviceScale } from "./__test-helpers__/device-scale.js";
import { snapIcons } from "./icon-crisp.js";

const SVG_NS = "http://www.w3.org/2000/svg";

interface IconOpts {
  readonly cls?: string;
  readonly size?: number;
  readonly strokeWidth?: number;
  readonly fillOnly?: boolean;
}

/**
 * An `ic-ui` icon by default (24-unit grid at 16px, stroke 1, so phase 0.5). Sized inline because `03-base.css` is not
 * loaded; root `fill`/`stroke` as `icons.ts`'s `svg()` writes them.
 */
function icon(opts: IconOpts = {}): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", opts.cls ?? "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  const px = `${String(opts.size ?? 16)}px`;
  svg.style.inlineSize = px;
  svg.style.blockSize = px;
  svg.style.display = "block";
  if (opts.strokeWidth !== undefined) {
    svg.style.strokeWidth = `${String(opts.strokeWidth)}px`;
  }
  const p = document.createElementNS(SVG_NS, "path");
  p.setAttribute("d", "M6 9l6 6 6-6");
  if (opts.fillOnly === true) {
    p.setAttribute("fill", "currentColor");
    p.setAttribute("stroke", "none");
  }
  svg.append(p);
  return svg;
}

function mount(
  rotateDeg: number | null,
  opts: IconOpts = {},
): { host: HTMLElement; svg: SVGSVGElement } {
  const host = document.createElement("div");
  host.style.position = "absolute";
  // Fractional and different per axis, so a correction on the wrong axis cannot pass.
  host.style.left = "10.3px";
  host.style.top = "20.7px";
  const svg = icon(opts);
  if (rotateDeg === null) {
    host.append(svg);
  } else {
    const wrap = document.createElement("span");
    wrap.className = "disclosure-chevron";
    wrap.style.display = "grid";
    wrap.style.placeItems = "center";
    wrap.style.transform = `rotate(${String(rotateDeg)}deg)`;
    wrap.append(svg);
    host.append(wrap);
  }
  document.body.append(host);
  return { host, svg };
}

const frac = (v: number): number => ((v % 1) + 1) % 1;

/** Distance on the shorter way round; 0 is crisp. */
function phaseOff(phase: number, target: number): number {
  const d = Math.abs(phase - target);
  return d > 0.5 ? 1 - d : d;
}

/** Worst axis of the box's screen phase against `target`, in device pixels at `dpr`. */
function boxOff(svg: SVGSVGElement, target: number, dpr = 1): number {
  const r = svg.getBoundingClientRect();
  return Math.max(phaseOff(frac(dpr * r.x), target), phaseOff(frac(dpr * r.y), target));
}

/** The existing tier's read: an `ic-ui` box at DPR 1 wants phase 0.5. */
function offTarget(svg: SVGSVGElement): number {
  return boxOff(svg, 0.5);
}

/**
 * Read through the screen CTM, so a glyph centred in a wider slot is measured where it paints: the coordinate 3 units
 * in, in device pixels at `dpr`.
 */
function drawingOff(svg: SVGSVGElement, target: number, dpr = 1): number {
  const m = svg.getScreenCTM();
  expect(m).not.toBeNull();
  if (m === null) {
    return 1;
  }
  return Math.max(
    phaseOff(frac(dpr * (m.e + 3 * m.a)), target),
    phaseOff(frac(dpr * (m.f + 3 * m.d)), target),
  );
}

const hosts: HTMLElement[] = [];
function place(rotateDeg: number | null, opts: IconOpts = {}): SVGSVGElement {
  const { host, svg } = mount(rotateDeg, opts);
  hosts.push(host);
  return svg;
}

afterEach(() => {
  vi.unstubAllGlobals();
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

describe("snapIcons", () => {
  it("puts an unrotated icon box on the phase its own scale implies", () => {
    const svg = place(null);
    expect(offTarget(svg)).toBeGreaterThan(0.1);
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("snaps an icon a rotated ancestor holds, measured in SCREEN space", () => {
    // `translate` composes inside the wrapper's rotation, so a screen-space correction written onto the box lands on the
    // other axis; only a screen-space assertion sees it.
    const svg = place(-90);
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("snaps at every right angle, not just the two the app happens to use today", () => {
    for (const deg of [90, 180, 270, -180]) {
      const svg = place(deg);
      snapIcons();
      expect(offTarget(svg), `rotate(${String(deg)}deg)`).toBeLessThan(0.02);
    }
  });

  it("declines an icon no translate could make crisp", () => {
    // At 30deg no phase helps, and the screen AABB is not the box.
    const svg = place(30);
    snapIcons();
    expect(svg.style.translate).toBe("");
  });

  it("declines an icon under an ancestor scale, then snaps it once the scale lifts", () => {
    // A translate written under a scale is scaled with it and misses, so passes chase their own offset. Layout size
    // disagreeing with painted size is the signal.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.transform = "scale(0.4)";
    }
    expect(snapIcons(), "nothing is written under the scale").toBe(0);
    expect(svg.style.translate).toBe("");

    // The declined reading is taken as soon as the scale lifts.
    if (host instanceof HTMLElement) {
      host.style.transform = "";
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("converges rather than chasing its own offset when a snapped icon moves", () => {
    // The offset in force is a screen delta and the value written a local one; reading back subtracts the screen one, or
    // passes compound.
    const svg = place(-90);
    snapIcons();
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.left = "44.85px";
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
    // A second pass over a converged icon moves nothing.
    expect(snapIcons()).toBe(0);
  });

  it("holds a box still across passes when both directions are equally crisp", () => {
    // At DPR 3 a 20px icon on an integer origin puts its 2px stroke exactly half a device pixel off: +0.5 and -0.5 are
    // both crisp, and the rect read back is float32, so a pass that re-picks the side chases rounding noise.
    vi.stubGlobal("devicePixelRatio", 3);
    const cases = [
      { size: 20, strokeWidth: 2 },
      { size: 16, strokeWidth: 1 },
    ];
    for (const c of cases) {
      for (let k = 0; k < 12; k++) {
        const svg = place(null, c);
        const host = svg.closest("div");
        expect(host).not.toBeNull();
        if (host instanceof HTMLElement) {
          host.style.left = `${String(37 + k / 12)}px`;
          host.style.top = "174px";
        }
        snapIcons();
        const first = svg.getBoundingClientRect();
        const label = `${String(c.size)}px stroke ${String(c.strokeWidth)} at +${String(k)}/12`;
        for (let pass = 0; pass < 3; pass++) {
          expect(snapIcons(), `${label}, pass ${String(pass + 2)}`).toBe(0);
        }
        const last = svg.getBoundingClientRect();
        expect([last.x, last.y], label).toEqual([first.x, first.y]);
        // Crisp is the stroke centre on frac(deviceWidth / 2).
        expect(drawingOff(svg, frac(c.strokeWidth * 1.5), 3), label).toBeLessThan(0.02);
      }
    }
  });

  it("declines an icon an ancestor is sliding on, then snaps it once the slide lands", () => {
    // The phone drawer opens on a `transform: translateX()` transition: no scale, so layout and painted size agree, and
    // a mid-slide pass measures a moving origin the settle pass then has to undo.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (!(host instanceof HTMLElement)) {
      return;
    }
    host.style.transform = "translateX(-100.4px)";
    host.getBoundingClientRect();
    host.style.transition = "transform 10s linear";
    host.style.transform = "translateX(0)";
    expect(host.getAnimations()).toHaveLength(1);
    expect(snapIcons(), "nothing is written mid-slide").toBe(0);
    expect(svg.style.translate).toBe("");

    for (const a of host.getAnimations()) {
      a.finish();
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("declines an icon inside an animation that moves it for ever", () => {
    // A dragged tab floats on an infinite transform loop; no reading inside it holds.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    host?.animate([{ transform: "translateY(0)" }, { transform: "translateY(-2.3px)" }], {
      duration: 2200,
      iterations: Infinity,
    });
    expect(snapIcons()).toBe(0);
    expect(svg.style.translate).toBe("");
  });

  it("still snaps an icon whose surroundings only repaint or loop for ever", () => {
    // A running run's status dot beats on opacity for ever beside the sub-tab arrow, a hovered row fades its
    // background, and a spinner ring loops a custom property for ever.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    host?.animate([{ opacity: 1 }, { opacity: 0.4 }], { duration: 1200, iterations: Infinity });
    host?.animate([{ "--arc": "-45deg" }, { "--arc": "315deg" }], {
      duration: 900,
      iterations: Infinity,
    });
    host?.animate([{ backgroundColor: "transparent" }, { backgroundColor: "rgb(40, 40, 40)" }], {
      duration: 10_000,
    });
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("holds every icon while a row above it collapses, then snaps once the row is gone", () => {
    // A closing tab collapses its max-height and padding (`slide-out`), which slides every row below it through
    // layout; none of their icons sits inside the animated row.
    const list = document.createElement("div");
    list.style.position = "absolute";
    list.style.left = "10.3px";
    list.style.top = "20.7px";
    const exiting = document.createElement("div");
    exiting.style.blockSize = "48px";
    exiting.style.overflow = "hidden";
    const svg = icon();
    list.append(exiting, svg);
    document.body.append(list);
    hosts.push(list);

    const collapse = exiting.animate(
      [
        { maxHeight: "48px", paddingBlock: "8px" },
        { maxHeight: "0px", paddingBlock: "0px" },
      ],
      { duration: 10_000, fill: "forwards" },
    );
    collapse.currentTime = 3700;
    expect(snapIcons(), "nothing is written mid-collapse").toBe(0);
    expect(svg.style.translate).toBe("");

    collapse.finish();
    exiting.remove();
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("returns to the smallest correction when the device pixel ratio changes", () => {
    // 20px stroke 2 on an integer origin: at DPR 1 the snap is half a CSS px, a whole device px at DPR 2, where the
    // unmoved box is already crisp.
    const svg = place(null, { size: 20, strokeWidth: 2 });
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.left = "37px";
      host.style.top = "174px";
    }
    const origin = svg.getBoundingClientRect();
    vi.stubGlobal("devicePixelRatio", 1);
    snapIcons();
    expect(svg.getBoundingClientRect().x, "the DPR 1 snap moves the box").not.toBe(origin.x);

    vi.stubGlobal("devicePixelRatio", 2);
    snapIcons();
    const after = svg.getBoundingClientRect();
    expect([after.x, after.y]).toEqual([origin.x, origin.y]);
    expect(drawingOff(svg, 0, 2)).toBeLessThan(0.02);
  });

  it("puts a stroke-2 tier on phase 0, where an even device stroke is crisp", () => {
    // `.ic-lg` strokes 2px, so at DPR 1 its centre wants a pixel boundary, not 0.5.
    const svg = place(null, { cls: "ic-lg", size: 24, strokeWidth: 2 });
    expect(boxOff(svg, 0)).toBeGreaterThan(0.1);
    snapIcons();
    expect(boxOff(svg, 0)).toBeLessThan(0.02);
  });

  it("puts a fill-only glyph on phase 0, where its edge sits on a pixel boundary", () => {
    const svg = place(null, { fillOnly: true });
    expect(boxOff(svg, 0)).toBeGreaterThan(0.1);
    snapIcons();
    expect(boxOff(svg, 0)).toBeLessThan(0.02);
  });

  it("targets the DEVICE phase at DPR 2, where a 1px stroke is two device pixels wide", () => {
    vi.stubGlobal("devicePixelRatio", 2);
    const svg = place(null);
    // Device phase 0 is CSS phase 0 or 0.5, so the move (at most a quarter CSS px at DPR 2) is what tells a DPR-aware
    // snap from a DPR 1 one.
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.left = "10.15px";
      host.style.top = "20.85px";
    }
    const before = svg.getBoundingClientRect();
    expect(boxOff(svg, 0, 2)).toBeGreaterThan(0.1);
    snapIcons();
    const after = svg.getBoundingClientRect();
    expect(boxOff(svg, 0, 2)).toBeLessThan(0.02);
    expect(Math.abs(after.x - before.x)).toBeLessThan(0.26);
    expect(Math.abs(after.y - before.y)).toBeLessThan(0.26);
  });

  it("scales by the rendered drawing, not the box, when a flex slot is wider than the glyph", () => {
    // The rail form's `[id="scroll-bottom"] > svg` is a 42px flex slot centring a 16px drawing.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.display = "flex";
    }
    svg.style.flex = "0 0 42px";
    expect(svg.getBoundingClientRect().width).toBe(42);
    snapIcons();
    expect(drawingOff(svg, 0.5)).toBeLessThan(0.02);
  });
});

// A snap taken off the screen goes stale whenever a scroll changes the device-pixel phase, and the next pass then moves
// every icon in the scroller while its text stands still.
describe("snapping inside a scroller", () => {
  afterEach(async () => {
    await clearDeviceScale();
  });

  /** A 100px scroller over 1000px of content, its box at a fractional offset, holding `icons` placed by `style`. */
  function scroller(styles: readonly string[]): { box: HTMLElement; svgs: SVGSVGElement[] } {
    const frame = document.createElement("div");
    frame.style.cssText = "position:absolute;left:10.3px;top:20.7px;";
    const box = document.createElement("div");
    box.style.cssText = "overflow-y:auto;block-size:100px;inline-size:200px;";
    const content = document.createElement("div");
    content.style.cssText = "block-size:1000px;padding-block-start:5.3px;";
    const svgs = styles.map((style) => {
      const holder = document.createElement("div");
      holder.style.cssText = style;
      const svg = icon();
      holder.append(svg);
      content.append(holder);
      return svg;
    });
    box.append(content);
    frame.append(box);
    document.body.append(frame);
    hosts.push(frame);
    return { box, svgs };
  }

  /** Snap at scrollTop 40, scroll one pixel, snap again: the translate before and after, and how far it carried the
   *  icon on screen. */
  function acrossOnePixel(
    box: HTMLElement,
    svg: SVGSVGElement,
  ): { carried: number; before: string; after: string } {
    box.scrollTop = 40;
    snapIcons();
    const before = svg.style.translate;
    const y = svg.getBoundingClientRect().y;
    box.scrollTop = 41;
    expect(box.scrollTop, "the premise: a one-pixel scroll, half a device pixel of phase").toBe(41);
    const carried = Math.round((y - svg.getBoundingClientRect().y) * 1000) / 1000;
    snapIcons();
    return { carried, before, after: svg.style.translate };
  }

  it("moves no in-flow icon when the scroller scrolls by a pixel at DPR 1.5", async () => {
    await setDeviceScale(1.5);
    const { box, svgs } = scroller([""]);
    const svg = svgs[0]!;

    const { carried, before, after } = acrossOnePixel(box, svg);

    expect({ needed: before !== "", carried, after }).toEqual({
      needed: true,
      carried: 1,
      after: before,
    });
  });

  it.each([
    // Painted where the viewport puts it, however far the scroller has moved.
    ["a fixed icon", "position:fixed;left:40.3px;top:60.6px;"],
    // Stuck to the scroller's top once its own offset has scrolled by.
    ["a stuck sticky icon", "position:sticky;top:0;"],
  ])(
    "moves %s neither on screen nor in its snap on a one-pixel scroll at DPR 1.5",
    async (_, style) => {
      await setDeviceScale(1.5);
      const { box, svgs } = scroller([style]);

      const { carried, before, after } = acrossOnePixel(box, svgs[0]!);

      expect({ carried, after }).toEqual({ carried: 0, after: before });
    },
  );

  it("keeps the snap of an absolute icon whose containing block is outside the scroller", async () => {
    await setDeviceScale(1.5);
    const { box, svgs } = scroller(["position:absolute;left:3.3px;top:7.7px;"]);

    const { carried, before, after } = acrossOnePixel(box, svgs[0]!);

    expect({ carried, after }).toEqual({ carried: 0, after: before });
  });

  it("keeps the snap of a fixed icon a contained row holds, which the scroller carries", async () => {
    await setDeviceScale(1.5);
    // `contain: paint` (which `.msg-row`'s `content-visibility: auto` implies) makes the row the fixed box's
    // containing block.
    const { box, svgs } = scroller(["contain:paint;block-size:30px;"]);
    const svg = svgs[0]!;
    const fixed = document.createElement("div");
    fixed.style.cssText = "position:fixed;left:4.3px;top:6.6px;";
    svg.replaceWith(fixed);
    fixed.append(svg);

    const { carried, before, after } = acrossOnePixel(box, svg);

    expect({ carried, after }).toEqual({ carried: 1, after: before });
  });

  it("keeps the snap of a sticky icon its inset has not caught, which scrolls with the content", async () => {
    await setDeviceScale(1.5);
    const { box, svgs } = scroller(["position:sticky;top:0;margin-block-start:300px;"]);

    const { carried, before, after } = acrossOnePixel(box, svgs[0]!);

    expect({ carried, after }).toEqual({ carried: 1, after: before });
  });
});
