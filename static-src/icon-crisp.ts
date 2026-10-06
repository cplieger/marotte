/** Snaps icon boxes onto the pixel grid so a structural stroke or fill edge paints whole
 *  device pixels. */

const TIERS = ".ic-inline, .ic-ui, .ic-lg, .ic-hero";
const SHAPES = "path, circle, line, rect, polyline, polygon, ellipse";

/** `s` is the screen offset in force, `t` the local translate that produces it. The two
 *  differ whenever an ancestor rotates the icon, and the rect is measured in screen space. */
const applied = new WeakMap<SVGSVGElement, { sx: number; sy: number; tx: number; ty: number }>();

const frac = (v: number): number => ((v % 1) + 1) % 1;

/** Device phase a multiple-of-3 coordinate wants. A stroke is crisp with its centre on
 *  frac(deviceWidth / 2): a half pixel when its device width is odd, a boundary when even.
 *  A fill-only glyph is crisp with its edge on a boundary. The first stroked shape decides;
 *  a glyph with none is fill-only. `strokeWidth` is CSS px under `non-scaling-stroke`. */
function wantedDevicePhase(el: SVGSVGElement, dpr: number): number {
  for (const shape of el.querySelectorAll(SHAPES)) {
    const cs = getComputedStyle(shape);
    if (cs.stroke !== "none") {
      return frac((parseFloat(cs.strokeWidth) * dpr) / 2);
    }
  }
  return 0;
}

/** Smallest CSS translate putting the coordinate 3 units in from `origin` on device `phase`. */
function toDevicePhase(origin: number, scale: number, phase: number, dpr: number): number {
  let delta = phase - frac(dpr * (origin + 3 * scale));
  if (delta > 0.5) {
    delta -= 1;
  }
  if (delta <= -0.5) {
    delta += 1;
  }
  return delta / dpr;
}

const q = (v: number): number => Math.round(v * 1000) / 1000;

/** The icon's own space is not the screen's: `.disclosure-chevron` rotates its child, and
 *  `translate` composes inside that rotation, so a screen-space correction written straight
 *  onto the box lands on the wrong axis. Maps a screen delta into the element's own space.
 *
 *  `getScreenCTM` carries the whole ancestor chain; dividing by the drawing's scale leaves the
 *  rotation alone, which is ORTHOGONAL for the axis-aligned cases — so the inverse is the
 *  transpose. Anything else answers null: a non-right-angle rotation cannot be crisp at all,
 *  and the box's screen AABB is not its box, so the phase read would be meaningless too. */
function toLocal(m: DOMMatrix, scale: number, dx: number, dy: number): [number, number] | null {
  const a = m.a / scale;
  const b = m.b / scale;
  const c = m.c / scale;
  const d = m.d / scale;
  const unit = (v: number): boolean => Math.abs(Math.abs(v) - 1) < 0.01;
  const zero = (v: number): boolean => Math.abs(v) < 0.01;
  const aligned =
    (unit(a) && unit(d) && zero(b) && zero(c)) || (zero(a) && zero(d) && unit(b) && unit(c));
  if (!aligned) {
    return null;
  }
  return [q(a * dx + b * dy), q(c * dx + d * dy)];
}

/** Set by `snapIcons` when it declined an icon an ancestor was mid-transform on, so
 *  the caller re-asks once that transform settles. A NON-AXIS-ALIGNED rotation is a
 *  PERMANENT decline and deliberately does not set it: re-arming on one would poll
 *  every 250ms for the life of the page. */
let deferred = false;

/** Returns how many boxes moved. Also republishes `deferred` for this pass, which is
 *  why the reset lives HERE rather than at the caller: assigning it beside the call
 *  narrows it to `false` for the read that follows, and the caller cannot see that
 *  this function writes it. */
export function snapIcons(root: ParentNode = document): number {
  deferred = false;
  // Every rect read before any style write: interleaving forces a layout per icon.
  const work: { el: SVGSVGElement; sx: number; sy: number; tx: number; ty: number }[] = [];
  const dpr = window.devicePixelRatio;
  for (const el of root.querySelectorAll<SVGSVGElement>(TIERS)) {
    const rect = el.getBoundingClientRect();
    const m = el.getScreenCTM();
    if (!rect.width || !rect.height || m === null) {
      continue;
    }
    // The DRAWING's scale, not the box's: a flex slot can be wider than the glyph it holds
    // (`[id="scroll-bottom"] > svg` in the rail form), and `preserveAspectRatio` centres it.
    const scale = Math.hypot(m.a, m.b);
    if (scale === 0) {
      continue;
    }
    // AN ANCESTOR MID-SCALE IS A WRONG READING, NOT A DIFFERENT ONE, so it is declined
    // and re-asked rather than answered. `.pill-expand-content` opens on
    // `scale(0.4) -> scale(1)` (15-input.css): an icon snapped during that flight
    // reads a SCALED rect, so both the measured phase and the scale-dependent target
    // come out of a geometry about to change, and the settle pass then corrects the
    // whole set at once. Layout size is unaffected by any transform, so disagreeing
    // with the painted size is exactly "something is scaling me".
    const layout = parseFloat(getComputedStyle(el).inlineSize);
    if (Math.abs(rect.width - layout) > 0.02) {
      deferred = true;
      continue;
    }
    const phase = wantedDevicePhase(el, dpr);
    // `rect` includes the offset already applied, so measure the original box. The offset is
    // subtracted in SCREEN space, which is the space the rect is in.
    const prev = applied.get(el) ?? { sx: 0, sy: 0, tx: 0, ty: 0 };
    const sx = q(toDevicePhase(rect.x - prev.sx, scale, phase, dpr));
    const sy = q(toDevicePhase(rect.y - prev.sy, scale, phase, dpr));
    const local = toLocal(m, scale, sx, sy);
    if (local === null) {
      continue;
    }
    work.push({ el, sx, sy, tx: local[0], ty: local[1] });
  }

  let changed = 0;
  for (const { el, sx, sy, tx, ty } of work) {
    const prev = applied.get(el);
    if (prev?.tx === tx && prev.ty === ty) {
      continue;
    }
    applied.set(el, { sx, sy, tx, ty });
    // `translate`, not `transform`: a running animation's transform outranks inline.
    el.style.translate = tx === 0 && ty === 0 ? "" : `${String(tx)}px ${String(ty)}px`;
    changed++;
  }
  return changed;
}

let fullPass = false;
let resize: ResizeObserver | undefined;
let added: MutationObserver | undefined;
const pending = new Set<SVGSVGElement>();
let incremental = false;
let settleTimer: ReturnType<typeof setTimeout> | undefined;
const SETTLE_MS = 250;

function scheduleFull(): void {
  if (fullPass) {
    return;
  }
  fullPass = true;
  requestAnimationFrame(() => {
    fullPass = false;
    pending.clear();
    // A pass that MOVED something has itself changed the layout, so re-check once it settles.
    // Self-terminating: an icon already on target computes the same offset and reports no
    // change, so a converged document arms nothing.
    //
    // A DEFERRED icon re-arms it for the other reason: its ancestor is mid-scale, so the
    // reading it declined becomes available as soon as that transform lands. Also
    // self-terminating, because a settled ancestor stops deferring.
    const moved = snapIcons();
    if (moved > 0 || deferred) {
      scheduleSettle();
    }
  });
}

/** Re-measure once the DOM stops moving: a reflow after an icon was snapped leaves its
 *  offset stale, and the transcript reflows constantly while a turn streams. Debounced,
 *  so this is one pass per quiet period rather than one per frame. */
function scheduleSettle(): void {
  if (settleTimer !== undefined) {
    clearTimeout(settleTimer);
  }
  settleTimer = setTimeout(() => {
    settleTimer = undefined;
    scheduleFull();
  }, SETTLE_MS);
}

function scheduleAdded(nodes: SVGSVGElement[]): void {
  for (const n of nodes) {
    pending.add(n);
  }
  scheduleSettle();
  if (incremental || fullPass) {
    return;
  }
  incremental = true;
  requestAnimationFrame(() => {
    incremental = false;
    if (fullPass || pending.size === 0) {
      return;
    }
    const batch = [...pending];
    pending.clear();
    // Per arrival, so the cost tracks arrivals rather than the whole document.
    for (const el of batch) {
      if (el.isConnected) {
        snapIcons(scopeOf(el));
      }
    }
  });
}

/** A `ParentNode` yielding exactly `el`, so `snapIcons` serves one element too. */
function scopeOf(el: SVGSVGElement): ParentNode {
  return {
    querySelectorAll: () => [el] as unknown as NodeListOf<Element>,
  } as unknown as ParentNode;
}

function iconsIn(node: Node): SVGSVGElement[] {
  if (!(node instanceof Element)) {
    return [];
  }
  const out = [...node.querySelectorAll<SVGSVGElement>(TIERS)];
  if (node.matches(TIERS)) {
    out.push(node as SVGSVGElement);
  }
  return out;
}

/** Idempotent. */
export function initIconCrisp(): void {
  scheduleFull();
  if (resize !== undefined) {
    return;
  }
  // `document.body`, not `#app`: modals and the login form are body-level siblings.
  const target = document.body;
  // A resize moves existing icons; an addition only needs the new nodes measured, and
  // a ResizeObserver does not fire for content added inside its target.
  resize = new ResizeObserver(scheduleFull);
  resize.observe(target);
  added = new MutationObserver((records) => {
    const fresh: SVGSVGElement[] = [];
    let rotated = false;
    for (const rec of records) {
      if (rec.type === "attributes") {
        rotated = true;
        continue;
      }
      for (const node of rec.addedNodes) {
        fresh.push(...iconsIn(node));
      }
    }
    if (fresh.length) {
      scheduleAdded(fresh);
    }
    // A disclosure toggle re-rotates its chevron, which changes the map a screen correction
    // has to be written through, and it adds no node for the branch above to see. The settle
    // delay outlasts the rotation's own transition; a pass taken mid-rotation reads a map
    // that is not axis-aligned and `toLocal` declines it, so the stale offset simply holds.
    if (rotated) {
      scheduleSettle();
    }
  });
  added.observe(target, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["aria-expanded"],
  });
  // A web font landing changes control heights, moving every icon inside them.
  void document.fonts.ready.then(scheduleFull);
}
