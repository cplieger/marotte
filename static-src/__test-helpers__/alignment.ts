// Vertical alignment of text and icons as the eye reads it: two text runs on one line share a
// baseline, and an icon or a box's text sits between the cap-height and x-height middles of the
// text it accompanies. Box centres alone are not enough: two centred boxes of different font sizes
// leave their baselines apart by `Δfont-size × (ascent − descent) / 2`.

/** One run of text: where its first line sits and the styles that put it there. */
interface TextPiece {
  readonly sel: string;
  readonly text: string;
  readonly baseline: number;
  /** The baseline read off the glyph run's rect and the font's ascent, a cross-check on `baseline`. */
  readonly rangeBaseline: number;
  readonly top: number;
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  /** The last line's baseline, top, bottom and right edge, where a run to its right joins it. */
  readonly endBaseline: number;
  readonly endTop: number;
  readonly endBottom: number;
  readonly endRight: number;
  readonly boxCy: number;
  readonly capMid: number;
  readonly xMid: number;
  readonly caps: boolean;
  readonly lines: number;
  readonly fontSize: string;
  readonly lineHeight: string;
  readonly fontFamily: string;
  readonly verticalAlign: string;
  readonly display: string;
  readonly parentDisplay: string;
  readonly parentAlignItems: string;
}

type FindingKind = "baseline" | "icon-text" | "box-text" | "box-icon";

export interface Finding {
  readonly kind: FindingKind;
  /** Stable across repeats of one shape: the measured elements' selector keys. */
  readonly key: string;
  /** Signed px: the second piece's baseline minus the first's, or the centre's distance out of the
   *  [cap-middle, x-middle] band (positive low). */
  readonly delta: number;
  readonly a: TextPiece | BoxInfo;
  readonly b: TextPiece | BoxInfo;
  /** The element a screenshot of this site frames. */
  readonly frame: Element;
}

interface BoxInfo {
  readonly sel: string;
  readonly cy: number;
  readonly h: number;
  readonly top: number;
  readonly display: string;
  readonly verticalAlign: string;
  readonly parentDisplay: string;
  readonly parentAlignItems: string;
}

const r2 = (n: number): number => Math.round(n * 100) / 100;

/** `tag#id.class.class` of an element and its parent, enough to grep its rule. */
function selectorKey(el: Element): string {
  const one = (e: Element): string => {
    const id = e.id === "" ? "" : `#${e.id}`;
    const cls = [...e.classList]
      .filter((c) => !/^(hidden|is-|active|open)/.test(c))
      .slice(0, 3)
      .map((c) => `.${c}`)
      .join("");
    return `${e.tagName.toLowerCase()}${id}${cls}`;
  };
  const p = el.parentElement;
  return p === null || p === document.body ? one(el) : `${one(p)} > ${one(el)}`;
}

const metricsCache = new Map<string, { cap: number; x: number; ascent: number }>();
const ctx = document.createElement("canvas").getContext("2d")!;

/** Cap height, x-height and ascent at the element's font size. Measured at 100x and scaled: canvas
 *  text bounds come back rounded to whole pixels, and Chromium's rounding changes with the DPR. */
function fontMetrics(cs: CSSStyleDeclaration): { cap: number; x: number; ascent: number } {
  const font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
  let m = metricsCache.get(font);
  if (m === undefined) {
    const k = 100;
    ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${String(parseFloat(cs.fontSize) * k)}px ${cs.fontFamily}`;
    m = {
      cap: ctx.measureText("H").actualBoundingBoxAscent / k,
      x: ctx.measureText("x").actualBoundingBoxAscent / k,
      ascent: ctx.measureText("Hx").fontBoundingBoxAscent / k,
    };
    metricsCache.set(font, m);
  }
  return m;
}

const FLEXLIKE = /^(inline-)?(flex|grid)$/;

/** The y of the baseline a text node's first (or last) line sits on. A zero-size inline-block
 *  placed beside it sits on that baseline. Where it cannot (a flex or grid item has no line box to
 *  hold one, and at a wrap the probe can stay on the previous line), the glyph run's content-area
 *  top plus the font's ascent stands in; the two agree to a rounding elsewhere. */
function textBaseline(node: Text, at: "start" | "end" = "start"): { probe: number; range: number } {
  const parent = node.parentElement!;
  const range = document.createRange();
  const end = node.data.trimEnd().length;
  range.setStart(node, at === "start" ? 0 : Math.max(0, end - 1));
  range.setEnd(node, at === "start" ? Math.min(1, node.length) : end);
  const rr = range.getClientRects()[0] ?? range.getBoundingClientRect();
  const viaRange = rr.top + fontMetrics(getComputedStyle(parent)).ascent;
  if (FLEXLIKE.test(getComputedStyle(parent).display)) {
    return { probe: viaRange, range: viaRange };
  }
  const probe = document.createElement("span");
  probe.style.cssText =
    "display:inline-block;inline-size:0;block-size:0;margin:0;padding:0;border:0;vertical-align:baseline";
  parent.insertBefore(probe, at === "start" ? node : node.nextSibling);
  const y = probe.getBoundingClientRect().top;
  probe.remove();
  return { probe: Math.abs(y - viaRange) <= 1.5 ? y : viaRange, range: viaRange };
}

/** The first non-blank text node under `root` that renders. */
function firstText(root: Node): Text | null {
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n = w.nextNode(); n !== null; n = w.nextNode()) {
    const t = n as Text;
    if (t.data.trim() !== "" && rendered(t)) {
      return t;
    }
  }
  return null;
}

function rendered(t: Text): boolean {
  const p = t.parentElement;
  if (p === null || !p.checkVisibility({ opacityProperty: true, visibilityProperty: true })) {
    return false;
  }
  const pr = p.getBoundingClientRect();
  if (pr.width <= 1 || pr.height <= 1) {
    return false;
  }
  const r = document.createRange();
  r.selectNodeContents(t);
  const b = r.getClientRects()[0];
  return b !== undefined && b.width > 0 && b.height > 0 && onTop(p, b);
}

/** Whether `el` is what a pointer at `r`'s centre reaches, so a layer above it does not hide it.
 *  A point outside the viewport cannot be hit-tested and counts as shown. */
function onTop(el: Element, r: DOMRect): boolean {
  const x = r.left + r.width / 2;
  const y = r.top + r.height / 2;
  if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) {
    return true;
  }
  const hit = document.elementFromPoint(x, y);
  return hit === null || el.contains(hit) || hit.contains(el) || commonDepth(el, hit) <= 3;
}

/** How many levels above `a` the nearest ancestor holding `b` sits. */
function commonDepth(a: Element, b: Element): number {
  let d = 0;
  for (let e: Element | null = a; e !== null; e = e.parentElement, d++) {
    if (e.contains(b)) {
      return d;
    }
  }
  return Infinity;
}

function piece(node: Text): TextPiece {
  const parent = node.parentElement!;
  const cs = getComputedStyle(parent);
  const pcs = parent.parentElement === null ? cs : getComputedStyle(parent.parentElement);
  const range = document.createRange();
  range.selectNodeContents(node);
  const rects = [...range.getClientRects()].filter((r) => r.width > 0);
  const first = rects[0] ?? range.getBoundingClientRect();
  const lines = new Set(rects.map((r) => Math.round(r.top))).size;
  const last = rects.at(-1) ?? first;
  const { probe, range: viaRange } = textBaseline(node);
  const endBaseline = lines > 1 ? textBaseline(node, "end").probe : probe;
  const m = fontMetrics(cs);
  const box = parent.getBoundingClientRect();
  const words = node.data.trim();
  const caps =
    cs.textTransform === "uppercase" || (/[A-Z0-9]/.test(words) && words === words.toUpperCase());
  return {
    sel: selectorKey(parent),
    text: words.slice(0, 40),
    baseline: r2(probe),
    rangeBaseline: r2(viaRange),
    top: r2(first.top),
    bottom: r2(first.bottom),
    left: r2(first.left),
    right: r2(
      Math.max(
        first.right,
        ...rects.filter((r) => Math.round(r.top) === Math.round(first.top)).map((r) => r.right),
      ),
    ),
    endBaseline: r2(endBaseline),
    endTop: r2(last.top),
    endBottom: r2(last.bottom),
    endRight: r2(last.right),
    boxCy: r2(box.top + box.height / 2),
    capMid: r2(probe - m.cap / 2),
    xMid: r2(probe - (caps ? m.cap : m.x) / 2),
    caps,
    lines,
    fontSize: cs.fontSize,
    lineHeight: cs.lineHeight,
    fontFamily: cs.fontFamily.split(",")[0]!.trim(),
    verticalAlign: cs.verticalAlign,
    display: cs.display,
    parentDisplay: pcs.display,
    parentAlignItems: pcs.alignItems,
  };
}

function boxInfo(el: Element): BoxInfo {
  const r = el.getBoundingClientRect();
  const cs = getComputedStyle(el);
  const p = el.parentElement;
  const pcs = p === null ? cs : getComputedStyle(p);
  return {
    sel: selectorKey(el),
    cy: r2(r.top + r.height / 2),
    h: r2(r.height),
    top: r2(r.top),
    display: cs.display,
    verticalAlign: cs.verticalAlign,
    parentDisplay: pcs.display,
    parentAlignItems: pcs.alignItems,
  };
}

/** How far `y` lies outside the text's [cap-middle, x-middle] band, positive below it. */
function offBand(y: number, t: TextPiece): number {
  if (y < t.capMid) {
    return r2(y - t.capMid);
  }
  return y > t.xMid ? r2(y - t.xMid) : 0;
}

/** The common ancestor of two nodes. */
function common(a: Node, b: Node): Element {
  const r = document.createRange();
  r.setStart(a, 0);
  r.setEnd(b, 0);
  const c = r.commonAncestorContainer;
  return c instanceof Element ? c : c.parentElement!;
}

const overlap = (a: { top: number; bottom: number }, b: { top: number; bottom: number }): number =>
  Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);

/** Nearer than this side by side, two runs or a run and an icon read as one line. */
const SIDE_PX = 48;

/** A control or painted box: what the eye reads as one unit, whose contents align to each other. */
function unitOf(n: Node, root: Element, paint = true): Element | null {
  for (let e = n.parentElement; e !== null && e !== root; e = e.parentElement) {
    if (e.matches(CONTROL) || (paint && painted(e))) {
      return e;
    }
  }
  return null;
}

const ICONS = "svg, img, input[type=checkbox], input[type=radio]";

const CONTROL =
  "button, a, label, summary, li, [role=button], [role=tab], [role=option], [role=menuitem]";

/** The unit a run reads in beside its row: an unpainted link is text in the line, so a run inside
 *  one still pairs with the run before it. */
function rowUnit(n: Node, root: Element): Element | null {
  let u = unitOf(n, root, false);
  while (u?.matches("a") === true && !painted(u)) {
    u = unitOf(u, root, false);
  }
  return u;
}

/** Two things read as one line when they share a unit, or, when neither has one, when the nearest
 *  box holding both is no taller than a row. */
function sameRow(a: Node, b: Node, root: Element, lineH: number): Element | null {
  const ua = rowUnit(a, root);
  const ub = rowUnit(b, root);
  const c = common(a, b);
  if (ua !== null && !ua.contains(b)) {
    return null;
  }
  if (ub !== null && !ub.contains(a)) {
    return null;
  }
  return c.getBoundingClientRect().height <= Math.max(48, 2.5 * lineH) ? c : null;
}

export interface MeasureOptions {
  /** Subtrees left out of the sweep. */
  readonly skip?: string;
  /** The tallest painted box whose one line of text is checked against its centre; 48 px. */
  readonly maxBox?: number;
}

/** Every alignment the eye reads under `root`: neighbouring text runs' baselines, each icon against
 *  its nearest text, and each painted box's text or icon against the box's centre. */
export function measureAlignment(root: Element, opts: MeasureOptions = {}): Finding[] {
  const skip = (n: Node): boolean => {
    const e = n instanceof Element ? n : n.parentElement;
    return opts.skip !== undefined && e?.closest(opts.skip) != null;
  };
  const texts: { node: Text; p: TextPiece }[] = [];
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n = w.nextNode(); n !== null; n = w.nextNode()) {
    const t = n as Text;
    if (t.data.trim() === "" || skip(t) || !rendered(t)) {
      continue;
    }
    texts.push({ node: t, p: piece(t) });
  }
  const out: Finding[] = [];
  const lineOf = (p: TextPiece): number => p.bottom - p.top;

  // Neighbouring runs on one line: each run's last line against the next run to its right.
  for (const a of texts) {
    let best: { node: Text; p: TextPiece } | null = null;
    const end = { top: a.p.endTop, bottom: a.p.endBottom };
    for (const b of texts) {
      if (b === a || b.p.left < a.p.endRight - 1 || b.p.left - a.p.endRight > SIDE_PX) {
        continue;
      }
      if (overlap(end, b.p) < 0.5 * Math.min(end.bottom - end.top, lineOf(b.p))) {
        continue;
      }
      if (best === null || b.p.left < best.p.left) {
        best = b;
      }
    }
    if (best === null || a.node.parentElement === best.node.parentElement) {
      continue;
    }
    const row = sameRow(a.node, best.node, root, Math.max(lineOf(a.p), lineOf(best.p)));
    if (row === null) {
      continue;
    }
    out.push({
      kind: "baseline",
      key: `${a.p.sel} | ${best.p.sel}`,
      delta: r2(best.p.baseline - a.p.endBaseline),
      a: a.p,
      b: best.p,
      frame: row,
    });
  }

  // Each icon against the nearest run on its line.
  for (const s of root.querySelectorAll(ICONS)) {
    if (skip(s) || s.parentElement?.closest("svg") != null || !s.checkVisibility()) {
      continue;
    }
    const r = s.getBoundingClientRect();
    if (r.height === 0 || r.height > 40 || !onTop(s, r)) {
      continue;
    }
    let best: { node: Text; p: TextPiece } | null = null;
    let gap = Infinity;
    for (const t of texts) {
      const p = t.p;
      if (p.lines !== 1 || overlap(r, p) < 0.5 * Math.min(r.height, lineOf(p))) {
        continue;
      }
      const g = Math.max(p.left - r.right, r.left - p.right);
      if (g > -1 && g <= SIDE_PX && g < gap) {
        gap = g;
        best = t;
      }
    }
    if (best === null) {
      continue;
    }
    const row = sameRow(s, best.node, root, Math.max(r.height, lineOf(best.p)));
    if (row === null) {
      continue;
    }
    const box = boxInfo(s);
    out.push({
      kind: "icon-text",
      key: `${box.sel} ~ ${best.p.sel}`,
      delta: offBand(box.cy, best.p),
      a: best.p,
      b: box,
      frame: row,
    });
  }

  // Each painted box (a pill, a chip, a badge, a button) whose content is one line, against it.
  for (const el of root.querySelectorAll<HTMLElement>("*")) {
    if (skip(el) || !el.checkVisibility() || !painted(el)) {
      continue;
    }
    const r = el.getBoundingClientRect();
    if (r.height === 0 || r.height > (opts.maxBox ?? 48)) {
      continue;
    }
    const own = texts.filter((t) => el.contains(t.node) && unitOf(t.node, root) === el);
    const icons = [...el.querySelectorAll("svg, img")].filter(
      (i) =>
        i.parentElement?.closest("svg") == null &&
        unitOf(i, root) === el &&
        i.getBoundingClientRect().height > 0,
    );
    const box = boxInfo(el);
    if (own.length > 0) {
      const p = own.reduce((m, t) =>
        parseFloat(t.p.fontSize) > parseFloat(m.p.fontSize) ? t : m,
      ).p;
      const oneLine =
        own.every((t) => t.p.lines === 1 && overlap(t.p, p) > 0.5 * lineOf(p)) &&
        icons.every((i) => overlap(i.getBoundingClientRect(), p) > 0);
      if (!oneLine) {
        continue;
      }
      out.push({
        kind: "box-text",
        key: `${box.sel} :: ${p.sel}`,
        delta: r2(-offBand(box.cy, p)),
        a: box,
        b: p,
        frame: el,
      });
    } else if (icons.length === 1) {
      const ib = boxInfo(icons[0]!);
      out.push({
        kind: "box-icon",
        key: `${box.sel} :: ${ib.sel}`,
        delta: r2(ib.cy - box.cy),
        a: box,
        b: ib,
        frame: el,
      });
    }
  }
  return out;
}

/** The cap top and baseline of the first line of `el`'s first rendered text: its ink's band. */
export function inkBand(el: Element): { top: number; baseline: number } {
  const t = firstText(el);
  if (t === null) {
    throw new Error(`no text in ${selectorKey(el)}`);
  }
  const p = piece(t);
  return {
    top: r2(p.baseline - fontMetrics(getComputedStyle(t.parentElement!)).cap),
    baseline: p.baseline,
  };
}

/** A box the eye sees an edge of: a fill or a border. */
function painted(el: Element): boolean {
  const cs = getComputedStyle(el);
  const seen = (c: string): boolean => c !== "transparent" && !/rgba?\(.*,\s*0\)$/.test(c);
  if (seen(cs.backgroundColor) || cs.backgroundImage !== "none") {
    return true;
  }
  return (["top", "bottom"] as const).some(
    (s) =>
      parseFloat(cs.getPropertyValue(`border-${s}-width`)) > 0 &&
      seen(cs.getPropertyValue(`border-${s}-color`)) &&
      cs.getPropertyValue(`border-${s}-style`) !== "none",
  );
}

/** One label of the class 15-input.css's label note describes: a single-line text label that is a
 *  flex item centred in a row, measured against the row's centre. */
export interface LabelShape {
  readonly el: Element;
  readonly sel: string;
  readonly parent: string;
  readonly fontSize: string;
  readonly lineHeight: string;
  readonly textBoxTrim: string;
  readonly rowCy: number;
  readonly boxCy: number;
  /** The label's cap-band and x-band middles less the row's centre, positive low. */
  readonly capOff: number;
  readonly xOff: number;
}

export function labelShapes(root: Element, opts: MeasureOptions = {}): LabelShape[] {
  const out: LabelShape[] = [];
  for (const el of root.querySelectorAll<HTMLElement>("*")) {
    if (opts.skip !== undefined && el.closest(opts.skip) !== null) {
      continue;
    }
    const p = el.parentElement;
    if (p === null || !el.checkVisibility()) {
      continue;
    }
    const pcs = getComputedStyle(p);
    const cs = getComputedStyle(el);
    const centred =
      /flex/.test(pcs.display) &&
      !pcs.flexDirection.startsWith("column") &&
      (cs.alignSelf === "center" || (cs.alignSelf === "auto" && pcs.alignItems === "center"));
    if (!centred || /flex|grid/.test(cs.display) || el.children.length > 0) {
      continue;
    }
    const t = firstText(el);
    if (t === null) {
      continue;
    }
    const pc = piece(t);
    if (pc.lines !== 1) {
      continue;
    }
    const rowCy = flexLineCentre(el, p, pcs);
    out.push({
      el,
      sel: selectorKey(el),
      parent: selectorKey(p),
      fontSize: cs.fontSize,
      lineHeight: cs.lineHeight,
      textBoxTrim: cs.getPropertyValue("text-box-trim") || "n/a",
      rowCy: r2(rowCy),
      boxCy: pc.boxCy,
      capOff: r2(pc.capMid - rowCy),
      xOff: r2(pc.xMid - rowCy),
    });
  }
  return out;
}

/** How far the label's [cap-middle, x-middle] band lies off its row's centre, positive low; 0 when
 *  the centre falls inside the band. */
export function labelOffset(l: LabelShape): number {
  const lo = Math.min(l.capOff, l.xOff);
  const hi = Math.max(l.capOff, l.xOff);
  return lo > 0 ? lo : hi < 0 ? hi : 0;
}

/** The centre of the flex line `el` sits on: the content box of a one-line row, else the margin
 *  boxes of the siblings sharing its line. */
function flexLineCentre(el: HTMLElement, p: HTMLElement, pcs: CSSStyleDeclaration): number {
  const pr = p.getBoundingClientRect();
  const px = (v: string): number => parseFloat(v) || 0;
  if (pcs.flexWrap === "nowrap") {
    const top = pr.top + px(pcs.borderTopWidth) + px(pcs.paddingTop);
    const bottom = pr.bottom - px(pcs.borderBottomWidth) - px(pcs.paddingBottom);
    return (top + bottom) / 2;
  }
  const margin = (e: Element): { top: number; bottom: number } => {
    const r = e.getBoundingClientRect();
    const cs = getComputedStyle(e);
    return { top: r.top - px(cs.marginTop), bottom: r.bottom + px(cs.marginBottom) };
  };
  const own = margin(el);
  let top = own.top;
  let bottom = own.bottom;
  for (const sib of p.children) {
    const m = margin(sib);
    if (sib.getBoundingClientRect().height > 0 && overlap(m, own) > 0) {
      top = Math.min(top, m.top);
      bottom = Math.max(bottom, m.bottom);
    }
  }
  return (top + bottom) / 2;
}
