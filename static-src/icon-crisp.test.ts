import { afterEach, describe, expect, it, vi } from "vitest";

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

/** Read through the screen CTM, so a glyph centred in a wider slot is measured where it paints. */
function drawingOff(svg: SVGSVGElement, target: number): number {
  const m = svg.getScreenCTM();
  expect(m).not.toBeNull();
  if (m === null) {
    return 1;
  }
  return Math.max(phaseOff(frac(m.e + 3 * m.a), target), phaseOff(frac(m.f + 3 * m.d), target));
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

  it("declines an icon an ancestor is mid-SCALE on, rather than snapping a doomed reading", () => {
    // `.pill-expand-content` opens on `scale(0.4) -> scale(1)`; a pass mid-flight measures the scaled box, and the settle
    // pass then moves every icon at once. Layout size disagreeing with painted size is the signal.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.transform = "scale(0.4)";
    }
    expect(snapIcons(), "nothing is written while the ancestor scales").toBe(0);
    expect(svg.style.translate).toBe("");

    // The declined reading is taken as soon as that transform lands.
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
