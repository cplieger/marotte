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

/** Device px; above `q`'s rounding (0.0015 at DPR 3) and the float32 noise in a rect, below anything visible. */
const CRISP_DEVICE_PX = 0.01;

/** One axis's smallest screen offset that paints the box crisp. */
function axisOffset(
  painted: number,
  inForce: number,
  scale: number,
  phase: number,
  dpr: number,
): number {
  // `painted` includes the offset in force, so subtract it in screen space to measure the original box.
  const fresh = q(toDevicePhase(painted - inForce, scale, phase, dpr));
  // At a residual of exactly half a device pixel both directions are crisp, and re-deriving the side from a float32
  // rect flips it every pass, so a crisp box keeps its side. Only when no longer than `fresh`: after a DPR change the
  // old offset can be crisp a whole device pixel off the box.
  const crisp = Math.abs(toDevicePhase(painted, scale, phase, dpr)) * dpr < CRISP_DEVICE_PX;
  const minimal = Math.abs(Math.abs(inForce) - Math.abs(fresh)) * dpr < CRISP_DEVICE_PX;
  return crisp && minimal ? inForce : fresh;
}

/** Properties that move a box without a layout. */
const MOVES = ["transform", "translate", "rotate", "scale"];

/**
 * Keyframe properties that leave layout alone, so they move nothing outside the animated element; MOVES catches the
 * transform family moving its own subtree. Anything else counts as layout: a needless hold costs one re-ask, a missed
 * one a wobble.
 */
const PAINT_ONLY =
  /^(?:opacity|color|fill|filter|backdropFilter|visibility|zIndex|clipPath|boxShadow|textShadow|caretColor|accentColor|transformOrigin|background.*|outline.*|stroke.*|mask.*|border.*Color|textDecoration.*|transform|translate|rotate|scale)$/;

/** `getKeyframes()` keys that describe the keyframe, not a property. */
const KEYFRAME_META = new Set(["offset", "computedOffset", "easing", "composite"]);

interface Motion {
  /** Elements a running animation or transition is moving; `forever` marks an infinite one, under which no reading holds. */
  readonly movers: readonly { readonly el: Element; readonly forever: boolean }[];
  /** A finite animation is changing layout, which moves boxes outside its own subtree (a row collapsing above them). */
  readonly reflowing: boolean;
}

function currentMotion(): Motion {
  const movers: { el: Element; forever: boolean }[] = [];
  let reflowing = false;
  for (const a of document.getAnimations()) {
    const fx = a.effect;
    if (a.playState !== "running" || !(fx instanceof KeyframeEffect) || fx.target === null) {
      continue;
    }
    const forever = fx.getTiming().iterations === Infinity;
    const props = fx
      .getKeyframes()
      .flatMap((k) => Object.keys(k).filter((p) => !KEYFRAME_META.has(p)));
    // Finite only: an infinite one (a spinner's custom-property loop) would hold every pass for ever.
    if (!forever && props.some((p) => !PAINT_ONLY.test(p))) {
      reflowing = true;
    }
    // A pseudo-element's transform moves the pseudo-element, not the icons beside it.
    if (fx.pseudoElement === null && props.some((p) => MOVES.includes(p))) {
      movers.push({ el: fx.target, forever });
    }
  }
  return { movers, reflowing };
}

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
 * Set when an icon was declined on a reading that can change (a finite animation, a moving ancestor), so the caller
 * re-asks. An infinite animation and a static ancestor scale are permanent declines and do not set it, or the page
 * would poll forever.
 */
let deferred = false;

/** Icons under a static ancestor scale or tilt, rebuilt by every full pass; a transform ending on an ancestor of one
 *  re-arms the snap. */
const staticScaled = new Set<SVGSVGElement>();

/** Per-pass computed styles, so the containing-block walk reads each ancestor once. */
type StyleMemo = Map<Element, CSSStyleDeclaration>;

function styleOf(el: Element, memo: StyleMemo): CSSStyleDeclaration {
  let cs = memo.get(el);
  if (cs === undefined) {
    cs = getComputedStyle(el);
    memo.set(el, cs);
  }
  return cs;
}

const CB_PROPS = ["transform", "translate", "scale", "rotate", "perspective", "filter"] as const;

/** Whether `cs` makes its box a containing block for fixed and absolute descendants (CSS Position 3 §2.1). */
function containsFixed(cs: CSSStyleDeclaration): boolean {
  if (CB_PROPS.some((p) => cs[p] !== "none" && cs[p] !== "")) {
    return true;
  }
  if (/\b(layout|paint|strict|content)\b/.test(cs.contain) || cs.contentVisibility === "auto") {
    return true;
  }
  return /\b(transform|translate|scale|rotate|perspective|filter)\b/.test(cs.willChange);
}

function scrolls(cs: CSSStyleDeclaration, axis: "x" | "y"): boolean {
  const o = axis === "y" ? cs.overflowY : cs.overflowX;
  return o === "auto" || o === "scroll";
}

/** Whether a sticky box is stuck on `axis` against `scroller`'s scrollport: only then does it not move with the
 *  scroller's content. */
function stuck(box: Element, cs: CSSStyleDeclaration, scroller: Element, axis: "x" | "y"): boolean {
  const r = box.getBoundingClientRect();
  const s = scroller.getBoundingClientRect();
  const near = (a: number, b: number): boolean => Math.abs(a - b) <= 0.5;
  if (axis === "y") {
    const top = s.top + scroller.clientTop;
    return (
      (cs.top !== "auto" && near(r.top, top + parseFloat(cs.top))) ||
      (cs.bottom !== "auto" && near(r.bottom, top + scroller.clientHeight - parseFloat(cs.bottom)))
    );
  }
  const left = s.left + scroller.clientLeft;
  return (
    (cs.left !== "auto" && near(r.left, left + parseFloat(cs.left))) ||
    (cs.right !== "auto" && near(r.right, left + scroller.clientWidth - parseFloat(cs.right)))
  );
}

/** The scroll offsets that move `el` with its content: each scroller in its containing-block chain, less the axis a
 *  stuck sticky box pins to its nearest scroller. Added to a screen rect, it measures `el` in content space, where a
 *  scroll changes nothing, so no scroll leaves a snap stale. A top-layer box takes none. */
function contentOffset(el: Element, memo: StyleMemo): { x: number; y: number } {
  let x = 0;
  let y = 0;
  const at: { mode: "flow" | "absolute" | "fixed" } = { mode: "flow" };
  const sticky: { box: Element; cs: CSSStyleDeclaration }[] = [];
  const settle = (box: Element, cs: CSSStyleDeclaration): void => {
    if (cs.position === "fixed") {
      at.mode = "fixed";
    } else if (cs.position === "absolute") {
      at.mode = "absolute";
    } else if (cs.position === "sticky") {
      sticky.push({ box, cs });
    }
  };
  if (el.matches(":modal, :popover-open")) {
    return { x, y };
  }
  settle(el, styleOf(el, memo));
  for (let a = el.parentElement; a !== null; a = a.parentElement) {
    const cs = styleOf(a, memo);
    if (a.matches(":modal, :popover-open")) {
      break;
    }
    if (at.mode === "fixed" && containsFixed(cs)) {
      at.mode = "flow";
    } else if (at.mode === "absolute" && (cs.position !== "static" || containsFixed(cs))) {
      at.mode = "flow";
    } else if (at.mode !== "flow") {
      settle(a, cs);
      continue;
    }
    for (const axis of ["x", "y"] as const) {
      if (!scrolls(cs, axis)) {
        continue;
      }
      const pinned = sticky.findIndex((s) => stuck(s.box, s.cs, a, axis));
      if (pinned >= 0) {
        sticky.splice(pinned, 1);
        continue;
      }
      if (axis === "y") {
        y += a.scrollTop;
      } else {
        x += a.scrollLeft;
      }
    }
    if (scrolls(cs, "x") || scrolls(cs, "y")) {
      sticky.length = 0;
    }
    settle(a, cs);
  }
  return { x, y };
}

/** Returns how many boxes moved. Resets and republishes `deferred` for this pass. */
export function snapIcons(root: ParentNode = document): number {
  staticScaled.clear();
  return snapWith(root.querySelectorAll<SVGSVGElement>(TIERS), currentMotion());
}

/** Snap every tier icon inside each of `boxes`, once per call, leaving out an icon whose nearest `skip` ancestor lies
 *  inside that box. Reads boxes and writes only `translate`; a no-op before `initIconCrisp`. */
export function snapIconsIn(boxes: Iterable<Element>, skip: string | null): void {
  if (resize === undefined) {
    return;
  }
  const icons = new Set<SVGSVGElement>();
  for (const box of boxes) {
    for (const ic of iconsIn(box)) {
      const s = skip === null ? null : ic.closest(skip);
      if (s === null || s === box || !box.contains(s)) {
        icons.add(ic);
      }
    }
  }
  if (icons.size > 0) {
    const wasDeferred = deferred;
    snapWith(icons, currentMotion());
    deferred ||= wasDeferred;
  }
}

function snapWith(icons: Iterable<SVGSVGElement>, motion: Motion): number {
  deferred = false;
  // A reading against a moving origin (the phone drawer's slide, a row collapsing above) is undone by the settle pass
  // once the motion lands, which reads as a wobble.
  if (motion.reflowing) {
    deferred = true;
    return 0;
  }
  // Every rect read before any style write: interleaving forces a layout per icon.
  const work: { el: SVGSVGElement; sx: number; sy: number; tx: number; ty: number }[] = [];
  const dpr = window.devicePixelRatio;
  const memo: StyleMemo = new Map();
  for (const el of icons) {
    const movers = motion.movers.filter((m) => m.el.contains(el));
    if (movers.length > 0) {
      if (movers.every((m) => !m.forever)) {
        deferred = true;
      }
      continue;
    }
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
    // Layout size ignores transforms, so disagreeing with painted size means an ancestor scale or tilt. A translate
    // written under it is scaled with it, so passes can chase their own offset. With nothing moving it is static:
    // declined until a transform ends on that ancestor.
    const layout = parseFloat(styleOf(el, memo).inlineSize);
    if (Math.abs(rect.width - layout) > 0.02) {
      staticScaled.add(el);
      continue;
    }
    const off = contentOffset(el, memo);
    const phase = wantedDevicePhase(el, dpr);
    const prev = applied.get(el) ?? { sx: 0, sy: 0, tx: 0, ty: 0 };
    const sx = axisOffset(rect.x + off.x, prev.sx, scale, phase, dpr);
    const sy = axisOffset(rect.y + off.y, prev.sy, scale, phase, dpr);
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
    // Per arrival, so cost tracks arrivals; the animation scan is per batch.
    snapWith(
      batch.filter((el) => el.isConnected),
      currentMotion(),
    );
  });
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
  // A static scale's decline lifts only when a transform ends on its ancestor; hover colours end constantly and match
  // neither filter.
  const rearm = (e: Event): void => {
    const target = e.target;
    if (!(target instanceof Element)) {
      return;
    }
    if (e instanceof TransitionEvent && !MOVES.includes(e.propertyName)) {
      return;
    }
    for (const ic of staticScaled) {
      if (target.contains(ic)) {
        scheduleFull();
        return;
      }
    }
  };
  document.addEventListener("transitionend", rearm, { capture: true, passive: true });
  document.addEventListener("animationend", rearm, { capture: true, passive: true });
  // A web font landing changes control heights.
  void document.fonts.ready.then(scheduleFull);
}
