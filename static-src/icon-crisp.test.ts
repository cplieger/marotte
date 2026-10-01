import { afterEach, describe, expect, it, vi } from "vitest";

import { snapIcons } from "./icon-crisp.js";

const SVG_NS = "http://www.w3.org/2000/svg";

interface IconOpts {
  readonly cls?: string;
  readonly size?: number;
  readonly strokeWidth?: number;
  readonly fillOnly?: boolean;
}

/** An `ic-ui` icon by default: the 24-unit grid at 16px, stroke 1, so a multiple-of-3
 *  coordinate wants phase 0.5. Sized and stroked inline because `03-base.css` is not loaded
 *  here — the tier's SCALE and STROKE are the inputs. `fill`/`stroke` on the root are what
 *  `icons.ts`'s `svg()` writes, so the shapes classify as production's do. */
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

/** Places the icon at a deliberately fractional offset, optionally inside a rotated box. */
function mount(
  rotateDeg: number | null,
  opts: IconOpts = {},
): { host: HTMLElement; svg: SVGSVGElement } {
  const host = document.createElement("div");
  host.style.position = "absolute";
  // Fractional on BOTH axes and different per axis, so a correction applied to the wrong
  // one cannot accidentally satisfy the assertion.
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

/** Distance of `phase` from `target`, on the shorter way round. 0 is crisp. */
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

/** Worst axis of the DRAWING's coordinate 3, read through the screen CTM rather than the
 *  box, so a glyph centred in a wider slot is measured where it paints. */
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
    // The whole defect: `translate` composes INSIDE the wrapper's rotation, so a screen-space
    // correction written straight onto the box lands on the other axis. Only a screen-space
    // assertion can see it — the inline value looks plausible either way.
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
    // At 30deg the strokes cross the grid diagonally, so there is no phase that helps, and
    // the box's screen AABB is not its box — the phase read itself would be meaningless.
    const svg = place(30);
    snapIcons();
    expect(svg.style.translate).toBe("");
  });

  it("declines an icon an ancestor is mid-SCALE on, rather than snapping a doomed reading", () => {
    // THE DEFECT: `.pill-expand-content` opens on `scale(0.4) -> scale(1)`, and a pass
    // taken during that flight measures the SCALED box — so both the phase it reads and
    // the target `targetPhase` derives from its scale describe geometry that is about
    // to change, and the settle pass then moves every icon at once. Reported as the
    // role menu's icons jumping right every time it opened. The layout size cannot be
    // scaled by an ancestor, so disagreeing with the painted size IS the signal.
    const svg = place(null);
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.transform = "scale(0.4)";
    }
    expect(snapIcons(), "nothing is written while the ancestor scales").toBe(0);
    expect(svg.style.translate).toBe("");

    // And the reading it declined is taken as soon as that transform lands.
    if (host instanceof HTMLElement) {
      host.style.transform = "";
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("converges rather than chasing its own offset when a snapped icon moves", () => {
    // The offset in force is a SCREEN delta while the value written is a LOCAL one; reading
    // the box back means subtracting the screen one, or every later pass compounds the error.
    const svg = place(-90);
    snapIcons();
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.left = "44.85px";
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
    // A second pass over an already-converged icon must move nothing at all.
    expect(snapIcons()).toBe(0);
  });

  it("puts a stroke-2 tier on phase 0, where an even device stroke is crisp", () => {
    // `.ic-lg` strokes 2 CSS px, so at DPR 1 the stroke's centre wants a pixel BOUNDARY;
    // the 0.5 that is right for a 1px stroke splits a 2px one across three columns.
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
    // Device phase 0 is CSS phase 0 OR 0.5, so the box's phase alone cannot tell a DPR-aware
    // snap from one assuming DPR 1; what does is the MOVE, at most a quarter CSS px at DPR 2.
    // These offsets are the ones where the two disagree (DPR 1 would move 0.35 to reach 0.5).
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
    // The rail form's `[id="scroll-bottom"] > svg` takes `flex: 0 0 42px` against a 16px
    // block size, and `preserveAspectRatio` centres a 16px drawing inside it.
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
