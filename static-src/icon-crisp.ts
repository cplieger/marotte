/** Snaps icon boxes onto the pixel grid so a structural stroke or fill edge paints whole device pixels. */

const TIERS = ".ic-inline, .ic-ui, .ic-lg, .ic-hero";
const SHAPES = "path, circle, line, rect, polyline, polygon, ellipse";

/** `s` is the screen offset in force, `t` the local translate producing it; they differ when an ancestor rotates. */
const applied = new WeakMap<SVGSVGElement, { sx: number; sy: number; tx: number; ty: number }>();

const frac = (v: number): number => ((v % 1) + 1) % 1;

/**
 * A stroke is crisp with its centre on frac(deviceWidth / 2); a fill-only glyph with its edge on a boundary. The first
 * stroked shape decides. `strokeWidth` is CSS px under `non-scaling-stroke`.
 */
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

/**
 * Maps a screen delta into the element's own space: `translate` composes inside an ancestor rotation. For
 * axis-aligned rotations the inverse is the transpose; anything else answers null, since it cannot be crisp.
 */
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

/**
 * Set when an ancestor was mid-transform, so the caller re-asks. A non-axis-aligned rotation is a permanent decline
 * and does not set it, or the page would poll forever.
 */
let deferred = false;

/** Returns how many boxes moved. Resets and republishes `deferred` for this pass. */
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
    // The drawing's scale, not the box's: a flex slot can be wider than its glyph.
    const scale = Math.hypot(m.a, m.b);
    if (scale === 0) {
      continue;
    }
    // An ancestor mid-scale is a wrong reading, so it is declined and re-asked. `.pill-expand-content` opens on
    // `scale(0.4) -> scale(1)`; layout size ignores transforms, so disagreeing with painted size means "something is
    // scaling me".
    const layout = parseFloat(getComputedStyle(el).inlineSize);
    if (Math.abs(rect.width - layout) > 0.02) {
      deferred = true;
      continue;
    }
    const phase = wantedDevicePhase(el, dpr);
    // `rect` includes the offset already applied, so subtract it in screen space to measure the original box.
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
    // A pass that moved something changed layout, so re-check once it settles; a deferred icon re-arms too. Both
    // self-terminate on convergence.
    const moved = snapIcons();
    if (moved > 0 || deferred) {
      scheduleSettle();
    }
  });
}

/**
 * A reflow leaves a snapped offset stale, and the transcript reflows while streaming; debounced to one pass per quiet
 * period.
 */
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
    // Per arrival, so cost tracks arrivals.
    for (const el of batch) {
      if (el.isConnected) {
        snapIcons(scopeOf(el));
      }
    }
  });
}

/** Yields exactly `el`, so `snapIcons` serves one element too. */
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
  // Modals and the login form are body-level siblings of `#app`.
  const target = document.body;
  // A resize moves existing icons; a ResizeObserver does not fire for content added inside its target.
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
    // A disclosure toggle re-rotates its chevron and adds no node. The settle delay outlasts the rotation; a mid-rotation
    // pass is declined by `toLocal`, so the stale offset holds.
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
  // A web font landing changes control heights.
  void document.fonts.ready.then(scheduleFull);
}
